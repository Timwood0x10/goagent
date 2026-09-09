// Package mutation is the DEPRECATED public alias of internal/evoapi/mutation
// (M5). New code MUST import internal/evoapi/mutation; this package exists
// only for external consumers and is scheduled for removal.
package mutation

import (
	"github.com/Timwood0x10/ares/internal/evoapi/mutation"
	internalmutation "github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// MutationType represents the type of mutation applied to a strategy.
type MutationType = mutation.MutationType

const (
	// MutationParameter mutates strategy parameters.
	MutationParameter = mutation.MutationParameter
	// MutationPrompt mutates the prompt template.
	MutationPrompt = mutation.MutationPrompt
	// MutationTool mutates tool configuration.
	MutationTool = mutation.MutationTool
	// MutationCrossover combines two parents.
	MutationCrossover = mutation.MutationCrossover
	// MutationRoot is the root strategy.
	MutationRoot = mutation.MutationRoot
)

// Strategy is a public representation of an evolvable strategy.
type Strategy = mutation.Strategy

// Mutator wraps the internal mutation engine for public use.
type Mutator = mutation.Mutator

// MutatorConfig holds configuration for creating a Mutator.
type MutatorConfig = mutation.MutatorConfig

// FromInternal converts an internal mutation strategy to a public Strategy.
func FromInternal(s *internalmutation.Strategy) *Strategy { return mutation.FromInternal(s) }

// NewMutator creates a new public Mutator wrapping the internal mutation engine.
func NewMutator(cfg MutatorConfig) (*Mutator, error) { return mutation.NewMutator(cfg) }
