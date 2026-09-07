package evolution

import (
	"context"
	"errors"
	"fmt"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// MutationAdapter wraps a mutation.Mutator to implement evolution.MutatorInterface.
// It handles type conversion between evolution.Strategy and mutation.Strategy.
type MutationAdapter struct {
	mutator *mutation.Mutator
}

// NewMutationAdapter creates an adapter from a mutation.Mutator.
//
// Args:
//
//	m - the mutation mutator to wrap (must not be nil).
//
// Returns:
//
//	*MutationAdapter - the adapter instance.
//	error - non-nil if mutator is nil.
func NewMutationAdapter(m *mutation.Mutator) (*MutationAdapter, error) {
	if m == nil {
		return nil, errors.New("mutator must not be nil")
	}
	return &MutationAdapter{mutator: m}, nil
}

// Mutate delegates to the wrapped mutator and converts types.
// It converts the evolution.Strategy parent to mutation.Strategy before calling
// the mutator, then converts the resulting []*mutation.Strategy back to []evolution.Strategy.
//
// Args:
//
//	ctx - operation context for cancellation.
//	parent - the parent strategy in evolution package format.
//	n - number of candidate strategies to generate.
//
// Returns:
//
//	[]Strategy - the generated child strategies in evolution package format.
//	error - delegation error from the wrapped mutator or conversion error.
func (a *MutationAdapter) Mutate(ctx context.Context, parent Strategy, n int) ([]Strategy, error) {
	// Convert evolution.Strategy to mutation.Strategy.
	mutationParent := evolutionToMutationStrategy(parent)

	// Delegate to the wrapped mutator.
	children, err := a.mutator.Mutate(ctx, &mutationParent, n)
	if err != nil {
		return nil, fmt.Errorf("mutation adapter: %w", err)
	}

	// Convert []*mutation.Strategy back to []evolution.Strategy.
	result := make([]Strategy, 0, len(children))
	for _, child := range children {
		evolutionChild := mutationToEvolutionStrategy(child)
		result = append(result, evolutionChild)
	}

	return result, nil
}

// evolutionToMutationStrategy converts an evolution.Strategy to a mutation.Strategy.
func evolutionToMutationStrategy(s Strategy) mutation.Strategy {
	return mutation.Strategy{
		ID:                   s.ID,
		ParentID:             s.ParentID,
		Version:              s.Version,
		Name:                 s.Name,
		Params:               mutation.CloneParams(s.Params),
		PromptTemplate:       s.PromptTemplate,
		StrategyMutationType: mutation.ParseMutationType(s.StrategyMutationType),
		MutationDesc:         s.MutationDesc,
		Score:                s.Score,
		CreatedAt:            s.CreatedAt,
	}
}

// mutationToEvolutionStrategy converts a mutation.Strategy to an evolution.Strategy.
func mutationToEvolutionStrategy(s *mutation.Strategy) Strategy {
	if s == nil {
		return Strategy{}
	}
	return Strategy{
		ID:                   s.ID,
		Version:              s.Version,
		Name:                 s.Name,
		Params:               mutation.CloneParams(s.Params),
		ParentID:             s.ParentID,
		PromptTemplate:       s.PromptTemplate,
		StrategyMutationType: s.StrategyMutationType.String(),
		MutationDesc:         s.MutationDesc,
		Score:                s.Score,
		CreatedAt:            s.CreatedAt,
	}
}
