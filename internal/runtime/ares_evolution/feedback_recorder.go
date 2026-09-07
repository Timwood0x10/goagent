// Package evolution provides automatic experience extraction from flight recorder diagnostics.
// It bridges the flight recording system with the experience store to enable
// continuous learning from agent execution failures and anomalies.
package evolution

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/refine"
	aresExperience "github.com/Timwood0x10/ares/internal/runtime/memory/experience"
)

// recordedOutcome stores a strategy outcome entry for local querying.
type recordedOutcome struct {
	// StrategyID is the identifier of the strategy that was deployed.
	StrategyID string

	// Success indicates whether the deployment was successful.
	Success bool

	// Score is the fitness score achieved.
	Score float64

	// ExperienceIDs are the experience IDs associated with this outcome.
	ExperienceIDs []string
}

// FeedbackRecorder bridges strategy outcomes to the experience feedback system.
// It records outcomes both locally and to the external feedback service,
// enabling experience reinforcement through bandit feedback.
//
// Circuit breaker: when the feedback service returns N consecutive errors,
// the recorder enters a cool-down period and skips further service calls
// until the cooldown expires.
//
// When a refine.Refiner is attached (WithRefiner), each Register also applies
// a small-step Harness proposal updating the strategy's latest score, giving
// the evolution loop a rollback-capable, baseline-checked feedback trail
// (primitive 1: small-step evolution).
type FeedbackRecorder struct {
	feedbackService *aresExperience.FeedbackService
	outcomes        []recordedOutcome
	maxOutcomes     int
	mu              sync.RWMutex

	circuitBreakerConsecutiveErrors int
	circuitBreakerMaxErrors         int
	circuitBreakerCooldown          time.Duration
	circuitBreakerOpenedAt          time.Time

	// refiner, when non-nil, receives every registered outcome as a small-step
	// proposal (see WithRefiner). nil disables the refine trail.
	refiner *refine.Refiner
	// strategyScores is the refine.Store backing for strategy score updates,
	// keyed by "strategy:<id>". Guarded by mu.
	strategyScores map[string]float64
}

// FeedbackRecorderOption configures a FeedbackRecorder.
type FeedbackRecorderOption func(*FeedbackRecorder)

// WithRefiner attaches a refine.Refiner so every registered outcome is applied
// as a baseline-checked, rollback-capable score update. The refiner's store
// must be this recorder (it implements refine.Store).
func WithRefiner(ref *refine.Refiner) FeedbackRecorderOption {
	return func(r *FeedbackRecorder) {
		r.refiner = ref
	}
}

// NewFeedbackRecorder creates a FeedbackRecorder that records strategy outcomes
// to the given feedback service.
//
// Args:
//
//	feedbackService - the feedback service for experience reinforcement.
//
// Returns:
//
//	*FeedbackRecorder - the configured recorder instance.
func NewFeedbackRecorder(feedbackService *aresExperience.FeedbackService, opts ...FeedbackRecorderOption) *FeedbackRecorder {
	r := &FeedbackRecorder{
		feedbackService:         feedbackService,
		outcomes:                make([]recordedOutcome, 0),
		maxOutcomes:             1000,
		circuitBreakerMaxErrors: 3,
		circuitBreakerCooldown:  30 * time.Second,
		strategyScores:          make(map[string]float64),
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Get implements refine.Store for strategy score entries ("strategy:<id>").
func (r *FeedbackRecorder) Get(_ context.Context, target string) (any, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	score, ok := r.strategyScores[target]
	return score, ok
}

// Set implements refine.Store for strategy score entries.
func (r *FeedbackRecorder) Set(_ context.Context, target string, value any) error {
	score, ok := value.(float64)
	if !ok {
		return fmt.Errorf("feedback recorder: refine store value for %q is not a float64", target)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.strategyScores[target] = score
	return nil
}

// Register records a strategy outcome both locally and in the feedback service.
// For each non-empty experience ID in the outcome:
//   - If successful, RecordSuccess increments the usage count.
//   - If failed, RecordFailure decrements the rank.
//
// Empty experience IDs are silently skipped. A nil feedback service is also
// silently skipped, allowing the recorder to operate in offline mode.
//
// Args:
//
//	ctx - operation context for cancellation.
//	outcome - the strategy outcome to record.
//
// Returns:
//
//	error - delegation error from the feedback service, or nil.
func (r *FeedbackRecorder) Register(ctx context.Context, outcome StrategyOutcome) error {
	ro := recordedOutcome{
		StrategyID:    outcome.StrategyID,
		Success:       outcome.Success,
		Score:         outcome.Score,
		ExperienceIDs: make([]string, len(outcome.ExperienceIDs)),
	}
	copy(ro.ExperienceIDs, outcome.ExperienceIDs)

	// Store locally for querying.
	r.mu.Lock()
	r.outcomes = append(r.outcomes, ro)
	if r.maxOutcomes > 0 && len(r.outcomes) > r.maxOutcomes {
		trimCount := len(r.outcomes) - r.maxOutcomes
		r.outcomes = r.outcomes[trimCount:]
	}
	r.mu.Unlock()

	// Small-step Harness trail (primitive 1): when a refiner is attached, apply
	// the outcome as a baseline-checked, rollback-capable score update so the
	// evolution loop keeps a reversible feedback trail. A stale-write conflict
	// (concurrent mutation) is non-fatal: the outcome is still recorded locally
	// and to the feedback service below; only the refine trail skips that edit.
	if r.refiner != nil && outcome.StrategyID != "" {
		target := "strategy:" + outcome.StrategyID
		var before any
		if v, ok := r.Get(ctx, target); ok {
			before = v
		}
		proposal := refine.Proposal{
			ID:     fmt.Sprintf("feedback-%s-%d", outcome.StrategyID, time.Now().UnixNano()),
			Kind:   refine.KindMemory,
			Target: target,
			Before: before,
			After:  outcome.Score,
			Reason: fmt.Sprintf("strategy outcome success=%t", outcome.Success),
		}
		if applyErr := r.refiner.Apply(ctx, proposal); applyErr != nil {
			log.Warn("[FeedbackRecorder] refine apply failed; outcome still recorded",
				"strategy_id", outcome.StrategyID, "error", applyErr)
		}
	}

	// Skip feedback service if nil (offline mode).
	if r.feedbackService == nil {
		return nil
	}

	// Single critical section: check circuit breaker state atomically.
	r.mu.Lock()
	if r.circuitBreakerConsecutiveErrors >= r.circuitBreakerMaxErrors {
		if time.Since(r.circuitBreakerOpenedAt) < r.circuitBreakerCooldown {
			consecutive := r.circuitBreakerConsecutiveErrors
			remaining := r.circuitBreakerCooldown - time.Since(r.circuitBreakerOpenedAt)
			r.mu.Unlock()
			log.Warn("[FeedbackRecorder] Circuit breaker open, skipping feedback service",
				"consecutive_errors", consecutive,
				"cooldown_remaining", remaining)
			return nil
		}
		r.circuitBreakerConsecutiveErrors = 0
	}
	// Collect non-empty experience IDs while still holding the lock so the
	// subsequent service calls can run without the mutex held.
	expIDs := make([]string, 0, len(outcome.ExperienceIDs))
	for _, expID := range outcome.ExperienceIDs {
		if expID != "" {
			expIDs = append(expIDs, expID)
		}
	}
	r.mu.Unlock()

	// Make service calls outside the lock to avoid blocking other callers.
	// Circuit breaker state is updated atomically after all calls complete.
	var (
		failCount int
		lastErr   error
	)
	for _, expID := range expIDs {
		var err error
		if outcome.Success {
			err = r.feedbackService.RecordSuccess(ctx, outcome.TenantID, expID)
		} else {
			err = r.feedbackService.RecordFailure(ctx, outcome.TenantID, expID)
		}
		if err != nil {
			failCount++
			lastErr = err
		}
	}

	// Re-acquire lock and update circuit breaker state atomically.
	r.mu.Lock()
	if failCount > 0 {
		r.circuitBreakerConsecutiveErrors += failCount
		if r.circuitBreakerConsecutiveErrors >= r.circuitBreakerMaxErrors {
			r.circuitBreakerOpenedAt = time.Now()
			log.Warn("[FeedbackRecorder] Circuit breaker opened",
				"consecutive_errors", r.circuitBreakerConsecutiveErrors)
		}
	} else {
		r.circuitBreakerConsecutiveErrors = 0
	}
	r.mu.Unlock()

	return lastErr
}

// String returns a human-readable summary of recent outcomes.
//
// Returns:
//
//	string - summary showing total outcomes, success rate, and recent results.
func (r *FeedbackRecorder) String() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.outcomes) == 0 {
		return "FeedbackRecorder: no outcomes recorded"
	}

	total := len(r.outcomes)
	successes := 0
	for _, o := range r.outcomes {
		if o.Success {
			successes++
		}
	}

	start := 0
	if total > 5 {
		start = total - 5
	}
	var recent []string
	for i := start; i < total; i++ {
		o := r.outcomes[i]
		status := "FAIL"
		if o.Success {
			status = "OK"
		}
		recent = append(recent, fmt.Sprintf("%s score=%.2f", status, o.Score))
	}

	rate := float64(successes) / float64(total) * 100
	return fmt.Sprintf("FeedbackRecorder: %d outcomes, %.1f%% success rate, recent: [%s]",
		total, rate, strings.Join(recent, ", "))
}
