package mutation

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
)

// MutationOutcome tracks the cumulative performance statistics for a specific
// mutation type. It records attempts, wins, score deltas, and cost deltas to
// drive adaptive probability adjustments.
type MutationOutcome struct {
	// Attempts is the total number of times this mutation type was selected.
	Attempts int `json:"attempts"`

	// Wins is the number of times this mutation type led to improvement.
	Wins int `json:"wins"`

	// AvgScoreDelta is the average score change per attempt (positive =
	// improvement, negative = regression).
	AvgScoreDelta float64 `json:"avg_score_delta"`

	// AvgCostDelta is the average cost change per attempt (positive = more
	// expensive, negative = cheaper).
	AvgCostDelta float64 `json:"avg_cost_delta"`
}

// AdaptiveDistributionConfig holds configuration for adaptive mutation
// probability distribution.
type AdaptiveDistributionConfig struct {
	// Enabled enables adaptive probability adjustment.
	Enabled bool `json:"enabled"`

	// MinParamProb is the minimum probability for parameter mutation (default 0.30).
	MinParamProb float64 `json:"min_param_prob"`

	// MaxParamProb is the maximum probability for parameter mutation (default 0.90).
	MaxParamProb float64 `json:"max_param_prob"`

	// MinPromptProb is the minimum probability for prompt mutation (default 0.05).
	MinPromptProb float64 `json:"min_prompt_prob"`

	// MaxPromptProb is the maximum probability for prompt mutation (default 0.50).
	MaxPromptProb float64 `json:"max_prompt_prob"`

	// MinToolProb is the minimum probability for tool mutation (default 0.05).
	MinToolProb float64 `json:"min_tool_prob"`

	// MaxToolProb is the maximum probability for tool mutation (default 0.50).
	MaxToolProb float64 `json:"max_tool_prob"`

	// ExplorationFloor is the minimum exploration probability for any mutation
	// type, preventing early noise from collapsing exploration (default 0.03).
	ExplorationFloor float64 `json:"exploration_floor"`

	// WinRateWindow is the number of recent attempts to consider when computing
	// win rate for probability adjustments (0 = all, default 20).
	WinRateWindow int `json:"win_rate_window"`

	// MinAttemptsBeforeAdjust is the minimum total attempts across all mutation
	// types before probability adjustments begin. This prevents early noise from
	// collapsing exploration before sufficient data has been collected.
	// Default is 5. Set to 0 to disable (adjust immediately).
	MinAttemptsBeforeAdjust int `json:"min_attempts_before_adjust"`

	// LearningRate controls how quickly probabilities adjust to outcomes
	// (default 0.1). Higher values adapt faster but may overfit to noise.
	LearningRate float64 `json:"learning_rate"`
}

// DefaultAdaptiveDistributionConfig returns sensible defaults for adaptive
// mutation distribution configuration.
func DefaultAdaptiveDistributionConfig() AdaptiveDistributionConfig {
	return AdaptiveDistributionConfig{
		Enabled:                 false,
		MinParamProb:            0.30,
		MaxParamProb:            0.90,
		MinPromptProb:           0.05,
		MaxPromptProb:           0.50,
		MinToolProb:             0.05,
		MaxToolProb:             0.50,
		ExplorationFloor:        0.03,
		WinRateWindow:           20,
		LearningRate:            0.10,
		MinAttemptsBeforeAdjust: 5,
	}
}

// AdaptiveDistribution wraps a Mutator and adjusts mutation type probabilities
// based on observed outcomes. Repeatedly successful mutation types gain weight
// within configured bounds; repeated failures reduce weight without dropping
// below the exploration floor.
type AdaptiveDistribution struct {
	mutator *Mutator
	cfg     AdaptiveDistributionConfig

	mu sync.RWMutex

	// outcomes tracks cumulative statistics per mutation type.
	outcomes map[MutationType]*MutationOutcome

	// current probabilities for each mutation type (when all pools available).
	paramProb  float64
	promptProb float64
	toolProb   float64
}

// NewAdaptiveDistribution creates a new AdaptiveDistribution wrapping the given
// mutator with the specified configuration.
//
// Args:
//
//	m - the base mutator to wrap (must not be nil).
//	cfg - the adaptive distribution configuration (use
//	  DefaultAdaptiveDistributionConfig() for defaults).
//
// Returns:
//
//	*AdaptiveDistribution - the configured adaptive distribution instance.
//	error - non-nil if mutator is nil or configuration is invalid.
func NewAdaptiveDistribution(m *Mutator, cfg AdaptiveDistributionConfig) (*AdaptiveDistribution, error) {
	if m == nil {
		return nil, errors.New("mutator must not be nil")
	}

	// Validate bounds.
	if cfg.MinParamProb < 0 || cfg.MaxParamProb > 1 || cfg.MinParamProb > cfg.MaxParamProb {
		return nil, fmt.Errorf("invalid param probability bounds: min=%f, max=%f",
			cfg.MinParamProb, cfg.MaxParamProb)
	}
	if cfg.MinPromptProb < 0 || cfg.MaxPromptProb > 1 || cfg.MinPromptProb > cfg.MaxPromptProb {
		return nil, fmt.Errorf("invalid prompt probability bounds: min=%f, max=%f",
			cfg.MinPromptProb, cfg.MaxPromptProb)
	}
	if cfg.MinToolProb < 0 || cfg.MaxToolProb > 1 || cfg.MinToolProb > cfg.MaxToolProb {
		return nil, fmt.Errorf("invalid tool probability bounds: min=%f, max=%f",
			cfg.MinToolProb, cfg.MaxToolProb)
	}
	if cfg.ExplorationFloor < 0 || cfg.ExplorationFloor > 1 {
		return nil, fmt.Errorf("exploration floor must be in [0, 1], got %f",
			cfg.ExplorationFloor)
	}
	if cfg.LearningRate <= 0 || cfg.LearningRate > 1 {
		return nil, fmt.Errorf("learning rate must be in (0, 1], got %f",
			cfg.LearningRate)
	}

	ad := &AdaptiveDistribution{
		mutator: m,
		cfg:     cfg,
		outcomes: map[MutationType]*MutationOutcome{
			MutationParameter: {},
			MutationPrompt:    {},
			MutationTool:      {},
		},
		paramProb:  0.70,
		promptProb: 0.15,
		toolProb:   0.15,
	}

	return ad, nil
}

// Mutate generates n mutated child strategies using adaptive probabilities.
// It delegates to the underlying mutator's mutateOneWithProbs method with
// the current adaptive distribution.
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
//	error - ErrNilParent if parent is nil, ErrInvalidCount if n <= 0,
//	  or delegating error from the underlying mutator.
func (ad *AdaptiveDistribution) Mutate(ctx context.Context, parent *Strategy, n int) ([]*Strategy, error) {
	if parent == nil {
		return nil, ErrNilParent
	}
	if n <= 0 {
		return nil, ErrInvalidCount
	}

	paramProb, promptProb, toolProb := ad.CurrentProbabilities()

	children := make([]*Strategy, 0, n)
	for i := 0; i < n; i++ {
		select {
		case <-ctx.Done():
			return children, ctx.Err()
		default:
		}

		child, err := ad.mutator.mutateOneWithProbs(parent, i, paramProb, promptProb, toolProb)
		if err != nil {
			return nil, fmt.Errorf("adaptive mutate child %d: %w", i, err)
		}
		children = append(children, child)
	}

	return children, nil
}

// RecordOutcome feeds back the result of a mutation, updating the adaptive
// probability distribution. Call this after evaluating a child strategy to
// provide feedback for future probability adjustments.
//
// If adaptive distribution is not enabled, this is a no-op.
//
// Args:
//
//	t - the mutation type that was applied.
//	scoreDelta - the score difference (child score - parent score).
//	costDelta - the cost difference (child cost - parent cost).
//	won - true if the child outperformed the parent.
func (ad *AdaptiveDistribution) RecordOutcome(t MutationType, scoreDelta float64, costDelta float64, won bool) {
	if !ad.cfg.Enabled {
		return
	}

	ad.mu.Lock()
	defer ad.mu.Unlock()

	outcome, ok := ad.outcomes[t]
	if !ok {
		return
	}

	outcome.Attempts++
	if won {
		outcome.Wins++
	}

	// Update running average for score delta.
	n := float64(outcome.Attempts)
	outcome.AvgScoreDelta = outcome.AvgScoreDelta*(n-1)/n + scoreDelta/n

	// Update running average for cost delta.
	outcome.AvgCostDelta = outcome.AvgCostDelta*(n-1)/n + costDelta/n

	ad.adjustProbabilitiesLocked()
}

// CurrentProbabilities returns the current mutation type probabilities.
// The returned values are the probabilities when all pools are available;
// the adaptive distribution is always relative (naturally handling empty pools).
//
// Returns:
//
//	float64 - parameter mutation probability [0, 1].
//	float64 - prompt mutation probability [0, 1].
//	float64 - tool mutation probability [0, 1].
func (ad *AdaptiveDistribution) CurrentProbabilities() (float64, float64, float64) {
	ad.mu.RLock()
	defer ad.mu.RUnlock()

	return ad.paramProb, ad.promptProb, ad.toolProb
}

// Outcomes returns a snapshot of the current outcome statistics per mutation
// type. The returned map is a copy for thread safety.
//
// Returns:
//
//	map[MutationType]MutationOutcome - copy of current outcome stats.
func (ad *AdaptiveDistribution) Outcomes() map[MutationType]MutationOutcome {
	ad.mu.RLock()
	defer ad.mu.RUnlock()

	result := make(map[MutationType]MutationOutcome, len(ad.outcomes))
	for t, o := range ad.outcomes {
		result[t] = *o
	}
	return result
}

// Report returns a human-readable summary of the current adaptive distribution
// state, including probabilities and outcome statistics for each mutation type.
//
// Returns:
//
//	string - formatted report text.
func (ad *AdaptiveDistribution) Report() string {
	ad.mu.RLock()
	defer ad.mu.RUnlock()

	p, pr, pt := ad.paramProb, ad.promptProb, ad.toolProb
	report := "=== Adaptive Mutation Distribution ===\n"
	report += fmt.Sprintf("Probabilities: parameter=%.1f%%, prompt=%.1f%%, tool=%.1f%%\n",
		p*100, pr*100, pt*100)
	report += fmt.Sprintf("Bounds: param=[%.0f%%, %.0f%%], prompt=[%.0f%%, %.0f%%], tool=[%.0f%%, %.0f%%]\n",
		ad.cfg.MinParamProb*100, ad.cfg.MaxParamProb*100,
		ad.cfg.MinPromptProb*100, ad.cfg.MaxPromptProb*100,
		ad.cfg.MinToolProb*100, ad.cfg.MaxToolProb*100)
	report += fmt.Sprintf("Exploration floor: %.1f%%\n", ad.cfg.ExplorationFloor*100)
	report += fmt.Sprintf("Learning rate: %.2f\n\n", ad.cfg.LearningRate)

	report += "Outcomes:\n"
	for _, mt := range []MutationType{MutationParameter, MutationPrompt, MutationTool} {
		o := ad.outcomes[mt]
		if o.Attempts > 0 {
			winRate := float64(o.Wins) / float64(o.Attempts) * 100
			report += fmt.Sprintf("  %s: attempts=%d, wins=%d, win_rate=%.1f%%, "+
				"avg_score_delta=%+.4f, avg_cost_delta=%+.4f\n",
				mt, o.Attempts, o.Wins, winRate,
				o.AvgScoreDelta, o.AvgCostDelta)
		} else {
			report += fmt.Sprintf("  %s: no attempts yet\n", mt)
		}
	}

	return report
}

// adjustProbabilitiesLocked updates the current probabilities based on observed
// outcome statistics. Caller must hold ad.mu write lock.
//
// Algorithm:
//  1. Compute win rate for each mutation type (capped by WinRateWindow).
//  2. Adjust each probability toward the direction indicated by its win rate.
//  3. Parameter mutation is the safe fallback: it trends toward 0.70 by default.
//  4. Enforce exploration floor to prevent any type from being completely starved.
//  5. Normalize so the three probabilities sum to exactly 1.0 while keeping each
//     within its configured [min, max] bounds (bounded proportional fitting).
func (ad *AdaptiveDistribution) adjustProbabilitiesLocked() {
	totalAttempts := 0
	for _, o := range ad.outcomes {
		totalAttempts += o.Attempts
	}
	if totalAttempts == 0 {
		return
	}

	// Wait until enough data has been collected before adjusting probabilities.
	// This prevents early noise from collapsing exploration before sufficient
	// evidence has accumulated.
	if ad.cfg.MinAttemptsBeforeAdjust > 0 && totalAttempts < ad.cfg.MinAttemptsBeforeAdjust {
		return
	}

	// Map of mutation type to its win rate.
	winRates := make(map[MutationType]float64, 3)
	for mt, o := range ad.outcomes {
		if o.Attempts > 0 {
			winRates[mt] = float64(o.Wins) / float64(o.Attempts)
		} else {
			winRates[mt] = 0.5 // Neutral starting point.
		}
	}

	lr := ad.cfg.LearningRate
	floor := ad.cfg.ExplorationFloor

	// Parameter mutation adjustment: trends toward 0.70.
	// Positive score deltas increase param weight (safe bet), negative deltas
	// decrease it (favor exploration).
	baseParam := 0.70
	paramOutcome := ad.outcomes[MutationParameter]
	if paramOutcome.Attempts > 0 {
		paramSignal := (winRates[MutationParameter] - 0.5) * 2.0 // [-1, 1]
		ad.paramProb += lr * paramSignal * (ad.cfg.MaxParamProb - ad.cfg.MinParamProb)
		// Drift toward base.
		ad.paramProb += lr * (baseParam - ad.paramProb) * 0.5
	}

	// Prompt mutation adjustment: increases when prompt-guided candidates
	// improve scores.
	promptOutcome := ad.outcomes[MutationPrompt]
	if promptOutcome.Attempts > 0 {
		promptSignal := (winRates[MutationPrompt] - 0.5) * 2.0 // [-1, 1]
		ad.promptProb += lr * promptSignal * (ad.cfg.MaxPromptProb - ad.cfg.MinPromptProb)
		// Boost on positive avg score delta.
		if promptOutcome.AvgScoreDelta > 0 {
			ad.promptProb += lr * math.Min(promptOutcome.AvgScoreDelta*0.1, 0.1)
		}
	}

	// Tool mutation adjustment: decreases when tool mutations increase
	// failure rate or latency.
	toolOutcome := ad.outcomes[MutationTool]
	if toolOutcome.Attempts > 0 {
		toolSignal := (winRates[MutationTool] - 0.5) * 2.0 // [-1, 1]
		ad.toolProb += lr * toolSignal * (ad.cfg.MaxToolProb - ad.cfg.MinToolProb)
		// Penalty for high cost delta (latency/cost increase).
		if toolOutcome.AvgCostDelta > 0 {
			ad.toolProb -= lr * math.Min(toolOutcome.AvgCostDelta*0.05, 0.05)
		}
	}

	// Apply exploration floor.
	ad.paramProb = math.Max(ad.paramProb, floor)
	ad.promptProb = math.Max(ad.promptProb, floor)
	ad.toolProb = math.Max(ad.toolProb, floor)

	// Normalize to sum to 1.0 while keeping each probability within its
	// configured [min, max] bounds. This replaces the previous "clamp then
	// normalize" sequence, which could not satisfy both invariants at once:
	// normalization can push a clamped value above its max (when the
	// pre-normalization sum is below 1.0), and re-clamping after normalization
	// breaks the sum-to-1.0 invariant.
	ad.normalizeBoundedLocked()

	log.Debug("adaptive distribution adjusted",
		"param_prob", ad.paramProb,
		"prompt_prob", ad.promptProb,
		"tool_prob", ad.toolProb,
	)
}

// normalizeBoundedLocked scales the three probabilities so they sum to exactly
// 1.0 while respecting each one's configured [min, max] bounds. It uses
// iterative proportional fitting:
//  1. Clamp each value to its bounds.
//  2. If the sum already equals 1.0, stop.
//  3. If the sum exceeds 1.0, subtract the surplus proportionally from values
//     still above their min, clamping at the min.
//  4. If the sum is below 1.0, add the deficit proportionally to values still
//     below their max, clamping at the max.
//  5. Repeat until the sum is 1.0 (within floating-point tolerance) or no
//     further adjustment is possible.
//
// Because the redistribution share is computed from the available room, a
// value can never be pushed past its bound, so a single pass is exact in real
// arithmetic; extra iterations guard against floating-point drift.
//
// Caller must hold ad.mu write lock.
func (ad *AdaptiveDistribution) normalizeBoundedLocked() {
	const epsilon = 1e-9

	type probSlot struct {
		val *float64
		min float64
		max float64
	}
	slots := []probSlot{
		{&ad.paramProb, ad.cfg.MinParamProb, ad.cfg.MaxParamProb},
		{&ad.promptProb, ad.cfg.MinPromptProb, ad.cfg.MaxPromptProb},
		{&ad.toolProb, ad.cfg.MinToolProb, ad.cfg.MaxToolProb},
	}

	// Step 1: clamp each value to its configured bounds.
	for _, s := range slots {
		*s.val = Clamp(*s.val, s.min, s.max)
	}

	for iter := 0; iter < len(slots); iter++ {
		total := 0.0
		for _, s := range slots {
			total += *s.val
		}
		if math.Abs(total-1.0) < epsilon {
			return
		}

		if total > 1.0 {
			// Distribute surplus among values still above their min.
			surplus := total - 1.0
			movable := 0.0
			for _, s := range slots {
				if *s.val > s.min+epsilon {
					movable += *s.val - s.min
				}
			}
			if movable <= epsilon {
				return // cannot reduce further without breaching a min.
			}
			reduce := math.Min(surplus, movable)
			for _, s := range slots {
				if *s.val > s.min+epsilon {
					share := (*s.val - s.min) / movable
					*s.val = Clamp(*s.val-reduce*share, s.min, s.max)
				}
			}
		} else { // total < 1.0
			// Distribute deficit among values still below their max.
			deficit := 1.0 - total
			movable := 0.0
			for _, s := range slots {
				if *s.val < s.max-epsilon {
					movable += s.max - *s.val
				}
			}
			if movable <= epsilon {
				return // cannot grow further without breaching a max.
			}
			grow := math.Min(deficit, movable)
			for _, s := range slots {
				if *s.val < s.max-epsilon {
					share := (s.max - *s.val) / movable
					*s.val = Clamp(*s.val+grow*share, s.min, s.max)
				}
			}
		}
	}
}

// Clamp restricts a value to the [min, max] range.
func Clamp(v, min, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
