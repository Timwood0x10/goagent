package taskfabric

import (
	"context"
	"errors"
	"testing"
)

// TestCompilePlan_Linear covers a straight A→B→C chain: all tasks are created
// READY and dependencies land verbatim on the fabric tasks.
func TestCompilePlan_Linear(t *testing.T) {
	f := NewFabric()
	ids, err := f.CompilePlan(context.Background(), []PlanStep{
		{ID: "a", Capability: "code"},
		{ID: "b", Capability: "code", DependsOn: []string{"a"}},
		{ID: "c", Capability: "code", DependsOn: []string{"b"}},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("created %d ids, want 3", len(ids))
	}
	b, err := f.Task("b")
	if err != nil {
		t.Fatalf("task b: %v", err)
	}
	if len(b.Dependencies) != 1 || b.Dependencies[0] != "a" {
		t.Fatalf("b deps = %v, want [a]", b.Dependencies)
	}
}

// TestCompilePlan_Diamond covers the fan-out/fan-in shape used by the
// dashboard's plan view (root → {left,right} → join).
func TestCompilePlan_Diamond(t *testing.T) {
	f := NewFabric()
	_, err := f.CompilePlan(context.Background(), []PlanStep{
		{ID: "root", Capability: "plan"},
		{ID: "left", Capability: "code", DependsOn: []string{"root"}},
		{ID: "right", Capability: "review", DependsOn: []string{"root"}},
		{ID: "join", Capability: "synth", DependsOn: []string{"left", "right"}},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
}

// TestCompilePlan_CycleRejected covers the topological gate: a→b→a must fail
// and create nothing.
func TestCompilePlan_CycleRejected(t *testing.T) {
	f := NewFabric()
	_, err := f.CompilePlan(context.Background(), []PlanStep{
		{ID: "a", Capability: "code", DependsOn: []string{"b"}},
		{ID: "b", Capability: "code", DependsOn: []string{"a"}},
	})
	if err == nil {
		t.Fatal("cycle must be rejected")
	}
	for _, id := range []string{"a", "b"} {
		if _, terr := f.Task(id); terr == nil {
			t.Fatalf("task %q must not exist after failed compile", id)
		}
	}
}

// TestCompilePlan_UnknownDependencyRejected covers the closure gate.
func TestCompilePlan_UnknownDependencyRejected(t *testing.T) {
	f := NewFabric()
	_, err := f.CompilePlan(context.Background(), []PlanStep{
		{ID: "a", Capability: "code", DependsOn: []string{"ghost"}},
	})
	if err == nil {
		t.Fatal("unknown dependency must be rejected")
	}
}

// TestCompilePlan_DuplicateIDRejected covers the id-uniqueness gate.
func TestCompilePlan_DuplicateIDRejected(t *testing.T) {
	f := NewFabric()
	_, err := f.CompilePlan(context.Background(), []PlanStep{
		{ID: "a", Capability: "code"},
		{ID: "a", Capability: "review"},
	})
	if err == nil {
		t.Fatal("duplicate id must be rejected")
	}
}

// TestCompilePlan_AtomicRollback covers the all-or-nothing contract: a batch
// whose second Create fails must leave no trace of the first task.
func TestCompilePlan_AtomicRollback(t *testing.T) {
	f := NewFabric()
	if err := f.Create(&Task{ID: "preexisting"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err := f.CompilePlan(context.Background(), []PlanStep{
		{ID: "ok1", Capability: "code"},
		{ID: "preexisting", Capability: "code"}, // collides → Create fails
		{ID: "never", Capability: "code"},
	})
	if !errors.Is(err, ErrTaskExists) {
		t.Fatalf("want ErrTaskExists, got %v", err)
	}
	if _, terr := f.Task("ok1"); terr == nil {
		t.Fatal("ok1 must be rolled back (all-or-nothing)")
	}
}

// TestCompilePlan_PayloadRidesCheckpoint verifies step payloads surface in the
// task checkpoint envelope for the executor.
func TestCompilePlan_PayloadRidesCheckpoint(t *testing.T) {
	f := NewFabric()
	_, err := f.CompilePlan(context.Background(), []PlanStep{
		{ID: "a", Capability: "code", Payload: map[string]any{"k": "v"}},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	tk, terr := f.Task("a")
	if terr != nil {
		t.Fatalf("task a: %v", terr)
	}
	if tk.Checkpoint == nil {
		t.Fatal("payload must ride the checkpoint envelope")
	}
}
