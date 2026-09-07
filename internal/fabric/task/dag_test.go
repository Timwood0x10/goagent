package taskfabric

import (
	"testing"
	"time"
)

// depTask builds a task with dependencies.
func depTask(id string, deps ...string) *Task {
	return &Task{ID: id, Capability: "rust", Priority: 1, Dependencies: deps}
}

// TestFabricIsReady verifies the dependency gate: a task is ready only when
// READY itself and every dependency is COMPLETED.
func TestFabricIsReady(t *testing.T) {
	f := NewFabric()
	if err := f.Create(depTask("a")); err != nil {
		t.Fatalf("Create a: %v", err)
	}
	if err := f.Create(depTask("b", "a")); err != nil {
		t.Fatalf("Create b: %v", err)
	}
	// a has no deps → ready immediately.
	ready, err := f.IsReady("a")
	if err != nil || !ready {
		t.Fatalf("a must be ready, got %v err=%v", ready, err)
	}
	// b depends on a which is not completed → not ready.
	if ready, _ := f.IsReady("b"); ready {
		t.Fatal("b must not be ready while a is incomplete")
	}
	// Complete a → b becomes ready.
	epoch, err := f.Acquire("a", "agent-x", time.Minute)
	if err != nil {
		t.Fatalf("Acquire a: %v", err)
	}
	if err := f.Start("a", "agent-x", epoch); err != nil {
		t.Fatalf("Start a: %v", err)
	}
	if err := f.Complete("a", "agent-x", epoch); err != nil {
		t.Fatalf("Complete a: %v", err)
	}
	if ready, _ := f.IsReady("b"); !ready {
		t.Fatal("b must become ready after a completes")
	}
	// Unknown id.
	if _, err := f.IsReady("nope"); err != ErrTaskNotFound {
		t.Fatalf("want ErrTaskNotFound, got %v", err)
	}
}

// TestFabricReadyTasksDAG verifies the DAG-as-scheduling-source flow: A → B →
// C. Only A is initially ready; after A completes B becomes ready; after B
// completes C becomes ready. No leader dispatch needed.
func TestFabricReadyTasksDAG(t *testing.T) {
	f := NewFabric()
	if err := f.Create(depTask("a")); err != nil {
		t.Fatalf("Create a: %v", err)
	}
	if err := f.Create(depTask("b", "a")); err != nil {
		t.Fatalf("Create b: %v", err)
	}
	if err := f.Create(depTask("c", "b")); err != nil {
		t.Fatalf("Create c: %v", err)
	}

	// Stage 1: only a is ready.
	ready := f.ReadyTasks()
	if len(ready) != 1 || ready[0] != "a" {
		t.Fatalf("stage 1: want [a], got %v", ready)
	}
	// Complete a.
	if err := completeSimple(f, t, "a"); err != nil {
		t.Fatalf("complete a: %v", err)
	}
	// Stage 2: b becomes ready, c still blocked.
	ready = f.ReadyTasks()
	if len(ready) != 1 || ready[0] != "b" {
		t.Fatalf("stage 2: want [b], got %v", ready)
	}
	// Complete b.
	if err := completeSimple(f, t, "b"); err != nil {
		t.Fatalf("complete b: %v", err)
	}
	// Stage 3: c becomes ready.
	ready = f.ReadyTasks()
	if len(ready) != 1 || ready[0] != "c" {
		t.Fatalf("stage 3: want [c], got %v", ready)
	}
}

// completeSimple acquires, starts and completes a task via the fabric
// (LEASED → RUNNING → COMPLETED).
func completeSimple(f *Fabric, t *testing.T, id string) error {
	epoch, err := f.Acquire(id, "agent-x", time.Minute)
	if err != nil {
		return err
	}
	if err := f.Start(id, "agent-x", epoch); err != nil {
		return err
	}
	return f.Complete(id, "agent-x", epoch)
}
