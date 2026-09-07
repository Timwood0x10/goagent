package evolution

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

func TestShadowEvaluator_New(t *testing.T) {
	t.Run("default_config", func(t *testing.T) {
		cfg := DefaultShadowEvaluationConfig()
		e := NewShadowEvaluator(cfg)
		if e == nil {
			t.Fatal("expected non-nil evaluator")
		}
		if e.minSamples != 10 {
			t.Errorf("expected minSamples=10, got %d", e.minSamples)
		}
		if e.minWinRate != 0.55 {
			t.Errorf("expected minWinRate=0.55, got %f", e.minWinRate)
		}
	})

	t.Run("zero_values_use_defaults", func(t *testing.T) {
		cfg := ShadowEvaluationConfig{
			MinSamples: 0,
			MinWinRate: 0,
		}
		e := NewShadowEvaluator(cfg)
		if e.minSamples != 10 {
			t.Errorf("expected minSamples default 10, got %d", e.minSamples)
		}
		if e.minWinRate != 0.55 {
			t.Errorf("expected minWinRate default 0.55, got %f", e.minWinRate)
		}
	})

	t.Run("custom_values", func(t *testing.T) {
		cfg := ShadowEvaluationConfig{
			MinSamples: 5,
			MinWinRate: 0.60,
		}
		e := NewShadowEvaluator(cfg)
		if e.minSamples != 5 {
			t.Errorf("expected minSamples=5, got %d", e.minSamples)
		}
		if e.minWinRate != 0.60 {
			t.Errorf("expected minWinRate=0.60, got %f", e.minWinRate)
		}
	})
}

func TestShadowEvaluator_StartShadow(t *testing.T) {
	e := NewShadowEvaluator(DefaultShadowEvaluationConfig())
	active := &mutation.Strategy{ID: "active-v1"}
	candidate := &mutation.Strategy{ID: "candidate-v2"}

	e.SetActiveStrategy(active)
	e.StartShadow(candidate)

	if e.ShadowStrategy() != candidate {
		t.Error("shadow strategy not set correctly")
	}
	if e.ActiveStrategy() != active {
		t.Error("active strategy not preserved")
	}
}

func TestShadowEvaluator_StartShadow_ResetsResults(t *testing.T) {
	e := NewShadowEvaluator(DefaultShadowEvaluationConfig())
	e.SetActiveStrategy(&mutation.Strategy{ID: "active"})
	e.StartShadow(&mutation.Strategy{ID: "candidate-1"})

	// Record some results.
	e.RecordResult(80, 90)
	e.RecordResult(85, 95)
	if len(e.Results()) != 2 {
		t.Errorf("expected 2 results, got %d", len(e.Results()))
	}

	// Start a new shadow evaluation; results should be reset.
	e.StartShadow(&mutation.Strategy{ID: "candidate-2"})
	if len(e.Results()) != 0 {
		t.Errorf("expected 0 results after reset, got %d", len(e.Results()))
	}
}

func TestShadowEvaluator_RecordResult(t *testing.T) {
	e := NewShadowEvaluator(DefaultShadowEvaluationConfig())
	e.SetActiveStrategy(&mutation.Strategy{ID: "active"})
	e.StartShadow(&mutation.Strategy{ID: "candidate"})

	e.RecordResult(80, 90)
	e.RecordResult(85, 95)
	e.RecordResult(90, 85) // Shadow loses this one.

	results := e.Results()
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	if !results[0].ShadowWon {
		t.Error("expected result[0] shadow to win (90 > 80)")
	}
	if !results[1].ShadowWon {
		t.Error("expected result[1] shadow to win (95 > 85)")
	}
	if results[2].ShadowWon {
		t.Error("expected result[2] shadow to lose (85 < 90)")
	}
}

func TestShadowEvaluator_ShouldDeploy_BelowMinSamples(t *testing.T) {
	e := NewShadowEvaluator(ShadowEvaluationConfig{
		Enabled:    true,
		MinSamples: 5,
		MinWinRate: 0.55,
	})
	e.SetActiveStrategy(&mutation.Strategy{ID: "active"})
	e.StartShadow(&mutation.Strategy{ID: "candidate"})

	// Record only 3 results (below min of 5).
	e.RecordResult(80, 90)
	e.RecordResult(85, 95)
	e.RecordResult(90, 85)

	deploy, report := e.ShouldDeploy()
	if deploy {
		t.Error("expected false deployment below min samples")
	}
	if report == nil {
		t.Fatal("expected non-nil report")
	}
	if !strings.Contains(report.Recommendation, "insufficient samples") {
		t.Errorf("expected 'insufficient samples' recommendation, got: %s", report.Recommendation)
	}
	if report.TotalComparisons != 3 {
		t.Errorf("expected 3 total comparisons, got %d", report.TotalComparisons)
	}
}

func TestShadowEvaluator_ShouldDeploy_AboveThreshold(t *testing.T) {
	e := NewShadowEvaluator(ShadowEvaluationConfig{
		Enabled:    true,
		MinSamples: 5,
		MinWinRate: 0.55,
	})
	e.SetActiveStrategy(&mutation.Strategy{ID: "active"})
	e.StartShadow(&mutation.Strategy{ID: "candidate"})

	// Record 5 results; shadow wins 4 (80% win rate, above 55% threshold).
	e.RecordResult(80, 95) // Shadow wins
	e.RecordResult(85, 90) // Shadow wins
	e.RecordResult(90, 92) // Shadow wins
	e.RecordResult(87, 91) // Shadow wins
	e.RecordResult(95, 85) // Shadow loses

	deploy, report := e.ShouldDeploy()
	if !deploy {
		t.Error("expected true deployment when win rate exceeds threshold")
	}
	if report == nil {
		t.Fatal("expected non-nil report")
	}
	if report.ShadowWins != 4 {
		t.Errorf("expected 4 shadow wins, got %d", report.ShadowWins)
	}
	if report.WinRate != 0.80 {
		t.Errorf("expected 0.80 win rate, got %f", report.WinRate)
	}
	if !strings.Contains(report.Recommendation, "recommend deployment") {
		t.Errorf("expected 'recommend deployment' recommendation, got: %s", report.Recommendation)
	}
}

func TestShadowEvaluator_ShouldDeploy_BelowThreshold(t *testing.T) {
	e := NewShadowEvaluator(ShadowEvaluationConfig{
		Enabled:    true,
		MinSamples: 5,
		MinWinRate: 0.55,
	})
	e.SetActiveStrategy(&mutation.Strategy{ID: "active"})
	e.StartShadow(&mutation.Strategy{ID: "candidate"})

	// Record 5 results; shadow wins only 2 (40% win rate, below 55% threshold).
	e.RecordResult(80, 75) // Shadow loses
	e.RecordResult(85, 82) // Shadow loses
	e.RecordResult(90, 95) // Shadow wins
	e.RecordResult(87, 85) // Shadow loses
	e.RecordResult(95, 98) // Shadow wins

	deploy, report := e.ShouldDeploy()
	if deploy {
		t.Error("expected false deployment when win rate below threshold")
	}
	if report == nil {
		t.Fatal("expected non-nil report")
	}
	if report.ShadowWins != 2 {
		t.Errorf("expected 2 shadow wins, got %d", report.ShadowWins)
	}
	if !strings.Contains(report.Recommendation, "keep active") {
		t.Errorf("expected 'keep active' recommendation, got: %s", report.Recommendation)
	}
}

func TestShadowEvaluator_ShouldDeploy_NoResults(t *testing.T) {
	e := NewShadowEvaluator(DefaultShadowEvaluationConfig())
	e.SetActiveStrategy(&mutation.Strategy{ID: "active"})
	e.StartShadow(&mutation.Strategy{ID: "candidate"})

	deploy, report := e.ShouldDeploy()
	if deploy {
		t.Error("expected false deployment with no results")
	}
	if report != nil {
		t.Error("expected nil report with no results")
	}
}

// TestShadowEvaluator_StrictVsLoose locks the two-contract split (review:
// DreamCycle and the lifecycle G2 gate read the same evaluator with opposite
// insufficient-sample semantics — now explicit):
//   - Strict (ShouldDeploy, the G2 gate): insufficient samples → REJECT.
//   - Loose (ShouldDeployLoose, DreamCycle deploy path): insufficient
//     samples → defer to the deployer (true, "cannot judge yet").
//   - Enough samples → identical verdicts.
func TestShadowEvaluator_StrictVsLoose(t *testing.T) {
	e := NewShadowEvaluator(ShadowEvaluationConfig{Enabled: true, MinSamples: 3, MinWinRate: 0.5})

	// 2 comparisons (< MinSamples): strict rejects, loose defers.
	e.RecordResult(0.0, 1.0)
	e.RecordResult(0.0, 1.0)

	strictPass, strictReport := e.ShouldDeploy()
	assert.False(t, strictPass, "strict: insufficient samples must reject")
	assert.NotNil(t, strictReport)
	assert.Contains(t, strictReport.Recommendation, "fail-closed")

	loosePass, looseReport := e.ShouldDeployLoose()
	assert.True(t, loosePass, "loose: insufficient samples must defer to the deployer")
	assert.NotNil(t, looseReport)

	// Enough samples, shadow wins → identical verdicts.
	e.RecordResult(0.0, 1.0)
	strictPass, _ = e.ShouldDeploy()
	loosePass, _ = e.ShouldDeployLoose()
	assert.True(t, strictPass)
	assert.True(t, loosePass)
}

func TestShadowEvaluator_Reset(t *testing.T) {
	e := NewShadowEvaluator(DefaultShadowEvaluationConfig())
	e.SetActiveStrategy(&mutation.Strategy{ID: "active"})
	e.StartShadow(&mutation.Strategy{ID: "candidate"})
	e.RecordResult(80, 90)

	e.Reset()
	if e.ShadowStrategy() != nil {
		t.Error("expected nil shadow strategy after reset")
	}
	if len(e.Results()) != 0 {
		t.Error("expected empty results after reset")
	}
}

func TestShadowEvaluator_ThreadSafety(t *testing.T) {
	e := NewShadowEvaluator(ShadowEvaluationConfig{
		MinSamples: 100,
		MinWinRate: 0.55,
	})
	e.SetActiveStrategy(&mutation.Strategy{ID: "active"})
	e.StartShadow(&mutation.Strategy{ID: "candidate"})

	var wg sync.WaitGroup
	n := 50

	// Concurrently record results.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(score float64) {
			defer wg.Done()
			e.RecordResult(80, score)
		}(float64(70 + i%30))
	}

	// Concurrently read results.
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = e.Results()
			_, _ = e.ShouldDeploy()
		}()
	}

	wg.Wait()

	results := e.Results()
	if len(results) != n {
		t.Errorf("expected %d results, got %d", n, len(results))
	}
}

func TestShadowEvaluator_SetActiveStrategy(t *testing.T) {
	e := NewShadowEvaluator(DefaultShadowEvaluationConfig())
	s1 := &mutation.Strategy{ID: "v1"}
	s2 := &mutation.Strategy{ID: "v2"}

	e.SetActiveStrategy(s1)
	if e.ActiveStrategy() != s1 {
		t.Error("expected s1 as active strategy")
	}

	e.SetActiveStrategy(s2)
	if e.ActiveStrategy() != s2 {
		t.Error("expected s2 as active strategy after change")
	}
}

func TestShadowEvaluator_ShadowWonTie(t *testing.T) {
	e := NewShadowEvaluator(DefaultShadowEvaluationConfig())
	e.SetActiveStrategy(&mutation.Strategy{ID: "active"})
	e.StartShadow(&mutation.Strategy{ID: "candidate"})

	// Equal scores: shadow should NOT be considered as winning.
	e.RecordResult(80, 80)
	results := e.Results()
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ShadowWon {
		t.Error("expected shadow not to win on equal scores")
	}
}

// TestShadowEvaluator_TiesNotCountedAsSamples pins the B-3 fix: an exact tie
// (shadowScore == activeScore) carries no information about which strategy is
// better, so it must NOT count toward TotalComparisons or dilute the win rate.
// A cold-start prior-vs-prior comparison is exactly such a tie, so this is the
// deadlock-removal contract: ties are neither wins nor samples.
func TestShadowEvaluator_TiesNotCountedAsSamples(t *testing.T) {
	e := NewShadowEvaluator(ShadowEvaluationConfig{Enabled: true, MinSamples: 3, MinWinRate: 0.55})
	e.SetActiveStrategy(&mutation.Strategy{ID: "active"})
	e.StartShadow(&mutation.Strategy{ID: "candidate"})

	// Two decisive shadow wins + three ties. TotalComparisons must count only
	// the decisive wins (2), not the 5 recorded comparisons.
	e.RecordResult(0.2, 0.8) // shadow wins
	e.RecordResult(0.5, 0.5) // tie (e.g. both cold-start prior)
	e.RecordResult(0.3, 0.7) // shadow wins
	e.RecordResult(0.5, 0.5) // tie
	e.RecordResult(0.5, 0.5) // tie

	// Recorded raw results still include the ties (observability preserved).
	if got := len(e.Results()); got != 5 {
		t.Fatalf("raw results = %d, want 5 (ties recorded for observability)", got)
	}

	// Strict gate: TotalComparisons is decisive-only → 2 < MinSamples(3) →
	// fail-closed (the gate cannot vouch for the candidate on 2 samples).
	deploy, report := e.ShouldDeploy()
	if deploy {
		t.Fatal("2 decisive samples below MinSamples(3) must fail-closed")
	}
	if report == nil {
		t.Fatal("expected a non-nil report")
	}
	if report.TotalComparisons != 2 {
		t.Fatalf("TotalComparisons = %d, want 2 (ties excluded from the sample count)", report.TotalComparisons)
	}
	if report.ShadowWins != 2 {
		t.Fatalf("ShadowWins = %d, want 2", report.ShadowWins)
	}
	if report.TieCount != 3 {
		t.Fatalf("TieCount = %d, want 3 (ties reported separately)", report.TieCount)
	}
	if report.WinRate != 1.0 {
		t.Fatalf("WinRate = %.2f, want 1.0 (2/2 decisive)", report.WinRate)
	}
}

// TestShadowEvaluator_AllTiesNoDecisiveEvidence pins the P0-3 contract:
// when every comparison is a tie (the empty-evidence-store / full-cold-start
// case), there is no DECISIVE evidence — but the sample was still gathered.
// ShouldDeploy must fail-closed with a NON-NIL report (TieCount > 0,
// TotalComparisons == 0), so a caller can distinguish "no comparisons" (nil)
// from "comparisons gathered but all were ties". The loose contract must NOT
// defer on an all-tie MinSamples wall: enough raw comparisons that say nothing
// is a REJECTION, never a flip to "proceed" (review P0-3).
func TestShadowEvaluator_AllTiesNoDecisiveEvidence(t *testing.T) {
	e := NewShadowEvaluator(ShadowEvaluationConfig{Enabled: true, MinSamples: 3, MinWinRate: 0.55})
	e.SetActiveStrategy(&mutation.Strategy{ID: "active"})
	e.StartShadow(&mutation.Strategy{ID: "candidate"})

	for i := 0; i < 5; i++ {
		e.RecordResult(0.5, 0.5) // all cold-start prior-vs-prior ties
	}

	// Strict gate: fail-closed, non-nil report distinguishing "all ties" from
	// "no evidence".
	deploy, report := e.ShouldDeploy()
	if deploy {
		t.Fatal("all-tie evidence must fail-closed")
	}
	if report == nil {
		t.Fatal("all-tie evidence must report a non-nil report (distinguish from 'no comparisons')")
	}
	if report.TotalComparisons != 0 {
		t.Fatalf("TotalComparisons = %d, want 0 (ties are not decisive samples)", report.TotalComparisons)
	}
	if report.TieCount != 5 {
		t.Fatalf("TieCount = %d, want 5", report.TieCount)
	}

	// Loose contract: 5 raw comparisons >= MinSamples(3), zero decisive →
	// REJECT (do not defer, do not proceed). DreamCycle reads this via
	// shouldDeploy==false after the raw-count insufficiency check.
	looseDeploy, looseReport := e.ShouldDeployLoose()
	if looseDeploy {
		t.Fatalf("loose contract must reject an all-tie MinSamples wall (got deploy=true), report=%+v", looseReport)
	}
	if looseReport == nil {
		t.Fatal("loose contract must return a report for an all-tie wall")
	}
	if looseReport.TieCount != 5 {
		t.Fatalf("loose TieCount = %d, want 5", looseReport.TieCount)
	}
}

// TestShadowEvaluator_NearTieUsesSamePredicateEverywhere pins the review-P0
// fix: the decisive-sample filter and ShadowWon must use the SAME tie
// predicate (isTie), not `==` in one place and isTie in the other.
//
// A pair whose difference is within shadowTieEpsilon is a tie for ShadowWon.
// If the filter compared exactly, such a pair would be "decisive" — entering
// TotalComparisons while never entering ShadowWins — so every near-tie would
// be silently counted as a LOSS and drag the win rate below MinWinRate. That
// is the mirror image of the P1-3 false-positive: a false NEGATIVE verdict
// built from comparisons that carry no information.
func TestShadowEvaluator_NearTieUsesSamePredicateEverywhere(t *testing.T) {
	e := NewShadowEvaluator(ShadowEvaluationConfig{Enabled: true, MinSamples: 2, MinWinRate: 0.55})
	e.SetActiveStrategy(&mutation.Strategy{ID: "active"})
	e.StartShadow(&mutation.Strategy{ID: "candidate"})

	// Two decisive shadow wins, then three near-ties (micro-drift of the
	// attribution-derived prior between the two scorer calls). The near-ties
	// straddle zero in BOTH directions, which is exactly why they must not be
	// scored: the direction is jitter, not signal.
	e.RecordResult(0.2, 0.8)
	e.RecordResult(0.3, 0.7)
	e.RecordResult(0.5, 0.5+shadowTieEpsilon/2)
	e.RecordResult(0.5, 0.5-shadowTieEpsilon/2)
	e.RecordResult(0.5, 0.5+shadowTieEpsilon)

	deploy, report := e.ShouldDeploy()
	if report == nil {
		t.Fatal("expected a non-nil report")
	}
	if report.TotalComparisons != 2 {
		t.Fatalf("TotalComparisons = %d, want 2 (near-ties are not decisive samples)", report.TotalComparisons)
	}
	if report.TieCount != 3 {
		t.Fatalf("TieCount = %d, want 3 (near-ties counted as ties)", report.TieCount)
	}
	// The whole point: without the shared predicate the win rate would be
	// 2/5 = 0.4 < 0.55 and the candidate would be rejected on jitter.
	if report.WinRate != 1.0 {
		t.Fatalf("WinRate = %.4f, want 1.0 (2/2 decisive, near-ties excluded)", report.WinRate)
	}
	if !deploy {
		t.Fatalf("2 decisive wins at MinSamples(2) must pass, got reject: %s", report.Recommendation)
	}
}

// TestShadowEvaluator_NoComparisonsNilReport pins the "no evidence at all"
// contract: when nothing was recorded, ShouldDeploy returns a nil report so a
// caller can tell "no comparisons gathered" from "all ties" (the P0-3
// distinction that prevents an all-tie wall from being misread as a deferral).
func TestShadowEvaluator_NoComparisonsNilReport(t *testing.T) {
	e := NewShadowEvaluator(ShadowEvaluationConfig{Enabled: true, MinSamples: 3, MinWinRate: 0.55})
	e.SetActiveStrategy(&mutation.Strategy{ID: "active"})
	e.StartShadow(&mutation.Strategy{ID: "candidate"})

	if deploy, report := e.ShouldDeploy(); deploy || report != nil {
		t.Fatalf("no comparisons: strict must be fail-closed with nil report, got %v, %+v", deploy, report)
	}
	if deploy, report := e.ShouldDeployLoose(); deploy || report != nil {
		t.Fatalf("no comparisons: loose must report nil (defer), got %v, %+v", deploy, report)
	}
}

func TestShadowEvaluator_ResultsReturnsCopy(t *testing.T) {
	e := NewShadowEvaluator(DefaultShadowEvaluationConfig())
	e.SetActiveStrategy(&mutation.Strategy{ID: "active"})
	e.StartShadow(&mutation.Strategy{ID: "candidate"})

	e.RecordResult(80, 90)
	e.RecordResult(85, 95)

	results := e.Results()
	resultsCopy := make([]ShadowComparison, len(results))
	copy(resultsCopy, results)

	// Modify the returned copy.
	resultsCopy[0].ShadowWon = !resultsCopy[0].ShadowWon
	resultsCopy[0].ActiveScore = 999

	// Original should be unchanged.
	original := e.Results()
	if original[0].ActiveScore == 999 {
		t.Error("Results() should return a copy, not a reference")
	}
}

func TestShadowEvaluator_TimestampsAreSet(t *testing.T) {
	e := NewShadowEvaluator(DefaultShadowEvaluationConfig())
	e.SetActiveStrategy(&mutation.Strategy{ID: "active"})
	e.StartShadow(&mutation.Strategy{ID: "candidate"})

	before := time.Now()
	e.RecordResult(80, 90)
	after := time.Now()

	results := e.Results()
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	ts := results[0].Timestamp
	if ts.Before(before) || ts.After(after) {
		t.Error("timestamp should be between before and after")
	}
}
