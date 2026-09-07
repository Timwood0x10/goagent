package genome

import (
	"context"
	"fmt"
	"strings"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// Target type constants
const (
	TargetTypeParam          = "param"
	TargetTypePrompt         = "prompt"
	TargetTypeTool           = "tool"
	TargetTypePromptTemplate = "prompt_template"
)

// Direction constants
const (
	DirectionIncrease    = "increase"
	DirectionDecrease    = "decrease"
	DirectionSwap        = "swap"
	DirectionRestructure = "restructure"
)

// Parameter key constants
const (
	ParamTemperature = "temperature"
	ParamMaxTokens   = "max_tokens"
	ParamMaxSteps    = "max_steps"
	ParamTopK        = "top_k"
)

// Special values
const (
	RootLineageID          = "(root)"
	HypothesisNoHypotheses = "no hypotheses"
)

// Metric constants
const (
	MetricSuccessRate = "success_rate"
	MetricQuality     = "quality"
	MetricCost        = "cost"
	MetricLatency     = "latency"
)

// Selection method constants
const (
	SelectionRank        = "rank"
	SelectionLineageRank = "lineage_rank"
	SelectionSUS         = "sus"
	SelectionRoulette    = "roulette"
)

// Diversity seed type
const (
	DiversitySeedPrompt = "prompt_diversity_seed"
)

// MutationHypothesis represents a testable hypothesis about what mutation
// would improve strategy performance. It bridges LLM reflection into
// concrete mutation guidance for the genetic algorithm.
type MutationHypothesis struct {
	// TargetType is the type of mutation target: "param", "prompt", "tool".
	TargetType string `json:"target_type"`

	// TargetKey is the specific parameter name or "prompt_template" or tool name.
	TargetKey string `json:"target_key"`

	// SuggestedValue is the recommended new value (nil = remove/randomize).
	SuggestedValue any `json:"suggested_value,omitempty"`

	// Direction indicates the change direction: "increase", "decrease", "swap", "restructure".
	Direction string `json:"direction"`

	// Rationale explains why this change is expected to help.
	Rationale string `json:"rationale"`

	// Confidence in this hypothesis [0, 1].
	Confidence float64 `json:"confidence"`

	// SourceReflectionID tracks which reflection generated this hypothesis.
	SourceReflectionID string `json:"source_reflection_id,omitempty"`
}

// HypothesisGenerator converts reflections into testable mutation hypotheses.
type HypothesisGenerator struct {
	minConfidence float64
}

// NewHypothesisGenerator creates a hypothesis generator with the given minimum
// confidence threshold. Hypotheses below this threshold are filtered out.
func NewHypothesisGenerator(minConfidence float64) *HypothesisGenerator {
	if minConfidence <= 0 {
		minConfidence = 0.3
	}
	return &HypothesisGenerator{minConfidence: minConfidence}
}

// Generate converts reflections into mutation hypotheses.
// Returns all hypotheses that meet the confidence threshold.
func (hg *HypothesisGenerator) Generate(ctx context.Context, ref *Reflection) []MutationHypothesis {
	if ref == nil || len(ref.Recommendations) == 0 {
		return nil
	}

	hypotheses := make([]MutationHypothesis, 0, len(ref.Recommendations))
	for _, rec := range ref.Recommendations {
		if rec.Confidence < hg.minConfidence {
			continue
		}
		hyp := hg.recommendationToHypothesis(rec)
		if hyp != nil {
			hypotheses = append(hypotheses, *hyp)
		}
	}

	el.DebugContext(ctx, "generated mutation hypotheses from reflection",
		"total_recommendations", len(ref.Recommendations),
		"accepted", len(hypotheses),
		"min_confidence", hg.minConfidence,
	)
	return hypotheses
}

// recommendationToHypothesis converts a single Recommendation to a MutationHypothesis.
func (hg *HypothesisGenerator) recommendationToHypothesis(rec Recommendation) *MutationHypothesis {
	hyp := &MutationHypothesis{
		Direction:  rec.Action,
		Rationale:  rec.Rationale,
		Confidence: rec.Confidence,
	}

	// Parse target to determine type and key.
	target := rec.Target
	switch {
	case strings.HasPrefix(target, "param:"):
		hyp.TargetType = TargetTypeParam
		hyp.TargetKey = strings.TrimPrefix(target, "param:")
	case target == "prompt" || target == TargetTypePromptTemplate:
		hyp.TargetType = TargetTypePrompt
		hyp.TargetKey = TargetTypePromptTemplate
		hyp.Direction = DirectionRestructure
	case strings.HasPrefix(target, "tool:"):
		hyp.TargetType = TargetTypeTool
		hyp.TargetKey = strings.TrimPrefix(target, "tool:")
	default:
		// Generic target: try to infer from action.
		switch rec.Action {
		case DirectionIncrease, DirectionDecrease:
			hyp.TargetType = TargetTypeParam
			hyp.TargetKey = target
		case "swap":
			hyp.TargetType = TargetTypeTool
			hyp.TargetKey = target
		case DirectionRestructure:
			hyp.TargetType = TargetTypePrompt
			hyp.TargetKey = TargetTypePromptTemplate
		default:
			hyp.TargetType = TargetTypeParam
			hyp.TargetKey = target
		}
	}

	// Map direction to standard forms.
	switch strings.ToLower(hyp.Direction) {
	case "increase", "decrease", "swap", "restructure", "replace", "remove":
		// Valid direction.
	default:
		hyp.Direction = "replace"
	}

	return hyp
}

// ApplyHypothesis applies a hypothesis to a strategy, producing a mutated clone.
// Returns nil if the hypothesis cannot be applied to the given strategy.
func ApplyHypothesis(base *mutation.Strategy, hyp MutationHypothesis) *mutation.Strategy {
	if base == nil {
		return nil
	}
	clone := base.Clone()

	switch hyp.TargetType {
	case "param":
		if hyp.SuggestedValue != nil {
			if v, ok := hyp.SuggestedValue.(float64); ok {
				clone.Params[hyp.TargetKey] = clampParam(hyp.TargetKey, v)
			} else {
				clone.Params[hyp.TargetKey] = hyp.SuggestedValue
			}
		} else {
			// Apply direction-based change to numeric params.
			if v, ok := clone.Params[hyp.TargetKey].(float64); ok {
				switch hyp.Direction {
				case "increase":
					clone.Params[hyp.TargetKey] = clampParam(hyp.TargetKey, v*1.3)
				case "decrease":
					clone.Params[hyp.TargetKey] = clampParam(hyp.TargetKey, v*0.7)
				}
			} else {
				el.DebugContext(context.Background(), "ApplyHypothesis: skipping non-float64 param",
					"key", hyp.TargetKey, "direction", hyp.Direction,
				)
			}
		}
	case "prompt":
		if hyp.SuggestedValue != nil {
			if s, ok := hyp.SuggestedValue.(string); ok {
				clone.PromptTemplate = s
			}
		}
	case "tool":
		if hyp.SuggestedValue != nil {
			if s, ok := hyp.SuggestedValue.(string); ok {
				clone.Params["tools"] = s
			}
		}
	}

	clone.Score = -1
	clone.MutationDesc = fmt.Sprintf("hypothesis: %s (%.0f%%)", hyp.Rationale, hyp.Confidence*100)
	return clone
}

// paramRanges defines known parameter bounds (min, max) for common LLM parameters.
// Unknown params default to a wide range [0, 10000] to avoid clamping integer params
// like max_steps (5-20) or memory_limit (3-10) to overly small values.
var paramRanges = map[string][2]float64{
	"temperature":        {0, 2.0},
	"top_k":              {1, 100},
	"topK":               {1, 100},
	"topk":               {1, 100},
	"top_p":              {0, 1.0},
	"topP":               {0, 1.0},
	"topp":               {0, 1.0},
	"max_tokens":         {1, 32768},
	"maxTokens":          {1, 32768},
	"maxtokens":          {1, 32768},
	"max_steps":          {1, 100},
	"maxSteps":           {1, 100},
	"memory_limit":       {1, 100},
	"memoryLimit":        {1, 100},
	"conflict_threshold": {0, 1.0},
	"conflictThreshold":  {0, 1.0},
}

// clampParam bounds a parameter value to its known valid range.
// Unknown params use a wide default [0, 10000] to accommodate integer params.
// Note: parameters with range starting at 0 (temperature, top_p) will never
// reach exactly 0 — any value ≤ 0 is lifted to epsilon (0.0001). This is
// intentional: some LLM providers reject temperature=0 as invalid, and a
// near-zero value is effectively deterministic for practical purposes.
func clampParam(key string, v float64) float64 {
	const epsilon = 0.0001
	r, ok := paramRanges[key]
	if !ok {
		r = [2]float64{0, 10000}
	}
	if v <= 0 || v < r[0] {
		if r[0] > 0 {
			return r[0]
		}
		return epsilon
	}
	if v > 0 && v < epsilon {
		return epsilon
	}
	if v > r[1] {
		return r[1]
	}
	return v
}

// FormatHypotheses formats a set of hypotheses as a diagnostic string.
func FormatHypotheses(hypotheses []MutationHypothesis) string {
	if len(hypotheses) == 0 {
		return "no hypotheses"
	}
	parts := make([]string, 0, len(hypotheses))
	for _, h := range hypotheses {
		parts = append(parts, fmt.Sprintf("%s:%s → %s (%.0f%%)",
			h.TargetType, h.TargetKey, h.Direction, h.Confidence*100))
	}
	return strings.Join(parts, "; ")
}
