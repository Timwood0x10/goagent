package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/agents/sub"
)

// The dead-letter observability closure (RUNTIME.md #10): the bus's bounded
// dead-letter store was written on every failure path but had no reader.
// The bridge accessor surfaces the count so serve's periodic log (and any
// future panel) can read it; a failed send is retained, not vanished.

// TestBridgeDeadLetterCountSeesFailedSend pins the read path: an
// undeliverable send lands in the dead-letter store and the bridge's
// accessor observes it.
func TestBridgeDeadLetterCountSeesFailedSend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bridge, err := wireEvolutionIPC(
		[]sub.Agent{&collabStubAgent{id: "peer-live", typ: "research"}},
		nil, nil, nil,
	)
	require.NoError(t, err)

	// Before any failure: zero.
	assert.Equal(t, 0, bridge.DeadLetterCount())

	// A send to an unregistered agent fails and is dead-lettered.
	err = bridge.ipc.Bus().Send(ctx, "coordinator", "peer-ghost", "delegate-task", map[string]any{})
	require.Error(t, err, "sending to an unregistered agent must fail")

	assert.Equal(t, 1, bridge.DeadLetterCount(),
		"the failed send must be retained in the dead-letter store")

	// The snapshot carries the diagnosis fields.
	dl := bridge.ipc.Bus().DeadLetters().Snapshot()
	require.Len(t, dl, 1)
	assert.Equal(t, "coordinator", dl[0].From)
	assert.Equal(t, "peer-ghost", dl[0].To)
	assert.NotEmpty(t, dl[0].Reason)
	assert.False(t, dl[0].At.IsZero())
}

// TestBridgeDeadLetterCountNilSafe pins the zero-value contract.
func TestBridgeDeadLetterCountNilSafe(t *testing.T) {
	var bridge *evolutionIPCBridge
	assert.Equal(t, 0, bridge.DeadLetterCount(), "nil bridge reads as zero, never panics")
}
