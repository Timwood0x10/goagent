package taskfabric

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// The E1 contract: the evolution strategy active at SUBMISSION time is
// stamped once onto the task's checkpoint envelope, rides on every persisted
// event, and survives quanta and restarts — so the RuntimeObserver can
// attribute each sample to the strategy that actually produced it, even when
// a promote happens mid-task.

// newEventStoreFabric returns a fabric wired to an in-memory event store.
func newEventStoreFabric(t *testing.T) (*Fabric, *ares_events.MemoryEventStore) {
	t.Helper()
	store := ares_events.NewMemoryEventStore()
	f := NewFabric().WithEventStore(store)
	return f, store
}

// strategyEvents returns the persisted task.* events for a task.
func strategyEvents(t *testing.T, store *ares_events.MemoryEventStore, taskID string) []*ares_events.Event {
	t.Helper()
	evs, err := store.ReadAll(context.Background(), ares_events.ReadOptions{})
	require.NoError(t, err)
	var out []*ares_events.Event
	for _, ev := range evs {
		if ev.StreamID == taskID {
			out = append(out, ev)
		}
	}
	return out
}

// TestCheckpointDecode_V1EnvelopeWithoutStrategyID locks the v1→v2 forward
// compatibility: a v1 envelope (no strategy_id) decodes successfully with an
// empty StrategyID — it must NOT hit ErrCheckpointSchemaVersion, and
// consumers fall back to the active-strategy attribution exactly as before.
func TestCheckpointDecode_V1EnvelopeWithoutStrategyID(t *testing.T) {
	v1 := map[string]any{
		"schema_version":     1,
		"used_experience_id": "exp-1",
		"payload":            map[string]any{"task_desc": "do things"},
		"step_checkpoint":    map[string]any{"step": 2},
	}
	dc, err := DecodeCheckpoint(v1)
	require.NoError(t, err, "v1 envelope must decode under v2 code")
	assert.Equal(t, "", dc.StrategyID, "v1 envelope has no attribution — empty, not an error")
	assert.Equal(t, 1, dc.SchemaVersion)
	assert.Equal(t, "exp-1", dc.UsedExperienceID)
}

// TestCheckpointDecode_StrategyIDRoundTrip covers the typed-envelope and
// JSON-round-trip decode branches plus the encode path: the attribution
// must survive the full persistence protocol.
func TestCheckpointDecode_StrategyIDRoundTrip(t *testing.T) {
	env := &CheckpointEnvelope{
		SchemaVersion: CurrentCheckpointSchemaVersion,
		StrategyID:    "strategy-a",
		Payload:       map[string]any{"task_desc": "x"},
	}
	dc, err := DecodeCheckpoint(env)
	require.NoError(t, err)
	require.Equal(t, "strategy-a", dc.StrategyID)

	// JSON round-trip: the map branch must extract strategy_id too.
	raw, err := MarshalCheckpoint(env)
	require.NoError(t, err)
	var decoded any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	dc2, err := DecodeCheckpoint(decoded)
	require.NoError(t, err)
	assert.Equal(t, "strategy-a", dc2.StrategyID)

	// Re-encode carries the attribution forward (yield→resume re-wrap).
	assert.Equal(t, "strategy-a", EncodeCheckpoint(dc2).StrategyID)
}

// TestFabric_StrategyStickyAcrossQuantums is THE core E1 assertion: the task
// is stamped with strategy A at submission; a promote to B happens
// mid-flight (the stamp source now returns B); the quantum re-wraps the
// checkpoint the way the kernel scheduler does (preserving the stamped
// StrategyID); and the final task.completed event still attributes to A.
func TestFabric_StrategyStickyAcrossQuantums(t *testing.T) {
	f, store := newEventStoreFabric(t)
	stamp := "strategy-a"
	f = f.WithStrategyStamp(func() string { return stamp })

	require.NoError(t, f.Create(&Task{ID: "t1", Capability: "code"}))

	// Mid-flight promote: the control plane flips A→B.
	stamp = "strategy-b"

	// Quantum 1 yields: the scheduler re-wraps the decoded submission
	// metadata (including the stamped StrategyID) around the step output.
	tk, err := f.Task("t1")
	require.NoError(t, err)
	meta, err := DecodeCheckpoint(tk.Checkpoint)
	require.NoError(t, err)
	require.Equal(t, "strategy-a", meta.StrategyID, "submission stamp must survive the read")
	epoch, err := f.Acquire("t1", "agent-1", 1<<20)
	require.NoError(t, err)
	require.NoError(t, f.Start("t1", "agent-1", epoch))
	quantum1 := EncodeCheckpoint(DecodedCheckpoint{
		UserProfile:      meta.UserProfile,
		Payload:          meta.Payload,
		UsedExperienceID: meta.UsedExperienceID,
		StrategyID:       meta.StrategyID,
		StepCheckpoint:   map[string]any{"step": 1},
	})
	require.NoError(t, f.Yield("t1", "agent-1", epoch, quantum1))

	// Quantum 2: a new agent re-acquires the SUSPENDED task and resumes.
	epoch2, err := f.Acquire("t1", "agent-2", 1<<20)
	require.NoError(t, err)
	require.NoError(t, f.Start("t1", "agent-2", epoch2))

	// Complete with the re-wrapped envelope — still carrying A.
	quantum2 := EncodeCheckpoint(DecodedCheckpoint{
		StrategyID:     meta.StrategyID,
		StepCheckpoint: map[string]any{"result": "ok"},
	})
	require.NoError(t, f.CompleteWithCheckpoint("t1", "agent-2", epoch2, quantum2))

	var completedStrategy string
	for _, ev := range strategyEvents(t, store, "t1") {
		if ev.Type == ares_events.EventTaskCompleted {
			sid, _ := ev.Payload["strategy_id"].(string)
			completedStrategy = sid
		}
	}
	assert.Equal(t, "strategy-a", completedStrategy,
		"the completed event must attribute to the SUBMISSION-time strategy, not the mid-flight active")
}

// TestFabric_StrategyAttributionSurvivesRestart locks the cross-restart
// contract: the stamped envelope is folded back from the durable log, and
// events recorded AFTER the restart still carry the original attribution.
func TestFabric_StrategyAttributionSurvivesRestart(t *testing.T) {
	store := ares_events.NewMemoryEventStore()

	f1 := NewFabric().WithEventStore(store).
		WithStrategyStamp(func() string { return "strategy-a" })
	require.NoError(t, f1.Create(&Task{ID: "t1", Capability: "code"}))

	f2 := NewFabric().WithEventStore(store)
	require.NoError(t, f2.RestoreFromStore(context.Background()))

	tk, err := f2.Task("t1")
	require.NoError(t, err)
	dc, err := DecodeCheckpoint(tk.Checkpoint)
	require.NoError(t, err)
	require.Equal(t, "strategy-a", dc.StrategyID, "restored task must keep its attribution")

	// Post-restart events re-derive the attribution from the checkpoint.
	epoch, err := f2.Acquire("t1", "agent-2", 1<<20)
	require.NoError(t, err)
	require.NoError(t, f2.Start("t1", "agent-2", epoch))
	require.NoError(t, f2.Complete("t1", "agent-2", epoch))

	var completedStrategy string
	for _, ev := range strategyEvents(t, store, "t1") {
		if ev.Type == ares_events.EventTaskCompleted {
			sid, _ := ev.Payload["strategy_id"].(string)
			completedStrategy = sid
		}
	}
	assert.Equal(t, "strategy-a", completedStrategy)
}

// TestFabric_StrategyStamp CallerEnvelopeWins: an explicitly pre-stamped
// envelope (the CompilePlan batch path) must not be overwritten by a fresher
// Create-time stamp — per-batch consistency beats per-task freshness.
func TestFabric_StrategyStampCallerEnvelopeWins(t *testing.T) {
	stamp := "strategy-batch"
	f := NewFabric().WithStrategyStamp(func() string { return stamp })

	ids, err := f.CompilePlan(context.Background(), []PlanStep{
		{ID: "step-1", Capability: "code", Payload: map[string]any{"k": 1}},
		{ID: "step-2", Capability: "code", Payload: map[string]any{"k": 2}},
	})
	require.NoError(t, err)
	require.Len(t, ids, 2)

	// Flip the stamp after the batch: the already-created tasks keep theirs.
	stamp = "strategy-later"
	require.NoError(t, f.Create(&Task{ID: "direct", Capability: "code"}))

	for _, id := range []string{"step-1", "step-2"} {
		tk, err := f.Task(id)
		require.NoError(t, err)
		dc, err := DecodeCheckpoint(tk.Checkpoint)
		require.NoError(t, err)
		assert.Equal(t, "strategy-batch", dc.StrategyID, "batch tasks share one sampled stamp")
	}
	tk, err := f.Task("direct")
	require.NoError(t, err)
	dc, err := DecodeCheckpoint(tk.Checkpoint)
	require.NoError(t, err)
	assert.Equal(t, "strategy-later", dc.StrategyID, "a later Create samples the current stamp")
}

// TestFabric_NoStampRegression locks the no-wiring behavior: without
// WithStrategyStamp, no payload key is written and checkpoints carry no
// attribution — the observer's activeID fallback path is byte-identical to
// the pre-E1 behavior.
func TestFabric_NoStampRegression(t *testing.T) {
	f, store := newEventStoreFabric(t)
	require.NoError(t, f.Create(&Task{ID: "t1", Capability: "code"}))
	epoch, err := f.Acquire("t1", "a", 1<<20)
	require.NoError(t, err)
	require.NoError(t, f.Start("t1", "a", epoch))

	for _, ev := range strategyEvents(t, store, "t1") {
		_, ok := ev.Payload["strategy_id"]
		assert.False(t, ok, "no stamp wired → no attribution key on %s", ev.Type)
	}
	tk, err := f.Task("t1")
	require.NoError(t, err)
	assert.Nil(t, tk.Checkpoint, "no stamp wired → no envelope is invented")
}
