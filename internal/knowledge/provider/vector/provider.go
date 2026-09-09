package vector

//nolint: errcheck // best-effort operations: ResponseWriter writes, cleanup Close/Wait, deferred shutdown
import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Timwood0x10/ares/internal/embedding"
	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/provider"
	"github.com/Timwood0x10/ares/internal/scoreutil"
	"github.com/Timwood0x10/ares/internal/storage"
)

// VectorProvider implements GraphProvider by querying a VectorStore for
// semantically similar content. It converts vector search results into
// KnowledgeObjects that the AKF pipeline can process.
//
// Typical setup:
//
//	store := postgres.NewVectorSearcher(pool)         // pgvector
//	vp := vector.NewVectorProvider(store, vector.Config{
//	    Name:       "knowledge-vectors",
//	    Namespace:  "docs",
//	    Collection: "knowledge_chunks_1024",
//	    IntentTags: []string{"knowledge", "doc", "guide"},
//	})
//	registry.Register(vp)
type VectorProvider struct {
	store  storage.VectorStore
	config Config
}

// Config configures the VectorProvider.
type Config struct {
	// Name is the provider name used for identification and routing.
	Name string

	// Namespace is prepended to KnowledgeObject IDs for provenance tracking.
	Namespace string

	// Collection is the VectorStore collection/table to search.
	// Examples: "knowledge_chunks_1024", "doc_embeddings", "my_collection".
	Collection string

	// IntentTags are keywords used by IntentMatch to score relevance.
	// Tags matching the query goal increase the provider's selection score.
	IntentTags []string

	// VectorDimension sets the dimension used when auto-creating the collection.
	// Common: 384 (bge-small), 768 (bge-base), 1024 (bge-large), 1536 (openai).
	VectorDimension int

	// DefaultScore is returned when a search result has no explicit score.
	// Most vector stores return a similarity score; this is only used when
	// the search result's score is 0.
	DefaultScore float64

	// Embedder optionally provides real query embedding. When set, Stream uses
	// it to embed the intent goal so vector search compares semantically
	// meaningful vectors instead of a deterministic hash fallback. When nil,
	// the provider degrades to a deterministic hash vector so it keeps working
	// without an external embedding service.
	Embedder embedding.EmbeddingService
}

// DefaultConfig returns a sensible default configuration.
func DefaultConfig() Config {
	return Config{
		VectorDimension: 1024,
		DefaultScore:    0.5,
	}
}

// NewVectorProvider creates a VectorProvider backed by the given VectorStore.
func NewVectorProvider(store storage.VectorStore, cfg Config) (*VectorProvider, error) {
	if store == nil {
		return nil, fmt.Errorf("vector provider %s: store is nil", cfg.Name)
	}
	if cfg.Name == "" {
		return nil, errors.New("vector provider: name is required")
	}
	if cfg.Collection == "" {
		return nil, fmt.Errorf("vector provider %s: collection is required", cfg.Name)
	}
	if cfg.VectorDimension <= 0 {
		cfg.VectorDimension = 1024
	}
	if cfg.DefaultScore <= 0 {
		cfg.DefaultScore = 0.5
	}

	// Attempt to create the collection; most backends return an error if the
	// collection already exists, which is safe to ignore.
	_ = store.CreateCollection(context.Background(), cfg.Collection, cfg.VectorDimension)

	return &VectorProvider{
		store:  store,
		config: cfg,
	}, nil
}

// Name returns the provider identifier.
func (p *VectorProvider) Name() string { return p.config.Name }

// ProviderType returns the backing data source type for query-planning routing.
func (p *VectorProvider) ProviderType() provider.ProviderType { return provider.ProviderVector }

// Compile-time guard that VectorProvider satisfies TypedProvider.
var _ provider.TypedProvider = (*VectorProvider)(nil)

// IntentMatch scores relevance based on configured intent tags.
// Returns 0.0–1.0 where higher = more relevant to the query.
func (p *VectorProvider) IntentMatch(intent knowledge.Intent) float64 {
	if len(p.config.IntentTags) == 0 || intent.Goal == "" {
		return 0.4 // generic relevance when no tags configured
	}
	goal := strings.ToLower(intent.Goal)
	matches := 0
	for _, tag := range p.config.IntentTags {
		if strings.Contains(goal, strings.ToLower(tag)) {
			matches++
		}
	}
	if matches == 0 {
		return 0.2 // weak match — provider exists but not targeted
	}
	// Score scales with what fraction of tags matched.
	return 0.3 + (float64(matches)/float64(len(p.config.IntentTags)))*0.7
}

// Stream queries the VectorStore for semantically similar content and streams
// the results as KnowledgeObjects. The intent goal is used as the query text.
func (p *VectorProvider) Stream(ctx context.Context, intent knowledge.Intent) (<-chan *knowledge.KnowledgeObject, <-chan error) {
	objCh := make(chan *knowledge.KnowledgeObject, 32)
	errCh := make(chan error, 1)

	// Use errgroup for structured concurrency so the streaming goroutine is
	// ctx-cancelable. The errgroup is not waited on here; callers observe
	// completion via objCh/errCh being closed.
	g, gCtx := errgroup.WithContext(ctx)
	g.Go(func() error {
		defer close(objCh)
		defer close(errCh)

		// Generate a query embedding from the intent goal. When an
		// EmbeddingService is configured, the goal is embedded with the real
		// model so vector search is semantically meaningful. Otherwise fall
		// back to a deterministic hash vector (no external service needed).
		queryVec, err := p.generateQueryVector(gCtx, intent)
		if err != nil {
			errCh <- fmt.Errorf("vector search %s: %w", p.config.Collection, err)
			return nil
		}

		limit := intent.Scope.MaxObjects
		if limit <= 0 {
			limit = 50
		}
		if limit > 200 {
			limit = 200 // safety cap
		}

		results, err := p.store.Search(gCtx, p.config.Collection, queryVec, limit)
		if err != nil {
			// If the collection doesn't exist yet, return empty (not an error).
			errCh <- fmt.Errorf("vector search %s: %w", p.config.Collection, err)
			return nil
		}

		for _, r := range results {
			if r == nil {
				continue
			}

			obj := p.resultToObject(r)

			select {
			case objCh <- obj:
			case <-gCtx.Done():
				return nil
			}
		}
		return nil
	})

	return objCh, errCh
}

// vectorReliability is the Confidence assigned to every vector-recalled
// object. It is a reliability prior, NOT a query-relevance score: vector
// embeddings are assumed to index reliable stored facts (documents, code,
// decisions), so their reliability as facts is high. The true query-relevance
// signal is the vector similarity score, which is carried on
// KnowledgeObject.Relevance (NOT Confidence) so the retriever ranks and
// filters on the right signal.
const vectorReliability = 1.0

// resultToObject converts a VectorStore SearchResult into a KnowledgeObject.
func (p *VectorProvider) resultToObject(r *storage.SearchResult) *knowledge.KnowledgeObject {
	obj := &knowledge.KnowledgeObject{
		ID:         fmt.Sprintf("%s:%s", p.config.Namespace, r.ID),
		Type:       knowledge.ObjectDocument,
		Namespace:  p.config.Namespace,
		Confidence: vectorReliability,
		Relevance:  scoreutil.ClampUnit(r.Score),
		CreatedAt:  time.Now(),
		Representations: map[string]string{
			"vector": r.ID, // link to the vector embedding
		},
	}

	// Extract summary and tags from metadata.
	if r.Metadata != nil {
		if summary, ok := r.Metadata["summary"].(string); ok {
			obj.Summary = summary
		}
		if content, ok := r.Metadata["content"].(string); ok {
			obj.Raw = []byte(content)
		}
		if objType, ok := r.Metadata["type"].(string); ok {
			obj.Type = knowledge.ObjectType(objType)
		}
		if tags, ok := r.Metadata["tags"].([]string); ok {
			obj.Tags = tags
		} else if tagsAny, ok := r.Metadata["tags"].([]any); ok {
			for _, t := range tagsAny {
				if s, ok := t.(string); ok {
					obj.Tags = append(obj.Tags, s)
				}
			}
		}
		obj.Metadata = r.Metadata
	}

	// Fallback: use ID as summary if nothing else is available.
	if obj.Summary == "" {
		obj.Summary = fmt.Sprintf("Vector result: %s (score: %.3f)", r.ID, r.Score)
	}

	return obj
}

// generateQueryVector produces a query vector from the intent goal.
//
// When an EmbeddingService is configured, the goal is embedded with the real
// model (prefixed "query:" to match how documents are stored under the
// "passage:" prefix) and normalized to unit length for cosine similarity. An
// embedding failure returns a wrapped error — the provider does NOT silently
// fall back to the hash vector, so a misconfigured embedder is observable
// instead of silently degrading search quality.
//
// When no embedder is configured, a deterministic hash vector is used so the
// provider still works without an external embedding service (e.g. in minimal
// configs). Same goal → same vector, preserving reproducibility.
func (p *VectorProvider) generateQueryVector(ctx context.Context, intent knowledge.Intent) ([]float64, error) {
	if p.config.Embedder != nil {
		vec, err := p.config.Embedder.EmbedWithPrefix(ctx, intent.Goal, "query:")
		if err != nil {
			return nil, fmt.Errorf("vector provider %s: embed query: %w", p.config.Name, err)
		}
		if len(vec) == 0 {
			return nil, fmt.Errorf("vector provider %s: embedder returned empty vector", p.config.Name)
		}
		return normalizeUnit(vec), nil
	}

	return p.hashQueryVector(intent), nil
}

// hashQueryVector derives a deterministic unit vector from the goal text.
// It is the fallback path used only when no EmbeddingService is configured.
func (p *VectorProvider) hashQueryVector(intent knowledge.Intent) []float64 {
	dim := p.config.VectorDimension
	if dim <= 0 {
		dim = 1024
	}

	// Seed from the goal text for determinism.
	seed := int64(0)
	for _, c := range intent.Goal {
		seed += int64(c)
	}
	if seed == 0 {
		seed = time.Now().UnixNano()
	}

	//nolint:gosec // deterministic seed from goal text, not security-sensitive
	rng := rand.New(rand.NewSource(seed))
	vec := make([]float64, dim)
	for i := range vec {
		vec[i] = rng.Float64()*2 - 1 // [-1, 1]
	}
	return normalizeUnit(vec)
}

// normalizeUnit scales vec to unit length so cosine similarity compares
// direction only. A zero vector is returned unchanged (magnitude 0).
func normalizeUnit(vec []float64) []float64 {
	var sum float64
	for _, v := range vec {
		sum += v * v
	}
	if sum == 0 {
		return vec
	}
	mag := 1.0 / math.Sqrt(sum)
	out := make([]float64, len(vec))
	for i, v := range vec {
		out[i] = v * mag
	}
	return out
}

// compile-time interface check.
var _ provider.GraphProvider = (*VectorProvider)(nil)
