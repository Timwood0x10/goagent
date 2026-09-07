// Package evolution provides rollback policies and strategy management for
// the autonomous evolution system. RollbackPolicy detects performance
// degradation and recommends reverting to a previous strategy.
package evolution

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// ScoreSnapshot captures a score observation at a given generation for
// degradation trend analysis.
type ScoreSnapshot struct {
	// Generation is the generation number when this score was recorded.
	Generation int
	// Score is the observed score value.
	Score float64
	// Timestamp is when this snapshot was recorded.
	Timestamp time.Time
}

// RollbackDecision describes the outcome of a rollback evaluation.
type RollbackDecision struct {
	// ShouldRollback indicates whether a rollback is recommended.
	ShouldRollback bool
	// Reason is the human-readable explanation for the decision.
	Reason string
	// CurrentScore is the most recent score observed.
	CurrentScore float64
	// ReferenceScore is the score used as the baseline for comparison.
	ReferenceScore float64
	// Degradation is the absolute difference between reference and current scores.
	Degradation float64
	// Threshold is the maximum allowed degradation before rollback triggers.
	Threshold float64
	// RecommendedAction describes what action to take.
	RecommendedAction string
}

// RollbackPolicy evaluates score trends and recommends rollback when
// degradation exceeds a configurable threshold. It detects both gradual
// decline (over a sliding window) and sudden drops below baseline.
type RollbackPolicy struct {
	mu                   sync.RWMutex
	scoreHistory         []ScoreSnapshot
	degradationThreshold float64 // max allowed score drop before rollback (default 0.15)
	windowSize           int     // generations to consider for trend (default 5)
	minSamples           int     // minimum samples before rollback triggers (default 3)
}

// RollbackOption configures a RollbackPolicy instance.
type RollbackOption func(*RollbackPolicy)

// WithDegradationThreshold sets the maximum allowed score degradation before
// rollback is triggered (default 0.15).
//
// Args:
//   - threshold: the degradation threshold (must be >= 0).
//
// Returns:
//   - RollbackOption: the configuration function.
func WithDegradationThreshold(threshold float64) RollbackOption {
	return func(p *RollbackPolicy) {
		if threshold >= 0 {
			p.degradationThreshold = threshold
		}
	}
}

// WithRollbackWindowSize sets the number of recent generations to consider
// for trend analysis (default 5).
//
// Args:
//   - size: the window size (must be >= 2).
//
// Returns:
//   - RollbackOption: the configuration function.
func WithRollbackWindowSize(size int) RollbackOption {
	return func(p *RollbackPolicy) {
		if size >= 2 {
			p.windowSize = size
		}
	}
}

// WithMinRollbackSamples sets the minimum number of score samples required
// before a rollback evaluation can trigger (default 3).
//
// Args:
//   - n: the minimum sample count (must be >= 1).
//
// Returns:
//   - RollbackOption: the configuration function.
func WithMinRollbackSamples(n int) RollbackOption {
	return func(p *RollbackPolicy) {
		if n >= 1 {
			p.minSamples = n
		}
	}
}

// NewRollbackPolicy creates a new rollback policy with default settings.
//
// Default configuration:
//   - degradationThreshold: 0.15
//   - windowSize: 5
//   - minSamples: 3
//
// Args:
//   - opts: optional configuration functions.
//
// Returns:
//   - *RollbackPolicy: the configured rollback policy.
func NewRollbackPolicy(opts ...RollbackOption) *RollbackPolicy {
	p := &RollbackPolicy{
		scoreHistory:         make([]ScoreSnapshot, 0),
		degradationThreshold: 0.15,
		windowSize:           5,
		minSamples:           3,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// RecordScore records a score snapshot for a given generation and appends
// it to the sliding window history.
//
// Args:
//   - generation: the generation number when the score was observed.
//   - score: the observed score value.
func (p *RollbackPolicy) RecordScore(generation int, score float64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	snapshot := ScoreSnapshot{
		Generation: generation,
		Score:      score,
		Timestamp:  time.Now(),
	}
	p.scoreHistory = append(p.scoreHistory, snapshot)

	// Trim history to window size.
	if len(p.scoreHistory) > p.windowSize {
		p.scoreHistory = p.scoreHistory[len(p.scoreHistory)-p.windowSize:]
	}
}

// Evaluate checks whether rollback is needed based on:
//   - Gradual degradation: score consistently declining over the window.
//   - Sudden drop: current score below baseline minus threshold.
//
// Returns a RollbackDecision with a clear recommendation. Returns a no-rollback
// decision with explanation when insufficient samples are available or when
// the score history is empty.
//
// Returns:
//   - *RollbackDecision: the evaluation result (never nil).
func (p *RollbackPolicy) Evaluate() *RollbackDecision {
	p.mu.RLock()
	defer p.mu.RUnlock()

	decision := &RollbackDecision{
		ShouldRollback:    false,
		RecommendedAction: "continue monitoring",
	}

	if len(p.scoreHistory) == 0 {
		decision.Reason = "no score data available"
		return decision
	}

	if len(p.scoreHistory) < p.minSamples {
		decision.Reason = fmt.Sprintf("insufficient samples (%d < %d)", len(p.scoreHistory), p.minSamples)
		return decision
	}

	// Calculate current score (most recent) and reference score (oldest in window).
	currentScore := p.scoreHistory[len(p.scoreHistory)-1].Score
	referenceScore := p.scoreHistory[0].Score
	degradation := referenceScore - currentScore

	decision.CurrentScore = currentScore
	decision.ReferenceScore = referenceScore
	decision.Degradation = degradation
	decision.Threshold = p.degradationThreshold

	// Check 1: Gradual degradation — score consistently declining over the window.
	if p.isGradualDeclineLocked() {
		decision.ShouldRollback = true
		decision.Reason = fmt.Sprintf("gradual degradation detected: score declined from %.2f to %.2f over %d generations",
			referenceScore, currentScore, len(p.scoreHistory))
		decision.RecommendedAction = "rollback to previous stable strategy"
		return decision
	}

	// Check 2: Sudden drop — current score below reference minus threshold.
	if degradation > p.degradationThreshold {
		decision.ShouldRollback = true
		decision.Reason = fmt.Sprintf("sudden score drop: degradation %.2f exceeds threshold %.2f",
			degradation, p.degradationThreshold)
		decision.RecommendedAction = "immediate rollback recommended"
		return decision
	}

	decision.Reason = fmt.Sprintf("degradation %.2f within threshold %.2f", degradation, p.degradationThreshold)
	return decision
}

// isGradualDeclineLocked checks if scores show a consistent declining trend
// across the current window. Caller must hold at least a read lock.
//
// Returns:
//   - bool: true if at least 3 samples show monotonic decline in the recent half.
func (p *RollbackPolicy) isGradualDeclineLocked() bool {
	if len(p.scoreHistory) < 3 {
		return false
	}

	// Check the most recent half of the window for monotonic decline.
	checkStart := len(p.scoreHistory) / 2

	declines := 0
	for i := checkStart; i < len(p.scoreHistory)-1; i++ {
		if p.scoreHistory[i+1].Score < p.scoreHistory[i].Score {
			declines++
		}
	}

	// At least 2 consecutive declines in the recent half.
	return declines >= 2 && declines >= (len(p.scoreHistory)-checkStart-1)
}

// Reset clears all recorded score history.
func (p *RollbackPolicy) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scoreHistory = make([]ScoreSnapshot, 0)
}

// ScoreHistory returns a copy of all recorded score snapshots.
//
// Returns:
//   - []ScoreSnapshot: a copy of the score history (never nil).
func (p *RollbackPolicy) ScoreHistory() []ScoreSnapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()

	result := make([]ScoreSnapshot, len(p.scoreHistory))
	copy(result, p.scoreHistory)
	return result
}

// ASMOption configures an ActiveStrategyManager instance.
type ASMOption func(*ActiveStrategyManager)

// WithASMGuardrails attaches guardrails to the active strategy manager.
// When set, Deploy checks PostEvolveCheck and auto-rollbacks on critical events.
//
// Args:
//   - guardrails: the guardrail instance (may be nil, in which case this is a no-op).
//
// Returns:
//   - ASMOption: the configuration function.
func WithASMGuardrails(guardrails *EvolutionGuardrails) ASMOption {
	return func(m *ActiveStrategyManager) {
		m.guardrails = guardrails
	}
}

// ActiveStrategyManager manages strategy deployment and rollback using
// a StrategyStore for persistence. It tracks the current and previous
// strategies, and uses a RollbackPolicy to detect degradation.
type ActiveStrategyManager struct {
	store      StrategyStore // persistent strategy storage
	current    *mutation.Strategy
	previous   *mutation.Strategy
	mu         sync.RWMutex
	rollback   *RollbackPolicy
	guardrails *EvolutionGuardrails
}

// NewActiveStrategyManager creates a new strategy manager with the given
// store and rollback policy.
//
// Args:
//   - store: persistent strategy store (must not be nil).
//   - rollbackPolicy: rollback policy for degradation detection (may be nil).
//   - opts: optional configuration functions.
//
// Returns:
//   - *ActiveStrategyManager: the configured manager.
//   - error: non-nil if store is nil.
func NewActiveStrategyManager(store StrategyStore, rollbackPolicy *RollbackPolicy, opts ...ASMOption) (*ActiveStrategyManager, error) {
	if store == nil {
		return nil, errors.New("strategy store must not be nil")
	}
	if rollbackPolicy == nil {
		rollbackPolicy = NewRollbackPolicy()
	}
	m := &ActiveStrategyManager{
		store:    store,
		rollback: rollbackPolicy,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m, nil
}

// Deploy stores the given strategy as the active strategy and saves the
// previous active strategy (if any) for potential rollback. The strategy
// is persisted via the StrategyStore.
//
// Args:
//   - ctx: operation context for cancellation.
//   - strategy: the strategy to deploy (must not be nil).
//
// Returns:
//   - error: non-nil if strategy is nil or store operation fails.
func (m *ActiveStrategyManager) Deploy(ctx context.Context, strategy *mutation.Strategy) error {
	if strategy == nil {
		return errors.New("strategy must not be nil")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Save current as previous before overwriting.
	m.previous = m.current
	m.current = strategy.Clone()

	// Persist to store using evolution.Strategy type.
	evoStrategy := strategyToEvoStrategy(strategy)
	if err := m.store.SetActive(ctx, evoStrategy); err != nil {
		// Rollback in-memory state on store failure.
		m.current = m.previous
		m.previous = nil
		return fmt.Errorf("store set active: %w", err)
	}

	log.Info("[ActiveStrategyManager] Strategy deployed",
		"strategy_id", strategy.ID,
		"version", strategy.Version,
		"score", strategy.Score,
		"previous_score", m.scoreOrZero(m.previous),
	)

	// Auto-rollback: if guardrails fire a critical event, immediately revert.
	if m.guardrails != nil {
		postResult := m.guardrails.PostEvolveCheck(ctx, strategy.Score, 0, nil)
		if postResult.ShouldStop {
			log.Warn("[ActiveStrategyManager] Guardrail critical after deploy, auto-rolling back",
				"strategy_id", strategy.ID,
				"score", strategy.Score,
				"reason", "guardrail critical after deploy",
				"events", len(postResult.Events),
			)
			// Rollback: restore previous as active.
			if m.previous != nil {
				prevEvo := strategyToEvoStrategy(m.previous)
				if err := m.store.SetActive(ctx, prevEvo); err != nil {
					return fmt.Errorf("store set active (rollback): %w", err)
				}
				m.current = m.previous
				m.previous = nil
				log.Info("[ActiveStrategyManager] Auto-rollback completed",
					"strategy_id", m.current.ID,
					"version", m.current.Version,
					"score", m.current.Score,
					"reason", "guardrail critical after deploy",
					"previous_window_avg", m.rollbackWindowAvg(),
				)
			}
			return errors.New("guardrail block deployment: critical event after deploy")
		}
	}

	return nil
}

// Rollback restores the previous strategy as the active one. Returns the
// rolled-back strategy. If no previous strategy exists, returns an error.
//
// Args:
//   - ctx: operation context for cancellation.
//
// Returns:
//   - *mutation.Strategy: the restored previous strategy.
//   - error: non-nil if no previous strategy exists or store operation fails.
//
// ErrNoPreviousStrategy is returned by ActiveStrategyManager.Rollback when
// no previous strategy exists to restore. Callers should match it with
// errors.Is: the fail-closed default config promotes exactly one seed
// strategy (previous stays nil), so "rollback unavailable" is an EXPECTED
// condition until a second promote happens — not a malfunction.
var ErrNoPreviousStrategy = errors.New("no previous strategy available for rollback")

func (m *ActiveStrategyManager) Rollback(ctx context.Context) (*mutation.Strategy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.previous == nil {
		return nil, ErrNoPreviousStrategy
	}

	previousClone := m.previous.Clone()

	// Persist rollback to store.
	evoStrategy := strategyToEvoStrategy(previousClone)
	if err := m.store.SetActive(ctx, evoStrategy); err != nil {
		return nil, fmt.Errorf("store rollback set active: %w", err)
	}

	// Swap: current becomes previous (for potential re-rollback).
	m.previous = m.current
	m.current = previousClone

	log.Info("[ActiveStrategyManager] Rollback completed",
		"strategy_id", previousClone.ID,
		"version", previousClone.Version,
		"score", previousClone.Score,
		"reason", "manual rollback triggered",
		"previous_window_avg", m.rollbackWindowAvg(),
	)
	return previousClone, nil
}

// Current returns the currently active strategy (cloned).
//
// Returns:
//   - *mutation.Strategy: clone of the current strategy, or nil if none deployed.
func (m *ActiveStrategyManager) Current() *mutation.Strategy {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.current == nil {
		return nil
	}
	return m.current.Clone()
}

// Previous returns the previously active strategy (cloned).
//
// Returns:
//   - *mutation.Strategy: clone of the previous strategy, or nil if none.
func (m *ActiveStrategyManager) Previous() *mutation.Strategy {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.previous == nil {
		return nil
	}
	return m.previous.Clone()
}

// RollbackPolicy returns the underlying rollback policy for score recording
// and evaluation. The returned policy shares the same state.
//
// Returns:
//   - *RollbackPolicy: the rollback policy instance.
func (m *ActiveStrategyManager) RollbackPolicy() *RollbackPolicy {
	return m.rollback
}

// RecordScore records a score snapshot in the rollback policy for
// degradation trend analysis.
//
// Args:
//   - generation: the generation number.
//   - score: the observed score.
func (m *ActiveStrategyManager) RecordScore(generation int, score float64) {
	m.rollback.RecordScore(generation, score)
}

// scoreOrZero returns the score of a strategy, or 0 if nil.
func (m *ActiveStrategyManager) scoreOrZero(s *mutation.Strategy) float64 {
	if s == nil {
		return 0
	}
	return s.Score
}

// rollbackWindowAvg computes the average score over the rollback score history.
// Returns 0 if no scores are recorded.
func (m *ActiveStrategyManager) rollbackWindowAvg() float64 {
	history := m.rollback.ScoreHistory()
	if len(history) == 0 {
		return 0
	}
	var total float64
	for _, s := range history {
		total += s.Score
	}
	return total / float64(len(history))
}

// strategyToEvoStrategy converts a mutation.Strategy to an evolution.Strategy
// for persistence via the StrategyStore interface.
func strategyToEvoStrategy(s *mutation.Strategy) *Strategy {
	if s == nil {
		return nil
	}

	paramsCopy := make(map[string]any, len(s.Params))
	for k, v := range s.Params {
		paramsCopy[k] = v
	}

	return &Strategy{
		ID:                   s.ID,
		Version:              s.Version,
		Params:               paramsCopy,
		ParentID:             s.ParentID,
		PromptTemplate:       s.PromptTemplate,
		StrategyMutationType: s.StrategyMutationType.String(),
		MutationDesc:         s.MutationDesc,
		Score:                s.Score,
		CreatedAt:            s.CreatedAt,
	}
}

// ensure interface compliance
var _ = strategyToEvoStrategy

// RollbackPolicyConfig holds configuration for creating a RollbackPolicy
// within the wired evolution system.
type RollbackPolicyConfig struct {
	// Enabled enables the rollback policy when true.
	Enabled bool `json:"enabled"`
	// DegradationThreshold is the max allowed score drop before rollback (default 0.15).
	DegradationThreshold float64 `json:"degradation_threshold"`
	// WindowSize is the number of generations to consider for trend (default 5).
	WindowSize int `json:"window_size"`
	// MinSamples is the minimum samples before rollback triggers (default 3).
	MinSamples int `json:"min_samples"`
}
