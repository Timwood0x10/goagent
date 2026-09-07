package arena

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestApproximatePValue_TDistributionAnchors locks the exact Student's t
// two-tailed p-value computation against known distribution anchors: the
// t-critical values for p=0.05 at several degrees of freedom, plus a few
// non-critical points. The previous ad-hoc scaling was inaccurate near
// p≈0.05 for small df; the incomplete-beta implementation must match the
// standard t-table values within tolerance.
func TestApproximatePValue_TDistributionAnchors(t *testing.T) {
	cases := []struct {
		name string
		t    float64
		df   float64
		want float64 // known two-tailed p-value from the t distribution
	}{
		// t-critical values at p=0.05 (two-tailed) from standard t-tables.
		{"df1_critical", 12.706, 1, 0.05},
		{"df5_critical", 2.571, 5, 0.05},
		{"df10_critical", 2.228, 10, 0.05},
		{"df30_critical", 2.042, 30, 0.05},
		// Non-critical points for df=10.
		{"df10_t1", 1.0, 10, 0.3409},
		{"df10_t2", 2.0, 10, 0.0734},
		{"df10_t3", 3.0, 10, 0.0133},
		// Edge cases.
		{"zero_t", 0.0, 10, 1.0},
		{"df1_t1", 1.0, 1, 0.5000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := approximatePValue(tc.t, tc.df)
			if math.Abs(got-tc.want) > 1e-3 {
				t.Errorf("approximatePValue(t=%.3f, df=%.0f) = %.4f, want %.4f (±0.001)",
					tc.t, tc.df, got, tc.want)
			}
		})
	}
}

// TestApproximatePValue_MonotonicInT locks that the p-value decreases as |t|
// grows for a fixed df — a sanity contract for the incomplete-beta path.
func TestApproximatePValue_MonotonicInT(t *testing.T) {
	df := 10.0
	prev := 1.0
	for i := 1; i <= 10; i++ {
		p := approximatePValue(float64(i), df)
		if p > prev {
			t.Fatalf("p-value not monotonic: t=%d p=%.4f > previous %.4f", i, p, prev)
		}
		prev = p
	}
}

type mockScorer struct {
	scores    map[int]float64 // call index → score
	callCount int
	mu        sync.Mutex
}

// newMockScorer creates a mock scorer with predefined scores.
func newMockScorer(scores map[int]float64) *mockScorer {
	return &mockScorer{
		scores: scores,
	}
}

// Score returns the predetermined score for the current call index.
func (m *mockScorer) Score(_ context.Context, runResult any) (float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	idx := m.callCount
	m.callCount++

	score, ok := m.scores[idx]
	if !ok {
		return 0.0, errors.New("mock: no score configured for this call")
	}
	return score, nil
}

// TestRegressionResult_DefaultFields verifies zero-value initialization.
func TestRegressionResult_DefaultFields(t *testing.T) {
	var result RegressionResult

	if result.OldStrategyID != "" {
		t.Errorf("expected empty OldStrategyID, got %q", result.OldStrategyID)
	}
	if result.NewStrategyID != "" {
		t.Errorf("expected empty NewStrategyID, got %q", result.NewStrategyID)
	}
	if result.OldAvg != 0 {
		t.Errorf("expected OldAvg 0, got %f", result.OldAvg)
	}
	if result.NewAvg != 0 {
		t.Errorf("expected NewAvg 0, got %f", result.NewAvg)
	}
	if result.WinRate != 0 {
		t.Errorf("expected WinRate 0, got %f", result.WinRate)
	}
	if result.Confident {
		t.Error("expected Confident false")
	}
	if result.Samples != 0 {
		t.Errorf("expected Samples 0, got %d", result.Samples)
	}
	if result.PValue != 0 {
		t.Errorf("expected PValue 0, got %f", result.PValue)
	}
	if !result.TestedAt.IsZero() {
		t.Error("expected TestedAt to be zero")
	}
	if len(result.OldScores) != 0 {
		t.Error("expected empty OldScores")
	}
	if len(result.NewScores) != 0 {
		t.Error("expected empty NewScores")
	}
}

// TestDefaultRegressionConfig verifies default configuration values.
func TestDefaultRegressionConfig(t *testing.T) {
	cfg := DefaultRegressionConfig()

	if cfg.BaselineRuns != 5 {
		t.Errorf("expected BaselineRuns 5, got %d", cfg.BaselineRuns)
	}
	if cfg.CompareRuns != 5 {
		t.Errorf("expected CompareRuns 5, got %d", cfg.CompareRuns)
	}
	if cfg.TestSuite != "" {
		t.Errorf("expected empty TestSuite, got %q", cfg.TestSuite)
	}
	if cfg.Confidence != 0.05 {
		t.Errorf("expected Confidence 0.05, got %f", cfg.Confidence)
	}
	if cfg.MinWinRate != 0.55 {
		t.Errorf("expected MinWinRate 0.55, got %f", cfg.MinWinRate)
	}
	if cfg.OldStrategy != nil {
		t.Error("expected nil OldStrategy")
	}
	if cfg.NewStrategy != nil {
		t.Error("expected nil NewStrategy")
	}
}

// TestNewRegressionTester_ValidArgs verifies successful creation.
func TestNewRegressionTester_ValidArgs(t *testing.T) {
	service := NewService(nil, nil, nil)
	scorer := newMockScorer(map[int]float64{0: 1.0})

	tester, err := NewRegressionTester(service, scorer)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tester == nil {
		t.Fatal("expected non-nil tester")
	}
	if tester.arena != service {
		t.Error("arena not set correctly")
	}
	if tester.scorer != scorer {
		t.Error("scorer not set correctly")
	}
}

// TestNewRegressionTester_NilArena checks nil arena rejection.
func TestNewRegressionTester_NilArena(t *testing.T) {
	scorer := newMockScorer(map[int]float64{0: 1.0})

	tester, err := NewRegressionTester(nil, scorer)
	if err == nil {
		t.Fatal("expected error for nil arena")
	}
	if tester != nil {
		t.Error("expected nil tester on error")
	}
	if !errors.Is(err, ErrNilArena) {
		t.Errorf("expected ErrNilArena, got %v", err)
	}
}

// TestNewRegressionTester_NilScorer checks nil scorer rejection.
func TestNewRegressionTester_NilScorer(t *testing.T) {
	service := NewService(nil, nil, nil)

	tester, err := NewRegressionTester(service, nil)
	if err == nil {
		t.Fatal("expected error for nil scorer")
	}
	if tester != nil {
		t.Error("expected nil tester on error")
	}
	if !errors.Is(err, ErrNilScorer) {
		t.Errorf("expected ErrNilScorer, got %v", err)
	}
}

// TestNewRegressionTesterWithScorer verifies the scorer-only constructor used by
// the evolution candidate gate 3 (no arena Service required).
func TestNewRegressionTesterWithScorer(t *testing.T) {
	t.Run("valid scorer", func(t *testing.T) {
		scorer := newMockScorer(map[int]float64{0: 1.0})
		tester, err := NewRegressionTesterWithScorer(scorer)
		require.NoError(t, err)
		require.NotNil(t, tester)
		if tester.scorer != scorer {
			t.Error("scorer not set correctly")
		}
	})

	t.Run("nil scorer rejected", func(t *testing.T) {
		tester, err := NewRegressionTesterWithScorer(nil)
		require.Error(t, err)
		require.Nil(t, tester)
		require.ErrorIs(t, err, ErrNilScorer)
	})

	t.Run("runs regression without a service", func(t *testing.T) {
		// The scorer-only tester must run a full regression comparison even
		// though no arena.Service was provided.
		oldScorer := newMockScorer(map[int]float64{0: 55, 1: 52, 2: 58, 3: 54, 4: 56})
		newScorer := newMockScorer(map[int]float64{0: 85, 1: 88, 2: 82, 3: 90, 4: 86})
		composite := &compositeScorer{scorers: map[any]Scorer{"old": oldScorer, "new": newScorer}}
		tester, err := NewRegressionTesterWithScorer(composite)
		require.NoError(t, err)

		result, err := tester.Run(context.Background(), RegressionConfig{
			OldStrategy:  "old",
			NewStrategy:  "new",
			BaselineRuns: 5,
			CompareRuns:  5,
			Confidence:   0.05,
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		if !result.Confident {
			t.Error("expected confident true for an obvious improvement")
		}
		if result.NewAvg <= result.OldAvg {
			t.Errorf("expected new avg to exceed old avg: old=%f new=%f", result.OldAvg, result.NewAvg)
		}
	})
}

// TestRun_NewBetterStrategy verifies high win rate and confidence when new is better.
func TestRun_NewBetterStrategy(t *testing.T) {
	service := NewService(nil, nil, nil)

	// Use separate scorers for each strategy to avoid race conditions.
	oldScorer := newMockScorer(map[int]float64{
		0: 55.0, 1: 52.0, 2: 58.0, 3: 54.0, 4: 56.0,
	})
	newScorer := newMockScorer(map[int]float64{
		0: 85.0, 1: 88.0, 2: 82.0, 3: 90.0, 4: 86.0,
	})

	// Create a composite scorer that routes to the correct scorer based on input.
	compositeScorer := &compositeScorer{
		scorers: map[any]Scorer{
			"old-strategy": oldScorer,
			"new-strategy": newScorer,
		},
	}

	tester, err := NewRegressionTester(service, compositeScorer)
	if err != nil {
		t.Fatalf("failed to create tester: %v", err)
	}

	cfg := RegressionConfig{
		OldStrategy:  "old-strategy",
		NewStrategy:  "new-strategy",
		BaselineRuns: 5,
		CompareRuns:  5,
		Confidence:   0.05,
	}

	result, err := tester.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.WinRate < 0.8 {
		t.Errorf("expected WinRate >= 0.8, got %f", result.WinRate)
	}
	if !result.Confident {
		t.Error("expected Confident true for obvious improvement")
	}
	if result.OldAvg < 50 || result.OldAvg > 60 {
		t.Errorf("expected OldAvg between 50-60, got %f", result.OldAvg)
	}
	if result.NewAvg < 80 || result.NewAvg > 90 {
		t.Errorf("expected NewAvg between 80-90, got %f", result.NewAvg)
	}
	if result.Samples != 5 {
		t.Errorf("expected Samples 5, got %d", result.Samples)
	}
	if result.TestedAt.IsZero() {
		t.Error("expected TestedAt to be set")
	}
}

// TestRun_OldBetterStrategy verifies low win rate when new is worse.
func TestRun_OldBetterStrategy(t *testing.T) {
	service := NewService(nil, nil, nil)

	oldScorer := newMockScorer(map[int]float64{
		0: 85.0, 1: 88.0, 2: 82.0, 3: 90.0, 4: 86.0,
	})
	newScorer := newMockScorer(map[int]float64{
		0: 55.0, 1: 52.0, 2: 58.0, 3: 54.0, 4: 56.0,
	})

	compositeScorer := &compositeScorer{
		scorers: map[any]Scorer{
			"old": oldScorer,
			"new": newScorer,
		},
	}

	tester, err := NewRegressionTester(service, compositeScorer)
	if err != nil {
		t.Fatalf("failed to create tester: %v", err)
	}

	cfg := RegressionConfig{
		OldStrategy:  "old",
		NewStrategy:  "new",
		BaselineRuns: 5,
		CompareRuns:  5,
		Confidence:   0.05,
	}

	result, err := tester.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.WinRate > 0.2 {
		t.Errorf("expected WinRate <= 0.2, got %f", result.WinRate)
	}
	if result.OldAvg < 80 {
		t.Errorf("expected OldAvg > 80, got %f", result.OldAvg)
	}
	if result.NewAvg > 60 {
		t.Errorf("expected NewAvg < 60, got %f", result.NewAvg)
	}
}

// TestRun_EqualStrategies verifies win rate ~0.5 when strategies are equal.
func TestRun_EqualStrategies(t *testing.T) {
	service := NewService(nil, nil, nil)

	// Both strategies return identical scores.
	baseScore := 70.0
	scores := make([]float64, 10)
	for i := range scores {
		scores[i] = baseScore
	}

	combinedScorer := &sequentialScorer{scores: scores}

	tester, err := NewRegressionTester(service, combinedScorer)
	if err != nil {
		t.Fatalf("failed to create tester: %v", err)
	}

	cfg := RegressionConfig{
		OldStrategy:  "strategy-a",
		NewStrategy:  "strategy-b",
		BaselineRuns: 5,
		CompareRuns:  5,
		Confidence:   0.05,
	}

	result, err := tester.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Win rate should be exactly 1.0 since all scores are equal (new >= old).
	if result.WinRate < 0.9 {
		t.Errorf("expected WinRate >= 0.9 for identical strategies, got %f", result.WinRate)
	}
	// Should NOT be confident since there's no real difference.
	if result.Confident {
		t.Error("expected Confident false for identical strategies")
	}
	if mathAbs(result.OldAvg-result.NewAvg) > 0.001 {
		t.Errorf("expected similar averages, OldAvg=%f, NewAvg=%f", result.OldAvg, result.NewAvg)
	}
}

// TestBuildResult_MinWinRateThreshold verifies that NewBetter reflects the
// configured MinWinRate floor: the new strategy is only "better" when its win
// rate meets or exceeds the threshold. This locks in the previously dead
// MinWinRate config so it actually drives a result.
func TestBuildResult_MinWinRateThreshold(t *testing.T) {
	scorer := newMockScorer(map[int]float64{0: 55, 1: 52, 2: 58, 3: 54, 4: 56})
	tester, err := NewRegressionTesterWithScorer(scorer)
	require.NoError(t, err)

	tests := []struct {
		name       string
		oldScore   float64
		newScore   float64
		minWinRate float64
		wantBetter bool
	}{
		{"new_strictly_wins_above_floor", 50, 90, 0.8, true},
		{"new_loses_below_floor", 60, 50, 0.4, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 5 baseline + 5 compare runs, all identical to force a stable
			// pair-wise win rate.
			base := []float64{tt.oldScore, tt.oldScore, tt.oldScore, tt.oldScore, tt.oldScore}
			comp := []float64{tt.newScore, tt.newScore, tt.newScore, tt.newScore, tt.newScore}
			res := tester.buildResult(RegressionConfig{MinWinRate: tt.minWinRate}, base, comp)
			if res.NewBetter != tt.wantBetter {
				t.Errorf("NewBetter = %v, want %v (win rate %.2f vs floor %.2f)",
					res.NewBetter, tt.wantBetter, res.WinRate, tt.minWinRate)
			}
		})
	}
}

// TestBuildResult_SamplesReflectsScored verifies that Samples reports the actual
// number of scored runs rather than the configured run counts, which is
// important when adaptive mode trims both score slices.
func TestBuildResult_SamplesReflectsScored(t *testing.T) {
	scorer := newMockScorer(map[int]float64{0: 50, 1: 50})
	tester, err := NewRegressionTesterWithScorer(scorer)
	require.NoError(t, err)

	oldScores := []float64{50, 50, 50}
	newScores := []float64{55, 55, 55}
	res := tester.buildResult(RegressionConfig{BaselineRuns: 10, CompareRuns: 10}, oldScores, newScores)
	if res.Samples != 3 {
		t.Errorf("Samples = %d, want 3 (actual scored runs, not configured 10)", res.Samples)
	}
	if len(res.OldScores) != 3 || len(res.NewScores) != 3 {
		t.Errorf("score slices trimmed unexpectedly: old=%d new=%d", len(res.OldScores), len(res.NewScores))
	}
}

// TestComputeSignificance_ObviousDifference checks statistical significance detection.
func TestComputeSignificance_ObviousDifference(t *testing.T) {
	oldScores := []float64{10.0, 11.0, 10.0, 12.0, 11.0} // Mean ~10.8
	newScores := []float64{90.0, 91.0, 89.0, 92.0, 90.0} // Mean ~90.4

	confident, pValue := computeSignificance(oldScores, newScores, 0.05)

	if !confident {
		t.Error("expected confident true for obvious difference")
	}
	if pValue >= 0.05 {
		t.Errorf("expected p-value < 0.05, got %f", pValue)
	}
	if pValue < 0 {
		t.Errorf("p-value should be non-negative, got %f", pValue)
	}
}

// TestComputeSignificance_NoDifference checks no significance for similar data.
func TestComputeSignificance_NoDifference(t *testing.T) {
	scores := []float64{50.0, 51.0, 49.0, 50.0, 51.0, 50.0, 49.0, 51.0}

	confident, pValue := computeSignificance(scores, scores, 0.05)

	if confident {
		t.Error("expected confident false for identical data")
	}
	if pValue < 0.5 { // p-value should be very high for identical data
		t.Errorf("expected p-value >= 0.5, got %f", pValue)
	}
}

// TestComputeSignificance_SingleSample handles edge case with minimal samples.
func TestComputeSignificance_SingleSample(t *testing.T) {
	oldScores := []float64{50.0}
	newScores := []float64{60.0}

	confident, pValue := computeSignificance(oldScores, newScores, 0.05)

	if confident {
		t.Error("expected confident false with single sample")
	}
	if pValue != 1.0 {
		t.Errorf("expected p-value 1.0 for single sample, got %f", pValue)
	}
}

// TestComputeSignificance_EmptySlices handles empty input gracefully.
func TestComputeSignificance_EmptySlices(t *testing.T) {
	confident, pValue := computeSignificance([]float64{}, []float64{1.0}, 0.05)

	if confident {
		t.Error("expected confident false with empty slice")
	}
	if pValue != 1.0 {
		t.Errorf("expected p-value 1.0 for empty slice, got %f", pValue)
	}
}

// TestRun_CancelByContext verifies context cancellation propagation.
func TestRun_CancelByContext(t *testing.T) {
	service := NewService(nil, nil, nil)

	// Scorer that simulates slow scoring.
	slowScorer := &slowScorer{delay: 100 * time.Millisecond}

	tester, err := NewRegressionTester(service, slowScorer)
	if err != nil {
		t.Fatalf("failed to create tester: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	cfg := RegressionConfig{
		OldStrategy:  "old",
		NewStrategy:  "new",
		BaselineRuns: 3,
		CompareRuns:  3,
	}

	start := time.Now()
	result, err := tester.Run(ctx, cfg)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("expected error from context cancellation")
	}
	if result != nil {
		t.Error("expected nil result on cancellation")
	}
	// Should have cancelled quickly, not waited for all runs.
	if elapsed > 200*time.Millisecond {
		t.Errorf("took too long to cancel: %v", elapsed)
	}
}

// TestRun_InvalidConfig checks configuration validation.
func TestRun_InvalidConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     RegressionConfig
		wantErr error
	}{
		{
			name: "nil old strategy",
			cfg: RegressionConfig{
				OldStrategy:  nil,
				NewStrategy:  "new",
				BaselineRuns: 5,
				CompareRuns:  5,
			},
			wantErr: ErrNilStrategy,
		},
		{
			name: "nil new strategy",
			cfg: RegressionConfig{
				OldStrategy:  "old",
				NewStrategy:  nil,
				BaselineRuns: 5,
				CompareRuns:  5,
			},
			wantErr: ErrNilStrategy,
		},
		{
			name: "negative baseline runs",
			cfg: RegressionConfig{
				OldStrategy:  "old",
				NewStrategy:  "new",
				BaselineRuns: -1,
				CompareRuns:  5,
			},
			wantErr: ErrInvalidRuns,
		},
		{
			name: "confidence out of range",
			cfg: RegressionConfig{
				OldStrategy:  "old",
				NewStrategy:  "new",
				BaselineRuns: 5,
				CompareRuns:  5,
				Confidence:   1.5,
			},
			wantErr: ErrConfidenceRange,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := NewService(nil, nil, nil)
			scorer := newMockScorer(map[int]float64{0: 1.0})
			tester, err := NewRegressionTester(service, scorer)
			if err != nil {
				t.Fatalf("failed to create tester: %v", err)
			}

			result, err := tester.Run(context.Background(), tt.cfg)
			if err == nil {
				t.Error("expected error but got none")
			}
			if result != nil {
				t.Error("expected nil result on validation error")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("expected error %v, got %v", tt.wantErr, err)
			}
		})
	}
}

// TestComputeMean verifies mean calculation.
func TestComputeMean(t *testing.T) {
	tests := []struct {
		name   string
		scores []float64
		want   float64
	}{
		{"empty slice", []float64{}, 0},
		{"single value", []float64{42.0}, 42.0},
		{"multiple values", []float64{10.0, 20.0, 30.0}, 20.0},
		{"negative values", []float64{-10.0, 10.0}, 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeMean(tt.scores)
			if mathAbs(got-tt.want) > 1e-9 {
				t.Errorf("computeMean(%v) = %f, want %f", tt.scores, got, tt.want)
			}
		})
	}
}

// TestComputeVariance verifies variance calculation.
func TestComputeVariance(t *testing.T) {
	tests := []struct {
		name   string
		scores []float64
		want   float64
	}{
		{"empty slice", []float64{}, 0},
		{"constant values", []float64{5.0, 5.0, 5.0}, 0.0},
		{"varying values", []float64{2.0, 4.0, 6.0}, 4.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeVariance(tt.scores)
			if mathAbs(got-tt.want) > 1e-6 {
				t.Errorf("computeVariance(%v) = %f, want %f", tt.scores, got, tt.want)
			}
		})
	}
}

// TestComputeWinRate verifies win rate calculation.
func TestComputeWinRate(t *testing.T) {
	tests := []struct {
		name      string
		oldScores []float64
		newScores []float64
		want      float64
	}{
		{"new wins all", []float64{1, 2, 3}, []float64{4, 5, 6}, 1.0},
		{"old wins all", []float64{4, 5, 6}, []float64{1, 2, 3}, 0.0},
		{"mixed results", []float64{5, 3, 7}, []float64{4, 6, 2}, 1.0 / 3.0},
		{"equal scores", []float64{5, 5, 5}, []float64{5, 5, 5}, 1.0},
		{"empty slices", []float64{}, []float64{}, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeWinRate(tt.oldScores, tt.newScores)
			if mathAbs(got-tt.want) > 1e-9 {
				t.Errorf("computeWinRate(%v, %v) = %f, want %f",
					tt.oldScores, tt.newScores, got, tt.want)
			}
		})
	}
}

// sequentialScorer returns scores in sequence for deterministic testing.
type sequentialScorer struct {
	scores []float64
	idx    int
	mu     sync.Mutex
}

// Score returns the next score in sequence.
func (s *sequentialScorer) Score(_ context.Context, runResult any) (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.idx >= len(s.scores) {
		return 0.0, errors.New("sequential scorer: no more scores")
	}
	score := s.scores[s.idx]
	s.idx++
	return score, nil
}

// slowScorer introduces artificial delay for timeout testing.
type slowScorer struct {
	delay time.Duration
}

// Score sleeps before returning to simulate slow processing.
func (s *slowScorer) Score(_ context.Context, runResult any) (float64, error) {
	time.Sleep(s.delay)
	return 75.0, nil
}

// compositeScorer routes scoring requests to different scorers based on input.
type compositeScorer struct {
	scorers map[any]Scorer
	mu      sync.Mutex
}

// Score routes to the appropriate scorer based on the input key.
// If the input is a TestCaseInput, it unpacks the embedded strategy as the key.
func (c *compositeScorer) Score(ctx context.Context, input any) (float64, error) {
	key := input
	if tci, ok := input.(TestCaseInput); ok {
		key = tci.Strategy
	}

	c.mu.Lock()
	scorer, ok := c.scorers[key]
	c.mu.Unlock()

	if !ok {
		return 0.0, fmt.Errorf("composite scorer: no scorer for input %v", input)
	}
	return scorer.Score(ctx, key)
}

// mathAbs is a helper to avoid importing math in test file.
func mathAbs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// TestRegressionTester_FingerprintCache_SameEnvReusesResult verifies the
// environment-fingerprint cache (原语4: 环境未变则不重跑): running the same
// config twice with a fingerprint function must return the memoized result on
// the second call without invoking the scorer again.
func TestRegressionTester_FingerprintCache_SameEnvReusesResult(t *testing.T) {
	scorer := newMockScorer(map[int]float64{
		0: 60.0, 1: 70.0, 2: 65.0, // old strategy: 3 runs
		3: 80.0, 4: 85.0, 5: 90.0, // new strategy: 3 runs
	})
	fp := func(cfg RegressionConfig) string {
		return cfg.TestSuite + "|" + fmt.Sprintf("%v", cfg.OldStrategy) + "|" + fmt.Sprintf("%v", cfg.NewStrategy)
	}
	rt, err := NewRegressionTesterWithScorer(scorer, WithFingerprint(fp))
	require.NoError(t, err)

	cfg := DefaultRegressionConfig()
	cfg.OldStrategy = "old-v1"
	cfg.NewStrategy = "new-v1"
	cfg.TestSuite = "suite-a"
	cfg.BaselineRuns = 3
	cfg.CompareRuns = 3

	first, err := rt.Run(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, first)
	callsAfterFirst := scorer.callCount
	require.Equal(t, 6, callsAfterFirst, "first run must score all runs")

	second, err := rt.Run(context.Background(), cfg)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.Equal(t, callsAfterFirst, scorer.callCount, "second run must be served from cache (no new scorer calls)")
	require.Equal(t, first.WinRate, second.WinRate, "cached result must match the original")
}

// TestRegressionTester_FingerprintCache_ChangedEnvReruns verifies that a
// different environment fingerprint bypasses the cache and re-scores.
func TestRegressionTester_FingerprintCache_ChangedEnvReruns(t *testing.T) {
	scorer := newMockScorer(map[int]float64{
		0: 60.0, 1: 70.0, 2: 65.0, 3: 80.0, 4: 85.0, 5: 90.0,
		6: 50.0, 7: 55.0, 8: 52.0, 9: 95.0, 10: 92.0, 11: 88.0,
	})
	fp := func(cfg RegressionConfig) string {
		return cfg.TestSuite + "|" + fmt.Sprintf("%v", cfg.OldStrategy) + "|" + fmt.Sprintf("%v", cfg.NewStrategy)
	}
	rt, err := NewRegressionTesterWithScorer(scorer, WithFingerprint(fp))
	require.NoError(t, err)

	cfg := DefaultRegressionConfig()
	cfg.OldStrategy = "old-v1"
	cfg.NewStrategy = "new-v1"
	cfg.TestSuite = "suite-a"
	cfg.BaselineRuns = 3
	cfg.CompareRuns = 3

	_, err = rt.Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Equal(t, 6, scorer.callCount)

	// Same suite but a new candidate strategy: environment changed, re-run.
	cfg.NewStrategy = "new-v2"
	_, err = rt.Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Equal(t, 12, scorer.callCount, "changed fingerprint must re-score")
}

// TestRegressionTester_FingerprintCache_NoFingerprintNoCache verifies that
// without WithFingerprint the tester never caches (backwards compatible).
func TestRegressionTester_FingerprintCache_NoFingerprintNoCache(t *testing.T) {
	scorer := newMockScorer(map[int]float64{
		0: 60.0, 1: 70.0, 2: 65.0, 3: 80.0, 4: 85.0, 5: 90.0,
		6: 61.0, 7: 71.0, 8: 66.0, 9: 81.0, 10: 86.0, 11: 91.0,
	})
	rt, err := NewRegressionTesterWithScorer(scorer)
	require.NoError(t, err)

	cfg := DefaultRegressionConfig()
	cfg.OldStrategy = "old"
	cfg.NewStrategy = "new"
	cfg.TestSuite = "suite"
	cfg.BaselineRuns = 3
	cfg.CompareRuns = 3

	_, err = rt.Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Equal(t, 6, scorer.callCount)

	_, err = rt.Run(context.Background(), cfg)
	require.NoError(t, err)
	require.Equal(t, 12, scorer.callCount, "no fingerprint function means every run re-scores")
}
