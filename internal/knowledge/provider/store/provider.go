package store

import (
	"context"
	"fmt"
	"log/slog"

	"golang.org/x/sync/errgroup"

	"github.com/Timwood0x10/ares/internal/embedding"
	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/provider"
)

// StoreProvider adapts a KnowledgeStore into a GraphProvider. It is the read
// side of the AKG loop: the distillation pipeline writes candidate objects
// through the quality gate into the store, and the StoreProvider recalls the
// promoted active objects via HybridSearch and streams them to the runtime.
//
// The EmbeddingService is optional: when nil the provider degrades to
// lexical-only search (the store layer handles scoring). When set, the query
// goal is embedded best-effort and supplied as QueryVector so vector recall
// participates in HybridSearch scoring.
type StoreProvider struct {
	name  string
	store knowledge.KnowledgeStore
	emb   embedding.EmbeddingService // optional; nil = lexical-only search
	model string
	ns    string
}

// New creates a StoreProvider backed by the given KnowledgeStore.
//
// Args:
//
//	name  - provider identifier registered with the ProviderRegistry.
//	st    - KnowledgeStore holding AKG-distilled facts; must not be nil.
//	emb   - optional EmbeddingService used to embed retrieval queries; nil
//	        signals lexical-only recall.
//	model - embedding model name selecting which Representation to compare;
//	        empty is valid when emb is nil.
//	ns    - namespace filter restricting recall to one AKG namespace.
func New(name string, st knowledge.KnowledgeStore, emb embedding.EmbeddingService, model, ns string) *StoreProvider {
	return &StoreProvider{
		name:  name,
		store: st,
		emb:   emb,
		model: model,
		ns:    ns,
	}
}

// Name returns the provider identifier.
func (p *StoreProvider) Name() string { return p.name }

// ProviderType returns the backing data source type for query-planning routing.
func (p *StoreProvider) ProviderType() provider.ProviderType { return provider.ProviderStore }

// Compile-time guard that StoreProvider satisfies TypedProvider.
var _ provider.TypedProvider = (*StoreProvider)(nil)

// IntentMatch scores relevance for the given intent. The store provider is a
// generic recall source, so it returns a moderate score that keeps it
// selectable unless another provider is clearly a better fit:
//   - 0.75 when the intent asks for memory/decision/document types (the
//     canonical AKG fact types),
//   - 0.60 when the intent declares no type preference (the store is a safe
//     default recall source),
//   - 0.50 otherwise (the store still has value but is not the primary fit).
func (p *StoreProvider) IntentMatch(intent knowledge.Intent) float64 {
	if len(intent.Scope.Types) == 0 {
		return 0.6
	}
	for _, t := range intent.Scope.Types {
		switch t {
		case knowledge.ObjectMemory, knowledge.ObjectDecision, knowledge.ObjectDocument:
			return 0.75
		}
	}
	return 0.5
}

// Stream queries the backing KnowledgeStore via HybridSearch and emits the
// recalled active KnowledgeObjects one at a time. Both channels are closed
// when the stream completes or ctx is cancelled.
//
// When an EmbeddingService is configured the intent goal is embedded
// best-effort and supplied as QueryVector to enable vector recall; on embed
// error the provider falls back to lexical-only search rather than failing
// the whole stream.
func (p *StoreProvider) Stream(ctx context.Context, intent knowledge.Intent) (<-chan *knowledge.KnowledgeObject, <-chan error) {
	objCh := make(chan *knowledge.KnowledgeObject, 64)
	errCh := make(chan error, 1)

	// Use errgroup for structured concurrency so the streaming goroutine is
	// ctx-cancelable. The errgroup is not waited on here; callers observe
	// completion via objCh/errCh being closed.
	g, gCtx := errgroup.WithContext(ctx)
	g.Go(func() error {
		defer close(objCh)
		defer close(errCh)

		limit := intent.Scope.MaxObjects
		if limit <= 0 {
			limit = 20
		}

		req := knowledge.HybridSearchRequest{
			Query:        intent.Goal,
			Namespace:    p.ns,
			TopK:         limit * 2,
			FinalK:       limit,
			MinScore:     0, // provider does not filter; the retriever layer applies its minScore
			Model:        p.model,
			StatusFilter: []knowledge.ObjectStatus{knowledge.StatusActive},
		}

		// Best-effort query embedding: enables vector recall when an embedding
		// service is wired. On error we fall back to lexical-only search rather
		// than failing the whole stream; the error is logged so silent
		// degradation is observable.
		if p.emb != nil {
			if vec, err := p.emb.Embed(gCtx, intent.Goal); err == nil {
				req.QueryVector = toFloat32(vec)
			} else {
				slog.Warn("store provider: embed query, falling back to lexical-only",
					"provider", p.name, "error", err)
			}
		}

		scored, err := p.store.HybridSearch(gCtx, req)
		if err != nil {
			errCh <- fmt.Errorf("store provider %q: hybrid search: %w", p.name, err)
			return nil
		}

		for _, s := range scored {
			if s.Object == nil {
				continue
			}
			// Relevance is a transient query-time field (see object.go): it
			// is not persisted by the store. The StoreProvider is the runtime
			// path's view of HybridSearch, so it must populate Relevance from
			// FinalScore — the same signal the retriever's direct store path
			// uses — otherwise collectSnippets would see Relevance=0 on
			// every AKG-distilled object and filter them all out as noise.
			// Mutating s.Object is safe: HybridSearch returns fresh pointers
			// per call, not aliases into stored state.
			s.Object.Relevance = s.FinalScore
			select {
			case objCh <- s.Object:
			case <-gCtx.Done():
				return nil
			}
		}
		return nil
	})

	return objCh, errCh
}

// toFloat32 converts an embedding service's []float64 vector to the
// []float32 shape required by HybridSearchRequest.QueryVector. Embedding
// services return float64 for cross-language compatibility, while the vector
// stores and cosine similarity operate on float32 to halve memory.
func toFloat32(v []float64) []float32 {
	out := make([]float32, len(v))
	for i, f := range v {
		out[i] = float32(f)
	}
	return out
}

// compile-time interface check.
var _ provider.GraphProvider = (*StoreProvider)(nil)
