// Package genome provides genetic algorithm crossover (recombination) operators
// for combining two parent strategies into a child strategy. It implements
// uniform crossover and multi-point crossover variants with configurable
// randomness sources for deterministic reproduction in evolutionary algorithms.
package genome

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// PromptCrossoverMode controls how PromptTemplate is inherited during crossover.
type PromptCrossoverMode int

const (
	// PromptInherit inherits the prompt template from the higher-scoring parent.
	// This is the default mode and matches the original behavior.
	PromptInherit PromptCrossoverMode = iota
	// PromptHalfSplit performs half-sentence crossover on prompt templates
	// (first half of parent A, second half of parent B).
	PromptHalfSplit
	// PromptUniform randomly picks from either parent's prompt template.
	// Unlike PromptInherit, it does not use score — both parents have equal
	// chance, promoting prompt diversity in the population.
	PromptUniform
)

// CrossoverType defines how parameters are recombined between two parents.
type CrossoverType int

const (
	// CrossoverUniform: each parameter is independently selected from either parent.
	CrossoverUniform CrossoverType = iota
	// CrossoverTwoPoint: two cut points divide the sorted parameter list into three segments;
	// the middle segment is swapped between parents.
	CrossoverTwoPoint
	// CrossoverSegment: a contiguous block of parameters is selected from parent B;
	// the rest come from parent A.
	CrossoverSegment
)

// Crossover combines two parent strategies into a child strategy using
// uniform crossover by default. Each parameter is independently selected
// from either parent A or parent B with equal probability.
type Crossover struct {
	rng              *rand.Rand   // Deterministic randomness source.
	deterministicIDs bool         // When true, use counter-based IDs instead of UUID.
	idCounter        atomic.Int64 // Monotonic counter for deterministic ID generation (thread-safe).
	promptMode       PromptCrossoverMode
	crossType        CrossoverType // Parameter recombination strategy.
}

// Validate checks the internal configuration state of the Crossover instance
// and returns an error if any invariant is violated. This is a defensive check
// intended for use after construction or deserialization, not as a replacement
// for option-level validation in NewCrossover().
//
// Validated invariants:
//   - rng must not be nil (required for reproducible parameter selection).
//   - promptMode must be a recognized value (PromptInherit/PromptHalfSplit/PromptUniform).
//
// Returns:
//
//	error - non-nil if configuration is invalid, nil if all invariants hold.
func (c *Crossover) Validate() error {
	if c == nil {
		return errors.New("crossover Validate: instance is nil")
	}
	if c.rng == nil {
		return errors.New("crossover: rng must not be nil, ensure WithSeed was called or NewCrossover succeeded")
	}
	switch c.promptMode {
	case PromptInherit, PromptHalfSplit, PromptUniform:
		// Valid modes.
	default:
		return fmt.Errorf("crossover: invalid prompt mode %d, must be one of PromptInherit(%d), PromptHalfSplit(%d), PromptUniform(%d)",
			c.promptMode, PromptInherit, PromptHalfSplit, PromptUniform)
	}
	return nil
}

// NewCrossover creates a new crossover operator with default configuration.
//
// Default configuration:
//   - rng: seeded with current time (non-deterministic).
//   - promptMode: PromptInherit.
//
// Args:
//
//	opts - optional configuration functions (WithSeed).
//
// Returns:
//
//	*Crossover - the configured crossover instance.
//	error - non-nil if any option fails validation.
func NewCrossover(opts ...CrossoverOption) (*Crossover, error) {
	c := &Crossover{
		rng:        rand.New(rand.NewSource(rand.Int63())), // #nosec G404 - strategy crossover doesn't need crypto rand
		promptMode: PromptInherit,
	}

	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, fmt.Errorf("apply crossover option: %w", err)
		}
	}

	return c, nil
}

// CrossoverOption configures a Crossover instance.
type CrossoverOption func(*Crossover) error

// WithSeed sets the random seed for deterministic crossover results.
// Using the same seed produces reproducible child strategies.
//
// Args:
//
//	seed - the random seed value.
//
// Returns:
//
//	CrossoverOption - the configuration function.
func WithSeed(seed int64) CrossoverOption {
	return func(c *Crossover) error {
		c.rng = rand.New(rand.NewSource(seed)) // #nosec G404 - strategy crossover doesn't need crypto rand
		return nil
	}
}

// WithDeterministicIDs enables counter-based child strategy IDs instead of UUIDs.
// When enabled, each crossover child gets an ID like "det-cross-{counter}".
// This ensures reproducible IDs across runs with the same inputs and seed.
//
// Args:
//
//	enabled - whether to use deterministic IDs.
//
// Returns:
//
//	CrossoverOption - the configuration function.
func WithDeterministicIDs(enabled bool) CrossoverOption {
	return func(c *Crossover) error {
		c.deterministicIDs = enabled
		return nil
	}
}

// WithPromptMode sets the prompt crossover mode for combining parent prompt templates.
// Supported modes: PromptInherit, PromptHalfSplit, PromptUniform.
// Default is PromptInherit.
//
// Args:
//
//	mode - the prompt crossover mode to use.
//
// Returns:
//
//	CrossoverOption - the configuration function.
func WithPromptMode(mode PromptCrossoverMode) CrossoverOption {
	return func(c *Crossover) error {
		c.promptMode = mode
		return nil
	}
}

// WithCrossoverType sets the parameter recombination strategy.
// Default: CrossoverUniform (each parameter independently selected from either parent).
// Supported: CrossoverUniform, CrossoverTwoPoint, CrossoverSegment.
func WithCrossoverType(t CrossoverType) CrossoverOption {
	return func(c *Crossover) error {
		if t < CrossoverUniform || t > CrossoverSegment {
			return fmt.Errorf("invalid crossover type: %d", int(t))
		}
		c.crossType = t
		return nil
	}
}

// Crossover performs uniform crossover on two parent strategies.
// For each parameter key present in either parent, the child inherits
// from parent A or B with 50% probability each. The PromptTemplate
// is inherited from the higher-scoring parent (parent A on tie).
//
// Args:
//
//	ctx - operation context (used for cancellation).
//	a - first parent strategy (must not be nil).
//	b - second parent strategy (must not be nil).
//
// Returns:
//
//	*mutation.Strategy - the generated child strategy.
//	error - mutation.ErrNilParent if either parent is nil, ctx.Err() if cancelled.
func (c *Crossover) Crossover(ctx context.Context, a, b *mutation.Strategy) (*mutation.Strategy, error) {
	if a == nil || b == nil {
		return nil, fmt.Errorf("%w: both parents must be non-nil", mutation.ErrNilParent)
	}

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("crossover cancelled: %w", ctx.Err())
	default:
	}

	var desc string
	var childParams map[string]any

	switch c.crossType {
	case CrossoverTwoPoint:
		childParams, desc = c.twoPointCrossParams(a.Params, b.Params)
	case CrossoverSegment:
		childParams, desc = c.segmentCrossParams(a.Params, b.Params)
	default: // CrossoverUniform
		childParams, desc = c.uniformCrossParams(a.Params, b.Params)
	}

	var promptTemplate string
	switch c.promptMode {
	case PromptHalfSplit:
		promptTemplate = c.halfSplitPromptCrossover(a, b)
		if promptTemplate != a.PromptTemplate && promptTemplate != b.PromptTemplate {
			desc += " | half_split_prompt"
		}
	case PromptUniform:
		if c.rng.Intn(2) == 0 {
			promptTemplate = a.PromptTemplate
		} else {
			promptTemplate = b.PromptTemplate
		}
		desc += " | uniform_prompt"
	default: // PromptInherit
		promptTemplate = c.selectPromptTemplate(a, b)
	}

	child := &mutation.Strategy{
		ID:                   c.generateChildID(a.ID, b.ID),
		ParentID:             formatParentIDs(a.ID, b.ID),
		Version:              maxVersion(a.Version, b.Version) + 1,
		Params:               childParams,
		PromptTemplate:       promptTemplate,
		StrategyMutationType: mutation.MutationCrossover,
		MutationDesc:         desc,
		Score:                -1,
		CreatedAt:            time.Now(),
	}

	el.DebugContext(ctx, "crossover completed",
		"child_id", child.ID,
		"parent_a", a.ID,
		"parent_b", b.ID,
		"version", child.Version,
	)

	return child, nil
}

// uniformCrossParams applies uniform crossover to the parameter maps of two parents.
// For each key present in either parent, randomly selects from A or B.
// Returns the child params map and a description string of inheritance.
func (c *Crossover) uniformCrossParams(paramsA, paramsB map[string]any) (map[string]any, string) {
	allKeys := collectParamKeys(paramsA, paramsB)
	sort.Strings(allKeys)

	childParams := make(map[string]any, len(allKeys))
	var fromA, fromB []string

	for _, key := range allKeys {
		valA, existsA := paramsA[key]
		valB, existsB := paramsB[key]

		switch {
		case existsA && existsB:
			if c.rng.Float64() < 0.5 {
				childParams[key] = valA
				fromA = append(fromA, key)
			} else {
				childParams[key] = valB
				fromB = append(fromB, key)
			}
		case existsA:
			childParams[key] = valA
			fromA = append(fromA, key)
		default:
			childParams[key] = valB
			fromB = append(fromB, key)
		}
	}

	desc := buildInheritanceDesc(fromA, fromB, "uniform")
	return childParams, desc
}

// twoPointCrossParams applies two-point crossover to the parameter maps.
// The sorted parameter list is divided into three segments by two cut points;
// the middle segment is swapped between parents.
//
// Example with 6 params, cut at 2 and 4:
//
//	Parent A: [p1, p2, p3, p4, p5, p6]
//	Parent B: [q1, q2, q3, q4, q5, q6]
//	Child:    [p1, p2, q3, q4, p5, p6]  (middle segment from B)
func (c *Crossover) twoPointCrossParams(paramsA, paramsB map[string]any) (map[string]any, string) {
	allKeys := collectParamKeys(paramsA, paramsB)
	sort.Strings(allKeys)
	n := len(allKeys)
	if n < 3 {
		// Fall back to uniform for small parameter sets.
		return c.uniformCrossParams(paramsA, paramsB)
	}

	points := generateCrossoverPoints(c.rng, 2, n)
	if len(points) < 2 {
		return c.uniformCrossParams(paramsA, paramsB)
	}
	// points[0] and points[1] are the two cut positions (1-indexed).
	// The middle segment (points[0] to points[1]-1) comes from parent B.
	// Outer segments (before points[0] and after points[1]-1) come from parent A.
	cut1, cut2 := points[0], points[1]

	childParams := make(map[string]any, n)
	var fromA, fromB []string

	for i, key := range allKeys {
		valA, existsA := paramsA[key]
		valB, existsB := paramsB[key]

		// Middle segment: from parent B.
		if i >= cut1 && i < cut2 {
			if existsB {
				childParams[key] = valB
				fromB = append(fromB, key)
			} else if existsA {
				childParams[key] = valA
				fromA = append(fromA, key)
			}
			continue
		}

		// Outer segments: from parent A.
		if existsA {
			childParams[key] = valA
			fromA = append(fromA, key)
		} else if existsB {
			childParams[key] = valB
			fromB = append(fromB, key)
		}
	}

	desc := buildInheritanceDesc(fromA, fromB, "two_point")
	return childParams, desc
}

// segmentCrossParams applies segment crossover: a contiguous block of parameters
// is taken from parent B, and the rest from parent A. The segment start and length
// are randomly chosen.
//
// Example with 6 params, segment at 2-3:
//
//	Parent A: [p1, p2, p3, p4, p5, p6]
//	Parent B: [q1, q2, q3, q4, q5, q6]
//	Child:    [p1, p2, q3, q4, p5, p6]  (segment 2-3 from B)
func (c *Crossover) segmentCrossParams(paramsA, paramsB map[string]any) (map[string]any, string) {
	allKeys := collectParamKeys(paramsA, paramsB)
	sort.Strings(allKeys)
	n := len(allKeys)
	if n < 2 {
		return c.uniformCrossParams(paramsA, paramsB)
	}

	// Randomly choose segment start and end.
	start := c.rng.Intn(n)
	end := c.rng.Intn(n)
	if start > end {
		start, end = end, start
	}
	if end-start < 1 {
		// Ensure at least 1 param in the segment, or fall back.
		if start < n-1 {
			end = start + 1
		} else {
			start = end - 1
		}
	}

	childParams := make(map[string]any, n)
	var fromA, fromB []string

	for i, key := range allKeys {
		valA, existsA := paramsA[key]
		valB, existsB := paramsB[key]

		// Segment range: from parent B.
		if i >= start && i <= end {
			if existsB {
				childParams[key] = valB
				fromB = append(fromB, key)
			} else if existsA {
				childParams[key] = valA
				fromA = append(fromA, key)
			}
			continue
		}

		// Outside segment: from parent A.
		if existsA {
			childParams[key] = valA
			fromA = append(fromA, key)
		} else if existsB {
			childParams[key] = valB
			fromB = append(fromB, key)
		}
	}

	desc := buildInheritanceDesc(fromA, fromB, "segment")
	return childParams, desc
}

// selectPromptTemplate returns the prompt template from the higher-scoring parent.
// If scores are equal, parent A's template is preferred.
func (c *Crossover) selectPromptTemplate(a, b *mutation.Strategy) string {
	if a.Score >= b.Score {
		return a.PromptTemplate
	}
	return b.PromptTemplate
}

// halfSplitPromptCrossover performs half-split crossover on prompt templates.
// The child inherits the first half of parent A's template and the second half
// of parent B's template. This is the "half-sentence crossover"
// from the genetic algorithm design document.
//
// If either template is empty, falls back to selectPromptTemplate behavior.
// If both are empty, returns empty string.
func (c *Crossover) halfSplitPromptCrossover(a, b *mutation.Strategy) string {
	tmplA := a.PromptTemplate
	tmplB := b.PromptTemplate

	// Fall back if either template is empty.
	if tmplA == "" || tmplB == "" {
		return c.selectPromptTemplate(a, b)
	}

	// mid is based on rune count, not byte count, so multi-byte characters
	// (Chinese, emoji, etc.) are never split in the middle of a character.
	// tmplB's rune-buffer slicing avoids panics when mid > len(runesB).
	runesA := []rune(tmplA)
	runesB := []rune(tmplB)

	mid := len(runesA) / 2
	if len(runesA) > 0 && mid == 0 {
		mid = 1
	}
	if len(runesB) <= mid {
		return string(runesA[:mid]) + tmplB
	}

	result := string(runesA[:mid]) + string(runesB[mid:])

	el.DebugContext(context.Background(), "half-split prompt crossover",
		"parent_a_len", len(tmplA),
		"parent_b_len", len(tmplB),
		"mid_point", mid,
		"child_len", len(result),
	)

	return result
}

// CrossoverInterface defines the contract for crossover operations.
// Implementations combine two parent strategies to produce a child strategy.
type CrossoverInterface interface {
	// Crossover performs crossover on two parent strategies and returns a child.
	Crossover(ctx context.Context, a, b *mutation.Strategy) (*mutation.Strategy, error)
}

// generateChildID returns a unique child strategy ID using either a deterministic
// counter or a random UUID, depending on configuration.
func (c *Crossover) generateChildID(parentA, parentB string) string {
	if c.deterministicIDs {
		counter := c.idCounter.Add(1)
		aShort := parentA
		if len(aShort) > 8 {
			aShort = aShort[:8]
		}
		bShort := parentB
		if len(bShort) > 8 {
			bShort = bShort[:8]
		}
		return fmt.Sprintf("det-cross-%s-%s-%d", aShort, bShort, counter)
	}
	return uuid.New().String()
}

// collectParamKeys returns the sorted union of all parameter keys from both parent maps.
func collectParamKeys(paramsA, paramsB map[string]any) []string {
	keySet := make(map[string]struct{}, len(paramsA)+len(paramsB))
	for k := range paramsA {
		keySet[k] = struct{}{}
	}
	for k := range paramsB {
		keySet[k] = struct{}{}
	}

	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// formatParentIDs combines two parent IDs into a single ParentID field.
func formatParentIDs(idA, idB string) string {
	return idA + "\u00d7" + idB // × symbol (Unicode multiplication sign)
}

// maxVersion returns the larger of two version numbers.
func maxVersion(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// buildInheritanceDesc creates a human-readable description of which params came from which parent.
func buildInheritanceDesc(fromA, fromB []string, method string) string {
	parts := []string{fmt.Sprintf("crossover(%s): ", method)}
	if len(fromA) > 0 {
		parts = append(parts, fmt.Sprintf("from_A=[%s]", strings.Join(fromA, ",")))
	}
	if len(fromB) > 0 {
		parts = append(parts, fmt.Sprintf("from_B=[%s]", strings.Join(fromB, ",")))
	}
	if len(fromA) == 0 && len(fromB) == 0 {
		parts = append(parts, "no_parameters")
	}
	return strings.Join(parts, " ")
}

// generateCrossoverPoints generates k unique crossover point indices in range [1, n-1].
// If k >= n-1, returns all possible split positions.
// Uses O(k) average-case allocation via rejection sampling; falls back to
// O(N) shuffle for dense selections (k > N/2).
func generateCrossoverPoints(rng *rand.Rand, k, n int) []int {
	maxPoints := n - 1
	if maxPoints <= 0 {
		return nil
	}
	if k >= maxPoints {
		k = maxPoints
	}

	// For dense selections, O(N) shuffle is fine.
	// k == maxPoints guard prevents any theoretical infinite-loop edge case
	// if the upper-bound cap logic is ever removed or refactored.
	if k > maxPoints/2 || k == maxPoints {
		positions := make([]int, maxPoints)
		for i := 0; i < maxPoints; i++ {
			positions[i] = i + 1
		}
		for i := 0; i < k; i++ {
			j := rng.Intn(maxPoints-i) + i
			positions[i], positions[j] = positions[j], positions[i]
		}
		result := positions[:k]
		sort.Ints(result)
		return result
	}

	// Rejection sampling: O(k) average-case allocation.
	seen := make(map[int]struct{}, k)
	result := make([]int, 0, k)
	for len(result) < k {
		candidate := rng.Intn(maxPoints) + 1
		if _, exists := seen[candidate]; !exists {
			seen[candidate] = struct{}{}
			result = append(result, candidate)
		}
	}
	sort.Ints(result)
	return result
}
