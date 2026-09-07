package evolution

import (
	"context"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// TestZeroLLMClosure_EndToEndPromotion verifies the P0-1 invariant: in a
// zero-LLM config with a seeded evidence store, a candidate strategy can
// complete the G2 shadow gate and be promoted to ACTIVE. This is the
// end-to-end "Submit → G2 → Promote" chain the reviews demand.
//
// Setup:
//   - WiredEvolutionSystem with zero-LLM config (no cfg.Scorer, replay scorer
//     injected after construction, mirroring peer_mode.go).
//   - Memory evidence store pre-seeded with KindFitness records for the active
//     strategy across multiple 10-minute replay windows, scoring low (0.2).
//   - The candidate (never executed) falls back to the prior (0.5) and wins
//     every comparison → win rate 1.0 → G2 passes → promoted.
//
// Reverse case (empty store): both strategies score the prior → every
// comparison is an exact tie → ties excluded from TotalComparisons (B-3) →
// TotalComparisons == 0 → G2 fail-closed → candidate NOT promoted.
func TestZeroLLMClosure_EndToEndPromotion(t *testing.T) {
	defer discardLogs()()

	// --- Positive case: pre-seeded evidence → candidate promoted ---
	t.Run("promoted_after_zero_llm_shadow", func(t *testing.T) {
		store := &memEvidenceStore{}
		ctx := context.Background()
		now := time.Now()

		// Pre-seed fitness records for the active strategy across 4 replay
		// windows. Each window is 10 minutes wide; records are placed at the
		// midpoint of each window so the scorer always finds evidence.
		span := replayWindowSpan
		activeID := "seed-active"
		for i := 0; i < 4; i++ {
			at := now.Add(-time.Duration(i)*span - span/2)
			_ = store.Append(ctx, fitnessRecord(activeID, 0.2, at))
		}

		// Build a system with memory strategy store, shadow eval enabled,
		// and zero-LLM posture (no cfg.Scorer, DeterministicScorerEnabled).
		base := &mutation.Strategy{ID: "base", Version: 1, Score: 50.0}
		cfg := DefaultSystemConfig()
		cfg.StrategyStore = NewMemoryStrategyStore(0)
		cfg.ShadowEvalConfig = ShadowEvaluationConfig{
			Enabled:    true,
			MinSamples: 4,
			MinWinRate: 0.55,
		}
		cfg.DeterministicScorerEnabled = true
		cfg.EnableDreamCycle = false
		cfg.EnableScheduler = false

		system, err := NewWiredEvolutionSystem(base, cfg)
		if err != nil {
			t.Fatalf("NewWiredEvolutionSystem: %v", err)
		}
		if system.ShadowEvaluator == nil {
			t.Fatal("shadow evaluator must be wired when ShadowEvalConfig.Enabled is true")
		}
		if system.Lifecycle == nil {
			t.Fatal("lifecycle must be wired when StrategyStore is set")
		}

		// Inject the ReplayScorer (zero-LLM evidence source, mirroring
		// peer_mode.go's wiring). The scorer reads the pre-seeded store.
		replay := NewReplayScorer(store, func() float64 { return 0.5 })
		if !replay.HasStore() {
			t.Fatal("ReplayScorer must report a store when one is wired")
		}
		system.ShadowEvaluator.SetShadowScorer(replay.Score)

		// Deploy a seed active strategy so the lifecycle is "born seeded"
		// and the first Submit runs gates (not seed promotion).
		if err := system.ActiveStrategyManager.Deploy(ctx, &mutation.Strategy{ID: activeID, Version: 1, Score: 60.0}); err != nil {
			t.Fatalf("Deploy seed: %v", err)
		}

		// Submit a candidate strategy.
		candidate := &mutation.Strategy{
			ID: "cand-v2", Version: 2, Score: 70.0,
			Params: map[string]any{"temperature": 0.7},
		}
		system.Lifecycle.Submit(ctx, candidate, 1)

		// Assert the candidate was promoted to active.
		snap := system.Lifecycle.Snapshot()
		if snap.State != "active" {
			t.Fatalf("expected state=active after promotion, got %q", snap.State)
		}
		if snap.ActiveID != candidate.ID {
			t.Fatalf("expected active strategy to be promoted candidate %q, got %q", candidate.ID, snap.ActiveID)
		}
		if snap.LastDecision != "promoted" {
			t.Fatalf("expected last decision 'promoted', got %q", snap.LastDecision)
		}

		// Verify the shadow gate had enough comparisons to judge.
		// The sampler's Prime gathered MinSamples comparisons; the G2 gate
		// should have seen them all.
		se := system.ShadowEvaluator
		_, report := se.ShouldDeploy()
		if report == nil || report.TotalComparisons < cfg.ShadowEvalConfig.MinSamples {
			t.Fatalf("shadow report: TotalComparisons=%d, want >=%d (report=%+v)",
				reportOrNil(report), cfg.ShadowEvalConfig.MinSamples, report)
		}
		if report.WinRate < cfg.ShadowEvalConfig.MinWinRate {
			t.Fatalf("shadow win rate %.2f below threshold %.2f", report.WinRate, cfg.ShadowEvalConfig.MinWinRate)
		}
	})

	// --- Reverse case: empty evidence store → fail-closed, not promoted ---
	t.Run("fail_closed_without_evidence", func(t *testing.T) {
		emptyStore := &memEvidenceStore{}
		ctx := context.Background()

		base := &mutation.Strategy{ID: "base", Version: 1, Score: 50.0}
		cfg := DefaultSystemConfig()
		cfg.StrategyStore = NewMemoryStrategyStore(0)
		cfg.ShadowEvalConfig = ShadowEvaluationConfig{
			Enabled:    true,
			MinSamples: 4,
			MinWinRate: 0.55,
		}
		cfg.DeterministicScorerEnabled = true
		cfg.EnableDreamCycle = false
		cfg.EnableScheduler = false

		system, err := NewWiredEvolutionSystem(base, cfg)
		if err != nil {
			t.Fatalf("NewWiredEvolutionSystem: %v", err)
		}

		// Inject ReplayScorer over an EMPTY evidence store. Every comparison
		// is cold-start prior-vs-prior → exact tie → excluded from
		// TotalComparisons → TotalComparisons == 0 → G2 fail-closed.
		replay := NewReplayScorer(emptyStore, func() float64 { return 0.5 })
		if !replay.HasStore() {
			t.Fatal("ReplayScorer must report a store when one is wired")
		}
		system.ShadowEvaluator.SetShadowScorer(replay.Score)

		seedID := "seed-active"
		if err := system.ActiveStrategyManager.Deploy(ctx, &mutation.Strategy{ID: seedID, Version: 1, Score: 60.0}); err != nil {
			t.Fatalf("Deploy seed: %v", err)
		}

		candidate := &mutation.Strategy{
			ID: "cand-v2", Version: 2, Score: 70.0,
			Params: map[string]any{"temperature": 0.7},
		}
		system.Lifecycle.Submit(ctx, candidate, 1)

		// Assert the candidate was NOT promoted — the active strategy is
		// still the seed.
		snap := system.Lifecycle.Snapshot()
		if snap.ActiveID != seedID {
			t.Fatalf("expected active strategy to remain seed %q, got %q (candidate was promoted despite empty evidence store)", seedID, snap.ActiveID)
		}

		// Lock the CONTRACT: the rejection must come from the G2 shadow gate's
		// fail-closed path, not from any arbitrary gate or a no-op Submit. The
		// evaluator that backs G2 must itself report fail-closed with an
		// all-tie evidence set (TotalComparisons == 0, TieCount == MinSamples).
		// (Review P2: ActiveID alone would pass if ANY gate rejected, hiding a
		// regression where G2 stopped being the decisive judge.)
		deploy, report := system.ShadowEvaluator.ShouldDeploy()
		if deploy {
			t.Fatal("shadow evaluator must fail-closed on an all-tie evidence set")
		}
		if report == nil || report.TotalComparisons != 0 || report.TieCount < cfg.ShadowEvalConfig.MinSamples {
			t.Fatalf("expected G2 fail-closed via all-tie exclusion, got report=%+v", report)
		}
	})
}

// reportOrNil returns the report's TotalComparisons, or -1 when report is nil.
func reportOrNil(r *ShadowReport) int {
	if r == nil {
		return -1
	}
	return r.TotalComparisons
}
