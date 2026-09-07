package memory

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/provider"
	"github.com/Timwood0x10/ares/internal/scoreutil"
)

// TaskSearcher is the minimal interface needed to query historical tasks.
type TaskSearcher interface {
	SearchSimilarTasks(ctx context.Context, query string, limit int) ([]SearchResult, error)
}

// SearchResult is a single task returned by TaskSearcher.
//
// Score carries the backing search engine's similarity score in [0, 1] when
// available (e.g. cosine similarity from a vector index). When the backing
// engine does not expose a score, Score is left 0 and the MemoryProvider
// derives a rank-based Relevance from result ordering instead.
type SearchResult struct {
	ID        string
	Summary   string
	Timestamp time.Time
	Score     float64
}

// defaultReliability is the Confidence assigned to every memory object. It is
// a reliability prior, NOT a query-relevance score: memories distilled from
// past tasks are assumed moderately reliable as facts. Query relevance is
// captured separately on KnowledgeObject.Relevance at stream time and is the
// signal the retriever ranks/filters on.
const defaultReliability = 0.7

// MemoryProvider wraps a TaskSearcher as a GraphProvider.
// It maps the user's intent.Goal to a similarity search over past tasks.
type MemoryProvider struct {
	name     string
	searcher TaskSearcher
}

// New creates a MemoryProvider.
func New(name string, searcher TaskSearcher) *MemoryProvider {
	return &MemoryProvider{name: name, searcher: searcher}
}

// Name returns the provider identifier.
func (p *MemoryProvider) Name() string { return p.name }

// ProviderType returns the backing data source type for query-planning routing.
func (p *MemoryProvider) ProviderType() provider.ProviderType { return provider.ProviderMemory }

// Compile-time guard that MemoryProvider satisfies TypedProvider.
var _ provider.TypedProvider = (*MemoryProvider)(nil)

// IntentMatch scores relevance based on intent type overlap.
// Returns a moderated score [0.1, 0.8] — high for memory/decision types, low for
// code/architecture types where memory is less useful.
func (p *MemoryProvider) IntentMatch(intent knowledge.Intent) float64 {
	if len(intent.Scope.Types) == 0 {
		return 0.5
	}
	for _, t := range intent.Scope.Types {
		switch t {
		case knowledge.ObjectMemory, knowledge.ObjectDecision:
			return 0.8
		case knowledge.ObjectIssue, knowledge.ObjectCommit:
			return 0.6
		case knowledge.ObjectCode, knowledge.ObjectArchitecture:
			return 0.3
		}
	}
	return 0.4
}

// Stream searches similar tasks and emits them as KnowledgeObjects.
func (p *MemoryProvider) Stream(ctx context.Context, intent knowledge.Intent) (<-chan *knowledge.KnowledgeObject, <-chan error) {
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

		// A nil searcher means the provider was wired without a backing search
		// engine (e.g. memory distillation enabled before the vector index is
		// ready). Degrade to an empty stream instead of nil-pointer panicking the
		// process (a single component must not kill the
		// kernel). Log-free by design: callers treat an empty stream as "no
		// memories".
		if p.searcher == nil {
			return nil
		}

		results, err := p.searcher.SearchSimilarTasks(gCtx, intent.Goal, limit)
		if err != nil {
			errCh <- fmt.Errorf("memory provider %q: %w", p.name, err)
			return nil
		}

		for i, r := range results {
			summary := r.Summary
			if len(summary) > 200 {
				summary = summary[:200] + "..."
			}

			obj := &knowledge.KnowledgeObject{
				ID:         fmt.Sprintf("%s_%s", p.name, r.ID),
				Type:       knowledge.ObjectMemory,
				Namespace:  p.name,
				Summary:    summary,
				Confidence: defaultReliability,
				Relevance:  relevanceFromScore(r.Score, i, len(results)),
				CreatedAt:  r.Timestamp,
				UpdatedAt:  time.Now(),
			}

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

// relevanceFromScore derives the query-time Relevance for a memory object.
//
// When the backing search engine returns a real similarity score (>0), it is
// used directly: it is the truest query-relevance signal available. When no
// score is available (score <= 0), Relevance is derived from the result rank
// position i within a result set of size n: the first result gets 1.0 and
// relevance decays linearly to a floor of 0.1 so even the last result keeps
// a non-zero signal. This keeps memory objects rankable without lying about
// having a real similarity score.
//
// Args:
//
//	score - backing engine similarity score, 0 means "not available".
//	i     - zero-based rank index of this result within the result slice.
//	n     - total number of results returned by the backing engine.
func relevanceFromScore(score float64, i, n int) float64 {
	if score > 0 {
		return scoreutil.ClampUnit(score)
	}
	if n <= 0 {
		return 0.1
	}
	rel := 1.0 - float64(i)/float64(n)
	if rel < 0.1 {
		rel = 0.1
	}
	return rel
}
