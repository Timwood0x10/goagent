package mutation

import (
	"errors"
	"math/rand"
)

// MutatorOption configures a Mutator instance.
type MutatorOption func(*Mutator) error

// WithParamRanges sets custom parameter ranges for mutation.
// Replaces the default parameter ranges entirely.
//
// Args:
//
//	ranges - the custom parameter range map to use.
//
// Returns:
//
//	MutatorOption - the configuration function.
func WithParamRanges(ranges map[string]ParamRange) MutatorOption {
	return func(m *Mutator) error {
		if len(ranges) == 0 {
			return errors.New("param ranges must not be empty")
		}
		m.paramRanges = ranges
		return nil
	}
}

// WithPromptPool sets the available prompt templates for prompt mutation.
//
// Args:
//
//	pool - the list of prompt template strings.
//
// Returns:
//
//	MutatorOption - the configuration function.
func WithPromptPool(pool []string) MutatorOption {
	return func(m *Mutator) error {
		if len(pool) == 0 {
			return errors.New("prompt pool must not be empty")
		}
		m.promptPool = make([]string, len(pool))
		copy(m.promptPool, pool)
		return nil
	}
}

// WithToolPool sets the available tool configurations for tool mutation.
// Each entry in the pool represents a distinct tool configuration string
// (e.g., "web_search,calculator,code_exec").
//
// Args:
//
//	tools - the list of tool configuration strings.
//
// Returns:
//
//	MutatorOption - the configuration function.
func WithToolPool(tools []string) MutatorOption {
	return func(m *Mutator) error {
		if len(tools) == 0 {
			return errors.New("tool pool must not be empty")
		}
		m.toolPool = make([]string, len(tools))
		copy(m.toolPool, tools)
		return nil
	}
}

// WithDeterministicIDs enables counter-based strategy IDs instead of UUIDs.
// When enabled, each mutated strategy gets an ID like "det-mut-{parentShortID}-{counter}".
// This produces reproducible IDs across runs with the same inputs and seed.
//
// Args:
//
//	enabled - whether to use deterministic IDs.
//
// Returns:
//
//	MutatorOption - the configuration function.
func WithDeterministicIDs(enabled bool) MutatorOption {
	return func(m *Mutator) error {
		m.deterministicIDs = enabled
		return nil
	}
}

// WithSeed sets a deterministic seed for the random number generator.
// Using the same seed produces reproducible mutation results.
//
// Args:
//
//	seed - the random seed value.
//
// Returns:
//
//	MutatorOption - the configuration function.
func WithSeed(seed int64) MutatorOption {
	return func(m *Mutator) error {
		m.rng = rand.New(rand.NewSource(seed)) // #nosec G404 — strategy mutation doesn't need crypto rand
		return nil
	}
}
