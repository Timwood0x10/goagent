package taskfabric

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// TestRestoreFromStoreResumesSuspendedTask is the T2 integration contract: a
// fabric instance discarded mid-execution (process crash) is fully rebuilt
// from the event store alone — the task folds back to READY with its
// checkpoint, and the scheduler's ordinary acquire path resumes it.
func TestRestoreFromStoreResumesSuspendedTask(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	f1 := NewFabric().WithEventStore(store)

	t1 := &Task{
		ID:           "t1",
		Capability:   "rust/unsafe-analysis",
		Priority:     7,
		Dependencies: []string{"t0"},
		Origin:       "agent-root",
		RetryPolicy:  RetryPolicy{MaxRetries: 3, Attempts: 1},
		Deadline:     time.Now().Add(time.Hour).Truncate(time.Second),
	}
	require.NoError(t, f1.Create(t1))
	epoch1, err := f1.Acquire("t1", "agent-a", time.Minute)
	require.NoError(t, err)
	require.NoError(t, f1.Start("t1", "agent-a", epoch1))

	env := EncodeCheckpoint(DecodedCheckpoint{
		Payload:        map[string]any{"task_desc": "audit crate"},
		StepCheckpoint: map[string]any{"step": 3},
	})
	require.NoError(t, f1.Yield("t1", "agent-a", epoch1, env))

	// Simulate the crash: f1 is abandoned entirely.
	f2 := NewFabric().WithEventStore(store)
	require.NoError(t, f2.RestoreFromStore(context.Background()))

	got, err := f2.Task("t1")
	require.NoError(t, err)
	require.Equal(t, StateReady, got.State, "non-terminal task must fold to READY")
	require.Empty(t, got.Owner, "lease must never be restored")
	require.Nil(t, got.Lease)
	require.Equal(t, "rust/unsafe-analysis", got.Capability)
	require.Equal(t, 7, got.Priority)
	require.Equal(t, []string{"t0"}, got.Dependencies)
	require.Equal(t, "agent-root", got.Origin)
	require.Equal(t, RetryPolicy{MaxRetries: 3, Attempts: 1}, got.RetryPolicy)
	require.Equal(t, t1.Deadline, got.Deadline)

	// The checkpoint survives as the JSON-round-tripped map form that
	// DecodeCheckpoint's second branch handles.
	require.NotNil(t, got.Checkpoint)
	dc, err := DecodeCheckpoint(got.Checkpoint)
	require.NoError(t, err)
	require.Equal(t, CurrentCheckpointSchemaVersion, dc.SchemaVersion)
	require.Equal(t, map[string]any{"task_desc": "audit crate"}, dc.Payload)
	require.Equal(t, map[string]any{"step": float64(3)}, dc.StepCheckpoint)

	// Resume through the ordinary scheduler path: acquire on the rebuilt
	// fabric, then complete.
	epoch2, err := f2.Acquire("t1", "agent-b", time.Minute)
	require.NoError(t, err)
	require.NoError(t, f2.Start("t1", "agent-b", epoch2))
	require.NoError(t, f2.Complete("t1", "agent-b", epoch2))

	// Epoch monotonicity: the rebuilt fabric's first fencing token must
	// strictly dominate every token the pre-crash fabric handed out.
	require.Greater(t, epoch2, epoch1, "restored epoch must exceed all pre-crash epochs")

	// The stale pre-crash holder presenting its OLD epoch is rejected: agent-a
	// replays its pre-crash fencing token against the rebuilt fabric.
	require.NoError(t, f2.Create(newTask("t2")))
	_, err = f2.Acquire("t2", "agent-a", time.Minute)
	require.NoError(t, err)
	err = f2.Complete("t2", "agent-a", epoch1)
	require.ErrorIs(t, err, ErrEpochMismatch, "pre-crash epoch must not be accepted after restore")
}

// TestRestoreFromKeepsTerminalTasks verifies that COMPLETED/FAILED tasks are
// restored as terminal and never revived to READY.
func TestRestoreFromKeepsTerminalTasks(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	f1 := NewFabric().WithEventStore(store)

	require.NoError(t, f1.Create(newTask("done")))
	e1, err := f1.Acquire("done", "agent-a", time.Minute)
	require.NoError(t, err)
	require.NoError(t, f1.Start("done", "agent-a", e1))
	require.NoError(t, f1.Complete("done", "agent-a", e1))

	f2 := NewFabric().WithEventStore(store)
	require.NoError(t, f2.RestoreFromStore(context.Background()))

	got, err := f2.Task("done")
	require.NoError(t, err)
	require.Equal(t, StateCompleted, got.State, "terminal task must not be revived")
	_, err = f2.Acquire("done", "agent-b", time.Minute)
	require.Error(t, err, "a completed task must not be acquirable")
}

// TestRestoreFromStoreIdempotent verifies the fold is a pure function of the
// log: two consecutive restores converge to identical state.
func TestRestoreFromStoreIdempotent(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	f1 := NewFabric().WithEventStore(store)
	require.NoError(t, f1.Create(newTask("t1")))
	e1, err := f1.Acquire("t1", "agent-a", time.Minute)
	require.NoError(t, err)
	require.NoError(t, f1.Start("t1", "agent-a", e1))
	require.NoError(t, f1.Yield("t1", "agent-a", e1, map[string]any{"step": 1}))

	f2 := NewFabric().WithEventStore(store)
	require.NoError(t, f2.RestoreFromStore(context.Background()))
	first, err := f2.Task("t1")
	require.NoError(t, err)
	firstEpoch, err := f2.Acquire("t1", "agent-b", time.Minute)
	require.NoError(t, err)

	// Second restore: reset-and-fold must converge to the same state.
	require.NoError(t, f2.RestoreFromStore(context.Background()))
	second, err := f2.Task("t1")
	require.NoError(t, err)
	require.Equal(t, first.State, second.State)
	require.Equal(t, first.Capability, second.Capability)
	require.Equal(t, first.RetryPolicy, second.RetryPolicy)

	secondEpoch, err := f2.Acquire("t1", "agent-c", time.Minute)
	require.NoError(t, err)
	// Strictly growing, not exactly +1: the restore re-reads the log — which
	// now also contains the task.acquired of firstEpoch — and sets the epoch
	// to max+1, so the next token skips ahead. Only monotonicity matters for
	// fencing correctness; gaps are harmless.
	require.Greater(t, secondEpoch, firstEpoch, "epoch must keep growing monotonically across restores")
}

// TestRestoreEpochDominatesUnpersistedAcquires is the fencing regression for
// the epoch source: Acquire bumps f.epoch but emits task.acquired, which is
// observability-only. If the restore scanned only must-persist events, every
// token granted after the last checkpoint would be re-issued and a stale
// pre-crash holder would pass ownerLocked's epoch check. The epoch therefore
// rides on every persisted event and the scan spans all of them.
func TestRestoreEpochDominatesUnpersistedAcquires(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	f1 := NewFabric().WithEventStore(store)

	require.NoError(t, f1.Create(newTask("t1")))
	require.NoError(t, f1.Create(newTask("t2")))
	require.NoError(t, f1.Create(newTask("t3")))

	// Three Acquires with NO checkpoint/terminal event after them: the only
	// record of tokens 1..3 is the observability-only task.acquired.
	_, err := f1.Acquire("t1", "agent-a", time.Minute)
	require.NoError(t, err)
	_, err = f1.Acquire("t2", "agent-a", time.Minute)
	require.NoError(t, err)
	lastPreCrash, err := f1.Acquire("t3", "agent-a", time.Minute)
	require.NoError(t, err)

	f2 := NewFabric().WithEventStore(store)
	require.NoError(t, f2.RestoreFromStore(context.Background()))

	next, err := f2.Acquire("t1", "agent-b", time.Minute)
	require.NoError(t, err)
	require.Greater(t, next, lastPreCrash,
		"restored epoch must dominate tokens recorded only on task.acquired")

	// The stale holder replaying its pre-crash token is rejected.
	require.NoError(t, f2.Start("t1", "agent-b", next))
	require.ErrorIs(t, f2.Complete("t1", "agent-a", lastPreCrash), ErrNotOwner)
}

// TestRestoreFromStoreNoStoreIsNoOp verifies the SDK default path (no event
// store) starts cleanly.
func TestRestoreFromStoreNoStoreIsNoOp(t *testing.T) {
	f := NewFabric()
	require.NoError(t, f.RestoreFromStore(context.Background()))
	require.NoError(t, f.Create(newTask("t1")))
	got, err := f.Task("t1")
	require.NoError(t, err)
	require.Equal(t, StateReady, got.State)
}

// TestRestoreSkipsOrphanEvents verifies one corrupt/incomplete record does
// not abort the rebuild: a checkpointed event for a task with no created
// event is skipped (and logged) while the healthy tasks still restore.
func TestRestoreSkipsOrphanEvents(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	// Hand-craft an orphan checkpointed event with no matching created event.
	require.NoError(t, store.Append(context.Background(), "orphan", []*ares_events.Event{{
		Type:       ares_events.EventTaskCheckpointed,
		StreamID:   "orphan",
		ModuleName: "taskfabric",
		Payload: map[string]any{
			restoreKeyTaskID: "orphan",
			restoreKeyState:  string(StateSuspended),
		},
		Timestamp: time.Now(),
	}}, 0))

	f1 := NewFabric().WithEventStore(store)
	require.NoError(t, f1.Create(newTask("healthy")))

	f2 := NewFabric().WithEventStore(store)
	require.NoError(t, f2.RestoreFromStore(context.Background()))
	_, err := f2.Task("healthy")
	require.NoError(t, err, "healthy tasks must restore despite an orphan record")
	_, err = f2.Task("orphan")
	require.ErrorIs(t, err, ErrTaskNotFound, "orphan must not materialize")
}
