package evolution

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// The latency-penalty contract: completed-task samples carry a multiplicative
// wall-time factor (1/(1+t/scale)) so the GA stops rewarding slow successes,
// while correctness keeps dominating — a failure is never penalty-rescued and
// any success still outscores any failure.

// completedAt builds a task.completed event whose created_at payload places
// the task's start elapsed before the event timestamp.
func completedAt(started time.Time, at time.Time) *ares_events.Event {
	return &ares_events.Event{
		Type:      ares_events.EventTaskCompleted,
		Timestamp: at,
		Payload: map[string]any{
			"task_id":    "t-lat",
			"created_at": started.Format(time.RFC3339),
		},
	}
}

func TestLatencyPenaltyCurve(t *testing.T) {
	obs := &RuntimeObserver{latencyScale: 30 * time.Second}
	// RFC3339 carries no sub-second precision, so pin the event time to a
	// whole second — the created_at round-trip is then exact.
	now := time.Now().Truncate(time.Second)

	tests := []struct {
		name    string
		started time.Time
		want    float64
	}{
		{"zero_latency_full_score", now, 1.0},
		{"at_scale_halves_score", now.Add(-30 * time.Second), 0.5},
		{"double_scale_third_score", now.Add(-60 * time.Second), 1.0 / 3.0},
		{"negative_elapsed_treated_as_zero", now.Add(time.Second), 1.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := obs.latencyPenalty(completedAt(tt.started, now))
			assert.InDelta(t, tt.want, got, 1e-9)
		})
	}

	// Monotonic decreasing: every 2x slower task scores strictly less.
	prev := obs.latencyPenalty(completedAt(now.Add(-time.Second), now))
	for d := 2; d <= 64; d *= 2 {
		got := obs.latencyPenalty(completedAt(now.Add(-time.Duration(d)*time.Second), now))
		assert.Less(t, got, prev, "slower task must score strictly lower at %ds", d)
		prev = got
	}
	// Bounded in (0,1].
	assert.Greater(t, prev, 0.0)
	assert.LessOrEqual(t, prev, 1.0)
}

func TestLatencyPenaltyDegradedInputs(t *testing.T) {
	obs := &RuntimeObserver{latencyScale: 30 * time.Second}
	now := time.Now().Truncate(time.Second)

	t.Run("missing_created_at_returns_one", func(t *testing.T) {
		assert.Equal(t, 1.0, obs.latencyPenalty(&ares_events.Event{
			Type:      ares_events.EventTaskCompleted,
			Timestamp: now,
			Payload:   map[string]any{"task_id": "t"},
		}))
	})
	t.Run("unparsable_created_at_returns_one", func(t *testing.T) {
		assert.Equal(t, 1.0, obs.latencyPenalty(&ares_events.Event{
			Type:      ares_events.EventTaskCompleted,
			Timestamp: now,
			Payload:   map[string]any{"created_at": "not-a-time"},
		}))
	})
	t.Run("nil_payload_returns_one", func(t *testing.T) {
		assert.Equal(t, 1.0, obs.latencyPenalty(&ares_events.Event{
			Type:      ares_events.EventTaskCompleted,
			Timestamp: now,
		}))
	})
}

func TestEventToSampleAppliesLatencyPenalty(t *testing.T) {
	obs := &RuntimeObserver{latencyScale: 30 * time.Second}
	now := time.Now().Truncate(time.Second)

	t.Run("slow_success_scored_below_one", func(t *testing.T) {
		s, ok := obs.eventToSample(completedAt(now.Add(-30*time.Second), now))
		require.True(t, ok)
		assert.True(t, s.Success)
		assert.InDelta(t, 0.5, s.Score, 1e-9)
	})
	t.Run("same_second_success_scores_full", func(t *testing.T) {
		// RFC3339 truncates sub-second starts onto the same second, so a
		// sub-second task is exactly the zero-elapsed case: full score.
		s, ok := obs.eventToSample(completedAt(now, now))
		require.True(t, ok)
		assert.Equal(t, 1.0, s.Score)
	})
	t.Run("failure_never_penalty_rescued", func(t *testing.T) {
		s, ok := obs.eventToSample(&ares_events.Event{
			Type:      ares_events.EventTaskFailed,
			Timestamp: now,
			Payload:   map[string]any{"created_at": now.Add(-time.Hour).Format(time.RFC3339)},
		})
		require.True(t, ok)
		assert.False(t, s.Success)
		assert.Equal(t, 0.0, s.Score)
	})
	t.Run("slow_success_still_beats_failure", func(t *testing.T) {
		slow, ok := obs.eventToSample(completedAt(now.Add(-24*time.Hour), now))
		require.True(t, ok)
		fail, ok := obs.eventToSample(&ares_events.Event{Type: ares_events.EventTaskFailed, Timestamp: now})
		require.True(t, ok)
		assert.Greater(t, slow.Score, fail.Score,
			"correctness dominates: even a day-long success must outscore a failure")
		assert.Greater(t, slow.Score, 0.0)
		assert.Less(t, slow.Score, 0.01)
	})
}

func TestLatencyPenaltyDisabledByDefaultScale(t *testing.T) {
	// A zero scale falls back to the default constant (never disables
	// silently); the penalty is always measurable this way.
	obs := &RuntimeObserver{}
	now := time.Now().Truncate(time.Second)
	got := obs.latencyPenalty(completedAt(now.Add(-30*time.Second), now))
	assert.InDelta(t, 0.5, got, 1e-9, "zero scale must fall back to defaultLatencyScale")
	assert.False(t, math.IsNaN(got))
}
