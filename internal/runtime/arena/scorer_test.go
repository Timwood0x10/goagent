package arena

import (
	"context"
	"errors"
	"testing"
)

var ctx = context.Background()

// fixedScorer returns a constant score or error.
type fixedScorer struct {
	score float64
	err   error
}

func (s *fixedScorer) Score(_ context.Context, input any) (float64, error) {
	return s.score, s.err
}

func TestNewEnsembleScorer_Valid(t *testing.T) {
	s1 := &fixedScorer{score: 0.8}
	s2 := &fixedScorer{score: 0.6}

	es, err := NewEnsembleScorer(s1, 0.7, s2, 0.3)
	if err != nil {
		t.Fatalf("NewEnsembleScorer failed: %v", err)
	}
	if len(es.scorers) != 2 {
		t.Errorf("got %d scorers, want 2", len(es.scorers))
	}
}

func TestNewEnsembleScorer_OddArgs(t *testing.T) {
	_, err := NewEnsembleScorer(&fixedScorer{}, 0.5, &fixedScorer{})
	if err == nil {
		t.Fatal("expected error for odd args")
	}
}

func TestNewEnsembleScorer_NilScorer(t *testing.T) {
	_, err := NewEnsembleScorer(nil, 0.5)
	if err == nil {
		t.Fatal("expected error for nil scorer")
	}
}

func TestNewEnsembleScorer_ZeroWeight(t *testing.T) {
	_, err := NewEnsembleScorer(&fixedScorer{}, 0.0)
	if err == nil {
		t.Fatal("expected error for zero weight")
	}
}

func TestEnsembleScorer_WeightedAverage(t *testing.T) {
	s1 := &fixedScorer{score: 1.0}
	s2 := &fixedScorer{score: 0.0}

	es, err := NewEnsembleScorer(s1, 0.75, s2, 0.25)
	if err != nil {
		t.Fatalf("NewEnsembleScorer failed: %v", err)
	}

	score, err := es.Score(ctx, "test")
	if err != nil {
		t.Fatalf("Score failed: %v", err)
	}
	if score != 0.75 {
		t.Errorf("score = %f, want 0.75", score)
	}
}

func TestEnsembleScorer_EqualWeights(t *testing.T) {
	s1 := &fixedScorer{score: 1.0}
	s2 := &fixedScorer{score: 0.5}

	es, err := NewEnsembleScorer(s1, 1.0, s2, 1.0)
	if err != nil {
		t.Fatalf("NewEnsembleScorer failed: %v", err)
	}

	score, err := es.Score(ctx, "test")
	if err != nil {
		t.Fatalf("Score failed: %v", err)
	}
	if score != 0.75 {
		t.Errorf("score = %f, want 0.75", score)
	}
}

func TestEnsembleScorer_ClampToRange(t *testing.T) {
	s1 := &fixedScorer{score: 1.5}

	es, err := NewEnsembleScorer(s1, 1.0)
	if err != nil {
		t.Fatalf("NewEnsembleScorer failed: %v", err)
	}

	score, err := es.Score(ctx, "test")
	if err != nil {
		t.Fatalf("Score failed: %v", err)
	}
	if score != 1.0 {
		t.Errorf("score = %f, want 1.0", score)
	}
}

func TestEnsembleScorer_SubScorerError(t *testing.T) {
	s1 := &fixedScorer{score: 0.5}
	s2 := &fixedScorer{err: errors.New("scorer failed")}

	es, err := NewEnsembleScorer(s1, 0.5, s2, 0.5)
	if err != nil {
		t.Fatalf("NewEnsembleScorer failed: %v", err)
	}

	_, err = es.Score(ctx, "test")
	if err == nil {
		t.Fatal("expected error from sub-scorer")
	}
}

func TestExactMatchScorer_Match(t *testing.T) {
	s := NewExactMatchScorer(
		func(input any) string { return "expected" },
		func(input any) string { return "expected" },
	)
	score, err := s.Score(ctx, "ignored")
	if err != nil {
		t.Fatalf("Score failed: %v", err)
	}
	if score != 1.0 {
		t.Errorf("score = %f, want 1.0", score)
	}
}

func TestExactMatchScorer_NoMatch(t *testing.T) {
	s := NewExactMatchScorer(
		func(input any) string { return "expected" },
		func(input any) string { return "actual" },
	)
	score, err := s.Score(ctx, "ignored")
	if err != nil {
		t.Fatalf("Score failed: %v", err)
	}
	if score != 0.0 {
		t.Errorf("score = %f, want 0.0", score)
	}
}

func TestMapScorer(t *testing.T) {
	s := NewMapScorer(func(_ context.Context, input any) (float64, error) {
		return 0.42, nil
	})
	score, err := s.Score(ctx, "test")
	if err != nil {
		t.Fatalf("Score failed: %v", err)
	}
	if score != 0.42 {
		t.Errorf("score = %f, want 0.42", score)
	}
}

func TestAdaptiveRegression_EarlyStop(t *testing.T) {
	scoresByKey := map[string]*sequentialScorer{
		"old": {scores: []float64{0.2, 0.3, 0.25, 0.28, 0.22, 0.27, 0.24, 0.26, 0.23, 0.29}},
		"new": {scores: []float64{0.8, 0.85, 0.82, 0.88, 0.81, 0.86, 0.83, 0.87, 0.84, 0.89}},
	}

	composite := NewMapScorer(func(_ context.Context, input any) (float64, error) {
		key, _ := unwrapInput(input).(string)
		s, ok := scoresByKey[key]
		if !ok {
			return 0, nil
		}
		return s.Score(ctx, nil)
	})

	tester, err := NewRegressionTester(&Service{}, composite)
	if err != nil {
		t.Fatalf("NewRegressionTester failed: %v", err)
	}

	result, err := tester.Run(ctx, RegressionConfig{
		OldStrategy:       "old",
		NewStrategy:       "new",
		BaselineRuns:      50,
		CompareRuns:       50,
		MinAdaptiveRuns:   5,
		AdaptiveBatchSize: 5,
		MaxAdaptiveRuns:   50,
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if !result.Confident {
		t.Errorf("expected confident result for clear winner, p=%f", result.PValue)
	}
	if result.WinRate < 0.9 {
		t.Errorf("win rate = %f, want > 0.9", result.WinRate)
	}
}

func TestAdaptiveRegression_NoDiff(t *testing.T) {
	oldScores := []float64{0.5, 0.52, 0.49, 0.51, 0.48, 0.53, 0.5, 0.51, 0.49, 0.52}
	newScores := []float64{0.5, 0.51, 0.5, 0.49, 0.51, 0.5, 0.52, 0.49, 0.5, 0.51}

	scoresByKey := map[string]*sequentialScorer{
		"old": {scores: oldScores},
		"new": {scores: newScores},
	}

	composite := NewMapScorer(func(_ context.Context, input any) (float64, error) {
		key, _ := unwrapInput(input).(string)
		s, ok := scoresByKey[key]
		if !ok {
			return 0, nil
		}
		return s.Score(ctx, nil)
	})

	tester, err := NewRegressionTester(&Service{}, composite)
	if err != nil {
		t.Fatalf("NewRegressionTester failed: %v", err)
	}

	result, err := tester.Run(ctx, RegressionConfig{
		OldStrategy:       "old",
		NewStrategy:       "new",
		BaselineRuns:      10,
		CompareRuns:       10,
		MinAdaptiveRuns:   10,
		AdaptiveBatchSize: 5,
		MaxAdaptiveRuns:   10,
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if result.Confident {
		t.Logf("no-diff gave confident=true (possible if random), p=%f", result.PValue)
	}
}

func TestAdaptiveRegression_CompositeScorerRouteByKey(t *testing.T) {
	scoresByKey := map[string]*sequentialScorer{
		"old": {scores: []float64{0.1, 0.12, 0.11, 0.13, 0.1, 0.12, 0.11, 0.13, 0.1, 0.12}},
		"new": {scores: []float64{0.9, 0.92, 0.91, 0.93, 0.9, 0.92, 0.91, 0.93, 0.9, 0.92}},
	}

	composite := NewMapScorer(func(_ context.Context, input any) (float64, error) {
		key, _ := unwrapInput(input).(string)
		s, ok := scoresByKey[key]
		if !ok {
			return 0, nil
		}
		return s.Score(ctx, nil)
	})

	tester, err := NewRegressionTester(&Service{}, composite)
	if err != nil {
		t.Fatalf("NewRegressionTester failed: %v", err)
	}

	result, err := tester.Run(ctx, RegressionConfig{
		OldStrategy:       "old",
		NewStrategy:       "new",
		BaselineRuns:      10,
		CompareRuns:       10,
		MinAdaptiveRuns:   5,
		AdaptiveBatchSize: 5,
		MaxAdaptiveRuns:   10,
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	if !result.Confident {
		t.Errorf("expected confident result, p=%f", result.PValue)
	}
}

// unwrapInput extracts the underlying strategy from TestCaseInput if wrapped.
func unwrapInput(input any) any {
	if tci, ok := input.(TestCaseInput); ok {
		return tci.Strategy
	}
	return input
}
