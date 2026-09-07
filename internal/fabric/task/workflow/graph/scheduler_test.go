// package graph - tests for schedulers.

package graph

import (
	"math"
	"testing"
)

func TestDefaultScheduler(t *testing.T) {
	scheduler := NewDefaultScheduler()

	// Test empty queue
	if id := scheduler.Select([]string{}); id != "" {
		t.Errorf("expected empty string, got %s", id)
	}

	// Test single item
	if id := scheduler.Select([]string{"node1"}); id != "node1" {
		t.Errorf("expected node1, got %s", id)
	}

	// Test multiple items (FIFO)
	queue := []string{"node1", "node2", "node3"}
	if id := scheduler.Select(queue); id != "node1" {
		t.Errorf("expected node1, got %s", id)
	}
}

func TestPriorityScheduler(t *testing.T) {
	priorities := map[string]int{
		"node1": 1,
		"node2": 10,
		"node3": 5,
	}
	scheduler := NewPriorityScheduler(priorities)

	// Test empty queue
	if id := scheduler.Select([]string{}); id != "" {
		t.Errorf("expected empty string, got %s", id)
	}

	// Test highest priority
	queue := []string{"node1", "node2", "node3"}
	if id := scheduler.Select(queue); id != "node2" {
		t.Errorf("expected node2 (priority 10), got %s", id)
	}

	// Test default priority for unknown node
	queue = []string{"unknown"}
	if id := scheduler.Select(queue); id != "unknown" {
		t.Errorf("expected unknown, got %s", id)
	}

	// Test nil priorities
	scheduler = NewPriorityScheduler(nil)
	queue = []string{"node1", "node2"}
	if id := scheduler.Select(queue); id != "node1" {
		t.Errorf("expected node1 (default priority), got %s", id)
	}
}

func TestShortJobScheduler(t *testing.T) {
	estimates := map[string]int{
		"node1": 100,
		"node2": 50,
		"node3": 200,
	}
	scheduler := NewShortJobScheduler(estimates)

	// Test empty queue
	if id := scheduler.Select([]string{}); id != "" {
		t.Errorf("expected empty string, got %s", id)
	}

	// Test shortest job
	queue := []string{"node1", "node2", "node3"}
	if id := scheduler.Select(queue); id != "node2" {
		t.Errorf("expected node2 (50ms), got %s", id)
	}

	// Test default estimate for unknown node
	queue = []string{"unknown"}
	if id := scheduler.Select(queue); id != "unknown" {
		t.Errorf("expected unknown, got %s", id)
	}

	// Test nil estimates
	scheduler = NewShortJobScheduler(nil)
	queue = []string{"node1", "node2"}
	if id := scheduler.Select(queue); id != "node1" {
		t.Errorf("expected node1 (default estimate), got %s", id)
	}
}

// TestWeightedFairScheduler_EmptyReady verifies Select returns "" for an empty
// ready queue and does not panic.
func TestWeightedFairScheduler_EmptyReady(t *testing.T) {
	s := NewWeightedFairScheduler(map[string]int{"A": 3})
	if id := s.Select([]string{}); id != "" {
		t.Errorf("expected empty string for empty ready, got %s", id)
	}
}

// TestWeightedFairScheduler_CountersStayBounded reproduces the R11 bug: the
// previous implementation incremented every ready node's counter on each Select
// and never decremented, so counters grew without limit (roughly linearly with
// the number of calls). With the bounded DRR fix, every counter must stay
// within a constant band (bounded by the sum of ready weights) no matter how
// many Select calls are made.
func TestWeightedFairScheduler_CountersStayBounded(t *testing.T) {
	weights := map[string]int{"A": 5, "B": 3, "C": 2}
	s := NewWeightedFairScheduler(weights)
	ready := []string{"A", "B", "C"}
	totalWeight := 0
	for _, id := range ready {
		totalWeight += s.weight(id)
	}

	const iterations = 10000
	for i := 0; i < iterations; i++ {
		s.Select(ready)
	}

	// Counters must remain bounded by the (constant) total weight, NOT by the
	// iteration count. The old bug left each counter near `iterations`.
	for id := range weights {
		c := s.counter[id]
		if abs := int(math.Abs(float64(c))); abs > totalWeight {
			t.Errorf("counter[%s]=%d exceeds bound totalWeight=%d after %d calls "+
				"(counters must stay bounded, not grow with call count)",
				id, c, totalWeight, iterations)
		}
	}
}

// TestWeightedFairScheduler_RespectsWeights verifies selection is genuinely
// proportional to the configured weights, not just "first ready". With weights
// 5:3:2 over a full DRR cycle the scheduler selects A:B:C exactly 5:3:2.
func TestWeightedFairScheduler_RespectsWeights(t *testing.T) {
	weights := map[string]int{"A": 5, "B": 3, "C": 2}
	s := NewWeightedFairScheduler(weights)
	ready := []string{"A", "B", "C"}

	const iterations = 1000
	counts := map[string]int{}
	for i := 0; i < iterations; i++ {
		counts[s.Select(ready)]++
	}

	// Expected proportions: A=500, B=300, C=200 (5:3:2 of 1000). Allow a small
	// tolerance for tie-break boundary effects.
	expect := map[string]int{"A": 500, "B": 300, "C": 200}
	const tol = 5
	for id, want := range expect {
		got := counts[id]
		if abs := int(math.Abs(float64(got - want))); abs > tol {
			t.Errorf("weight not respected: %s selected %d times, want %d (±%d)", id, got, want, tol)
		}
	}
}

// TestWeightedFairScheduler_EqualWeightsIsRoundRobin verifies that with equal
// weights the scheduler does NOT collapse to "always pick the first ready node"
// (the old behavior once counters saturated). Instead it round-robins so every
// ready node is selected.
func TestWeightedFairScheduler_EqualWeightsIsRoundRobin(t *testing.T) {
	s := NewWeightedFairScheduler(map[string]int{"A": 1, "B": 1})
	ready := []string{"A", "B"}

	counts := map[string]int{}
	for i := 0; i < 100; i++ {
		counts[s.Select(ready)]++
	}

	if counts["A"] == 100 {
		t.Errorf("collapsed to always-first-ready: A=100, B=%d; expected round-robin", counts["B"])
	}
	if counts["B"] == 0 {
		t.Errorf("B never selected under equal weights; expected round-robin, got A=%d B=%d",
			counts["A"], counts["B"])
	}
	// Round-robin over 100 calls with 2 equal-weight nodes should be ~50/50.
	if math.Abs(float64(counts["A"]-counts["B"])) > 2 {
		t.Errorf("equal weights should round-robin ~50/50, got A=%d B=%d", counts["A"], counts["B"])
	}
}

// TestWeightedFairScheduler_SingleReadyBounded verifies that a single always
// ready node is selected every time while its counter stays bounded (net zero
// per call), rather than growing without limit.
func TestWeightedFairScheduler_SingleReadyBounded(t *testing.T) {
	s := NewWeightedFairScheduler(map[string]int{"A": 4})
	ready := []string{"A"}

	for i := 0; i < 5000; i++ {
		if id := s.Select(ready); id != "A" {
			t.Fatalf("call %d: expected A, got %s", i, id)
		}
	}
	if c := s.counter["A"]; c != 0 {
		t.Errorf("single-ready counter must stay at 0 (net zero per call), got %d after 5000 calls", c)
	}
}
