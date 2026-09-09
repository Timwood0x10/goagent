// Package genome provides the public API for genetic algorithm genome operations,
// including crossover (recombination) operators for combining parent strategies.
package genome

import (
	"context"
	"fmt"

	"github.com/Timwood0x10/ares/internal/evoapi/mutation"
	internalgenome "github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
	internalmutation "github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// CrossoverType defines the crossover strategy used during reproduction.
type CrossoverType string

const (
	CrossoverUniform     CrossoverType = "uniform"
	CrossoverSinglePoint CrossoverType = "single_point"
	CrossoverTwoPoint    CrossoverType = "two_point"
	CrossoverScattered   CrossoverType = "scattered"
)

// PromptCrossoverMode controls how prompt templates are inherited during crossover.
type PromptCrossoverMode int

const (
	PromptInherit PromptCrossoverMode = iota
	PromptHalfSplit
	PromptUniform
)

// Crosser wraps the internal genome crossover engine for public use.
type Crosser struct {
	inner *internalgenome.Crossover
}

// CrosserConfig holds configuration for creating a Crosser.
type CrosserConfig struct {
	CrossoverType CrossoverType
	PromptMode    PromptCrossoverMode
}

// NewCrosser creates a new public Crosser wrapping the internal crossover engine.
func NewCrosser(cfg CrosserConfig) (*Crosser, error) {
	var opts []internalgenome.CrossoverOption

	switch cfg.PromptMode {
	case PromptHalfSplit:
		opts = append(opts, internalgenome.WithPromptMode(internalgenome.PromptHalfSplit))
	case PromptUniform:
		opts = append(opts, internalgenome.WithPromptMode(internalgenome.PromptUniform))
	default:
		opts = append(opts, internalgenome.WithPromptMode(internalgenome.PromptInherit))
	}

	inner, err := internalgenome.NewCrossover(opts...)
	if err != nil {
		return nil, fmt.Errorf("new crosser: %w", err)
	}

	return &Crosser{inner: inner}, nil
}

// Crossover performs crossover on two parent strategies and returns a child strategy.
func (c *Crosser) Crossover(ctx context.Context, parentA, parentB *mutation.Strategy) (*mutation.Strategy, error) {
	if parentA == nil || parentB == nil {
		return nil, fmt.Errorf("both parents must be non-nil")
	}

	internalA := parentA.ToInternal()
	internalB := parentB.ToInternal()

	child, err := c.inner.Crossover(ctx, internalA, internalB)
	if err != nil {
		return nil, fmt.Errorf("crossover: %w", err)
	}

	return mutation.FromInternal(child), nil
}

// CrossWithType performs crossover with a specific type override.
func (c *Crosser) CrossWithType(ctx context.Context, parentA, parentB *mutation.Strategy, crossoverType CrossoverType) (*mutation.Strategy, error) {
	if parentA == nil || parentB == nil {
		return nil, fmt.Errorf("both parents must be non-nil")
	}

	internalA := parentA.ToInternal()
	internalB := parentB.ToInternal()

	switch crossoverType {
	case CrossoverUniform, CrossoverScattered, "":
		child, err := c.inner.Crossover(ctx, internalA, internalB)
		if err != nil {
			return nil, fmt.Errorf("uniform crossover: %w", err)
		}
		return mutation.FromInternal(child), nil

	case CrossoverSinglePoint:
		return c.singlePointCrossover(ctx, internalA, internalB)

	case CrossoverTwoPoint:
		return c.twoPointCrossover(ctx, internalA, internalB)

	default:
		return nil, fmt.Errorf("unsupported crossover type: %s", crossoverType)
	}
}

func (c *Crosser) singlePointCrossover(ctx context.Context, a, b *internalmutation.Strategy) (*mutation.Strategy, error) {
	allKeys := collectSortedKeys(a.Params, b.Params)
	if len(allKeys) == 0 {
		child, err := c.inner.Crossover(ctx, a, b)
		if err != nil {
			return nil, err
		}
		return mutation.FromInternal(child), nil
	}

	splitPoint := len(allKeys) / 2
	childParams := make(map[string]any, len(allKeys))
	for i, key := range allKeys {
		if i < splitPoint {
			if v, ok := a.Params[key]; ok {
				childParams[key] = v
			} else if v, ok := b.Params[key]; ok {
				childParams[key] = v
			}
		} else {
			if v, ok := b.Params[key]; ok {
				childParams[key] = v
			} else if v, ok := a.Params[key]; ok {
				childParams[key] = v
			}
		}
	}

	child := &internalmutation.Strategy{
		ID:                   a.ID + "-x-" + b.ID,
		ParentID:             a.ID + "," + b.ID,
		Version:              max(a.Version, b.Version) + 1,
		Params:               childParams,
		PromptTemplate:       a.PromptTemplate,
		StrategyMutationType: internalmutation.MutationCrossover,
		MutationDesc:         "single_point_crossover",
		Score:                -1,
	}
	return mutation.FromInternal(child), nil
}

func (c *Crosser) twoPointCrossover(ctx context.Context, a, b *internalmutation.Strategy) (*mutation.Strategy, error) {
	allKeys := collectSortedKeys(a.Params, b.Params)
	if len(allKeys) < 3 {
		return c.singlePointCrossover(ctx, a, b)
	}

	pt1 := len(allKeys) / 3
	pt2 := 2 * len(allKeys) / 3
	if pt1 == pt2 {
		pt2 = pt1 + 1
	}

	childParams := make(map[string]any, len(allKeys))
	for i, key := range allKeys {
		if i < pt1 || i >= pt2 {
			if v, ok := a.Params[key]; ok {
				childParams[key] = v
			} else if v, ok := b.Params[key]; ok {
				childParams[key] = v
			}
		} else {
			if v, ok := b.Params[key]; ok {
				childParams[key] = v
			} else if v, ok := a.Params[key]; ok {
				childParams[key] = v
			}
		}
	}

	child := &internalmutation.Strategy{
		ID:                   a.ID + "-x2-" + b.ID,
		ParentID:             a.ID + "," + b.ID,
		Version:              max(a.Version, b.Version) + 1,
		Params:               childParams,
		PromptTemplate:       a.PromptTemplate,
		StrategyMutationType: internalmutation.MutationCrossover,
		MutationDesc:         "two_point_crossover",
		Score:                -1,
	}
	return mutation.FromInternal(child), nil
}

func collectSortedKeys(a, b map[string]any) []string {
	seen := make(map[string]struct{})
	for k := range a {
		seen[k] = struct{}{}
	}
	for k := range b {
		seen[k] = struct{}{}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

func sortStrings(s []string) {
	for i := 0; i < len(s); i++ {
		for j := i + 1; j < len(s); j++ {
			if s[i] > s[j] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
