//go:build closure

package evolution

// scheduler_tick_gate_test.go locks design-doc §8 acceptance assertions 5–6
// (ga-runtime-evolution-design-zh.md):
//
//	5. single-trigger — repeated Tick calls inside MinInterval run at most
//	   one evolution cycle (throttling applies to the time-triggered path
//	   too).
//	6. dimensional consistency — RecordScore clamps every input to [0,1] so
//	   the score window stays dimensionally consistent with RollbackPolicy
//	   thresholds.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestClosure_Tick_MinIntervalThrottlesRepeatTicks (§8 assertion 5).
func TestClosure_Tick_MinIntervalThrottlesRepeatTicks(t *testing.T) {
	adapter := newMockAdapterForScheduler()
	s := NewEvolutionScheduler(nil, adapter, WithMinInterval(time.Hour))
	s.SetEnabled(true)

	// Tick only evolves on TriggerOnIdle via degradation detection or the
	// 100-score periodic branch. The window trims to scoreWindowSize (50),
	// so seed a degradation signal instead: 30 successes then 10 failures
	// → avg 0.75 vs recent 0.0 → drop 1.0 ≥ 0.15 → the FIRST Tick genuinely
	// runs one generation.
	for i := 0; i < 30; i++ {
		s.RecordScore(taskScoreSuccess)
	}
	for i := 0; i < 10; i++ {
		s.RecordScore(taskScoreFailure)
	}

	ctx := context.Background()
	s.Tick(ctx)
	assert.Equal(t, 1, adapter.runCountLocked(),
		"first Tick with an eligible window must run exactly one generation")

	// Second Tick inside MinInterval must be throttled: still one run.
	s.Tick(ctx)
	assert.Equal(t, 1, adapter.runCountLocked(),
		"Tick inside MinInterval must not start a second generation")
}

// TestClosure_RecordScore_ClampsToUnitInterval (§8 assertion 6).
func TestClosure_RecordScore_ClampsToUnitInterval(t *testing.T) {
	s := NewEvolutionScheduler(nil, newMockAdapterForScheduler())

	s.RecordScore(5.0) // out-of-range high → clamped to 1.0
	avg, _, count := s.scoreSnapshot()
	assert.Equal(t, 1, count)
	assert.InDelta(t, 1.0, avg, 0.0001, "scores above 1.0 must clamp to 1.0")

	s.RecordScore(-2.0) // out-of-range low → clamped to 0.0
	avg, _, count = s.scoreSnapshot()
	assert.Equal(t, 2, count)
	assert.InDelta(t, 0.5, avg, 0.0001, "scores below 0.0 must clamp to 0.0 (window: 1.0 + 0.0)")
}

// runCountLocked is a race-free accessor for the mock's run counter.
func (m *mockAdapterForScheduler) runCountLocked() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runCount
}
