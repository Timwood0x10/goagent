package genome

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// --- Test helpers ---

// newSelStrategy creates a test strategy with minimal fields for selection tests.
func newSelStrategy(id string, score float64) *mutation.Strategy {
	return &mutation.Strategy{
		ID:     id,
		Score:  score,
		Params: map[string]any{"key": "value"},
	}
}

// makePopulation creates a test population with descending scores.
func makePopulation(scores ...float64) []*mutation.Strategy {
	pop := make([]*mutation.Strategy, 0, len(scores))
	for i, s := range scores {
		pop = append(pop, newSelStrategy(fmt.Sprintf("S%d", i), s))
	}
	return pop
}

// --- SortByScore tests ---

func TestSortByScore(t *testing.T) {
	t.Run("correct descending order", func(t *testing.T) {
		strategies := makePopulation(3.0, 1.0, 5.0, 2.0, 4.0)
		SortByScore(strategies)

		for i := 1; i < len(strategies); i++ {
			if strategies[i].Score > strategies[i-1].Score {
				t.Errorf("index %d: score %.1f > previous %.1f (not descending)",
					i, strategies[i].Score, strategies[i-1].Score)
			}
		}

		expected := []float64{5.0, 4.0, 3.0, 2.0, 1.0}
		for i, s := range strategies {
			if s.Score != expected[i] {
				t.Errorf("index %d: got score %.1f, want %.1f", i, s.Score, expected[i])
			}
		}
	})

	t.Run("unevaluated at end", func(t *testing.T) {
		strategies := []*mutation.Strategy{
			newSelStrategy("a", -1), // unevaluated
			newSelStrategy("b", 5.0),
			newSelStrategy("c", -1), // unevaluated
			newSelStrategy("d", 3.0),
			newSelStrategy("e", -1), // unevaluated
		}
		SortByScore(strategies)

		expectedScores := []float64{5.0, 3.0, -1, -1, -1}
		for i, s := range strategies {
			if s.Score != expectedScores[i] {
				t.Errorf("index %d: got score %.1f, want %.1f", i, s.Score, expectedScores[i])
			}
		}
	})

	t.Run("empty slice safe", func(t *testing.T) {
		var strategies []*mutation.Strategy
		// Should not panic.
		SortByScore(strategies)
		if len(strategies) != 0 {
			t.Error("expected empty slice after sorting empty input")
		}
	})

	t.Run("single element safe", func(t *testing.T) {
		strategies := makePopulation(42.0)
		SortByScore(strategies)
		if len(strategies) != 1 || strategies[0].Score != 42.0 {
			t.Error("single element should remain unchanged")
		}
	})

	t.Run("all unevaluated", func(t *testing.T) {
		strategies := []*mutation.Strategy{
			newSelStrategy("a", -1),
			newSelStrategy("b", -1),
			newSelStrategy("c", -1),
		}
		SortByScore(strategies)
		// All should remain in place (stable sort).
		if len(strategies) != 3 {
			t.Error("expected 3 elements")
		}
	})

	t.Run("same scores preserve order (stable)", func(t *testing.T) {
		strategies := []*mutation.Strategy{
			newSelStrategy("first", 5.0),
			newSelStrategy("second", 5.0),
			newSelStrategy("third", 5.0),
		}
		SortByScore(strategies)
		// Stable sort preserves original order among equal scores.
		if strategies[0].ID != "first" || strategies[1].ID != "second" || strategies[2].ID != "third" {
			t.Error("stable sort should preserve order of equal-scored items")
		}
	})
}

// --- TruncationSelection tests ---

func TestTruncationSelection(t *testing.T) {
	ctx := context.Background()
	sel := NewTruncationSelection()

	t.Run("selects correct count", func(t *testing.T) {
		pop := makePopulation(10.0, 20.0, 30.0, 40.0, 50.0)
		result, err := sel.Select(ctx, pop, 3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 3 {
			t.Fatalf("got %d results, want 3", len(result))
		}
	})

	t.Run("selects highest scoring first", func(t *testing.T) {
		pop := makePopulation(10.0, 50.0, 30.0, 40.0, 20.0)
		result, err := sel.Select(ctx, pop, 3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expectedScores := []float64{50.0, 40.0, 30.0}
		for i, s := range result {
			if s.Score != expectedScores[i] {
				t.Errorf("index %d: got score %.1f, want %.1f", i, s.Score, expectedScores[i])
			}
		}
	})

	t.Run("n greater than population size returns all", func(t *testing.T) {
		pop := makePopulation(10.0, 20.0, 30.0)
		result, err := sel.Select(ctx, pop, 100)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 3 {
			t.Fatalf("got %d results, want 3 (full population)", len(result))
		}
	})

	t.Run("n equals zero returns error", func(t *testing.T) {
		pop := makePopulation(10.0, 20.0)
		_, err := sel.Select(ctx, pop, 0)
		if err == nil {
			t.Fatal("expected error for n=0")
		}
		if !errors.Is(err, ErrInvalidSelectionSize) {
			t.Errorf("got error %v, want ErrInvalidSelectionSize", err)
		}
	})

	t.Run("negative n returns error", func(t *testing.T) {
		pop := makePopulation(10.0, 20.0)
		_, err := sel.Select(ctx, pop, -1)
		if err == nil {
			t.Fatal("expected error for negative n")
		}
	})

	t.Run("empty population returns error", func(t *testing.T) {
		emptyPop := []*mutation.Strategy{}
		_, err := sel.Select(ctx, emptyPop, 2)
		if err == nil {
			t.Fatal("expected error for empty population")
		}
		if !errors.Is(err, ErrSelectionEmptyPopulation) {
			t.Errorf("got error %v, want ErrSelectionEmptyPopulation", err)
		}
	})

	t.Run("nil population returns error", func(t *testing.T) {
		_, err := sel.Select(ctx, nil, 2)
		if err == nil {
			t.Fatal("expected error for nil population")
		}
	})

	t.Run("all same scores selects first n", func(t *testing.T) {
		pop := makePopulation(5.0, 5.0, 5.0, 5.0, 5.0)
		result, err := sel.Select(ctx, pop, 3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 3 {
			t.Fatalf("got %d results, want 3", len(result))
		}
		// All have same score; truncation picks first n after sort.
		for _, s := range result {
			if s.Score != 5.0 {
				t.Error("all selected should have score 5.0")
			}
		}
	})

	t.Run("unevaluated strategies go to end", func(t *testing.T) {
		pop := []*mutation.Strategy{
			newSelStrategy("a", -1), // unevaluated
			newSelStrategy("b", 30.0),
			newSelStrategy("c", -1), // unevaluated
			newSelStrategy("d", 10.0),
			newSelStrategy("e", 20.0),
		}
		result, err := sel.Select(ctx, pop, 3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Should pick top 3 evaluated: 30, 20, 10
		expectedScores := []float64{30.0, 20.0, 10.0}
		for i, s := range result {
			if s.Score != expectedScores[i] {
				t.Errorf("index %d: got score %.1f, want %.1f", i, s.Score, expectedScores[i])
			}
		}
	})

	t.Run("valid context succeeds", func(t *testing.T) {
		pop := makePopulation(10.0, 20.0)
		result, err := sel.Select(context.Background(), pop, 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 result, got %d", len(result))
		}
	})
}

// --- TournamentSelection tests ---

func TestTournamentSelection(t *testing.T) {
	t.Run("returns correct count", func(t *testing.T) {
		ctx := context.Background()
		ts, err := NewTournamentSelection(WithTournamentSeed(42))
		if err != nil {
			t.Fatalf("create selector: %v", err)
		}

		pop := makePopulation(10.0, 20.0, 30.0, 40.0, 50.0)
		result, err := ts.Select(ctx, pop, 7)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 7 {
			t.Fatalf("got %d results, want 7", len(result))
		}
	})

	t.Run("biased toward higher scores", func(t *testing.T) {
		ctx := context.Background()
		ts, err := NewTournamentSelection(
			WithTournamentSize(3),
			WithTournamentSeed(12345),
		)
		if err != nil {
			t.Fatalf("create selector: %v", err)
		}

		// Population where one individual has much higher score.
		pop := []*mutation.Strategy{
			newSelStrategy("low_a", 1.0),
			newSelStrategy("low_b", 1.0),
			newSelStrategy("low_c", 1.0),
			newSelStrategy("high", 100.0),
		}

		counts := make(map[string]int)
		iterations := 500
		for i := 0; i < iterations; i++ {
			result, err := ts.Select(ctx, pop, 1)
			if err != nil {
				t.Fatalf("iteration %d: %v", i, err)
			}
			counts[result[0].ID]++
		}

		highCount := counts["high"]
		lowTotal := iterations - highCount

		// High-scoring individual should be selected significantly more often.
		// With k=3 and 1 high out of 4, probability is roughly 1-(3/4 choose 3)/(4 choose 3) = 1-1/4 = 75% per tournament.
		if highCount < lowTotal {
			t.Errorf("high scorer selected %d/%d times, expected majority (biased)",
				highCount, iterations)
		}
		t.Logf("distribution: high=%d, lows total=%d", highCount, lowTotal)
	})

	t.Run("tournament size affects selection pressure", func(t *testing.T) {
		ctx := context.Background()

		pop := []*mutation.Strategy{
			newSelStrategy("low", 1.0),
			newSelStrategy("mid", 5.0),
			newSelStrategy("high", 100.0),
		}

		// Small tournament (k=2): less pressure, more randomness.
		tsSmall, _ := NewTournamentSelection(
			WithTournamentSize(2),
			WithTournamentSeed(99),
		)
		// Large tournament (k=3): more pressure, almost always pick best.
		tsLarge, _ := NewTournamentSelection(
			WithTournamentSize(3),
			WithTournamentSeed(99),
		)

		countSmall := countSelections(ctx, tsSmall, pop, 1000)
		countLarge := countSelections(ctx, tsLarge, pop, 1000)

		ratioSmall := float64(countSmall["high"]) / 1000.0
		ratioLarge := float64(countLarge["high"]) / 1000.0

		t.Logf("k=2 high ratio: %.2f, k=3 high ratio: %.2f", ratioSmall, ratioLarge)

		// Larger tournament should select the highest scorer more often.
		if ratioLarge <= ratioSmall {
			t.Error("larger tournament size should increase selection pressure toward high scorers")
		}
	})

	t.Run("empty population returns error", func(t *testing.T) {
		ctx := context.Background()
		ts, _ := NewTournamentSelection()
		_, err := ts.Select(ctx, []*mutation.Strategy{}, 2)
		if err == nil {
			t.Fatal("expected error for empty population")
		}
	})

	t.Run("n zero returns error", func(t *testing.T) {
		ctx := context.Background()
		ts, _ := NewTournamentSelection()
		pop := makePopulation(10.0, 20.0)
		_, err := ts.Select(ctx, pop, 0)
		if err == nil {
			t.Fatal("expected error for n=0")
		}
	})

	t.Run("n greater than pop size succeeds", func(t *testing.T) {
		ctx := context.Background()
		ts, _ := NewTournamentSelection(WithTournamentSeed(42))
		pop := makePopulation(10.0, 20.0, 30.0)
		result, err := ts.Select(ctx, pop, 100)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 100 {
			t.Fatalf("got %d results, want 100", len(result))
		}
	})

	t.Run("deterministic with same seed", func(t *testing.T) {
		ctx := context.Background()

		ts1, _ := NewTournamentSelection(WithTournamentSeed(999))
		ts2, _ := NewTournamentSelection(WithTournamentSeed(999))

		pop := makePopulation(1.0, 2.0, 3.0, 4.0, 5.0, 6.0, 7.0, 8.0, 9.0, 10.0)

		result1, err := ts1.Select(ctx, pop, 5)
		if err != nil {
			t.Fatalf("first run: %v", err)
		}
		result2, err := ts2.Select(ctx, pop, 5)
		if err != nil {
			t.Fatalf("second run: %v", err)
		}

		if len(result1) != len(result2) {
			t.Fatalf("different lengths: %d vs %d", len(result1), len(result2))
		}
		for i := range result1 {
			if result1[i].ID != result2[i].ID {
				t.Errorf("index %d: got %q, want %q (not deterministic)", i, result1[i].ID, result2[i].ID)
			}
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		ts, _ := NewTournamentSelection(WithTournamentSeed(42))

		// Cancel after a short delay.
		cancel() // Cancel immediately.

		pop := makePopulation(1.0, 2.0, 3.0, 4.0, 5.0)
		result, err := ts.Select(ctx, pop, 10000)
		if err == nil {
			t.Fatal("expected context cancellation error")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("got error %v, want context.Canceled", err)
		}
		// May return partial results before cancellation was detected.
		t.Logf("returned %d results before cancellation", len(result))
	})

	t.Run("invalid tournament size", func(t *testing.T) {
		_, err := NewTournamentSelection(WithTournamentSize(1))
		if err == nil {
			t.Fatal("expected error for tournament size < 2")
		}
		if !errors.Is(err, ErrInvalidTournamentSize) {
			t.Errorf("got error %v, want ErrInvalidTournamentSize", err)
		}
	})
}

// countSelections runs many selections and counts how often each ID appears.
func countSelections(ctx context.Context, ts *TournamentSelection, pop []*mutation.Strategy, n int) map[string]int {
	counts := make(map[string]int)
	for i := 0; i < n; i++ {
		result, _ := ts.Select(ctx, pop, 1)
		if len(result) > 0 {
			counts[result[0].ID]++
		}
	}
	return counts
}

// --- RouletteWheelSelection tests ---

func TestRouletteWheelSelection(t *testing.T) {
	t.Run("returns correct count", func(t *testing.T) {
		ctx := context.Background()
		rw, err := NewRouletteWheelSelection(WithRouletteSeed(42))
		if err != nil {
			t.Fatalf("create selector: %v", err)
		}

		pop := makePopulation(10.0, 20.0, 30.0)
		result, err := rw.Select(ctx, pop, 5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 5 {
			t.Fatalf("got %d results, want 5", len(result))
		}
	})

	t.Run("higher scores selected more often", func(t *testing.T) {
		ctx := context.Background()
		rw, err := NewRouletteWheelSelection(WithRouletteSeed(12345))
		if err != nil {
			t.Fatalf("create selector: %v", err)
		}

		// Population with clearly different scores.
		pop := []*mutation.Strategy{
			newSelStrategy("low", 1.0),
			newSelStrategy("medium", 10.0),
			newSelStrategy("high", 100.0),
		}

		counts := make(map[string]int)
		iterations := 2000
		for i := 0; i < iterations; i++ {
			result, err := rw.Select(ctx, pop, 1)
			if err != nil {
				t.Fatalf("iteration %d: %v", i, err)
			}
			counts[result[0].ID]++
		}

		highCount := counts["high"]
		medCount := counts["medium"]
		lowCount := counts["low"]

		t.Logf("distribution: high=%d, medium=%d, low=%d", highCount, medCount, lowCount)

		// Higher score should be selected more often than lower.
		if highCount <= lowCount {
			t.Errorf("high scorer (%d) should be selected more than low scorer (%d)",
				highCount, lowCount)
		}
		if medCount <= lowCount {
			t.Errorf("medium scorer (%d) should be selected more than low scorer (%d)",
				medCount, lowCount)
		}
	})

	t.Run("all zero scores uniform distribution", func(t *testing.T) {
		ctx := context.Background()
		rw, _ := NewRouletteWheelSelection(WithRouletteSeed(77))

		pop := makePopulation(0.0, 0.0, 0.0, 0.0, 0.0)

		counts := make(map[string]int)
		iterations := 5000
		for i := 0; i < iterations; i++ {
			result, _ := rw.Select(ctx, pop, 1)
			counts[result[0].ID]++
		}

		// With uniform distribution, each should get ~20% (1000/5000).
		expected := iterations / len(pop)
		tolerance := expected / 10 // 10% tolerance.

		for id, count := range counts {
			diff := count - expected
			if diff < 0 {
				diff = -diff
			}
			if diff > tolerance {
				t.Errorf("%s: got %d selections, expected ~%d (±%d)", id, count, expected, tolerance)
			} else {
				t.Logf("%s: %d selections (expected ~%d)", id, count, expected)
			}
		}
	})

	t.Run("negative scores treated as unevaluated", func(t *testing.T) {
		ctx := context.Background()
		rw, _ := NewRouletteWheelSelection(WithRouletteSeed(88))

		// All negative scores are considered unevaluated and filtered out.
		pop := makePopulation(-10.0, -5.0, -0.5)

		result, err := rw.Select(ctx, pop, 1)
		if !errors.Is(err, ErrSelectionEmptyPopulation) {
			t.Fatalf("got result=%v err=%v, want ErrSelectionEmptyPopulation", result, err)
		}
	})

	t.Run("single individual population", func(t *testing.T) {
		ctx := context.Background()
		rw, _ := NewRouletteWheelSelection(WithRouletteSeed(55))

		pop := makePopulation(42.0)
		result, err := rw.Select(ctx, pop, 10)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 10 {
			t.Fatalf("got %d results, want 10", len(result))
		}
		for i, s := range result {
			if s.ID != "S0" {
				t.Errorf("index %d: got ID %q, want S0", i, s.ID)
			}
		}
	})

	t.Run("empty population returns error", func(t *testing.T) {
		ctx := context.Background()
		rw, _ := NewRouletteWheelSelection()
		_, err := rw.Select(ctx, []*mutation.Strategy{}, 2)
		if err == nil {
			t.Fatal("expected error for empty population")
		}
	})

	t.Run("n zero returns error", func(t *testing.T) {
		ctx := context.Background()
		rw, _ := NewRouletteWheelSelection()
		pop := makePopulation(10.0, 20.0)
		_, err := rw.Select(ctx, pop, 0)
		if err == nil {
			t.Fatal("expected error for n=0")
		}
	})

	t.Run("n greater than pop size succeeds", func(t *testing.T) {
		ctx := context.Background()
		rw, _ := NewRouletteWheelSelection(WithRouletteSeed(33))
		pop := makePopulation(10.0, 20.0, 30.0)
		result, err := rw.Select(ctx, pop, 50)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 50 {
			t.Fatalf("got %d results, want 50", len(result))
		}
	})

	t.Run("mixed positive and negative scores", func(t *testing.T) {
		ctx := context.Background()
		rw, _ := NewRouletteWheelSelection(WithRouletteSeed(200))

		pop := makePopulation(0.0, 5.0, 10.0, 15.0)

		counts := make(map[string]int)
		iterations := 2000
		for i := 0; i < iterations; i++ {
			result, _ := rw.Select(ctx, pop, 1)
			counts[result[0].ID]++
		}

		dCount := counts["S3"] // 15.0 highest
		aCount := counts["S0"] // 0.0 lowest

		t.Logf("mixed dist: S0(0)=%d, S1(5)=%d, S2(10)=%d, S3(15)=%d",
			counts["S0"], counts["S1"], counts["S2"], dCount)

		if dCount <= aCount {
			t.Error("highest score should be selected most often")
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		rw, _ := NewRouletteWheelSelection(WithRouletteSeed(42))

		cancel() // Cancel immediately.

		pop := makePopulation(1.0, 2.0, 3.0, 4.0, 5.0)
		result, err := rw.Select(ctx, pop, 100000)
		if err == nil {
			t.Fatal("expected context cancellation error")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("got error %v, want context.Canceled", err)
		}
		t.Logf("returned %d results before cancellation", len(result))
	})

	t.Run("deterministic with same seed", func(t *testing.T) {
		ctx := context.Background()

		rw1, _ := NewRouletteWheelSelection(WithRouletteSeed(888))
		rw2, _ := NewRouletteWheelSelection(WithRouletteSeed(888))

		pop := makePopulation(1.0, 2.0, 3.0, 4.0, 5.0)

		result1, err := rw1.Select(ctx, pop, 10)
		if err != nil {
			t.Fatalf("first run: %v", err)
		}
		result2, err := rw2.Select(ctx, pop, 10)
		if err != nil {
			t.Fatalf("second run: %v", err)
		}

		for i := range result1 {
			if result1[i].ID != result2[i].ID {
				t.Errorf("index %d: got %q, want %q (not deterministic)", i, result1[i].ID, result2[i].ID)
			}
		}
	})
}

// --- PickParent tests ---

func TestPickParent(t *testing.T) {
	ctx := context.Background()

	t.Run("returns valid strategy", func(t *testing.T) {
		ts, _ := NewTournamentSelection(WithTournamentSeed(42))
		rng := rand.New(rand.NewSource(99)) // #nosec G404 - test code

		pop := makePopulation(10.0, 50.0, 30.0)
		parent, err := PickParent(ctx, pop, ts, rng)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parent == nil {
			t.Fatal("expected non-nil parent")
		}
		if parent.Score != 50.0 {
			// With tournament selection and good rng, likely picks high scorer.
			t.Logf("parent score: %.1f (may vary with tournament)", parent.Score)
		}
	})

	t.Run("error on empty population", func(t *testing.T) {
		ts, _ := NewTournamentSelection()
		rng := rand.New(rand.NewSource(1)) // #nosec G404 - test code

		_, err := PickParent(ctx, []*mutation.Strategy{}, ts, rng)
		if err == nil {
			t.Fatal("expected error for empty population")
		}
	})

	t.Run("works with truncation selection", func(t *testing.T) {
		trunc := NewTruncationSelection()
		rng := rand.New(rand.NewSource(55)) // #nosec G404 - test code

		pop := makePopulation(10.0, 80.0, 30.0, 60.0)
		parent, err := PickParent(ctx, pop, trunc, rng)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Truncation always picks the highest scorer.
		if parent.Score != 80.0 {
			t.Errorf("truncation should pick highest scorer, got score %.1f", parent.Score)
		}
	})

	t.Run("nil selector uses default tournament", func(t *testing.T) {
		rng := rand.New(rand.NewSource(time.Now().UnixNano())) // #nosec G404 - test code

		pop := makePopulation(10.0, 90.0, 30.0)
		parent, err := PickParent(ctx, pop, nil, rng)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parent == nil {
			t.Fatal("expected non-nil parent from default selector")
		}
		t.Logf("default tournament picked score: %.1f", parent.Score)
	})

	t.Run("works with roulette wheel", func(t *testing.T) {
		rw, _ := NewRouletteWheelSelection(WithRouletteSeed(333))
		rng := rand.New(rand.NewSource(77)) // #nosec G404 - test code

		pop := makePopulation(10.0, 100.0, 50.0)
		parent, err := PickParent(ctx, pop, rw, rng)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if parent == nil {
			t.Fatal("expected non-nil parent")
		}
		t.Logf("roulette picked: ID=%s, Score=%.1f", parent.ID, parent.Score)
	})
}

// --- Interface compliance test ---

func TestSelectionInterface(t *testing.T) {
	ctx := context.Background()
	pop := makePopulation(1.0, 2.0, 3.0)

	var _ Selection = NewTruncationSelection()

	ts, _ := NewTournamentSelection()
	var _ Selection = ts

	rw, _ := NewRouletteWheelSelection()
	var _ Selection = rw

	lr, _ := NewLineageRankSelection()
	var _ Selection = lr

	rs := NewRankSelection()
	var _ Selection = rs

	sus := NewSUSSelection()
	var _ Selection = sus

	// Verify interface works polymorphically.
	selectors := []struct {
		name string
		sel  Selection
	}{
		{"truncation", NewTruncationSelection()},
		{"tournament", ts},
		{"roulette", rw},
		{"lineage_rank", lr},
		{"rank", rs},
		{"sus", sus},
	}

	for _, tc := range selectors {
		t.Run(tc.name, func(t *testing.T) {
			result, err := tc.sel.Select(ctx, pop, 2)
			if err != nil {
				t.Fatalf("interface select error: %v", err)
			}
			if len(result) != 2 {
				t.Errorf("got %d results, want 2", len(result))
			}
		})
	}
}

// --- Edge case tests ---

func TestValidateSelectInputs(t *testing.T) {
	t.Run("valid context passes validation", func(t *testing.T) {
		err := validateSelectInputs(context.Background(), makePopulation(1.0), 1)
		if err != nil {
			t.Fatalf("unexpected error for valid context: %v", err)
		}
	})

	t.Run("empty population", func(t *testing.T) {
		err := validateSelectInputs(context.Background(), []*mutation.Strategy{}, 1)
		if !errors.Is(err, ErrSelectionEmptyPopulation) {
			t.Errorf("got %v, want ErrSelectionEmptyPopulation", err)
		}
	})

	t.Run("nil population treated as empty", func(t *testing.T) {
		err := validateSelectInputs(context.Background(), nil, 1)
		if !errors.Is(err, ErrSelectionEmptyPopulation) {
			t.Errorf("got %v, want ErrSelectionEmptyPopulation", err)
		}
	})

	t.Run("n less than or equal zero", func(t *testing.T) {
		err := validateSelectInputs(context.Background(), makePopulation(1.0), 0)
		if !errors.Is(err, ErrInvalidSelectionSize) {
			t.Errorf("got %v, want ErrInvalidSelectionSize", err)
		}
		err = validateSelectInputs(context.Background(), makePopulation(1.0), -5)
		if !errors.Is(err, ErrInvalidSelectionSize) {
			t.Errorf("got %v, want ErrInvalidSelectionSize", err)
		}
	})

	t.Run("valid inputs pass", func(t *testing.T) {
		err := validateSelectInputs(context.Background(), makePopulation(1.0), 1)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

// --- LineageRankSelection tests ---

func TestLineageRankSelection_Select(t *testing.T) {
	ctx := context.Background()

	t.Run("returns correct count", func(t *testing.T) {
		ls, err := NewLineageRankSelection(WithLineageRankSeed(42))
		if err != nil {
			t.Fatalf("create selector: %v", err)
		}

		pop := makePopulation(100.0, 80.0, 60.0, 40.0, 20.0)
		pop[0].ParentID = "A"
		pop[1].ParentID = "A"
		pop[2].ParentID = "B"
		pop[3].ParentID = "B"
		pop[4].ParentID = "C"

		result, err := ls.Select(ctx, pop, 3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 3 {
			t.Fatalf("got %d results, want 3", len(result))
		}
	})

	t.Run("penalty increases underrepresented lineage selection", func(t *testing.T) {
		// Compare threshold=0 (max penalty) vs threshold=1 (no penalty)
		// with the same seed. Underrepresented lineage B should be selected
		// more often when penalty is enabled.
		lsWithPenalty, _ := NewLineageRankSelection(
			WithLineageRankSeed(42),
			WithLineagePenaltyThreshold(0),
		)
		lsNoPenalty, _ := NewLineageRankSelection(
			WithLineageRankSeed(42),
			WithLineagePenaltyThreshold(1),
		)

		// Lineage A has 3/5 = 60% share, lineage B has 2/5 = 40%.
		pop := makePopulation(100.0, 90.0, 85.0, 80.0, 70.0)
		pop[0].ParentID = "A"
		pop[1].ParentID = "A"
		pop[2].ParentID = "A"
		pop[3].ParentID = "B"
		pop[4].ParentID = "B"

		// With threshold=0: A share=0.6, excess=0.6/(1.0-0)=0.6, penalty=0.5*0.6=0.3
		//   A weights: 5*0.7=3.5, 4*0.7=2.8, 3*0.7=2.1 → sum=8.4
		//   B share=0.4, excess=0.4, penalty=0.2
		//   B weights: 2*0.8=1.6, 1*0.8=0.8 → sum=2.4
		//   Total=10.8, expected B probability = 2.4/10.8 ≈ 22.22%
		//   N=5000, μ=1111.1, σ=√(5000*0.2222*0.7778)≈29.4, ±3σ → [1023, 1199]
		//
		// With threshold=1: no penalty (share never >1.0)
		//   A weights: 5, 4, 3 → sum=12
		//   B weights: 2, 1 → sum=3
		//   Total=15, expected B probability = 3/15 = 20.0%
		//   N=5000, μ=1000.0, σ=√(5000*0.20*0.80)≈28.3, ±3σ → [915, 1085]

		countPenalty := runLineageSelection(t, ctx, lsWithPenalty, pop, 5000)
		countNoPenalty := runLineageSelection(t, ctx, lsNoPenalty, pop, 5000)

		pB := countPenalty["B"]
		nB := countNoPenalty["B"]

		t.Logf("with penalty:  A=%d, B=%d (B share=%.1f%%)",
			countPenalty["A"], pB, float64(pB)/50.0)
		t.Logf("no penalty:    A=%d, B=%d (B share=%.1f%%)",
			countNoPenalty["A"], nB, float64(nB)/50.0)

		// Direction: penalty should increase B's count vs no penalty.
		if pB <= nB {
			t.Errorf("penalty should increase B selections: with=%d, without=%d", pB, nB)
		}

		// Range: with penalty, B share must be within 3σ of 22.22%.
		const (
			expectedBWithPenalty = 1111
			maxDev3SigmaPenalty  = 88 // 3 * 29.4, rounded up
		)
		if pB < expectedBWithPenalty-maxDev3SigmaPenalty || pB > expectedBWithPenalty+maxDev3SigmaPenalty {
			t.Errorf("with penalty B count %d outside 3σ range [%d, %d] (expected ~%d, share %.1f%%)",
				pB,
				expectedBWithPenalty-maxDev3SigmaPenalty,
				expectedBWithPenalty+maxDev3SigmaPenalty,
				expectedBWithPenalty,
				float64(pB)/50.0)
		}

		// Range: without penalty (threshold=1), B share must be within 3σ of 20.0%.
		const (
			expectedBNoPenalty = 1000
			maxDev3SigmaNoPen  = 85 // 3 * 28.3, rounded up
		)
		if nB < expectedBNoPenalty-maxDev3SigmaNoPen || nB > expectedBNoPenalty+maxDev3SigmaNoPen {
			t.Errorf("no penalty B count %d outside 3σ range [%d, %d] (expected ~%d, share %.1f%%)",
				nB,
				expectedBNoPenalty-maxDev3SigmaNoPen,
				expectedBNoPenalty+maxDev3SigmaNoPen,
				expectedBNoPenalty,
				float64(nB)/50.0)
		}
	})

	t.Run("no penalty with threshold=1", func(t *testing.T) {
		ls, err := NewLineageRankSelection(
			WithLineageRankSeed(42),
			WithLineagePenaltyThreshold(1),
		)
		if err != nil {
			t.Fatalf("create selector: %v", err)
		}

		pop := makePopulation(100.0, 90.0, 80.0)
		pop[0].ParentID = "A"
		pop[1].ParentID = "A"
		pop[2].ParentID = "B"

		result, err := ls.Select(ctx, pop, 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("got %d results, want 2", len(result))
		}
	})

	t.Run("invalid threshold out of range", func(t *testing.T) {
		_, err := NewLineageRankSelection(WithLineagePenaltyThreshold(1.5))
		if err == nil {
			t.Fatal("expected error for threshold > 1")
		}
		_, err = NewLineageRankSelection(WithLineagePenaltyThreshold(-0.1))
		if err == nil {
			t.Fatal("expected error for threshold < 0")
		}
	})

	t.Run("invalid strength out of range", func(t *testing.T) {
		_, err := NewLineageRankSelection(WithLineagePenaltyStrength(1.5))
		if err == nil {
			t.Fatal("expected error for strength > 1")
		}
	})
}

func TestLineageRankSelection_EdgeCases(t *testing.T) {
	ctx := context.Background()

	t.Run("empty population returns error", func(t *testing.T) {
		ls, _ := NewLineageRankSelection()
		_, err := ls.Select(ctx, []*mutation.Strategy{}, 2)
		if err == nil {
			t.Fatal("expected error for empty population")
		}
		if !errors.Is(err, ErrSelectionEmptyPopulation) {
			t.Errorf("got %v, want ErrSelectionEmptyPopulation", err)
		}
	})

	t.Run("n zero returns error", func(t *testing.T) {
		ls, _ := NewLineageRankSelection()
		pop := makePopulation(10.0, 20.0)
		_, err := ls.Select(ctx, pop, 0)
		if err == nil {
			t.Fatal("expected error for n=0")
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		ls, _ := NewLineageRankSelection()
		cancel()

		pop := makePopulation(1.0, 2.0, 3.0)
		_, err := ls.Select(ctx, pop, 10000)
		if err == nil {
			t.Fatal("expected context cancellation error")
		}
	})

	t.Run("deterministic with same seed", func(t *testing.T) {
		ctx := context.Background()
		ls1, _ := NewLineageRankSelection(WithLineageRankSeed(999))
		ls2, _ := NewLineageRankSelection(WithLineageRankSeed(999))

		pop := makePopulation(1.0, 2.0, 3.0, 4.0, 5.0)

		result1, err := ls1.Select(ctx, pop, 5)
		if err != nil {
			t.Fatalf("first run: %v", err)
		}
		result2, err := ls2.Select(ctx, pop, 5)
		if err != nil {
			t.Fatalf("second run: %v", err)
		}

		for i := range result1 {
			if result1[i].ID != result2[i].ID {
				t.Errorf("index %d: got %q, want %q (not deterministic)", i, result1[i].ID, result2[i].ID)
			}
		}
	})
}

// runLineageSelection runs n single-selection iterations and returns per-lineage counts.
// Errors from the selector are fatal — silent skipping would mask regressions.
func runLineageSelection(t *testing.T, ctx context.Context, ls *LineageRankSelection, pop []*mutation.Strategy, n int) map[string]int {
	t.Helper()
	counts := make(map[string]int)
	for i := 0; i < n; i++ {
		result, err := ls.Select(ctx, pop, 1)
		if err != nil {
			t.Fatalf("lineage selection iteration %d: %v", i, err)
		}
		if len(result) > 0 {
			pid := result[0].ParentID
			if pid == "" {
				pid = "(root)"
			}
			counts[pid]++
		}
	}
	return counts
}

// --- SUSSelection tests ---

func TestSUSSelection_Select(t *testing.T) {
	ctx := context.Background()

	t.Run("returns correct count", func(t *testing.T) {
		sus := NewSUSSelection()
		pop := makePopulation(10.0, 20.0, 30.0, 40.0, 50.0)
		result, err := sus.Select(ctx, pop, 3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 3 {
			t.Fatalf("got %d results, want 3", len(result))
		}
	})

	t.Run("higher scores selected more often", func(t *testing.T) {
		sus := NewSUSSelection()

		pop := []*mutation.Strategy{
			newSelStrategy("low", 1.0),
			newSelStrategy("medium", 10.0),
			newSelStrategy("high", 100.0),
		}

		counts := make(map[string]int)
		iterations := 2000
		for i := 0; i < iterations; i++ {
			result, err := sus.Select(ctx, pop, 1)
			if err != nil {
				t.Fatalf("iteration %d: %v", i, err)
			}
			counts[result[0].ID]++
		}

		highCount := counts["high"]
		medCount := counts["medium"]
		lowCount := counts["low"]

		t.Logf("SUS distribution: high=%d, medium=%d, low=%d",
			highCount, medCount, lowCount)

		if highCount <= lowCount {
			t.Errorf("high scorer (%d) should be selected more than low scorer (%d)",
				highCount, lowCount)
		}

		// Proportionality: with scores [1, 10, 100], expected ratio is ~1:10:100.
		// high should be at least 5x medium (conservative bound).
		if medCount > 0 && highCount < medCount*5 {
			t.Errorf("high:medium ratio too low: %d:%d, expected high >> medium",
				highCount, medCount)
		}
	})

	t.Run("all unevaluated returns error", func(t *testing.T) {
		sus := NewSUSSelection()
		pop := []*mutation.Strategy{
			newSelStrategy("a", -1),
			newSelStrategy("b", -1),
		}
		_, err := sus.Select(ctx, pop, 1)
		if !errors.Is(err, ErrSelectionEmptyPopulation) {
			t.Errorf("got %v, want ErrSelectionEmptyPopulation", err)
		}
	})

	t.Run("mixed evaluated and unevaluated", func(t *testing.T) {
		sus := NewSUSSelection()
		pop := []*mutation.Strategy{
			newSelStrategy("ev1", 10.0),
			newSelStrategy("unev", -1),
			newSelStrategy("ev2", 20.0),
		}
		result, err := sus.Select(ctx, pop, 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("got %d results, want 2", len(result))
		}
		for _, s := range result {
			if s.Score < 0 {
				t.Errorf("unevaluated strategy should not be selected")
			}
		}
	})
}

func TestSUSSelection_UniformDistribution(t *testing.T) {
	ctx := context.Background()
	sus := NewSUSSelection()

	pop := makePopulation(5.0, 5.0, 5.0, 5.0, 5.0)

	counts := make(map[string]int)
	iterations := 5000
	for i := 0; i < iterations; i++ {
		result, err := sus.Select(ctx, pop, 1)
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		counts[result[0].ID]++
	}

	expected := iterations / len(pop)
	tolerance := expected / 10 // 10% tolerance

	for id, count := range counts {
		diff := count - expected
		if diff < 0 {
			diff = -diff
		}
		if diff > tolerance {
			t.Errorf("%s: got %d selections, expected ~%d (±%d)", id, count, expected, tolerance)
		}
	}
}

func TestSUSSelection_EdgeCases(t *testing.T) {
	ctx := context.Background()

	t.Run("empty population returns error", func(t *testing.T) {
		sus := NewSUSSelection()
		_, err := sus.Select(ctx, []*mutation.Strategy{}, 2)
		if err == nil {
			t.Fatal("expected error for empty population")
		}
	})

	t.Run("n zero returns error", func(t *testing.T) {
		sus := NewSUSSelection()
		pop := makePopulation(10.0, 20.0)
		_, err := sus.Select(ctx, pop, 0)
		if err == nil {
			t.Fatal("expected error for n=0")
		}
	})

	t.Run("n greater than pop size succeeds", func(t *testing.T) {
		sus := NewSUSSelection()
		pop := makePopulation(10.0, 20.0, 30.0)
		result, err := sus.Select(ctx, pop, 50)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 50 {
			t.Fatalf("got %d results, want 50", len(result))
		}
	})
}

// --- RankSelection tests ---

func TestRankSelection_Select(t *testing.T) {
	ctx := context.Background()

	t.Run("returns correct count", func(t *testing.T) {
		rs := NewRankSelection()
		pop := makePopulation(10.0, 20.0, 30.0)
		result, err := rs.Select(ctx, pop, 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("got %d results, want 2", len(result))
		}
	})

	t.Run("higher scores selected more often", func(t *testing.T) {
		rs := NewRankSelection()

		pop := []*mutation.Strategy{
			newSelStrategy("low", 1.0),
			newSelStrategy("med", 10.0),
			newSelStrategy("high", 100.0),
		}

		counts := make(map[string]int)
		iterations := 2000
		for i := 0; i < iterations; i++ {
			result, err := rs.Select(ctx, pop, 1)
			if err != nil {
				t.Fatalf("iteration %d: %v", i, err)
			}
			counts[result[0].ID]++
		}

		highCount := counts["high"]
		lowCount := counts["low"]

		t.Logf("Rank distribution: high=%d, med=%d, low=%d",
			highCount, counts["med"], lowCount)

		if highCount <= lowCount {
			t.Errorf("high scorer (%d) should be selected more than low scorer (%d)",
				highCount, lowCount)
		}
	})

	t.Run("all equal scores uniform distribution", func(t *testing.T) {
		rs := NewRankSelection()
		pop := makePopulation(5.0, 5.0, 5.0)

		counts := make(map[string]int)
		iterations := 3000
		for i := 0; i < iterations; i++ {
			result, err := rs.Select(ctx, pop, 1)
			if err != nil {
				t.Fatalf("iteration %d: %v", i, err)
			}
			counts[result[0].ID]++
		}

		// Rank still assigns different weights by position: 3, 2, 1
		// S0 gets rank 3 (highest), S1 gets 2, S2 gets 1
		// S0 should have higher selection than S2
		if counts["S0"] <= counts["S2"] {
			t.Errorf("S0 (rank=3, count=%d) should be selected more than S2 (rank=1, count=%d)",
				counts["S0"], counts["S2"])
		}
	})

	t.Run("single individual selected repeatedly", func(t *testing.T) {
		rs := NewRankSelection()
		pop := makePopulation(42.0)
		result, err := rs.Select(ctx, pop, 5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 5 {
			t.Fatalf("got %d results, want 5", len(result))
		}
	})
}

func TestRankSelection_EdgeCases(t *testing.T) {
	ctx := context.Background()

	t.Run("empty population returns error", func(t *testing.T) {
		rs := NewRankSelection()
		_, err := rs.Select(ctx, []*mutation.Strategy{}, 2)
		if err == nil {
			t.Fatal("expected error for empty population")
		}
	})

	t.Run("n zero returns error", func(t *testing.T) {
		rs := NewRankSelection()
		pop := makePopulation(10.0, 20.0)
		_, err := rs.Select(ctx, pop, 0)
		if err == nil {
			t.Fatal("expected error for n=0")
		}
	})

	t.Run("n greater than pop size succeeds", func(t *testing.T) {
		rs := NewRankSelection()
		pop := makePopulation(10.0, 20.0)
		result, err := rs.Select(ctx, pop, 10)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 10 {
			t.Fatalf("got %d results, want 10", len(result))
		}
	})

	t.Run("context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		rs := NewRankSelection()
		cancel()

		pop := makePopulation(1.0, 2.0, 3.0, 4.0, 5.0)
		_, err := rs.Select(ctx, pop, 100000)
		if err == nil {
			t.Fatal("expected context cancellation error")
		}
	})
}

// --- Helper function tests ---

func TestFindMinScore(t *testing.T) {
	tests := []struct {
		name     string
		pop      []*mutation.Strategy
		expected float64
	}{
		{"single element", makePopulation(42.0), 42.0},
		{"positive scores", makePopulation(3.0, 1.0, 5.0, 2.0), 1.0},
		{"negative scores", makePopulation(-1.0, -10.0, -3.0), -10.0},
		{"mixed scores", makePopulation(-5.0, 0.0, 10.0, -1.0), -5.0},
		{"all same", makePopulation(7.0, 7.0, 7.0), 7.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findMinScore(tt.pop)
			if got != tt.expected {
				t.Errorf("got %.1f, want %.1f", got, tt.expected)
			}
		})
	}
}
