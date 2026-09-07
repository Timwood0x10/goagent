package evolution

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/feedback"
)

// waitForEvidence polls the store until n records of the given source exist or
// the deadline passes. The recorder writes from a drain goroutine, so the test
// must synchronize on the OBSERVABLE result rather than sleeping a guessed
// interval (code_rules §7.3 bans time.Sleep as a synchronization primitive).
func waitForEvidence(t *testing.T, store evidence.Store, source string, n int) []evidence.Evidence {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		evs, err := store.Query(context.Background(), evidence.Filter{
			Source: source,
			Kind:   evidence.KindFitness,
			Limit:  100,
		})
		if err != nil {
			t.Fatalf("query %s evidence: %v", source, err)
		}
		if len(evs) >= n {
			return evs
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d %s records, got %d", n, source, len(evs))
		}
	}
}

// decodeFitness extracts the fields every fitness consumer relies on.
func decodeFitness(t *testing.T, ev evidence.Evidence) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return payload
}

// TestChannelFeedback_CollaborationBecomesAttributedEvidence is the Step Y.2
// closure assertion: a collaboration receipt observed on the bus becomes
// KindFitness evidence under its OWN source, attributed to the active strategy,
// with the outcome mapped onto the [0,1] scale every consumer expects.
func TestChannelFeedback_CollaborationBecomesAttributedEvidence(t *testing.T) {
	store := evidence.NewMemoryStore()
	rec, err := NewChannelFeedbackRecorder(store, func() string { return "strategy-A" },
		ChannelFeedbackChannels{Collaboration: true})
	if err != nil {
		t.Fatalf("new recorder: %v", err)
	}
	rec.Start(context.Background())
	defer rec.Stop()

	rec.OnCollaboration(feedback.CollaborationOutcome{
		Initiator: "planner", Target: "researcher", Topic: "verify",
		Outcome: feedback.OutcomeSuccess, Latency: 12 * time.Millisecond,
	})
	rec.OnCollaboration(feedback.CollaborationOutcome{
		Initiator: "planner", Target: "ghost", Topic: "verify",
		Outcome: feedback.OutcomeNotFound,
	})

	evs := waitForEvidence(t, store, collaborationEvidenceSource, 2)
	var sawSuccess, sawNotFound bool
	for _, ev := range evs {
		p := decodeFitness(t, ev)
		if got := p[evidenceKeyStrategyID]; got != "strategy-A" {
			t.Errorf("strategy_id = %v, want strategy-A", got)
		}
		switch p["outcome"] {
		case string(feedback.OutcomeSuccess):
			sawSuccess = true
			if p["value"] != 1.0 {
				t.Errorf("success value = %v, want 1.0", p["value"])
			}
		case string(feedback.OutcomeNotFound):
			sawNotFound = true
			if p["value"] != 0.0 {
				t.Errorf("not_found value = %v, want 0.0", p["value"])
			}
			if p["target"] != "ghost" {
				t.Errorf("target = %v, want ghost", p["target"])
			}
		}
	}
	if !sawSuccess || !sawNotFound {
		t.Errorf("want both outcomes recorded, success=%v not_found=%v", sawSuccess, sawNotFound)
	}
}

// TestChannelFeedback_ToolCallBecomesAttributedEvidence is the Step Y.3
// counterpart.
func TestChannelFeedback_ToolCallBecomesAttributedEvidence(t *testing.T) {
	store := evidence.NewMemoryStore()
	rec, err := NewChannelFeedbackRecorder(store, func() string { return "strategy-B" },
		ChannelFeedbackChannels{ToolCalls: true})
	if err != nil {
		t.Fatalf("new recorder: %v", err)
	}
	rec.Start(context.Background())
	defer rec.Stop()

	rec.OnToolCall(feedback.ToolCallOutcome{
		Tool: "web_search", Caller: "agent-1",
		Outcome: feedback.OutcomeSuccess, Latency: 40 * time.Millisecond,
	})

	evs := waitForEvidence(t, store, toolCallEvidenceSource, 1)
	p := decodeFitness(t, evs[0])
	if p["tool"] != "web_search" || p["caller"] != "agent-1" {
		t.Errorf("tool/caller = %v/%v, want web_search/agent-1", p["tool"], p["caller"])
	}
	if p[evidenceKeyStrategyID] != "strategy-B" {
		t.Errorf("strategy_id = %v, want strategy-B", p[evidenceKeyStrategyID])
	}
	if p["value"] != 1.0 || p["success"] != true {
		t.Errorf("value/success = %v/%v, want 1.0/true", p["value"], p["success"])
	}
	if p["latency_ms"] != float64(40) {
		t.Errorf("latency_ms = %v, want 40", p["latency_ms"])
	}
}

// TestChannelFeedback_DisarmedChannelRecordsNothing locks the isolation
// guarantee that makes the feature safe to ship default-off: arming one channel
// must not smuggle the other one in. Enforced at the recorder, so no wiring site
// can get it wrong.
func TestChannelFeedback_DisarmedChannelRecordsNothing(t *testing.T) {
	store := evidence.NewMemoryStore()
	rec, err := NewChannelFeedbackRecorder(store, func() string { return "strategy-A" },
		ChannelFeedbackChannels{Collaboration: true}) // tool channel NOT armed
	if err != nil {
		t.Fatalf("new recorder: %v", err)
	}
	rec.Start(context.Background())

	rec.OnToolCall(feedback.ToolCallOutcome{Tool: "web_search", Outcome: feedback.OutcomeSuccess})
	rec.OnCollaboration(feedback.CollaborationOutcome{
		Initiator: "a", Target: "b", Topic: "t", Outcome: feedback.OutcomeSuccess,
	})
	// Stop flushes the queue, so after it returns the store holds everything
	// that was ever going to be written.
	rec.Stop()

	tools, err := store.Query(context.Background(), evidence.Filter{Source: toolCallEvidenceSource, Limit: 10})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(tools) != 0 {
		t.Errorf("disarmed tool channel wrote %d records, want 0", len(tools))
	}
	collab, err := store.Query(context.Background(), evidence.Filter{Source: collaborationEvidenceSource, Limit: 10})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(collab) != 1 {
		t.Errorf("armed collaboration channel wrote %d records, want 1", len(collab))
	}
}

// TestChannelFeedback_UnattributableRecordIsDropped locks the anti-fabrication
// rule: with no active strategy there is nobody to credit, and writing the
// record anyway would either be ignored by the scoped aggregator or credited to
// the wrong strategy. Dropping it and counting the drop is the honest outcome.
func TestChannelFeedback_UnattributableRecordIsDropped(t *testing.T) {
	store := evidence.NewMemoryStore()
	rec, err := NewChannelFeedbackRecorder(store, func() string { return "" },
		ChannelFeedbackChannels{ToolCalls: true})
	if err != nil {
		t.Fatalf("new recorder: %v", err)
	}
	rec.Start(context.Background())
	rec.OnToolCall(feedback.ToolCallOutcome{Tool: "web_search", Outcome: feedback.OutcomeSuccess})
	rec.Stop()

	evs, err := store.Query(context.Background(), evidence.Filter{Source: toolCallEvidenceSource, Limit: 10})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(evs) != 0 {
		t.Errorf("wrote %d unattributable records, want 0", len(evs))
	}
	if rec.Dropped() != 1 {
		t.Errorf("dropped = %d, want 1 (a silent drop is worse than a counted one)", rec.Dropped())
	}
}

// TestChannelFeedback_UnobservedOutcomeIsNotEvidence locks that an abandoned
// call never becomes fitness evidence and is not counted as a drop either — it
// was never a measurement.
func TestChannelFeedback_UnobservedOutcomeIsNotEvidence(t *testing.T) {
	store := evidence.NewMemoryStore()
	rec, err := NewChannelFeedbackRecorder(store, func() string { return "strategy-A" },
		ChannelFeedbackChannels{Collaboration: true, ToolCalls: true})
	if err != nil {
		t.Fatalf("new recorder: %v", err)
	}
	rec.Start(context.Background())
	rec.OnCollaboration(feedback.CollaborationOutcome{
		Initiator: "a", Target: "b", Topic: "t", Outcome: feedback.OutcomeUnobserved,
	})
	rec.OnToolCall(feedback.ToolCallOutcome{Tool: "x", Outcome: feedback.OutcomeUnobserved})
	rec.Stop()

	for _, src := range []string{collaborationEvidenceSource, toolCallEvidenceSource} {
		evs, err := store.Query(context.Background(), evidence.Filter{Source: src, Limit: 10})
		if err != nil {
			t.Fatalf("query %s: %v", src, err)
		}
		if len(evs) != 0 {
			t.Errorf("%s wrote %d records for an unobserved outcome, want 0", src, len(evs))
		}
	}
	if rec.Dropped() != 0 {
		t.Errorf("dropped = %d, want 0 — an unobserved attempt is not a lost measurement", rec.Dropped())
	}
}

// appendFailStore is a test double whose Append always fails, so a test can
// verify that a refused store write is counted as a drop (Dropped() must be a
// faithful lower bound on lost evidence, including samples the store turned
// away). Query/Aggregate delegate to the embedded MemoryStore.
type appendFailStore struct {
	evidence.Store
}

func (s *appendFailStore) Append(_ context.Context, _ evidence.Evidence) error {
	return errors.New("injected append failure")
}

// TestChannelFeedback_AppendFailureCountsAsDrop locks that a sample the store
// refuses to persist is scored as lost evidence: the drain must count it
// rather than silently swallow it, or Dropped() would under-report how much
// fitness feedback never reached the judgment path.
func TestChannelFeedback_AppendFailureCountsAsDrop(t *testing.T) {
	store := &appendFailStore{Store: evidence.NewMemoryStore()}
	rec, err := NewChannelFeedbackRecorder(store, func() string { return "strategy-A" },
		ChannelFeedbackChannels{ToolCalls: true})
	if err != nil {
		t.Fatalf("new recorder: %v", err)
	}
	rec.Start(context.Background())
	rec.OnToolCall(feedback.ToolCallOutcome{Tool: "web_search", Outcome: feedback.OutcomeSuccess})
	rec.Stop()

	if rec.Dropped() != 1 {
		t.Errorf("dropped = %d, want 1 (a store-refused sample is a lost one)", rec.Dropped())
	}
}

// TestNewChannelFeedbackRecorder_RejectsDeadWiring locks the fail-loud
// construction contract: each missing dependency would make the recorder a
// no-op that reports itself as wired.
func TestNewChannelFeedbackRecorder_RejectsDeadWiring(t *testing.T) {
	armed := ChannelFeedbackChannels{ToolCalls: true}
	if _, err := NewChannelFeedbackRecorder(nil, func() string { return "s" }, armed); err == nil {
		t.Error("nil store must be rejected")
	}
	if _, err := NewChannelFeedbackRecorder(evidence.NewMemoryStore(), nil, armed); err == nil {
		t.Error("nil active-strategy resolver must be rejected")
	}
	if _, err := NewChannelFeedbackRecorder(evidence.NewMemoryStore(), func() string { return "s" },
		ChannelFeedbackChannels{}); err == nil {
		t.Error("a recorder with no armed channel must be rejected")
	}
}

// TestChannelFeedback_SourcesAreIsolatedFromStrategyVerdicts is the N-1
// isolation contract extended to the new channels: neither channel may write to
// "strategy" (the rollback window and deployment staging read it) or to
// "strategy_shadow" (the A/B pair reads it). A shared source would corrupt
// verdicts that measure a different thing.
func TestChannelFeedback_SourcesAreIsolatedFromStrategyVerdicts(t *testing.T) {
	if collaborationEvidenceSource == observerEvidenceSource ||
		collaborationEvidenceSource == shadowEvidenceSource {
		t.Errorf("collaboration source %q collides with a strategy verdict source", collaborationEvidenceSource)
	}
	if toolCallEvidenceSource == observerEvidenceSource ||
		toolCallEvidenceSource == shadowEvidenceSource {
		t.Errorf("tool_call source %q collides with a strategy verdict source", toolCallEvidenceSource)
	}

	store := evidence.NewMemoryStore()
	rec, err := NewChannelFeedbackRecorder(store, func() string { return "strategy-A" },
		ChannelFeedbackChannels{Collaboration: true, ToolCalls: true})
	if err != nil {
		t.Fatalf("new recorder: %v", err)
	}
	rec.Start(context.Background())
	rec.OnCollaboration(feedback.CollaborationOutcome{
		Initiator: "a", Target: "b", Topic: "t", Outcome: feedback.OutcomeSuccess,
	})
	rec.OnToolCall(feedback.ToolCallOutcome{Tool: "x", Outcome: feedback.OutcomeFailure})
	rec.Stop()

	for _, src := range []string{observerEvidenceSource, shadowEvidenceSource} {
		evs, err := store.Query(context.Background(), evidence.Filter{Source: src, Limit: 10})
		if err != nil {
			t.Fatalf("query %s: %v", src, err)
		}
		if len(evs) != 0 {
			t.Errorf("channel feedback polluted source %q with %d records", src, len(evs))
		}
	}
}
