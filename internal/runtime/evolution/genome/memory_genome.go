//nolint:gosec // GA mutation intentionally uses math/rand (performance, not crypto).
package genome

//nolint: errcheck // best-effort operations: ResponseWriter writes, cleanup Close/Wait, deferred shutdown
import (
	"context"
	"fmt"
	"math/rand"

	"github.com/Timwood0x10/ares/internal/evidence"
)

const (
	// MemoryGenomeName is the registry name for the memory genome.
	MemoryGenomeName = "memory"
)

// MemoryGenomeConfig controls the memory management parameters.
type MemoryGenomeConfig struct {
	// MaxHistory is the maximum number of turns to keep in context.
	MaxHistory int

	// MaxSessions is the maximum number of sessions to store.
	MaxSessions int

	// MaxDistilledTasks is the maximum number of distilled tasks to store.
	MaxDistilledTasks int

	// UseStructuredCleaning enables the structured prompt builder.
	UseStructuredCleaning bool

	// EvidenceStore provides memory quality evidence for fitness evaluation.
	// May be nil; fitness falls back to a constant when nil.
	EvidenceStore evidence.Store
}

// DefaultMemoryGenomeConfig returns sensible default memory parameters.
func DefaultMemoryGenomeConfig() MemoryGenomeConfig {
	return MemoryGenomeConfig{
		MaxHistory:            10,
		MaxSessions:           100,
		MaxDistilledTasks:     5000,
		UseStructuredCleaning: false,
	}
}

// MemoryGenome evolves memory management parameters.
//
// Mutation changes:
//   - MaxHistory: 3–50
//   - MaxSessions: 20–500
//   - MaxDistilledTasks: 500–20000
//   - UseStructuredCleaning: toggle
type MemoryGenome struct {
	config MemoryGenomeConfig
}

// NewMemoryGenome creates a new MemoryGenome.
func NewMemoryGenome(config MemoryGenomeConfig) *MemoryGenome {
	return &MemoryGenome{config: config}
}

// Name returns the genome identifier.
func (g *MemoryGenome) Name() string { return MemoryGenomeName }

// Config returns the current config. Used by the Diff Engine.
func (g *MemoryGenome) Config() MemoryGenomeConfig { return g.config }

// Mutate generates n candidate genomes with one random parameter change each.
func (g *MemoryGenome) Mutate(_ context.Context, n int) ([]Genome, error) {
	if n <= 0 {
		return nil, nil
	}
	children := make([]Genome, 0, n)
	for i := 0; i < n; i++ {
		child := g.clone()
		switch rand.Intn(4) {
		case 0:
			child.mutateMaxHistory()
		case 1:
			child.mutateMaxSessions()
		case 2:
			child.mutateMaxDistilledTasks()
		case 3:
			child.mutateStructuredCleaning()
		}
		children = append(children, child)
	}
	return children, nil
}

// Crossover recombines this genome with another to produce a child.
func (g *MemoryGenome) Crossover(_ context.Context, other Genome) (Genome, error) {
	otherMG, ok := other.(*MemoryGenome)
	if !ok {
		return nil, fmt.Errorf("memory: crossover incompatible genome type %T", other)
	}
	child := g.clone()
	if rand.Float64() < 0.5 {
		child.config.MaxHistory = otherMG.config.MaxHistory
	}
	if rand.Float64() < 0.5 {
		child.config.MaxSessions = otherMG.config.MaxSessions
	}
	if rand.Float64() < 0.5 {
		child.config.MaxDistilledTasks = otherMG.config.MaxDistilledTasks
	}
	if rand.Float64() < 0.5 {
		child.config.UseStructuredCleaning = otherMG.config.UseStructuredCleaning
	}
	return child, nil
}

// Fitness evaluates this genome's quality based on real memory quality
// evidence: the measured retrieval hit rate (Value in [0, 1]) produced by the
// memory retriever and distillation paths. When no evidence is available yet,
// a neutral 0.5 is returned so the GA keeps exploring.
//
// MaxHistory/MaxSessions are deliberately not baked into the score: fitness
// reflects actual memory usefulness (hit rate) so the GA converges to the
// configuration that demonstrably retrieves the most relevant memories.
func (g *MemoryGenome) Fitness(ctx context.Context) (float64, error) {
	score, err := avgFitnessValue(ctx, g.config.EvidenceStore, MemoryGenomeName, 0, 50)
	if err != nil {
		return 0.5, nil
	}
	return score, nil
}

// Snapshot returns the current config as the serializable state.
func (g *MemoryGenome) Snapshot(_ context.Context) (any, error) {
	return g.config, nil
}

// ── Mutation implementations ─────────────────

func (g *MemoryGenome) mutateMaxHistory() {
	delta := rand.Intn(11) - 5 // [-5, 5]
	g.config.MaxHistory += delta
	if g.config.MaxHistory < 3 {
		g.config.MaxHistory = 3
	}
	if g.config.MaxHistory > 50 {
		g.config.MaxHistory = 50
	}
}

func (g *MemoryGenome) mutateMaxSessions() {
	delta := rand.Intn(101) - 50 // [-50, 50]
	g.config.MaxSessions += delta
	if g.config.MaxSessions < 20 {
		g.config.MaxSessions = 20
	}
	if g.config.MaxSessions > 500 {
		g.config.MaxSessions = 500
	}
}

func (g *MemoryGenome) mutateMaxDistilledTasks() {
	delta := rand.Intn(2001) - 1000 // [-1000, 1000]
	g.config.MaxDistilledTasks += delta
	if g.config.MaxDistilledTasks < 500 {
		g.config.MaxDistilledTasks = 500
	}
	if g.config.MaxDistilledTasks > 20000 {
		g.config.MaxDistilledTasks = 20000
	}
}

func (g *MemoryGenome) mutateStructuredCleaning() {
	g.config.UseStructuredCleaning = !g.config.UseStructuredCleaning
}

func (g *MemoryGenome) clone() *MemoryGenome {
	cfg := g.config
	return &MemoryGenome{config: cfg}
}
