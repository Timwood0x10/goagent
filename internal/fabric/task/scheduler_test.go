package taskfabric

import "testing"

// TestScoreCapabilityGating verifies candidates without the required
// capability score 0 (even with high confidence), capable candidates score
// > 0, and unconstrained tasks are open to anyone.
func TestScoreCapabilityGating(t *testing.T) {
	// Incapable candidate: 0 regardless of confidence.
	if s := Score("rust", Candidate{Capabilities: []string{"python", "web"}, Confidence: 0.9}); s != 0 {
		t.Fatalf("incapable candidate must score 0, got %v", s)
	}
	// Capable candidate with confidence: > 0.
	if s := Score("rust", Candidate{Capabilities: []string{"rust"}, Confidence: 0.9}); s <= 0 {
		t.Fatalf("capable candidate must score > 0, got %v", s)
	}
	// Unconstrained task (no capability required): open.
	if s := Score("", Candidate{Capabilities: []string{"python"}, Confidence: 0.5}); s <= 0 {
		t.Fatalf("unconstrained task must be open, got %v", s)
	}
}

// TestPickBestExecutorWins verifies "who is the best executor" (design §8):
// the agent covering the capability chain fully, with low load and high
// confidence, wins — not merely the idle one.
func TestPickBestExecutorWins(t *testing.T) {
	candidates := []Candidate{
		{AgentID: "A", Capabilities: []string{"rust"}, Load: 0.2, Confidence: 0.81},
		{AgentID: "B", Capabilities: []string{"python"}, Load: 0.0, Confidence: 0.99},
		{AgentID: "C", Capabilities: []string{"rust", "unsafe-analysis"}, Load: 0.4, Confidence: 0.97},
	}
	best := Pick("rust/unsafe-analysis", candidates)
	if best == nil || best.AgentID != "C" {
		t.Fatalf("want C (full capability coverage) to win, got %+v", best)
	}
	// B is incapable for the rust chain → 0 despite being idle with high
	// confidence.
	if s := Score("rust/unsafe-analysis", candidates[1]); s != 0 {
		t.Fatalf("B must score 0, got %v", s)
	}
}

// TestScoreLoadDiscountsBusyAgents verifies load in [0,1] discounts a busy
// candidate (everything else equal).
func TestScoreLoadDiscountsBusyAgents(t *testing.T) {
	idle := Candidate{Capabilities: []string{"rust"}, Load: 0.0, Confidence: 0.9}
	busy := Candidate{Capabilities: []string{"rust"}, Load: 0.8, Confidence: 0.9}
	if Score("rust", idle) <= Score("rust", busy) {
		t.Fatalf("idle agent must outscore busy agent: idle=%v busy=%v",
			Score("rust", idle), Score("rust", busy))
	}
}

// TestScorePriorityBoostsHighPriority verifies Priority boosts the score
// (score × (1 + priority)) and that the default 0 keeps pre-priority behavior.
func TestScorePriorityBoostsHighPriority(t *testing.T) {
	normal := Candidate{Capabilities: []string{"rust"}, Load: 0.0, Confidence: 0.5}
	boosted := Candidate{Capabilities: []string{"rust"}, Load: 0.0, Confidence: 0.5, Priority: 1.0}
	base := Score("rust", normal)
	high := Score("rust", boosted)
	if high != 2*base {
		t.Fatalf("priority 1.0 must double the score: base=%v high=%v", base, high)
	}
	if base != 0.5 {
		t.Fatalf("priority 0 must keep pre-priority score, got %v", base)
	}
}

// TestPickPrefersHighPriorityTieBreak verifies that among otherwise-equal
// candidates, the higher-priority one wins (OS-thread analog).
func TestPickPrefersHighPriorityTieBreak(t *testing.T) {
	candidates := []Candidate{
		{AgentID: "A", Capabilities: []string{"rust"}, Load: 0.0, Confidence: 0.9},
		{AgentID: "B", Capabilities: []string{"rust"}, Load: 0.0, Confidence: 0.9, Priority: 0.5},
	}
	best := Pick("rust", candidates)
	if best == nil || best.AgentID != "B" {
		t.Fatalf("want B (higher priority) to win, got %+v", best)
	}
}

// TestCapabilityOverlapProportional verifies proportional chain coverage: a
// partial cover scores proportionally, a full cover scores 1, no overlap
// scores 0, and an unconstrained task is open.
func TestCapabilityOverlapProportional(t *testing.T) {
	// Partial coverage: "rust" covers half of the chain.
	if o := CapabilityOverlap("rust/unsafe-analysis", []string{"rust"}); o != 0.5 {
		t.Fatalf("want 0.5, got %v", o)
	}
	// Full coverage.
	if o := CapabilityOverlap("rust/unsafe-analysis", []string{"rust", "unsafe-analysis"}); o != 1.0 {
		t.Fatalf("want 1.0, got %v", o)
	}
	// No overlap.
	if o := CapabilityOverlap("rust/llvm", []string{"python"}); o != 0 {
		t.Fatalf("want 0, got %v", o)
	}
	// Unconstrained → open.
	if o := CapabilityOverlap("", []string{"anything"}); o != 1.0 {
		t.Fatalf("unconstrained must be open, got %v", o)
	}
}

// TestCapabilityOverlapExactMatchWins pins the namespaced-capability fix: a
// candidate declaring the required chain verbatim covers it fully (1.0), so
// among sibling "tool/*" agents the exact executor always beats the partial
// prefix match — deterministically, not by map-iteration order. Without the
// exact-match short-circuit both "tool/write" and "tool/research" scored 0.5
// for a "tool/write" task (each matching only the shared "tool" segment), and
// Pick's strict-greater tie-break let the wrong agent win at random.
func TestCapabilityOverlapExactMatchWins(t *testing.T) {
	// Exact declaration covers its own chain fully.
	if o := CapabilityOverlap("tool/write", []string{"tool/write"}); o != 1.0 {
		t.Fatalf("exact match must be 1.0, got %v", o)
	}
	// A sibling sharing only the namespace prefix stays a partial (0.5) cover.
	if o := CapabilityOverlap("tool/write", []string{"tool/research"}); o != 0.5 {
		t.Fatalf("sibling tool/* must score 0.5, got %v", o)
	}
	// Pick routes the task to the exact executor regardless of candidate order.
	for _, order := range [][]Candidate{
		{{AgentID: "research", Capabilities: []string{"tool/research"}, Confidence: 1},
			{AgentID: "writer", Capabilities: []string{"tool/write"}, Confidence: 1}},
		{{AgentID: "writer", Capabilities: []string{"tool/write"}, Confidence: 1},
			{AgentID: "research", Capabilities: []string{"tool/research"}, Confidence: 1}},
	} {
		if best := Pick("tool/write", order); best == nil || best.AgentID != "writer" {
			t.Fatalf("writer must win tool/write regardless of order, got %+v", best)
		}
	}
}

// TestPick_LastResortKeepsAllFailureCandidateReachable pins the retry
// reachability contract: a candidate whose history is all failures has
// Confidence 0 ⇒ Score 0, but when it is among the capability-overlapping
// candidates and nothing else scores positive, Pick must still return it —
// otherwise a single recorded failure permanently stranded tasks whose only
// capable executor had failed once (bounded retries could never run).
func TestPick_LastResortKeepsAllFailureCandidateReachable(t *testing.T) {
	failed := Candidate{AgentID: "flaky", Capabilities: []string{"code"}, Confidence: 0}
	if got := Pick("code", []Candidate{failed}); got == nil {
		t.Fatal("all-failure candidate must remain reachable as last resort, got nil")
	} else if got.AgentID != "flaky" {
		t.Fatalf("want flaky, got %s", got.AgentID)
	}
}

// TestPick_HealthyBeatsFailed pins the ranking half of the last-resort
// contract: a healthy candidate still wins; the fallback only fires when NO
// candidate scores positive.
func TestPick_HealthyBeatsFailed(t *testing.T) {
	cands := []Candidate{
		{AgentID: "flaky", Capabilities: []string{"code"}, Confidence: 0},
		{AgentID: "solid", Capabilities: []string{"code"}, Confidence: 1.0},
	}
	got := Pick("code", cands)
	if got == nil || got.AgentID != "solid" {
		t.Fatalf("healthy candidate must win, got %v", got)
	}
}

// TestPick_NoOverlapStillNil pins the capability gate: the fallback tier must
// NOT resuscitate candidates that cannot do the work at all.
func TestPick_NoOverlapStillNil(t *testing.T) {
	writer := Candidate{AgentID: "writer", Capabilities: []string{"write"}, Confidence: 1.0}
	if got := Pick("code", []Candidate{writer}); got != nil {
		t.Fatalf("non-overlapping candidate must never be picked, got %v", got.AgentID)
	}
}
