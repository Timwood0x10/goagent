// package integration provides end-to-end integration tests for the ares_runtime layer.
package ares_integration

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/agents/base"
	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/runtime"
)

// --- Mock agents for integration tests ---

// integrationAgent is a controllable mock agent for integration tests.
type integrationAgent struct {
	mu       sync.RWMutex
	id       string
	agentTyp models.AgentType
	status   models.AgentStatus
	started  atomic.Int32
	stopped  atomic.Int32
}

func newIntegrationAgent(id string, agentType models.AgentType) *integrationAgent {
	return &integrationAgent{
		id:       id,
		agentTyp: agentType,
		status:   models.AgentStatusOffline,
	}
}

func (a *integrationAgent) ID() string             { return a.id }
func (a *integrationAgent) Type() models.AgentType { return a.agentTyp }
func (a *integrationAgent) Status() models.AgentStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.status
}

func (a *integrationAgent) setStatus(s models.AgentStatus) {
	a.mu.Lock()
	a.status = s
	a.mu.Unlock()
}

func (a *integrationAgent) Start(ctx context.Context) error {
	a.started.Add(1)
	a.setStatus(models.AgentStatusReady)
	<-ctx.Done()
	a.setStatus(models.AgentStatusOffline)
	return nil
}

func (a *integrationAgent) Stop(_ context.Context) error {
	a.stopped.Add(1)
	a.setStatus(models.AgentStatusOffline)
	return nil
}

func (a *integrationAgent) Process(_ context.Context, _ any) (any, error) {
	return nil, errors.New("not implemented in mock")
}

func (a *integrationAgent) ProcessStream(_ context.Context, _ any) (<-chan base.AgentEvent, error) {
	ch := make(chan base.AgentEvent)
	close(ch)
	return ch, nil
}

// integrationStatefulAgent extends integrationAgent with StatefulAgent.
type integrationStatefulAgent struct {
	*integrationAgent
	restoredState map[string]any
	replayedEvts  []*ares_events.Event
}

func newIntegrationStatefulAgent(id string, agentType models.AgentType) *integrationStatefulAgent {
	return &integrationStatefulAgent{
		integrationAgent: newIntegrationAgent(id, agentType),
	}
}

func (a *integrationStatefulAgent) RestoreState(state map[string]any) error {
	a.restoredState = state
	return nil
}

func (a *integrationStatefulAgent) ReplayEvents(evts []*ares_events.Event) error {
	a.replayedEvts = evts
	return nil
}

func (a *integrationStatefulAgent) Snapshot() (map[string]any, error) {
	return map[string]any{"agent_id": a.id}, nil
}

// --- Integration Tests ---

// TestRuntime_FullFailoverCycle tests the full lifecycle: register -> start -> kill -> restore -> verify.
func TestRuntime_FullFailoverCycle(t *testing.T) {
	eventStore := ares_events.NewMemoryEventStore()
	config := &runtime.Config{
		HealthCheckInterval: 50 * time.Millisecond,
		MaxRestartsPerAgent: 5,
	}
	m := runtime.New(config, eventStore, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Step 1: Create and register the agent.
	leader := newIntegrationStatefulAgent("leader-1", models.AgentTypeTop)
	var factoryCallCount atomic.Int32
	factory := func() base.Agent {
		factoryCallCount.Add(1)
		return newIntegrationStatefulAgent("leader-1", models.AgentTypeTop)
	}
	m.RegisterAgent(leader, factory)

	// Step 2: Pre-populate ares_events (simulating prior session).
	streamID := "leader-1"
	err := eventStore.Append(ctx, streamID, []*ares_events.Event{
		{
			Type: ares_events.EventSessionCreated,
			Payload: map[string]any{
				"session_id": "session-abc",
			},
		},
		{
			Type: ares_events.EventTaskCreated,
			Payload: map[string]any{
				"task_id": "task-001",
			},
		},
	}, 0)
	require.NoError(t, err)

	// Step 3: Start the runtime.
	require.NoError(t, m.Start(ctx))
	time.Sleep(100 * time.Millisecond)

	// Verify agent started.
	assert.Equal(t, int32(1), leader.started.Load())
	assert.Equal(t, models.AgentStatusReady, leader.Status())
	assert.Equal(t, 1, m.Stats().ActiveAgents)

	// Step 4: Kill the agent (simulate crash).
	m.NotifyAgentDead("leader-1", "simulated crash")

	// Step 5: Wait for restoration.
	time.Sleep(500 * time.Millisecond)

	// Verify factory was called.
	assert.GreaterOrEqual(t, factoryCallCount.Load(), int32(1), "factory should have been called")

	// Verify a new agent was created and restored.
	stats := m.Stats()
	assert.Equal(t, 1, stats.ActiveAgents, "one agent should be active after restore")
	assert.Equal(t, 1, stats.TotalRestarts, "total restarts should be 1")

	// The old leader should have been stopped.
	assert.Equal(t, int32(1), leader.stopped.Load())
}

// TestRuntime_MultipleAgentTypes tests that multiple agent types (leader + sub-agent)
// can be registered, started, and restored independently.
func TestRuntime_MultipleAgentTypes(t *testing.T) {
	eventStore := ares_events.NewMemoryEventStore()
	config := &runtime.Config{
		HealthCheckInterval: 50 * time.Millisecond,
		MaxRestartsPerAgent: 0, // Unlimited.
	}
	m := runtime.New(config, eventStore, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create leader and sub-agent.
	leader := newIntegrationStatefulAgent("leader-1", models.AgentTypeTop)
	sub := newIntegrationStatefulAgent("sub-1", models.AgentTypeTop)

	var leaderFactoryCalls, subFactoryCalls atomic.Int32
	leaderFactory := func() base.Agent {
		leaderFactoryCalls.Add(1)
		return newIntegrationStatefulAgent("leader-1", models.AgentTypeTop)
	}
	subFactory := func() base.Agent {
		subFactoryCalls.Add(1)
		return newIntegrationStatefulAgent("sub-1", models.AgentTypeTop)
	}

	// Register both agents.
	m.RegisterAgent(leader, leaderFactory)
	m.RegisterAgent(sub, subFactory)

	// Pre-populate ares_events for both agents.
	err := eventStore.Append(ctx, "leader-1", []*ares_events.Event{
		{
			Type:    ares_events.EventSessionCreated,
			Payload: map[string]any{"session_id": "leader-session"},
		},
	}, 0)
	require.NoError(t, err)

	err = eventStore.Append(ctx, "sub-1", []*ares_events.Event{
		{
			Type:    ares_events.EventTaskCreated,
			Payload: map[string]any{"task_id": "sub-task-1"},
		},
	}, 0)
	require.NoError(t, err)

	// Start runtime.
	require.NoError(t, m.Start(ctx))
	time.Sleep(100 * time.Millisecond)

	// Both agents should be running.
	assert.Equal(t, int32(1), leader.started.Load())
	assert.Equal(t, int32(1), sub.started.Load())
	assert.Equal(t, 2, m.Stats().ActiveAgents)

	// Kill only the leader.
	m.NotifyAgentDead("leader-1", "leader crash")
	time.Sleep(500 * time.Millisecond)

	// Leader should have been restored.
	assert.GreaterOrEqual(t, leaderFactoryCalls.Load(), int32(1), "leader factory should have been called")
	// Sub-agent factory should NOT have been called.
	assert.Equal(t, int32(0), subFactoryCalls.Load())
	// Sub-agent should still be running.
	assert.Equal(t, int32(0), sub.stopped.Load())

	// Now kill the sub-agent.
	m.NotifyAgentDead("sub-1", "sub crash")
	time.Sleep(500 * time.Millisecond)

	// Sub-agent should have been restored.
	assert.GreaterOrEqual(t, subFactoryCalls.Load(), int32(1), "sub factory should have been called")

	// Both should be active.
	stats := m.Stats()
	assert.Equal(t, 2, stats.ActiveAgents)
	assert.GreaterOrEqual(t, stats.TotalRestarts, 2)
}
