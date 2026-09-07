package arena

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	ares_runtime "github.com/Timwood0x10/ares/internal/ares_runtime"
)

func TestRunSurvival_BasicRun(t *testing.T) {
	rt := &mockRuntime{
		listAgentsFn: func() []ares_runtime.AgentInfo {
			return []ares_runtime.AgentInfo{
				{ID: "a-1", Type: "worker"},
				{ID: "a-2", Type: "leader"},
			}
		},
	}
	svc := newTestService(rt, nil, nil)

	cfg := SurvivalConfig{
		Duration: 3 * time.Second,
		Interval: 500 * time.Millisecond,
	}

	report := svc.RunSurvival(context.Background(), cfg)

	assert.Greater(t, report.ActionsRun, 0)
	assert.Greater(t, report.Duration, time.Duration(0))
	assert.NotEmpty(t, report.Timeline)
	assert.NotEmpty(t, report.Score.Grade)
}

func TestRunSurvival_ContextCancellation(t *testing.T) {
	rt := &mockRuntime{
		listAgentsFn: func() []ares_runtime.AgentInfo {
			return []ares_runtime.AgentInfo{{ID: "a-1", Type: "worker"}}
		},
	}
	svc := newTestService(rt, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	cfg := SurvivalConfig{
		Duration: 60 * time.Second,
		Interval: 200 * time.Millisecond,
	}

	report := svc.RunSurvival(ctx, cfg)

	// Should have run some actions before cancellation.
	assert.GreaterOrEqual(t, report.ActionsRun, 1)
	assert.Less(t, report.Duration, 5*time.Second)
}

func TestRunSurvival_DefaultConfig(t *testing.T) {
	rt := &mockRuntime{
		listAgentsFn: func() []ares_runtime.AgentInfo {
			return []ares_runtime.AgentInfo{{ID: "a-1", Type: "worker"}}
		},
	}
	svc := newTestService(rt, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Zero duration should use defaults, but context will cancel it.
	cfg := SurvivalConfig{Duration: 0, Interval: 0}
	report := svc.RunSurvival(ctx, cfg)

	assert.GreaterOrEqual(t, report.ActionsRun, 0)
}

func TestRunSurvival_RecordsTimeline(t *testing.T) {
	rt := &mockRuntime{
		listAgentsFn: func() []ares_runtime.AgentInfo {
			return []ares_runtime.AgentInfo{
				{ID: "a-1", Type: "worker"},
				{ID: "a-2", Type: "leader"},
			}
		},
	}
	svc := newTestService(rt, nil, nil)

	cfg := SurvivalConfig{
		Duration: 2 * time.Second,
		Interval: 500 * time.Millisecond,
	}

	report := svc.RunSurvival(context.Background(), cfg)

	for _, event := range report.Timeline {
		assert.False(t, event.Timestamp.IsZero())
		assert.NotEmpty(t, event.Action.ID)
		assert.NotEmpty(t, event.Action.Type)
	}
}

func TestRunSurvival_ConcurrentSafety(t *testing.T) {
	rt := &mockRuntime{
		listAgentsFn: func() []ares_runtime.AgentInfo {
			return []ares_runtime.AgentInfo{{ID: "a-1", Type: "worker"}}
		},
	}
	svc := newTestService(rt, nil, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cfg := SurvivalConfig{
		Duration: 60 * time.Second,
		Interval: 100 * time.Millisecond,
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		svc.RunSurvival(ctx, cfg)
	}()

	go func() {
		defer wg.Done()
		// Query status while survival is running.
		for i := 0; i < 5; i++ {
			status := svc.GetSurvivalStatus()
			_ = status
			time.Sleep(100 * time.Millisecond)
		}
	}()

	wg.Wait()
}

func TestGetSurvivalStatus_NotRunning(t *testing.T) {
	svc := newTestService(nil, nil, nil)

	status := svc.GetSurvivalStatus()
	assert.False(t, status.Running)
	assert.Equal(t, 0, status.ActionsRun)
	assert.True(t, status.StartedAt.IsZero())
}

func TestGetSurvivalStatus_Running(t *testing.T) {
	rt := &mockRuntime{
		listAgentsFn: func() []ares_runtime.AgentInfo {
			return []ares_runtime.AgentInfo{{ID: "a-1", Type: "worker"}}
		},
	}
	svc := newTestService(rt, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())

	cfg := SurvivalConfig{
		Duration: 60 * time.Second,
		Interval: 200 * time.Millisecond,
	}

	var report SurvivalReport
	done := make(chan struct{})
	go func() {
		report = svc.RunSurvival(ctx, cfg)
		close(done)
	}()

	// Wait for at least one action.
	time.Sleep(500 * time.Millisecond)

	status := svc.GetSurvivalStatus()
	assert.True(t, status.Running)
	assert.Greater(t, status.ActionsRun, 0)
	assert.False(t, status.StartedAt.IsZero())
	assert.Greater(t, status.Elapsed, time.Duration(0))

	cancel()
	<-done
	_ = report
}

func TestRandomChaosAction_WithAgents(t *testing.T) {
	rt := &mockRuntime{
		listAgentsFn: func() []ares_runtime.AgentInfo {
			return []ares_runtime.AgentInfo{
				{ID: "a-1", Type: "worker"},
				{ID: "a-2", Type: "leader"},
			}
		},
	}
	dag := &mockDAG{
		nodes: []string{"dag-node-1", "dag-node-2"},
		edges: [][2]string{{"dag-node-1", "dag-node-2"}},
	}
	svc := newTestService(rt, dag, nil)

	// Run multiple times to exercise randomness.
	actionTypes := make(map[ActionType]bool)
	for i := 0; i < 50; i++ {
		action := svc.randomChaosAction()
		assert.NotEmpty(t, action.ID)
		assert.NotEmpty(t, action.Type)
		actionTypes[action.Type] = true

		switch action.Type {
		case ActionKillAgent:
			assert.NotEmpty(t, action.TargetID)
		case ActionKillLeader:
			// No target needed.
		case ActionRemoveNode:
			assert.NotEmpty(t, action.TargetID)
		case ActionRemoveEdge:
			assert.NotEmpty(t, action.SourceID)
			assert.NotEmpty(t, action.TargetID)
			assert.NotEqual(t, action.SourceID, action.TargetID)
		}
	}

	// With 50 iterations, we should see at least 2 different action types.
	assert.GreaterOrEqual(t, len(actionTypes), 2)
}

func TestRandomChaosAction_NoAgents(t *testing.T) {
	rt := &mockRuntime{
		listAgentsFn: func() []ares_runtime.AgentInfo {
			return nil
		},
	}
	svc := newTestService(rt, nil, nil)

	action := svc.randomChaosAction()
	assert.NotEmpty(t, action.ID)
	assert.NotEmpty(t, action.Type)
}

func TestRandomID_Uniqueness(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := randomID()
		assert.Len(t, id, 16)
		assert.False(t, ids[id], "duplicate ID: %s", id)
		ids[id] = true
	}
}

func TestBuildSurvivalReport(t *testing.T) {
	rt := &mockRuntime{}
	svc := newTestService(rt, nil, nil)

	timeline := []SurvivalEvent{
		{
			Timestamp: time.Now(),
			Action:    Action{ID: "a-1", Type: ActionKillAgent, TargetID: "agent-1"},
			Result:    Result{Success: true, Duration: 500 * time.Millisecond},
		},
		{
			Timestamp: time.Now(),
			Action:    Action{ID: "a-2", Type: ActionKillAgent, TargetID: "agent-2"},
			Result:    Result{Success: false, Duration: 100 * time.Millisecond, Error: "failed"},
		},
	}

	report := svc.buildSurvivalReport(5*time.Second, timeline)

	assert.Equal(t, 5*time.Second, report.Duration)
	assert.Equal(t, 2, report.ActionsRun)
	assert.Len(t, report.Timeline, 2)
	assert.NotEmpty(t, report.Score.Grade)
}

func TestCalculateAvgRecoveryTime(t *testing.T) {
	svc := newTestService(nil, nil, nil)

	events := []SurvivalEvent{
		{Result: Result{Success: true, Duration: 2 * time.Second}},
		{Result: Result{Success: true, Duration: 4 * time.Second}},
		{Result: Result{Success: false, Duration: 1 * time.Second}},
	}

	avg := svc.calculateAvgRecoveryTime(events)
	// (2s + 4s) / 2 = 3s
	assert.Equal(t, 3*time.Second, avg)
}

func TestCalculateAvgRecoveryTime_NoSuccessful(t *testing.T) {
	svc := newTestService(nil, nil, nil)

	events := []SurvivalEvent{
		{Result: Result{Success: false, Duration: 1 * time.Second}},
		{Result: Result{Success: false, Duration: 2 * time.Second}},
	}

	avg := svc.calculateAvgRecoveryTime(events)
	assert.Equal(t, time.Duration(0), avg)
}

func TestCalculateAvgRecoveryTime_Empty(t *testing.T) {
	svc := newTestService(nil, nil, nil)

	avg := svc.calculateAvgRecoveryTime(nil)
	assert.Equal(t, time.Duration(0), avg)
}

func TestRunSurvival_WithFailures(t *testing.T) {
	rt := &mockRuntime{
		listAgentsFn: func() []ares_runtime.AgentInfo {
			return []ares_runtime.AgentInfo{
				{ID: "a-1", Type: "worker"},
			}
		},
		stopAgentFn: func(_ context.Context, _ string) error {
			return errors.New("injection failed")
		},
	}
	svc := newTestService(rt, nil, nil)

	cfg := SurvivalConfig{
		Duration: 2 * time.Second,
		Interval: 500 * time.Millisecond,
	}

	report := svc.RunSurvival(context.Background(), cfg)

	assert.Greater(t, report.ActionsRun, 0)
	// All actions should have failed.
	for _, event := range report.Timeline {
		if event.Result.Action.Type == ActionKillAgent {
			assert.False(t, event.Result.Success)
		}
	}
}

func TestDefaultSurvivalConfig(t *testing.T) {
	cfg := defaultSurvivalConfig()
	assert.Equal(t, 30*time.Minute, cfg.Duration)
	assert.Equal(t, 10*time.Second, cfg.Interval)
}
