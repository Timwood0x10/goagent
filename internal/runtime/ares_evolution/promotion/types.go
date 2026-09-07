// Package promotion provides strategy promotion and demotion logic.
// This file defines types for tracking strategy lifecycle states across generations.
package promotion

import (
	"context"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/experience"
)

// StrategyState represents the lifecycle state of a strategy.
type StrategyState string

const (
	// StrategyStateCandidate indicates a new strategy that has not yet been validated.
	StrategyStateCandidate StrategyState = "candidate"

	// StrategyStateShadow indicates a strategy running in shadow mode, collecting evidence.
	StrategyStateShadow StrategyState = "shadow"

	// StrategyStateChampion indicates a strategy promoted to default strategy.
	StrategyStateChampion StrategyState = "champion"

	// StrategyStateDemoted indicates a former champion that is now deprecated.
	StrategyStateDemoted StrategyState = "demoted"

	// StrategyStateRetired indicates a strategy that is no longer used.
	StrategyStateRetired StrategyState = "retired"
)

// StrategyPromotionRecord represents a record of a strategy state transition.
// It captures the state change along with the evidence that triggered it.
type StrategyPromotionRecord struct {
	// StrategyID is the unique identifier of the strategy.
	StrategyID string `json:"strategy_id"`

	// State is the new state of the strategy.
	State StrategyState `json:"state"`

	// PreviousState is the state before this transition.
	PreviousState StrategyState `json:"previous_state"`

	// Generation is the evolution generation when this transition occurred.
	Generation int `json:"generation"`

	// Evidence is the evidence at the time of promotion or demotion.
	Evidence experience.Evidence `json:"evidence"`

	// Reason is a human-readable explanation for the state change.
	Reason string `json:"reason"`

	// Timestamp is when this state transition occurred.
	Timestamp time.Time `json:"timestamp"`
}

// ScoreSnapshot captures a strategy's score at a specific generation.
type ScoreSnapshot struct {
	// StrategyID is the unique identifier of the strategy.
	StrategyID string `json:"strategy_id"`

	// Score is the evidence score at this snapshot.
	Score float64 `json:"score"`

	// Generation is the evolution generation when this snapshot was taken.
	Generation int `json:"generation"`

	// Timestamp is when this snapshot was recorded.
	Timestamp time.Time `json:"timestamp"`
}

// PromotionCriteria defines the criteria for promoting or demoting strategies.
type PromotionCriteria struct {
	// MinSampleCount is the minimum number of samples to consider promotion.
	// Default: 100.
	MinSampleCount int `json:"min_sample_count"`

	// MinSuccessRate is the minimum success rate required for champion status.
	// Default: 0.85.
	MinSuccessRate float64 `json:"min_success_rate"`

	// MaxErrorRate is the maximum error rate allowed for champion status.
	// Default: 0.15.
	MaxErrorRate float64 `json:"max_error_rate"`

	// MaxLatencyP95 is the maximum P95 latency in milliseconds for champion status.
	// Default: 5000.
	MaxLatencyP95 int64 `json:"max_latency_p95"`

	// MinConfidence is the minimum confidence level required for promotion.
	// Default: 0.7.
	MinConfidence float64 `json:"min_confidence"`

	// ChampionHoldPeriod is the number of generations to hold champion status
	// before evaluating rolling improvement for retention. Default: 5.
	ChampionHoldPeriod int `json:"champion_hold_period"`

	// DemotionThreshold is the score drop threshold for demotion.
	// If the success rate drops by this amount, the strategy is demoted.
	// Default: 0.3.
	DemotionThreshold float64 `json:"demotion_threshold"`

	// CoolDownGenerations is the minimum generations between state transitions.
	// This prevents rapid promotion/demotion cycles.
	// Default: 3.
	CoolDownGenerations int `json:"cool_down_generations"`

	// MinAbsoluteImprovement is the minimum absolute score improvement required
	// for champion promotion from shadow state. Default: 0.5.
	MinAbsoluteImprovement float64 `json:"min_absolute_improvement"`

	// MinRollingImprovement is the minimum rolling average improvement required
	// for champion retention. Default: 0.1.
	MinRollingImprovement float64 `json:"min_rolling_improvement"`

	// ImprovementWindow is the number of generations to look back when computing
	// the rolling average improvement. Default: 3.
	ImprovementWindow int `json:"improvement_window"`

	// MaxChampionTenure is the maximum number of generations a champion can hold
	// its position without demonstrating improvement (rolling improvement >=
	// MinRollingImprovement). When this limit is exceeded, the champion is demoted
	// back to shadow to allow competing strategies a fair chance.
	// Default: 20 (generations).
	MaxChampionTenure int `json:"max_champion_tenure"`
}

// DefaultPromotionCriteria returns a PromotionCriteria with sensible defaults.
func DefaultPromotionCriteria() *PromotionCriteria {
	return &PromotionCriteria{
		MinSampleCount:         100,
		MinSuccessRate:         0.85,
		MaxErrorRate:           0.15,
		MaxLatencyP95:          5000,
		MinConfidence:          0.7,
		ChampionHoldPeriod:     5,
		DemotionThreshold:      0.3,
		CoolDownGenerations:    3,
		MinAbsoluteImprovement: 0.5,
		MinRollingImprovement:  0.1,
		ImprovementWindow:      3,
		MaxChampionTenure:      20,
	}
}

// StrategyInfo tracks the current state and metadata of a strategy.
type StrategyInfo struct {
	// StrategyID is the unique identifier of the strategy.
	StrategyID string `json:"strategy_id"`

	// TaskType is the type of task this strategy handles.
	TaskType string `json:"task_type"`

	// CurrentState is the current state of the strategy.
	CurrentState StrategyState `json:"current_state"`

	// Generation is the evolution generation when the state was last updated.
	Generation int `json:"generation"`

	// LastStateChange is when the state was last changed.
	LastStateChange time.Time `json:"last_state_change"`

	// ChampionSince is when the strategy became champion (if applicable).
	ChampionSince *time.Time `json:"champion_since,omitempty"`

	// GenerationCount counts how many generations the strategy has held its current state.
	GenerationCount int `json:"generation_count"`

	// ScoreHistory tracks recent evidence scores for rolling improvement calculation.
	ScoreHistory []ScoreSnapshot `json:"score_history,omitempty"`

	// BaselineScore is the evidence score when the strategy entered its current state.
	// Used to measure absolute improvement for promotion decisions.
	BaselineScore float64 `json:"baseline_score"`
}

// PromotionLogic defines the interface for strategy promotion and demotion.
type PromotionLogic interface {
	// Evaluate evaluates a strategy's evidence and determines its appropriate state.
	// Returns the recommended state, a human-readable reason, and any error.
	Evaluate(ctx context.Context, strategyID string, evidence experience.Evidence) (StrategyState, string, error)

	// Promote promotes a strategy to the next higher state.
	// Returns an error if the promotion is not allowed.
	Promote(ctx context.Context, strategyID string) error

	// Demote demotes a strategy to a lower state with the given reason.
	// Returns an error if the demotion is not allowed.
	Demote(ctx context.Context, strategyID string, reason string) error

	// GetHistory returns the promotion history for a strategy.
	GetHistory(ctx context.Context, strategyID string) ([]StrategyPromotionRecord, error)

	// GetCurrentState returns the current state of a strategy.
	// Returns StrategyStateCandidate if the strategy has never been tracked.
	GetCurrentState(ctx context.Context, strategyID string) (StrategyState, error)

	// GetChampions returns all strategies currently in champion state for a task type.
	GetChampions(ctx context.Context, taskType string) ([]StrategyInfo, error)

	// GetStrategyInfo returns detailed information about a strategy.
	GetStrategyInfo(ctx context.Context, strategyID string) (*StrategyInfo, error)
}

// PromotionDecision represents the result of evaluating a strategy for promotion.
type PromotionDecision struct {
	// StrategyID is the strategy being evaluated.
	StrategyID string `json:"strategy_id"`

	// CurrentState is the current state of the strategy.
	CurrentState StrategyState `json:"current_state"`

	// RecommendedState is the recommended state based on evidence.
	RecommendedState StrategyState `json:"recommended_state"`

	// ShouldPromote indicates if the strategy should be promoted.
	ShouldPromote bool `json:"should_promote"`

	// ShouldDemote indicates if the strategy should be demoted.
	ShouldDemote bool `json:"should_demote"`

	// Reason is the human-readable reason for the recommendation.
	Reason string `json:"reason"`

	// Score is the overall score used for the decision.
	Score float64 `json:"score"`

	// Evidence is the evidence used for the decision.
	Evidence experience.Evidence `json:"evidence"`
}

// IsValid returns true if the StrategyState is a valid state.
func (s StrategyState) IsValid() bool {
	switch s {
	case StrategyStateCandidate, StrategyStateShadow, StrategyStateChampion, StrategyStateDemoted, StrategyStateRetired:
		return true
	default:
		return false
	}
}

// String returns the string representation of StrategyState.
func (s StrategyState) String() string {
	return string(s)
}

// CanPromoteTo returns true if this state can be promoted to the target state.
func (s StrategyState) CanPromoteTo(target StrategyState) bool {
	transitions := map[StrategyState][]StrategyState{
		StrategyStateCandidate: {StrategyStateShadow},
		StrategyStateShadow:    {StrategyStateChampion},
		StrategyStateChampion:  {},
		StrategyStateDemoted:   {StrategyStateShadow},
		StrategyStateRetired:   {},
	}

	allowed, exists := transitions[s]
	if !exists {
		return false
	}

	for _, allowedTarget := range allowed {
		if allowedTarget == target {
			return true
		}
	}
	return false
}

// CanDemoteTo returns true if this state can be demoted to the target state.
func (s StrategyState) CanDemoteTo(target StrategyState) bool {
	transitions := map[StrategyState][]StrategyState{
		StrategyStateCandidate: {StrategyStateRetired},
		StrategyStateShadow:    {StrategyStateDemoted, StrategyStateRetired},
		StrategyStateChampion:  {StrategyStateDemoted},
		StrategyStateDemoted:   {StrategyStateRetired},
		StrategyStateRetired:   {},
	}

	allowed, exists := transitions[s]
	if !exists {
		return false
	}

	for _, allowedTarget := range allowed {
		if allowedTarget == target {
			return true
		}
	}
	return false
}

// IsEmpty returns true if the StrategyPromotionRecord has no meaningful data.
func (r *StrategyPromotionRecord) IsEmpty() bool {
	return r.StrategyID == ""
}

// IsEmpty returns true if the StrategyInfo has no meaningful data.
func (i *StrategyInfo) IsEmpty() bool {
	return i.StrategyID == ""
}

// MeetsPromotionCriteria checks if the evidence meets the promotion criteria.
func MeetsPromotionCriteria(e experience.Evidence, criteria *PromotionCriteria) bool {
	if criteria == nil {
		criteria = DefaultPromotionCriteria()
	}

	if e.SampleCount < int64(criteria.MinSampleCount) {
		return false
	}

	if e.SuccessRate < criteria.MinSuccessRate {
		return false
	}

	if e.ErrorRate > criteria.MaxErrorRate {
		return false
	}

	if e.LatencyP95 > criteria.MaxLatencyP95 {
		return false
	}

	if e.Confidence < criteria.MinConfidence {
		return false
	}

	return true
}

// CalculateEvidenceScore computes an overall score for the evidence.
// Higher scores indicate better performance.
func CalculateEvidenceScore(e experience.Evidence) float64 {
	// Weight factors for different metrics
	const (
		successRateWeight = 0.4
		errorRateWeight   = 0.3
		confidenceWeight  = 0.2
		latencyWeight     = 0.1
	)

	// Normalize latency (assume max latency of 10 seconds)
	normalizedLatency := 1.0 - float64(e.LatencyP95)/10000.0
	if normalizedLatency < 0 {
		normalizedLatency = 0
	}

	// Calculate weighted score
	score := (e.SuccessRate * successRateWeight) +
		((1.0 - e.ErrorRate) * errorRateWeight) +
		(e.Confidence * confidenceWeight) +
		(normalizedLatency * latencyWeight)

	return score
}
