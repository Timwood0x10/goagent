package main

import (
	"context"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/aresrecovery"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/fabric/task"
)

// TestW4EvolutionFeedbackChangesSchedulerBehavior verifies the evolution feedback
// loop: execution results (success/failure) are collected, fed back to the
// scheduler's confidence scoring, and the next scheduling decision changes
// as a result.
//
// Setup: two agents (A, B) with the same capability. Initially both have
// neutral confidence (1.0). Agent A fails several tasks; Agent B succeeds.
// The evolution feedback adapter reads the attribution and pushes the
// confidence into the loadTracker. After the feedback, the scheduler must
// prefer B over A (B's confidence is higher → higher score).
func TestW4EvolutionFeedbackChangesSchedulerBehavior(t *testing.T) {
	// Build the attribution store and the loadTracker.
	attribution := aresrecovery.NewExecutionAttribution()
	tracker := newLoadTracker()

	// Two agents with the same capability.
	agentA := &stubAgent{id: "agent-A", typ: models.AgentType("code")}
	agentB := &stubAgent{id: "agent-B", typ: models.AgentType("code")}

	// Fabric with a task that requires "code".
	f := taskfabric.NewFabric()
	if err := f.Create(&taskfabric.Task{
		ID:          "w4-task",
		Capability:  "code",
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 1},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Simulate execution outcomes: A fails 3 times, B succeeds 3 times.
	for i := 0; i < 3; i++ {
		attribution.Record("agent-A", "code", false)
		attribution.Record("agent-B", "code", true)
	}

	// Before feedback: both agents have confidence 1.0 (neutral prior,
	// no override set). Score is equal → Schedule picks the first candidate
	// (agent-A in the map iteration order, but Pick is deterministic: it
	// picks the highest score; ties go to the first encountered).
	tracker.Begin("agent-A") // mark A as busy so B wins
	confA := tracker.Confidence("agent-A")
	confB := tracker.Confidence("agent-B")
	if confA != 1.0 || confB != 1.0 {
		t.Fatalf("pre-feedback confidence must be 1.0 for both, got A=%f B=%f", confA, confB)
	}
	tracker.End("agent-A", true)

	// Apply the feedback: read attribution → push confidence into tracker.
	adapter := aresrecovery.NewEvolutionFeedbackAdapter(attribution, tracker)
	updated := adapter.Apply(context.Background())
	if updated != 2 {
		t.Fatalf("feedback must update 2 agents, got %d", updated)
	}

	// After feedback: A's confidence is 0.0 (0/3), B's is 1.0 (3/3).
	confA = tracker.Confidence("agent-A")
	confB = tracker.Confidence("agent-B")
	if confA != 0.0 {
		t.Fatalf("agent-A confidence must be 0.0 after feedback (0/3 success), got %f", confA)
	}
	if confB != 1.0 {
		t.Fatalf("agent-B confidence must be 1.0 after feedback (3/3 success), got %f", confB)
	}

	// Now verify the scheduler prefers B over A. Build candidates and score.
	cands := []taskfabric.Candidate{
		{AgentID: "agent-A", Capabilities: []string{"code"}, Load: 0, Confidence: tracker.Confidence("agent-A")},
		{AgentID: "agent-B", Capabilities: []string{"code"}, Load: 0, Confidence: tracker.Confidence("agent-B")},
	}
	winner := taskfabric.Pick("code", cands)
	if winner == nil {
		t.Fatal("Pick must return a winner")
	}
	if winner.AgentID != "agent-B" {
		t.Fatalf("after feedback, winner must be agent-B (higher confidence), got %s", winner.AgentID)
	}

	// Verify the scheduler actually executes the task with B, not A.
	sched := NewKernelScheduler(f, map[string]CapabilityExecutor{
		"agent-A": agentA,
		"agent-B": agentB,
	}, tracker)
	sched.PollInterval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(ctx)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tk, err := f.Task("w4-task")
		if err == nil && tk.State == taskfabric.StateCompleted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if agentB.executedCount() != 1 {
		t.Fatalf("agent-B must execute the task (higher confidence), got %d executions", agentB.executedCount())
	}
	if agentA.executedCount() != 0 {
		t.Fatalf("agent-A must NOT execute (lower confidence), got %d executions", agentA.executedCount())
	}
}

// TestW4CapabilityConfidenceAttribution verifies the per-capability
// attribution: an agent that succeeds on "code" but fails on "rust" has
// different confidence values per capability. The attribution store tracks
// the capability dimension, not just the aggregate.
func TestW4CapabilityConfidenceAttribution(t *testing.T) {
	attr := aresrecovery.NewExecutionAttribution()

	// Agent A: 3/3 success on "code", 0/2 success on "rust".
	for i := 0; i < 3; i++ {
		attr.Record("agent-A", "code", true)
	}
	for i := 0; i < 2; i++ {
		attr.Record("agent-A", "rust", false)
	}

	// Per-capability confidence.
	codeConf := attr.CapabilityConfidence("agent-A", "code")
	rustConf := attr.CapabilityConfidence("agent-A", "rust")
	if codeConf != 1.0 {
		t.Fatalf("code confidence must be 1.0 (3/3), got %f", codeConf)
	}
	if rustConf != 0.0 {
		t.Fatalf("rust confidence must be 0.0 (0/2), got %f", rustConf)
	}

	// Aggregate confidence: 3/5 = 0.6.
	agentConf := attr.AgentConfidence("agent-A")
	if agentConf != 0.6 {
		t.Fatalf("aggregate confidence must be 0.6 (3/5), got %f", agentConf)
	}
}

// TestW4CapabilityFeedbackConsumedByScheduler verifies the per-capability
// feedback path end-to-end: execution results recorded per capability are fed
// back through Apply, and the scheduler scores the SAME agent differently for
// two capabilities (design-fix: per-capability data is consumed, not just
// collected).
//
// Setup: agent-A succeeds on "code" (3/3) and fails on "rust" (0/2). After
// Apply, the scheduler's ConfidenceFor must return 1.0 on "code" and 0.0 on
// "rust" for the same agent, and the task-level Score must prefer a
// high-confidence rival on "rust".
func TestW4CapabilityFeedbackConsumedByScheduler(t *testing.T) {
	attribution := aresrecovery.NewExecutionAttribution()
	tracker := newLoadTracker()

	// Same agent: strong on "code", weak on "rust".
	for i := 0; i < 3; i++ {
		attribution.Record("agent-A", "code", true)
	}
	for i := 0; i < 2; i++ {
		attribution.Record("agent-A", "rust", false)
	}

	adapter := aresrecovery.NewEvolutionFeedbackAdapter(attribution, tracker)
	updated := adapter.Apply(context.Background())
	if updated != 1 {
		t.Fatalf("feedback must update 1 agent, got %d", updated)
	}

	// The same agent resolves to different confidences per capability.
	codeConf := tracker.ConfidenceFor("agent-A", "code")
	rustConf := tracker.ConfidenceFor("agent-A", "rust")
	if codeConf != 1.0 {
		t.Fatalf("ConfidenceFor(code) must be 1.0 (3/3), got %f", codeConf)
	}
	if rustConf != 0.0 {
		t.Fatalf("ConfidenceFor(rust) must be 0.0 (0/2), got %f", rustConf)
	}

	// The capability-specific value takes precedence over the aggregate one.
	agg := tracker.Confidence("agent-A") // 3/5 = 0.6
	if agg >= codeConf || agg <= rustConf {
		t.Fatalf("capability confidence must be more specific than aggregate (agg=%f code=%f rust=%f)", agg, codeConf, rustConf)
	}

	// A "rust" task must prefer a rival with higher rust confidence over
	// agent-A (whose rust confidence is 0.0).
	cands := []taskfabric.Candidate{
		{AgentID: "agent-A", Capabilities: []string{"code", "rust"}, Load: 0, Confidence: tracker.ConfidenceFor("agent-A", "rust")},
		{AgentID: "agent-B", Capabilities: []string{"rust"}, Load: 0, Confidence: 0.5},
	}
	winner := taskfabric.Pick("rust", cands)
	if winner == nil {
		t.Fatal("Pick must return a winner")
	}
	if winner.AgentID != "agent-B" {
		t.Fatalf("on 'rust', agent-B (confidence 0.5) must beat agent-A (0.0), got %s", winner.AgentID)
	}
}

// TestW4CapabilityFeedbackClearedByNegative verifies that a negative
// confidence clears the capability-specific override (falls back to the
// aggregate), matching SetAgentConfidence semantics.
func TestW4CapabilityFeedbackClearedByNegative(t *testing.T) {
	tracker := newLoadTracker()
	tracker.SetCapabilityConfidence("agent-A", "code", 0.25)
	tracker.SetCapabilityConfidence("agent-A", "code", -1.0)
	if got := tracker.ConfidenceFor("agent-A", "code"); got != 1.0 {
		t.Fatalf("cleared capability override must fall back to neutral prior 1.0, got %f", got)
	}
}

// TestW4FeedbackAdapterApplyIsIdempotent verifies that calling Apply multiple
// times with no new results is harmless — the confidence values are the same
// after the second call.
func TestW4FeedbackAdapterApplyIsIdempotent(t *testing.T) {
	attr := aresrecovery.NewExecutionAttribution()
	tracker := newLoadTracker()

	attr.Record("agent-X", "code", true)
	attr.Record("agent-X", "code", false)

	adapter := aresrecovery.NewEvolutionFeedbackAdapter(attr, tracker)
	adapter.Apply(context.Background())
	conf1 := tracker.Confidence("agent-X")
	adapter.Apply(context.Background())
	conf2 := tracker.Confidence("agent-X")
	if conf1 != conf2 {
		t.Fatalf("idempotent Apply must not change confidence: %f → %f", conf1, conf2)
	}
}

// TestW4FeedbackAdapterNilSafe verifies the adapter and loop handle nil
// gracefully without panicking.
func TestW4FeedbackAdapterNilSafe(t *testing.T) {
	// nil adapter → Apply is a no-op.
	var nilAdapter *aresrecovery.EvolutionFeedbackAdapter
	if got := nilAdapter.Apply(context.Background()); got != 0 {
		t.Fatalf("nil adapter Apply must return 0, got %d", got)
	}

	// nil source/injector → Apply returns 0.
	adapter := aresrecovery.NewEvolutionFeedbackAdapter(nil, nil)
	if got := adapter.Apply(context.Background()); got != 0 {
		t.Fatalf("nil source/injector Apply must return 0, got %d", got)
	}
}
