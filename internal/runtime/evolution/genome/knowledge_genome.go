//nolint:gosec // GA mutation intentionally uses math/rand (performance, not crypto).
package genome

//nolint: errcheck // best-effort operations: ResponseWriter writes, cleanup Close/Wait, deferred shutdown
import (
	"context"
	"fmt"
	"math/rand"

	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/knowledge"
)

const (
	defaultReducer     = "default"
	plannerBalanced    = "balanced"
	plannerArchFirst   = "architecture-first"
	plannerMemoryFirst = "memory-first"
)

// KnowledgeGenomeConfig controls the AKF knowledge retrieval parameters.
type KnowledgeGenomeConfig struct {
	// MaxResults caps the number of results per knowledge query.
	MaxResults int

	// ReducerStrategy selects how query results are reduced: default / strict / relaxed.
	ReducerStrategy string

	// PlannerStrategy selects the knowledge planning strategy: architecture-first / memory-first / balanced.
	PlannerStrategy string

	// SummarizerType selects the summarizer: truncation / llm.
	SummarizerType string

	// EvidenceStore provides retrieval quality evidence for fitness evaluation.
	// May be nil; fitness falls back to a constant when nil.
	EvidenceStore evidence.Store
}

// DefaultKnowledgeGenomeConfig returns sensible default knowledge parameters.
func DefaultKnowledgeGenomeConfig() KnowledgeGenomeConfig {
	return KnowledgeGenomeConfig{
		MaxResults:      100,
		ReducerStrategy: defaultReducer,
		PlannerStrategy: plannerBalanced,
		SummarizerType:  "truncation",
	}
}

// KnowledgeGenome evolves AKF knowledge retrieval parameters.
//
// Mutation changes:
//   - MaxResults: 5–200
//   - Reducer strategy: default / strict / relaxed
//   - Planner strategy: architecture-first / memory-first / balanced
//   - Summarizer: truncation / llm
type KnowledgeGenome struct {
	pipeline *knowledge.KnowledgePipeline
	config   KnowledgeGenomeConfig
}

// NewKnowledgeGenome creates a new KnowledgeGenome wrapping the given pipeline.
func NewKnowledgeGenome(pipeline *knowledge.KnowledgePipeline, config KnowledgeGenomeConfig) *KnowledgeGenome {
	return &KnowledgeGenome{
		pipeline: pipeline,
		config:   config,
	}
}

// Name returns the genome identifier.
func (g *KnowledgeGenome) Name() string { return KnowledgeGenomeName }

// Config returns the current config. Used by the Diff Engine.
func (g *KnowledgeGenome) Config() KnowledgeGenomeConfig { return g.config }

// Mutate generates n candidate genomes with one random parameter change each.
func (g *KnowledgeGenome) Mutate(_ context.Context, n int) ([]Genome, error) {
	if n <= 0 {
		return nil, nil
	}

	children := make([]Genome, 0, n)
	for i := 0; i < n; i++ {
		child := g.clone()
		param := rand.Intn(4)
		switch param {
		case 0:
			child.mutateMaxResults()
		case 1:
			child.mutateReducerStrategy()
		case 2:
			child.mutatePlannerStrategy()
		case 3:
			child.mutateSummarizerType()
		}
		children = append(children, child)
	}
	return children, nil
}

// Crossover recombines this genome with another to produce a child.
func (g *KnowledgeGenome) Crossover(_ context.Context, other Genome) (Genome, error) {
	otherKG, ok := other.(*KnowledgeGenome)
	if !ok {
		return nil, fmt.Errorf("knowledge: crossover incompatible genome type %T", other)
	}

	child := g.clone()
	if rand.Float64() < 0.5 {
		child.config.MaxResults = otherKG.config.MaxResults
	}
	if rand.Float64() < 0.5 {
		child.config.ReducerStrategy = otherKG.config.ReducerStrategy
	}
	if rand.Float64() < 0.5 {
		child.config.PlannerStrategy = otherKG.config.PlannerStrategy
	}
	if rand.Float64() < 0.5 {
		child.config.SummarizerType = otherKG.config.SummarizerType
	}
	return child, nil
}

// Fitness evaluates this genome's quality based on real knowledge retrieval
// quality evidence: the measured relevance ratio (Value in [0, 1]) produced by
// the AKF runtime. When no evidence is available yet, a neutral 0.5 is returned
// so the GA keeps exploring.
//
// MaxResults/ReducerStrategy/PlannerStrategy are deliberately not baked into
// the score: fitness reflects actual retrieval quality so the GA converges to
// whichever configuration demonstrably retrieves the most relevant knowledge.
func (g *KnowledgeGenome) Fitness(ctx context.Context) (float64, error) {
	score, err := avgFitnessValue(ctx, g.config.EvidenceStore, KnowledgeGenomeName, 0, 50)
	if err != nil {
		return 0.5, nil
	}
	return score, nil
}

// Snapshot returns the current config as the serializable state.
func (g *KnowledgeGenome) Snapshot(_ context.Context) (any, error) {
	return g.config, nil
}

// ── Mutation implementations ─────────────────

func (g *KnowledgeGenome) mutateMaxResults() {
	delta := rand.Intn(41) - 20 // [-20, 20]
	g.config.MaxResults += delta
	if g.config.MaxResults < 5 {
		g.config.MaxResults = 5
	}
	if g.config.MaxResults > 200 {
		g.config.MaxResults = 200
	}
}

func (g *KnowledgeGenome) mutateReducerStrategy() {
	strategies := []string{defaultReducer, "strict", "relaxed"}
	g.config.ReducerStrategy = strategies[rand.Intn(len(strategies))]
}

func (g *KnowledgeGenome) mutatePlannerStrategy() {
	strategies := []string{plannerArchFirst, plannerMemoryFirst, plannerBalanced}
	g.config.PlannerStrategy = strategies[rand.Intn(len(strategies))]
}

func (g *KnowledgeGenome) mutateSummarizerType() {
	types := []string{"truncation", "llm"}
	g.config.SummarizerType = types[rand.Intn(len(types))]
}

// clone creates a deep copy of the KnowledgeGenome.
func (g *KnowledgeGenome) clone() *KnowledgeGenome {
	cfg := g.config
	return &KnowledgeGenome{
		pipeline: g.pipeline,
		config:   cfg,
	}
}
