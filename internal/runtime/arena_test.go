package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// ---------------------------------------------------------------------------
// ArenaPlugin tests
// ---------------------------------------------------------------------------

func TestArenaPlugin_New(t *testing.T) {
	a := NewArenaPlugin("")
	assert.Equal(t, "arena", a.Name())
	a2 := NewArenaPlugin("custom-arena")
	assert.Equal(t, "custom-arena", a2.Name())
}

func TestArenaPlugin_Lifecycle(t *testing.T) {
	bus := NewPluginBus()
	a := NewArenaPlugin("test-arena")
	require.NoError(t, bus.Register(a))
	require.NoError(t, bus.Start(context.Background()))
	require.NoError(t, bus.Stop(context.Background()))
}

func TestArenaPlugin_ScheduleAndCancelFault(t *testing.T) {
	a := NewArenaPlugin("test-arena")
	a.ScheduleFault("plugin-1", FaultPluginError)
	a.CancelFault("plugin-1")
	// No fault should trigger
	err := a.checkFault(context.Background())
	require.NoError(t, err)
}

func TestArenaPlugin_InjectErrorFault(t *testing.T) {
	a := NewArenaPlugin("test-arena")
	a.ScheduleFault("plugin-1", FaultPluginError)
	err := a.checkFault(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrFaultInjected)
}

func TestArenaPlugin_InjectTimeoutFault(t *testing.T) {
	// Use a bus with a short timeout so the fault triggers a timeout error.
	bus := NewPluginBus(WithPluginTimeout(10 * time.Millisecond))
	a := NewArenaPlugin("test-arena")
	require.NoError(t, bus.Register(a))
	require.NoError(t, bus.Start(context.Background()))

	// Schedule a timeout fault — the arena's BeforeStep will block,
	// and the bus's invokeWithTimeout will interrupt it.
	a.ScheduleFault("plugin-1", FaultPluginTimeout)

	err := bus.BeforeStep(context.Background(), "exec-1", &Step{ID: "s1"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPluginTimeout)

	a.CancelFault("plugin-1")
}

func TestArenaPlugin_InjectPanicFault(t *testing.T) {
	bus := NewPluginBus(WithPluginTimeout(100 * time.Millisecond))
	arena := NewArenaPlugin("test-arena")
	require.NoError(t, bus.Register(arena))
	require.NoError(t, bus.Start(context.Background()))

	// Schedule a panic — the arena's BeforeStep will panic, and the bus
	// should recover it via invokeWithTimeout.
	arena.ScheduleFault("plugin-1", FaultPluginPanic)

	err := bus.BeforeStep(context.Background(), "exec-1", &Step{ID: "s1"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPluginPanic)

	arena.CancelFault("plugin-1")

	// Bus should still work after panic recovery.
	err = bus.BeforeStep(context.Background(), "exec-1", &Step{ID: "s2"})
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Failure mode tests: graceful degradation per plugin type
// ---------------------------------------------------------------------------

// TestObserverPlugin_FailureDoesNotBlockBus verifies that when ObserverPlugin
// encounters an error (event store unavailable), the bus still works.
func TestObserverPlugin_FailureDoesNotBlockBus(t *testing.T) {
	// Use a store that rejects appends to simulate failure.
	failStore := &failingEventStore{}

	bus := NewPluginBus()
	obs := NewObserverPlugin("failing-observer", failStore)
	require.NoError(t, bus.Register(obs))
	require.NoError(t, bus.Start(context.Background()))

	// Emit an event — the observer should log a warning but not block.
	bus.Emit(context.Background(), "exec-1", EventWorkflowStarted, "test", nil)
	time.Sleep(50 * time.Millisecond)

	// Bus should still work for other operations.
	err := bus.BeforeStep(context.Background(), "exec-1", &Step{ID: "s1"})
	require.NoError(t, err)

	err = bus.AfterStep(context.Background(), "exec-1", &StepResult{
		StepID: "s1", Status: StepStatusCompleted,
	})
	require.NoError(t, err)
}

// TestCheckpointPlugin_FailureDoesNotBlockBus verifies that when
// CheckpointPlugin's store fails, the bus continues operating.
func TestCheckpointPlugin_FailureDoesNotBlockBus(t *testing.T) {
	failStore := &failingCheckpointStore{}
	bus := NewPluginBus()

	cp := NewCheckpointPlugin("failing-cp", failStore)
	require.NoError(t, bus.Register(cp))
	require.NoError(t, bus.Start(context.Background()))

	// BeforeStep/AfterStep should still work even if save fails.
	err := bus.BeforeStep(context.Background(), "exec-1", &Step{ID: "s1"})
	require.Error(t, err) // save fails, but bus doesn't panic

	err = bus.AfterStep(context.Background(), "exec-1", &StepResult{
		StepID: "s1", Status: StepStatusCompleted,
	})
	require.Error(t, err) // save fails again
}

// TestCheckpointPlugin_NoStoreIsGraceful verifies that a CheckpointPlugin
// without a store does not interfere with execution.
func TestCheckpointPlugin_NoStoreIsGraceful(t *testing.T) {
	bus := NewPluginBus()
	cp := NewCheckpointPlugin("no-store-cp", nil)
	require.NoError(t, bus.Register(cp))
	require.NoError(t, bus.Start(context.Background()))

	err := bus.BeforeStep(context.Background(), "exec-1", &Step{ID: "s1"})
	require.NoError(t, err)

	err = bus.AfterStep(context.Background(), "exec-1", &StepResult{
		StepID: "s1", Status: StepStatusCompleted,
	})
	require.NoError(t, err)
}

// TestInterruptPlugin_NoCollectorIsGraceful verifies InterruptPlugin works
// without a collector attached.
func TestInterruptPlugin_NoCollectorIsGraceful(t *testing.T) {
	bus := NewPluginBus()
	p := NewInterruptPlugin("test-hitl")
	require.NoError(t, bus.Register(p))
	require.NoError(t, bus.Start(context.Background()))

	err := bus.AfterStep(context.Background(), "exec-1", &StepResult{
		StepID: "s1", Status: StepStatusSkipped, Error: "rejected by human",
	})
	require.NoError(t, err)
}

// TestLoopPlugin_NoCollectorIsGraceful verifies LoopPlugin works without
// a collector.
func TestLoopPlugin_NoCollectorIsGraceful(t *testing.T) {
	bus := NewPluginBus()
	p := NewLoopPlugin("test-loop", LoopConfig{MaxIterations: 5})
	require.NoError(t, bus.Register(p))
	require.NoError(t, bus.Start(context.Background()))

	err := bus.BeforeStep(context.Background(), "exec-1", &Step{ID: "s1"})
	require.NoError(t, err)
}

// TestPluginBus_PluginPanicInAfterStep verifies that a panic in AfterStep
// is recovered and does not crash the bus.
func TestPluginBus_PluginPanicInAfterStep(t *testing.T) {
	b := NewPluginBus()
	require.NoError(t, b.Start(context.Background()))

	// Register a hook that panics in AfterStep.
	b.RegisterHook("panic-after", &panickingAfterStepHook{})

	err := b.AfterStep(context.Background(), "exec-1", &StepResult{StepID: "s1"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPluginPanic)
}

// panickingAfterStepHook panics in AfterStep for recovery testing.
type panickingAfterStepHook struct{}

func (h *panickingAfterStepHook) BeforeStep(_ context.Context, _ string, _ *Step) error {
	return nil
}

func (h *panickingAfterStepHook) AfterStep(_ context.Context, _ string, _ *StepResult) error {
	panic("after step panic")
}

// TestPluginBus_MultipleHooksOneFails verifies that when one hook fails,
// remaining hooks still execute.
func TestPluginBus_MultipleHooksOneFails(t *testing.T) {
	b := NewPluginBus()
	require.NoError(t, b.Start(context.Background()))

	h1 := newTestHook()
	h2 := newTestHook()
	h2.beforeErr = errors.New("hook 2 before error")
	h3 := newTestHook()

	b.RegisterHook("h1", h1)
	b.RegisterHook("h2", h2)
	b.RegisterHook("h3", h3)

	// BeforeStep runs all hooks; h2 fails but h3 still executes.
	err := b.BeforeStep(context.Background(), "exec-1", &Step{ID: "s1"})
	require.Error(t, err)

	assert.Equal(t, []string{"s1"}, h1.before)
	assert.Equal(t, []string{"s1"}, h2.before) // h2 was called, returned error
	assert.Equal(t, []string{"s1"}, h3.before) // h3 still called despite h2 error

	// AfterStep should work normally since only h2's beforeErr is set.
	err = b.AfterStep(context.Background(), "exec-1", &StepResult{StepID: "s1"})
	require.NoError(t, err)

	assert.Equal(t, []string{"s1"}, h1.after)
	assert.Equal(t, []string{"s1"}, h2.after)
	assert.Equal(t, []string{"s1"}, h3.after)
}

// ---------------------------------------------------------------------------
// Helper types
// ---------------------------------------------------------------------------

// failingEventStore rejects all Append calls.
type failingEventStore struct{}

func (s *failingEventStore) Append(_ context.Context, _ string, _ []*ares_events.Event, _ int64) error {
	return errors.New("store unavailable")
}

func (s *failingEventStore) Read(_ context.Context, _ string, _ ares_events.ReadOptions) ([]*ares_events.Event, error) {
	return nil, errors.New("store unavailable")
}

func (s *failingEventStore) ReadAll(_ context.Context, _ ares_events.ReadOptions) ([]*ares_events.Event, error) {
	return nil, errors.New("store unavailable")
}

func (s *failingEventStore) Subscribe(_ context.Context, _ ares_events.EventFilter) (<-chan *ares_events.Event, error) {
	return nil, errors.New("store unavailable")
}

func (s *failingEventStore) StreamVersion(_ context.Context, _ string) (int64, error) {
	return 0, errors.New("store unavailable")
}

// failingCheckpointStore rejects all Save calls.
type failingCheckpointStore struct {
	data map[string][]byte
}

func (s *failingCheckpointStore) Save(_ context.Context, _ string, _ []byte) error {
	return errors.New("store unavailable")
}

func (s *failingCheckpointStore) Load(_ context.Context, key string) ([]byte, error) {
	if s.data == nil {
		return nil, nil
	}
	return s.data[key], nil
}
