package evolution

import (
	"context"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

func TestShadowSampler_Prime(t *testing.T) {
	active := &mutation.Strategy{ID: "active-v1"}
	candidate := &mutation.Strategy{ID: "cand-v2"}

	t.Run("no_independent_scorer_leaves_zero_comparisons", func(t *testing.T) {
		// No scorer wired → the sampler must NOT fabricate evidence; the G2
		// gate stays fail-closed (0 comparisons).
		e := NewShadowEvaluator(ShadowEvaluationConfig{Enabled: true, MinSamples: 3})
		s := NewShadowSampler(e, 3)
		s.Prime(context.Background(), candidate, active)
		if got := len(e.Results()); got != 0 {
			t.Fatalf("expected 0 comparisons with no scorer, got %d", got)
		}
	})

	t.Run("with_scorer_gathers_min_samples", func(t *testing.T) {
		e := NewShadowEvaluator(ShadowEvaluationConfig{Enabled: true, MinSamples: 4})
		// Deterministic scorer: candidate always outperforms active.
		e.SetShadowScorer(func(_ context.Context, s *mutation.Strategy) float64 {
			if s.ID == candidate.ID {
				return 0.9
			}
			return 0.6
		})
		s := NewShadowSampler(e, 4)
		s.Prime(context.Background(), candidate, active)

		results := e.Results()
		if len(results) != 4 {
			t.Fatalf("expected 4 comparisons, got %d", len(results))
		}
		// All four compare active(0.6) vs candidate(0.9) → candidate wins.
		for _, r := range results {
			if !r.ShadowWon {
				t.Errorf("expected candidate to win, got active=%v shadow=%v", r.ActiveScore, r.ShadowScore)
			}
		}
	})

	t.Run("losing_candidate_produces_failing_verdict", func(t *testing.T) {
		// A scorer where candidate loses → win rate 0 → ShouldDeploy rejects.
		e := NewShadowEvaluator(ShadowEvaluationConfig{Enabled: true, MinSamples: 3, MinWinRate: 0.55})
		e.SetShadowScorer(func(_ context.Context, s *mutation.Strategy) float64 {
			if s.ID == candidate.ID {
				return 0.4
			}
			return 0.8
		})
		s := NewShadowSampler(e, 3)
		s.Prime(context.Background(), candidate, active)
		pass, report := e.ShouldDeploy()
		if pass {
			t.Fatal("expected ShouldDeploy=false when candidate loses")
		}
		if report == nil || report.TotalComparisons != 3 || report.WinRate != 0.0 {
			t.Fatalf("unexpected report: %+v", report)
		}
	})

	t.Run("prime_restarts_window_per_candidate", func(t *testing.T) {
		// Each Prime restarts the window (StartShadow resets), so calling it
		// twice only ever leaves the SECOND candidate's samples — the gate must
		// never judge a candidate on evidence gathered for another one.
		e := NewShadowEvaluator(ShadowEvaluationConfig{Enabled: true, MinSamples: 2})
		e.SetShadowScorer(func(_ context.Context, _ *mutation.Strategy) float64 {
			return 0.5
		})
		s := NewShadowSampler(e, 2)
		other := &mutation.Strategy{ID: "other"}
		s.Prime(context.Background(), candidate, active)
		s.Prime(context.Background(), other, active)
		if got := len(e.Results()); got != 2 {
			t.Fatalf("expected reset window of 2 comparisons, got %d", got)
		}
		if e.ShadowStrategy() == nil || e.ShadowStrategy().ID != other.ID {
			t.Fatalf("expected shadow strategy to be the latest prime, got %+v", e.ShadowStrategy())
		}
	})

	t.Run("nil_candidate_is_noop", func(t *testing.T) {
		e := NewShadowEvaluator(ShadowEvaluationConfig{Enabled: true, MinSamples: 2})
		e.SetShadowScorer(func(_ context.Context, _ *mutation.Strategy) float64 { return 0.5 })
		s := NewShadowSampler(e, 2)
		s.Prime(context.Background(), nil, active)
		if got := len(e.Results()); got != 0 {
			t.Fatalf("expected no comparisons for a nil candidate, got %d", got)
		}
	})

	t.Run("cancelled_context_stops_sampling", func(t *testing.T) {
		// A cancelled context must not keep burning scorer calls (an LLM
		// scorer would issue one network call per Evaluate).
		e := NewShadowEvaluator(ShadowEvaluationConfig{Enabled: true, MinSamples: 5})
		calls := 0
		e.SetShadowScorer(func(_ context.Context, _ *mutation.Strategy) float64 {
			calls++
			return 0.5
		})
		s := NewShadowSampler(e, 50)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		s.Prime(ctx, candidate, active)
		if calls != 0 {
			t.Fatalf("expected no scorer calls under a cancelled context, got %d", calls)
		}
	})

	t.Run("default_sample_count_on_non_positive", func(t *testing.T) {
		e := NewShadowEvaluator(ShadowEvaluationConfig{Enabled: true, MinSamples: 1})
		e.SetShadowScorer(func(_ context.Context, _ *mutation.Strategy) float64 { return 0.5 })
		s := NewShadowSampler(e, 0)
		s.Prime(context.Background(), candidate, active)
		if got := len(e.Results()); got != defaultShadowSamples {
			t.Fatalf("expected %d comparisons from the default, got %d", defaultShadowSamples, got)
		}
	})
}

// TestShadowSampler_Prime_BatchTimeout verifies the batch deadline (fix #2):
// a scorer that never returns promptly (simulating a hung LLM call) must not
// hold the evolution heartbeat hostage — Prime returns once the deadline
// elapses, leaving fewer than MinSamples comparisons so the G2 gate stays
// fail-closed rather than judging a partial window.
func TestShadowSampler_Prime_BatchTimeout(t *testing.T) {
	e := NewShadowEvaluator(ShadowEvaluationConfig{Enabled: true, MinSamples: 3})
	// Blocks until the context deadline fires, then returns a tie score.
	e.SetShadowScorer(func(ctx context.Context, _ *mutation.Strategy) float64 {
		<-ctx.Done()
		return 0.5
	})
	s := NewShadowSampler(e, 5)
	s.timeout = 50 * time.Millisecond

	done := make(chan struct{})
	go func() {
		s.Prime(context.Background(), &mutation.Strategy{ID: "cand"}, &mutation.Strategy{ID: "active"})
		close(done)
	}()

	select {
	case <-done:
		// Prime bounded by the batch deadline.
	case <-time.After(5 * time.Second):
		t.Fatal("Prime did not return within the batch deadline — heartbeat held hostage")
	}

	got := len(e.Results())
	if got >= 3 {
		t.Fatalf("expected fewer than MinSamples(3) comparisons after a timed-out batch, got %d", got)
	}
}

func TestShadowSampler_NilSafe(t *testing.T) {
	// A nil sampler must not panic when called (lifecycle nil-checks, but be
	// defensive), and neither must one with a nil evaluator.
	var s *ShadowSampler
	s.Prime(context.Background(), &mutation.Strategy{ID: "x"}, &mutation.Strategy{ID: "y"})

	empty := &ShadowSampler{}
	empty.Prime(context.Background(), &mutation.Strategy{ID: "x"}, &mutation.Strategy{ID: "y"})
}
