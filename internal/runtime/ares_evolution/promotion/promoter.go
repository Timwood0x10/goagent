// Package promotion provides strategy promotion and demotion logic.
// This file implements the DefaultPromoter which manages strategy lifecycle states.
package promotion

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/logger"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/experience"
)

var el = logger.New("promotion")

var (
	// ErrStrategyNotFound indicates the strategy does not exist.
	ErrStrategyNotFound = errors.New("strategy not found")

	// ErrInvalidStateTransition indicates an invalid state transition.
	ErrInvalidStateTransition = errors.New("invalid state transition")

	// ErrCoolDownActive indicates the strategy is in cool-down period.
	ErrCoolDownActive = errors.New("cool-down period active")

	// ErrInsufficientEvidence indicates insufficient evidence for promotion.
	ErrInsufficientEvidence = errors.New("insufficient evidence for promotion")
)

// DefaultPromoter implements the PromotionLogic interface.
// It manages strategy states across generations with thread-safe operations.
type DefaultPromoter struct {
	// mu protects all internal state.
	mu sync.RWMutex

	// criteria defines the promotion and demotion criteria.
	criteria *PromotionCriteria

	// strategies maps strategyID to StrategyInfo.
	strategies map[string]*StrategyInfo

	// history maps strategyID to a slice of promotion records.
	history map[string][]StrategyPromotionRecord

	// champions maps taskType to a slice of champion strategy IDs.
	champions map[string][]string

	// currentGeneration is the current evolution generation.
	currentGeneration int
}

// NewDefaultPromoter creates a new DefaultPromoter with the given criteria.
// If criteria is nil, default criteria are used.
func NewDefaultPromoter(criteria *PromotionCriteria) *DefaultPromoter {
	if criteria == nil {
		criteria = DefaultPromotionCriteria()
	}

	return &DefaultPromoter{
		criteria:          criteria,
		strategies:        make(map[string]*StrategyInfo),
		history:           make(map[string][]StrategyPromotionRecord),
		champions:         make(map[string][]string),
		currentGeneration: 0,
	}
}

// Evaluate evaluates a strategy's evidence and determines its appropriate state.
// Returns the recommended state, a human-readable reason, and any error.
func (p *DefaultPromoter) Evaluate(
	ctx context.Context,
	strategyID string,
	evidence experience.Evidence,
) (StrategyState, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Get or create strategy info
	info, exists := p.strategies[strategyID]

	// Calculate score and record in ScoreHistory
	score := CalculateEvidenceScore(evidence)

	if !exists {
		info = &StrategyInfo{
			StrategyID:      strategyID,
			TaskType:        evidence.TaskType,
			CurrentState:    StrategyStateCandidate,
			Generation:      p.currentGeneration,
			LastStateChange: time.Now(),
			BaselineScore:   score,
		}
		p.strategies[strategyID] = info
	}

	// Record score snapshot in history.
	snapshot := ScoreSnapshot{
		StrategyID: strategyID,
		Score:      score,
		Generation: p.currentGeneration,
		Timestamp:  time.Now(),
	}
	info.ScoreHistory = append(info.ScoreHistory, snapshot)

	// Cap history to prevent unbounded growth (keep last 20 entries).
	if len(info.ScoreHistory) > 20 {
		info.ScoreHistory = info.ScoreHistory[len(info.ScoreHistory)-20:]
	}

	// Periodic sweep of long-retired strategies (#56).
	p.pruneRetiredLocked(time.Now())

	// Set baseline score when entering shadow state for the first time.
	// This captures the score at the point the strategy became shadow
	// and is used to measure absolute improvement for promotion.
	if info.CurrentState == StrategyStateShadow && info.BaselineScore == 0 {
		p.captureBaselineOnShadow(info)
	}

	// Check cool-down period
	if !p.canTransition(info) {
		return info.CurrentState, "cool-down period active", nil
	}

	// Evaluate based on current state
	var recommendedState StrategyState
	var reason string

	switch info.CurrentState {
	case StrategyStateCandidate:
		recommendedState, reason = p.evaluateCandidate(strategyID, evidence, score)

	case StrategyStateShadow:
		recommendedState, reason = p.evaluateShadow(strategyID, evidence, score, info)

	case StrategyStateChampion:
		recommendedState, reason = p.evaluateChampion(strategyID, evidence, score, info)

	case StrategyStateDemoted:
		recommendedState, reason = p.evaluateDemoted(strategyID, evidence, score, info)

	case StrategyStateRetired:
		recommendedState = StrategyStateRetired
		reason = "strategy is retired"

	default:
		return info.CurrentState, "unknown state", fmt.Errorf("promotion.Evaluate: unknown state: %s", info.CurrentState)
	}

	el.Debug(ctx, "Evaluate", "evaluated strategy",
		"strategy_id", strategyID,
		"current_state", info.CurrentState,
		"recommended_state", recommendedState,
		"score", score,
		"reason", reason,
	)

	return recommendedState, reason, nil
}

// evaluateCandidate evaluates a candidate strategy for promotion to shadow mode.
func (p *DefaultPromoter) evaluateCandidate(
	strategyID string,
	evidence experience.Evidence,
	score float64,
) (StrategyState, string) {
	// Candidate needs to show some promise to move to shadow mode
	if evidence.SampleCount >= 10 && evidence.SuccessRate >= 0.5 {
		return StrategyStateShadow, fmt.Sprintf(
			"promoted to shadow: success_rate=%.2f, sample_count=%d",
			evidence.SuccessRate, evidence.SampleCount,
		)
	}

	return StrategyStateCandidate, "needs more evidence to enter shadow mode"
}

// evaluateShadow evaluates a shadow strategy for promotion to champion.
func (p *DefaultPromoter) evaluateShadow(
	strategyID string,
	evidence experience.Evidence,
	score float64,
	info *StrategyInfo,
) (StrategyState, string) {
	// Check if evidence meets promotion criteria
	if MeetsPromotionCriteria(evidence, p.criteria) {
		// Also require score improvement over baseline when a baseline exists.
		// A strategy with stable but non-improving scores should not
		// replace the current champion.
		if info.BaselineScore > 0 {
			scoreDelta := score - info.BaselineScore
			if scoreDelta < p.criteria.MinAbsoluteImprovement {
				return StrategyStateShadow, fmt.Sprintf(
					"blocked: score_delta=%.2f below min_improvement=%.2f",
					scoreDelta, p.criteria.MinAbsoluteImprovement,
				)
			}

			return StrategyStateChampion, fmt.Sprintf(
				"promoted to champion: success_rate=%.2f, error_rate=%.2f, latency_p95=%d, confidence=%.2f, score_delta=%.2f",
				evidence.SuccessRate, evidence.ErrorRate, evidence.LatencyP95, evidence.Confidence, scoreDelta,
			)
		}

		return StrategyStateChampion, fmt.Sprintf(
			"promoted to champion: success_rate=%.2f, error_rate=%.2f, latency_p95=%d, confidence=%.2f",
			evidence.SuccessRate, evidence.ErrorRate, evidence.LatencyP95, evidence.Confidence,
		)
	}

	// Check for demotion due to poor performance
	if evidence.SampleCount >= int64(p.criteria.MinSampleCount) &&
		evidence.SuccessRate < (p.criteria.MinSuccessRate-p.criteria.DemotionThreshold) {
		return StrategyStateDemoted, fmt.Sprintf(
			"demoted: success_rate=%.2f below threshold",
			evidence.SuccessRate,
		)
	}

	return StrategyStateShadow, "collecting more evidence"
}

// evaluateChampion evaluates a champion strategy for demotion.
func (p *DefaultPromoter) evaluateChampion(
	strategyID string,
	evidence experience.Evidence,
	score float64,
	info *StrategyInfo,
) (StrategyState, string) {
	// Check if performance has degraded
	if evidence.SampleCount >= int64(p.criteria.MinSampleCount) {
		// Check for significant performance drop
		if evidence.SuccessRate < (p.criteria.MinSuccessRate-p.criteria.DemotionThreshold) ||
			evidence.ErrorRate > p.criteria.MaxErrorRate ||
			evidence.LatencyP95 > p.criteria.MaxLatencyP95 {
			return StrategyStateDemoted, fmt.Sprintf(
				"demoted: success_rate=%.2f, error_rate=%.2f, latency_p95=%d",
				evidence.SuccessRate, evidence.ErrorRate, evidence.LatencyP95,
			)
		}
	}

	// Check if champion hold period has passed but no significant improvement.
	if info.GenerationCount >= p.criteria.ChampionHoldPeriod {
		rollingImprovement := p.calculateRollingImprovement(info)

		// Hard tenure limit: demote if champion has held the position for
		// MaxChampionTenure generations without demonstrating improvement.
		// This prevents safe-but-stagnant strategies from permanently occupying
		// the champion slot — a system that never regresses also never evolves.
		if p.criteria.MaxChampionTenure > 0 && info.GenerationCount >= p.criteria.MaxChampionTenure {
			if rollingImprovement < p.criteria.MinRollingImprovement {
				return StrategyStateShadow, fmt.Sprintf(
					"demoted to shadow: tenure=%d exceeded max=%d, rolling_improvement=%.4f below min=%.2f",
					info.GenerationCount, p.criteria.MaxChampionTenure,
					rollingImprovement, p.criteria.MinRollingImprovement,
				)
			}
			return StrategyStateChampion, fmt.Sprintf(
				"champion tenure extended: rolling_improvement=%.4f meets min=%.2f",
				rollingImprovement, p.criteria.MinRollingImprovement,
			)
		}

		if rollingImprovement < p.criteria.MinRollingImprovement {
			return StrategyStateChampion, fmt.Sprintf(
				"champion flagged: rolling_improvement=%.4f below min=%.2f",
				rollingImprovement, p.criteria.MinRollingImprovement,
			)
		}
		return StrategyStateChampion, "champion status maintained"
	}

	return StrategyStateChampion, "champion status stable"
}

// evaluateDemoted evaluates a demoted strategy for re-promotion or retirement.
func (p *DefaultPromoter) evaluateDemoted(
	strategyID string,
	evidence experience.Evidence,
	score float64,
	info *StrategyInfo,
) (StrategyState, string) {
	// Check if the strategy has improved enough to re-enter shadow mode
	if MeetsPromotionCriteria(evidence, p.criteria) {
		return StrategyStateShadow, fmt.Sprintf(
			"re-promoted to shadow: success_rate=%.2f, confidence=%.2f",
			evidence.SuccessRate, evidence.Confidence,
		)
	}

	// Check if it should be retired after extended poor performance
	if info.GenerationCount >= p.criteria.ChampionHoldPeriod*2 {
		return StrategyStateRetired, "retired due to extended poor performance"
	}

	return StrategyStateDemoted, "monitoring for improvement"
}

// Promote promotes a strategy to the next higher state.
func (p *DefaultPromoter) Promote(ctx context.Context, strategyID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	info, exists := p.strategies[strategyID]
	if !exists {
		return ErrStrategyNotFound
	}

	// Determine target state
	var targetState StrategyState
	switch info.CurrentState {
	case StrategyStateCandidate:
		targetState = StrategyStateShadow
	case StrategyStateShadow:
		targetState = StrategyStateChampion
	case StrategyStateDemoted:
		targetState = StrategyStateShadow
	default:
		return fmt.Errorf("%w: cannot promote from %s", ErrInvalidStateTransition, info.CurrentState)
	}

	// Validate transition
	if !info.CurrentState.CanPromoteTo(targetState) {
		return fmt.Errorf("%w: cannot promote from %s to %s",
			ErrInvalidStateTransition, info.CurrentState, targetState)
	}

	// Check cool-down
	if !p.canTransition(info) {
		return ErrCoolDownActive
	}

	// Perform promotion
	p.transitionState(info, targetState, p.currentGeneration, "manual promotion")

	// Capture baseline score when entering shadow state via promotion.
	// This ensures the improvement check in evaluateShadow has a reference point.
	if targetState == StrategyStateShadow {
		// Use the previous entry in ScoreHistory if available, otherwise leave
		// baseline at the value set during initial strategy creation.
		p.captureBaselineOnShadow(info)
	}

	// Update champions list if promoted to champion
	if targetState == StrategyStateChampion {
		p.addToChampions(info.TaskType, strategyID)
	}

	return nil
}

// Demote demotes a strategy to a lower state with the given reason.
func (p *DefaultPromoter) Demote(ctx context.Context, strategyID string, reason string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	info, exists := p.strategies[strategyID]
	if !exists {
		return ErrStrategyNotFound
	}

	// Determine target state
	var targetState StrategyState
	switch info.CurrentState {
	case StrategyStateCandidate:
		targetState = StrategyStateRetired
	case StrategyStateShadow:
		targetState = StrategyStateDemoted
	case StrategyStateChampion:
		targetState = StrategyStateDemoted
	case StrategyStateDemoted:
		targetState = StrategyStateRetired
	default:
		return fmt.Errorf("%w: cannot demote from %s", ErrInvalidStateTransition, info.CurrentState)
	}

	// Validate transition
	if !info.CurrentState.CanDemoteTo(targetState) {
		return fmt.Errorf("%w: cannot demote from %s to %s",
			ErrInvalidStateTransition, info.CurrentState, targetState)
	}

	// Check cool-down (except for retirement)
	if targetState != StrategyStateRetired && !p.canTransition(info) {
		return ErrCoolDownActive
	}

	// Save old state before transition for champion list management.
	oldState := info.CurrentState

	// Perform demotion
	p.transitionState(info, targetState, p.currentGeneration, reason)

	// Remove from champions list if demoted from champion.
	// Must check oldState because transitionState has already updated CurrentState.
	if oldState == StrategyStateChampion {
		p.removeFromChampions(info.TaskType, strategyID)
	}

	return nil
}

// GetHistory returns the promotion history for a strategy.
func (p *DefaultPromoter) GetHistory(ctx context.Context, strategyID string) ([]StrategyPromotionRecord, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	history, exists := p.history[strategyID]
	if !exists {
		return []StrategyPromotionRecord{}, nil
	}

	return history, nil
}

// GetCurrentState returns the current state of a strategy.
func (p *DefaultPromoter) GetCurrentState(ctx context.Context, strategyID string) (StrategyState, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	info, exists := p.strategies[strategyID]
	if !exists {
		return StrategyStateCandidate, nil
	}

	return info.CurrentState, nil
}

// GetChampions returns all strategies currently in champion state for a task type.
func (p *DefaultPromoter) GetChampions(ctx context.Context, taskType string) ([]StrategyInfo, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	championIDs, exists := p.champions[taskType]
	if !exists {
		return []StrategyInfo{}, nil
	}

	champions := make([]StrategyInfo, 0, len(championIDs))
	for _, id := range championIDs {
		if info, exists := p.strategies[id]; exists && info.CurrentState == StrategyStateChampion {
			champions = append(champions, *info)
		}
	}

	return champions, nil
}

// GetStrategyInfo returns detailed information about a strategy.
func (p *DefaultPromoter) GetStrategyInfo(ctx context.Context, strategyID string) (*StrategyInfo, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	info, exists := p.strategies[strategyID]
	if !exists {
		return nil, ErrStrategyNotFound
	}

	return info, nil
}

// SetGeneration sets the current evolution generation.
func (p *DefaultPromoter) SetGeneration(generation int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.currentGeneration = generation

	// Update generation count for all strategies
	for _, info := range p.strategies {
		info.GenerationCount++
	}
}

// canTransition checks if the strategy can transition based on cool-down period.
// New strategies (GenerationCount == 0) can transition immediately.
func (p *DefaultPromoter) canTransition(info *StrategyInfo) bool {
	// Allow immediate transition for new strategies
	if info.GenerationCount == 0 {
		return true
	}
	// Require cool-down period for subsequent transitions
	if info.GenerationCount < p.criteria.CoolDownGenerations {
		return false
	}
	return true
}

// calculateRollingImprovement computes the average score improvement over
// the recent ImprovementWindow generations from the strategy's score history.
// Returns 0 if there are fewer than 2 data points in the window.
func (p *DefaultPromoter) calculateRollingImprovement(info *StrategyInfo) float64 {
	window := p.criteria.ImprovementWindow
	if window <= 0 {
		window = 3
	}
	history := info.ScoreHistory
	if len(history) < 2 {
		return 0
	}
	// Look at last `window` entries for the rolling average.
	start := len(history) - window
	if start < 0 {
		start = 0
	}
	var totalDelta float64
	count := 0
	for i := start + 1; i < len(history); i++ {
		totalDelta += history[i].Score - history[i-1].Score
		count++
	}
	if count == 0 {
		return 0
	}
	return totalDelta / float64(count)
}

// captureBaselineOnShadow sets the baseline score from the most recent
// ScoreHistory entry when a strategy enters shadow state. This captures the
// score at shadow entry as the reference point for improvement checks.
func (p *DefaultPromoter) captureBaselineOnShadow(info *StrategyInfo) {
	if len(info.ScoreHistory) > 0 {
		info.BaselineScore = info.ScoreHistory[len(info.ScoreHistory)-1].Score
	}
}

// transitionState performs a state transition and records it in history.
func (p *DefaultPromoter) transitionState(
	info *StrategyInfo,
	newState StrategyState,
	generation int,
	reason string,
) {
	oldState := info.CurrentState

	// Update strategy info
	info.CurrentState = newState
	info.Generation = generation
	info.LastStateChange = time.Now()
	info.GenerationCount = 0

	if newState == StrategyStateChampion {
		now := time.Now()
		info.ChampionSince = &now
	} else {
		info.ChampionSince = nil
	}

	// Record in history
	record := StrategyPromotionRecord{
		StrategyID:    info.StrategyID,
		State:         newState,
		PreviousState: oldState,
		Generation:    generation,
		Evidence:      experience.Evidence{}, // Will be populated by caller if needed
		Reason:        reason,
		Timestamp:     time.Now(),
	}

	p.history[info.StrategyID] = append(p.history[info.StrategyID], record)

	// Cap per-strategy promotion history (#56): state transitions are
	// append-only and a long-running process accumulates them forever.
	if len(p.history[info.StrategyID]) > maxPromotionHistoryPerStrategy {
		p.history[info.StrategyID] = p.history[info.StrategyID][len(p.history[info.StrategyID])-maxPromotionHistoryPerStrategy:]
	}
}

// Caps bounding promoter growth (#56).
const (
	// maxPromotionHistoryPerStrategy caps the per-strategy transition record list.
	maxPromotionHistoryPerStrategy = 64
	// retiredRetention is how long a Retired strategy (and its history)
	// stays queryable before the sweep removes it.
	retiredRetention = 24 * time.Hour
)

// pruneRetiredLocked removes strategies that have been Retired longer than
// retiredRetention, together with their history (#56): strategies map only
// ever grew — retiring changed state but never removed entries, so both maps
// grew without bound on every new strategy ID. Caller must hold p.mu for
// writing.
func (p *DefaultPromoter) pruneRetiredLocked(now time.Time) {
	cutoff := now.Add(-retiredRetention)
	for id, info := range p.strategies {
		if info.CurrentState == StrategyStateRetired && info.LastStateChange.Before(cutoff) {
			delete(p.strategies, id)
			delete(p.history, id)
		}
	}
}

// addToChampions adds a strategy to the champions list for a task type.
func (p *DefaultPromoter) addToChampions(taskType string, strategyID string) {
	champions := p.champions[taskType]

	// Check if already in list
	for _, id := range champions {
		if id == strategyID {
			return
		}
	}

	// Add to list
	p.champions[taskType] = append(champions, strategyID)
}

// removeFromChampions removes a strategy from the champions list for a task type.
func (p *DefaultPromoter) removeFromChampions(taskType string, strategyID string) {
	champions := p.champions[taskType]

	// Find and remove
	for i, id := range champions {
		if id == strategyID {
			p.champions[taskType] = append(champions[:i], champions[i+1:]...)
			return
		}
	}
}

// RegisterStrategy registers a new strategy with initial state.
func (p *DefaultPromoter) RegisterStrategy(strategyID string, taskType string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, exists := p.strategies[strategyID]; exists {
		return fmt.Errorf("strategy %s already registered", strategyID)
	}

	p.strategies[strategyID] = &StrategyInfo{
		StrategyID:      strategyID,
		TaskType:        taskType,
		CurrentState:    StrategyStateCandidate,
		Generation:      p.currentGeneration,
		LastStateChange: time.Now(),
		GenerationCount: 0,
	}

	return nil
}

// GetAllStrategies returns all registered strategies.
func (p *DefaultPromoter) GetAllStrategies() map[string]StrategyInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make(map[string]StrategyInfo)
	for id, info := range p.strategies {
		result[id] = *info
	}

	return result
}
