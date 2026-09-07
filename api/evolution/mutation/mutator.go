// Package mutation provides the public API for strategy mutation in the evolution system.
// It wraps the internal mutation engine for use by external modules.
package mutation

import (
	"context"
	"fmt"

	internalmutation "github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// MutationType represents the type of mutation applied to a strategy.
type MutationType string

const (
	MutationParameter MutationType = "parameter"
	MutationPrompt    MutationType = "prompt"
	MutationTool      MutationType = "tool"
	MutationCrossover MutationType = "crossover"
	MutationRoot      MutationType = "root"
)

// Strategy is a public representation of an evolvable strategy.
type Strategy struct {
	ID             string
	Version        int
	Score          float64
	ParentID       string
	PromptTemplate string
	Params         map[string]any
	MutationType   MutationType
}

// ToInternal converts a public Strategy to an internal mutation.Strategy.
func (s *Strategy) ToInternal() *internalmutation.Strategy {
	if s == nil {
		return nil
	}
	return &internalmutation.Strategy{
		ID:                   s.ID,
		Version:              s.Version,
		Score:                s.Score,
		ParentID:             s.ParentID,
		PromptTemplate:       s.PromptTemplate,
		Params:               internalmutation.CloneParams(s.Params),
		StrategyMutationType: parseMutationType(s.MutationType),
	}
}

// FromInternal converts an internal mutation.Strategy to a public Strategy.
func FromInternal(s *internalmutation.Strategy) *Strategy {
	if s == nil {
		return nil
	}
	return &Strategy{
		ID:             s.ID,
		Version:        s.Version,
		Score:          s.Score,
		ParentID:       s.ParentID,
		PromptTemplate: s.PromptTemplate,
		Params:         internalmutation.CloneParams(s.Params),
		MutationType:   toMutationType(s.StrategyMutationType),
	}
}

func parseMutationType(mt MutationType) internalmutation.MutationType {
	switch mt {
	case MutationParameter:
		return internalmutation.MutationParameter
	case MutationPrompt:
		return internalmutation.MutationPrompt
	case MutationTool:
		return internalmutation.MutationTool
	case MutationCrossover:
		return internalmutation.MutationCrossover
	case MutationRoot, "":
		return internalmutation.MutationRoot
	default:
		return internalmutation.MutationRoot
	}
}

func toMutationType(mt internalmutation.MutationType) MutationType {
	switch mt {
	case internalmutation.MutationParameter:
		return MutationParameter
	case internalmutation.MutationPrompt:
		return MutationPrompt
	case internalmutation.MutationTool:
		return MutationTool
	case internalmutation.MutationCrossover:
		return MutationCrossover
	case internalmutation.MutationRoot:
		return MutationRoot
	default:
		return MutationRoot
	}
}

// Mutator wraps the internal mutation engine for public use.
type Mutator struct {
	inner      *internalmutation.Mutator
	paramProb  float64
	promptProb float64
}

// MutatorConfig holds configuration for creating a Mutator.
type MutatorConfig struct {
	// ParamRanges defines the allowed values for each mutable parameter.
	// Keys are parameter names, values are the allowed values.
	// If nil, default ranges are used.
	ParamRanges map[string][]any

	// PromptPool is the set of available prompt templates for prompt mutation.
	// If nil, prompt mutation is disabled.
	PromptPool []string

	// ToolPool is the set of available tool configurations for tool mutation.
	// If nil, tool mutation is disabled.
	ToolPool []string

	// ParamMutationProb is the probability of mutating a parameter (0.0-1.0).
	// Default: 0.3
	ParamMutationProb float64

	// PromptMutationProb is the probability of mutating the prompt template (0.0-1.0).
	// Default: 0.3
	PromptMutationProb float64
}

// NewMutator creates a new public Mutator wrapping the internal mutation engine.
func NewMutator(cfg MutatorConfig) (*Mutator, error) {
	var opts []internalmutation.MutatorOption

	if len(cfg.ParamRanges) > 0 {
		ranges := make(map[string]internalmutation.ParamRange, len(cfg.ParamRanges))
		for k, vals := range cfg.ParamRanges {
			ranges[k] = internalmutation.ParamRange{
				Name:   k,
				Values: vals,
			}
		}
		opts = append(opts, internalmutation.WithParamRanges(ranges))
	}

	if len(cfg.PromptPool) > 0 {
		opts = append(opts, internalmutation.WithPromptPool(cfg.PromptPool))
	}

	if len(cfg.ToolPool) > 0 {
		opts = append(opts, internalmutation.WithToolPool(cfg.ToolPool))
	}

	inner, err := internalmutation.NewMutator(opts...)
	if err != nil {
		return nil, fmt.Errorf("new mutator: %w", err)
	}

	paramProb := cfg.ParamMutationProb
	if paramProb <= 0 {
		paramProb = 0.3
	}
	promptProb := cfg.PromptMutationProb
	if promptProb <= 0 {
		promptProb = 0.3
	}

	return &Mutator{
		inner:      inner,
		paramProb:  paramProb,
		promptProb: promptProb,
	}, nil
}

// Mutate generates a single mutated child strategy from the parent.
func (m *Mutator) Mutate(ctx context.Context, parent *Strategy) (*Strategy, error) {
	if parent == nil {
		return nil, fmt.Errorf("parent strategy must not be nil")
	}

	internal := parent.ToInternal()
	children, err := m.inner.Mutate(ctx, internal, 1)
	if err != nil {
		return nil, fmt.Errorf("mutate: %w", err)
	}
	if len(children) == 0 {
		return nil, fmt.Errorf("mutator returned no children")
	}

	return FromInternal(children[0]), nil
}

// MutateN generates n mutated child strategies from the parent.
func (m *Mutator) MutateN(ctx context.Context, parent *Strategy, n int) ([]*Strategy, error) {
	if parent == nil {
		return nil, fmt.Errorf("parent strategy must not be nil")
	}
	if n <= 0 {
		return nil, fmt.Errorf("mutation count must be positive")
	}

	internal := parent.ToInternal()
	children, err := m.inner.Mutate(ctx, internal, n)
	if err != nil {
		return nil, fmt.Errorf("mutate n: %w", err)
	}

	result := make([]*Strategy, len(children))
	for i, child := range children {
		result[i] = FromInternal(child)
	}
	return result, nil
}
