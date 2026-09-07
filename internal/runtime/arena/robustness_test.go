package arena

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ares_runtime "github.com/Timwood0x10/ares/internal/ares_runtime"
)

func TestRobustness_ConcurrentFaults(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	seen := make(map[string]int)
	rt := &mockRuntime{
		stopAgentFn: func(_ context.Context, id string) error {
			mu.Lock()
			seen["stop:"+id]++
			mu.Unlock()
			return nil
		},
		partitionNetFn: func(_ context.Context, id string) error {
			mu.Lock()
			seen["partition:"+id]++
			mu.Unlock()
			return nil
		},
		toolTimeoutFn: func(_ context.Context, id string, _ time.Duration) error {
			mu.Lock()
			seen["timeout:"+id]++
			mu.Unlock()
			return nil
		},
		corruptMemFn: func(_ context.Context, id string) error {
			mu.Lock()
			seen["corrupt:"+id]++
			mu.Unlock()
			return nil
		},
		listAgentsFn: func() []ares_runtime.AgentInfo {
			return []ares_runtime.AgentInfo{{ID: "agent-1", Type: "worker"}}
		},
	}
	svc := newTestService(rt, &mockDAG{}, nil)

	// Execute four faults concurrently on the same agent.
	var wg sync.WaitGroup
	actions := []Action{
		{ID: "a1", Type: ActionKillAgent, TargetID: "agent-1"},
		{ID: "a2", Type: ActionNetworkPartition, TargetID: "agent-1"},
		{ID: "a3", Type: ActionToolTimeout, TargetID: "agent-1", Metadata: map[string]any{"duration": "5s"}},
		{ID: "a4", Type: ActionMemoryCorrupt, TargetID: "agent-1"},
	}
	for _, a := range actions {
		wg.Add(1)
		go func(act Action) {
			defer wg.Done()
			result := svc.Execute(context.Background(), act)
			assert.True(t, result.Success)
		}(a)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, seen["stop:agent-1"])
	assert.Equal(t, 1, seen["partition:agent-1"])
	assert.Equal(t, 1, seen["timeout:agent-1"])
	assert.Equal(t, 1, seen["corrupt:agent-1"])
}

func TestRobustness_FaultCascade(t *testing.T) {
	t.Parallel()

	var killCount atomic.Int32
	rt := &mockRuntime{
		stopAgentFn: func(_ context.Context, id string) error {
			killCount.Add(1)
			return nil
		},
		listAgentsFn: func() []ares_runtime.AgentInfo {
			return []ares_runtime.AgentInfo{
				{ID: "leader-1", Type: "leader"},
				{ID: "worker-1", Type: "worker"},
				{ID: "worker-2", Type: "worker"},
			}
		},
	}
	dag := &mockDAG{}
	svc := newTestService(rt, dag, nil)

	// Kill leader, then immediately kill an agent, then remove a node.
	actions := []Action{
		{ID: "c1", Type: ActionKillLeader},
		{ID: "c2", Type: ActionKillAgent, TargetID: "worker-1"},
		{ID: "c3", Type: ActionRemoveNode, TargetID: "worker-2"},
	}
	for _, a := range actions {
		result := svc.Execute(context.Background(), a)
		assert.True(t, result.Success, "action %s failed: %s", a.ID, a.Type)
	}

	assert.Equal(t, int32(2), killCount.Load()) // leader + worker-1
	assert.ElementsMatch(t, []string{"worker-2"}, dag.removedNodes)
}

func TestRobustness_WorkflowRecovery(t *testing.T) {
	t.Parallel()

	var pauseOrder, resumeOrder []string
	var mu sync.Mutex
	rt := &mockRuntime{
		pauseAgentFn: func(_ context.Context, id string) error {
			mu.Lock()
			pauseOrder = append(pauseOrder, id)
			mu.Unlock()
			return nil
		},
		resumeAgentFn: func(_ context.Context, id string) error {
			mu.Lock()
			resumeOrder = append(resumeOrder, id)
			mu.Unlock()
			return nil
		},
		listAgentsFn: func() []ares_runtime.AgentInfo {
			return []ares_runtime.AgentInfo{{ID: "agent-1", Type: "worker"}}
		},
	}
	svc := newTestService(rt, &mockDAG{}, nil)

	// Pause then resume simulates a recovery cycle.
	assert.True(t, svc.Execute(context.Background(), Action{
		ID: "r1", Type: ActionPauseAgent, TargetID: "agent-1",
	}).Success)

	assert.True(t, svc.Execute(context.Background(), Action{
		ID: "r2", Type: ActionResumeAgent, TargetID: "agent-1",
	}).Success)

	assert.Equal(t, []string{"agent-1"}, pauseOrder)
	assert.Equal(t, []string{"agent-1"}, resumeOrder)
}

func TestRobustness_InjectorErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		action  Action
		wantErr string
	}{
		{
			name:    "ares_runtime returns error on kill",
			action:  Action{ID: "e1", Type: ActionKillAgent, TargetID: "agent-1"},
			wantErr: "mock fail",
		},
		{
			name:    "ares_runtime returns error on pause",
			action:  Action{ID: "e2", Type: ActionPauseAgent, TargetID: "agent-1"},
			wantErr: "mock fail",
		},
		{
			name:    "ares_runtime returns error on memory corrupt",
			action:  Action{ID: "e3", Type: ActionMemoryCorrupt, TargetID: "agent-1"},
			wantErr: "mock fail",
		},
		{
			name:    "ares_runtime returns error on LLM failure",
			action:  Action{ID: "e4", Type: ActionLLMFailure, TargetID: "agent-1"},
			wantErr: "mock fail",
		},
		{
			name:    "bad duration metadata",
			action:  Action{ID: "e5", Type: ActionToolTimeout, TargetID: "a1", Metadata: map[string]any{"duration": "not-a-duration"}},
			wantErr: "invalid duration",
		},
		{
			name:    "nil ares_runtime for injector",
			action:  Action{ID: "e6", Type: ActionKillAgent, TargetID: "agent-1"},
			wantErr: "ares_runtime is nil",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &mockRuntime{
				stopAgentFn:     func(_ context.Context, _ string) error { return fmt.Errorf("mock fail") },
				pauseAgentFn:    func(_ context.Context, _ string) error { return fmt.Errorf("mock fail") },
				corruptMemFn:    func(_ context.Context, _ string) error { return fmt.Errorf("mock fail") },
				injectLLMFailFn: func(_ context.Context, _ string, _ string) error { return fmt.Errorf("mock fail") },
			}
			if tt.name == "nil ares_runtime for injector" {
				svc := newTestService(nil, &mockDAG{}, nil)
				result := svc.Execute(context.Background(), tt.action)
				assert.False(t, result.Success)
				assert.Contains(t, result.Error, "ares_runtime is nil")
			} else {
				svc := newTestService(rt, &mockDAG{}, nil)
				result := svc.Execute(context.Background(), tt.action)
				assert.False(t, result.Success)
				assert.Contains(t, result.Error, tt.wantErr)
			}
		})
	}
}

func TestRobustness_LLMFailureRecovery(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var llmFails []struct{ id, errType string }
	rt := &mockRuntime{
		injectLLMFailFn: func(_ context.Context, id string, errType string) error {
			mu.Lock()
			llmFails = append(llmFails, struct{ id, errType string }{id, errType})
			mu.Unlock()
			return nil
		},
		listAgentsFn: func() []ares_runtime.AgentInfo {
			return []ares_runtime.AgentInfo{{ID: "agent-1", Type: "worker"}}
		},
	}
	svc := newTestService(rt, &mockDAG{}, nil)

	// Inject LLM failure with timeout error, then recover by injecting a success.
	action1 := Action{ID: "l1", Type: ActionLLMFailure, TargetID: "agent-1", Metadata: map[string]any{"error_type": "timeout"}}
	assert.True(t, svc.Execute(context.Background(), action1).Success)

	// Recover by re-injecting to clear the fault.
	action2 := Action{ID: "l2", Type: ActionLLMFailure, TargetID: "agent-1", Metadata: map[string]any{"error_type": "none"}}
	assert.True(t, svc.Execute(context.Background(), action2).Success)

	mu.Lock()
	require.Len(t, llmFails, 2)
	assert.Equal(t, "agent-1", llmFails[0].id)
	assert.Equal(t, "timeout", llmFails[0].errType)
	assert.Equal(t, "none", llmFails[1].errType)
	mu.Unlock()
}

func TestRobustness_MCPDisconnectAndReconnect(t *testing.T) {
	t.Parallel()

	var disconnects int32
	rt := &mockRuntime{
		disconnectMCPFn: func(_ context.Context, id string) error {
			atomic.AddInt32(&disconnects, 1)
			return nil
		},
		listAgentsFn: func() []ares_runtime.AgentInfo {
			return []ares_runtime.AgentInfo{{ID: "agent-1", Type: "worker"}}
		},
	}
	// Use a second mock to simulate reconnection (just call Execute again).
	dag := &mockDAG{}
	svc := newTestService(rt, dag, nil)

	// Disconnect MCP.
	assert.True(t, svc.Execute(context.Background(), Action{
		ID: "m1", Type: ActionMCPDisconnect, TargetID: "agent-1",
	}).Success)

	// "Reconnect" is not a built-in action, but we can verify the DAG is intact
	// by removing a node after disconnect to simulate topology change.
	assert.True(t, svc.Execute(context.Background(), Action{
		ID: "m2", Type: ActionRemoveNode, TargetID: "agent-1",
	}).Success)

	assert.Equal(t, int32(1), atomic.LoadInt32(&disconnects))
	assert.ElementsMatch(t, []string{"agent-1"}, dag.removedNodes)
}

func TestRobustness_ContextCancellation(t *testing.T) {
	t.Parallel()

	rt := &mockRuntime{
		stopAgentFn: func(ctx context.Context, _ string) error {
			<-ctx.Done()
			return ctx.Err()
		},
		listAgentsFn: func() []ares_runtime.AgentInfo {
			return []ares_runtime.AgentInfo{{ID: "agent-1", Type: "worker"}}
		},
	}
	svc := newTestService(rt, &mockDAG{}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := svc.Execute(ctx, Action{
		ID: "cc1", Type: ActionKillAgent, TargetID: "agent-1",
	})
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "context canceled")
}

func TestRobustness_ConcurrentMetricsConsistency(t *testing.T) {
	t.Parallel()

	// Verify that MetricsCollector remains consistent under concurrent
	// fault injection and metric snapshotting.
	rt := &mockRuntime{listAgentsFn: func() []ares_runtime.AgentInfo { return nil }}
	svc := newTestService(rt, &mockDAG{}, nil)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			svc.Execute(context.Background(), Action{
				ID:       fmt.Sprintf("m-%d", n),
				Type:     ActionKillAgent,
				TargetID: "ghost-agent",
			})
		}(i)
	}

	// Concurrently read metrics.
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc.Metrics()
		}()
	}
	wg.Wait()

	metrics := svc.Metrics()
	assert.NotNil(t, metrics)
}

func TestRobustness_LongScenarioPartialFailure(t *testing.T) {
	t.Parallel()

	// A long scenario where some actions fail but the scenario continues.
	var mu sync.Mutex
	shouldFail := false
	rt := &mockRuntime{
		stopAgentFn: func(_ context.Context, id string) error {
			mu.Lock()
			fail := shouldFail
			mu.Unlock()
			if fail {
				return assert.AnError
			}
			return nil
		},
		listAgentsFn: func() []ares_runtime.AgentInfo {
			return []ares_runtime.AgentInfo{
				{ID: "agent-1", Type: "worker"},
				{ID: "agent-2", Type: "worker"},
				{ID: "agent-3", Type: "worker"},
			}
		},
	}
	svc := newTestService(rt, &mockDAG{}, nil)

	results := make([]Result, 3)
	actions := []Action{
		{ID: "s1", Type: ActionKillAgent, TargetID: "agent-1"},
		{ID: "s2", Type: ActionKillAgent, TargetID: "agent-2"},
		{ID: "s3", Type: ActionKillAgent, TargetID: "agent-3"},
	}

	for i, a := range actions {
		if i == 1 {
			mu.Lock()
			shouldFail = true
			mu.Unlock()
		}
		results[i] = svc.Execute(context.Background(), a)
		if i == 1 {
			mu.Lock()
			shouldFail = false
			mu.Unlock()
		}
	}

	assert.True(t, results[0].Success)
	assert.False(t, results[1].Success)
	assert.True(t, results[2].Success)
}
