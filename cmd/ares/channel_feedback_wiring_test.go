// channel_feedback_wiring_test.go is the Step Y.2/Y.3 wiring contract. It lives
// in cmd/ares because that is the only place where all three layers meet: the
// kernel IPC bus, the tool binder, and the evolution recorder. The two observer
// interfaces are satisfied STRUCTURALLY (no layer imports another), which means
// a rename on either side would break the wiring silently at some future
// refactor — the compile-time assertions below are what make that impossible.
package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/agentipc"
	"github.com/Timwood0x10/ares/internal/agents/sub"
	"github.com/Timwood0x10/ares/internal/aresrecovery"
	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/feedback"
	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
)

// Compile-time proof that the recorder satisfies both producer-side observer
// interfaces. Without these, the structural satisfaction is only checked at the
// (optional, config-gated) wiring call sites in serve — i.e. not at all in CI.
var (
	_ agentipc.CollaborationObserver = (*evolution.ChannelFeedbackRecorder)(nil)
	_ sub.ToolCallObserver           = (*evolution.ChannelFeedbackRecorder)(nil)
)

// TestChannelFeedback_BusToEvidenceEndToEnd is the Y.2 end-to-end assertion:
// a real collaboration over the real Bus produces strategy-attributed fitness
// evidence in the real store. Each half is unit-tested separately; this proves
// the two halves are actually connected.
func TestChannelFeedback_BusToEvidenceEndToEnd(t *testing.T) {
	store := evidence.NewMemoryStore()
	rec, err := evolution.NewChannelFeedbackRecorder(store, func() string { return "strategy-live" },
		evolution.ChannelFeedbackChannels{Collaboration: true})
	if err != nil {
		t.Fatalf("new recorder: %v", err)
	}
	rec.Start(context.Background())

	bus := agentipc.NewBus().WithCollaborationObserver(rec)
	if err := bus.Register("researcher", func(_ context.Context, msg *agentipc.Message) (*agentipc.Message, error) {
		return &agentipc.Message{Topic: msg.Topic, Payload: "found it"}, nil
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	if _, err := bus.Request(context.Background(), "planner", "researcher",
		"verify-conclusion", "is X safe?", time.Second); err != nil {
		t.Fatalf("request: %v", err)
	}
	// An unregistered target: the addressing mistake evolution should learn from.
	if _, err := bus.Request(context.Background(), "planner", "ghost",
		"verify-conclusion", nil, time.Second); err == nil {
		t.Fatal("want ErrAgentNotRegistered for an unregistered target")
	}
	// Stop flushes the queue, so the store is complete once it returns.
	rec.Stop()

	evs, err := store.Query(context.Background(), evidence.Filter{
		Source: "collaboration",
		Kind:   evidence.KindFitness,
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("collaboration evidence count = %d, want 2", len(evs))
	}

	outcomes := map[string]float64{}
	for _, ev := range evs {
		var p map[string]any
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if p["strategy_id"] != "strategy-live" {
			t.Errorf("strategy_id = %v, want strategy-live", p["strategy_id"])
		}
		target, _ := p["target"].(string)
		value, _ := p["value"].(float64)
		outcomes[target] = value
	}
	if outcomes["researcher"] != 1.0 {
		t.Errorf("successful collaboration scored %v, want 1.0", outcomes["researcher"])
	}
	if outcomes["ghost"] != 0.0 {
		t.Errorf("unroutable collaboration scored %v, want 0.0", outcomes["ghost"])
	}
}

// TestChannelFeedback_ProductionSendPathProducesEvidence is the assertion that
// keeps Y.2 honest about the PRODUCTION path. The peer bridge does not call
// Request: wireEvolutionIPC routes every peer message through
// EvolutionAwareIPC.Send → Bus.Send (evolution_ipc.go). An implementation that
// observed only Request would pass every unit test and record nothing in a real
// deployment, so this drives the same Send path the bridge uses and asserts the
// evidence actually lands.
func TestChannelFeedback_ProductionSendPathProducesEvidence(t *testing.T) {
	store := evidence.NewMemoryStore()
	rec, err := evolution.NewChannelFeedbackRecorder(store, func() string { return "strategy-live" },
		evolution.ChannelFeedbackChannels{Collaboration: true})
	if err != nil {
		t.Fatalf("new recorder: %v", err)
	}
	rec.Start(context.Background())

	bus := agentipc.NewBus().WithCollaborationObserver(rec)
	if err := bus.Register("peer-a", func(context.Context, *agentipc.Message) (*agentipc.Message, error) {
		return nil, nil // fire-and-forget acceptance, as the peer bridge does
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	// The wire policy layer the bridge interposes must not swallow the
	// observation: drive Send through EvolutionAwareIPC exactly as
	// wireEvolutionIPC does (nil policy source = plain json, the default).
	ipc := aresrecovery.NewEvolutionAwareIPC(bus, nil)
	if err := ipc.Send(context.Background(), "peer-b", "peer-a", peerTopic, "hello"); err != nil {
		t.Fatalf("send via evolution-aware IPC: %v", err)
	}
	if err := ipc.Send(context.Background(), "peer-b", "ghost", peerTopic, "hello"); err == nil {
		t.Fatal("want an error sending to an unregistered peer")
	}
	rec.Stop()

	evs, err := store.Query(context.Background(), evidence.Filter{
		Source: "collaboration",
		Kind:   evidence.KindFitness,
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("collaboration evidence count = %d, want 2 (the production Send path must be observed)", len(evs))
	}
	for _, ev := range evs {
		var p map[string]any
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if p["kind"] != string(feedback.CollabSend) {
			t.Errorf("kind = %v, want %q", p["kind"], feedback.CollabSend)
		}
		if p["strategy_id"] != "strategy-live" {
			t.Errorf("strategy_id = %v, want strategy-live", p["strategy_id"])
		}
	}
}

// TestChannelFeedback_ToolBinderToEvidenceEndToEnd is the Y.3 counterpart: the
// decorator applied at serve's single binder site turns real tool invocations
// into fitness evidence.
func TestChannelFeedback_ToolBinderToEvidenceEndToEnd(t *testing.T) {
	store := evidence.NewMemoryStore()
	rec, err := evolution.NewChannelFeedbackRecorder(store, func() string { return "strategy-live" },
		evolution.ChannelFeedbackChannels{ToolCalls: true})
	if err != nil {
		t.Fatalf("new recorder: %v", err)
	}
	rec.Start(context.Background())

	inner := sub.NewToolBinder()
	inner.BindTool("web_search", func(context.Context, map[string]any) (any, error) {
		return "results", nil
	})
	binder := sub.ObserveToolCalls(inner, rec)

	if _, err := binder.CallTool(context.Background(), "web_search", nil); err != nil {
		t.Fatalf("call web_search: %v", err)
	}
	if _, err := binder.CallTool(context.Background(), "no_such_tool", nil); err == nil {
		t.Fatal("want an error for an unknown tool")
	}
	rec.Stop()

	evs, err := store.Query(context.Background(), evidence.Filter{
		Source: "tool_call",
		Kind:   evidence.KindFitness,
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("tool_call evidence count = %d, want 2", len(evs))
	}
	byTool := map[string]string{}
	for _, ev := range evs {
		var p map[string]any
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("decode: %v", err)
		}
		tool, _ := p["tool"].(string)
		outcome, _ := p["outcome"].(string)
		byTool[tool] = outcome
	}
	if byTool["web_search"] != string(feedback.OutcomeSuccess) {
		t.Errorf("web_search outcome = %q, want success", byTool["web_search"])
	}
	if byTool["no_such_tool"] != string(feedback.OutcomeNotFound) {
		t.Errorf("no_such_tool outcome = %q, want not_found", byTool["no_such_tool"])
	}
}

// TestChannelFeedback_NilRecorderLeavesProductionPathsUntouched locks the
// default-off contract at the WIRING level, which is what keeps `make gate`
// unchanged: with no recorder, the arming predicates are false, so serve never
// attaches the bus observer and never wraps the binder.
func TestChannelFeedback_NilRecorderLeavesProductionPathsUntouched(t *testing.T) {
	var rec *evolution.ChannelFeedbackRecorder // the default: channels disarmed
	if rec.CollaborationArmed() {
		t.Error("a nil recorder must not report the collaboration channel armed")
	}
	if rec.ToolCallsArmed() {
		t.Error("a nil recorder must not report the tool channel armed")
	}
	// And the binder decorator is a no-op for a nil observer, so even a wiring
	// mistake cannot insert an indirection that measures nothing.
	inner := sub.NewToolBinder()
	if got := sub.ObserveToolCalls(inner, nil); got != inner {
		t.Error("ObserveToolCalls with no observer must return the binder unchanged")
	}
}
