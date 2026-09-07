package genome

import (
	"context"
	"errors"
	"fmt"
	"time"

	aresgenome "github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// PromptGenome wraps the existing GA (mutation.Strategy) with the new Genome interface.
// It bridges the legacy evolution system into the unified runtime evolution framework.
//
// Mutation uses the existing Mutator to generate child strategies.
// Crossover uses the existing Crossover to combine two strategies.
// Fitness uses the existing scoring pipeline (TieredScorer or ScorerFunc).
type PromptGenome struct {
	strategy *mutation.Strategy
	mutator  *mutation.Mutator
	crosser  aresgenome.CrossoverInterface
	scorer   aresgenome.ScorerFunc
	created  time.Time
}

// PromptGenomeConfig holds optional configuration for PromptGenome.
type PromptGenomeConfig struct {
	// Scorer is the fitness scoring function. If nil, Fitness returns 0.5.
	Scorer aresgenome.ScorerFunc

	// Mutator generates child strategies. If nil, a default mutator is created.
	Mutator *mutation.Mutator

	// Crosser combines two parent strategies. If nil, a default crosser is created.
	Crosser aresgenome.CrossoverInterface
}

// NewPromptGenome creates a PromptGenome wrapping the given strategy.
func NewPromptGenome(strategy *mutation.Strategy, cfg PromptGenomeConfig) *PromptGenome {
	g := &PromptGenome{
		strategy: strategy.Clone(),
		mutator:  cfg.Mutator,
		crosser:  cfg.Crosser,
		scorer:   cfg.Scorer,
		created:  time.Now(),
	}
	return g
}

// Name returns the genome identifier.
func (g *PromptGenome) Name() string { return "prompt" }

// Strategy returns the underlying mutation strategy. Used by Diff Engine.
func (g *PromptGenome) Strategy() *mutation.Strategy { return g.strategy }

// Mutate generates n candidate genomes using the existing GA mutator.
func (g *PromptGenome) Mutate(ctx context.Context, n int) ([]Genome, error) {
	if n <= 0 {
		return nil, nil
	}
	if g.mutator == nil {
		return nil, errors.New("prompt: no mutator configured")
	}

	children, err := g.mutator.Mutate(ctx, g.strategy, n)
	if err != nil {
		return nil, fmt.Errorf("prompt: mutate: %w", err)
	}

	result := make([]Genome, len(children))
	for i, child := range children {
		result[i] = &PromptGenome{
			strategy: child,
			mutator:  g.mutator,
			crosser:  g.crosser,
			scorer:   g.scorer,
			created:  time.Now(),
		}
	}
	return result, nil
}

// Crossover recombines this genome with another prompt genome to produce a child.
func (g *PromptGenome) Crossover(ctx context.Context, other Genome) (Genome, error) {
	otherPG, ok := other.(*PromptGenome)
	if !ok {
		return nil, fmt.Errorf("prompt: crossover incompatible genome type %T", other)
	}
	if g.crosser == nil {
		// Fallback: clone the higher-scoring parent.
		child := g.strategy.Clone()
		if otherPG.strategy.Score > g.strategy.Score {
			child = otherPG.strategy.Clone()
		}
		return &PromptGenome{
			strategy: child,
			mutator:  g.mutator,
			crosser:  g.crosser,
			scorer:   g.scorer,
			created:  time.Now(),
		}, nil
	}

	child, err := g.crosser.Crossover(ctx, g.strategy, otherPG.strategy)
	if err != nil {
		return nil, fmt.Errorf("prompt: crossover: %w", err)
	}
	return &PromptGenome{
		strategy: child,
		mutator:  g.mutator,
		crosser:  g.crosser,
		scorer:   g.scorer,
		created:  time.Now(),
	}, nil
}

// Fitness evaluates this genome using the configured scorer.
func (g *PromptGenome) Fitness(_ context.Context) (float64, error) {
	if g.scorer == nil {
		return 0.5, nil
	}
	score := g.scorer(g.strategy)
	if !IsScoreEvaluated(score) {
		return 0.0, nil
	}
	return score, nil
}

// Snapshot returns the current strategy as the serializable state.
func (g *PromptGenome) Snapshot(_ context.Context) (any, error) {
	return g.strategy, nil
}
