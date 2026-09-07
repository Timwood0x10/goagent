package agentfabric

import (
	"context"
	"errors"
	"testing"
)

// TestSpawnResourceQuotaRejectsOverBudget verifies P5 resource admission: a
// spawn whose claim exceeds the remaining budget is rejected with
// ErrResourceQuotaExceeded and leaves the fabric untouched (no agent, no
// partial allocation).
func TestSpawnResourceQuotaRejectsOverBudget(t *testing.T) {
	f := NewFabric().WithResourceBudget(map[string]float64{"cpu": 4, "memory": 1024})

	// Within budget: ok.
	if _, err := f.Spawn(context.Background(), SpawnSpec{
		Identity: "a1", Capabilities: []string{"code"},
		Resources: map[string]any{"cpu": 2, "memory": 512},
	}); err != nil {
		t.Fatalf("spawn within budget: %v", err)
	}

	// Over budget (cpu 2 + 3 > 4): rejected.
	if _, err := f.Spawn(context.Background(), SpawnSpec{
		Identity: "a2", Capabilities: []string{"code"},
		Resources: map[string]any{"cpu": 3},
	}); !errors.Is(err, ErrResourceQuotaExceeded) {
		t.Fatalf("over-budget spawn must be rejected with ErrResourceQuotaExceeded, got %v", err)
	}
	if _, err := f.Get("a2"); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("rejected spawn must not register the agent, got %v", err)
	}
	// Exactly at budget is allowed (2 cpu used, 2 remaining, claim 2 = 4).
	if _, err := f.Spawn(context.Background(), SpawnSpec{
		Identity: "a2", Capabilities: []string{"code"},
		Resources: map[string]any{"cpu": 2},
	}); err != nil {
		t.Fatalf("exactly-at-budget spawn must be allowed, got %v", err)
	}
}

// TestSpawnResourceUnbudgetedKeyAllowed verifies resources without a budget
// entry are never rejected (carried as hints, pre-P5 behavior preserved).
func TestSpawnResourceUnbudgetedKeyAllowed(t *testing.T) {
	f := NewFabric().WithResourceBudget(map[string]float64{"cpu": 1})
	if _, err := f.Spawn(context.Background(), SpawnSpec{
		Identity:  "a1",
		Resources: map[string]any{"gpu": 8}, // no gpu budget → allowed
	}); err != nil {
		t.Fatalf("unbudgeted resource must be allowed, got %v", err)
	}
}

// TestSpawnNoBudgetAlwaysAllowed verifies a fabric without a budget never
// rejects on resources (backward compatible).
func TestSpawnNoBudgetAlwaysAllowed(t *testing.T) {
	f := NewFabric()
	if _, err := f.Spawn(context.Background(), SpawnSpec{
		Identity:  "a1",
		Resources: map[string]any{"cpu": 9999},
	}); err != nil {
		t.Fatalf("no-budget fabric must never reject resources, got %v", err)
	}
}

// TestKillReleasesResourceQuota verifies a killed agent's claim returns to the
// budget so a later spawn can reuse it (crash path; P5).
func TestKillReleasesResourceQuota(t *testing.T) {
	f := NewFabric().WithResourceBudget(map[string]float64{"cpu": 4})
	if _, err := f.Spawn(context.Background(), SpawnSpec{
		Identity:  "a1",
		Resources: map[string]any{"cpu": 4},
	}); err != nil {
		t.Fatalf("spawn a1: %v", err)
	}
	if _, err := f.Spawn(context.Background(), SpawnSpec{
		Identity:  "a2",
		Resources: map[string]any{"cpu": 1},
	}); !errors.Is(err, ErrResourceQuotaExceeded) {
		t.Fatalf("a2 must be rejected while a1 holds the whole cpu budget, got %v", err)
	}

	if err := f.Kill(context.Background(), "a1"); err != nil {
		t.Fatalf("Kill a1: %v", err)
	}
	// Budget freed: a2 now fits.
	if _, err := f.Spawn(context.Background(), SpawnSpec{
		Identity:  "a2",
		Resources: map[string]any{"cpu": 1},
	}); err != nil {
		t.Fatalf("a2 must fit after a1 killed, got %v", err)
	}
}

// TestRetireReleasesResourceQuota verifies a retired agent's claim returns to
// the budget (graceful decommission path; P5).
func TestRetireReleasesResourceQuota(t *testing.T) {
	f := NewFabric().WithResourceBudget(map[string]float64{"memory": 1000})
	if _, err := f.Spawn(context.Background(), SpawnSpec{
		Identity:  "a1",
		Resources: map[string]any{"memory": 1000},
	}); err != nil {
		t.Fatalf("spawn a1: %v", err)
	}
	if err := f.Retire(context.Background(), "a1"); err != nil {
		t.Fatalf("Retire a1: %v", err)
	}
	if _, err := f.Spawn(context.Background(), SpawnSpec{
		Identity:  "a2",
		Resources: map[string]any{"memory": 1000},
	}); err != nil {
		t.Fatalf("a2 must fit after a1 retired, got %v", err)
	}
}

// TestSpawnResourceClaimNotDoubleReleased verifies kill/retire are idempotent
// with respect to the quota: a second release of the same agent is a no-op
// (the claim is cleared on the first release).
func TestSpawnResourceClaimNotDoubleReleased(t *testing.T) {
	f := NewFabric().WithResourceBudget(map[string]float64{"cpu": 2})
	if _, err := f.Spawn(context.Background(), SpawnSpec{
		Identity:  "a1",
		Resources: map[string]any{"cpu": 2},
	}); err != nil {
		t.Fatalf("spawn a1: %v", err)
	}
	if err := f.Kill(context.Background(), "a1"); err != nil {
		t.Fatalf("first Kill: %v", err)
	}
	if err := f.Kill(context.Background(), "a1"); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("second Kill must report not found (already removed), got %v", err)
	}
	// The budget must still be free after the double release attempt.
	if _, err := f.Spawn(context.Background(), SpawnSpec{
		Identity:  "a2",
		Resources: map[string]any{"cpu": 2},
	}); err != nil {
		t.Fatalf("budget must not be double-consumed, got %v", err)
	}
}

// TestConcurrentSpawnResourceBudgetIsSafe verifies concurrent spawns under a
// tight budget never over-allocate (mutex-serialized admission; run with
// -race).
func TestConcurrentSpawnResourceBudgetIsSafe(t *testing.T) {
	f := NewFabric().WithResourceBudget(map[string]float64{"cpu": 4})
	const workers = 8
	const claim = 2.0 // each worker wants 2 → only 2 of 8 can win

	results := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func(n int) {
			_, err := f.Spawn(context.Background(), SpawnSpec{
				Identity:  "agent", // duplicate → some fail with ErrAgentExists too
				Resources: map[string]any{"cpu": claim},
			})
			results <- err
		}(i)
	}
	accepted := 0
	for i := 0; i < workers; i++ {
		if err := <-results; err == nil {
			accepted++
		}
	}
	// At most 2 agents can hold cpu=2 each (budget 4); duplicates reduce the
	// count further but never exceed 2.
	if accepted > 2 {
		t.Fatalf("concurrent spawns over-allocated the budget: %d accepted (max 2)", accepted)
	}
}
