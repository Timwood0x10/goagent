package ares_bootstrap

import (
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/aresrecovery"
	"github.com/Timwood0x10/ares/internal/introspect"
)

// TestEvolutionTrajectoryProvider verifies the tracer adapter renders recorded
// generations as JSON-friendly values enriched with change attribution
// and human-feedback combined fitness, and nil input yields nil
// (endpoint disabled).
func TestEvolutionTrajectoryProvider(t *testing.T) {
	if got := NewEvolutionTrajectoryProvider(nil, nil); got != nil {
		t.Fatalf("nil tracer must yield nil provider, got %v", got)
	}

	tracer := aresrecovery.NewEvolutionTracer()
	tracer.Record(1, 0.6, []string{"s1"}, []aresrecovery.GenerationChange{
		{StrategyID: "s1", Description: "temp 0.7→0.4", Impact: 0.2},
	})
	tracer.Record(2, 0.9, []string{"s1", "s2"}, []aresrecovery.GenerationChange{
		{StrategyID: "s1", Description: "temp 0.4→0.3"},
		{StrategyID: "s2", Description: "sampling top-p"},
	})
	store := aresrecovery.NewFeedbackStore()
	store.Add(aresrecovery.HumanFeedback{CandidateID: "s2", Rating: 5, Approved: true, Comments: "great"})

	provider := NewEvolutionTrajectoryProvider(tracer, store)
	if provider == nil {
		t.Fatal("non-nil tracer must yield a provider")
	}
	traj := provider.EvolutionTrajectory()
	if len(traj) != 2 {
		t.Fatalf("want 2 generations, got %d", len(traj))
	}
	gen := traj[0]
	if gen["generation"] != 1 || gen["best_score"] != 0.6 {
		t.Fatalf("unexpected generation view %v", gen)
	}
	if gen["breakthrough"] != false || gen["regression"] != false {
		t.Fatalf("unexpected flags %v", gen)
	}

	// Generation 2 must be attributed: delta 0.9-0.6 = 0.3 split across the
	// two changes (explicit Impact on s1 in gen 1 does not leak here).
	gen2 := traj[1]
	changes, ok := gen2["changes"].([]map[string]any)
	if !ok || len(changes) != 2 {
		t.Fatalf("want 2 attributed changes, got %v", gen2["changes"])
	}
	for _, c := range changes {
		imp, _ := c["impact"].(float64)
		if imp < 0.149 || imp > 0.151 {
			t.Fatalf("want equal-split impact ~0.15, got %v", c["impact"])
		}
	}
	// s2 is human-rated (5/5): combined_fitness = 0.3*0.9 + 0.7*5.
	feedback, ok := gen2["feedback"].([]map[string]any)
	if !ok || len(feedback) != 1 || feedback[0]["candidate_id"] != "s2" {
		t.Fatalf("want feedback enrichment for s2, got %v", gen2["feedback"])
	}
	if got, want := feedback[0]["combined_fitness"].(float64), aresrecovery.CombinedFitness(0.9, 5); got < want-1e-9 || got > want+1e-9 {
		t.Fatalf("combined fitness %v, want %v", got, want)
	}
	if feedback[0]["human_rating"] != float64(5) {
		t.Fatalf("rating %v", feedback[0]["human_rating"])
	}
}

// TestEvolutionFeedbackSink verifies the feedback adapter records dashboard
// submissions into the aresrecovery store.
func TestEvolutionFeedbackSink(t *testing.T) {
	if got := NewEvolutionFeedbackSink(nil); got != nil {
		t.Fatalf("nil store must yield nil sink, got %v", got)
	}

	store := aresrecovery.NewFeedbackStore()
	sink := NewEvolutionFeedbackSink(store)
	if sink == nil {
		t.Fatal("non-nil store must yield a sink")
	}
	if err := sink.SubmitFeedback(introspect.EvolutionFeedback{
		CandidateID: "c1", Rating: 4, Approved: true, Reason: "good", Comments: "nice",
	}); err != nil {
		t.Fatalf("SubmitFeedback: %v", err)
	}
	fb := store.ForCandidate("c1")
	if fb == nil {
		t.Fatal("feedback must be recorded")
	}
	if fb.Rating != 4 || !fb.Approved || fb.Reason != "good" || fb.Comments != "nice" {
		t.Fatalf("unexpected feedback %+v", fb)
	}
}

// TestObservabilitySpansProvider verifies the GlobalTracer adapter renders
// spans as JSON-friendly values, and nil input yields nil (endpoint disabled).
func TestObservabilitySpansProvider(t *testing.T) {
	if got := NewObservabilitySpansProvider(nil); got != nil {
		t.Fatalf("nil tracer must yield nil provider, got %v", got)
	}

	tracer := aresrecovery.NewGlobalTracer().WithClock(func() time.Time {
		return time.Unix(1700000000, 0)
	})
	tracer.TraceTask("t1", "created", nil)
	tracer.TraceTask("t1", "acquired", map[string]any{"agent": "a1"})
	tracer.Close("t1", "completed")

	provider := NewObservabilitySpansProvider(tracer)
	if provider == nil {
		t.Fatal("non-nil tracer must yield a provider")
	}
	spans := provider.Spans()
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d", len(spans))
	}
	span := spans[0]
	if span["kind"] != "task" || span["id"] != "t1" || span["status"] != "completed" {
		t.Fatalf("unexpected span %v", span)
	}
	events, ok := span["events"].([]map[string]any)
	if !ok || len(events) != 2 {
		t.Fatalf("want 2 events, got %v", span["events"])
	}
	if events[1]["name"] != "acquired" {
		t.Fatalf("unexpected event %v", events[1])
	}
}

// TestAdaptersSatisfyContracts verifies the adapters implement the introspect
// provider/sink contracts at compile time.
func TestAdaptersSatisfyContracts(t *testing.T) {
	var (
		_ introspect.EvolutionTrajectoryProvider = (*evolutionTrajectoryAdapter)(nil)
		_ introspect.EvolutionFeedbackSink       = (*evolutionFeedbackAdapter)(nil)
		_ introspect.ObservabilitySpansProvider  = (*globalTracerAdapter)(nil)
	)
}
