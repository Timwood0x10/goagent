package evolution

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/evidence"
)

// The E1 observation-side contract: events carry the submission-time
// strategy_id (stamped by the fabric / sub-agent), the observer prefers it
// over the current activeID, and the aggregator's per-strategy windows stay
// isolated — the foundation per-task attribution needs before promotion is
// ever opened.

// feedObserver writes n completed (success) and n failed samples for each of
// two strategies straight through the observer's event path, bypassing the
// subscriber for determinism.
func feedObserver(obs *RuntimeObserver, n int) {
	ctx := context.Background()
	for i := 0; i < n; i++ {
		obs.processEvent(ctx, &ares_events.Event{
			Type: ares_events.EventTaskCompleted,
			Payload: map[string]any{
				"task_id":     "t-a-" + string(rune('0'+i)),
				"strategy_id": "strategy-a",
			},
		})
		obs.processEvent(ctx, &ares_events.Event{
			Type: ares_events.EventTaskFailed,
			Payload: map[string]any{
				"task_id":     "t-b-" + string(rune('0'+i)),
				"strategy_id": "strategy-b",
			},
		})
	}
}

// TestObserver_EventAttributionIsolation feeds events attributed to two
// different strategies and asserts per-strategy fitness windows count exactly
// their own samples — no cross-contamination into the currently-active idea.
func TestObserver_EventAttributionIsolation(t *testing.T) {
	store := evidence.NewMemoryStore()
	// activeID would attribute everything to one strategy — the event
	// payload attribution must win.
	obs := NewRuntimeObserver(nil,
		WithObserverEvidenceStore(store),
		WithObserverActiveIDFunc(func() string { return "strategy-active" }),
	)
	feedObserver(obs, 3)

	// Keep the judge gate low: this test cares about attribution isolation,
	// not the MinSamplesBeforeJudge policy (10 by default).
	cfg := DefaultAggregatorConfig()
	cfg.MinSamplesBeforeJudge = 2
	agg := NewRuntimeFitnessAggregator(store, cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	wa := agg.Window(ctx, "strategy-a")
	require.True(t, wa.Ok, "strategy-a window must resolve")
	assert.Equal(t, 3, wa.Count, "strategy-a counts only its own samples")
	assert.InDelta(t, 1.0, wa.Mean, 0.001, "strategy-a samples are all successes")

	wb := agg.Window(ctx, "strategy-b")
	require.True(t, wb.Ok, "strategy-b window must resolve")
	assert.Equal(t, 3, wb.Count, "strategy-b counts only its own samples")
	assert.InDelta(t, 0.0, wb.Mean, 0.001, "strategy-b samples are all failures")

	// The active strategy saw neither sample — it participated in nothing.
	waa := agg.Window(ctx, "strategy-active")
	if waa.Ok {
		assert.Equal(t, 0, waa.Count, "the activeID must not absorb attributed events")
	}
}

// TestObserver_UnattributedEventFallsBackToActiveID locks the no-wiring
// regression: an event WITHOUT strategy_id still attributes to the currently
// active strategy — identical to the pre-E1 behavior.
func TestObserver_UnattributedEventFallsBackToActiveID(t *testing.T) {
	store := evidence.NewMemoryStore()
	obs := NewRuntimeObserver(nil,
		WithObserverEvidenceStore(store),
		WithObserverActiveIDFunc(func() string { return "current-active" }),
	)
	obs.processEvent(context.Background(), &ares_events.Event{
		Type:    ares_events.EventTaskCompleted,
		Payload: map[string]any{"task_id": "t1"},
	})

	evs, err := store.Query(context.Background(), evidence.Filter{Source: "strategy", Limit: 10})
	require.NoError(t, err)
	require.Len(t, evs, 1)
	assert.Contains(t, string(evs[0].Payload), `"strategy_id":"current-active"`)
}
