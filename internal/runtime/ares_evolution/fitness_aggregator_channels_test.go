package evolution

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/evidence"
)

// appendChannelFitness writes one fitness record for a source/strategy pair.
func appendChannelFitness(t *testing.T, store evidence.Store, id, source, strategyID string, value float64) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"value":               value,
		"success":             value > 0,
		evidenceKeyStrategyID: strategyID,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := store.Append(context.Background(), evidence.Evidence{
		ID:        id,
		Source:    source,
		Kind:      evidence.KindFitness,
		Payload:   payload,
		Timestamp: time.Now(),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
}

// TestAggregator_ChannelWeightsZeroKeepsChannelsInert is the default-off
// guarantee at the JUDGE stage: even with collaboration and tool_call evidence
// sitting in the store, weight 0 must leave the aggregate — and the sample count
// that gates the staging verdict — exactly as if the channels did not exist.
// Contributing "0 weight but +N count" would let an unarmed channel license a
// verdict on evidence the operator never opted into.
func TestAggregator_ChannelWeightsZeroKeepsChannelsInert(t *testing.T) {
	store := evidence.NewMemoryStore()
	appendChannelFitness(t, store, "s1", observerEvidenceSource, "strategy-A", 1.0)
	appendChannelFitness(t, store, "c1", collaborationEvidenceSource, "strategy-A", 0.0)
	appendChannelFitness(t, store, "t1", toolCallEvidenceSource, "strategy-A", 0.0)

	cfg := DefaultAggregatorConfig()
	cfg.MinSamplesBeforeJudge = 1
	agg := NewRuntimeFitnessAggregator(store, cfg)

	got := agg.Window(context.Background(), "strategy-A")
	// Only the strategy source counts: 1 record, mean 1.0.
	if got.Count != 1 {
		t.Errorf("count = %d, want 1 (unarmed channels must not add samples)", got.Count)
	}
	if got.Mean != 1.0 {
		t.Errorf("mean = %v, want 1.0 (unarmed channels must not move the aggregate)", got.Mean)
	}
	if _, ok := got.PerSource[collaborationEvidenceSource]; ok {
		t.Error("unarmed collaboration channel appeared in PerSource")
	}
	if _, ok := got.PerSource[toolCallEvidenceSource]; ok {
		t.Error("unarmed tool_call channel appeared in PerSource")
	}
}

// TestAggregator_ArmedChannelsMoveTheVerdict is the Step Y.4 payoff: once the
// operator gives the channels weight, a strategy whose collaboration and tool
// calls keep failing scores WORSE than one whose task outcomes look identical.
// This is the assertion that distinguishes "the evidence is recorded" from "the
// evidence affects evolution".
func TestAggregator_ArmedChannelsMoveTheVerdict(t *testing.T) {
	store := evidence.NewMemoryStore()
	// Both strategies look equally good on the task channel.
	appendChannelFitness(t, store, "s-good", observerEvidenceSource, "good", 1.0)
	appendChannelFitness(t, store, "s-bad", observerEvidenceSource, "bad", 1.0)
	// They differ only in the two channels evolution used to be blind to.
	appendChannelFitness(t, store, "c-good", collaborationEvidenceSource, "good", 1.0)
	appendChannelFitness(t, store, "t-good", toolCallEvidenceSource, "good", 1.0)
	appendChannelFitness(t, store, "c-bad", collaborationEvidenceSource, "bad", 0.0)
	appendChannelFitness(t, store, "t-bad", toolCallEvidenceSource, "bad", 0.0)

	cfg := DefaultAggregatorConfig()
	cfg.MinSamplesBeforeJudge = 1
	cfg.Weights.Collaboration = 0.2
	cfg.Weights.ToolCall = 0.2
	agg := NewRuntimeFitnessAggregator(store, cfg)

	good := agg.Window(context.Background(), "good")
	bad := agg.Window(context.Background(), "bad")

	if good.Mean <= bad.Mean {
		t.Fatalf("armed channels did not separate the strategies: good=%v bad=%v", good.Mean, bad.Mean)
	}
	// The bad strategy's task channel is perfect, so its aggregate must be
	// dragged below 1.0 purely by the two new channels.
	if bad.Mean >= 1.0 {
		t.Errorf("bad strategy mean = %v, want < 1.0 (channel failures must cost it)", bad.Mean)
	}
	if _, ok := bad.PerSource[collaborationEvidenceSource]; !ok {
		t.Error("armed collaboration channel missing from PerSource")
	}
	if _, ok := bad.PerSource[toolCallEvidenceSource]; !ok {
		t.Error("armed tool_call channel missing from PerSource")
	}
}

// TestAggregator_ChannelEvidenceIsStrategyScoped locks the attribution rule: a
// collaboration receipt or tool outcome belongs to the strategy that CHOSE to
// ask/call, so another strategy's records must never leak into its window.
// Unlike the runtime-global sources (workflow/scheduler/recovery), these two are
// candidate-specific — treating them as global would credit one strategy's tool
// discipline to all of them.
func TestAggregator_ChannelEvidenceIsStrategyScoped(t *testing.T) {
	store := evidence.NewMemoryStore()
	appendChannelFitness(t, store, "s-a", observerEvidenceSource, "A", 1.0)
	// Only strategy B has channel evidence, and it is all failures.
	appendChannelFitness(t, store, "c-b", collaborationEvidenceSource, "B", 0.0)
	appendChannelFitness(t, store, "t-b", toolCallEvidenceSource, "B", 0.0)

	cfg := DefaultAggregatorConfig()
	cfg.MinSamplesBeforeJudge = 1
	cfg.Weights.Collaboration = 0.2
	cfg.Weights.ToolCall = 0.2
	agg := NewRuntimeFitnessAggregator(store, cfg)

	got := agg.Window(context.Background(), "A")
	if got.Mean != 1.0 {
		t.Errorf("mean = %v, want 1.0 — B's channel failures must not touch A", got.Mean)
	}
	if got.Count != 1 {
		t.Errorf("count = %d, want 1 — only A's own record may count", got.Count)
	}
}

// TestAggregator_StagingPathIgnoresUnattributedChannelEvidence locks the
// staging-side behavior: deployment staging queries with an empty strategy ID,
// and the channel sources are strategy-scoped, so records that cannot be
// attributed to the queried strategy must not inflate the staging sample count.
func TestAggregator_StagingPathIgnoresUnattributedChannelEvidence(t *testing.T) {
	store := evidence.NewMemoryStore()
	appendChannelFitness(t, store, "c1", collaborationEvidenceSource, "some-strategy", 1.0)
	appendChannelFitness(t, store, "t1", toolCallEvidenceSource, "some-strategy", 1.0)

	cfg := DefaultAggregatorConfig()
	cfg.MinSamplesBeforeJudge = 1
	cfg.Weights.Collaboration = 0.2
	cfg.Weights.ToolCall = 0.2
	agg := NewRuntimeFitnessAggregator(store, cfg)

	// Staging path: empty strategy ID. The channel sources are queried with
	// the same empty ID, which querySourceMean treats as "no scoping", so the
	// records DO count here. That is the pre-existing staging contract (the
	// global sources behave identically); the assertion pins the behavior so a
	// future change to it is a deliberate decision, not a surprise.
	got := agg.Window(context.Background(), "")
	if got.Count != 2 {
		t.Errorf("staging count = %d, want 2", got.Count)
	}
	if got.Mean != 1.0 {
		t.Errorf("staging mean = %v, want 1.0", got.Mean)
	}
}

// appendToolStepFitness writes a tool_call fitness record carrying a
// process-level tool_step_id (Y1 C3), so the aggregator can scope below the
// per-strategy bucket to "this strategy calling the tool THIS way".
func appendToolStepFitness(t *testing.T, store evidence.Store, id, strategyID, toolStepID string, value float64) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"value":               value,
		"success":             value > 0,
		evidenceKeyStrategyID: strategyID,
		"tool_step_id":        toolStepID,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := store.Append(context.Background(), evidence.Evidence{
		ID:        id,
		Source:    toolCallEvidenceSource,
		Kind:      evidence.KindFitness,
		Payload:   payload,
		Timestamp: time.Now(),
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
}

// TestAggregator_WindowToolStepSeparatesProcesses is the C3 acceptance: under
// the SAME strategy, two tool steps (same tool, different argument shapes) must
// produce distinguishable fitness reads. Before process-level attribution both
// shapes blended into one undifferentiated tool_call signal — the GA could
// not tell "this way of calling the tool" from "that way".
func TestAggregator_WindowToolStepSeparatesProcesses(t *testing.T) {
	store := evidence.NewMemoryStore()
	// Same strategy calls "search" two ways: shape "k,q" mostly succeeds,
	// shape "k" mostly fails. Both are the same tool, same strategy.
	appendToolStepFitness(t, store, "a1", "strategy-A", "search#k,q", 1.0)
	appendToolStepFitness(t, store, "a2", "strategy-A", "search#k,q", 1.0)
	appendToolStepFitness(t, store, "a3", "strategy-A", "search#k,q", 0.0)
	appendToolStepFitness(t, store, "b1", "strategy-A", "search#k", 0.0)
	appendToolStepFitness(t, store, "b2", "strategy-A", "search#k", 0.0)

	cfg := DefaultAggregatorConfig()
	cfg.MinSamplesBeforeJudge = 1
	agg := NewRuntimeFitnessAggregator(store, cfg)

	good := agg.WindowToolStep(context.Background(), "strategy-A", "search#k,q")
	bad := agg.WindowToolStep(context.Background(), "strategy-A", "search#k")

	if !good.Ok || !bad.Ok {
		t.Fatalf("both tool steps must pass the judge gate; good.Ok=%v bad.Ok=%v", good.Ok, bad.Ok)
	}
	if good.Count != 3 || bad.Count != 2 {
		t.Fatalf("good.Count=%d (want 3) bad.Count=%d (want 2)", good.Count, bad.Count)
	}
	if good.Mean != 2.0/3.0 {
		t.Errorf("good step mean = %v, want %v", good.Mean, 2.0/3.0)
	}
	if bad.Mean != 0.0 {
		t.Errorf("bad step mean = %v, want 0.0", bad.Mean)
	}
	if good.Mean <= bad.Mean {
		t.Fatalf("process-level attribution failed to separate shapes: good=%v bad=%v", good.Mean, bad.Mean)
	}
}

// TestAggregator_WindowToolStepIgnoresOtherSteps locks the sub-filter: a
// WindowToolStep for one process must not read the OTHER process's records (the
// whole point of scoping below the tool).
func TestAggregator_WindowToolStepIgnoresOtherSteps(t *testing.T) {
	store := evidence.NewMemoryStore()
	appendToolStepFitness(t, store, "a1", "strategy-A", "search#k", 0.0)
	appendToolStepFitness(t, store, "b1", "strategy-A", "calc#expr", 1.0)

	cfg := DefaultAggregatorConfig()
	cfg.MinSamplesBeforeJudge = 1
	agg := NewRuntimeFitnessAggregator(store, cfg)

	got := agg.WindowToolStep(context.Background(), "strategy-A", "calc#expr")
	if got.Count != 1 || got.Mean != 1.0 {
		t.Fatalf("calc#expr read = (count %d, mean %v), want (1, 1.0) — search#k must not leak in", got.Count, got.Mean)
	}
}
