package context

import (
	"context"
	"errors"
	"fmt"
	"strings"

	apiembed "github.com/Timwood0x10/ares/api/embedding"
	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/runtime/memory/distillation"
	memembed "github.com/Timwood0x10/ares/internal/runtime/memory/embedding"
	"github.com/Timwood0x10/ares/internal/scoreutil"
)

// EvidenceEmitter is the minimal interface the retriever uses to report
// retrieval outcomes to the unified Evidence Store. It is kept minimal to
// avoid a direct import of internal/evidence from this package (mirrors the
// pattern used by ProductionMemoryManager). The GA MemoryGenome consumes the
// mean retrieval-hit value (1.0 hit / 0.0 miss) under Source "memory".
type EvidenceEmitter interface {
	Emit(ctx context.Context, kind evidence.EvidenceKind, payload any, opts ...evidence.EvidenceOption) error
}

// ExperienceSearcher is the minimal retrieval contract needed by
// MemoryRetriever. It is a strict subset of distillation.ExperienceRepository
// (which also defines Create/Update/Delete/GetByMemoryType/CountByMemoryType),
// so any full ExperienceRepository implementation also satisfies it.
//
// Defining this narrow interface here follows Interface Segregation: the
// retriever only reads, so it should not force adapters to implement the
// write-side methods they will never call.
type ExperienceSearcher interface {
	// SearchByVector returns experiences whose embeddings are closest to the
	// given query vector, scoped to tenantID, capped at limit.
	SearchByVector(ctx context.Context, vector []float64, tenantID string, limit int) ([]distillation.Experience, error)
}

// Default values for the MemoryRetriever. The TopK and MinScore defaults are
// exported so that callers across packages (memory managers, bootstrap wiring)
// reference the same canonical values instead of re-declaring magic numbers
// that could drift over time.
const (
	// DefaultMinScore is the minimum Score a snippet must reach to be returned
	// when the caller passes minScore <= 0. Experiences below this confidence
	// are unlikely to be worth surfacing in the prompt, so we drop them rather
	// than waste context tokens.
	DefaultMinScore = 0.4
	// DefaultTopK is the maximum number of snippets returned when the caller
	// passes topK <= 0.
	DefaultTopK = 5
)

// Internal defaults kept unexported because they are retriever-implementation
// details with no cross-package callers.
const (
	// memoryDefaultTenant is used when the caller does not supply a tenantID.
	memoryDefaultTenant = "default"
	// memorySourceExperience is the Source tag for snippets derived from
	// distilled experiences. Kept as a constant so goconst stays quiet and
	// the value is grep-able across the context builder.
	memorySourceExperience = "experience"
)

// MemoryRetriever retrieves past distilled experiences by vector similarity
// and surfaces them as ContextSnippets for prompt augmentation.
//
// It is the in-tree implementation of ContextRetriever. The retriever is
// stateless after construction: all concurrency safety comes from the
// underlying ExperienceRepository and EmbeddingService / EmbeddingPipeline,
// which are each responsible for their own thread-safety.
//
// Embedding path: when a pipeline is configured, the input is embedded via
// the canonical pipeline (BuildSpec + Embed) so the query vector matches
// the prefix scheme used at write time. When only an embedder is configured,
// Embedder.Embed is called directly as a fallback for callers that have not
// yet migrated to the pipeline.
type MemoryRetriever struct {
	embedder apiembed.EmbeddingService
	pipeline memembed.EmbeddingPipeline
	expRepo  ExperienceSearcher
	tenantID string
	minScore float64

	// evidenceEmitter reports retrieval hit/miss outcomes to the unified
	// Evidence Store. Optional: nil disables emission entirely, so the
	// retriever keeps working in minimal configurations that lack wiring.
	evidenceEmitter EvidenceEmitter
}

// NewMemoryRetriever constructs a MemoryRetriever.
//
// At least one of embedder or pipeline MUST be non-nil; the pipeline is
// preferred when both are supplied. expRepo MUST be non-nil. An empty
// tenantID is normalized to "default". A non-positive minScore is
// normalized to 0.4 so callers cannot accidentally disable filtering by
// passing zero.
//
// Args:
//
//	embedder - fallback embedding service used when pipeline is nil.
//	           May be nil if pipeline is non-nil.
//	pipeline - canonical embedding pipeline used when non-nil.
//	           May be nil if embedder is non-nil.
//	expRepo  - experience searcher used for vector search. Required.
//	           Any distillation.ExperienceRepository satisfies this.
//	tenantID - tenant identifier for multi-tenant isolation.
//	minScore - minimum Score for a snippet to be returned. <= 0 ⇒ 0.4.
//
// Returns:
//
//	*MemoryRetriever - configured retriever, ready to call Retrieve on.
//	error            - wrapped error if any required dependency is missing.
func NewMemoryRetriever(
	embedder apiembed.EmbeddingService,
	pipeline memembed.EmbeddingPipeline,
	expRepo ExperienceSearcher,
	tenantID string,
	minScore float64,
) (*MemoryRetriever, error) {
	if embedder == nil && pipeline == nil {
		return nil, errors.New("memory retriever: embedder and pipeline are both nil")
	}
	if expRepo == nil {
		return nil, errors.New("memory retriever: experience repository is nil")
	}

	if tenantID == "" {
		tenantID = memoryDefaultTenant
	}
	if minScore <= 0 {
		minScore = DefaultMinScore
	}

	return &MemoryRetriever{
		embedder: embedder,
		pipeline: pipeline,
		expRepo:  expRepo,
		tenantID: tenantID,
		minScore: minScore,
	}, nil
}

// SetEvidenceEmitter configures an optional evidence emitter. Retrieval
// outcomes (hit/miss) are reported under Source "memory" so the GA
// MemoryGenome can score memory quality from real usage. Nil disables
// emission. It is safe to call before or after Retrieve; the emitter is
// only read under the retriever's own serialized execution.
func (r *MemoryRetriever) SetEvidenceEmitter(em EvidenceEmitter) {
	r.evidenceEmitter = em
}

// Retrieve queries the experience repository for experiences that are
// semantically similar to the input, and converts them to ContextSnippets
// sorted by Score descending.
//
// Behavior:
//   - Empty input returns an empty slice + nil error (no embedding call).
//   - topK <= 0 is normalized to 5.
//   - Embedding failure returns a wrapped error; the retriever does not
//     silently fall back to keyword search (code_rules §9).
//   - Snippets with Score < minScore are filtered out.
//   - At most topK snippets are returned, sorted by Score descending.
//
// Args:
//
//	ctx   - operation context, honoured for cancellation and timeout.
//	input - query text to embed and search with.
//	topK  - maximum number of snippets to return.
//
// Returns:
//
//	[]ContextSnippet - matching experiences as snippets, or nil/empty on
//	                   no match. Sorted by Score descending.
//	error            - wrapped error on embedding or repository failure.
func (r *MemoryRetriever) Retrieve(
	ctx context.Context,
	input string,
	topK int,
) ([]ContextSnippet, error) {
	if input == "" {
		return []ContextSnippet{}, nil
	}
	if topK <= 0 {
		topK = DefaultTopK
	}

	vec, err := r.embed(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("memory retriever: embed input: %w", err)
	}
	if len(vec) == 0 {
		return nil, errors.New("memory retriever: embed returned empty vector")
	}

	experiences, err := r.expRepo.SearchByVector(ctx, vec, r.tenantID, topK)
	if err != nil {
		return nil, fmt.Errorf("memory retriever: search experiences: %w", err)
	}

	snippets := r.toSnippets(experiences)
	snippets = filterByMinScore(snippets, r.minScore)
	SortSnippetsByScore(snippets)

	if len(snippets) > topK {
		snippets = snippets[:topK]
	}

	// Report the retrieval outcome to the unified Evidence Store. The GA
	// MemoryGenome aggregates the mean hit value (1.0 hit / 0.0 miss) under
	// Source "memory". Only real searches emit — the empty-input short-circuit
	// above returns before this point.
	if r.evidenceEmitter != nil {
		hitValue := 0.0
		if len(snippets) > 0 {
			hitValue = 1.0
		}
		_ = r.evidenceEmitter.Emit(ctx, evidence.KindFitness,
			map[string]any{"value": hitValue},
			evidence.WithMetadata("source", "memory"),
			evidence.WithMetadata("type", "retrieval"),
		)
	}

	return snippets, nil
}

// embed produces the query embedding via the pipeline when configured, else
// via the fallback embedder. The caller is responsible for the empty-input
// short-circuit; this method assumes input is non-empty.
func (r *MemoryRetriever) embed(ctx context.Context, input string) ([]float64, error) {
	if r.pipeline != nil {
		spec, err := r.pipeline.BuildSpec(memembed.KindMemoryQuery, input)
		if err != nil {
			return nil, fmt.Errorf("build query spec: %w", err)
		}
		vec, err := r.pipeline.Embed(ctx, spec)
		if err != nil {
			return nil, fmt.Errorf("pipeline embed: %w", err)
		}
		return vec, nil
	}

	vec, err := r.embedder.Embed(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("embedder embed: %w", err)
	}
	return vec, nil
}

// toSnippets converts the repository's Experience slice into ContextSnippets,
// skipping entries that would produce an empty Content (no problem and no
// solution). Confidence is clamped to [0, 1] so downstream sorting and
// filtering operate on a well-defined range.
func (r *MemoryRetriever) toSnippets(
	experiences []distillation.Experience,
) []ContextSnippet {
	snippets := make([]ContextSnippet, 0, len(experiences))
	for i := range experiences {
		exp := experiences[i]
		content := formatExperienceContent(exp.Problem, exp.Solution)
		if content == "" {
			continue
		}
		snippets = append(snippets, ContextSnippet{
			Source:  memorySourceExperience,
			Content: content,
			Score:   scoreutil.ClampUnit(exp.Confidence),
			Metadata: map[string]any{
				"id":                exp.ID,
				"extraction_method": string(exp.ExtractionMethod),
				"problem":           exp.Problem,
				"solution":          exp.Solution,
			},
		})
	}
	return snippets
}

// filterByMinScore drops snippets whose Score is strictly below the
// threshold. Snippets at exactly the threshold are kept.
func filterByMinScore(snippets []ContextSnippet, minScore float64) []ContextSnippet {
	out := make([]ContextSnippet, 0, len(snippets))
	for _, s := range snippets {
		if s.Score >= minScore {
			out = append(out, s)
		}
	}
	return out
}

// formatExperienceContent renders the problem/solution pair as a stable,
// prompt-friendly string. Empty parts are omitted so we never emit a
// dangling "Problem:" label with no body.
func formatExperienceContent(problem, solution string) string {
	var b strings.Builder
	if problem != "" {
		b.WriteString("Problem: ")
		b.WriteString(problem)
	}
	if solution != "" {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("Solution: ")
		b.WriteString(solution)
	}
	return b.String()
}
