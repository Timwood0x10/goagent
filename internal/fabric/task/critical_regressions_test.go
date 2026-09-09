package taskfabric

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// The DEEP_CODE_REVIEW_2026 CRITICAL regressions, pinned: empty-capability
// restore survival (1.3) and nil-Yield checkpoint preservation (1.2).

// TestRestoreEmptyCapabilityTaskSurvives pins 1.3: an unconstrained task
// (empty capability — legal, CapabilityOverlap("") == 1) must survive a
// cross-restart restore. The regression: recordLocked wrote the capability
// restore key only when non-empty, while foldRestoreEvent required the key
// to exist — the task vanished from the rebuilt fabric on every restart.
func TestRestoreEmptyCapabilityTaskSurvives(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	f := NewFabric().WithEventStore(store)

	// An unconstrained task: Capability "" (no scheduler restriction).
	require.NoError(t, f.Create(&Task{ID: "t-open", Capability: ""}))

	// Rebuild from the durable log alone (cross-restart).
	f2 := NewFabric().WithEventStore(store)
	require.NoError(t, f2.RestoreFromStore(context.Background()))

	got, err := f2.Task("t-open")
	require.NoError(t, err, "the empty-capability task must survive the restore")
	assert.Equal(t, "", got.Capability)
	assert.Equal(t, StateReady, got.State, "restored fresh task folds to READY")
}

// TestFabricYieldNilCheckpointPreserves pins 1.2: a nil-checkpoint Yield (a
// progress pause with no new data) must NOT erase the checkpoint saved by a
// previous quantum or the submission envelope. The regression: Yield
// unconditionally assigned, so (nil, false, nil) outcomes wiped
// Payload/StrategyID/SessionID/token accounting.
func TestFabricYieldNilCheckpointPreserves(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	f := NewFabric().WithEventStore(store)

	// Submit with a metadata envelope, drive one quantum to a real yield.
	require.NoError(t, f.Create(&Task{
		ID:         "t-nil-yield",
		Capability: "rust",
		Checkpoint: EncodeCheckpoint(DecodedCheckpoint{
			Payload:     map[string]any{"input": "hello"},
			SessionID:   "sess-1",
			InputTokens: 10,
		}),
	}))
	epoch, err := f.Acquire("t-nil-yield", "agent-a", time.Minute)
	require.NoError(t, err)
	require.NoError(t, f.Start("t-nil-yield", "agent-a", epoch))
	require.NoError(t, f.Yield("t-nil-yield", "agent-a", epoch,
		map[string]any{"progress": "step-1"}))

	// The nil Yield: the quantum made no progress worth persisting. The
	// scheduler's real path is re-acquire → Start (RUNNING) → Yield(nil).
	require.NoError(t, f.Release("t-nil-yield", "agent-a", epoch))
	epoch2, err := f.Acquire("t-nil-yield", "agent-b", time.Minute)
	require.NoError(t, err)
	require.NoError(t, f.Start("t-nil-yield", "agent-b", epoch2))
	require.NoError(t, f.Yield("t-nil-yield", "agent-b", epoch2, nil))

	tk, err := f.Task("t-nil-yield")
	require.NoError(t, err)
	require.Equal(t, StateSuspended, tk.State)

	// The last REAL checkpoint survives; the nil yield did not erase it.
	kept, ok := tk.Checkpoint.(map[string]any)
	require.True(t, ok, "checkpoint must survive a nil yield, got %T", tk.Checkpoint)
	assert.Equal(t, "step-1", kept["progress"])

	// And the envelope's invariants survive the whole cycle (the nil yield
	// must not have dropped the envelope either — Checkpoint here IS the
	// quantum's step checkpoint per Yield's contract; the envelope lives in
	// the scheduler's re-wrap path, asserted in the kernel tests).
}
