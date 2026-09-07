// Package genome provides score evaluation helpers for evolution.
package genome

// ScoreUnevaluated is the sentinel value indicating a strategy has not been
// evaluated yet. Use IsScoreEvaluated to check instead of direct comparison.
const ScoreUnevaluated = -1.0

// IsScoreEvaluated returns true if the given score represents an evaluated
// strategy (score >= 0). Use this helper instead of direct comparison with
// ScoreUnevaluated to avoid floating-point ambiguity.
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
