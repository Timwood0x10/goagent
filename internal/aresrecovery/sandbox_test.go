package aresrecovery

import (
	"context"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/fabric/agent"
	"github.com/Timwood0x10/ares/internal/fabric/task"
)

// newTestSandbox builds a scratch sandbox with its own fabrics + recovery.
func newTestSandbox(t *testing.T) *Sandbox {
	t.Helper()
	tasks := taskfabric.NewFabric()
	agents := agentfabric.NewFabric()
	rec := New(tasks, agents, DefaultRestartPolicy())
	return NewSandbox(tasks, agents, rec)
}

// TestSandboxReplayRecoveryChain verifies the scripted chain
// create → acquire → kill → lease.expire → recover.all leaves the task
// recoverable (Agent death ≠ Task death).
func TestSandboxReplayRecoveryChain(t *testing.T) {
	sb := newTestSandbox(t)
	ctx := context.Background()

	outcomes, err := sb.Replay(ctx, []SandboxEvent{
		{Type: SandboxEventTaskCreate, TaskID: "t1"},
		{Type: SandboxEventAgentSpawn, AgentID: "a1"},
		{Type: SandboxEventTaskAcquire, TaskID: "t1", AgentID: "a1"},
		{Type: SandboxEventAgentKill, AgentID: "a1"},
		{Type: SandboxEventLeaseExpire, TaskID: "t1"},
		{Type: SandboxEventRecoverAll, TaskID: "t1"},
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(outcomes) != 6 {
		t.Fatalf("want 6 outcomes, got %d", len(outcomes))
	}
	// After the kill + lease expiry, the recovery chain must have requeued the
	// task away from its dead owner's RUNNING/LEASED state.
	last := outcomes[len(outcomes)-1]
	if last.TaskState == "" {
		t.Fatal("task must exist after recovery")
	}
	if recovered := last.Detail["recovered"]; recovered == nil || recovered.(int) < 1 {
		t.Fatalf("recovery must requeue the task, detail=%v", last.Detail)
	}
}

// TestSandboxReplayUnknownEvent verifies an unknown scripted event is
// rejected instead of silently skipped.
func TestSandboxReplayUnknownEvent(t *testing.T) {
	sb := newTestSandbox(t)
	_, err := sb.Replay(context.Background(), []SandboxEvent{{Type: "nope", TaskID: "t1"}})
	if err == nil {
		t.Fatal("unknown event must error")
	}
}

// TestSandboxSimulateAgentDeath verifies the offline failure prediction: a
// killed agent's task survives via lease expiry + recovery, ending in a
// recoverable state.
func TestSandboxSimulateAgentDeath(t *testing.T) {
	sb := newTestSandbox(t)
	ctx := context.Background()
	if err := sb.tasks.Create(&taskfabric.Task{ID: "t1", Capability: sandboxCapability}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := sb.agents.Spawn(ctx, agentfabric.SpawnSpec{Identity: "a1", Capabilities: []string{sandboxCapability}}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if _, err := sb.tasks.Acquire("t1", "a1", time.Minute); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	res, err := sb.Simulate(ctx, "a1", "t1")
	if err != nil {
		t.Fatalf("simulate: %v", err)
	}
	if !res.Recovered {
		t.Fatal("recovery must requeue the dead agent's task")
	}
	if res.FinalTaskState == "" {
		t.Fatal("task must survive the agent's death")
	}
	if res.FinalTaskState != string(taskfabric.StateReady) &&
		res.FinalTaskState != string(taskfabric.StateLeased) &&
		res.FinalTaskState != string(taskfabric.StateRunning) {
		t.Fatalf("task ended in unexpected state %q", res.FinalTaskState)
	}
}

// TestSandboxClockDrivesLeaseExpiry verifies the injected clock makes lease
// expiry deterministic (no real sleeps).
func TestSandboxClockDrivesLeaseExpiry(t *testing.T) {
	base := time.Now()
	sb := newTestSandbox(t)
	sb.WithClock(func() time.Time { return base })
	ctx := context.Background()
	if err := sb.tasks.Create(&taskfabric.Task{ID: "t1", Capability: sandboxCapability}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := sb.agents.Spawn(ctx, agentfabric.SpawnSpec{Identity: "a1", Capabilities: []string{sandboxCapability}}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if _, err := sb.tasks.Acquire("t1", "a1", time.Minute); err != nil {
		t.Fatalf("acquire: %v", err)
	}

	// With the frozen clock the lease is still valid → nothing requeued.
	requeued := sb.recovery.RequeueExpiredLeases()
	if len(requeued) != 0 {
		t.Fatalf("with a frozen clock nothing must expire, got %v", requeued)
	}
}
