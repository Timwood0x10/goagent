package scoring

import (
	"fmt"
	"sync/atomic"
)

// ErrInvalidBudgetLimit is returned when maxLLMCalls is <= 0.
// Errors are defined in errors.go.

// Budget holds LLM scoring resource limits and current usage for one evolution generation.
type Budget struct {
	// MaxLLMCalls is the maximum number of LLM scorer calls allowed per generation.
	// Immutable after construction.
	MaxLLMCalls int64

	// UsedLLMCalls is the number of LLM scorer calls made in the current generation.
	UsedLLMCalls atomic.Int64

	// CacheHits is the number of score lookups served from cache.
	CacheHits atomic.Int64

	// FallbackCount is the number of times LLM scoring failed and fell back.
	FallbackCount atomic.Int64
}

// NewBudget creates a new scoring budget.
//
// Args:
//
//	maxLLMCalls - maximum LLM calls allowed per generation (must be > 0).
//
// Returns:
//
//	*Budget - the budget instance.
//	error - non-nil if maxLLMCalls <= 0.
func NewBudget(maxLLMCalls int) (*Budget, error) {
	if maxLLMCalls <= 0 {
		return nil, fmt.Errorf("new budget: %w", ErrInvalidBudgetLimit)
	}
	return &Budget{
		MaxLLMCalls: int64(maxLLMCalls),
	}, nil
}

// TryRecordLLMCall atomically checks the budget and records a call if within limit.
//
// Returns true if the call was recorded (budget allowed), false if at capacity.
func (b *Budget) TryRecordLLMCall() bool {
	used := b.UsedLLMCalls.Load()
	for used < b.MaxLLMCalls {
		if b.UsedLLMCalls.CompareAndSwap(used, used+1) {
			return true
		}
		used = b.UsedLLMCalls.Load()
	}
	return false
}

// CanCallLLM checks if an LLM call is still within budget.
//
// Returns:
//
//	bool - true if an LLM call can be made.
func (b *Budget) CanCallLLM() bool {
	return b.UsedLLMCalls.Load() < b.MaxLLMCalls
}

// RecordCacheHit records a cache hit (does not consume budget).
func (b *Budget) RecordCacheHit() {
	b.CacheHits.Add(1)
}

// RecordFallback records a fallback to heuristic scoring.
func (b *Budget) RecordFallback() {
	b.FallbackCount.Add(1)
}

// Reset resets usage counters for a new generation while keeping limits.
func (b *Budget) Reset() {
	b.UsedLLMCalls.Store(0)
	b.CacheHits.Store(0)
	b.FallbackCount.Store(0)
}

// Usage returns current budget utilization.
//
// Returns:
//
//	used - LLM calls used.
//	max - max LLM calls allowed.
//	cacheHits - cache hit count.
//	fallbacks - fallback count.
func (b *Budget) Usage() (used, max, cacheHits, fallbacks int) {
	return int(b.UsedLLMCalls.Load()), int(b.MaxLLMCalls), int(b.CacheHits.Load()), int(b.FallbackCount.Load())
}
