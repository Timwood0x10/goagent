// Package postgres provides PostgreSQL database operations for the storage system.
package postgres

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/Timwood0x10/ares/internal/errors"
)

// CircuitBreakerState represents the state of a circuit breaker.
type CircuitBreakerState string

const (
	CircuitBreakerStateClosed   CircuitBreakerState = "closed"
	CircuitBreakerStateOpen     CircuitBreakerState = "open"
	CircuitBreakerStateHalfOpen CircuitBreakerState = "half-open"
)

// CircuitBreaker provides failure detection and automatic fallback for unreliable services.
// This implements the circuit breaker pattern to prevent cascading failures.
//
// Semantics:
//   - failureCount tracks CONSECUTIVE failures in Closed state (reset on each success).
//   - failureThreshold therefore means "open after N consecutive failures", not cumulative.
//   - In HalfOpen state, a single failure immediately re-opens the circuit.
type CircuitBreaker struct {
	mu               sync.RWMutex
	state            CircuitBreakerState
	failureCount     int
	failureThreshold int
	successThreshold int
	lastFailureTime  time.Time
	openTimeout      time.Duration
	halfOpenSuccess  int
	halfOpenInflight atomic.Int32
	lastProbeTime    time.Time // Tracks when the last half-open probe was allowed.
	lastCleanupTime  time.Time
	stopCh           chan struct{}
	cleanupStopped   atomic.Bool
	cleanupWg        sync.WaitGroup // Waits for the cleanup goroutine to exit in Close.
}

// NewCircuitBreaker creates a new CircuitBreaker instance.
//
// Args:
//
//	failureThreshold - number of CONSECUTIVE failures before opening the circuit.
//	  A success between failures resets the counter, so this is not a cumulative count.
//	openTimeout - time to wait before attempting half-open state.
//
// Returns:
//
//	*CircuitBreaker - the configured circuit breaker instance.
func NewCircuitBreaker(failureThreshold int, openTimeout time.Duration) *CircuitBreaker {
	cb := &CircuitBreaker{
		state:            CircuitBreakerStateClosed,
		failureThreshold: failureThreshold,
		successThreshold: 3,
		openTimeout:      openTimeout,
		lastCleanupTime:  time.Now(),
		stopCh:           make(chan struct{}),
	}

	// Start cleanup goroutine to prevent halfOpenInflight leaks.
	cb.cleanupWg.Add(1)
	go cb.cleanupLoop()

	return cb
}

// AllowRequest checks if a request should be allowed based on circuit breaker state.
// Returns error if circuit is open or enters open state.
func (cb *CircuitBreaker) AllowRequest() error {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitBreakerStateClosed:
		return nil

	case CircuitBreakerStateOpen:
		if time.Since(cb.lastFailureTime) > cb.openTimeout {
			// Move to half-open state and allow one probe request.
			cb.state = CircuitBreakerStateHalfOpen
			cb.halfOpenSuccess = 0
			cb.halfOpenInflight.Store(1) // Reserve the single half-open slot.
			cb.lastProbeTime = time.Now()
			return nil
		}
		return errors.ErrCircuitBreakerOpen

	case CircuitBreakerStateHalfOpen:
		if !cb.halfOpenInflight.CompareAndSwap(0, 1) {
			return errors.ErrCircuitBreakerOpen
		}
		cb.lastProbeTime = time.Now()
		return nil

	default:
		return errors.ErrInvalidState
	}
}

// RecordSuccess records a successful operation.
// In Closed state, this resets the consecutive failure count to zero,
// meaning failureThreshold tracks CONSECUTIVE failures (not cumulative).
// In HalfOpen state, this counts toward the success threshold needed to
// fully close the circuit.
func (cb *CircuitBreaker) RecordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case CircuitBreakerStateHalfOpen:
		cb.halfOpenSuccess++
		// Guard against going negative: if the inflight counter was already
		// cleaned up by cleanupHalfOpenInflight, a decrement here would make
		// it negative and permanently break CompareAndSwap(0,1) in AllowRequest.
		if cb.halfOpenInflight.Load() > 0 {
			cb.halfOpenInflight.Add(-1)
		}
		if cb.halfOpenSuccess >= cb.successThreshold {
			cb.state = CircuitBreakerStateClosed
			cb.failureCount = 0
			cb.halfOpenSuccess = 0
		}

	case CircuitBreakerStateClosed:
		// Reset consecutive failure count on each success.
		// This means failureThreshold behaves as "N consecutive failures"
		// rather than "N cumulative failures". This is intentional: it allows
		// intermittent failures without opening the circuit, while still
		// protecting against sustained outage patterns.
		cb.failureCount = 0

	default:
		// Unknown state: no-op. Future states must explicitly handle success semantics.
	}
}

// cleanupHalfOpenInflight cleans up leaked inflight counters.
// Only resets the counter if the last probe has been pending longer than
// the open timeout, indicating the probe likely leaked.
func (cb *CircuitBreaker) cleanupHalfOpenInflight() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state != CircuitBreakerStateHalfOpen {
		return
	}

	if cb.halfOpenInflight.Load() <= 0 {
		return
	}

	// Only reset if the last probe has been pending longer than openTimeout.
	if time.Since(cb.lastProbeTime) > cb.openTimeout {
		log.Warn("Detected halfOpenInflight leak, resetting counter",
			"current_count", cb.halfOpenInflight.Load())
		cb.halfOpenInflight.Store(0)
	}
}

// RecordFailure records a failed operation.
// In Closed state, this increments the consecutive failure count.
// In HalfOpen state, this immediately re-opens the circuit.
func (cb *CircuitBreaker) RecordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	if cb.state == CircuitBreakerStateHalfOpen {
		if cb.halfOpenInflight.Load() > 0 {
			cb.halfOpenInflight.Add(-1)
		}
		cb.state = CircuitBreakerStateOpen
		cb.lastFailureTime = time.Now()
		return
	}

	cb.failureCount++
	cb.lastFailureTime = time.Now()

	if cb.failureCount >= cb.failureThreshold {
		cb.state = CircuitBreakerStateOpen
	}
}

// State returns the current circuit breaker state.
// Returns current state.
func (cb *CircuitBreaker) State() CircuitBreakerState {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// cleanupLoop runs periodic cleanup to prevent halfOpenInflight leaks.
// This should be started as a goroutine in NewCircuitBreaker.
func (cb *CircuitBreaker) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute) // Check every 5 minutes
	defer ticker.Stop()
	defer cb.cleanupWg.Done()

	for {
		select {
		case <-cb.stopCh:
			return
		case <-ticker.C:
			cb.cleanupHalfOpenInflight()
		}
	}
}

// Close stops the cleanup goroutine and closes the circuit breaker.
func (cb *CircuitBreaker) Close() {
	if cb.cleanupStopped.CompareAndSwap(false, true) {
		close(cb.stopCh)
		cb.cleanupWg.Wait()
	}
}

// Reset resets the circuit breaker to closed state.
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.state = CircuitBreakerStateClosed
	cb.failureCount = 0
	cb.lastFailureTime = time.Time{}
	cb.halfOpenSuccess = 0
	cb.halfOpenInflight.Store(0)
}
