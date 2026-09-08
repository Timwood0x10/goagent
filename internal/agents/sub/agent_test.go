// nolint: errcheck // Test code may ignore return values
package sub

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/runtime/protocol/ahp"
)

// Test constants
const (
	TestAgentID = "sub1"
	TestTaskID  = "task-1"
)

// stubExecutor is a scripted TaskExecutor for subAgent lifecycle tests
// (replaces the deleted ReAct tool loop in test setups). It reports
// one scripted successful result; failure paths use failingExecutor.
type stubExecutor struct {
	result *models.TaskResult
}

func newStubExecutor() *stubExecutor {
	res := models.NewTaskResult("stub-task", models.AgentTypeTop)
	res.Success = true
	return &stubExecutor{result: res}
}

// Execute implements TaskExecutor.
func (e *stubExecutor) Execute(context.Context, *models.Task) (*models.TaskResult, error) {
	return e.result, nil
}

// RegisterFallback implements TaskExecutor. No-op: no fallback loop.
func (e *stubExecutor) RegisterFallback(models.AgentType, FallbackHandler) {
}

func TestMessageHandler_Handle(t *testing.T) {
	handler := NewMessageHandler("test_agent")

	// Test nil message
	err := handler.Handle(context.Background(), nil)
	if err == nil {
		t.Error("Handle() should return error for nil message")
	}

	// Test valid message
	msg := ahp.NewHeartbeatMessage("test")
	err = handler.Handle(context.Background(), msg)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
}

func TestToolBinder_BindAndCall(t *testing.T) {
	binder := NewToolBinder()

	// Bind a tool
	binder.BindTool("test_tool", func(ctx context.Context, args map[string]any) (any, error) {
		return "test_result", nil
	})

	// Call the tool
	result, err := binder.CallTool(context.Background(), "test_tool", nil)
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}

	if result != "test_result" {
		t.Errorf("CallTool() got %v, want 'test_result'", result)
	}
}

func TestToolBinder_CallNonExistentTool(t *testing.T) {
	binder := NewToolBinder()

	_, err := binder.CallTool(context.Background(), "non_existent", nil)
	if err == nil {
		t.Error("CallTool() should return error for non-existent tool")
	}
}

func TestHeartbeatSender_StartStop(t *testing.T) {
	sender := NewHeartbeatSender("test_agent", 100, nil)

	ctx, cancel := context.WithCancel(context.Background())

	go sender.Start(ctx)

	// Let it run briefly
	cancel()

	sender.Stop()
}

func TestSubAgent_New(t *testing.T) {
	executor := newStubExecutor()
	handler := NewMessageHandler(TestAgentID)

	agent := New(TestAgentID, models.AgentTypeTop, executor, handler, nil, nil)

	if agent.ID() != TestAgentID {
		t.Errorf("expected %s, got %s", TestAgentID, agent.ID())
	}
	if agent.Type() != models.AgentTypeTop {
		t.Errorf("expected AgentTypeTop")
	}
}

func TestSubAgent_DefaultConfig(t *testing.T) {
	cfg := DefaultSubAgentConfig(models.AgentTypeTop)

	if cfg.Type != models.AgentTypeTop {
		t.Errorf("expected AgentTypeTop")
	}
}

func TestSubAgent_StartStop(t *testing.T) {
	executor := newStubExecutor()
	handler := NewMessageHandler(TestAgentID)

	agent := New(TestAgentID, models.AgentTypeTop, executor, handler, nil, nil)

	// Start
	err := agent.Start(context.Background())
	if err != nil {
		t.Errorf("Start() error = %v", err)
	}

	if agent.Status() != models.AgentStatusReady {
		t.Errorf("expected status Ready after Start")
	}

	// Start again should fail
	err = agent.Start(context.Background())
	if err == nil {
		t.Error("Start() should return error when already started")
	}

	// Stop
	err = agent.Stop(context.Background())
	if err != nil {
		t.Errorf("Stop() error = %v", err)
	}

	if agent.Status() != models.AgentStatusOffline {
		t.Errorf("expected status Offline after Stop")
	}

	// Stop again should fail
	err = agent.Stop(context.Background())
	if err == nil {
		t.Error("Stop() should return error when not running")
	}
}

func TestSubAgent_Process(t *testing.T) {
	executor := newStubExecutor()
	handler := NewMessageHandler(TestAgentID)

	agent := New(TestAgentID, models.AgentTypeTop, executor, handler, nil, nil)

	// Process without starting should auto-start
	task := models.NewTask("task_1", models.AgentTypeTop, &models.UserProfile{})
	result, err := agent.Process(context.Background(), task)
	if err != nil {
		t.Errorf("Process() error = %v", err)
	}
	_ = result
}

func TestSubAgent_Heartbeat(t *testing.T) {
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")
	hbMon := ahp.NewHeartbeatMonitor(ahp.DefaultHeartbeatConfig())

	sub := &subAgent{
		id:           TestAgentID,
		agentType:    models.AgentTypeTop,
		status:       models.AgentStatusReady,
		executor:     executor,
		handler:      handler,
		tools:        make(map[string]func(ctx context.Context, args map[string]any) (any, error)),
		heartbeatMon: hbMon,
	}

	err := sub.Heartbeat(context.Background())
	if err != nil {
		t.Errorf("Heartbeat() error = %v", err)
	}

	if !sub.IsAlive() {
		t.Error("IsAlive() should return true after heartbeat")
	}
}

func TestSubAgent_Execute(t *testing.T) {
	executor := newStubExecutor()
	handler := NewMessageHandler(TestAgentID)

	agent := New(TestAgentID, models.AgentTypeTop, executor, handler, nil, nil)

	task := models.NewTask("task_1", models.AgentTypeTop, &models.UserProfile{})
	result, err := agent.Execute(context.Background(), task)
	if err != nil {
		t.Errorf("Execute() error = %v", err)
	}
	if result == nil {
		t.Error("Execute() should return result")
	}
}

func TestToolBinder_ListTools(t *testing.T) {
	binder := NewToolBinder()

	binder.BindTool("tool1", func(ctx context.Context, args map[string]any) (any, error) {
		return nil, errors.New("not implemented")
	})
	binder.BindTool("tool2", func(ctx context.Context, args map[string]any) (any, error) {
		return nil, errors.New("not implemented")
	})

	// ListTools is not implemented, so just test that tools can be bound and called
	// The tool intentionally returns "not implemented" error, which is expected
	result, err := binder.CallTool(context.Background(), "tool1", nil)
	if err == nil {
		t.Error("CallTool() expected error 'not implemented', got nil")
	}
	if err != nil && err.Error() != "not implemented" {
		t.Errorf("CallTool() error = %v, want 'not implemented'", err)
	}
	if result != nil {
		t.Errorf("CallTool() got %v, want nil", result)
	}
}

func TestMessageHandler_HandleTaskMessage(t *testing.T) {
	handler := NewMessageHandler("test_agent")

	// Create a task message
	msg := ahp.NewTaskMessage("leader", "test_agent", "task1", "session1", map[string]any{"key": "value"})

	// Handle the task message - will fail since executor is nil
	err := handler.Handle(context.Background(), msg)
	// Error expected since there's no executor
	_ = err
}

func TestMessageHandler_HandleAckMessage(t *testing.T) {
	handler := NewMessageHandler("test_agent")

	// Create an ACK message
	msg := ahp.NewACKMessage("test_agent", "leader", "task1", "session1")

	// Handle the ACK message
	err := handler.Handle(context.Background(), msg)
	if err != nil {
		t.Errorf("Handle() error = %v", err)
	}
}

// --- StatefulAgent implementation tests ---

func TestSubAgent_ImplementsStatefulAgent(t *testing.T) {
	// Compile-time check is enforced by the package-level var declaration.
	// This test verifies the interface at runtime as well.
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	agent := New("sub1", models.AgentTypeTop, executor, handler, nil, nil)

	_, ok := agent.(interface {
		RestoreState(map[string]any) error
		ReplayEvents([]*ares_events.Event) error
		Snapshot() (map[string]any, error)
	})
	assert.True(t, ok, "subAgent should implement StatefulAgent methods")
}

func TestSubAgent_RestoreState_NilState(t *testing.T) {
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	agent := New("sub1", models.AgentTypeTop, executor, handler, nil, nil)
	a := agent.(*subAgent)

	err := a.RestoreState(nil)
	assert.NoError(t, err, "RestoreState with nil should not error")
	assert.Equal(t, models.AgentStatusOffline, a.Status())
}

func TestSubAgent_RestoreState_ValidStatus(t *testing.T) {
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	agent := New("sub1", models.AgentTypeTop, executor, handler, nil, nil)
	a := agent.(*subAgent)

	err := a.RestoreState(map[string]any{
		"status": string(models.AgentStatusReady),
	})
	assert.NoError(t, err)
	assert.Equal(t, models.AgentStatusReady, a.Status())
}

func TestSubAgent_RestoreState_EmptyStatusIgnored(t *testing.T) {
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	agent := New("sub1", models.AgentTypeTop, executor, handler, nil, nil)
	a := agent.(*subAgent)

	err := a.RestoreState(map[string]any{
		"status": "",
	})
	assert.NoError(t, err)
	assert.Equal(t, models.AgentStatusOffline, a.Status(),
		"empty status should not overwrite current status")
}

func TestSubAgent_RestoreState_IgnoresNonStringStatus(t *testing.T) {
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	agent := New("sub1", models.AgentTypeTop, executor, handler, nil, nil)
	a := agent.(*subAgent)

	err := a.RestoreState(map[string]any{
		"status": 12345, // not a string
	})
	assert.NoError(t, err)
	assert.Equal(t, models.AgentStatusOffline, a.Status(),
		"non-string status should be ignored")
}

func TestSubAgent_RestoreState_IgnoresExtraKeys(t *testing.T) {
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	agent := New("sub1", models.AgentTypeTop, executor, handler, nil, nil)
	a := agent.(*subAgent)

	err := a.RestoreState(map[string]any{
		"status":      string(models.AgentStatusBusy),
		"unknown_key": "value",
	})
	assert.NoError(t, err)
	assert.Equal(t, models.AgentStatusBusy, a.Status())
}

func TestSubAgent_RestoreState_EmptyMap(t *testing.T) {
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	agent := New("sub1", models.AgentTypeTop, executor, handler, nil, nil)
	a := agent.(*subAgent)

	err := a.RestoreState(map[string]any{})
	assert.NoError(t, err)
	assert.Equal(t, models.AgentStatusOffline, a.Status())
}

func TestSubAgent_ReplayEvents_Empty(t *testing.T) {
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	agent := New("sub1", models.AgentTypeTop, executor, handler, nil, nil)
	a := agent.(*subAgent)

	err := a.ReplayEvents(nil)
	assert.NoError(t, err, "ReplayEvents with nil should not error")

	err = a.ReplayEvents([]*ares_events.Event{})
	assert.NoError(t, err, "ReplayEvents with empty slice should not error")
}

func TestSubAgent_ReplayEvents_NilEventSkipped(t *testing.T) {
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	agent := New("sub1", models.AgentTypeTop, executor, handler, nil, nil)
	a := agent.(*subAgent)

	err := a.ReplayEvents([]*ares_events.Event{nil, nil})
	assert.NoError(t, err, "nil ares_events should be skipped without panic")
}

func TestSubAgent_ReplayEvents_TaskCompleted(t *testing.T) {
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	agent := New("sub1", models.AgentTypeTop, executor, handler, nil, nil)
	a := agent.(*subAgent)

	evts := []*ares_events.Event{
		{
			Type: ares_events.EventTaskCompleted,
			Payload: map[string]any{
				KeyTaskID: TestTaskID,
			},
		},
		{
			Type: ares_events.EventTaskCompleted,
			Payload: map[string]any{
				KeyTaskID: "task-2",
			},
		},
	}

	err := a.ReplayEvents(evts)
	assert.NoError(t, err, "ReplayEvents should succeed for task completion ares_events")
}

func TestSubAgent_ReplayEvents_UnknownEventTypeIgnored(t *testing.T) {
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	agent := New("sub1", models.AgentTypeTop, executor, handler, nil, nil)
	a := agent.(*subAgent)

	evts := []*ares_events.Event{
		{
			Type:    ares_events.EventAgentStarted,
			Payload: map[string]any{},
		},
		{
			Type: ares_events.EventTaskCreated,
			Payload: map[string]any{
				KeyTaskID: TestTaskID,
			},
		},
	}

	err := a.ReplayEvents(evts)
	assert.NoError(t, err, "unknown event types should be silently ignored")
}

func TestSubAgent_Snapshot_OfflineStatus(t *testing.T) {
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	agent := New("sub1", models.AgentTypeTop, executor, handler, nil, nil)
	a := agent.(*subAgent)

	snap, err := a.Snapshot()
	require.NoError(t, err)
	assert.Equal(t, TestAgentID, snap[KeyAgentID])
	assert.Equal(t, string(models.AgentStatusOffline), snap[KeyStatus])
}

func TestSubAgent_Snapshot_ReadyStatus(t *testing.T) {
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	agent := New("sub1", models.AgentTypeTop, executor, handler, nil, nil)
	a := agent.(*subAgent)

	_ = a.Start(context.Background())

	snap, err := a.Snapshot()
	require.NoError(t, err)
	assert.Equal(t, TestAgentID, snap[KeyAgentID])
	assert.Equal(t, string(models.AgentStatusReady), snap[KeyStatus])
}

func TestSubAgent_Snapshot_ReturnsCopy(t *testing.T) {
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	agent := New("sub1", models.AgentTypeTop, executor, handler, nil, nil)
	a := agent.(*subAgent)

	snap1, _ := a.Snapshot()
	snap2, _ := a.Snapshot()

	// Mutate snap1 and verify snap2 is unaffected.
	snap1[KeyStatus] = "mutated"
	assert.NotEqual(t, snap1[KeyStatus], snap2[KeyStatus],
		"Snapshot should return independent copies")
}

func TestSubAgent_WithEventStore(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	agent := New(TestAgentID, models.AgentTypeTop, executor, handler, nil, nil,
		WithEventStore(store))
	a := agent.(*subAgent)

	assert.NotNil(t, a.eventStore, "WithEventStore should set eventStore")
}

func TestSubAgent_EmitEvent_WithStore(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	agent := New(TestAgentID, models.AgentTypeTop, executor, handler, nil, nil,
		WithEventStore(store))
	a := agent.(*subAgent)

	a.emitEvent(context.Background(), ares_events.EventTaskCompleted, map[string]any{
		KeyTaskID: TestTaskID,
	})

	// Verify the event was stored.
	evts, err := store.Read(context.Background(), TestAgentID, ares_events.ReadOptions{})
	require.NoError(t, err)
	require.Len(t, evts, 1)
	assert.Equal(t, ares_events.EventTaskCompleted, evts[0].Type)
	assert.Equal(t, TestTaskID, evts[0].Payload[KeyTaskID])
	assert.Equal(t, TestAgentID, evts[0].StreamID)
}

func TestSubAgent_EmitEvent_NilStore(t *testing.T) {
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	agent := New(TestAgentID, models.AgentTypeTop, executor, handler, nil, nil)
	a := agent.(*subAgent)

	// Should not panic when eventStore is nil.
	a.emitEvent(context.Background(), ares_events.EventTaskCompleted, map[string]any{
		KeyTaskID: TestTaskID,
	})
}

func TestSubAgent_EmitEvent_NilPayload(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	agent := New("sub1", models.AgentTypeTop, executor, handler, nil, nil,
		WithEventStore(store))
	a := agent.(*subAgent)

	// Should handle nil payload without panic.
	a.emitEvent(context.Background(), ares_events.EventAgentStarted, nil)

	evts, err := store.Read(context.Background(), "sub1", ares_events.ReadOptions{})
	require.NoError(t, err)
	require.Len(t, evts, 1)
	assert.Equal(t, ares_events.EventAgentStarted, evts[0].Type)
	assert.Nil(t, evts[0].Payload)
}

func TestSubAgent_RestoreAndSnapshot_Roundtrip(t *testing.T) {
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	agent := New("sub1", models.AgentTypeTop, executor, handler, nil, nil)
	a := agent.(*subAgent)

	// Restore state.
	err := a.RestoreState(map[string]any{
		"status": string(models.AgentStatusBusy),
	})
	require.NoError(t, err)

	// Take snapshot and verify roundtrip.
	snap, err := a.Snapshot()
	require.NoError(t, err)
	assert.Equal(t, string(models.AgentStatusBusy), snap["status"])
	assert.Equal(t, "sub1", snap["agent_id"])
}

func TestSubAgent_StatefulAgent_ConcurrentAccess(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	agent := New("sub1", models.AgentTypeTop, executor, handler, nil, nil,
		WithEventStore(store))
	a := agent.(*subAgent)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			_ = a.RestoreState(map[string]any{
				"status": string(models.AgentStatusReady),
			})
		}()
		go func() {
			defer wg.Done()
			_, _ = a.Snapshot()
		}()
		go func() {
			defer wg.Done()
			a.emitEvent(context.Background(), ares_events.EventTaskCompleted, map[string]any{
				"task_id": "task-concurrent",
			})
		}()
	}
	wg.Wait()
}

// failingExecutor is a TaskExecutor that always returns an error.
type failingExecutor struct {
	err error
}

func (e *failingExecutor) Execute(_ context.Context, _ *models.Task) (*models.TaskResult, error) {
	return nil, e.err
}

func (e *failingExecutor) RegisterFallback(_ models.AgentType, _ FallbackHandler) {}

func TestSubAgent_Start_EmitsAgentStartedEvent(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	agent := New("sub1", models.AgentTypeTop, executor, handler, nil, nil,
		WithEventStore(store))

	err := agent.Start(context.Background())
	require.NoError(t, err)

	evts, err := store.Read(context.Background(), "sub1", ares_events.ReadOptions{})
	require.NoError(t, err)
	require.Len(t, evts, 1)
	assert.Equal(t, ares_events.EventAgentStarted, evts[0].Type)
	assert.Equal(t, "sub1", evts[0].Payload["agent_id"])
	assert.Equal(t, string(models.AgentTypeTop), evts[0].Payload["type"])
}

func TestSubAgent_Stop_EmitsAgentStoppedEvent(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	agent := New("sub1", models.AgentTypeTop, executor, handler, nil, nil,
		WithEventStore(store))

	require.NoError(t, agent.Start(context.Background()))

	err := agent.Stop(context.Background())
	require.NoError(t, err)

	evts, err := store.Read(context.Background(), "sub1", ares_events.ReadOptions{})
	require.NoError(t, err)
	// Should have EventAgentStarted and EventAgentStopped.
	require.Len(t, evts, 2)
	assert.Equal(t, ares_events.EventAgentStarted, evts[0].Type)
	assert.Equal(t, ares_events.EventAgentStopped, evts[1].Type)
	assert.Equal(t, "sub1", evts[1].Payload["agent_id"])
}

func TestSubAgent_Execute_Success_EmitsTaskEvents(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	agent := New("sub1", models.AgentTypeTop, executor, handler, nil, nil,
		WithEventStore(store))

	task := models.NewTask("task-1", models.AgentTypeTop, &models.UserProfile{})
	_, err := agent.Execute(context.Background(), task)
	require.NoError(t, err)

	evts, err := store.Read(context.Background(), "sub1", ares_events.ReadOptions{})
	require.NoError(t, err)
	require.Len(t, evts, 2)

	assert.Equal(t, ares_events.EventTaskCreated, evts[0].Type)
	assert.Equal(t, "task-1", evts[0].Payload["task_id"])
	assert.Equal(t, "sub1", evts[0].Payload["agent_id"])

	assert.Equal(t, ares_events.EventTaskCompleted, evts[1].Type)
	assert.Equal(t, "task-1", evts[1].Payload["task_id"])
	assert.Equal(t, "sub1", evts[1].Payload["agent_id"])
}

func TestSubAgent_Execute_Failure_EmitsTaskFailedEvent(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	exec := &failingExecutor{err: assert.AnError}
	handler := NewMessageHandler("sub1")

	agent := New("sub1", models.AgentTypeTop, exec, handler, nil, nil,
		WithEventStore(store))

	task := models.NewTask("task-1", models.AgentTypeTop, &models.UserProfile{})
	_, err := agent.Execute(context.Background(), task)
	require.Error(t, err)

	evts, err := store.Read(context.Background(), "sub1", ares_events.ReadOptions{})
	require.NoError(t, err)
	require.Len(t, evts, 2)

	assert.Equal(t, ares_events.EventTaskCreated, evts[0].Type)
	assert.Equal(t, "task-1", evts[0].Payload["task_id"])

	assert.Equal(t, ares_events.EventTaskFailed, evts[1].Type)
	assert.Equal(t, "task-1", evts[1].Payload["task_id"])
	assert.Equal(t, "sub1", evts[1].Payload["agent_id"])
	assert.NotEmpty(t, evts[1].Payload["error"])
}

func TestSubAgent_ProcessStream_EmitsTaskEvents(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	agent := New("sub1", models.AgentTypeTop, executor, handler, nil, nil,
		WithEventStore(store))

	// Start agent first so ProcessStream does not auto-start (which adds an extra event).
	require.NoError(t, agent.Start(context.Background()))

	task := models.NewTask("task-stream-1", models.AgentTypeTop, &models.UserProfile{})
	ch, err := agent.ProcessStream(context.Background(), task)
	require.NoError(t, err)

	// Drain the channel.
	for range ch {
	}

	// Events: EventAgentStarted (from Start), EventTaskCreated, EventTaskCompleted.
	// (The executor-defer EventSubTaskResult died with the tool loop.
	// No production consumer matched its shape — the skills recorder reads
	// Payload["task"]/["success"], which that event never carried.)
	evts, err := store.Read(context.Background(), "sub1", ares_events.ReadOptions{})
	require.NoError(t, err)
	require.Len(t, evts, 3)

	assert.Equal(t, ares_events.EventAgentStarted, evts[0].Type)
	assert.Equal(t, ares_events.EventTaskCreated, evts[1].Type)
	assert.Equal(t, "task-stream-1", evts[1].Payload["task_id"])
	assert.Equal(t, "sub1", evts[1].Payload["agent_id"])

	assert.Equal(t, ares_events.EventTaskCompleted, evts[2].Type)
	assert.Equal(t, "task-stream-1", evts[2].Payload["task_id"])
}

func TestSubAgent_ProcessStream_Failure_EmitsTaskFailedEvent(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	exec := &failingExecutor{err: assert.AnError}
	handler := NewMessageHandler("sub1")

	agent := New("sub1", models.AgentTypeTop, exec, handler, nil, nil,
		WithEventStore(store))

	// Start agent first so ProcessStream does not auto-start (which adds an extra event).
	require.NoError(t, agent.Start(context.Background()))

	task := models.NewTask("task-stream-fail", models.AgentTypeTop, nil)
	ch, err := agent.ProcessStream(context.Background(), task)
	require.NoError(t, err)

	// Drain the channel.
	for range ch {
	}

	// Events: EventAgentStarted (from Start), EventTaskCreated, EventTaskFailed.
	evts, err := store.Read(context.Background(), "sub1", ares_events.ReadOptions{})
	require.NoError(t, err)
	require.Len(t, evts, 3)

	assert.Equal(t, ares_events.EventAgentStarted, evts[0].Type)
	assert.Equal(t, ares_events.EventTaskCreated, evts[1].Type)
	assert.Equal(t, "task-stream-fail", evts[1].Payload["task_id"])

	assert.Equal(t, ares_events.EventTaskFailed, evts[2].Type)
	assert.Equal(t, "task-stream-fail", evts[2].Payload["task_id"])
	assert.NotEmpty(t, evts[2].Payload["error"])
}

func TestSubAgent_Execute_NilEventStore_NoPanic(t *testing.T) {
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	// No WithEventStore — eventStore is nil.
	agent := New("sub1", models.AgentTypeTop, executor, handler, nil, nil)

	task := models.NewTask("task-1", models.AgentTypeTop, &models.UserProfile{})
	_, err := agent.Execute(context.Background(), task)
	require.NoError(t, err, "Execute should succeed even without event store")
}

func TestSubAgent_FullLifecycle_EmitsAllEvents(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	executor := newStubExecutor()
	handler := NewMessageHandler("sub1")

	agent := New("sub1", models.AgentTypeTop, executor, handler, nil, nil,
		WithEventStore(store))

	// Start.
	require.NoError(t, agent.Start(context.Background()))

	// Execute a task.
	task := models.NewTask("task-lifecycle", models.AgentTypeTop, &models.UserProfile{})
	_, err := agent.Execute(context.Background(), task)
	require.NoError(t, err)

	// Stop.
	require.NoError(t, agent.Stop(context.Background()))

	evts, err := store.Read(context.Background(), "sub1", ares_events.ReadOptions{})
	require.NoError(t, err)
	require.Len(t, evts, 4)

	assert.Equal(t, ares_events.EventAgentStarted, evts[0].Type)
	assert.Equal(t, ares_events.EventTaskCreated, evts[1].Type)
	assert.Equal(t, ares_events.EventTaskCompleted, evts[2].Type)
	assert.Equal(t, ares_events.EventAgentStopped, evts[3].Type)
}

// nolint: errcheck // Test code may ignore return values
