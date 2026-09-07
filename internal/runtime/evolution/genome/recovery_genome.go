//nolint:gosec // GA mutation intentionally uses math/rand (performance, not crypto).
package genome

//nolint: errcheck // best-effort operations: ResponseWriter writes, cleanup Close/Wait, deferred shutdown
import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/workflow/engine"
)

// RecoveryGenomeConfig controls the recovery strategy evolution behaviour.
type RecoveryGenomeConfig struct {
	// MaxAttemptsRange sets the min/max retry attempts bounds.
	MaxAttemptsMin int
	MaxAttemptsMax int

	// BackoffRange sets the min/max initial backoff bounds.
	BackoffMin time.Duration
	BackoffMax time.Duration

	// EvidenceStore provides recovery performance evidence for fitness evaluation.
	// May be nil; fitness falls back to a constant when nil.
	EvidenceStore evidence.Store
}

// DefaultRecoveryGenomeConfig returns a sensible default configuration.
func DefaultRecoveryGenomeConfig() RecoveryGenomeConfig {
	return RecoveryGenomeConfig{
		MaxAttemptsMin: 1,
		MaxAttemptsMax: 5,
		BackoffMin:     100 * time.Millisecond,
		BackoffMax:     10 * time.Second,
	}
}

// RecoveryGenome evolves the recovery strategy for step failures.
//
// Mutation changes:
//   - Strategy: retry / replace_node / fail_fast
//   - MaxAttempts: 1–5
//   - Backoff: 100ms–10s
type RecoveryGenome struct {
	policy *engine.RecoveryPolicy
	config RecoveryGenomeConfig
}

// NewRecoveryGenome creates a new RecoveryGenome with the given recovery policy.
func NewRecoveryGenome(policy *engine.RecoveryPolicy, config RecoveryGenomeConfig) *RecoveryGenome {
	return &RecoveryGenome{
		policy: policy,
		config: config,
	}
}

// Name returns the genome identifier.
func (g *RecoveryGenome) Name() string { return RecoveryGenomeName }

// Policy returns the current recovery policy. Used by the Diff Engine.
func (g *RecoveryGenome) Policy() *engine.RecoveryPolicy { return g.policy }

// Mutate generates n candidate genomes with one random parameter change each.
func (g *RecoveryGenome) Mutate(_ context.Context, n int) ([]Genome, error) {
	if n <= 0 {
		return nil, nil
	}

	children := make([]Genome, 0, n)
	for i := 0; i < n; i++ {
		child := g.clone()
		param := rand.Intn(3)
		switch param {
		case 0:
			child.mutateStrategy()
		case 1:
			child.mutateMaxAttempts()
		case 2:
			child.mutateReplacementAgent()
		}
		children = append(children, child)
	}
	return children, nil
}

// Crossover recombines this genome with another to produce a child.
func (g *RecoveryGenome) Crossover(_ context.Context, other Genome) (Genome, error) {
	otherRG, ok := other.(*RecoveryGenome)
	if !ok {
		return nil, fmt.Errorf("recovery: crossover incompatible genome type %T", other)
	}

	child := g.clone()
	if rand.Float64() < 0.5 {
		child.policy.Strategy = otherRG.policy.Strategy
	}
	if rand.Float64() < 0.5 {
		child.policy.MaxAttempts = otherRG.policy.MaxAttempts
	}
	if rand.Float64() < 0.5 {
		child.policy.ReplacementAgent = otherRG.policy.ReplacementAgent
	}
	return child, nil
}

// Fitness evaluates the recovery strategy quality based on real recovery
// evidence. It aggregates the measured recovery success rate (Value in [0, 1])
// produced by chaos arena and runtime recovery paths. When no evidence is
// available yet, a neutral 0.5 is returned so the GA keeps exploring.
//
// The strategy type is not hard-coded here: fitness reflects actual recovery
// outcomes under the current strategy, so the GA converges to whichever
// strategy (replace/retry/fail_fast) demonstrably recovers the most.
func (g *RecoveryGenome) Fitness(ctx context.Context) (float64, error) {
	score, err := avgFitnessValue(ctx, g.config.EvidenceStore, "recovery", 0, 50)
	if err != nil {
		return 0.5, nil
	}
	return score, nil
}

// Snapshot returns the current recovery policy as the serializable state.
func (g *RecoveryGenome) Snapshot(_ context.Context) (any, error) {
	return g.policy, nil
}

// ── Mutation implementations ─────────────────

func (g *RecoveryGenome) mutateStrategy() {
	strategies := []engine.RecoveryStrategy{
		engine.RecoveryRetry,
		engine.RecoveryReplaceNode,
		engine.RecoveryFailFast,
	}
	g.policy.Strategy = strategies[rand.Intn(len(strategies))]
}

func (g *RecoveryGenome) mutateMaxAttempts() {
	delta := rand.Intn(3) - 1 // [-1, 1]
	g.policy.MaxAttempts += delta
	if g.policy.MaxAttempts < g.config.MaxAttemptsMin {
		g.policy.MaxAttempts = g.config.MaxAttemptsMin
	}
	if g.policy.MaxAttempts > g.config.MaxAttemptsMax {
		g.policy.MaxAttempts = g.config.MaxAttemptsMax
	}
}

func (g *RecoveryGenome) mutateReplacementAgent() {
	agents := []string{"auto-healer", "fallback-analyzer", "degraded-mode"}
	g.policy.ReplacementAgent = agents[rand.Intn(len(agents))]
}

// clone creates a deep copy of the RecoveryGenome.
func (g *RecoveryGenome) clone() *RecoveryGenome {
	cp := *g.policy
	return &RecoveryGenome{
		policy: &cp,
		config: g.config,
	}
}
