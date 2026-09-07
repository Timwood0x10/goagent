package mutation

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/Timwood0x10/ares/internal/truncate"
)

// EvolutionHint represents a distilled guidance hint derived from past
// experiences. It captures what worked, what failed, and what parameter
// biases should be applied during strategy mutation.
type EvolutionHint struct {
	// ID is the unique identifier of this hint.
	ID string

	// TaskType is the type of task this hint applies to.
	TaskType string

	// Problem is the abstract problem statement.
	Problem string

	// Solution is the concise solution approach that worked.
	Solution string

	// Constraints are important constraints or context for the solution.
	Constraints []string

	// FailedPatterns are patterns that led to failure and should be avoided.
	FailedPatterns []string

	// PreferredTools are tool configurations that worked well.
	PreferredTools []string

	// PromptSnippets are prompt text snippets that contributed to success.
	PromptSnippets []string

	// ParamHints maps parameter names to suggested values.
	ParamHints map[string]float64

	// Confidence is the confidence score for this hint (0.0 to 1.0).
	Confidence float64

	// SourceExperienceIDs are the IDs of the experiences that produced this hint.
	SourceExperienceIDs []string
}

// StrategyOutcome records the result of deploying a strategy mutation,
// enabling the experience provider to learn from real execution outcomes.
type StrategyOutcome struct {
	// StrategyID is the ID of the strategy that was deployed.
	StrategyID string

	// TaskType is the type of task this strategy was used for.
	TaskType string

	// Success indicates whether the strategy deployment was successful.
	Success bool

	// Score is the fitness score achieved by this strategy.
	Score float64

	// Cost is the computational cost incurred.
	Cost float64

	// LatencyMs is the execution latency in milliseconds.
	LatencyMs int64

	// MutationType describes what kind of mutation produced this strategy.
	MutationType string

	// ExperienceIDs are the IDs of experiences that influenced this strategy.
	ExperienceIDs []string

	// Timestamp is when this outcome was recorded.
	Timestamp time.Time
}

// HintProvider provides evolution hints for guided strategy mutation.
// Implementations should return relevant hints for a given task type,
// or an empty slice when no hints are available.
type HintProvider interface {
	// HintsForTask returns evolution hints relevant to the given task type.
	// Returns up to limit hints, ordered by relevance.
	// An empty slice with nil error means no hints are available.
	HintsForTask(ctx context.Context, taskType string, limit int) ([]EvolutionHint, error)

	// RecordStrategyOutcome persists a strategy outcome for future learning.
	RecordStrategyOutcome(ctx context.Context, outcome StrategyOutcome) error
}

// GuidedMutatorOption configures an ExperienceGuidedMutator.
type GuidedMutatorOption func(*ExperienceGuidedMutator)

// WithGuidedConfidence sets the minimum confidence threshold for hints to
// influence mutation. Hints with Confidence below this threshold are ignored.
// Default is 0.5.
//
// Args:
//
//	confidence - minimum confidence threshold in [0.0, 1.0].
//
// Returns:
//
//	GuidedMutatorOption - the configuration function.
func WithGuidedConfidence(confidence float64) GuidedMutatorOption {
	return func(m *ExperienceGuidedMutator) {
		if confidence < 0 {
			confidence = 0
		}
		if confidence > 1 {
			confidence = 1
		}
		m.confidence = confidence
	}
}

// WithGuidedPromptBoost sets the factor by which prompt hints boost prompt
// mutation probability. Default is 2.0.
//
// Args:
//
//	boost - the boost multiplier (must be >= 1.0).
//
// Returns:
//
//	GuidedMutatorOption - the configuration function.
func WithGuidedPromptBoost(boost float64) GuidedMutatorOption {
	return func(m *ExperienceGuidedMutator) {
		if boost < 1 {
			boost = 1
		}
		m.promptBoost = boost
	}
}

// WithGuidedToolBoost sets the factor by which tool hints boost tool
// mutation probability. Default is 2.0.
//
// Args:
//
//	boost - the boost multiplier (must be >= 1.0).
//
// Returns:
//
//	GuidedMutatorOption - the configuration function.
func WithGuidedToolBoost(boost float64) GuidedMutatorOption {
	return func(m *ExperienceGuidedMutator) {
		if boost < 1 {
			boost = 1
		}
		m.toolBoost = boost
	}
}

// ExperienceGuidedMutator wraps a Mutator and biases its mutation decisions
// using evolution hints from an ExperienceProvider. When no hints are available
// or confidence is too low, it falls back to the base mutator's random behavior.
//
// Guidance effects:
//   - Prompt snippets from high-confidence hints bias prompt mutation choices.
//   - Preferred tools from hints bias tool mutation choices.
//   - ParamHints bias parameter value selection toward suggested values.
//   - FailedPatterns reduce the probability of mutation types associated
//     with known failure patterns.
type ExperienceGuidedMutator struct {
	base        *Mutator
	provider    HintProvider
	confidence  float64
	promptBoost float64
	toolBoost   float64
}

// NewExperienceGuidedMutator creates an ExperienceGuidedMutator wrapping the
// given base mutator with the provided hint provider.
//
// Args:
//
//	base - the underlying mutator (must not be nil).
//	provider - the hint provider for guidance (must not be nil).
//	opts - optional configuration functions.
//
// Returns:
//
//	*ExperienceGuidedMutator - the configured guided mutator.
//	error - non-nil if base or provider is nil.
func NewExperienceGuidedMutator(
	base *Mutator,
	provider HintProvider,
	opts ...GuidedMutatorOption,
) (*ExperienceGuidedMutator, error) {
	if base == nil {
		return nil, errors.New("base mutator must not be nil")
	}
	if provider == nil {
		return nil, errors.New("hint provider must not be nil")
	}

	m := &ExperienceGuidedMutator{
		base:        base,
		provider:    provider,
		confidence:  0.5,
		promptBoost: 2.0,
		toolBoost:   2.0,
	}

	for _, opt := range opts {
		opt(m)
	}

	return m, nil
}

// Mutate generates n mutated child strategies from the given parent.
// When hints are available (above confidence threshold), mutation decisions
// are biased by hint data. Otherwise, delegates to the base mutator.
//
// Args:
//
//	ctx - operation context for cancellation.
//	parent - the parent strategy to mutate (must not be nil).
//	n - number of child strategies to generate (must be > 0).
//
// Returns:
//
//	[]*Strategy - the generated child strategies.
//	error - ErrNilParent if parent is nil, ErrInvalidCount if n <= 0.
func (m *ExperienceGuidedMutator) Mutate(
	ctx context.Context,
	parent *Strategy,
	n int,
) ([]*Strategy, error) {
	if parent == nil {
		return nil, ErrNilParent
	}
	if n <= 0 {
		return nil, ErrInvalidCount
	}

	// Attempt to fetch hints. On any error or empty result, fall back to base.
	taskType := parentTaskType(parent)
	hints, err := m.provider.HintsForTask(ctx, taskType, n)
	if err != nil || len(hints) == 0 {
		return m.base.Mutate(ctx, parent, n)
	}

	// Filter hints by confidence threshold.
	hints = filterHintsByConfidence(hints, m.confidence)
	if len(hints) == 0 {
		return m.base.Mutate(ctx, parent, n)
	}

	// Merge hints to produce a consolidated guidance signal.
	guidance := mergeHints(hints)

	children := make([]*Strategy, 0, n)
	for i := 0; i < n; i++ {
		select {
		case <-ctx.Done():
			return children, ctx.Err()
		default:
		}

		child, err := m.mutateOneGuided(parent, i, guidance)
		if err != nil {
			return nil, fmt.Errorf("guided mutate child %d: %w", i, err)
		}
		children = append(children, child)
	}

	return children, nil
}

// guidedSignal holds consolidated guidance data from multiple hints.
type guidedSignal struct {
	promptSnippets []string
	preferredTools []string
	paramHints     map[string]float64
	failedPatterns []string
	hasPromptHints bool
	hasToolHints   bool
	hasParamHints  bool
	hasFailedHints bool
}

// filterHintsByConfidence returns hints with Confidence >= threshold.
func filterHintsByConfidence(hints []EvolutionHint, threshold float64) []EvolutionHint {
	if threshold <= 0 {
		return hints
	}

	result := make([]EvolutionHint, 0, len(hints))
	for _, h := range hints {
		if h.Confidence >= threshold {
			result = append(result, h)
		}
	}
	return result
}

// mergeHints combines multiple evolution hints into a single guidance signal.
// For multi-valued fields (PromptSnippets, PreferredTools, FailedPatterns),
// values are deduplicated and merged. For ParamHints, the mean value is used
// when the same parameter appears in multiple hints.
func mergeHints(hints []EvolutionHint) guidedSignal {
	signal := guidedSignal{
		paramHints: make(map[string]float64),
	}

	seenPrompts := make(map[string]int)  // value -> count
	seenTools := make(map[string]int)    // value -> count
	seenPatterns := make(map[string]int) // value -> count
	paramSum := make(map[string]float64)
	paramCount := make(map[string]int)

	for _, h := range hints {
		for _, s := range h.PromptSnippets {
			seenPrompts[s]++
		}
		for _, t := range h.PreferredTools {
			seenTools[t]++
		}
		for _, p := range h.FailedPatterns {
			seenPatterns[p]++
		}
		for k, v := range h.ParamHints {
			paramSum[k] += v
			paramCount[k]++
		}
	}

	// Sort keys for deterministic iteration.
	signal.promptSnippets = sortedKeys(seenPrompts)
	signal.preferredTools = sortedKeys(seenTools)
	signal.failedPatterns = sortedKeys(seenPatterns)

	for k := range paramCount {
		signal.paramHints[k] = paramSum[k] / float64(paramCount[k])
	}

	signal.hasPromptHints = len(signal.promptSnippets) > 0
	signal.hasToolHints = len(signal.preferredTools) > 0
	signal.hasParamHints = len(signal.paramHints) > 0
	signal.hasFailedHints = len(signal.failedPatterns) > 0

	return signal
}

// sortedKeys returns the keys of a string->int map sorted alphabetically.
func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// mutateOneGuided generates a single guided mutation.
// The mutation type is chosen based on guidance, then the selected mutation
// is biased by hint data.
func (m *ExperienceGuidedMutator) mutateOneGuided(
	parent *Strategy,
	index int,
	guidance guidedSignal,
) (*Strategy, error) {
	// Determine mutation type with bias from hints.
	mt := m.chooseGuidedMutationType(guidance)

	var child *Strategy
	var err error

	switch mt {
	case MutationPrompt:
		child, err = m.guidedMutatePrompt(parent, guidance)
	case MutationTool:
		child, err = m.guidedMutateTool(parent, guidance)
	default:
		child, err = m.guidedMutateParameter(parent, guidance)
	}

	if err != nil {
		return nil, err
	}

	// Fill in metadata.
	child.ID = uuid.New().String()
	child.ParentID = parent.ID
	child.Version = parent.Version + 1
	child.Score = -1
	child.CreatedAt = parent.CreatedAt

	return child, nil
}

// chooseGuidedMutationType selects a mutation type, biased by available guidance.
// When prompt or tool hints exist, the probability of those types is boosted.
// When failed patterns exist, the corresponding mutation types are penalized.
func (m *ExperienceGuidedMutator) chooseGuidedMutationType(
	guidance guidedSignal,
) MutationType {
	hasPrompt := len(m.base.promptPool) > 0 || guidance.hasPromptHints
	hasTool := len(m.base.toolPool) > 0 || guidance.hasToolHints

	if !hasPrompt && !hasTool {
		return MutationParameter
	}

	// Base probabilities (mirrors Mutator.mutateOne distribution).
	paramProb := 0.70
	promptProb := 0.15
	toolProb := 0.15

	// Adjust for unavailable pools.
	if !hasPrompt {
		paramProb = 0.80
		promptProb = 0
		toolProb = 0.20
	} else if !hasTool {
		paramProb = 0.80
		promptProb = 0.20
		toolProb = 0
	}

	// Boost prompt probability when prompt hints exist and pool is available.
	if guidance.hasPromptHints && hasPrompt {
		boostedPrompt := promptProb * m.promptBoost
		totalBeforeBoost := paramProb + promptProb + toolProb
		if totalBeforeBoost > 0 {
			// Redistribute: take from param, give to prompt.
			boostAmount := boostedPrompt - promptProb
			if boostAmount > paramProb*0.5 {
				boostAmount = paramProb * 0.5
			}
			promptProb += boostAmount
			paramProb -= boostAmount
		}
	}

	// Boost tool probability when tool hints exist and pool is available.
	if guidance.hasToolHints && hasTool {
		boostedTool := toolProb * m.toolBoost
		totalBeforeBoost := paramProb + promptProb + toolProb
		if totalBeforeBoost > 0 {
			boostAmount := boostedTool - toolProb
			if boostAmount > paramProb*0.5 {
				boostAmount = paramProb * 0.5
			}
			toolProb += boostAmount
			paramProb -= boostAmount
		}
	}

	// Penalize parameter mutation when failed patterns exist.
	if guidance.hasFailedHints {
		penalty := paramProb * 0.2
		paramProb -= penalty
		// Redistribute penalty to prompt and tool.
		switch {
		case promptProb > 0 && toolProb > 0:
			promptProb += penalty * 0.5
			toolProb += penalty * 0.5
		case promptProb > 0:
			promptProb += penalty
		case toolProb > 0:
			toolProb += penalty
		}
	}

	// Sample based on adjusted probabilities.
	total := paramProb + promptProb + toolProb
	r := m.base.rng.Float64() * total
	if r < paramProb {
		return MutationParameter
	}
	if hasPrompt && r < paramProb+promptProb {
		return MutationPrompt
	}
	if hasTool && r < paramProb+promptProb+toolProb {
		return MutationTool
	}
	return MutationParameter
}

// guidedMutatePrompt mutates the prompt template, preferring high-value
// snippets from guidance when available.
func (m *ExperienceGuidedMutator) guidedMutatePrompt(
	parent *Strategy,
	guidance guidedSignal,
) (*Strategy, error) {
	child := parent.Clone()

	// If we have prompt snippets from hints, prefer them.
	if guidance.hasPromptHints && len(guidance.promptSnippets) > 0 {
		// Pick a random snippet from the guidance.
		snippet := guidance.promptSnippets[m.base.rng.Intn(len(guidance.promptSnippets))]
		if snippet != "" {
			child.PromptTemplate = snippet
			child.MutationDesc = fmt.Sprintf("guided prompt from hint snippet: %q", truncate.WithEllipsis(snippet, 60))
			child.StrategyMutationType = MutationPrompt
			return child, nil
		}
	}

	// Fall back to prompt pool mutation.
	if len(m.base.promptPool) > 1 {
		newTemplate := m.base.pickDifferentString(m.base.promptPool, parent.PromptTemplate)
		if newTemplate != "" {
			child.PromptTemplate = newTemplate
			child.MutationDesc = "guided prompt from pool"
			child.StrategyMutationType = MutationPrompt
			return child, nil
		}
	}

	// No alternative prompt available; do parameter mutation instead.
	child.MutationDesc = "no prompt alternatives available, falling back to parameter"
	return m.base.mutateParameter(child)
}

// guidedMutateTool mutates the tool configuration, preferring tools from
// guidance when available.
func (m *ExperienceGuidedMutator) guidedMutateTool(
	parent *Strategy,
	guidance guidedSignal,
) (*Strategy, error) {
	child := parent.Clone()

	// If we have preferred tools from hints, use them.
	if guidance.hasToolHints && len(guidance.preferredTools) > 0 {
		tool := guidance.preferredTools[m.base.rng.Intn(len(guidance.preferredTools))]
		if tool != "" {
			if child.Params == nil {
				child.Params = make(map[string]any)
			}
			child.Params["tools"] = tool
			child.MutationDesc = fmt.Sprintf("guided tool from hint: %q", tool)
			child.StrategyMutationType = MutationTool
			return child, nil
		}
	}

	// Fall back to tool pool mutation.
	if len(m.base.toolPool) > 1 {
		currentTools, hasToolsKey := parent.Params["tools"].(string)
		if !hasToolsKey {
			if child.Params == nil {
				child.Params = make(map[string]any)
			}
			child.Params["tools"] = m.base.toolPool[0]
			child.MutationDesc = fmt.Sprintf("guided tool initialized to %q", m.base.toolPool[0])
			child.StrategyMutationType = MutationTool
			return child, nil
		}

		newTools := m.base.pickDifferentString(m.base.toolPool, currentTools)
		if newTools != "" {
			if child.Params == nil {
				child.Params = make(map[string]any)
			}
			child.Params["tools"] = newTools
			child.MutationDesc = fmt.Sprintf("guided tool changed to %q", newTools)
			child.StrategyMutationType = MutationTool
			return child, nil
		}
	}

	// No tool alternatives; do parameter mutation instead.
	child.MutationDesc = "no tool alternatives available, falling back to parameter"
	return m.base.mutateParameter(child)
}

// guidedMutateParameter mutates a parameter, biasing toward values suggested
// by ParamHints when available.
func (m *ExperienceGuidedMutator) guidedMutateParameter(
	parent *Strategy,
	guidance guidedSignal,
) (*Strategy, error) {
	// When we have param hints, try to bias toward suggested values.
	if guidance.hasParamHints {
		for paramName, suggestedVal := range guidance.paramHints {
			// Check if this parameter exists in the parent.
			if _, exists := parent.Params[paramName]; exists {
				child := parent.Clone()
				if child.Params == nil {
					child.Params = make(map[string]any)
				}
				child.Params[paramName] = suggestedVal
				child.MutationDesc = fmt.Sprintf("guided parameter %q set to %v (from hint)", paramName, suggestedVal)
				child.StrategyMutationType = MutationParameter
				return child, nil
			}
			// Also check if this is a known parameter in DefaultParamRanges.
			if _, known := DefaultParamRanges[paramName]; known {
				child := parent.Clone()
				if child.Params == nil {
					child.Params = make(map[string]any)
				}
				child.Params[paramName] = suggestedVal
				child.MutationDesc = fmt.Sprintf("guided parameter %q initialized to %v (from hint)", paramName, suggestedVal)
				child.StrategyMutationType = MutationParameter
				return child, nil
			}
		}
	}

	// Fall back to base parameter mutation.
	return m.base.mutateParameter(parent)
}

// parentTaskType extracts a task type identifier from the parent strategy.
// It checks Name first, then Params["task_type"], and falls back to "default".
func parentTaskType(parent *Strategy) string {
	if parent == nil {
		return "default"
	}
	if parent.Name != "" {
		return parent.Name
	}
	if parent.Params != nil {
		if t, ok := parent.Params["task_type"].(string); ok && t != "" {
			return t
		}
	}
	return "default"
}
