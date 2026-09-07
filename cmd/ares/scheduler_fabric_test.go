package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/fabric/agent"
	"github.com/Timwood0x10/ares/internal/fabric/task"
)

// recordingCognition is an agentfabric.Cognition that completes every task in
// one quantum and records the executed task ids (真实执行体).
type recordingCognition struct {
	mu       sync.Mutex
	executed []string
}

var _ agentfabric.Cognition = (*recordingCognition)(nil)

func (c *recordingCognition) ExecuteStep(_ context.Context, task *models.Task) (*agentfabric.StepOutcome, error) {
	c.mu.Lock()
	c.executed = append(c.executed, task.TaskID)
	c.mu.Unlock()
	res := models.NewTaskResult(task.TaskID, task.AgentType)
	res.SetSuccess(nil, "run by fabric agent")
	return &agentfabric.StepOutcome{Done: true, Result: res}, nil
}

func (c *recordingCognition) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.executed)
}

// waitTaskState polls the fabric until the task reaches one of the given
// terminal states or the timeout elapses. It returns the task's current state.
func waitTaskState(t *testing.T, f *taskfabric.Fabric, taskID string, timeout time.Duration) taskfabric.TaskState {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		tk, err := f.Task(taskID)
		if err == nil {
			if tk.State == taskfabric.StateCompleted || tk.State == taskfabric.StateFailed {
				return tk.State
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	tk, err := f.Task(taskID)
	if err != nil {
		t.Fatalf("Task(%q): %v", taskID, err)
	}
	return tk.State
}

// TestKernelSchedulerSchedulesFabricAgents is the acceptance test
// for the single-source rule: a fabric agent spawned WITH a CognitionFactory is
// immediately a schedulable candidate — the scheduler executes the task
// through the agent's injected Cognition, not a phantom. Killing the agent
// removes it from the candidate pool: a new task requiring the same
// capability is no longer executed.
func TestKernelSchedulerSchedulesFabricAgents(t *testing.T) {
	f := taskfabric.NewFabric()
	fab := agentfabric.NewFabric()
	// No static executors: the candidate pool is the fabric alone.
	sched := NewKernelScheduler(f, nil, nil)
	sched.WithAgentFabric(fab)
	sched.PollInterval = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(ctx)

	cog := &recordingCognition{}
	if _, err := fab.Spawn(ctx, agentfabric.SpawnSpec{
		Identity:     "fab-code",
		Capabilities: []string{"code"},
		CognitionFactory: func([]string) agentfabric.Cognition {
			return cog
		},
	}); err != nil {
		t.Fatalf("spawn fabric agent: %v", err)
	}

	// The spawned agent must be schedulable immediately (no manual executor
	// registration): a task requiring "code" is executed by the fabric agent.
	if err := f.Create(&taskfabric.Task{
		ID:          "t-fab-1",
		Capability:  "code",
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 1},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if state := waitTaskState(t, f, "t-fab-1", 3*time.Second); state != taskfabric.StateCompleted {
		t.Fatalf("task t-fab-1 must be executed by the fabric agent, got %s", state)
	}
	if cog.count() != 1 {
		t.Fatalf("fabric agent cognition must run exactly one quantum, got %d", cog.count())
	}

	// Kill the agent: it must disappear from the candidate pool. A new task
	// requiring the same capability stays unexecuted (no capable candidate).
	if err := fab.Kill(ctx, "fab-code"); err != nil {
		t.Fatalf("kill fabric agent: %v", err)
	}
	if err := f.Create(&taskfabric.Task{
		ID:          "t-fab-2",
		Capability:  "code",
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 1},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The task must remain READY (never COMPLETED/FAILED) — nothing can run it.
	state := waitTaskState(t, f, "t-fab-2", 400*time.Millisecond)
	if state == taskfabric.StateCompleted {
		t.Fatal("killed fabric agent must not be selected for a new task")
	}
	if cog.count() != 1 {
		t.Fatalf("killed agent must not run more quanta, got %d", cog.count())
	}
}

// TestKernelSchedulerFabricKillBeatsStaticRegistration verifies the single-
// source rule (scheduler 只认 fabric 动态群体): when
// the fabric is wired, killing the fabric agent removes it from the candidate
// pool EVEN IF the same id is still statically registered — the static copy is
// managed through the fabric, so a killed agent is never resurrected via the
// stale registration.
func TestKernelSchedulerFabricKillBeatsStaticRegistration(t *testing.T) {
	f := taskfabric.NewFabric()
	fab := agentfabric.NewFabric()
	// Static registration of the same id (legacy peer wiring): the sub.Agent
	// copy is also managed as a fabric agent.
	static := &w2StubAgent{id: "fab-code", typ: models.AgentType("code")}
	executors := map[string]CapabilityExecutor{"fab-code": static}
	tracker := newLoadTracker()
	sched := NewKernelScheduler(f, executors, tracker)
	sched.WithAgentFabric(fab)
	sched.PollInterval = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(ctx)

	cog := &recordingCognition{}
	if _, err := fab.Spawn(ctx, agentfabric.SpawnSpec{
		Identity:     "fab-code",
		Capabilities: []string{"code"},
		CognitionFactory: func([]string) agentfabric.Cognition {
			return cog
		},
	}); err != nil {
		t.Fatalf("spawn: %v", err)
	}

	// The fabric agent executes the first task.
	if err := f.Create(&taskfabric.Task{
		ID:          "t-c1-static-1",
		Capability:  "code",
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 1},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if state := waitTaskState(t, f, "t-c1-static-1", 3*time.Second); state != taskfabric.StateCompleted {
		t.Fatalf("task must be executed before the kill, got %s", state)
	}
	if cog.count() != 1 {
		t.Fatalf("fabric cognition must run one quantum, got %d", cog.count())
	}

	// Kill the fabric agent. The static registration must NOT resurrect it:
	// a new task requiring "code" stays unexecuted.
	if err := fab.Kill(ctx, "fab-code"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if err := f.Create(&taskfabric.Task{
		ID:          "t-c1-static-2",
		Capability:  "code",
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 1},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	state := waitTaskState(t, f, "t-c1-static-2", 400*time.Millisecond)
	if state == taskfabric.StateCompleted {
		t.Fatal("killed fabric agent must not be resurrected via its stale static registration")
	}
}

// TestKernelSchedulerSkipsNonExecutableFabricAgents verifies the executa­bility contract at
// the scheduler boundary: an agent spawned WITHOUT a CognitionFactory is
// managed but NOT schedulable — the scheduler never offers it as a candidate,
// so a task requiring its capability is never executed.
func TestKernelSchedulerSkipsNonExecutableFabricAgents(t *testing.T) {
	f := taskfabric.NewFabric()
	fab := agentfabric.NewFabric()
	sched := NewKernelScheduler(f, nil, nil)
	sched.WithAgentFabric(fab)
	sched.PollInterval = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(ctx)

	// No CognitionFactory: managed but not executable.
	if _, err := fab.Spawn(ctx, agentfabric.SpawnSpec{
		Identity:     "fab-shell",
		Capabilities: []string{"audit"},
	}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if err := f.Create(&taskfabric.Task{
		ID:          "t-fab-3",
		Capability:  "audit",
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 1},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	state := waitTaskState(t, f, "t-fab-3", 400*time.Millisecond)
	if state == taskfabric.StateCompleted {
		t.Fatal("non-executable fabric agent must not be scheduled")
	}
}

// TestKernelSchedulerFabricAgentMatchesAnyCapability verifies that candidate
// scoring uses the agent's FULL declared capability set — a task matching the
// second capability is still scheduled to the fabric agent.
func TestKernelSchedulerFabricAgentMatchesAnyCapability(t *testing.T) {
	f := taskfabric.NewFabric()
	fab := agentfabric.NewFabric()
	sched := NewKernelScheduler(f, nil, nil)
	sched.WithAgentFabric(fab)
	sched.PollInterval = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(ctx)

	cog := &recordingCognition{}
	if _, err := fab.Spawn(ctx, agentfabric.SpawnSpec{
		Identity:     "fab-multi",
		Capabilities: []string{"code", "ffi-safety"},
		CognitionFactory: func([]string) agentfabric.Cognition {
			return cog
		},
	}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if err := f.Create(&taskfabric.Task{
		ID:          "t-fab-4",
		Capability:  "ffi-safety",
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 1},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if state := waitTaskState(t, f, "t-fab-4", 3*time.Second); state != taskfabric.StateCompleted {
		t.Fatalf("task matching the second capability must be executed, got %s", state)
	}
	if cog.count() != 1 {
		t.Fatalf("fabric agent cognition must run one quantum, got %d", cog.count())
	}
}
