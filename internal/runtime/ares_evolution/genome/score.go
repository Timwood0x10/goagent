// Package genome provides score constants and helpers for strategy evaluation.
package genome

// ScoreUnevaluated is the sentinel value indicating a strategy has not been
// evaluated yet. All scoring functions must return this value (or any value < 0)
// for unevaluated strategies, and IsScoreEvaluated should be used to check
// evaluation status instead of direct comparison.
const ScoreUnevaluated = -1.0

// IsScoreEvaluated returns true if the given score represents an evaluated
// strategy (score >= 0). Use this helper instead of direct comparison with
// ScoreUnevaluated to avoid floating-point ambiguity when scorers return
// negative values like -0.5.
//
// Args:
//
//	score - the fitness score to check.
//
// Returns:
//
//	bool - true if the strategy has been evaluated, false otherwise.
func IsScoreEvaluated(score float64) bool {
	return score >= 0
}
