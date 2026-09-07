package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/agents/base"
	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/core/models"
	memory "github.com/Timwood0x10/ares/internal/runtime/memory"
)

// --- Mock implementations ---

// mockAgent implements base.Agent for testing.
type mockAgent struct {
	mu       sync.RWMutex
	id       string
	agentTyp models.AgentType
	status   models.AgentStatus
	startFn  func(ctx context.Context) error
	stopFn   func(ctx context.Context) error
	started  atomic.Int32
	stopped  atomic.Int32
}

func newMockAgent(id string) *mockAgent {
	return &mockAgent{
		id:       id,
		agentTyp: models.AgentTypeTop,
		status:   models.AgentStatusOffline,
	}
}

func (a *mockAgent) ID() string                 { return a.id }
func (a *mockAgent) Type() models.AgentType     { return a.agentTyp }
func (a *mockAgent) Status() models.AgentStatus { a.mu.RLock(); defer a.mu.RUnlock(); return a.status }

func (a *mockAgent) setStatus(s models.AgentStatus) {
	a.mu.Lock()
	a.status = s
	a.mu.Unlock()
}

func (a *mockAgent) Start(ctx context.Context) error {
	a.started.Add(1)
	if a.startFn != nil {
		if err := a.startFn(ctx); err != nil {
			return err
		}
	}
	a.setStatus(models.AgentStatusReady)
	// Block until context cancelled to simulate a long-running agent.
	<-ctx.Done()
	a.setStatus(models.AgentStatusOffline)
	return nil
}

func (a *mockAgent) Stop(ctx context.Context) error {
	a.stopped.Add(1)
	if a.stopFn != nil {
		return a.stopFn(ctx)
	}
	a.setStatus(models.AgentStatusOffline)
	return nil
}

func (a *mockAgent) Process(ctx context.Context, input any) (any, error) {
	return nil, errors.New("not implemented in mock")
}

func (a *mockAgent) ProcessStream(ctx context.Context, input any) (<-chan base.AgentEvent, error) {
	ch := make(chan base.AgentEvent)
	close(ch)
	return ch, nil
}

// mockStatefulAgent implements both base.Agent and base.StatefulAgent.
type mockStatefulAgent struct {
	*mockAgent
	restoreStateFn func(state map[string]any) error
	replayEventsFn func(evts []*ares_events.Event) error
	snapshotFn     func() (map[string]any, error)
	restoredState  map[string]any
	replayedEvts   []*ares_events.Event
}

func newMockStatefulAgent(id string) *mockStatefulAgent {
	return &mockStatefulAgent{
		mockAgent: newMockAgent(id),
	}
}

func (a *mockStatefulAgent) RestoreState(state map[string]any) error {
	a.restoredState = state
	if a.restoreStateFn != nil {
		return a.restoreStateFn(state)
	}
	return nil
}

func (a *mockStatefulAgent) ReplayEvents(evts []*ares_events.Event) error {
	a.replayedEvts = evts
	if a.replayEventsFn != nil {
		return a.replayEventsFn(evts)
	}
	return nil
}

func (a *mockStatefulAgent) Snapshot() (map[string]any, error) {
	if a.snapshotFn != nil {
		return a.snapshotFn()
	}
	return map[string]any{"agent_id": a.id}, nil
}

// mockFactory creates a factory function that tracks calls.
type mockFactory struct {
	mu        sync.Mutex
	agents    []*mockStatefulAgent
	callCount int
}

func newMockFactory() *mockFactory {
	return &mockFactory{}
}

func (f *mockFactory) create() AgentFactory {
	return func() base.Agent {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.callCount++
		a := newMockStatefulAgent(fmt.Sprintf("agent-%d", f.callCount))
		f.agents = append(f.agents, a)
		return a
	}
}

func (f *mockFactory) lastAgent() *mockStatefulAgent {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.agents) == 0 {
		return nil
	}
	return f.agents[len(f.agents)-1]
}

// mockMemoryManager is a MemoryManager implementation for testing.
type mockMemoryManager struct {
	getLatestSessionCalls int
	getMessagesCalls      int
}

func newMockMemoryManager() *mockMemoryManager {
	return &mockMemoryManager{}
}

func (m *mockMemoryManager) CreateSession(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (m *mockMemoryManager) AddMessage(_ context.Context, _, _, _ string) error {
	return nil
}

func (m *mockMemoryManager) GetMessages(_ context.Context, _ string) ([]memory.Message, error) {
	m.getMessagesCalls++
	return []memory.Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
	}, nil
}

func (m *mockMemoryManager) DeleteSession(_ context.Context, _ string) error {
	return nil
}

func (m *mockMemoryManager) BuildContext(_ context.Context, _, _ string) (string, error) {
	return "", nil
}

func (m *mockMemoryManager) CreateTask(_ context.Context, _, _, _ string) (string, error) {
	return "", nil
}

func (m *mockMemoryManager) CreateTaskWithID(_ context.Context, _, _, _, _ string) error {
	return nil
}

func (m *mockMemoryManager) UpdateTaskOutput(_ context.Context, _, _ string) error {
	return nil
}

func (m *mockMemoryManager) DistillTask(_ context.Context, _ string) (*models.Task, error) {
	return nil, errors.New("not implemented in mock")
}

func (m *mockMemoryManager) StoreDistilledTask(_ context.Context, _ string, _ *models.Task) error {
	return nil
}

func (m *mockMemoryManager) SearchSimilarTasks(_ context.Context, _ string, _ int) ([]*models.Task, error) {
	return nil, nil
}

func (m *mockMemoryManager) GetLatestSessionForAgent(_ context.Context, _ string) (string, error) {
	m.getLatestSessionCalls++
	return "sess-mem", nil
}

func (m *mockMemoryManager) Start(_ context.Context) error { return nil }
func (m *mockMemoryManager) Stop(_ context.Context) error  { return nil }
func (m *mockMemoryManager) SetEventStore(_ ares_events.EventStore, _ string) {
}

func (m *mockMemoryManager) AddStructuredMessage(_ context.Context, _ string, _ memory.Message) error {
	return nil
}

func (m *mockMemoryManager) BuildPromptMessages(_ context.Context, _ string) ([]memory.Message, error) {
	return nil, nil
}

// errEventStore is an EventStore that returns an error on Read.
type errEventStore struct {
	readErr error
}

func (s *errEventStore) Append(_ context.Context, _ string, _ []*ares_events.Event, _ int64) error {
	return nil
}

func (s *errEventStore) Read(_ context.Context, _ string, _ ares_events.ReadOptions) ([]*ares_events.Event, error) {
	return nil, s.readErr
}

func (s *errEventStore) ReadAll(_ context.Context, _ ares_events.ReadOptions) ([]*ares_events.Event, error) {
	return nil, s.readErr
}

func (s *errEventStore) Subscribe(_ context.Context, _ ares_events.EventFilter) (<-chan *ares_events.Event, error) {
	ch := make(chan *ares_events.Event)
	close(ch)
	return ch, nil
}

func (s *errEventStore) StreamVersion(_ context.Context, _ string) (int64, error) {
	return 0, nil
}

// errMemoryManager is a MemoryManager that returns errors on session/message retrieval.
type errMemoryManager struct {
	sessionErr  error
	messagesErr error
}

func (m *errMemoryManager) CreateSession(_ context.Context, _ string) (string, error) {
	return "", nil
}

func (m *errMemoryManager) AddMessage(_ context.Context, _, _, _ string) error {
	return nil
}

func (m *errMemoryManager) GetMessages(_ context.Context, _ string) ([]memory.Message, error) {
	return nil, m.messagesErr
}

func (m *errMemoryManager) DeleteSession(_ context.Context, _ string) error {
	return nil
}

func (m *errMemoryManager) BuildContext(_ context.Context, _, _ string) (string, error) {
	return "", nil
}

func (m *errMemoryManager) CreateTask(_ context.Context, _, _, _ string) (string, error) {
	return "", nil
}

func (m *errMemoryManager) CreateTaskWithID(_ context.Context, _, _, _, _ string) error {
	return nil
}

func (m *errMemoryManager) UpdateTaskOutput(_ context.Context, _, _ string) error {
	return nil
}

func (m *errMemoryManager) DistillTask(_ context.Context, _ string) (*models.Task, error) {
	return nil, errors.New("not implemented in mock")
}

func (m *errMemoryManager) StoreDistilledTask(_ context.Context, _ string, _ *models.Task) error {
	return nil
}

func (m *errMemoryManager) SearchSimilarTasks(_ context.Context, _ string, _ int) ([]*models.Task, error) {
	return nil, nil
}

func (m *errMemoryManager) GetLatestSessionForAgent(_ context.Context, _ string) (string, error) {
	return "", m.sessionErr
}

func (m *errMemoryManager) Start(_ context.Context) error { return nil }
func (m *errMemoryManager) Stop(_ context.Context) error  { return nil }
func (m *errMemoryManager) SetEventStore(_ ares_events.EventStore, _ string) {
}

func (m *errMemoryManager) AddStructuredMessage(_ context.Context, _ string, _ memory.Message) error {
	return nil
}

func (m *errMemoryManager) BuildPromptMessages(_ context.Context, _ string) ([]memory.Message, error) {
	return nil, nil
}

// slowStopAgent blocks in Stop until the provided channel is closed or context expires.
type slowStopAgent struct {
	*mockAgent
	stopBlocker chan struct{}
}

func newSlowStopAgent(id string) *slowStopAgent {
	return &slowStopAgent{
		mockAgent:   newMockAgent(id),
		stopBlocker: make(chan struct{}),
	}
}

func (a *slowStopAgent) Stop(ctx context.Context) error {
	a.stopped.Add(1)
	select {
	case <-a.stopBlocker:
	case <-ctx.Done():
	}
	return nil
}

// nonStatefulFactory creates agents that do NOT implement StatefulAgent.
type nonStatefulFactory struct {
	mu     sync.Mutex
	agents []*mockAgent
}

func newNonStatefulFactory() *nonStatefulFactory {
	return &nonStatefulFactory{}
}

func (f *nonStatefulFactory) create(id string) AgentFactory {
	return func() base.Agent {
		f.mu.Lock()
		defer f.mu.Unlock()
		a := newMockAgent(id)
		f.agents = append(f.agents, a)
		return a
	}
}

func (f *nonStatefulFactory) lastAgent() *mockAgent {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.agents) == 0 {
		return nil
	}
	return f.agents[len(f.agents)-1]
}

// --- Tests ---

func TestManager_RegisterAgent(t *testing.T) {
	m := New(nil, nil, nil)
	agent := newMockAgent("a1")
	factory := func() base.Agent { return newMockAgent("a1") }

	m.RegisterAgent(agent, factory)

	// Registered agent is tracked in the map (but Offline, so not counted as active).
	m.mu.RLock()
	_, exists := m.agents["a1"]
	m.mu.RUnlock()
	assert.True(t, exists, "agent should be in the agents map")
}

func TestManager_RegisterAgent_NilAgent(t *testing.T) {
	m := New(nil, nil, nil)
	factory := func() base.Agent { return newMockAgent("a1") }

	// Should not panic, just no-op.
	m.RegisterAgent(nil, factory)

	stats := m.Stats()
	assert.Equal(t, 0, stats.ActiveAgents)
}

func TestManager_RegisterAgent_NilFactory(t *testing.T) {
	m := New(nil, nil, nil)
	agent := newMockAgent("a1")

	// Should not panic, just no-op.
	m.RegisterAgent(agent, nil)

	stats := m.Stats()
	assert.Equal(t, 0, stats.ActiveAgents)
}

func TestManager_StartAgent(t *testing.T) {
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	agent := newMockAgent("a1")
	err := m.StartAgent(ctx, agent)
	require.NoError(t, err)

	// Give the goroutine time to call Start.
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(1), agent.started.Load())
	assert.Equal(t, models.AgentStatusReady, agent.Status())

	stats := m.Stats()
	assert.Equal(t, 1, stats.ActiveAgents)
}

func TestManager_StartAgent_NilAgent(t *testing.T) {
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	err := m.StartAgent(ctx, nil)
	assert.ErrorIs(t, err, ErrNilAgent)
}

func TestManager_StartAgent_AlreadyRegistered(t *testing.T) {
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	agent := newMockAgent("a1")
	require.NoError(t, m.StartAgent(ctx, agent))

	// Second start with same ID should fail.
	err := m.StartAgent(ctx, newMockAgent("a1"))
	assert.ErrorIs(t, err, ErrAgentAlreadyRegistered)
}

func TestManager_StopAgent(t *testing.T) {
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	agent := newMockAgent("a1")
	require.NoError(t, m.StartAgent(ctx, agent))
	time.Sleep(50 * time.Millisecond)

	err := m.StopAgent(ctx, "a1")
	require.NoError(t, err)

	// Agent should have been stopped.
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(1), agent.stopped.Load())
	assert.Equal(t, models.AgentStatusOffline, agent.Status())

	stats := m.Stats()
	assert.Equal(t, 0, stats.ActiveAgents)
}

func TestManager_StopAgent_NotFound(t *testing.T) {
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	err := m.StopAgent(ctx, "nonexistent")
	assert.ErrorIs(t, err, ErrAgentNotFound)
}

func TestManager_RestartAgent(t *testing.T) {
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	agent := newMockAgent("a1")
	factory := func() base.Agent { return newMockAgent("a1") }
	m.RegisterAgent(agent, factory)

	require.NoError(t, m.StartAgent(ctx, agent))
	time.Sleep(50 * time.Millisecond)

	err := m.RestartAgent(ctx, "a1")
	require.NoError(t, err)

	// Old agent should be stopped.
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(1), agent.stopped.Load())

	// Stats should reflect the restart.
	stats := m.Stats()
	assert.Equal(t, 1, stats.TotalRestarts)
	assert.Equal(t, 1, stats.ActiveAgents)
}

func TestManager_RestartAgent_NotFound(t *testing.T) {
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	err := m.RestartAgent(ctx, "nonexistent")
	assert.ErrorIs(t, err, ErrAgentNotFound)
}

func TestManager_NotifyAgentDead_TriggersRestore(t *testing.T) {
	factory := newMockFactory()
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	agent := newMockAgent("a1")
	m.RegisterAgent(agent, factory.create())
	require.NoError(t, m.StartAgent(ctx, agent))
	time.Sleep(50 * time.Millisecond)

	// Notify agent dead.
	m.NotifyAgentDead("a1", "test crash")

	// Give time for async restore.
	time.Sleep(200 * time.Millisecond)

	// Factory should have been called to create a new agent.
	factory.mu.Lock()
	callCount := factory.callCount
	factory.mu.Unlock()
	assert.GreaterOrEqual(t, callCount, 1, "factory should have been called for restoration")
}

func TestManager_RestoreAgent_CreatesNewInstance(t *testing.T) {
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	factory := newMockFactory()

	err := m.RestoreAgent(ctx, "a1", factory.create())
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	// Factory should have been called.
	newAgent := factory.lastAgent()
	require.NotNil(t, newAgent)
	assert.Equal(t, "agent-1", newAgent.ID())
	assert.Equal(t, models.AgentStatusReady, newAgent.Status())

	// TotalRestarts is not incremented by RestoreAgent directly;
	// NotifyAgentDead owns the restart accounting.
	stats := m.Stats()
	assert.Equal(t, 0, stats.TotalRestarts)
	assert.Equal(t, 1, stats.ActiveAgents)
}

func TestManager_RestoreAgent_ReplaysEvents(t *testing.T) {
	eventStore := ares_events.NewMemoryEventStore()
	m := New(nil, eventStore, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	// Pre-populate ares_events in the store using the agent:streamID convention.
	err := eventStore.Append(ctx, "a1", []*ares_events.Event{
		{
			Type: ares_events.EventSessionCreated,
			Payload: map[string]any{
				"session_id": "sess-123",
			},
		},
		{
			Type: ares_events.EventTaskCreated,
			Payload: map[string]any{
				"task_id": "task-1",
			},
		},
	}, 0)
	require.NoError(t, err)

	factory := newMockFactory()
	err = m.RestoreAgent(ctx, "a1", factory.create())
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	// Verify ares_events were replayed.
	newAgent := factory.lastAgent()
	require.NotNil(t, newAgent)
	assert.NotNil(t, newAgent.replayedEvts, "ares_events should have been replayed")
	assert.Len(t, newAgent.replayedEvts, 3) // 2 pre-populated + 1 failover.triggered emitted by RestoreAgent
}

func TestManager_RestoreAgent_RestoresState(t *testing.T) {
	eventStore := ares_events.NewMemoryEventStore()
	m := New(nil, eventStore, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	// Pre-populate with session event using the agent:streamID convention.
	err := eventStore.Append(ctx, "a1", []*ares_events.Event{
		{
			Type: ares_events.EventSessionCreated,
			Payload: map[string]any{
				"session_id": "sess-456",
			},
		},
	}, 0)
	require.NoError(t, err)

	factory := newMockFactory()
	err = m.RestoreAgent(ctx, "a1", factory.create())
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	newAgent := factory.lastAgent()
	require.NotNil(t, newAgent)
	require.NotNil(t, newAgent.restoredState)
	assert.Equal(t, "sess-456", newAgent.restoredState["session_id"])
}

func TestManager_RestoreAgent_NilFactory(t *testing.T) {
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	err := m.RestoreAgent(ctx, "a1", nil)
	assert.ErrorIs(t, err, ErrNilFactory)
}

func TestManager_ConcurrentNotifyAgentDead(t *testing.T) {
	factory := newMockFactory()
	config := &Config{
		HealthCheckInterval: 100 * time.Millisecond,
		MaxRestartsPerAgent: 0, // Unlimited.
	}
	m := New(config, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	agent := newMockAgent("a1")
	m.RegisterAgent(agent, factory.create())
	require.NoError(t, m.StartAgent(ctx, agent))
	time.Sleep(50 * time.Millisecond)

	// Concurrently notify agent dead multiple times.
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m.NotifyAgentDead("a1", fmt.Sprintf("concurrent-crash-%d", i))
		}(i)
	}
	wg.Wait()

	// Wait for all restoration goroutines to complete.
	time.Sleep(500 * time.Millisecond)

	// Verify at least one restore happened.
	factory.mu.Lock()
	callCount := factory.callCount
	factory.mu.Unlock()
	assert.GreaterOrEqual(t, callCount, 1, "at least one restore should have happened")
}

func TestManager_Stats(t *testing.T) {
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	agent := newMockAgent("a1")
	require.NoError(t, m.StartAgent(ctx, agent))
	time.Sleep(50 * time.Millisecond)

	stats := m.Stats()
	assert.Equal(t, 1, stats.ActiveAgents)
	assert.Equal(t, 0, stats.TotalRestarts)
	assert.Greater(t, stats.Uptime, time.Duration(0))
}

func TestManager_Start_NilContext(t *testing.T) {
	m := New(nil, nil, nil)

	var ctx context.Context // nil context
	err := m.Start(ctx)     //nolint:staticcheck // Intentionally testing nil context guard.
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "context must not be nil")
}

func TestManager_DoubleStart(t *testing.T) {
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	err := m.Start(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
}

func TestManager_DoubleStop(t *testing.T) {
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	require.NoError(t, m.Stop())

	// Second stop should be a no-op (no error).
	err := m.Stop()
	assert.NoError(t, err)
}

func TestManager_PanicRecovery(t *testing.T) {
	factory := newMockFactory()
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	// Agent that panics on start.
	panicAgent := &panicMockAgent{
		mockAgent: newMockAgent("panic-agent"),
	}
	m.RegisterAgent(panicAgent, factory.create())

	// StartAgent should not propagate the panic.
	err := m.StartAgent(ctx, panicAgent)
	require.NoError(t, err)

	// Wait for the panic to be caught and restoration triggered.
	time.Sleep(300 * time.Millisecond)

	// Factory should have been called for restoration.
	factory.mu.Lock()
	callCount := factory.callCount
	factory.mu.Unlock()
	assert.GreaterOrEqual(t, callCount, 1, "factory should have been called after panic")
}

// panicMockAgent panics during Start.
type panicMockAgent struct {
	*mockAgent
}

func (a *panicMockAgent) Start(ctx context.Context) error {
	panic("intentional test panic")
}

func TestManager_StopAgent_StopsAgentContext(t *testing.T) {
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	agent := newMockAgent("a1")
	require.NoError(t, m.StartAgent(ctx, agent))

	// Wait for agent goroutine to start.
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, models.AgentStatusReady, agent.Status())

	// Stop should cancel the agent context, causing Start to return.
	err := m.StopAgent(ctx, "a1")
	require.NoError(t, err)

	// The mock agent's Start listens on ctx.Done() and sets status to Offline.
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, models.AgentStatusOffline, agent.Status())
}

func TestManager_RestoreAgent_WithoutEventStore(t *testing.T) {
	// RestoreAgent should work even without an EventStore.
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	factory := newMockFactory()
	err := m.RestoreAgent(ctx, "a1", factory.create())
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	newAgent := factory.lastAgent()
	require.NotNil(t, newAgent)
	// No ares_events to replay, so replayedEvts should be nil.
	assert.Nil(t, newAgent.replayedEvts)
}

func TestManager_NotifyAgentDead_NoFactory(t *testing.T) {
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	agent := newMockAgent("a1")
	require.NoError(t, m.StartAgent(ctx, agent))
	time.Sleep(50 * time.Millisecond)

	// NotifyAgentDead without a factory should not panic.
	assert.NotPanics(t, func() {
		m.NotifyAgentDead("a1", "no factory")
	})
}

func TestManager_NotifyAgentDead_ConcurrentRespectsLimit(t *testing.T) {
	factory := newMockFactory()
	config := &Config{
		HealthCheckInterval: 100 * time.Millisecond,
		MaxRestartsPerAgent: 3,
	}
	m := New(config, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	agent := newMockAgent("a1")
	m.RegisterAgent(agent, factory.create())
	require.NoError(t, m.StartAgent(ctx, agent))
	time.Sleep(50 * time.Millisecond)

	// Fire many concurrent NotifyAgentDead calls.
	// With the TOCTOU fix, at most MaxRestartsPerAgent (3) should proceed.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m.NotifyAgentDead("a1", fmt.Sprintf("concurrent-crash-%d", i))
		}(i)
	}
	wg.Wait()

	// Wait for async restores to complete.
	time.Sleep(500 * time.Millisecond)

	// The restart count on the agent should not exceed MaxRestartsPerAgent.
	m.mu.RLock()
	ma := m.agents["a1"]
	restarts := 0
	if ma != nil {
		restarts = ma.restarts
	}
	m.mu.RUnlock()

	assert.LessOrEqual(t, restarts, config.MaxRestartsPerAgent,
		"restarts should not exceed MaxRestartsPerAgent even under concurrency")
}

func TestManager_NotifyAgentDead_MaxRestartsExceeded(t *testing.T) {
	factory := newMockFactory()
	config := &Config{
		HealthCheckInterval: 100 * time.Millisecond,
		MaxRestartsPerAgent: 1,
	}
	m := New(config, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	agent := newMockAgent("a1")
	m.RegisterAgent(agent, factory.create())
	require.NoError(t, m.StartAgent(ctx, agent))
	time.Sleep(50 * time.Millisecond)

	// Simulate an agent with 1 restart already (matching the max).
	m.mu.Lock()
	if ma, ok := m.agents["a1"]; ok {
		ma.restarts = 1
	}
	m.mu.Unlock()

	// NotifyAgentDead should skip because max restarts reached.
	m.NotifyAgentDead("a1", "max test")
	time.Sleep(200 * time.Millisecond)

	factory.mu.Lock()
	callCount := factory.callCount
	factory.mu.Unlock()
	assert.Equal(t, 0, callCount, "factory should not be called when max restarts exceeded")
}

func TestManager_Start_ReturnsError(t *testing.T) {
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	// Agent that returns error on Start.
	failAgent := newMockAgent("fail-agent")
	failAgent.startFn = func(ctx context.Context) error {
		return fmt.Errorf("start failure")
	}

	err := m.StartAgent(ctx, failAgent)
	// StartAgent itself should succeed (error is handled internally).
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)
}

func TestManager_Stop_RuntimeNotStarted(t *testing.T) {
	m := New(nil, nil, nil)

	// Stop before start should be a no-op.
	err := m.Stop()
	assert.NoError(t, err)
}

func TestManager_StartAgent_RuntimeStopped(t *testing.T) {
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))
	require.NoError(t, m.Stop())

	agent := newMockAgent("a1")
	err := m.StartAgent(ctx, agent)
	assert.ErrorIs(t, err, ErrRuntimeStopped)
}

// TestManager_StartAgent_BeforeStart verifies that calling StartAgent before
// Start() does not panic. The errgroup is initialized in New(), so m.g.Go()
// is safe even without Start().
func TestManager_StartAgent_BeforeStart(t *testing.T) {
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agent := newMockAgent("a1")
	// Before the fix, this would panic because m.g was nil.
	// After the fix, the errgroup is initialized in New() with a background context.
	assert.NotPanics(t, func() {
		_ = m.StartAgent(ctx, agent)
	})
}

// TestManager_NotifyAgentDead_BeforeStart verifies that calling NotifyAgentDead
// before Start() does not panic.
func TestManager_NotifyAgentDead_BeforeStart(t *testing.T) {
	factory := newMockFactory()
	m := New(nil, nil, nil)

	agent := newMockAgent("a1")
	m.RegisterAgent(agent, factory.create())

	assert.NotPanics(t, func() {
		m.NotifyAgentDead("a1", "test before start")
	})
}

// TestManager_RestoreAgent_WithEventStore verifies that RestoreAgent replays ares_events
// from the EventStore and passes them to a StatefulAgent.
func TestManager_RestoreAgent_WithEventStore(t *testing.T) {
	eventStore := ares_events.NewMemoryEventStore()
	m := New(nil, eventStore, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	// Pre-populate the event store with two ares_events for stream "a1".
	err := eventStore.Append(ctx, "a1", []*ares_events.Event{
		{
			Type:    ares_events.EventSessionCreated,
			Payload: map[string]any{"session_id": "sess-abc"},
		},
		{
			Type:    ares_events.EventTaskCreated,
			Payload: map[string]any{"task_id": "task-1"},
		},
	}, 0)
	require.NoError(t, err)

	factory := newMockFactory()
	err = m.RestoreAgent(ctx, "a1", factory.create())
	require.NoError(t, err)

	// Wait for the agent goroutine to call Start.
	time.Sleep(100 * time.Millisecond)

	restoredAgent := factory.lastAgent()
	require.NotNil(t, restoredAgent, "factory should have created an agent")

	// Events must have been replayed to the StatefulAgent.
	require.NotNil(t, restoredAgent.replayedEvts, "ares_events should have been replayed")
	assert.Len(t, restoredAgent.replayedEvts, 3) // 2 pre-populated + 1 failover.triggered

	// State should contain session_id extracted from EventSessionCreated.
	require.NotNil(t, restoredAgent.restoredState, "state should have been restored")
	assert.Equal(t, "sess-abc", restoredAgent.restoredState["session_id"])

	// Agent should be running.
	assert.Equal(t, models.AgentStatusReady, restoredAgent.Status())
}

// TestManager_RestoreAgent_WithMemoryManager verifies that RestoreAgent loads
// cognitive state (conversation history) via MemoryManager during restoration.
func TestManager_RestoreAgent_WithMemoryManager(t *testing.T) {
	eventStore := ares_events.NewMemoryEventStore()
	memManager := newMockMemoryManager()
	m := New(nil, eventStore, memManager)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	// Pre-populate the event store with a session event.
	err := eventStore.Append(ctx, "a1", []*ares_events.Event{
		{
			Type:    ares_events.EventSessionCreated,
			Payload: map[string]any{"session_id": "sess-mem"},
		},
	}, 0)
	require.NoError(t, err)

	factory := newMockFactory()
	err = m.RestoreAgent(ctx, "a1", factory.create())
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	restoredAgent := factory.lastAgent()
	require.NotNil(t, restoredAgent)

	// RestoreState should have been called with combined state.
	require.NotNil(t, restoredAgent.restoredState)
	assert.Equal(t, "sess-mem", restoredAgent.restoredState["session_id"])

	// Conversation history from the mock memory manager should be present.
	history, ok := restoredAgent.restoredState["conversation_history"]
	require.True(t, ok, "conversation_history should be in restored state")
	assert.Len(t, history.([]memory.Message), 2)

	// Events should have been replayed.
	require.NotNil(t, restoredAgent.replayedEvts)
	assert.Len(t, restoredAgent.replayedEvts, 2) // 1 pre-populated + 1 failover.triggered
}

// TestManager_RestoreAgent_AgentNotStatefulAgent verifies that RestoreAgent works
// correctly when the factory returns an agent that does NOT implement StatefulAgent.
// State restoration and event replay should be skipped without error.
func TestManager_RestoreAgent_AgentNotStatefulAgent(t *testing.T) {
	factory := newNonStatefulFactory()
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	err := m.RestoreAgent(ctx, "a1", factory.create("a1"))
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	restoredAgent := factory.lastAgent()
	require.NotNil(t, restoredAgent, "factory should have created an agent")
	assert.Equal(t, "a1", restoredAgent.ID())

	// Agent should be running despite not implementing StatefulAgent.
	assert.Equal(t, models.AgentStatusReady, restoredAgent.Status())

	// GetAgent should return the restored agent.
	got := m.GetAgent("a1")
	assert.Equal(t, restoredAgent, chaosUnwrap(got))
}

// TestManager_RestoreAgent_EventStoreError verifies that when the EventStore returns
// an error, the agent is still created and started without restored state.
func TestManager_RestoreAgent_EventStoreError(t *testing.T) {
	store := &errEventStore{readErr: fmt.Errorf("database connection lost")}
	factory := newMockFactory()
	m := New(nil, store, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	err := m.RestoreAgent(ctx, "a1", factory.create())
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	// Agent should have been created and started despite EventStore failure.
	restoredAgent := factory.lastAgent()
	require.NotNil(t, restoredAgent)

	// No ares_events were available, so RestoreState and ReplayEvents should not have been called.
	assert.Nil(t, restoredAgent.restoredState, "no state should be restored on event store error")
	assert.Nil(t, restoredAgent.replayedEvts, "no ares_events should be replayed on event store error")

	// Agent should still be running.
	assert.Equal(t, models.AgentStatusReady, restoredAgent.Status())
}

// TestManager_RestoreAgent_MemoryManagerError verifies that when the MemoryManager
// returns an error during cognitive recovery, the agent is still restored and started.
func TestManager_RestoreAgent_MemoryManagerError(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	memMgr := &errMemoryManager{
		sessionErr:  fmt.Errorf("checkpoint table corrupted"),
		messagesErr: fmt.Errorf("messages query timeout"),
	}
	factory := newMockFactory()
	m := New(nil, store, memMgr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	// Append a session event so buildCognitiveState tries to load messages.
	err := store.Append(ctx, "a1", []*ares_events.Event{
		{
			Type:    ares_events.EventSessionCreated,
			Payload: map[string]any{"session_id": "sess-err"},
		},
	}, 0)
	require.NoError(t, err)

	err = m.RestoreAgent(ctx, "a1", factory.create())
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	restoredAgent := factory.lastAgent()
	require.NotNil(t, restoredAgent)

	// The operational state (session_id) should still be restored from ares_events.
	require.NotNil(t, restoredAgent.restoredState)
	assert.Equal(t, "sess-err", restoredAgent.restoredState["session_id"])

	// Conversation history should NOT be present because GetMessages returned an error.
	_, hasHistory := restoredAgent.restoredState["conversation_history"]
	assert.False(t, hasHistory, "conversation_history should be absent when GetMessages fails")

	// Agent should be running despite memory manager errors.
	assert.Equal(t, models.AgentStatusReady, restoredAgent.Status())
}

// TestManager_NotifyAgentDead_AfterStop verifies that NotifyAgentDead is a no-op
// when the runtime has been stopped, preventing resurrection of agents during shutdown.
func TestManager_NotifyAgentDead_AfterStop(t *testing.T) {
	factory := newMockFactory()
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	agent := newMockAgent("a1")
	m.RegisterAgent(agent, factory.create())
	require.NoError(t, m.StartAgent(ctx, agent))
	time.Sleep(50 * time.Millisecond)

	// Stop the runtime.
	require.NoError(t, m.Stop())

	// NotifyAgentDead after stop should be a no-op.
	m.NotifyAgentDead("a1", "late crash")
	time.Sleep(200 * time.Millisecond)

	factory.mu.Lock()
	callCount := factory.callCount
	factory.mu.Unlock()

	// Factory should NOT have been called because runtime is stopped.
	assert.Equal(t, 0, callCount, "factory should not be called after runtime is stopped")
}

// TestManager_NotifyAgentDead_NoFactory verifies that NotifyAgentDead correctly
// skips restoration when no factory is registered for the agent.
func TestManager_NotifyAgentDead_NoFactoryRegistered(t *testing.T) {
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	agent := newMockAgent("a1")
	// Start the agent without registering a factory.
	require.NoError(t, m.StartAgent(ctx, agent))
	time.Sleep(50 * time.Millisecond)

	// NotifyAgentDead should be a no-op without panicking.
	assert.NotPanics(t, func() {
		m.NotifyAgentDead("a1", "crash without factory")
	})
	time.Sleep(200 * time.Millisecond)

	// Total restarts should remain zero since no factory was registered.
	stats := m.Stats()
	assert.Equal(t, 0, stats.TotalRestarts)
}

// TestManager_Stop_ConcurrentStopAgent verifies that Stop() and StopAgent() called
// concurrently do not cause panics or data races.
func TestManager_Stop_ConcurrentStopAgent(t *testing.T) {
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	// Register and start multiple agents.
	for i := 0; i < 5; i++ {
		agent := newMockAgent(fmt.Sprintf("agent-%d", i))
		require.NoError(t, m.StartAgent(ctx, agent))
	}
	time.Sleep(100 * time.Millisecond)

	var wg sync.WaitGroup

	// Call StopAgent for specific agents concurrently.
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_ = m.StopAgent(ctx, id)
		}(fmt.Sprintf("agent-%d", i))
	}

	// Call Stop() concurrently.
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = m.Stop()
	}()

	wg.Wait()

	// All agents should be stopped.
	stats := m.Stats()
	assert.Equal(t, 0, stats.ActiveAgents)
}

// TestManager_Stop_WithRunningAgents verifies that Stop() gracefully stops all
// currently running agents.
func TestManager_Stop_WithRunningAgents(t *testing.T) {
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	agents := make([]*mockAgent, 5)
	for i := 0; i < 5; i++ {
		agents[i] = newMockAgent(fmt.Sprintf("agent-%d", i))
		require.NoError(t, m.StartAgent(ctx, agents[i]))
	}
	time.Sleep(100 * time.Millisecond)

	// Verify all agents are active.
	stats := m.Stats()
	assert.Equal(t, 5, stats.ActiveAgents)

	// Stop the runtime.
	require.NoError(t, m.Stop())

	// All agents should have had Stop called.
	for i, agent := range agents {
		assert.GreaterOrEqual(t, agent.stopped.Load(), int32(1),
			"agent-%d should have been stopped", i)
	}

	// No agents should remain active.
	stats = m.Stats()
	assert.Equal(t, 0, stats.ActiveAgents)
}

// TestManager_Stop_TimeoutRespected verifies that agents which take too long to stop
// are cancelled via context and Stop() still completes within the configured timeout.
func TestManager_Stop_TimeoutRespected(t *testing.T) {
	config := &Config{
		HealthCheckInterval: 1 * time.Second,
		AgentStopTimeout:    100 * time.Millisecond,
		OverallStopTimeout:  500 * time.Millisecond,
	}
	m := New(config, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	// Register a slow-stop agent that blocks until its blocker channel is closed.
	slow := newSlowStopAgent("slow-agent")
	require.NoError(t, m.StartAgent(ctx, slow))
	time.Sleep(50 * time.Millisecond)

	// Stop the runtime. The slow agent should be cancelled by context timeout.
	start := time.Now()
	err := m.Stop()
	elapsed := time.Since(start)
	require.NoError(t, err)

	// Stop should complete in roughly AgentStopTimeout, not hang forever.
	assert.Less(t, elapsed, 5*time.Second,
		"Stop should not block indefinitely even with a slow agent")

	// The slow agent's Stop should have been called at least once.
	assert.GreaterOrEqual(t, slow.stopped.Load(), int32(1))
}

// TestManager_Stats_AfterMultipleOperations verifies that Stats() accurately reflects
// the runtime state after a series of register, start, stop, and restart operations.
func TestManager_Stats_AfterMultipleOperations(t *testing.T) {
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	// Register and start two agents.
	agent1 := newMockAgent("a1")
	agent2 := newMockAgent("a2")
	factory1 := func() base.Agent { return newMockAgent("a1") }
	factory2 := func() base.Agent { return newMockAgent("a2") }

	m.RegisterAgent(agent1, factory1)
	m.RegisterAgent(agent2, factory2)

	require.NoError(t, m.StartAgent(ctx, agent1))
	require.NoError(t, m.StartAgent(ctx, agent2))
	time.Sleep(100 * time.Millisecond)

	stats := m.Stats()
	assert.Equal(t, 2, stats.ActiveAgents)
	assert.Equal(t, 0, stats.TotalRestarts)

	// Stop one agent.
	require.NoError(t, m.StopAgent(ctx, "a1"))
	time.Sleep(50 * time.Millisecond)

	stats = m.Stats()
	assert.Equal(t, 1, stats.ActiveAgents)
	assert.Equal(t, 0, stats.TotalRestarts)

	// Restart the other agent.
	require.NoError(t, m.RestartAgent(ctx, "a2"))
	time.Sleep(100 * time.Millisecond)

	stats = m.Stats()
	assert.Equal(t, 1, stats.ActiveAgents, "restarted agent should count as one active agent")
	assert.Equal(t, 1, stats.TotalRestarts, "restart should increment total restarts")
	assert.Greater(t, stats.Uptime, time.Duration(0), "uptime should be positive after operations")
}

// TestManager_Stats_ConcurrentAccess verifies that Stats() is safe to call
// while agents are being registered and stopped concurrently.
func TestManager_Stats_ConcurrentAccess(t *testing.T) {
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	var wg sync.WaitGroup
	errs := make(chan error, 30)

	// Concurrently register and start agents.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			agentID := fmt.Sprintf("concurrent-agent-%d", id)
			agent := newMockAgent(agentID)
			if err := m.StartAgent(ctx, agent); err != nil {
				errs <- err
			}
		}(i)
	}

	// Concurrently read Stats.
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stats := m.Stats()
			// ActiveAgents must be non-negative.
			if stats.ActiveAgents < 0 {
				errs <- fmt.Errorf("negative ActiveAgents: %d", stats.ActiveAgents)
			}
		}()
	}

	// Concurrently stop agents.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			agentID := fmt.Sprintf("concurrent-agent-%d", id)
			_ = m.StopAgent(ctx, agentID)
		}(i)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent operation error: %v", err)
	}
}

// TestManager_GetAgent_Exists verifies that GetAgent returns the correct agent
// instance when the agent is registered and running.
func TestManager_GetAgent_Exists(t *testing.T) {
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	agent := newMockAgent("a1")
	require.NoError(t, m.StartAgent(ctx, agent))
	time.Sleep(50 * time.Millisecond)

	got := m.GetAgent("a1")
	require.NotNil(t, got, "GetAgent should return the registered agent")
	assert.Equal(t, "a1", got.ID())
}

// TestManager_GetAgent_NotExists verifies that GetAgent returns nil
// when the requested agent ID is not registered.
func TestManager_GetAgent_NotExists(t *testing.T) {
	m := New(nil, nil, nil)

	got := m.GetAgent("nonexistent")
	assert.Nil(t, got, "GetAgent should return nil for unregistered agent")
}

// TestManager_GetAgent_AfterRestore verifies that GetAgent returns the new agent
// instance after a RestoreAgent call replaces the original agent.
func TestManager_GetAgent_AfterRestore(t *testing.T) {
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, m.Start(ctx))

	// Register and start the original agent.
	original := newMockAgent("a1")
	factory := newMockFactory()
	m.RegisterAgent(original, factory.create())
	require.NoError(t, m.StartAgent(ctx, original))
	time.Sleep(50 * time.Millisecond)

	// GetAgent should return the original agent.
	gotBefore := m.GetAgent("a1")
	assert.Equal(t, original, chaosUnwrap(gotBefore), "GetAgent should return the original agent")

	// RestoreAgent creates a new instance from the factory.
	err := m.RestoreAgent(ctx, "a1", factory.create())
	require.NoError(t, err)
	time.Sleep(100 * time.Millisecond)

	// GetAgent should now return the new (restored) agent, not the original.
	gotAfter := m.GetAgent("a1")
	require.NotNil(t, gotAfter, "GetAgent should return the restored agent")
	assert.NotEqual(t, original, chaosUnwrap(gotAfter), "GetAgent should return the new agent, not the original")

	// The restored agent should be a StatefulAgent from the factory.
	newAgent := factory.lastAgent()
	require.NotNil(t, newAgent)
	assert.Equal(t, newAgent, chaosUnwrap(gotAfter))
}

// deadHeartbeatAgent implements base.Agent AND base.Heartbeater with
// IsAlive()==false, so the runtime health check detects it as dead and routes
// it through the resurrection path (NotifyAgentDead).
type deadHeartbeatAgent struct {
	*mockAgent
}

// Heartbeat satisfies base.Heartbeater; a no-op for this mock.
func (a *deadHeartbeatAgent) Heartbeat(context.Context) error { return nil }

// IsAlive is the Heartbeater liveness probe; always false for this mock.
func (a *deadHeartbeatAgent) IsAlive() bool { return false }

// aliveHeartbeatAgent is the healthy counterpart: it implements Heartbeater
// and reports alive, so the health check must NOT resurrect it.
type aliveHeartbeatAgent struct {
	*mockAgent
}

// Heartbeat satisfies base.Heartbeater; a no-op for this mock.
func (a *aliveHeartbeatAgent) Heartbeat(context.Context) error { return nil }

// IsAlive reports the agent as healthy.
func (a *aliveHeartbeatAgent) IsAlive() bool { return true }

// TestManager_HealthCheck_DetectsDeadHeartbeatAndResurrects is the health-poll
// coverage gap found in code review: the background
// health check must detect a dead heartbeater and trigger resurrection via
// NotifyAgentDead → factory. It exercises healthCheck() directly rather than
// waiting on the ticker, keeping the test fast and deterministic.
func TestManager_HealthCheck_DetectsDeadHeartbeatAndResurrects(t *testing.T) {
	factory := newMockFactory()
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, m.Start(ctx))

	// A heartbeater agent that reports dead. The factory must be registered
	// so NotifyAgentDead can resurrect through it.
	dead := &deadHeartbeatAgent{mockAgent: newMockAgent("dead-hb")}
	m.RegisterAgent(dead, factory.create())
	require.NoError(t, m.StartAgent(ctx, dead))
	time.Sleep(50 * time.Millisecond)

	// Sanity: the agent is registered as active before the health check.
	stats := m.Stats()
	assert.Equal(t, 1, stats.ActiveAgents)

	// Run one health check pass directly.
	m.healthCheck()

	// Resurrection is async (scheduleResurrection runs on the runtime errgroup
	// with an immediate first attempt); poll the factory until it fires or we
	// time out, so the test stays deterministic without over-sleeping.
	deadline := time.Now().Add(3 * time.Second)
	for {
		factory.mu.Lock()
		callCount := len(factory.agents)
		factory.mu.Unlock()
		if callCount >= 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dead heartbeater was not resurrected within 3s (factory calls=0)")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestManager_HealthCheck_IgnoresHealthyHeartbeat verifies the health check
// does not resurrect a heartbeater that reports alive.
func TestManager_HealthCheck_IgnoresHealthyHeartbeat(t *testing.T) {
	factory := newMockFactory()
	m := New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, m.Start(ctx))

	// A Heartbeater agent that reports alive must not be resurrected.
	alive := &aliveHeartbeatAgent{mockAgent: newMockAgent("healthy")}
	m.RegisterAgent(alive, factory.create())
	require.NoError(t, m.StartAgent(ctx, alive))
	time.Sleep(50 * time.Millisecond)

	m.healthCheck()
	time.Sleep(200 * time.Millisecond)

	factory.mu.Lock()
	callCount := len(factory.agents)
	factory.mu.Unlock()
	assert.Equal(t, 0, callCount,
		"healthy heartbeaters must not be resurrected")
}
