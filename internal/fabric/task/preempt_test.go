package taskfabric

import (
	"testing"
	"time"
)

// TestFabricPreemptCooperative verifies cooperative preemption: a RUNNING
// task returns to READY with its checkpoint preserved, and another agent can
// acquire and resume it (architecture invariant #9 — no hard interrupt).
func TestFabricPreemptCooperative(t *testing.T) {
	f := NewFabric()
	if err := f.Create(newTask("low")); err != nil {
		t.Fatalf("Create low: %v", err)
	}
	epoch, err := f.Acquire("low", "agent-a", time.Minute)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := f.Start("low", "agent-a", epoch); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// A high-priority task arrived; the Scheduler decided to preempt "low".
	if err := f.Preempt("low", "agent-a", epoch, "high-priority task arrived"); err != nil {
		t.Fatalf("Preempt: %v", err)
	}
	task, _ := f.Task("low")
	if task.State != StateReady || task.Owner != "" {
		t.Fatalf("want READY and unowned, got state=%s owner=%q", task.State, task.Owner)
	}
	// Another agent acquires the preempted task.
	if _, err := f.Acquire("low", "agent-b", time.Minute); err != nil {
		t.Fatalf("agent-b must acquire after preempt: %v", err)
	}
	// Event recorded.
	found := false
	for _, ev := range f.Events() {
		if ev.Type == EventTaskPreempted && ev.TaskID == "low" {
			found = true
		}
	}
	if !found {
		t.Fatal("want TaskPreempted event")
	}
}

// TestFabricPreemptPreservesCheckpoint verifies the checkpoint survives
// preemption: after a yield (checkpoint) and a later preempt, the checkpoint
// is still there for the next acquirer.
func TestFabricPreemptPreservesCheckpoint(t *testing.T) {
	f := NewFabric()
	if err := f.Create(newTask("t1")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	epoch, err := f.Acquire("t1", "agent-a", time.Minute)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := f.Start("t1", "agent-a", epoch); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := f.Yield("t1", "agent-a", epoch, map[string]any{"step": 4}); err != nil {
		t.Fatalf("Yield: %v", err)
	}
	// Resume: re-acquire, start, then get preempted.
	e2, err := f.Acquire("t1", "agent-a", time.Minute)
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	if err := f.Start("t1", "agent-a", e2); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := f.Preempt("t1", "agent-a", e2, "budget"); err != nil {
		t.Fatalf("Preempt: %v", err)
	}
	task, _ := f.Task("t1")
	kept, ok := task.Checkpoint.(map[string]any)
	if !ok || kept["step"] != 4 {
		t.Fatalf("checkpoint must survive preemption, got %+v", task.Checkpoint)
	}
}

// TestFabricPreemptFencing verifies only the lease holder at the current
// epoch can preempt — a stale holder or non-owner is rejected.
func TestFabricPreemptFencing(t *testing.T) {
	f := NewFabric()
	if err := f.Create(newTask("t1")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	epoch, err := f.Acquire("t1", "agent-a", time.Minute)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := f.Start("t1", "agent-a", epoch); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Non-owner rejected.
	if err := f.Preempt("t1", "agent-b", epoch, "x"); err != ErrNotOwner {
		t.Fatalf("non-owner preempt must be rejected, got %v", err)
	}
	// Wrong epoch rejected.
	if err := f.Preempt("t1", "agent-a", epoch+1, "x"); err != ErrEpochMismatch {
		t.Fatalf("stale epoch preempt must be rejected, got %v", err)
	}
	// The owner at the right epoch succeeds.
	if err := f.Preempt("t1", "agent-a", epoch, "ok"); err != nil {
		t.Fatalf("owner preempt must succeed: %v", err)
	}
}

// TestFabricPreemptDecisionByPriority verifies the Scheduler-side priority
// decision: a higher-priority task preempts a lower-priority RUNNING task.
func TestFabricPreemptDecisionByPriority(t *testing.T) {
	f := NewFabric()
	low := newTask("low")
	low.Priority = 1
	high := newTask("high")
	high.Priority = 100
	if err := f.Create(low); err != nil {
		t.Fatalf("Create low: %v", err)
	}
	if err := f.Create(high); err != nil {
		t.Fatalf("Create high: %v", err)
	}
	// Low-priority task is running under agent-a.
	epoch, err := f.Acquire("low", "agent-a", time.Minute)
	if err != nil {
		t.Fatalf("Acquire low: %v", err)
	}
	if err := f.Start("low", "agent-a", epoch); err != nil {
		t.Fatalf("Start low: %v", err)
	}
	// Scheduler decision: high(100) > low(1) → preempt low, dispatch high.
	if err := f.Preempt("low", "agent-a", epoch, "high-priority"); err != nil {
		t.Fatalf("Preempt low: %v", err)
	}
	if _, _, err := f.Schedule("high", []Candidate{{AgentID: "agent-a", Capabilities: []string{"rust"}, Confidence: 0.9}}, time.Minute); err != nil {
		t.Fatalf("Schedule high: %v", err)
	}
	task, _ := f.Task("high")
	if task.Owner != "agent-a" || task.State != StateLeased {
		t.Fatalf("high must be LEASED by agent-a, got owner=%q state=%s", task.Owner, task.State)
	}
}
