package evolution

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// The cost-penalty contract (the M4 cost channel): completed-task samples
// carry a multiplicative token-spend factor (1/(1+tokens/scale)) so the GA
// stops rewarding token-hungry successes, while correctness keeps dominating
// — a failure is never penalty-rescued and any success still outscores any
// failure. The token total comes from the terminal task.completed event's
// payload (recordLocked stamps input/output/total_tokens from the
// CheckpointEnvelope v4 cumulative usage).

// completedWithTokens builds a task.completed event carrying a cumulative
// token total in the payload.
func completedWithTokens(total any) *ares_events.Event {
	return &ares_events.Event{
		Type:      ares_events.EventTaskCompleted,
		Timestamp: time.Now(),
		Payload: map[string]any{
			"task_id":      "t-cost",
			"total_tokens": total,
		},
	}
}

func TestCostPenaltyCurve(t *testing.T) {
	obs := &RuntimeObserver{}

	t.Run("at_scale_halves_score", func(t *testing.T) {
		assert.InDelta(t, 0.5, obs.costPenalty(completedWithTokens(100_000)), 1e-9)
	})
	t.Run("double_scale_third_score", func(t *testing.T) {
		assert.InDelta(t, 1.0/3.0, obs.costPenalty(completedWithTokens(200_000)), 1e-9)
	})
	t.Run("small_spend_nearly_full", func(t *testing.T) {
		assert.InDelta(t, 1.0/(1.0+10_000.0/100_000.0), obs.costPenalty(completedWithTokens(10_000)), 1e-9)
	})

	// Monotonic decreasing: every 2x costlier task scores strictly less.
	prev := obs.costPenalty(completedWithTokens(1_000))
	for n := 2_000; n <= 512_000; n *= 2 {
		got := obs.costPenalty(completedWithTokens(n))
		assert.Less(t, got, prev, "costlier task must score strictly lower at %d tokens", n)
		prev = got
	}
	// Bounded in (0,1].
	assert.Greater(t, prev, 0.0)
	assert.LessOrEqual(t, prev, 1.0)
}

func TestCostPenaltyDegradedInputs(t *testing.T) {
	obs := &RuntimeObserver{}

	t.Run("missing_tokens_returns_one", func(t *testing.T) {
		assert.Equal(t, 1.0, obs.costPenalty(&ares_events.Event{
			Type:      ares_events.EventTaskCompleted,
			Timestamp: time.Now(),
			Payload:   map[string]any{"task_id": "t"},
		}))
	})
	t.Run("zero_tokens_returns_one", func(t *testing.T) {
		assert.Equal(t, 1.0, obs.costPenalty(completedWithTokens(0)))
	})
	t.Run("negative_tokens_returns_one", func(t *testing.T) {
		assert.Equal(t, 1.0, obs.costPenalty(completedWithTokens(-5)))
	})
	t.Run("non_numeric_tokens_returns_one", func(t *testing.T) {
		assert.Equal(t, 1.0, obs.costPenalty(completedWithTokens("many")))
	})
	t.Run("nil_payload_returns_one", func(t *testing.T) {
		assert.Equal(t, 1.0, obs.costPenalty(&ares_events.Event{
			Type:      ares_events.EventTaskCompleted,
			Timestamp: time.Now(),
		}))
	})
	t.Run("nil_event_returns_one", func(t *testing.T) {
		assert.Equal(t, 1.0, obs.costPenalty(nil))
	})
}

func TestEventToSampleAppliesCostPenalty(t *testing.T) {
	obs := &RuntimeObserver{}

	t.Run("token_heavy_success_scored_below_one", func(t *testing.T) {
		s, ok := obs.eventToSample(completedWithTokens(100_000))
		require.True(t, ok)
		assert.True(t, s.Success)
		assert.InDelta(t, 0.5, s.Score, 1e-9)
		assert.Equal(t, 100_000, s.TotalTokens)
	})

	t.Run("failure_never_penalty_rescued", func(t *testing.T) {
		s, ok := obs.eventToSample(&ares_events.Event{
			Type:      ares_events.EventTaskFailed,
			Timestamp: time.Now(),
			Payload:   map[string]any{"task_id": "t", "total_tokens": 5_000_000},
		})
		require.True(t, ok)
		assert.False(t, s.Success)
		assert.Equal(t, 0.0, s.Score)
		// Token accounting is reported on the sample even for failures —
		// the sample is the evidence record, not the score.
		assert.Equal(t, 5_000_000, s.TotalTokens)
	})

	t.Run("unaccounted_success_scores_one", func(t *testing.T) {
		s, ok := obs.eventToSample(&ares_events.Event{
			Type:      ares_events.EventTaskCompleted,
			Timestamp: time.Now(),
			Payload:   map[string]any{"task_id": "t"},
		})
		require.True(t, ok)
		assert.True(t, s.Success)
		assert.Equal(t, 1.0, s.Score)
		assert.Equal(t, 0, s.TotalTokens)
	})

	t.Run("cheap_success_outscores_expensive_success", func(t *testing.T) {
		cheap, ok1 := obs.eventToSample(completedWithTokens(1_000))
		expensive, ok2 := obs.eventToSample(completedWithTokens(1_000_000))
		require.True(t, ok1)
		require.True(t, ok2)
		assert.Greater(t, cheap.Score, expensive.Score)
		// Correctness still dominates: ANY success > any failure.
		failure, _ := obs.eventToSample(&ares_events.Event{
			Type:      ares_events.EventTaskFailed,
			Timestamp: time.Now(),
			Payload:   map[string]any{"task_id": "t"},
		})
		assert.Greater(t, expensive.Score, failure.Score)
	})
}

func TestCostAndLatencyPenaltiesCompose(t *testing.T) {
	obs := &RuntimeObserver{latencyScale: 30 * time.Second}
	now := time.Now().Truncate(time.Second)

	// A task that took one latency-scale AND burned one token-scale keeps
	// 0.5 × 0.5 = 0.25 of its success score.
	evt := &ares_events.Event{
		Type:      ares_events.EventTaskCompleted,
		Timestamp: now,
		Payload: map[string]any{
			"task_id":      "t-both",
			"created_at":   now.Add(-30 * time.Second).Format(time.RFC3339),
			"total_tokens": 100_000,
		},
	}
	s, ok := obs.eventToSample(evt)
	require.True(t, ok)
	assert.InDelta(t, 0.25, s.Score, 1e-9)
}
