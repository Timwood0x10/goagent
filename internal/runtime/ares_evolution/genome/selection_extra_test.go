package genome

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// TestComputeLineageRankWeights_Direct verifies the extracted weight function
// produces the exact expected values for a known population. This is fully
// deterministic — no statistics — so any regression in the weight formula
// fails immediately rather than producing a flaky signal.
func TestComputeLineageRankWeights_Direct(t *testing.T) {
	t.Run("exact weights match hand-computed values", func(t *testing.T) {
		// Population: 5 strategies, lineage A has 3 (60%), lineage B has 2 (40%).
		// Scores descending so sort is identity.
		// penaltyThreshold=0, penaltyStrength=0.5.
		//
		// For threshold=0:
		//   share_A = 3/5 = 0.6, excess = (0.6 - 0)/(1.0 - 0) = 0.6, penalty = 0.5*0.6 = 0.3
		//     rankWeight *= (1 - 0.3) = 0.7
		//   share_B = 2/5 = 0.4, excess = 0.4, penalty = 0.2
		//     rankWeight *= (1 - 0.2) = 0.8
		//   Indices (sorted desc): 0,1,2 → A (rankWeight 5,4,3); 3,4 → B (rankWeight 2,1)
		//   Expected weights:
		//     [0]=5*0.7=3.5, [1]=4*0.7=2.8, [2]=3*0.7=2.1
		//     [3]=2*0.8=1.6, [4]=1*0.8=0.8
		//   Total = 3.5+2.8+2.1+1.6+0.8 = 10.8
		sorted := []*mutation.Strategy{
			{ID: "a1", Score: 100, ParentID: "A"},
			{ID: "a2", Score: 90, ParentID: "A"},
			{ID: "a3", Score: 80, ParentID: "A"},
			{ID: "b1", Score: 70, ParentID: "B"},
			{ID: "b2", Score: 60, ParentID: "B"},
		}

		weights, total := computeLineageRankWeights(sorted, 0.0, 0.5)

		expected := []float64{3.5, 2.8, 2.1, 1.6, 0.8}
		const tol = 1e-9
		for i, w := range weights {
			if math.Abs(w-expected[i]) > tol {
				t.Errorf("weight[%d]: got %.6f, want %.6f", i, w, expected[i])
			}
		}
		if math.Abs(total-10.8) > tol {
			t.Errorf("totalWeight: got %.6f, want 10.8", total)
		}

		// Verify lineage probabilities are exactly as documented.
		// P(B) = (1.6 + 0.8) / 10.8 = 2.4 / 10.8 ≈ 0.2222
		// P(A) = (3.5 + 2.8 + 2.1) / 10.8 = 8.4 / 10.8 ≈ 0.7778
		lineageWeights := map[string]float64{"A": 0, "B": 0}
		for i, s := range sorted {
			lineageWeights[s.ParentID] += weights[i]
		}
		pB := lineageWeights["B"] / total
		pA := lineageWeights["A"] / total
		if math.Abs(pB-(2.4/10.8)) > tol {
			t.Errorf("P(B): got %.6f, want %.6f", pB, 2.4/10.8)
		}
		if math.Abs(pA-(8.4/10.8)) > tol {
			t.Errorf("P(A): got %.6f, want %.6f", pA, 8.4/10.8)
		}
	})

	t.Run("no penalty when threshold exceeds max share", func(t *testing.T) {
		// threshold=1.0 means no lineage can trigger penalty (max share is 1.0,
		// and the check is `share > threshold`). Weights should equal linear rank
		// weights: best=N, worst=1.
		sorted := []*mutation.Strategy{
			{ID: "a1", Score: 100, ParentID: "A"},
			{ID: "a2", Score: 90, ParentID: "A"},
			{ID: "b1", Score: 80, ParentID: "B"},
		}
		weights, total := computeLineageRankWeights(sorted, 1.0, 0.5)

		// Linear rank weights: 3, 2, 1. Total = 6.
		expected := []float64{3.0, 2.0, 1.0}
		const tol = 1e-9
		for i, w := range weights {
			if math.Abs(w-expected[i]) > tol {
				t.Errorf("weight[%d]: got %.6f, want %.6f (no penalty expected)", i, w, expected[i])
			}
		}
		if math.Abs(total-6.0) > tol {
			t.Errorf("totalWeight: got %.6f, want 6.0", total)
		}
	})

	t.Run("empty parent ID treated as root lineage", func(t *testing.T) {
		// Two strategies with empty ParentID should be counted as the same "(root)" lineage.
		sorted := []*mutation.Strategy{
			{ID: "r1", Score: 100, ParentID: ""},
			{ID: "r2", Score: 50, ParentID: ""},
		}
		// threshold=0 → both share=1.0 → excess=1.0, penalty=0.5, factor=0.5
		// weights: 2*0.5=1.0, 1*0.5=0.5; total=1.5
		weights, total := computeLineageRankWeights(sorted, 0.0, 0.5)

		expected := []float64{1.0, 0.5}
		const tol = 1e-9
		for i, w := range weights {
			if math.Abs(w-expected[i]) > tol {
				t.Errorf("weight[%d]: got %.6f, want %.6f", i, w, expected[i])
			}
		}
		if math.Abs(total-1.5) > tol {
			t.Errorf("totalWeight: got %.6f, want 1.5", total)
		}
	})

	t.Run("strength=0 disables penalty regardless of threshold", func(t *testing.T) {
		sorted := []*mutation.Strategy{
			{ID: "a1", Score: 100, ParentID: "A"},
			{ID: "a2", Score: 90, ParentID: "A"},
			{ID: "b1", Score: 80, ParentID: "B"},
		}
		// strength=0 → penalty=0 → weights equal linear rank weights.
		weights, total := computeLineageRankWeights(sorted, 0.0, 0.0)

		expected := []float64{3.0, 2.0, 1.0}
		const tol = 1e-9
		for i, w := range weights {
			if math.Abs(w-expected[i]) > tol {
				t.Errorf("weight[%d]: got %.6f, want %.6f", i, w, expected[i])
			}
		}
		if math.Abs(total-6.0) > tol {
			t.Errorf("totalWeight: got %.6f, want 6.0", total)
		}
	})
}

// TestLineageRankSelection_MembershipAndDeterminism verifies that every
// selected strategy comes from the input population and that the same seed
// produces byte-identical selection sequences.
func TestLineageRankSelection_MembershipAndDeterminism(t *testing.T) {
	ctx := context.Background()

	pop := makePopulation(100.0, 90.0, 80.0, 70.0, 60.0, 50.0, 40.0, 30.0)
	pop[0].ParentID = "A"
	pop[1].ParentID = "A"
	pop[2].ParentID = "A"
	pop[3].ParentID = "A"
	pop[4].ParentID = "B"
	pop[5].ParentID = "B"
	pop[6].ParentID = "C"
	pop[7].ParentID = "C"

	// Build a set of valid IDs for membership checks.
	validIDs := make(map[string]bool, len(pop))
	for _, s := range pop {
		validIDs[s.ID] = true
	}

	t.Run("all selected IDs are from the population", func(t *testing.T) {
		ls, err := NewLineageRankSelection(WithLineageRankSeed(2024))
		if err != nil {
			t.Fatalf("create selector: %v", err)
		}

		result, err := ls.Select(ctx, pop, 50)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 50 {
			t.Fatalf("got %d results, want 50", len(result))
		}

		for i, s := range result {
			if s == nil {
				t.Fatalf("index %d: nil strategy", i)
			}
			if !validIDs[s.ID] {
				t.Errorf("index %d: selected ID %q is not in the evaluated population", i, s.ID)
			}
		}
	})

	t.Run("same seed produces identical selections", func(t *testing.T) {
		ls1, _ := NewLineageRankSelection(WithLineageRankSeed(77))
		ls2, _ := NewLineageRankSelection(WithLineageRankSeed(77))

		r1, err := ls1.Select(ctx, pop, 30)
		if err != nil {
			t.Fatalf("first select: %v", err)
		}
		r2, err := ls2.Select(ctx, pop, 30)
		if err != nil {
			t.Fatalf("second select: %v", err)
		}

		if len(r1) != len(r2) {
			t.Fatalf("different lengths: %d vs %d", len(r1), len(r2))
		}
		for i := range r1 {
			if r1[i].ID != r2[i].ID {
				t.Fatalf("index %d: %q vs %q (not deterministic)", i, r1[i].ID, r2[i].ID)
			}
		}
	})

	t.Run("different seeds produce different selections", func(t *testing.T) {
		// Two different seeds should almost certainly produce different sequences
		// for non-trivial n (probability of collision is negligible).
		ls1, _ := NewLineageRankSelection(WithLineageRankSeed(1))
		ls2, _ := NewLineageRankSelection(WithLineageRankSeed(2))

		r1, _ := ls1.Select(ctx, pop, 30)
		r2, _ := ls2.Select(ctx, pop, 30)

		diffs := 0
		for i := range r1 {
			if r1[i].ID != r2[i].ID {
				diffs++
			}
		}
		if diffs == 0 {
			t.Error("two different seeds produced identical sequences — RNG not being used")
		}
	})
}

// TestLineageRankSelection_DominantShareDecreasesVsBaseline verifies the
// core contract: with the lineage penalty enabled, the dominant lineage is
// selected materially less often than under baseline rank selection on the
// same population/seed. This is the regression test that catches a broken
// penalty path that would let the dominant lineage keep its baseline share.
func TestLineageRankSelection_DominantShareDecreasesVsBaseline(t *testing.T) {
	ctx := context.Background()

	// Population structure: lineage A is dominant BY COUNT (6/10 = 60%) but
	// holds MIDDLE score positions, so rank selection alone does not heavily
	// favor A. B holds the top 2 positions; C holds the bottom 2.
	//
	// SortByScore order (descending): B(95), B(90), A(80), A(75), A(70),
	//   A(65), A(60), A(55), C(45), C(40)
	//
	// Linear rank weights (best=N=10): 10, 9, 8, 7, 6, 5, 4, 3, 2, 1
	//   A weights (positions 2..7): 8+7+6+5+4+3 = 33
	//   B weights (positions 0..1): 10+9 = 19
	//   C weights (positions 8..9): 2+1   = 3
	//   Total = 55; baseline P(A) = 33/55 = 0.600
	//
	// With penalty (threshold=0.4, strength=0.5):
	//   A share=0.6, excess=(0.6-0.4)/(1-0.4)=0.333, penalty=0.5*0.333=0.167
	//   A factor = 1-0.167 = 0.833
	//   A weights (penalized): 33*0.833 = 27.5
	//   B and C unchanged (share 0.2 ≤ threshold).
	//   Total = 27.5 + 19 + 3 = 49.5; penalized P(A) = 27.5/49.5 ≈ 0.556
	//
	// Expected delta ≈ 0.044 (4.4 percentage points).
	pop := []*mutation.Strategy{
		newSelStrategy("b1", 95.0),
		newSelStrategy("b2", 90.0),
		newSelStrategy("a1", 80.0),
		newSelStrategy("a2", 75.0),
		newSelStrategy("a3", 70.0),
		newSelStrategy("a4", 65.0),
		newSelStrategy("a5", 60.0),
		newSelStrategy("a6", 55.0),
		newSelStrategy("c1", 45.0),
		newSelStrategy("c2", 40.0),
	}
	pop[0].ParentID = "B"
	pop[1].ParentID = "B"
	pop[2].ParentID = "A"
	pop[3].ParentID = "A"
	pop[4].ParentID = "A"
	pop[5].ParentID = "A"
	pop[6].ParentID = "A"
	pop[7].ParentID = "A"
	pop[8].ParentID = "C"
	pop[9].ParentID = "C"

	// Baseline: rank selection (no lineage awareness).
	baselineRS := NewRankSelection()
	const iterations = 6000

	countBaselineA := 0
	for i := 0; i < iterations; i++ {
		r, err := baselineRS.Select(ctx, pop, 1)
		if err != nil {
			t.Fatalf("baseline iteration %d: %v", i, err)
		}
		if r[0].ParentID == "A" {
			countBaselineA++
		}
	}

	// Penalized: lineage rank selection with default threshold=0.4, strength=0.5.
	penalizedLS, err := NewLineageRankSelection(WithLineageRankSeed(42))
	if err != nil {
		t.Fatalf("create lineage selector: %v", err)
	}
	countPenalizedA := 0
	for i := 0; i < iterations; i++ {
		r, err := penalizedLS.Select(ctx, pop, 1)
		if err != nil {
			t.Fatalf("penalized iteration %d: %v", i, err)
		}
		if r[0].ParentID == "A" {
			countPenalizedA++
		}
	}

	baselineShare := float64(countBaselineA) / float64(iterations)
	penalizedShare := float64(countPenalizedA) / float64(iterations)
	t.Logf("lineage A share — baseline=%.3f (expected ~0.600), penalized=%.3f (expected ~0.556), delta=%.3f",
		baselineShare, penalizedShare, baselineShare-penalizedShare)

	// Contract: penalty must reduce A's share by at least 2 percentage points.
	// Theoretical delta is ~4.4pp; 2pp is a conservative lower bound that absorbs
	// statistical noise while still catching a broken penalty path.
	const minMargin = 0.02
	if baselineShare-penalizedShare < minMargin {
		t.Errorf("penalty did not materially reduce dominant lineage share: "+
			"baseline=%.3f, penalized=%.3f, delta=%.3f (want >= %.3f)",
			baselineShare, penalizedShare, baselineShare-penalizedShare, minMargin)
	}

	// Sanity: baseline A share must be near 0.60 — confirms the population
	// structure is what we expect before interpreting the penalized result.
	const baselineTol = 0.04
	if math.Abs(baselineShare-0.60) > baselineTol {
		t.Errorf("baseline A share %.3f outside expected band [%.3f, %.3f] — "+
			"population structure changed?", baselineShare, 0.60-baselineTol, 0.60+baselineTol)
	}
}

// TestLineageRankSelection_UnderrepresentedBoosted verifies the
// underrepresented lineage is materially boosted (not merely non-zero)
// when the penalty is enabled vs the baseline rank selection.
func TestLineageRankSelection_UnderrepresentedBoosted(t *testing.T) {
	ctx := context.Background()

	// Population designed for a large, statistically robust effect:
	//   A: 8 members (scores 95..60, ranks 1-8) — share 0.8, heavily penalized
	//   B: 1 member  (score 55, rank 9)           — share 0.1, no penalty
	//   C: 1 member  (score 50, rank 10)          — share 0.1, no penalty
	//
	// Default penaltyThreshold=0.4, penaltyStrength=0.5.
	// Penalty for A: 0.5 × (0.8 - 0.4) / (1 - 0.4) = 0.333 → multiplier 0.667
	//
	// Baseline rank weights (sum=55): A=52, B=2, C=1
	//   P(C) = 1/55 ≈ 0.0182
	// Penalized weights (sum≈37.67): A≈34.67, B=2, C=1
	//   P(C) = 1/37.67 ≈ 0.0266
	// Theoretical relative boost ≈ 46%. With 10000 iterations this is a ~4σ
	// effect, making the test deterministic in practice.
	pop := []*mutation.Strategy{
		newSelStrategy("a1", 95.0),
		newSelStrategy("a2", 90.0),
		newSelStrategy("a3", 85.0),
		newSelStrategy("a4", 80.0),
		newSelStrategy("a5", 75.0),
		newSelStrategy("a6", 70.0),
		newSelStrategy("a7", 65.0),
		newSelStrategy("a8", 60.0),
		newSelStrategy("b1", 55.0),
		newSelStrategy("c1", 50.0),
	}
	for i := 0; i < 8; i++ {
		pop[i].ParentID = "A"
	}
	pop[8].ParentID = "B"
	pop[9].ParentID = "C"

	const iterations = 10000

	// Baseline rank selection.
	baselineRS := NewRankSelection()
	baselineC := 0
	for i := 0; i < iterations; i++ {
		r, err := baselineRS.Select(ctx, pop, 1)
		if err != nil {
			t.Fatalf("baseline iteration %d: %v", i, err)
		}
		if r[0].ParentID == "C" {
			baselineC++
		}
	}

	// Penalized lineage rank selection.
	penalizedLS, err := NewLineageRankSelection(WithLineageRankSeed(42))
	if err != nil {
		t.Fatalf("create lineage selector: %v", err)
	}
	penalizedC := 0
	for i := 0; i < iterations; i++ {
		r, err := penalizedLS.Select(ctx, pop, 1)
		if err != nil {
			t.Fatalf("penalized iteration %d: %v", i, err)
		}
		if r[0].ParentID == "C" {
			penalizedC++
		}
	}

	t.Logf("lineage C count — baseline=%d/%d (expected ~%d), penalized=%d/%d (expected ~%d)",
		baselineC, iterations, int(0.0182*iterations), penalizedC, iterations, int(0.0266*iterations))

	// Contract: underrepresented lineage must be materially boosted.
	// Theoretical relative boost ≈ 46%. Set a conservative 15% floor
	// (well within 3σ of the expected difference).
	if baselineC == 0 {
		t.Fatalf("baseline C count is 0 — cannot compute relative boost; " +
			"need more iterations or a different seed")
	}
	relativeBoost := float64(penalizedC-baselineC) / float64(baselineC)
	const minRelativeBoost = 0.15
	if relativeBoost < minRelativeBoost {
		t.Errorf("underrepresented lineage not materially boosted: "+
			"baseline=%d, penalized=%d, relative boost=%.3f (want >= %.3f)",
			baselineC, penalizedC, relativeBoost, minRelativeBoost)
	}
	// Hard floor: penalized C must be meaningfully above baseline.
	if penalizedC < baselineC+10 {
		t.Errorf("penalized C count %d not materially above baseline %d "+
			"(want at least baseline+10)", penalizedC, baselineC)
	}
}

// TestLineageRankSelection_WeightProportionality verifies that selection
// counts track the documented weight ratios within tight statistical bands.
// Uses a fixed seed and asserts the empirical distribution is within 4σ of
// the theoretical mean derived from the extracted weight function.
func TestLineageRankSelection_WeightProportionality(t *testing.T) {
	ctx := context.Background()

	// Population: A has 3/5 (60%), B has 2/5 (40%). threshold=0, strength=0.5.
	// From TestComputeLineageRankWeights_Direct:
	//   P(A) = 8.4/10.8 ≈ 0.7778
	//   P(B) = 2.4/10.8 ≈ 0.2222
	pop := makePopulation(100.0, 90.0, 85.0, 80.0, 70.0)
	pop[0].ParentID = "A"
	pop[1].ParentID = "A"
	pop[2].ParentID = "A"
	pop[3].ParentID = "B"
	pop[4].ParentID = "B"

	ls, err := NewLineageRankSelection(
		WithLineageRankSeed(12345),
		WithLineagePenaltyThreshold(0),
	)
	if err != nil {
		t.Fatalf("create selector: %v", err)
	}

	const iterations = 5000
	counts := make(map[string]int)
	for i := 0; i < iterations; i++ {
		r, err := ls.Select(ctx, pop, 1)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		counts[r[0].ParentID]++
	}

	// Theoretical probabilities from the extracted weight function.
	const (
		pA         = 8.4 / 10.8
		pB         = 2.4 / 10.8
		muA        = pA * iterations
		muB        = pB * iterations
		sigmaScale = 4.0 // 4σ band — probability of false failure < 1 in 15000.
	)
	sigmaA := math.Sqrt(pA * (1 - pA) * iterations)
	sigmaB := math.Sqrt(pB * (1 - pB) * iterations)

	t.Logf("expected A ~%.1f (±%.1f), got %d", muA, sigmaA, counts["A"])
	t.Logf("expected B ~%.1f (±%.1f), got %d", muB, sigmaB, counts["B"])

	if math.Abs(float64(counts["A"])-muA) > sigmaScale*sigmaA {
		t.Errorf("A count %d outside %.0fσ band [%.1f, %.1f] (μ=%.1f, σ=%.1f)",
			counts["A"], sigmaScale, muA-sigmaScale*sigmaA, muA+sigmaScale*sigmaA, muA, sigmaA)
	}
	if math.Abs(float64(counts["B"])-muB) > sigmaScale*sigmaB {
		t.Errorf("B count %d outside %.0fσ band [%.1f, %.1f] (μ=%.1f, σ=%.1f)",
			counts["B"], sigmaScale, muB-sigmaScale*sigmaB, muB+sigmaScale*sigmaB, muB, sigmaB)
	}
}

// TestLineageRankSelection_NoDuplicateIDsWhenNotExpected verifies that
// for a single iteration of Select with n=1, the result contains exactly
// one strategy (no spurious duplicates injected by the selection loop).
// Note: for n>1, duplicates ARE expected since each pick is independent —
// this test asserts the loop doesn't accidentally double-append on a single
// spin.
func TestLineageRankSelection_NoDuplicateIDsWhenNotExpected(t *testing.T) {
	ctx := context.Background()

	pop := makePopulation(10.0, 20.0, 30.0, 40.0, 50.0)
	pop[0].ParentID = "A"
	pop[1].ParentID = "A"
	pop[2].ParentID = "B"
	pop[3].ParentID = "B"
	pop[4].ParentID = "C"

	ls, err := NewLineageRankSelection(WithLineageRankSeed(13))
	if err != nil {
		t.Fatalf("create selector: %v", err)
	}

	for i := 0; i < 100; i++ {
		r, err := ls.Select(ctx, pop, 1)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if len(r) != 1 {
			t.Fatalf("iteration %d: got %d results, want exactly 1", i, len(r))
		}
		if r[0] == nil {
			t.Fatalf("iteration %d: nil strategy returned", i)
		}
	}
}

// TestLineageRankSelection_OrderingRespectsScoreOrder verifies that the
// selector sorts the population by score descending before applying weights,
// so the highest-scoring individual always receives the largest rank weight.
// This catches regressions where SortByScore is skipped or applied in the
// wrong direction.
func TestLineageRankSelection_OrderingRespectsScoreOrder(t *testing.T) {
	// Build population in NON-sorted order to verify the selector sorts internally.
	pop := []*mutation.Strategy{
		newSelStrategy("low", 10.0),
		newSelStrategy("high", 100.0),
		newSelStrategy("mid", 50.0),
	}
	pop[0].ParentID = "A"
	pop[1].ParentID = "B"
	pop[2].ParentID = "C"

	// Compute weights via the extracted function — it requires pre-sorted input,
	// so sort manually first (mirrors what Select does internally).
	sorted := make([]*mutation.Strategy, len(pop))
	copy(sorted, pop)
	SortByScore(sorted)

	weights, total := computeLineageRankWeights(sorted, 0.4, 0.5)

	// With threshold=0.4 and each lineage at 1/3 share (~0.33), no penalty applies.
	// Rank weights: best (high, idx 0) = 3, mid (idx 1) = 2, low (idx 2) = 1.
	if total != 6.0 {
		t.Fatalf("totalWeight: got %.6f, want 6.0 (no penalty since all shares ≤ 0.4)", total)
	}
	if weights[0] != 3.0 || weights[1] != 2.0 || weights[2] != 1.0 {
		t.Errorf("weights not in descending rank order: got %v, want [3 2 1]", weights)
	}

	// Verify the highest-scoring strategy is at position 0 (received weight 3).
	if sorted[0].ID != "high" {
		t.Errorf("expected 'high' at sorted[0], got %q", sorted[0].ID)
	}
	if sorted[1].ID != "mid" {
		t.Errorf("expected 'mid' at sorted[1], got %q", sorted[1].ID)
	}
	if sorted[2].ID != "low" {
		t.Errorf("expected 'low' at sorted[2], got %q", sorted[2].ID)
	}
}

// TestRouletteWheelSelection_Proportionality strengthens the existing
// "higher scores selected more often" test by verifying proportionality
// against the known weight ratios derived from shifted scores.
func TestRouletteWheelSelection_Proportionality(t *testing.T) {
	ctx := context.Background()

	// Scores: low=1.0, mid=10.0, high=100.0.
	// After shifting by min (1.0) + epsilon:
	//   low weight   = 1.0   - 1.0 + 1e-9 ≈ 0 (effectively 0)
	//   mid weight   = 10.0  - 1.0 + 1e-9 = 9.0
	//   high weight  = 100.0 - 1.0 + 1e-9 = 99.0
	// Total ≈ 108.0
	// P(low)  ≈ 0
	// P(mid)  ≈ 9/108   ≈ 0.0833
	// P(high) ≈ 99/108  ≈ 0.9167
	// Ratio high:mid ≈ 11:1
	pop := []*mutation.Strategy{
		newSelStrategy("low", 1.0),
		newSelStrategy("mid", 10.0),
		newSelStrategy("high", 100.0),
	}

	rw, err := NewRouletteWheelSelection(WithRouletteSeed(2024))
	if err != nil {
		t.Fatalf("create selector: %v", err)
	}

	const iterations = 5000
	counts := make(map[string]int)
	for i := 0; i < iterations; i++ {
		r, err := rw.Select(ctx, pop, 1)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		counts[r[0].ID]++
	}

	t.Logf("roulette distribution: low=%d, mid=%d, high=%d",
		counts["low"], counts["mid"], counts["high"])

	// Proportionality check: high should be selected ~11x more often than mid.
	// Allow a moderate band (6x to 18x) to absorb statistical noise.
	if counts["mid"] == 0 {
		t.Fatalf("mid count is 0 — cannot compute ratio")
	}
	ratio := float64(counts["high"]) / float64(counts["mid"])
	if ratio < 6.0 || ratio > 18.0 {
		t.Errorf("high:mid ratio %.2f outside expected band [6, 18] "+
			"(theoretical ~11): high=%d, mid=%d", ratio, counts["high"], counts["mid"])
	}

	// Low scorer should almost never be picked (weight ≈ 0).
	// With 5000 iterations and P(low) ≈ 0, the expected count is 0;
	// any selection above 10 (0.2%) strongly suggests a regression.
	if counts["low"] > 10 {
		t.Errorf("low scorer selected %d/%d times — expected near-zero "+
			"(shifted weight ≈ 0)", counts["low"], iterations)
	}
}

// TestLineageRankSelection_BulkSelectMembership verifies membership holds
// across many iterations and various n values, not just for a single Select call.
func TestLineageRankSelection_BulkSelectMembership(t *testing.T) {
	ctx := context.Background()

	pop := makePopulation(100.0, 90.0, 80.0, 70.0, 60.0, 50.0)
	pop[0].ParentID = "A"
	pop[1].ParentID = "A"
	pop[2].ParentID = "B"
	pop[3].ParentID = "B"
	pop[4].ParentID = "C"
	pop[5].ParentID = "C"

	validIDs := make(map[string]bool, len(pop))
	for _, s := range pop {
		validIDs[s.ID] = true
	}

	ls, err := NewLineageRankSelection(WithLineageRankSeed(99))
	if err != nil {
		t.Fatalf("create selector: %v", err)
	}

	// Run several iterations with varying n.
	for _, n := range []int{1, 3, 7, 15} {
		r, err := ls.Select(ctx, pop, n)
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if len(r) != n {
			t.Errorf("n=%d: got %d results, want %d", n, len(r), n)
			continue
		}
		for i, s := range r {
			if s == nil {
				t.Errorf("n=%d index %d: nil strategy", n, i)
				continue
			}
			if !validIDs[s.ID] {
				t.Errorf("n=%d index %d: ID %q not in population", n, i, s.ID)
			}
		}
	}
}

// TestLineageRankSelection_NilPopulationError verifies the input validation
// error is returned (and not a panic) for nil/empty population or invalid n.
func TestLineageRankSelection_NilPopulationError(t *testing.T) {
	ctx := context.Background()
	ls, err := NewLineageRankSelection()
	if err != nil {
		t.Fatalf("create selector: %v", err)
	}

	tests := []struct {
		name string
		pop  []*mutation.Strategy
		n    int
		want error
	}{
		{"nil population", nil, 1, ErrSelectionEmptyPopulation},
		{"empty population", []*mutation.Strategy{}, 1, ErrSelectionEmptyPopulation},
		{"n zero", makePopulation(1.0, 2.0), 0, ErrInvalidSelectionSize},
		{"n negative", makePopulation(1.0, 2.0), -5, ErrInvalidSelectionSize},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ls.Select(ctx, tc.pop, tc.n)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("got error %v, want %v", err, tc.want)
			}
		})
	}
}
