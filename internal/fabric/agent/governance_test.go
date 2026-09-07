package agentfabric

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestGovernance_ConsumeAndExceed verifies the token/tool budget gate:
// consumption is recorded, and exceeding a budget returns the cooperative
// yield signal ErrResourceExceeded.
func TestGovernance_ConsumeAndExceed(t *testing.T) {
	ctx := context.Background()
	f := NewFabric()
	if _, err := f.Spawn(ctx, SpawnSpec{
		Identity: "A",
		Governance: Governance{
			TokenBudget: 100,
			ToolBudget:  5,
		},
	}); err != nil {
		t.Fatalf("spawn: %v", err)
	}

	// Within budget: ok.
	if ok, err := f.CheckResource("A", 60, 2); err != nil || !ok {
		t.Fatalf("CheckResource(60,2) = %v, %v; want true, nil", ok, err)
	}
	if err := f.ConsumeResource("A", 60, 2); err != nil {
		t.Fatalf("consume 60/2: %v", err)
	}

	// Crossing token budget: rejected.
	if ok, err := f.CheckResource("A", 50, 0); err != nil || ok {
		t.Fatalf("CheckResource(50,0) = %v, %v; want false (token would exceed)", ok, err)
	}
	if err := f.ConsumeResource("A", 50, 0); !errors.Is(err, ErrResourceExceeded) {
		t.Fatalf("consume 50 tokens must yield ErrResourceExceeded, got %v", err)
	}

	// Tool budget independent: 4 more tools OK, 1 more exceeds.
	if err := f.ConsumeResource("A", 0, 3); err != nil {
		t.Fatalf("consume 3 tools: %v", err)
	}
	if err := f.ConsumeResource("A", 0, 1); !errors.Is(err, ErrResourceExceeded) {
		t.Fatalf("4th tool must exceed (used 5), got %v", err)
	}
}

// TestGovernance_UnlimitedZeroValues verifies zero budgets mean unlimited —
// the default for legacy agents, preserving backward compatibility.
func TestGovernance_UnlimitedZeroValues(t *testing.T) {
	ctx := context.Background()
	f := NewFabric()
	if _, err := f.Spawn(ctx, SpawnSpec{Identity: "A"}); err != nil { // no Governance
		t.Fatalf("spawn: %v", err)
	}
	if err := f.ConsumeResource("A", 1<<30, 1<<30); err != nil {
		t.Fatalf("unlimited agent must accept huge consumption, got %v", err)
	}
	if _, err := f.CheckResource("A", 1<<30, 1<<30); err != nil {
		t.Fatalf("unlimited CheckResource must not error, got %v", err)
	}
}

// TestGovernance_DeadlineExceeded verifies the wall-clock deadline is armed at
// spawn and reported at quantum boundaries via the injected clock.
func TestGovernance_DeadlineExceeded(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	f := NewFabric().WithClock(func() time.Time { return now })
	if _, err := f.Spawn(ctx, SpawnSpec{
		Identity:   "A",
		Governance: Governance{Deadline: time.Minute},
	}); err != nil {
		t.Fatalf("spawn: %v", err)
	}

	if over, err := f.DeadlineExceeded("A"); err != nil || over {
		t.Fatalf("deadline right after spawn = %v, %v; want false", over, err)
	}

	// Advance clock past the deadline.
	now = now.Add(2 * time.Minute)
	if over, err := f.DeadlineExceeded("A"); err != nil || !over {
		t.Fatalf("deadline after 2min = %v, %v; want true", over, err)
	}
}

// TestGovernance_ResetResource verifies Reset clears counters and re-arms the
// deadline — the post-checkpoint "new quantum" hook.
func TestGovernance_ResetResource(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	f := NewFabric().WithClock(func() time.Time { return now })
	if _, err := f.Spawn(ctx, SpawnSpec{
		Identity: "A",
		Governance: Governance{
			TokenBudget: 100,
			Deadline:    time.Minute,
		},
	}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if err := f.ConsumeResource("A", 60, 0); err != nil {
		t.Fatalf("consume: %v", err)
	}

	// Advance past deadline, then reset.
	now = now.Add(2 * time.Minute)
	if err := f.ResetResource("A"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if over, _ := f.DeadlineExceeded("A"); over {
		t.Fatal("deadline must be re-armed by ResetResource")
	}
	tok, tool, err := f.BudgetUsage("A")
	if err != nil || tok != 0 || tool != 0 {
		t.Fatalf("usage after reset = %d/%d, %v; want 0/0", tok, tool, err)
	}
	if err := f.ConsumeResource("A", 100, 0); err != nil {
		t.Fatalf("consume full budget after reset: %v", err)
	}
}

// TestGovernance_UnknownAndUngovernedAgents verifies error paths: unknown id
// and agents spawned without budgets.
func TestGovernance_UnknownAndUngovernedAgents(t *testing.T) {
	ctx := context.Background()
	f := NewFabric()
	if _, err := f.Spawn(ctx, SpawnSpec{Identity: "A"}); err != nil {
		t.Fatalf("spawn: %v", err)
	}

	// Unknown agent.
	if err := f.ConsumeResource("ghost", 1, 0); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("unknown agent = %v, want ErrAgentNotFound", err)
	}
	// Ungoverned agent (spawned with zero Governance = unlimited): consume OK.
	if err := f.ConsumeResource("A", 1, 0); err != nil {
		t.Fatalf("zero-governance agent must be unlimited, got %v", err)
	}
	// BudgetUsage works too.
	if tok, _, err := f.BudgetUsage("A"); err != nil || tok != 1 {
		t.Fatalf("usage = %d, %v; want 1", tok, err)
	}
}

// TestGovernance_CheckDoesNotConsume verifies CheckResource is a pure
// pre-quantum gate: repeated checks do not burn budget.
func TestGovernance_CheckDoesNotConsume(t *testing.T) {
	ctx := context.Background()
	f := NewFabric()
	if _, err := f.Spawn(ctx, SpawnSpec{
		Identity:   "A",
		Governance: Governance{TokenBudget: 10},
	}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	for i := 0; i < 5; i++ {
		if ok, err := f.CheckResource("A", 10, 0); err != nil || !ok {
			t.Fatalf("check %d = %v, %v; checks must not consume", i, ok, err)
		}
	}
	tok, _, _ := f.BudgetUsage("A")
	if tok != 0 {
		t.Fatalf("usage after 5 checks = %d, want 0", tok)
	}
}
