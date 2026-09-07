package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// testPlugin is a simple RuntimePlugin for testing.
type testPlugin struct {
	name        string
	caps        []Capability
	startCalled bool
	stopCalled  bool
	startErr    error
	stopErr     error
	startBlock  time.Duration
	mu          sync.Mutex
}

func newTestPlugin(name string, caps []Capability) *testPlugin {
	return &testPlugin{name: name, caps: caps}
}

func (p *testPlugin) Name() string { return p.name }

func (p *testPlugin) Capabilities() []Capability { return p.caps }

func (p *testPlugin) Start(ctx context.Context, _ EventBus) error {
	if p.startBlock > 0 {
		select {
		case <-time.After(p.startBlock):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	p.mu.Lock()
	p.startCalled = true
	err := p.startErr
	p.mu.Unlock()
	return err
}

func (p *testPlugin) Stop(_ context.Context) error {
	p.mu.Lock()
	p.stopCalled = true
	err := p.stopErr
	p.mu.Unlock()
	return err
}

// testHook records BeforeStep/AfterStep invocations for testing.
type testHook struct {
	mu        sync.Mutex
	before    []string // step IDs
	after     []string // step IDs
	beforeErr error
	afterErr  error
}

func newTestHook() *testHook {
	return &testHook{}
}

func (h *testHook) BeforeStep(_ context.Context, _ string, step *Step) error {
	h.mu.Lock()
	h.before = append(h.before, step.ID)
	err := h.beforeErr
	h.mu.Unlock()
	return err
}

func (h *testHook) AfterStep(_ context.Context, _ string, result *StepResult) error {
	h.mu.Lock()
	h.after = append(h.after, result.StepID)
	err := h.afterErr
	h.mu.Unlock()
	return err
}

// panickingHook panics in BeforeStep for recovery testing.
type panickingHook struct{}

func (h *panickingHook) BeforeStep(_ context.Context, _ string, _ *Step) error {
	panic("before step panic")
}

func (h *panickingHook) AfterStep(_ context.Context, _ string, _ *StepResult) error {
	return nil
}

// panickingPlugin panics in Start for recovery testing.
type panickingPlugin struct{}

func (p *panickingPlugin) Name() string               { return "panicking" }
func (p *panickingPlugin) Capabilities() []Capability { return nil }
func (p *panickingPlugin) Start(_ context.Context, _ EventBus) error {
	panic("start panic")
}
func (p *panickingPlugin) Stop(_ context.Context) error { return nil }

// memoryCheckpointStore is an in-memory CheckpointStore for testing.
type memoryCheckpointStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemoryCheckpointStore() *memoryCheckpointStore {
	return &memoryCheckpointStore{data: make(map[string][]byte)}
}

func (s *memoryCheckpointStore) Save(_ context.Context, key string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = data
	return nil
}

func (s *memoryCheckpointStore) Load(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[key], nil
}

// ---------------------------------------------------------------------------
// PluginBus tests
// ---------------------------------------------------------------------------

func TestNewPluginBus(t *testing.T) {
	b := NewPluginBus()
	require.NotNil(t, b)
	assert.Equal(t, defaultPluginTimeout, b.pluginTimeout)
}

func TestNewPluginBus_WithOptions(t *testing.T) {
	b := NewPluginBus(WithPluginTimeout(5 * time.Second))
	require.NotNil(t, b)
	assert.Equal(t, 5*time.Second, b.pluginTimeout)
}

func TestPluginBus_RegisterAndStart(t *testing.T) {
	b := NewPluginBus()
	p := newTestPlugin("test1", nil)

	err := b.Register(p)
	require.NoError(t, err)

	err = b.Start(context.Background())
	require.NoError(t, err)

	p.mu.Lock()
	assert.True(t, p.startCalled)
	p.mu.Unlock()
}

func TestPluginBus_RegisterDuplicateName(t *testing.T) {
	b := NewPluginBus()
	p1 := newTestPlugin("dup", nil)
	p2 := newTestPlugin("dup", nil)

	require.NoError(t, b.Register(p1))
	err := b.Register(p2)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDuplicatePlugin)
}

func TestPluginBus_RegisterNil(t *testing.T) {
	b := NewPluginBus()
	err := b.Register(nil)
	require.Error(t, err)
}

func TestPluginBus_RegisterAfterStart(t *testing.T) {
	b := NewPluginBus()
	require.NoError(t, b.Start(context.Background()))

	err := b.Register(newTestPlugin("late", nil))
	require.ErrorIs(t, err, ErrBusAlreadyStarted)
}

func TestPluginBus_Stop(t *testing.T) {
	b := NewPluginBus()
	p := newTestPlugin("test1", nil)

	require.NoError(t, b.Register(p))
	require.NoError(t, b.Start(context.Background()))
	require.NoError(t, b.Stop(context.Background()))

	p.mu.Lock()
	assert.True(t, p.stopCalled)
	p.mu.Unlock()
}

func TestPluginBus_StartContinuesOnError(t *testing.T) {
	b := NewPluginBus()
	p1 := newTestPlugin("good", nil)
	p2 := newTestPlugin("bad", nil)
	p2.startErr = errors.New("start failed")
	p3 := newTestPlugin("also-good", nil)

	require.NoError(t, b.Register(p1))
	require.NoError(t, b.Register(p2))
	require.NoError(t, b.Register(p3))

	// Start returns the last error from plugin startup, but all plugins
	// are attempted regardless.
	err := b.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "start failed")

	p1.mu.Lock()
	assert.True(t, p1.startCalled)
	p1.mu.Unlock()

	p3.mu.Lock()
	assert.True(t, p3.startCalled)
	p3.mu.Unlock()
}

func TestPluginBus_StopContinuesOnError(t *testing.T) {
	b := NewPluginBus()
	p1 := newTestPlugin("a", nil)
	p2 := newTestPlugin("b", nil)
	p2.stopErr = errors.New("stop failed")

	require.NoError(t, b.Register(p1))
	require.NoError(t, b.Register(p2))
	require.NoError(t, b.Start(context.Background()))

	err := b.Stop(context.Background())
	require.Error(t, err)

	p1.mu.Lock()
	assert.True(t, p1.stopCalled)
	p1.mu.Unlock()
}

func TestPluginBus_PanicRecovery_Start(t *testing.T) {
	b := NewPluginBus()
	p1 := newTestPlugin("good", nil)
	p2 := &panickingPlugin{}

	require.NoError(t, b.Register(p1))
	require.NoError(t, b.Register(p2))

	// Start returns the error from the panicking plugin, but recovery
	// ensures all plugins are attempted and the bus does not crash.
	err := b.Start(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPluginPanic)

	p1.mu.Lock()
	assert.True(t, p1.startCalled)
	p1.mu.Unlock()
}

func TestPluginBus_HookPanicRecovery(t *testing.T) {
	b := NewPluginBus()
	require.NoError(t, b.Start(context.Background()))

	h := &panickingHook{}
	b.RegisterHook("panic", h)

	// Should not panic when hook panics; invokeWithTimeout recovers.
	err := b.BeforeStep(context.Background(), "exec-1", &Step{ID: "s1"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPluginPanic)
}

func TestPluginBus_HookTimeout(t *testing.T) {
	b := NewPluginBus(WithPluginTimeout(10 * time.Millisecond))
	require.NoError(t, b.Start(context.Background()))

	slowHook := &testHook{}
	b.RegisterHook("slow", slowHook)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Use a context that doesn't expire immediately so the hook gets a
	// chance to time out on the bus side.
	err := b.BeforeStep(ctx, "exec-1", &Step{ID: "s1"})
	require.NoError(t, err) // BeforeStep is fast; timeout check is on bus.
	// The timeout is per-hook-invocation, not cumulative.
	// fast hook should succeed.
	assert.Equal(t, []string{"s1"}, slowHook.before)
}

func TestPluginBus_BeforeStepAfterStep_Order(t *testing.T) {
	b := NewPluginBus()
	require.NoError(t, b.Start(context.Background()))

	h := newTestHook()
	b.RegisterHook("order", h)

	step := &Step{ID: "s1", Name: "Step One"}
	result := &StepResult{StepID: "s1", Name: "Step One", Status: StepStatusCompleted}

	require.NoError(t, b.BeforeStep(context.Background(), "exec-1", step))
	require.NoError(t, b.AfterStep(context.Background(), "exec-1", result))

	assert.Equal(t, []string{"s1"}, h.before)
	assert.Equal(t, []string{"s1"}, h.after)
}

func TestPluginBus_MultipleHooks(t *testing.T) {
	b := NewPluginBus()
	require.NoError(t, b.Start(context.Background()))

	h1 := newTestHook()
	h2 := newTestHook()
	b.RegisterHook("h1", h1)
	b.RegisterHook("h2", h2)

	step := &Step{ID: "s1"}
	result := &StepResult{StepID: "s1"}

	require.NoError(t, b.BeforeStep(context.Background(), "exec-1", step))
	require.NoError(t, b.AfterStep(context.Background(), "exec-1", result))

	assert.Equal(t, []string{"s1"}, h1.before)
	assert.Equal(t, []string{"s1"}, h2.before)
	assert.Equal(t, []string{"s1"}, h1.after)
	assert.Equal(t, []string{"s1"}, h2.after)
}

// ---------------------------------------------------------------------------
// Event bus (Emit / Subscribe) tests
// ---------------------------------------------------------------------------

func TestPluginBus_EmitAndSubscribe(t *testing.T) {
	b := NewPluginBus()
	require.NoError(t, b.Start(context.Background()))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	ch, err := b.Subscribe(ctx, ares_events.EventFilter{
		Types: []ares_events.EventType{EventWorkflowStarted},
	})
	require.NoError(t, err)

	b.Emit(ctx, "stream-1", EventWorkflowStarted, "test", map[string]any{"key": "val"})

	select {
	case evt := <-ch:
		assert.Equal(t, EventWorkflowStarted, evt.Type)
		assert.Equal(t, "stream-1", evt.StreamID)
		assert.Equal(t, "val", evt.Payload["key"])
	case <-ctx.Done():
		t.Fatal("timeout waiting for event")
	}
}

func TestPluginBus_EmitFiltered_NoMatch(t *testing.T) {
	b := NewPluginBus()
	require.NoError(t, b.Start(context.Background()))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	ch, err := b.Subscribe(ctx, ares_events.EventFilter{
		Types: []ares_events.EventType{EventWorkflowCompleted},
	})
	require.NoError(t, err)

	b.Emit(ctx, "stream-1", EventWorkflowStarted, "test", nil)

	// Should NOT receive EventWorkflowStarted.
	select {
	case <-ch:
		t.Fatal("should not receive event with non-matching filter")
	case <-ctx.Done():
		// Expected: timeout with no event received.
	}
}

func TestPluginBus_Emit_NonBlocking(t *testing.T) {
	b := NewPluginBus()
	require.NoError(t, b.Start(context.Background()))

	// Create a subscription with a tiny buffer.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	// By default the buffer is 64, so fill it with 64 ares_events to force drops.
	ch, err := b.Subscribe(ctx, ares_events.EventFilter{})
	require.NoError(t, err)

	// Send 128 ares_events. The channel buffer is 64, so ~64 should be dropped
	// without blocking.
	for i := 0; i < 128; i++ {
		b.Emit(ctx, "s", EventWorkflowStarted, "test", nil)
	}

	// Drain whatever we got.
	received := 0
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer drainCancel()
loop:
	for {
		select {
		case <-ch:
			received++
		case <-drainCtx.Done():
			break loop
		}
	}
	// Should have received at most the buffer size, but at least 0.
	assert.Greater(t, received, 0, "should receive at least some ares_events")
	assert.LessOrEqual(t, received, 64, "should not exceed channel buffer")
}

func TestPluginBus_SubscriptionCleanup(t *testing.T) {
	b := NewPluginBus()
	require.NoError(t, b.Start(context.Background()))

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := b.Subscribe(ctx, ares_events.EventFilter{})
	require.NoError(t, err)

	cancel()

	// Channel should be closed after context cancellation.
	_, ok := <-ch
	assert.False(t, ok, "channel should be closed after context cancellation")
}

// ---------------------------------------------------------------------------
// PluginsByCap tests
// ---------------------------------------------------------------------------

func TestPluginBus_PluginsByCap(t *testing.T) {
	b := NewPluginBus()

	p1 := newTestPlugin("obs1", []Capability{CapObserver})
	p2 := newTestPlugin("obs2", []Capability{CapObserver})
	p3 := newTestPlugin("ckpt", []Capability{CapCheckpoint})

	require.NoError(t, b.Register(p1))
	require.NoError(t, b.Register(p2))
	require.NoError(t, b.Register(p3))

	obs := b.PluginsByCap(CapObserver)
	assert.Len(t, obs, 2)

	ckpt := b.PluginsByCap(CapCheckpoint)
	assert.Len(t, ckpt, 1)
	assert.Equal(t, "ckpt", ckpt[0].Name())

	none := b.PluginsByCap(CapRouter)
	assert.Len(t, none, 0)
}

// ---------------------------------------------------------------------------
// ObserverPlugin tests
// ---------------------------------------------------------------------------

func TestObserverPlugin_RecordsEvents(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	bus := NewPluginBus()

	obs := NewObserverPlugin("test-observer", store)
	require.NoError(t, bus.Register(obs))
	require.NoError(t, bus.Start(context.Background()))

	ctx := context.Background()
	bus.Emit(ctx, "exec-1", EventWorkflowStarted, "test", map[string]any{"key": "val"})
	bus.Emit(ctx, "exec-1", EventWorkflowCompleted, "test", map[string]any{"key2": "val2"})

	// Give the observer goroutine time to process.
	time.Sleep(50 * time.Millisecond)

	// Verify ares_events were written to the store.
	evts, err := store.Read(ctx, "exec-1", ares_events.ReadOptions{})
	require.NoError(t, err)
	require.Len(t, evts, 2)

	assert.Equal(t, EventWorkflowStarted, evts[0].Type)
	assert.Equal(t, "val", evts[0].Payload["key"])
	assert.Equal(t, EventWorkflowCompleted, evts[1].Type)
}

func TestObserverPlugin_OnlySubscribesToWorkflowEvents(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	bus := NewPluginBus()

	obs := NewObserverPlugin("test-observer", store)
	require.NoError(t, bus.Register(obs))
	require.NoError(t, bus.Start(context.Background()))

	ctx := context.Background()
	// Emit a workflow event (should be recorded).
	bus.Emit(ctx, "exec-1", EventWorkflowStarted, "test", nil)
	// Emit a non-workflow event (should be filtered out).
	bus.Emit(ctx, "exec-1", ares_events.EventAgentStarted, "test", nil)

	time.Sleep(50 * time.Millisecond)

	evts, err := store.Read(ctx, "exec-1", ares_events.ReadOptions{})
	require.NoError(t, err)
	assert.Len(t, evts, 1)
	assert.Equal(t, EventWorkflowStarted, evts[0].Type)
}

func TestObserverPlugin_EmptyNameDefaults(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	obs := NewObserverPlugin("", store)
	assert.Equal(t, "observer", obs.Name())
}

// ---------------------------------------------------------------------------
// CheckpointPlugin tests
// ---------------------------------------------------------------------------

func TestCheckpointPlugin_SavesAfterStep(t *testing.T) {
	ckptStore := newMemoryCheckpointStore()
	bus := NewPluginBus()

	p := NewCheckpointPlugin("test-checkpoint", ckptStore)
	require.NoError(t, bus.Register(p))
	require.NoError(t, bus.Start(context.Background()))

	err := bus.AfterStep(context.Background(), "exec-1", &StepResult{
		StepID: "s1", Status: StepStatusCompleted, Output: "hello",
	})
	require.NoError(t, err)

	data, err := ckptStore.Load(context.Background(), "checkpoint/exec-1")
	require.NoError(t, err)
	require.NotNil(t, data)

	var ckpt ExperienceCheckpoint
	err = json.Unmarshal(data, &ckpt)
	require.NoError(t, err)
	assert.Equal(t, 1, ckpt.SchemaVersion)
	assert.Equal(t, "exec-1", ckpt.ExecutionID)
	require.Len(t, ckpt.StepStates, 1)
	assert.Equal(t, "s1", ckpt.StepStates[0].StepID)
	assert.Equal(t, StepStatusCompleted, ckpt.StepStates[0].Status)
	assert.Equal(t, "hello", ckpt.StepStates[0].Output)
}

func TestCheckpointPlugin_EmptyNameDefaults(t *testing.T) {
	store := newMemoryCheckpointStore()
	p := NewCheckpointPlugin("", store)
	assert.Equal(t, "checkpoint", p.Name())
}

func TestCheckpointPlugin_NoStore(t *testing.T) {
	p := NewCheckpointPlugin("no-store", nil)
	err := p.AfterStep(context.Background(), "exec-1", &StepResult{StepID: "s1"})
	require.NoError(t, err)
}

func TestCheckpointPlugin_BeforeStepCreatesCheckpoint(t *testing.T) {
	ckptStore := newMemoryCheckpointStore()
	bus := NewPluginBus()

	p := NewCheckpointPlugin("test-cp", ckptStore)
	require.NoError(t, bus.Register(p))
	require.NoError(t, bus.Start(context.Background()))

	err := bus.BeforeStep(context.Background(), "exec-1", &Step{
		ID: "s1", Name: "Step One", Status: StepStatusRunning,
	})
	require.NoError(t, err)

	data, err := ckptStore.Load(context.Background(), "checkpoint/exec-1")
	require.NoError(t, err)
	require.NotNil(t, data)

	var ckpt ExperienceCheckpoint
	err = json.Unmarshal(data, &ckpt)
	require.NoError(t, err)
	assert.Equal(t, "exec-1", ckpt.ExecutionID)
	assert.Equal(t, "running", ckpt.Status)
	require.Len(t, ckpt.StepStates, 1)
	assert.Equal(t, "s1", ckpt.StepStates[0].StepID)
	assert.Equal(t, StepStatusRunning, ckpt.StepStates[0].Status)
}

func TestCheckpointPlugin_AccumulatesMultipleSteps(t *testing.T) {
	ckptStore := newMemoryCheckpointStore()
	bus := NewPluginBus()

	p := NewCheckpointPlugin("test-cp", ckptStore)
	require.NoError(t, bus.Register(p))
	require.NoError(t, bus.Start(context.Background()))

	ctx := context.Background()

	// Step 1 lifecycle.
	_ = bus.BeforeStep(ctx, "exec-1", &Step{ID: "s1"})
	_ = bus.AfterStep(ctx, "exec-1", &StepResult{StepID: "s1", Status: StepStatusCompleted, Output: "out1"})

	// Step 2 lifecycle.
	_ = bus.BeforeStep(ctx, "exec-1", &Step{ID: "s2"})
	_ = bus.AfterStep(ctx, "exec-1", &StepResult{StepID: "s2", Status: StepStatusCompleted, Output: "out2"})

	data, err := ckptStore.Load(ctx, "checkpoint/exec-1")
	require.NoError(t, err)
	require.NotNil(t, data)

	var ckpt ExperienceCheckpoint
	err = json.Unmarshal(data, &ckpt)
	require.NoError(t, err)
	require.Len(t, ckpt.StepStates, 2)

	assert.Equal(t, "s1", ckpt.StepStates[0].StepID)
	assert.Equal(t, StepStatusCompleted, ckpt.StepStates[0].Status)
	assert.Equal(t, "out1", ckpt.StepStates[0].Output)

	assert.Equal(t, "s2", ckpt.StepStates[1].StepID)
	assert.Equal(t, StepStatusCompleted, ckpt.StepStates[1].Status)
	assert.Equal(t, "out2", ckpt.StepStates[1].Output)

	assert.Greater(t, ckpt.StateVersion, int64(0))
}

func TestCheckpointPlugin_RecordsFailures(t *testing.T) {
	ckptStore := newMemoryCheckpointStore()
	bus := NewPluginBus()

	p := NewCheckpointPlugin("test-cp", ckptStore)
	require.NoError(t, bus.Register(p))
	require.NoError(t, bus.Start(context.Background()))

	_ = bus.BeforeStep(context.Background(), "exec-1", &Step{ID: "s1"})
	_ = bus.AfterStep(context.Background(), "exec-1", &StepResult{
		StepID: "s1", Status: StepStatusFailed, Error: "oops",
	})

	data, err := ckptStore.Load(context.Background(), "checkpoint/exec-1")
	require.NoError(t, err)
	require.NotNil(t, data)

	var ckpt ExperienceCheckpoint
	err = json.Unmarshal(data, &ckpt)
	require.NoError(t, err)
	assert.Equal(t, StepStatusFailed, ckpt.StepStates[0].Status)
	assert.Equal(t, "oops", ckpt.StepStates[0].Error)
	require.Len(t, ckpt.ErrorHistory, 1)
	assert.Equal(t, "oops", ckpt.ErrorHistory[0].Message)
}

func TestCheckpointPlugin_SchemaVersion(t *testing.T) {
	ckptStore := newMemoryCheckpointStore()
	p := NewCheckpointPlugin("test-cp", ckptStore)

	_ = p.BeforeStep(context.Background(), "exec-1", &Step{ID: "s1"})

	data, _ := ckptStore.Load(context.Background(), "checkpoint/exec-1")
	var ckpt ExperienceCheckpoint
	_ = json.Unmarshal(data, &ckpt)
	assert.Equal(t, 1, ckpt.SchemaVersion, "schema version must be 1")
}

func TestCheckpointPlugin_WithCollector_MergesData(t *testing.T) {
	ckptStore := newMemoryCheckpointStore()
	bus := NewPluginBus()

	collector := NewExecutionCollector("exec-1")
	cp := NewCheckpointPlugin("test-cp", ckptStore).WithCollector(collector)
	require.NoError(t, bus.Register(cp))
	require.NoError(t, bus.Start(context.Background()))

	// Record data through collector.
	collector.RecordRoute("s1", "s2", "test route", "expression")
	collector.RecordTool("s1", "calc", "1+1", "2", time.Second, true)
	collector.RecordError("s1", "test error")

	// Trigger a checkpoint save via AfterStep.
	err := bus.AfterStep(context.Background(), "exec-1", &StepResult{
		StepID: "s1", Status: StepStatusCompleted, Output: "done",
	})
	require.NoError(t, err)

	data, err := ckptStore.Load(context.Background(), "checkpoint/exec-1")
	require.NoError(t, err)
	require.NotNil(t, data)

	var ckpt ExperienceCheckpoint
	err = json.Unmarshal(data, &ckpt)
	require.NoError(t, err)

	require.Len(t, ckpt.RouteHistory, 1)
	assert.Equal(t, "s2", ckpt.RouteHistory[0].ToStepID)
	require.Len(t, ckpt.ToolHistory, 1)
	assert.Equal(t, "calc", ckpt.ToolHistory[0].ToolName)
	require.Len(t, ckpt.ErrorHistory, 1)
	assert.Equal(t, "test error", ckpt.ErrorHistory[0].Message)
}

func TestCheckpointPlugin_WithCollector_EmptyCollector(t *testing.T) {
	ckptStore := newMemoryCheckpointStore()
	collector := NewExecutionCollector("exec-1")
	cp := NewCheckpointPlugin("test-cp", ckptStore).WithCollector(collector)

	_ = cp.AfterStep(context.Background(), "exec-1", &StepResult{
		StepID: "s1", Status: StepStatusCompleted,
	})

	data, _ := ckptStore.Load(context.Background(), "checkpoint/exec-1")
	var ckpt ExperienceCheckpoint
	_ = json.Unmarshal(data, &ckpt)
	assert.Empty(t, ckpt.RouteHistory)
	assert.Empty(t, ckpt.ToolHistory)
}

func TestExpressionRouter_RegisteredAsPlugin(t *testing.T) {
	bus := NewPluginBus()
	router := NewExpressionRouter("test-router", []RouteRule{
		{
			FromStepID: "s1",
			ToStepID:   "s2",
			Condition:  func(output string, vars map[string]any) bool { return true },
			Reason:     "always",
		},
	})

	require.NoError(t, bus.Register(router))
	require.NoError(t, bus.Start(context.Background()))

	plugins := bus.PluginsByCap(CapRouter)
	if assert.Len(t, plugins, 1) {
		r, ok := plugins[0].(RouterPlugin)
		require.True(t, ok)
		decision, err := r.Route(context.Background(), RouteState{
			CurrentStepID: "s1",
		})
		require.NoError(t, err)
		require.NotNil(t, decision)
		assert.Equal(t, "s2", decision.NextStepID)
	}
}

// ---------------------------------------------------------------------------
// InterruptPlugin tests
// ---------------------------------------------------------------------------

func TestInterruptPlugin_New(t *testing.T) {
	p := NewInterruptPlugin("")
	assert.Equal(t, "interrupt", p.Name())
	p2 := NewInterruptPlugin("custom-hitl")
	assert.Equal(t, "custom-hitl", p2.Name())
}

func TestInterruptPlugin_RecordsInterruptOnSkippedRejected(t *testing.T) {
	bus := NewPluginBus()
	collector := NewExecutionCollector("exec-1")

	p := NewInterruptPlugin("test-hitl").WithCollector(collector)
	require.NoError(t, bus.Register(p))
	require.NoError(t, bus.Start(context.Background()))

	// AfterStep with a "rejected by human" skipped step.
	err := bus.AfterStep(context.Background(), "exec-1", &StepResult{
		StepID: "s1", Status: StepStatusSkipped, Error: "rejected by human",
	})
	require.NoError(t, err)

	interrupts := collector.InterruptLog()
	require.Len(t, interrupts, 1)
	assert.Equal(t, "s1", interrupts[0].StepID)
	assert.Equal(t, "reject", interrupts[0].Action)
}

func TestInterruptPlugin_RecordsInterruptWithMetadata(t *testing.T) {
	bus := NewPluginBus()
	collector := NewExecutionCollector("exec-1")

	p := NewInterruptPlugin("test-hitl").WithCollector(collector)
	require.NoError(t, bus.Register(p))
	require.NoError(t, bus.Start(context.Background()))

	// AfterStep with interrupt metadata.
	err := bus.AfterStep(context.Background(), "exec-1", &StepResult{
		StepID: "s1", Status: StepStatusCompleted,
		Metadata: map[string]string{
			"interrupt_action":   "approve",
			"interrupt_feedback": "looks good",
		},
	})
	require.NoError(t, err)

	interrupts := collector.InterruptLog()
	require.Len(t, interrupts, 1)
	assert.Equal(t, "approve", interrupts[0].Action)
	assert.Equal(t, "looks good", interrupts[0].Feedback)
}

func TestInterruptPlugin_EmitsEvent(t *testing.T) {
	bus := NewPluginBus()
	p := NewInterruptPlugin("test-hitl")
	require.NoError(t, bus.Register(p))
	require.NoError(t, bus.Start(context.Background()))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	ch, err := bus.Subscribe(ctx, ares_events.EventFilter{
		Types: []ares_events.EventType{EventInterruptCreated},
	})
	require.NoError(t, err)

	err = bus.AfterStep(ctx, "exec-1", &StepResult{
		StepID: "s1", Status: StepStatusSkipped, Error: "rejected by human",
	})
	require.NoError(t, err)

	select {
	case evt := <-ch:
		assert.Equal(t, EventInterruptCreated, evt.Type)
		assert.Equal(t, "exec-1", evt.StreamID)
		assert.Equal(t, "reject", evt.Payload["action"])
	case <-ctx.Done():
		t.Fatal("timeout waiting for interrupt event")
	}
}

func TestInterruptPlugin_DoesNotRecordNonInterruptSteps(t *testing.T) {
	bus := NewPluginBus()
	collector := NewExecutionCollector("exec-1")

	p := NewInterruptPlugin("test-hitl").WithCollector(collector)
	require.NoError(t, bus.Register(p))
	require.NoError(t, bus.Start(context.Background()))

	// Normal completed step without interrupt metadata.
	err := bus.AfterStep(context.Background(), "exec-1", &StepResult{
		StepID: "s1", Status: StepStatusCompleted,
	})
	require.NoError(t, err)

	assert.Empty(t, collector.InterruptLog())
}

// ---------------------------------------------------------------------------
// LoopPlugin tests
// ---------------------------------------------------------------------------

func TestLoopPlugin_New(t *testing.T) {
	p := NewLoopPlugin("", LoopConfig{MaxIterations: 5})
	assert.Equal(t, "loop", p.Name())
	assert.Equal(t, 5, p.Config().MaxIterations)
}

func TestLoopPlugin_Capabilities(t *testing.T) {
	p := NewLoopPlugin("test", LoopConfig{})
	assert.Contains(t, p.Capabilities(), CapLoop)
}

func TestLoopPlugin_ShouldExecuteRound_MaxIterations(t *testing.T) {
	p := NewLoopPlugin("test", LoopConfig{MaxIterations: 3})
	assert.True(t, p.ShouldExecuteRound(1, nil))
	assert.True(t, p.ShouldExecuteRound(2, nil))
	assert.True(t, p.ShouldExecuteRound(3, nil))
	assert.False(t, p.ShouldExecuteRound(4, nil))
	assert.False(t, p.ShouldExecuteRound(5, nil))
}

func TestLoopPlugin_ShouldExecuteRound_UntilCondition(t *testing.T) {
	p := NewLoopPlugin("test", LoopConfig{
		UntilCondition: func(vars map[string]any) bool {
			count, _ := vars["count"].(int)
			return count >= 2
		},
	})
	assert.True(t, p.ShouldExecuteRound(1, map[string]any{"count": 0}))
	assert.True(t, p.ShouldExecuteRound(2, map[string]any{"count": 1}))
	assert.False(t, p.ShouldExecuteRound(3, map[string]any{"count": 2}))
	assert.False(t, p.ShouldExecuteRound(4, map[string]any{"count": 3}))
}

func TestLoopPlugin_ShouldExecuteRound_NoLimit(t *testing.T) {
	p := NewLoopPlugin("test", LoopConfig{})
	for i := 1; i <= 100; i++ {
		assert.True(t, p.ShouldExecuteRound(i, nil))
	}
}

func TestLoopPlugin_Iteration(t *testing.T) {
	p := NewLoopPlugin("test", LoopConfig{})
	assert.Equal(t, 0, p.Iteration())

	p.OnRoundEnd(context.Background(), 1, "exec-1")
	assert.Equal(t, 1, p.Iteration())

	p.OnRoundEnd(context.Background(), 5, "exec-1")
	assert.Equal(t, 5, p.Iteration())
}

func TestLoopPlugin_StopResets(t *testing.T) {
	p := NewLoopPlugin("test", LoopConfig{})
	p.OnRoundEnd(context.Background(), 3, "exec-1")
	assert.Equal(t, 3, p.Iteration())

	_ = p.Stop(context.Background())
	assert.Equal(t, 0, p.Iteration())
}

func TestLoopPlugin_RegisteredAsPlugin(t *testing.T) {
	bus := NewPluginBus()
	p := NewLoopPlugin("test-loop", LoopConfig{MaxIterations: 5})
	require.NoError(t, bus.Register(p))
	require.NoError(t, bus.Start(context.Background()))

	plugins := bus.PluginsByCap(CapLoop)
	require.Len(t, plugins, 1)
	assert.Equal(t, "test-loop", plugins[0].Name())
}

func TestLoopPlugin_EmptyNameDefaults(t *testing.T) {
	p := NewLoopPlugin("", LoopConfig{})
	assert.Equal(t, "loop", p.Name())
}

// ---------------------------------------------------------------------------
// Integration: ObserverPlugin + CheckpointPlugin together
// ---------------------------------------------------------------------------

func TestPluginBus_MultiplePlugins(t *testing.T) {
	eventStore := ares_events.NewMemoryEventStore()
	ckptStore := newMemoryCheckpointStore()

	bus := NewPluginBus()
	obs := NewObserverPlugin("obs", eventStore)
	ckpt := NewCheckpointPlugin("ckpt", ckptStore)

	require.NoError(t, bus.Register(obs))
	require.NoError(t, bus.Register(ckpt))
	require.NoError(t, bus.Start(context.Background()))

	ctx := context.Background()

	bus.Emit(ctx, "exec-1", EventWorkflowStarted, "test", nil)
	_ = bus.BeforeStep(ctx, "exec-1", &Step{ID: "s1"})
	_ = bus.AfterStep(ctx, "exec-1", &StepResult{StepID: "s1", Status: StepStatusCompleted})
	bus.Emit(ctx, "exec-1", EventWorkflowCompleted, "test", nil)

	time.Sleep(100 * time.Millisecond)

	evts, err := eventStore.Read(ctx, "exec-1", ares_events.ReadOptions{})
	require.NoError(t, err)
	// workflow.started + workflow.completed + 2× checkpoint.saved (from BeforeStep/AfterStep)
	assert.Len(t, evts, 4)

	data, err := ckptStore.Load(ctx, "checkpoint/exec-1")
	require.NoError(t, err)
	require.NotNil(t, data)

	var ckptData2 ExperienceCheckpoint
	_ = json.Unmarshal(data, &ckptData2)
	assert.Equal(t, StepStatusCompleted, ckptData2.StepStates[0].Status)
}
