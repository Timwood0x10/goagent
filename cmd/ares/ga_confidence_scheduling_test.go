package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/agents/sub"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/fabric/task"
)

// countingExecutor is a same-capability executor whose execution is counted.
// Two of them differ only in their scheduler-visible confidence, so the test
// can observe WHICH one the scheduler picks.
type countingExecutor struct {
	id       string
	typ      models.AgentType
	executed atomic.Int64
}

func (e *countingExecutor) ID() string { return e.id }
func (e *countingExecutor) Type() models.AgentType {
	return e.typ
}
func (e *countingExecutor) ExecuteStep(_ context.Context, task *models.Task) (*sub.StepOutcome, error) {
	e.executed.Add(1)
	res := models.NewTaskResult(task.TaskID, task.AgentType)
	res.SetSuccess(nil, "done by "+e.id)
	return &sub.StepOutcome{Done: true, Result: res}, nil
}

// TestGAConfidenceChangesNextSchedule is the acceptance
// (证明 GA 进化结果确实改变了下一轮的调度选择 — 目前
// 只有 spawn 可执行体的测试，没有调度行为变化的测试). Two executors with the
// SAME capability differ only in their scheduler-visible confidence (the
// feedback write path: EvolutionFeedbackAdapter → SetAgentConfidence). A task that
// matches the capability must be scheduled to the HIGH-confidence executor,
// and a confidence override that swaps the ranking must swap the selection —
// the GA's derived confidence directly drives the next Schedule.
func TestGAConfidenceChangesNextSchedule(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fabric := taskfabric.NewFabric()
	a := &countingExecutor{id: "agent-A", typ: models.AgentType("code")}
	b := &countingExecutor{id: "agent-B", typ: models.AgentType("code")}
	tracker := newLoadTracker()
	execs := map[string]CapabilityExecutor{"agent-A": a, "agent-B": b}

	sched := NewKernelScheduler(fabric, execs, tracker)
	sched.PollInterval = 10 * time.Millisecond
	go sched.Run(ctx)

	submit := func(id string) taskfabric.TaskState {
		if err := fabric.Create(&taskfabric.Task{
			ID:          id,
			Capability:  "code",
			RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 1},
		}); err != nil {
			t.Fatalf("Create(%s): %v", id, err)
		}
		state := waitFabricState(t, fabric, id, taskfabric.StateCompleted, 3*time.Second)
		if state != taskfabric.StateCompleted {
			t.Fatalf("task %s must complete, got %s", id, state)
		}
		return state
	}

	// Round 1: equal confidence → both executors are candidates; the task
	// completes (whoever wins is fine — the point is the next round).
	submit("t-f1-1")
	if a.executed.Load()+b.executed.Load() != 1 {
		t.Fatalf("exactly one executor must run per task, got A=%d B=%d", a.executed.Load(), b.executed.Load())
	}

	// GA 反馈回写：agent-A 的成功率评估为 0（常失败），agent-B 为 1。
	// SetAgentConfidence 是 EvolutionFeedbackAdapter 调用的同一接口。
	tracker.SetAgentConfidence("agent-A", 0.0)
	tracker.SetAgentConfidence("agent-B", 1.0)

	// Round 2: the high-confidence executor must win — the GA result CHANGED
	// the next scheduling choice.
	a.executed.Store(0)
	b.executed.Store(0)
	submit("t-f1-2")
	if b.executed.Load() != 1 {
		t.Fatalf("high-confidence agent-B must be selected after the GA override, got A=%d B=%d", a.executed.Load(), b.executed.Load())
	}

	// Round 3: a later GA evaluation flips the ranking — agent-A now wins.
	tracker.SetAgentConfidence("agent-A", 0.9)
	tracker.SetAgentConfidence("agent-B", 0.1)
	a.executed.Store(0)
	b.executed.Store(0)
	submit("t-f1-3")
	if a.executed.Load() != 1 {
		t.Fatalf("agent-A must win after the GA override flips, got A=%d B=%d", a.executed.Load(), b.executed.Load())
	}
}
