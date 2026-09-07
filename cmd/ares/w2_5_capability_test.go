package main

import (
	"context"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/agents/sub"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/fabric/task"
)

// minimalCapabilityExecutor is a bare-bones executor that implements ONLY the
// CapabilityExecutor interface (ID, Type, ExecuteStep) — it does NOT implement
// the full sub.Agent interface. This proves the scheduler is decoupled from
// sub.Agent: any type with the three methods is schedulable.
type minimalCapabilityExecutor struct {
	id  string
	typ models.AgentType
}

func (e *minimalCapabilityExecutor) ID() string             { return e.id }
func (e *minimalCapabilityExecutor) Type() models.AgentType { return e.typ }
func (e *minimalCapabilityExecutor) ExecuteStep(_ context.Context, task *models.Task) (*sub.StepOutcome, error) {
	res := models.NewTaskResult(task.TaskID, task.AgentType)
	res.SetSuccess(nil, "executed by minimal executor")
	return &sub.StepOutcome{Done: true, Result: res}, nil
}

// Compile-time check: minimalCapabilityExecutor satisfies CapabilityExecutor
// but NOT sub.Agent (it lacks Execute, Start, Stop, Process, ProcessStream,
// Status). This is the decoupling proof: the scheduler accepts executors that are
// not sub.Agent.
var _ CapabilityExecutor = (*minimalCapabilityExecutor)(nil)

// TestW2_5CapabilityExecutorDecoupling verifies the scheduler accepts an
// executor that is NOT a sub.Agent — only a CapabilityExecutor. This is the
// decoupling acceptance test: "移除 scheduler 对 sub.Agent 类型/角色的强绑定".
// The minimalCapabilityExecutor implements only ID/Type/ExecuteStep; it has
// no Execute, Start, Stop, Process, or ProcessStream methods. If the scheduler
// still depended on sub.Agent, this would not compile.
func TestW2_5CapabilityExecutorDecoupling(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fabric := taskfabric.NewFabric()
	executor := &minimalCapabilityExecutor{id: "mini-1", typ: models.AgentType("code")}
	executors := map[string]CapabilityExecutor{"mini-1": executor}

	sched := NewKernelScheduler(fabric, executors, nil)
	sched.PollInterval = 20 * time.Millisecond
	go sched.Run(ctx)

	// Create a task — the scheduler must schedule it to the minimal executor
	// even though it is NOT a sub.Agent.
	if err := fabric.Create(&taskfabric.Task{
		ID:          "w2-5-decoupling",
		Capability:  "code",
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 1},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Poll for completion.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tk, err := fabric.Task("w2-5-decoupling")
		if err == nil && tk.State == taskfabric.StateCompleted {
			t.Logf("W2-5 PASS: minimal CapabilityExecutor (not sub.Agent) scheduled and completed task")
			return
		}
		time.Sleep(20 * time.Millisecond)
	}

	tk, _ := fabric.Task("w2-5-decoupling")
	if tk == nil || tk.State != taskfabric.StateCompleted {
		t.Fatalf("task must be COMPLETED by the minimal CapabilityExecutor, got state=%v", tk)
	}
}

// TestW2_5SubAgentSatisfiesCapabilityExecutor verifies that sub.Agent (the
// production executor type) still satisfies CapabilityExecutor — backward
// compatibility. Every existing sub.Agent is automatically a CapabilityExecutor.
func TestW2_5SubAgentSatisfiesCapabilityExecutor(t *testing.T) {
	// Compile-time: the stubAgent type (declared in scheduler_test.go)
	// implements sub.Agent. If it also satisfies CapabilityExecutor, then
	// any sub.Agent is a CapabilityExecutor. This is the backward-compat
	// proof: the scheduler accepts both sub.Agent and bare CapabilityExecutor.
	var _ CapabilityExecutor = (*stubAgent)(nil)
	t.Log("W2-5 PASS: sub.Agent satisfies CapabilityExecutor (backward compatible)")
}
