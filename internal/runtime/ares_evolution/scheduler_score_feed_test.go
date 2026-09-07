package evolution

import (
	"context"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// waitForScore polls until the scheduler's score window holds n scores, using
// channel-driven event delivery with a bounded deadline (no time.Sleep sync).
func waitForScore(t *testing.T, s *EvolutionScheduler, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, _, count := s.scoreSnapshot()
		if count >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d scores, have %d", n, count)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestRegister_TaskOutcomeEventsFeedScores locks the production score feed:
// task.completed / task.failed events flowing through the EventStore must
// land in the scheduler's score window (success=1.0, failure=0.0 — normalized
// to [0,1], §8 assertion 6), so TriggerOnThreshold / TriggerOnIdle degradation
// detection works without an external feeder.
func TestRegister_TaskOutcomeEventsFeedScores(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	scheduler := NewEvolutionScheduler(store, newMockAdapterForScheduler())
	scheduler.Register()
	defer func() { scheduler.Shutdown() }()

	ctx := context.Background()
	appendEvt := func(suffix, evtType string) {
		id := time.Now().Format("150405.000000000") + suffix
		if err := store.Append(ctx, "agent-1", []*ares_events.Event{{
			ID:       id,
			StreamID: "agent-1",
			Type:     ares_events.EventType(evtType),
		}}, -1); err != nil {
			t.Fatalf("append %s: %v", evtType, err)
		}
	}
	for i := 0; i < 3; i++ {
		appendEvt("-c-"+string(rune('a'+i)), string(ares_events.EventTaskCompleted))
	}
	appendEvt("-f", string(ares_events.EventTaskFailed))

	waitForScore(t, scheduler, 4)

	avg, _, count := scheduler.scoreSnapshot()
	if count != 4 {
		t.Fatalf("expected 4 scores, got %d", count)
	}
	wantAvg := (3*taskScoreSuccess + taskScoreFailure) / 4
	if avg != wantAvg {
		t.Errorf("expected avg %.2f (3 success + 1 failure), got %.2f", wantAvg, avg)
	}
}
