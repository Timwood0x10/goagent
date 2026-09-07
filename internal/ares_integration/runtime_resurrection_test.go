// package integration provides end-to-end integration tests for the
// Runtime + EventStore + Agent resurrection flow.
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

// --- Test helpers for ares_runtime resurrection tests ---

// resurrectionAgent is a controllable mock agent that supports StatefulAgent
// for testing the full resurrection flow with state recovery.
type resurrectionAgent struct {
	mu            sync.RWMutex
	id            string
	agentTyp      models.AgentType
	status        models.AgentStatus
	started       atomic.Int32
	stopped       atomic.Int32
	restoredState map[string]any
	replayedEvts  []*ares_events.Event
}

func newResurrectionAgent(id string, agentType models.AgentType) *resurrectionAgent {
	return &resurrectionAgent{
		id:       id,
		agentTyp: agentType,
		status:   models.AgentStatusOffline,
	}
}

func (a *resurrectionAgent) ID() string             { return a.id }
func (a *resurrectionAgent) Type() models.AgentType { return a.agentTyp }
func (a *resurrectionAgent) Status() models.AgentStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.status
}

func (a *resurrectionAgent) setStatus(s models.AgentStatus) {
	a.mu.Lock()
	a.status = s
	a.mu.Unlock()
}

func (a *resurrectionAgent) Start(ctx context.Context) error {
	a.started.Add(1)
	a.setStatus(models.AgentStatusReady)
	<-ctx.Done()
	a.setStatus(models.AgentStatusOffline)
	return nil
}

func (a *resurrectionAgent) Stop(_ context.Context) error {
	a.stopped.Add(1)
	a.setStatus(models.AgentStatusOffline)
	return nil
}

func (a *resurrectionAgent) Process(_ context.Context, _ any) (any, error) {
	return nil, errors.New("not implemented in mock")
}

func (a *resurrectionAgent) ProcessStream(_ context.Context, _ any) (<-chan base.AgentEvent, error) {
	ch := make(chan base.AgentEvent)
	close(ch)
	return ch, nil
}

func (a *resurrectionAgent) RestoreState(state map[string]any) error {
	a.mu.Lock()
	a.restoredState = state
	a.mu.Unlock()
	return nil
}

func (a *resurrectionAgent) ReplayEvents(evts []*ares_events.Event) error {
	a.mu.Lock()
	a.replayedEvts = evts
	a.mu.Unlock()
	return nil
}

func (a *resurrectionAgent) Snapshot() (map[string]any, error) {
	return map[string]any{"agent_id": a.id}, nil
}

func (a *resurrectionAgent) getRestoredState() map[string]any {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.restoredState
}

// TestRuntimeResurrection_FullCycle tests the full resurrection flow:
// register agent -> start -> emit ares_events -> kill -> verify resurrection -> verify state recovery.
func TestRuntimeResurrection_FullCycle(t *testing.T) {
	eventStore := ares_events.NewMemoryEventStore()
	config := &runtime.Config{
		HealthCheckInterval: 50 * time.Millisecond,
		MaxRestartsPerAgent: 5,
		MaxReplayEvents:     100,
	}
	mgr := runtime.New(config, eventStore, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agent := newResurrectionAgent("res-agent-1", models.AgentTypeTop)

	// Track factory calls to verify resurrection happened.
	var factoryCalls atomic.Int32
	factory := func() base.Agent {
		factoryCalls.Add(1)
		return newResurrectionAgent("res-agent-1", models.AgentTypeTop)
	}

	mgr.RegisterAgent(agent, factory)

	// Pre-populate ares_events to simulate prior session activity.
	err := eventStore.Append(ctx, "res-agent-1", []*ares_events.Event{
		{
			Type:    ares_events.EventSessionCreated,
			Payload: map[string]any{"session_id": "session-001"},
		},
		{
			Type:    ares_events.EventTaskCreated,
			Payload: map[string]any{"task_id": "task-001"},
		},
	}, 0)
	require.NoError(t, err)

	// Start the runtime.
	require.NoError(t, mgr.Start(ctx))

	// Wait for the agent to start.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if agent.Status() == models.AgentStatusReady {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	assert.Equal(t, models.AgentStatusReady, agent.Status())
	assert.Equal(t, int32(1), agent.started.Load())

	// Kill the agent to trigger resurrection.
	mgr.NotifyAgentDead("res-agent-1", "simulated crash")

	// Wait for resurrection via factory call.
	resDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(resDeadline) {
		if factoryCalls.Load() >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	assert.GreaterOrEqual(t, factoryCalls.Load(), int32(1), "factory should have been called")

	// Verify a new agent is active.
	stats := mgr.Stats()
	assert.Equal(t, 1, stats.ActiveAgents, "one agent should be active after resurrection")
	assert.GreaterOrEqual(t, stats.TotalRestarts, 1, "total restarts should be >= 1")

	// Verify state was recovered from ares_events: the restored agent should have
	// received the session_id from EventSessionCreated ares_events.
	newAgent := mgr.GetAgent("res-agent-1")
	require.NotNil(t, newAgent)
	// The runtime wraps every agent in a chaos-injection decorator, so peel it
	// back to reach the concrete resurrectionAgent before asserting the type.
	statefulAgent, ok := runtime.UnwrapAgent(newAgent).(*resurrectionAgent)
	require.True(t, ok, "restored agent should be resurrectionAgent")

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if statefulAgent.getRestoredState() != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	restoredState := statefulAgent.getRestoredState()
	require.NotNil(t, restoredState, "restored state should not be nil")
	assert.Equal(t, "session-001", restoredState["session_id"], "session_id should be recovered from ares_events")
}

// TestRuntimeResurrection_MultipleAgents registers 3 agents, kills 2, and
// verifies both resurrect independently while the third remains untouched.
func TestRuntimeResurrection_MultipleAgents(t *testing.T) {
	eventStore := ares_events.NewMemoryEventStore()
	config := &runtime.Config{
		HealthCheckInterval: 50 * time.Millisecond,
		MaxRestartsPerAgent: 5,
	}
	mgr := runtime.New(config, eventStore, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agents := make([]*resurrectionAgent, 3)
	agentIDs := []string{"multi-1", "multi-2", "multi-3"}
	var factoryCalls [3]atomic.Int32

	for i := 0; i < 3; i++ {
		agents[i] = newResurrectionAgent(agentIDs[i], models.AgentTypeTop)
		idx := i
		factory := func() base.Agent {
			factoryCalls[idx].Add(1)
			return newResurrectionAgent(agentIDs[idx], models.AgentTypeTop)
		}
		mgr.RegisterAgent(agents[i], factory)
	}

	// Pre-populate ares_events for all agents.
	for _, id := range agentIDs {
		err := eventStore.Append(ctx, id, []*ares_events.Event{
			{
				Type:    ares_events.EventSessionCreated,
				Payload: map[string]any{"session_id": "session-" + id},
			},
		}, 0)
		require.NoError(t, err)
	}

	require.NoError(t, mgr.Start(ctx))

	// Wait for all agents to start.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		allReady := true
		for _, a := range agents {
			if a.Status() != models.AgentStatusReady {
				allReady = false
				break
			}
		}
		if allReady {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Kill agents 0 and 2, leave agent 1 alive.
	mgr.NotifyAgentDead("multi-1", "crash-1")
	mgr.NotifyAgentDead("multi-3", "crash-3")

	// Wait for both resurrections.
	resDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(resDeadline) {
		if factoryCalls[0].Load() >= 1 && factoryCalls[2].Load() >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	assert.GreaterOrEqual(t, factoryCalls[0].Load(), int32(1), "agent-1 factory should be called")
	assert.Equal(t, int32(0), factoryCalls[1].Load(), "agent-2 factory should NOT be called")
	assert.GreaterOrEqual(t, factoryCalls[2].Load(), int32(1), "agent-3 factory should be called")

	stats := mgr.Stats()
	assert.Equal(t, 3, stats.ActiveAgents, "all 3 agents should be active")
	assert.GreaterOrEqual(t, stats.TotalRestarts, 2, "total restarts should be >= 2")
}

// TestRuntimeResurrection_ConcurrentKillAndResurrect kills one agent while
// another is being resurrected, verifying no race conditions or panics.
func TestRuntimeResurrection_ConcurrentKillAndResurrect(t *testing.T) {
	eventStore := ares_events.NewMemoryEventStore()
	config := &runtime.Config{
		HealthCheckInterval: 50 * time.Millisecond,
		MaxRestartsPerAgent: 5,
		RestoreTimeout:      5 * time.Second,
	}
	mgr := runtime.New(config, eventStore, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agentA := newResurrectionAgent("concurrent-a", models.AgentTypeTop)
	agentB := newResurrectionAgent("concurrent-b", models.AgentTypeTop)

	var factoryA, factoryB atomic.Int32
	mgr.RegisterAgent(agentA, func() base.Agent {
		factoryA.Add(1)
		return newResurrectionAgent("concurrent-a", models.AgentTypeTop)
	})
	mgr.RegisterAgent(agentB, func() base.Agent {
		factoryB.Add(1)
		return newResurrectionAgent("concurrent-b", models.AgentTypeTop)
	})

	require.NoError(t, mgr.Start(ctx))

	// Wait for both agents to start.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if agentA.Status() == models.AgentStatusReady && agentB.Status() == models.AgentStatusReady {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Kill both agents concurrently to trigger concurrent resurrection.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		mgr.NotifyAgentDead("concurrent-a", "crash-a")
	}()
	go func() {
		defer wg.Done()
		mgr.NotifyAgentDead("concurrent-b", "crash-b")
	}()
	wg.Wait()

	// Wait for both resurrections. The factory counter increments before the
	// new managedAgent is installed into the runtime map, so waiting on the
	// counter alone leaves a window where Stats() still sees the old entry
	// marked stopped and the new one not yet registered. Wait on the
	// observable end state (ActiveAgents) instead.
	resDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(resDeadline) {
		if factoryA.Load() >= 1 && factoryB.Load() >= 1 && mgr.Stats().ActiveAgents == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	assert.GreaterOrEqual(t, factoryA.Load(), int32(1), "agent-a should be resurrected")
	assert.GreaterOrEqual(t, factoryB.Load(), int32(1), "agent-b should be resurrected")

	stats := mgr.Stats()
	assert.Equal(t, 2, stats.ActiveAgents, "both agents should be active")
	assert.GreaterOrEqual(t, stats.TotalRestarts, 2)
}

// TestRuntimeResurrection_EventStoreUnavailable verifies that when EventStore
// is nil, resurrection still works but without state recovery.
func TestRuntimeResurrection_EventStoreUnavailable(t *testing.T) {
	// Pass nil EventStore.
	config := &runtime.Config{
		HealthCheckInterval: 50 * time.Millisecond,
		MaxRestartsPerAgent: 5,
	}
	mgr := runtime.New(config, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agent := newResurrectionAgent("no-store-agent", models.AgentTypeTop)
	var factoryCalls atomic.Int32
	factory := func() base.Agent {
		factoryCalls.Add(1)
		return newResurrectionAgent("no-store-agent", models.AgentTypeTop)
	}
	mgr.RegisterAgent(agent, factory)

	require.NoError(t, mgr.Start(ctx))

	// Wait for agent to start.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if agent.Status() == models.AgentStatusReady {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	assert.Equal(t, models.AgentStatusReady, agent.Status())

	// Kill the agent.
	mgr.NotifyAgentDead("no-store-agent", "crash")

	// Wait for resurrection.
	resDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(resDeadline) {
		if factoryCalls.Load() >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	assert.GreaterOrEqual(t, factoryCalls.Load(), int32(1), "factory should be called")

	// The new agent should NOT have any restored state (no ares_events to replay).
	newAgent := mgr.GetAgent("no-store-agent")
	require.NotNil(t, newAgent)
	// Peel the chaos-injection decorator to reach the concrete resurrectionAgent.
	stateful, ok := runtime.UnwrapAgent(newAgent).(*resurrectionAgent)
	require.True(t, ok)

	// With nil EventStore, replayEvents returns nil, so no state is restored.
	assert.Nil(t, stateful.getRestoredState(), "no state should be restored without EventStore")
}

// TestRuntimeResurrection_MaxRestartsExceeded verifies that an agent that
// crashes MaxRestartsPerAgent times stops being resurrected.
func TestRuntimeResurrection_MaxRestartsExceeded(t *testing.T) {
	eventStore := ares_events.NewMemoryEventStore()
	const maxRestarts = 3
	config := &runtime.Config{
		HealthCheckInterval: 50 * time.Millisecond,
		MaxRestartsPerAgent: maxRestarts,
		RestoreTimeout:      2 * time.Second,
	}
	mgr := runtime.New(config, eventStore, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agent := newResurrectionAgent("max-restart-agent", models.AgentTypeTop)
	var factoryCalls atomic.Int32
	factory := func() base.Agent {
		factoryCalls.Add(1)
		return newResurrectionAgent("max-restart-agent", models.AgentTypeTop)
	}
	mgr.RegisterAgent(agent, factory)

	require.NoError(t, mgr.Start(ctx))

	// Wait for agent to start.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if agent.Status() == models.AgentStatusReady {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Kill the agent repeatedly up to MaxRestartsPerAgent.
	for i := 0; i < maxRestarts+1; i++ {
		mgr.NotifyAgentDead("max-restart-agent", "crash")
		// Give time for resurrection to be attempted.
		time.Sleep(300 * time.Millisecond)
	}

	// The factory should have been called at most MaxRestartsPerAgent times.
	// After exceeding the limit, NotifyAgentDead returns without scheduling restore.
	assert.LessOrEqual(t, factoryCalls.Load(), int32(maxRestarts),
		"factory calls should not exceed MaxRestartsPerAgent")

	// Verify ares_runtime stats reflect the cap.
	stats := mgr.Stats()
	assert.LessOrEqual(t, stats.TotalRestarts, maxRestarts,
		"total restarts should not exceed max")
}
