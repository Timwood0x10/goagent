package peer

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/runtime/protocol/ahp"
)

// recordingSender captures delivered messages for assertions.
type recordingSender struct {
	mu       sync.Mutex
	received []*ahp.AHPMessage
}

func (s *recordingSender) send(_ context.Context, msg *ahp.AHPMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.received = append(s.received, msg)
	return nil
}

func TestRegistry_RegisterLookupSend(t *testing.T) {
	r := NewRegistry()
	sender := &recordingSender{}

	require.NoError(t, r.Register("agent-b", sender.send))
	fn, ok := r.Lookup("agent-b")
	require.True(t, ok)
	require.NotNil(t, fn)

	msg := ahp.NewMessage(ahp.AHPMethodTask, "agent-a", "agent-b", "task-1", "sess-1")
	require.NoError(t, r.Send(context.Background(), "agent-b", msg))

	sender.mu.Lock()
	defer sender.mu.Unlock()
	require.Len(t, sender.received, 1)
	assert.Equal(t, "agent-b", sender.received[0].TargetAgent, "Send must stamp TargetAgent")
}

func TestRegistry_SendUnknownAgent(t *testing.T) {
	r := NewRegistry()
	msg := ahp.NewMessage(ahp.AHPMethodTask, "a", "ghost", "t", "s")
	err := r.Send(context.Background(), "ghost", msg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not registered")
}

func TestRegistry_RegisterValidation(t *testing.T) {
	r := NewRegistry()
	require.Error(t, r.Register("", func(context.Context, *ahp.AHPMessage) error { return nil }))
	require.Error(t, r.Register("agent-a", nil))
}

func TestRegistry_Unregister(t *testing.T) {
	r := NewRegistry()
	sender := &recordingSender{}
	require.NoError(t, r.Register("agent-b", sender.send))
	require.Len(t, r.IDs(), 1)

	r.Unregister("agent-b")
	_, ok := r.Lookup("agent-b")
	assert.False(t, ok, "agent must be gone after Unregister")
	assert.Empty(t, r.IDs())

	// Unregister of unknown ID is a no-op, not an error.
	r.Unregister("ghost")
}

func TestRegistry_SendErrorPropagates(t *testing.T) {
	r := NewRegistry()
	sendErr := errors.New("delivery failed")
	require.NoError(t, r.Register("agent-b", func(context.Context, *ahp.AHPMessage) error {
		return sendErr
	}))
	msg := ahp.NewMessage(ahp.AHPMethodTask, "a", "agent-b", "t", "s")
	err := r.Send(context.Background(), "agent-b", msg)
	require.ErrorIs(t, err, sendErr, "sender error must propagate")
}

// TestRegistry_SendStampsOverridesInconsistentTarget verifies Send overwrites a
// mismatched TargetAgent on the delivered copy (the previous test used a
// message whose TargetAgent already matched, so it could not prove the stamp
// actually happened).
func TestRegistry_SendStampsOverridesInconsistentTarget(t *testing.T) {
	r := NewRegistry()
	sender := &recordingSender{}
	require.NoError(t, r.Register("agent-b", sender.send))

	// Message claims a DIFFERENT target than the one we send to.
	msg := ahp.NewMessage(ahp.AHPMethodTask, "agent-a", "wrong-target", "task-1", "sess-1")
	require.NoError(t, r.Send(context.Background(), "agent-b", msg))

	sender.mu.Lock()
	defer sender.mu.Unlock()
	require.Len(t, sender.received, 1)
	assert.Equal(t, "agent-b", sender.received[0].TargetAgent,
		"Send must stamp the actual target, overriding the stale value")
}

// TestRegistry_SendDoesNotMutateCallerMessage verifies Send copies the message
// before stamping, so the caller's message (which may be reused concurrently)
// is never modified.
func TestRegistry_SendDoesNotMutateCallerMessage(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register("agent-b", func(context.Context, *ahp.AHPMessage) error { return nil }))

	msg := ahp.NewMessage(ahp.AHPMethodTask, "agent-a", "wrong-target", "task-1", "sess-1")
	require.NoError(t, r.Send(context.Background(), "agent-b", msg))

	assert.Equal(t, "wrong-target", msg.TargetAgent,
		"caller's message must keep its original TargetAgent")
}

// TestRegistry_CloneDoesNotAliasPayload verifies Clone deep-copies the Payload
// map so mutating one never affects the other.
func TestRegistry_CloneDoesNotAliasPayload(t *testing.T) {
	msg := ahp.NewMessage(ahp.AHPMethodTask, "a", "b", "t", "s")
	msg.Payload = map[string]any{"k": "v"}
	clone := msg.Clone()
	clone.Payload["k"] = "changed"
	assert.Equal(t, "v", msg.Payload["k"], "original payload must be untouched")
}

// TestRegistry_IDsSorted verifies IDs returns a deterministic sorted list.
func TestRegistry_IDsSorted(t *testing.T) {
	r := NewRegistry()
	for _, id := range []string{"zebra", "alpha", "mike"} {
		require.NoError(t, r.Register(id, func(context.Context, *ahp.AHPMessage) error { return nil }))
	}
	ids := r.IDs()
	assert.Equal(t, []string{"alpha", "mike", "zebra"}, ids, "IDs must be sorted deterministically")
}
