package sub

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/agents/base"
	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/core/models"
)

// panickingExecutor is a TaskExecutor that panics during Execute.
type panickingExecutor struct{}

func (e *panickingExecutor) Execute(_ context.Context, _ *models.Task) (*models.TaskResult, error) {
	panic("intentional test panic")
}

func (e *panickingExecutor) RegisterFallback(_ models.AgentType, _ FallbackHandler) {}

// TestSubAgent_ProcessStream_PanicDoesNotCrash verifies that a panic inside
// the ProcessStream goroutine is recovered, emits EventSubAgentFailed, and
// delivers an error AgentEvent on the channel without crashing the process
func TestSubAgent_ProcessStream_PanicDoesNotCrash(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	exec := &panickingExecutor{}
	handler := NewMessageHandler("sub-panic")

	agent := New("sub-panic", models.AgentTypeTop, exec, handler, nil, nil,
		WithEventStore(store))

	require.NoError(t, agent.Start(context.Background()))

	task := models.NewTask("task-panic-1", models.AgentTypeTop, &models.UserProfile{})
	ch, err := agent.ProcessStream(context.Background(), task)
	require.NoError(t, err)

	// Drain the channel and collect events.
	var sawErrorEvent bool
	for ev := range ch {
		if ev.Type == base.EventComplete && ev.Err != nil {
			sawErrorEvent = true
			assert.Contains(t, ev.Err.Error(), "panic")
		}
	}
	assert.True(t, sawErrorEvent, "channel should deliver an error event containing the panic")

	// Verify EventSubAgentFailed was emitted to the event store.
	evts, err := store.Read(context.Background(), "sub-panic", ares_events.ReadOptions{})
	require.NoError(t, err)

	var sawFailureEvent bool
	for _, ev := range evts {
		if ev.Type == ares_events.EventSubAgentFailed {
			sawFailureEvent = true
			assert.Equal(t, "sub-panic", ev.Payload[KeyAgentID])
			assert.Contains(t, ev.Payload[KeyError], "panic")
		}
	}
	assert.True(t, sawFailureEvent, "EventSubAgentFailed should be emitted on panic")

	// Agent should be back to Ready after recovery.
	assert.Equal(t, models.AgentStatusReady, agent.Status())
}

// TestSubAgent_ProcessStream_Panic_NilEventStore_NoCrash verifies panic recovery
// works even when no event store is configured (emit is a no-op).
func TestSubAgent_ProcessStream_Panic_NilEventStore_NoCrash(t *testing.T) {
	exec := &panickingExecutor{}
	handler := NewMessageHandler("sub-panic2")

	agent := New("sub-panic2", models.AgentTypeTop, exec, handler, nil, nil)

	require.NoError(t, agent.Start(context.Background()))

	task := models.NewTask("task-panic-2", models.AgentTypeTop, &models.UserProfile{})
	ch, err := agent.ProcessStream(context.Background(), task)
	require.NoError(t, err)

	var sawErrorEvent bool
	for ev := range ch {
		if ev.Type == base.EventComplete && ev.Err != nil {
			sawErrorEvent = true
		}
	}
	assert.True(t, sawErrorEvent, "channel should deliver an error event even without event store")
	assert.Equal(t, models.AgentStatusReady, agent.Status())
}
