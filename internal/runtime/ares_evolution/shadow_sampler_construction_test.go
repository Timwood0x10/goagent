package evolution

import (
	"context"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// TestNewShadowSampler_ConstructionDefaults locks the construction path
// (N-3 coverage debt): a non-positive sample count falls back to
// defaultShadowSamples, and a non-positive window span keeps the
// replayWindowSpan default — a config error can never shrink the window to a
// degenerate slice.
func TestNewShadowSampler_ConstructionDefaults(t *testing.T) {
	s := NewShadowSampler(NewShadowEvaluator(ShadowEvaluationConfig{}), 0)
	if s.samples != defaultShadowSamples {
		t.Fatalf("samples = %d, want default %d", s.samples, defaultShadowSamples)
	}
	if s.windowSpan != replayWindowSpan {
		t.Fatalf("windowSpan = %v, want default %v", s.windowSpan, replayWindowSpan)
	}

	span := 3 * time.Minute
	s2 := NewShadowSampler(NewShadowEvaluator(ShadowEvaluationConfig{}), 5, WithReplayWindowSpan(span))
	if s2.samples != 5 || s2.windowSpan != span {
		t.Fatalf("explicit options not applied: samples=%d span=%v", s2.samples, s2.windowSpan)
	}

	// Zero/negative span keeps the default instead of a degenerate window.
	s3 := NewShadowSampler(NewShadowEvaluator(ShadowEvaluationConfig{}), 5, WithReplayWindowSpan(0))
	if s3.windowSpan != replayWindowSpan {
		t.Fatalf("zero span must keep the default, got %v", s3.windowSpan)
	}
}

// TestShadowSampler_PrimeFailClosedWithoutScorer locks the fail-closed
// contract: without an independent scorer the sampler fabricates nothing —
// Prime is a no-op and the G2 gate stays without comparisons.
func TestShadowSampler_PrimeFailClosedWithoutScorer(t *testing.T) {
	se := NewShadowEvaluator(ShadowEvaluationConfig{Enabled: true})
	s := NewShadowSampler(se, 3)

	candidate := &mutation.Strategy{ID: "cand-1"}
	active := &mutation.Strategy{ID: "active-1"}

	// Nil receiver / nil evaluator / nil candidate guards.
	(*ShadowSampler)(nil).Prime(context.Background(), candidate, active)
	s.Prime(context.Background(), nil, active)

	// Wired but scorer-less: Prime must not fabricate comparisons.
	s.Prime(context.Background(), candidate, active)
	if got := len(se.Results()); got != 0 {
		t.Fatalf("no-scorer Prime recorded %d comparisons, want 0 (fail-closed)", got)
	}
	if se.ShadowStrategy() != nil {
		t.Fatal("no-scorer Prime must not point the evaluator at the candidate")
	}
}

// TestShadowSampler_SetExecutionFeeder locks the Step 4 feeder wiring:
// SetExecutionFeeder is nil-safe and clears with nil.
func TestShadowSampler_SetExecutionFeeder(t *testing.T) {
	var s *ShadowSampler
	s.SetExecutionFeeder(nil) // must not panic

	s = NewShadowSampler(NewShadowEvaluator(ShadowEvaluationConfig{}), 3)
	s.SetExecutionFeeder(nil)
	if s.execFeeder != nil {
		t.Fatal("nil feeder must clear the field")
	}
}
