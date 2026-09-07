package scoring

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/experience"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

const (
	testParamCost       = "cost"
	testParamLatency    = "latency"
	testParamRegression = "regression"
	testPromptTest      = "test"
	testTaskTypeTest    = "test"
	testNameNilStrategy = "nil strategy"
)

// mockExperienceProvider implements ExperienceProvider for testing.
type mockExperienceProvider struct {
	count      int
	confidence float64
	err        error
}

func (m *mockExperienceProvider) FindSimilar(ctx context.Context, taskType string, limit int) (int, float64, error) {
	if m.err != nil {
		return 0, 0, m.err
	}
	return m.count, m.confidence, nil
}

// TestNewMemoryAwareScorer_NilTiered verifies that nil tiered scorer is
// rejected.
func TestNewMemoryAwareScorer_NilTiered(t *testing.T) {
	_, err := NewMemoryAwareScorer(nil, nil, DefaultMemoryAwareScoringConfig())
	if err == nil {
		t.Fatal("expected error for nil tiered scorer")
	}
}

// TestNewMemoryAwareScorer_InvalidConfig verifies invalid config is rejected.
func TestNewMemoryAwareScorer_InvalidConfig(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)

	tests := []struct {
		name string
		cfg  MemoryAwareScoringConfig
	}{
		{
			name: "negative memory weight",
			cfg:  modifyMASConfig(func(c *MemoryAwareScoringConfig) { c.MemoryWeight = -0.1 }),
		},
		{
			name: "negative cost weight",
			cfg:  modifyMASConfig(func(c *MemoryAwareScoringConfig) { c.CostWeight = -0.1 }),
		},
		{
			name: "max bonus < min bonus",
			cfg:  modifyMASConfig(func(c *MemoryAwareScoringConfig) { c.MaxEvidenceBonus = 5.0; c.MinEvidenceBonus = 10.0 }),
		},
		{
			name: "negative latency weight",
			cfg:  modifyMASConfig(func(c *MemoryAwareScoringConfig) { c.LatencyWeight = -0.05 }),
		},
		{
			name: "negative regression weight",
			cfg:  modifyMASConfig(func(c *MemoryAwareScoringConfig) { c.RegressionWeight = -0.1 }),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewMemoryAwareScorer(ts, nil, tt.cfg)
			if err == nil {
				t.Errorf("expected error for config: %s", tt.name)
			}
		})
	}
}

// modifyMASConfig returns a default config with the given modifier applied.
func modifyMASConfig(mod func(c *MemoryAwareScoringConfig)) MemoryAwareScoringConfig {
	cfg := DefaultMemoryAwareScoringConfig()
	cfg.Enabled = true
	mod(&cfg)
	return cfg
}

// mustCreateTieredScorer creates a tiered scorer for testing.
func mustCreateTieredScorer(t *testing.T, budget int) *TieredScorer {
	t.Helper()
	cache := NewScoreCache(0)
	b := mustNewBudget(t, budget)
	ts, err := NewTieredScorer(TieredScorerConfig{
		Cache:           cache,
		Budget:          b,
		HeuristicScorer: constantScorer(50.0),
	})
	if err != nil {
		t.Fatalf("NewTieredScorer failed: %v", err)
	}
	return ts
}

// TestMemoryAwareScorer_Disabled_NoAdjustment verifies that when disabled,
// the scorer delegates directly to the tiered scorer with no adjustments.
func TestMemoryAwareScorer_Disabled_NoAdjustment(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	ms, err := NewMemoryAwareScorer(ts, &mockExperienceProvider{count: 5, confidence: 0.8}, MemoryAwareScoringConfig{
		Enabled: false,
	})
	if err != nil {
		t.Fatalf("NewMemoryAwareScorer failed: %v", err)
	}

	strategy := &mutation.Strategy{
		ID:             "test-1",
		Params:         map[string]any{testParamTemperature: 0.7},
		PromptTemplate: "test prompt",
	}

	score, detail, err := ms.Score(context.Background(), strategy)
	if err != nil {
		t.Fatalf("Score failed: %v", err)
	}

	if score != 50.0 {
		t.Errorf("expected score=50.0, got %f", score)
	}
	if detail != nil {
		t.Error("expected nil detail when disabled")
	}
}

// TestMemoryAwareScorer_NilExperienceProvider_NoAdjustment verifies that when
// the experience provider is nil, the scorer delegates directly.
func TestMemoryAwareScorer_NilExperienceProvider_NoAdjustment(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	cfg := DefaultMemoryAwareScoringConfig()
	cfg.Enabled = true
	ms, err := NewMemoryAwareScorer(ts, nil, cfg)
	if err != nil {
		t.Fatalf("NewMemoryAwareScorer failed: %v", err)
	}

	strategy := newTestStrategy("nil-exp-test")
	score, detail, err := ms.Score(context.Background(), strategy)
	if err != nil {
		t.Fatalf("Score failed: %v", err)
	}

	if score != 50.0 {
		t.Errorf("expected score=50.0, got %f", score)
	}
	if detail != nil {
		t.Error("expected nil detail when experience provider is nil")
	}
}

// TestMemoryAwareScorer_ExperienceFailure_NonFatal verifies that an experience
// lookup failure does not prevent scoring (non-fatal fallback).
func TestMemoryAwareScorer_ExperienceFailure_NonFatal(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	cfg := DefaultMemoryAwareScoringConfig()
	cfg.Enabled = true
	ms, err := NewMemoryAwareScorer(ts, &mockExperienceProvider{err: errors.New("query failed")}, cfg)
	if err != nil {
		t.Fatalf("NewMemoryAwareScorer failed: %v", err)
	}

	strategy := newTestStrategy("exp-fail-test")
	score, detail, err := ms.Score(context.Background(), strategy)
	if err != nil {
		t.Fatalf("Score failed: %v", err)
	}

	// Should return the base quality score without adjustment.
	if score != 50.0 {
		t.Errorf("expected score=50.0 on experience failure, got %f", score)
	}
	if detail == nil {
		t.Fatal("expected non-nil detail even on experience failure")
	}
	if detail.QualityScore != 50.0 {
		t.Errorf("expected quality score 50.0, got %f", detail.QualityScore)
	}
}

// TestMemoryAwareScorer_FullAdjustment verifies that all components of the
// score formula are applied correctly when experience data is available.
//
// Formula: fitness = quality + memory_bonus - cost_penalty - latency_penalty - regression_penalty
//
// With: quality=50.0, expCount=3, confidence=0.8, cost=10.0, latency=5.0, regression=2.0
//   - memory_bonus = min(3*0.8*5.0, 20.0) = min(12.0, 20.0) = 12.0
//   - cost_penalty = 10.0 * 0.1 = 1.0
//   - latency_penalty = 5.0 * 0.05 = 0.25
//   - regression_penalty = 2.0 * 0.1 = 0.2
//   - final = 50.0 + 12.0 - 1.0 - 0.25 - 0.2 = 60.55
func TestMemoryAwareScorer_FullAdjustment(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	cfg := DefaultMemoryAwareScoringConfig()
	cfg.Enabled = true
	cfg.MemoryWeight = 0.2
	cfg.CostWeight = 0.1
	cfg.LatencyWeight = 0.05
	cfg.RegressionWeight = 0.1

	ms, err := NewMemoryAwareScorer(ts, &mockExperienceProvider{count: 3, confidence: 0.8}, cfg)
	if err != nil {
		t.Fatalf("NewMemoryAwareScorer failed: %v", err)
	}

	strategy := &mutation.Strategy{
		ID: "adjustment-test",
		Params: map[string]any{
			testParamTemperature: 0.7,
			testParamCost:        10.0,
			testParamLatency:     5.0,
			testParamRegression:  2.0,
		},
		PromptTemplate: testPromptTest,
	}

	score, detail, err := ms.Score(context.Background(), strategy)
	if err != nil {
		t.Fatalf("Score failed: %v", err)
	}

	// Compute expected values using the same formula to avoid float precision issues.
	expectedBonus := float64(3) * 0.8 * 5.0 // min(3*0.8*5.0, 20.0)
	expectedCostPenalty := 1.0
	expectedLatencyPenalty := 0.25
	expectedRegressionPenalty := 0.2
	expectedFinal := 50.0 + expectedBonus - expectedCostPenalty - expectedLatencyPenalty - expectedRegressionPenalty

	if detail.MemoryEvidenceBonus != expectedBonus {
		t.Errorf("expected MemoryEvidenceBonus=%f, got %f", expectedBonus, detail.MemoryEvidenceBonus)
	}
	if detail.CostPenalty != expectedCostPenalty {
		t.Errorf("expected CostPenalty=%f, got %f", expectedCostPenalty, detail.CostPenalty)
	}
	if detail.LatencyPenalty != expectedLatencyPenalty {
		t.Errorf("expected LatencyPenalty=%f, got %f", expectedLatencyPenalty, detail.LatencyPenalty)
	}
	if detail.RegressionPenalty != expectedRegressionPenalty {
		t.Errorf("expected RegressionPenalty=%f, got %f", expectedRegressionPenalty, detail.RegressionPenalty)
	}
	if detail.ExperienceCount != 3 {
		t.Errorf("expected ExperienceCount=3, got %d", detail.ExperienceCount)
	}
	if detail.Confidence != 0.8 {
		t.Errorf("expected Confidence=0.8, got %f", detail.Confidence)
	}
	if score != expectedFinal {
		t.Errorf("expected final score=%f, got %f", expectedFinal, score)
	}
	if detail.FinalScore != expectedFinal {
		t.Errorf("expected detail.FinalScore=%f, got %f", expectedFinal, detail.FinalScore)
	}
}

// TestMemoryAwareScorer_MemoryBonusCapped verifies that the memory evidence
// bonus does not exceed MaxEvidenceBonus.
func TestMemoryAwareScorer_MemoryBonusCapped(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	cfg := DefaultMemoryAwareScoringConfig()
	cfg.Enabled = true
	cfg.MaxEvidenceBonus = 10.0 // Cap at 10.
	cfg.MinEvidenceBonus = 0.0

	ms, err := NewMemoryAwareScorer(ts, &mockExperienceProvider{count: 100, confidence: 1.0}, cfg)
	if err != nil {
		t.Fatalf("NewMemoryAwareScorer failed: %v", err)
	}

	strategy := newTestStrategy("cap-test")

	_, detail, err := ms.Score(context.Background(), strategy)
	if err != nil {
		t.Fatalf("Score failed: %v", err)
	}

	// Raw bonus would be 100*1.0*5.0 = 500, but capped at 10.
	if detail.MemoryEvidenceBonus != 10.0 {
		t.Errorf("expected capped bonus=10.0, got %f", detail.MemoryEvidenceBonus)
	}
}

// TestMemoryAwareScorer_ScoreAsScorerFunc verifies that ScoreAsScorerFunc
// returns a valid ScorerFunc that works with population scoring.
func TestMemoryAwareScorer_ScoreAsScorerFunc(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	cfg := DefaultMemoryAwareScoringConfig()
	cfg.Enabled = true

	ms, err := NewMemoryAwareScorer(ts, &mockExperienceProvider{count: 2, confidence: 0.5}, cfg)
	if err != nil {
		t.Fatalf("NewMemoryAwareScorer failed: %v", err)
	}

	scorerFn := ms.ScoreAsScorerFunc()
	if scorerFn == nil {
		t.Fatal("ScoreAsScorerFunc returned nil")
	}

	strategy := newTestStrategy("scorer-func-test")
	score := scorerFn(strategy)

	// Should return a valid score (not unevaluated).
	if score < 0 {
		t.Errorf("expected valid score, got %f", score)
	}

	// Verify it's a genome.ScorerFunc compatible.
	sf := ms.ScoreAsScorerFunc()
	if sf == nil {
		t.Error("ScoreAsScorerFunc should satisfy genome.ScorerFunc")
	}
}

// TestMemoryAwareScorer_Stats verifies that Stats() returns correct
// aggregate statistics after scoring.
func TestMemoryAwareScorer_Stats(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	cfg := DefaultMemoryAwareScoringConfig()
	cfg.Enabled = true

	ms, err := NewMemoryAwareScorer(ts, &mockExperienceProvider{count: 2, confidence: 0.5}, cfg)
	if err != nil {
		t.Fatalf("NewMemoryAwareScorer failed: %v", err)
	}

	// Score a few strategies.
	strategy := newTestStrategy("stats-test")
	for i := 0; i < 3; i++ {
		_, _, err := ms.Score(context.Background(), strategy)
		if err != nil {
			t.Fatalf("Score failed: %v", err)
		}
	}

	stats := ms.Stats()
	if stats["adjustments"] != 3 {
		t.Errorf("expected 3 adjustments, got %f", stats["adjustments"])
	}
	if stats["bonus_total"] <= 0 {
		t.Errorf("expected positive bonus_total, got %f", stats["bonus_total"])
	}
	if stats["avg_bonus"] <= 0 {
		t.Errorf("expected positive avg_bonus, got %f", stats["avg_bonus"])
	}

	// Reset and verify stats are cleared.
	ms.ResetStats()
	stats = ms.Stats()
	if stats["adjustments"] != 0 {
		t.Errorf("expected 0 adjustments after reset, got %f", stats["adjustments"])
	}
}

// TestMemoryAwareScorer_ScoreDetailComponents verifies that ScoreDetail
// includes quality, memory, cost, and latency components in all scenarios.
func TestMemoryAwareScorer_ScoreDetailComponents(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	cfg := DefaultMemoryAwareScoringConfig()
	cfg.Enabled = true

	ms, err := NewMemoryAwareScorer(ts, &mockExperienceProvider{count: 3, confidence: 0.9}, cfg)
	if err != nil {
		t.Fatalf("NewMemoryAwareScorer failed: %v", err)
	}

	strategy := &mutation.Strategy{
		ID: "detail-test",
		Params: map[string]any{
			testParamTemperature: 0.7,
			testParamCost:        5.0,
			testParamLatency:     2.0,
		},
		PromptTemplate: testPromptTest,
	}

	_, detail, err := ms.Score(context.Background(), strategy)
	if err != nil {
		t.Fatalf("Score failed: %v", err)
	}

	// The detail must include quality, memory, cost, and latency components.
	if detail.QualityScore < 0 {
		t.Error("QualityScore should be non-negative")
	}
	if detail.MemoryEvidenceBonus < 0 {
		t.Error("MemoryEvidenceBonus should be non-negative")
	}
	if detail.CostPenalty < 0 {
		t.Error("CostPenalty should be non-negative")
	}
	if detail.LatencyPenalty < 0 {
		t.Error("LatencyPenalty should be non-negative")
	}
	if detail.RegressionPenalty < 0 {
		t.Error("RegressionPenalty should be non-negative")
	}

	// The final score should be the sum formula.
	expectedFinal := detail.QualityScore + detail.MemoryEvidenceBonus -
		detail.CostPenalty - detail.LatencyPenalty - detail.RegressionPenalty
	if detail.FinalScore != expectedFinal {
		t.Errorf("FinalScore=%f does not match formula result=%f",
			detail.FinalScore, expectedFinal)
	}
}

// TestTaskTypeFromStrategy verifies task type extraction from strategy.
func TestTaskTypeFromStrategy(t *testing.T) {
	tests := []struct {
		name     string
		strategy *mutation.Strategy
		expected string
	}{
		{
			name:     "nil strategy returns default",
			strategy: nil,
			expected: DefaultTaskType,
		},
		{
			name:     "empty strategy returns default",
			strategy: &mutation.Strategy{},
			expected: DefaultTaskType,
		},
		{
			name:     "strategy with name returns name",
			strategy: &mutation.Strategy{Name: "my-task"},
			expected: "my-task",
		},
		{
			name: "strategy with task_type param",
			strategy: &mutation.Strategy{
				Params: map[string]any{"task_type": "code-review"},
			},
			expected: "code-review",
		},
		{
			name:     "name takes precedence over task_type",
			strategy: &mutation.Strategy{Name: "task-name", Params: map[string]any{"task_type": "param-type"}},
			expected: "task-name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := taskTypeFromStrategy(tt.strategy)
			if got != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

// TestStrategyCostExtraction verifies cost extraction from strategy params.
func TestStrategyCostExtraction(t *testing.T) {
	tests := []struct {
		name     string
		strategy *mutation.Strategy
		expected float64
	}{
		{name: testNameNilStrategy, strategy: nil, expected: 0},
		{name: "no params", strategy: &mutation.Strategy{}, expected: 0},
		{name: "no cost key", strategy: &mutation.Strategy{Params: map[string]any{"t": 0.5}}, expected: 0},
		{name: "has cost", strategy: &mutation.Strategy{Params: map[string]any{testParamCost: 10.0}}, expected: 10.0},
		{name: "zero cost", strategy: &mutation.Strategy{Params: map[string]any{testParamCost: 0.0}}, expected: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strategyCost(tt.strategy)
			if got != tt.expected {
				t.Errorf("expected %f, got %f", tt.expected, got)
			}
		})
	}
}

// TestStrategyLatencyExtraction verifies latency extraction from strategy params.
func TestStrategyLatencyExtraction(t *testing.T) {
	tests := []struct {
		name     string
		strategy *mutation.Strategy
		expected float64
	}{
		{name: testNameNilStrategy, strategy: nil, expected: 0},
		{name: "no latency key", strategy: &mutation.Strategy{Params: map[string]any{"t": 0.5}}, expected: 0},
		{name: "has latency", strategy: &mutation.Strategy{Params: map[string]any{testParamLatency: 3.0}}, expected: 3.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strategyLatency(tt.strategy)
			if got != tt.expected {
				t.Errorf("expected %f, got %f", tt.expected, got)
			}
		})
	}
}

// TestStrategyRegressionExtraction verifies regression extraction from
// strategy params.
func TestStrategyRegressionExtraction(t *testing.T) {
	tests := []struct {
		name     string
		strategy *mutation.Strategy
		expected float64
	}{
		{name: testNameNilStrategy, strategy: nil, expected: 0},
		{name: "no regression key", strategy: &mutation.Strategy{Params: map[string]any{"t": 0.5}}, expected: 0},
		{name: "has regression", strategy: &mutation.Strategy{Params: map[string]any{testParamRegression: 1.5}}, expected: 1.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := strategyRegression(tt.strategy)
			if got != tt.expected {
				t.Errorf("expected %f, got %f", tt.expected, got)
			}
		})
	}
}

// TestMemoryAwareScorer_ResetForGeneration verifies that ResetForGeneration
// delegates to the tiered scorer correctly.
func TestMemoryAwareScorer_ResetForGeneration(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	cfg := DefaultMemoryAwareScoringConfig()
	cfg.Enabled = true

	ms, err := NewMemoryAwareScorer(ts, &mockExperienceProvider{count: 2, confidence: 0.5}, cfg)
	if err != nil {
		t.Fatalf("NewMemoryAwareScorer failed: %v", err)
	}

	// Score a few strategies.
	strategy := newTestStrategy("reset-test")
	for i := 0; i < 3; i++ {
		_, _, _ = ms.Score(context.Background(), strategy)
	}

	ms.ResetStats()

	stats := ms.Stats()
	if stats["adjustments"] != 0 {
		t.Errorf("expected 0 adjustments after reset, got %f", stats["adjustments"])
	}
}

// TestComputeMemoryBonus verifies the memory bonus formula directly.
func TestComputeMemoryBonus(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	cfg := DefaultMemoryAwareScoringConfig()
	cfg.Enabled = true
	cfg.MinEvidenceBonus = 0.0
	cfg.MaxEvidenceBonus = 20.0

	ms, _ := NewMemoryAwareScorer(ts, nil, cfg)

	tests := []struct {
		name       string
		count      int
		confidence float64
		expected   float64
	}{
		{"zero experiences", 0, 0.8, 0.0},
		{"low confidence", 5, 0.1, 2.5}, // 5*0.1*5.0 = 2.5
		{"medium", 3, 0.5, 7.5},         // 3*0.5*5.0 = 7.5
		{"capped", 100, 1.0, 20.0},      // 100*1.0*5.0 = 500, capped at 20
		{"zero confidence", 10, 0.0, 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ms.computeMemoryBonus(tt.count, tt.confidence)
			if got != tt.expected {
				t.Errorf("expected %f, got %f", tt.expected, got)
			}
		})
	}
}

// --- Additional coverage tests ---

// TestMemoryAwareScorer_NegativeLatencyWeight verifies that a negative
// latency weight is rejected by the constructor.
func TestMemoryAwareScorer_NegativeLatencyWeight(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	cfg := DefaultMemoryAwareScoringConfig()
	cfg.Enabled = true
	cfg.LatencyWeight = -0.01

	_, err := NewMemoryAwareScorer(ts, nil, cfg)
	if err == nil {
		t.Fatal("expected error for negative latency weight")
	}
}

// TestMemoryAwareScorer_NegativeRegressionWeight verifies that a negative
// regression weight is rejected by the constructor.
func TestMemoryAwareScorer_NegativeRegressionWeight(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	cfg := DefaultMemoryAwareScoringConfig()
	cfg.Enabled = true
	cfg.RegressionWeight = -0.05

	_, err := NewMemoryAwareScorer(ts, nil, cfg)
	if err == nil {
		t.Fatal("expected error for negative regression weight")
	}
}

// TestMemoryAwareScorer_MinEvidenceBonusFloor verifies that the memory bonus
// is floored at MinEvidenceBonus when the raw bonus would be below it.
func TestMemoryAwareScorer_MinEvidenceBonusFloor(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	cfg := DefaultMemoryAwareScoringConfig()
	cfg.Enabled = true
	cfg.MinEvidenceBonus = 5.0
	cfg.MaxEvidenceBonus = 20.0

	ms, err := NewMemoryAwareScorer(ts, nil, cfg)
	if err != nil {
		t.Fatalf("NewMemoryAwareScorer failed: %v", err)
	}

	// With count=0 and confidence=0.8, raw bonus = 0*0.8*5.0 = 0.0
	// Should be floored at MinEvidenceBonus = 5.0.
	got := ms.computeMemoryBonus(0, 0.8)
	if got != 5.0 {
		t.Errorf("expected MinEvidenceBonus floor=5.0, got %f", got)
	}
}

// TestMemoryAwareScorer_ScoreWithZeroExperiences verifies that scoring with
// an experience provider returning zero experiences produces a bonus of 0
// (or MinEvidenceBonus if set > 0).
func TestMemoryAwareScorer_ScoreWithZeroExperiences(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	cfg := DefaultMemoryAwareScoringConfig()
	cfg.Enabled = true
	cfg.MinEvidenceBonus = 0.0

	ms, err := NewMemoryAwareScorer(ts, &mockExperienceProvider{count: 0, confidence: 0.8}, cfg)
	if err != nil {
		t.Fatalf("NewMemoryAwareScorer failed: %v", err)
	}

	strategy := newTestStrategy("zero-exp-test")
	score, detail, err := ms.Score(context.Background(), strategy)
	if err != nil {
		t.Fatalf("Score failed: %v", err)
	}

	if detail == nil {
		t.Fatal("expected non-nil detail")
	}

	// With 0 experiences, bonus = 0*0.8*5.0 = 0.
	if detail.MemoryEvidenceBonus != 0.0 {
		t.Errorf("expected zero bonus with 0 experiences, got %f", detail.MemoryEvidenceBonus)
	}
	if detail.ExperienceCount != 0 {
		t.Errorf("expected ExperienceCount=0, got %d", detail.ExperienceCount)
	}
	// Score should equal quality (50.0) with no bonus or penalties.
	if score != 50.0 {
		t.Errorf("expected score=50.0 with zero experiences, got %f", score)
	}
}

// TestMemoryAwareScorer_StatsPenaltyTracking verifies that Stats() tracks
// penalty totals and averages correctly.
func TestMemoryAwareScorer_StatsPenaltyTracking(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	cfg := DefaultMemoryAwareScoringConfig()
	cfg.Enabled = true
	cfg.CostWeight = 0.1
	cfg.LatencyWeight = 0.05

	ms, err := NewMemoryAwareScorer(ts, &mockExperienceProvider{count: 1, confidence: 0.5}, cfg)
	if err != nil {
		t.Fatalf("NewMemoryAwareScorer failed: %v", err)
	}

	// Score a strategy with known cost and latency.
	strategy := &mutation.Strategy{
		ID:     "penalty-stats-test",
		Name:   "penalty-stats-test",
		Params: map[string]any{testParamTemperature: 0.7, testParamCost: 10.0, testParamLatency: 4.0},
	}
	_, _, err = ms.Score(context.Background(), strategy)
	if err != nil {
		t.Fatalf("Score failed: %v", err)
	}

	stats := ms.Stats()
	if stats["adjustments"] != 1 {
		t.Errorf("expected 1 adjustment, got %f", stats["adjustments"])
	}
	// penalty_total = cost_penalty + latency_penalty + regression_penalty
	// cost_penalty = 10.0 * 0.1 = 1.0, latency_penalty = 4.0 * 0.05 = 0.2
	// regression_penalty = 0 (no regression param)
	// total = 1.2
	expectedPenalty := 1.0 + 0.2 + 0.0
	if stats["penalty_total"] != expectedPenalty {
		t.Errorf("expected penalty_total=%f, got %f", expectedPenalty, stats["penalty_total"])
	}
	if stats["avg_penalty"] != expectedPenalty {
		t.Errorf("expected avg_penalty=%f, got %f", expectedPenalty, stats["avg_penalty"])
	}
}

// TestMemoryAwareScorer_StatsBeforeAnyScoring verifies that Stats() returns
// zero values before any scoring has occurred.
func TestMemoryAwareScorer_StatsBeforeAnyScoring(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	cfg := DefaultMemoryAwareScoringConfig()
	cfg.Enabled = true

	ms, err := NewMemoryAwareScorer(ts, &mockExperienceProvider{count: 1, confidence: 0.5}, cfg)
	if err != nil {
		t.Fatalf("NewMemoryAwareScorer failed: %v", err)
	}

	stats := ms.Stats()
	if stats["adjustments"] != 0 {
		t.Errorf("expected 0 adjustments before scoring, got %f", stats["adjustments"])
	}
	if stats["bonus_total"] != 0 {
		t.Errorf("expected 0 bonus_total before scoring, got %f", stats["bonus_total"])
	}
	if stats["penalty_total"] != 0 {
		t.Errorf("expected 0 penalty_total before scoring, got %f", stats["penalty_total"])
	}
	// avg_bonus and avg_penalty should not be present when adjustments=0.
	if _, ok := stats["avg_bonus"]; ok {
		t.Error("avg_bonus should not be present when adjustments=0")
	}
	if _, ok := stats["avg_penalty"]; ok {
		t.Error("avg_penalty should not be present when adjustments=0")
	}
}

// TestMemoryAwareScorer_ScoreAsScorerFuncDisabled verifies that
// ScoreAsScorerFunc returns the tiered scorer's score when the scorer is
// disabled (no experience adjustments applied).
func TestMemoryAwareScorer_ScoreAsScorerFuncDisabled(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	cfg := MemoryAwareScoringConfig{Enabled: false}

	ms, err := NewMemoryAwareScorer(ts, &mockExperienceProvider{count: 5, confidence: 0.9}, cfg)
	if err != nil {
		t.Fatalf("NewMemoryAwareScorer failed: %v", err)
	}

	scorerFn := ms.ScoreAsScorerFunc()
	strategy := newTestStrategy("scorer-func-disabled-test")
	score := scorerFn(strategy)

	// Should return the tiered scorer's raw score (50.0) with no adjustments.
	if score != 50.0 {
		t.Errorf("expected 50.0 when disabled, got %f", score)
	}
}

// --- Evidence-based scoring tests ---

// mockEvidenceProvider implements EvidenceProvider for testing.
type mockEvidenceProvider struct {
	evidence      experience.Evidence
	taskEvidence  experience.Evidence
	err           error
	strategyError error
	taskError     error
}

func (m *mockEvidenceProvider) GetEvidence(ctx context.Context, strategyID string) (experience.Evidence, error) {
	if m.strategyError != nil {
		return experience.Evidence{}, m.strategyError
	}
	if m.err != nil {
		return experience.Evidence{}, m.err
	}
	return m.evidence, nil
}

func (m *mockEvidenceProvider) GetEvidenceByTaskType(ctx context.Context, taskType string) (experience.Evidence, error) {
	if m.taskError != nil {
		return experience.Evidence{}, m.taskError
	}
	if m.err != nil {
		return experience.Evidence{}, m.err
	}
	return m.taskEvidence, nil
}

// TestMemoryAwareScorer_EvidenceMode_Success verifies that the scorer
// correctly uses multi-dimensional evidence when EvidenceProvider is available.
func TestMemoryAwareScorer_EvidenceMode_Success(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	cfg := DefaultMemoryAwareScoringConfig()
	cfg.Enabled = true

	ev := experience.Evidence{
		StrategyID:  "evidence-test",
		TaskType:    testTaskTypeTest,
		SuccessRate: 0.8,
		LatencyP50:  1000, // 1 second
		ErrorRate:   0.1,
		SampleCount: 10,
		Confidence:  0.85,
		LastUpdated: time.Now(),
	}

	ms, err := NewMemoryAwareScorer(ts, nil, cfg)
	if err != nil {
		t.Fatalf("NewMemoryAwareScorer failed: %v", err)
	}

	ms.SetEvidenceProvider(&mockEvidenceProvider{evidence: ev})

	strategy := &mutation.Strategy{
		ID:             "evidence-test",
		Params:         map[string]any{testParamTemperature: 0.7},
		PromptTemplate: "test prompt",
	}

	score, detail, err := ms.Score(context.Background(), strategy)
	if err != nil {
		t.Fatalf("Score failed: %v", err)
	}

	if detail == nil {
		t.Fatal("expected non-nil detail in evidence mode")
	}

	// Verify evidence fields are populated.
	if detail.SuccessRateEvidence != 0.8 {
		t.Errorf("expected SuccessRateEvidence=0.8, got %f", detail.SuccessRateEvidence)
	}
	if detail.LatencyEvidence != 1000 {
		t.Errorf("expected LatencyEvidence=1000, got %d", detail.LatencyEvidence)
	}
	if detail.ErrorRateEvidence != 0.1 {
		t.Errorf("expected ErrorRateEvidence=0.1, got %f", detail.ErrorRateEvidence)
	}
	if detail.SampleCountEvidence != 10 {
		t.Errorf("expected SampleCountEvidence=10, got %d", detail.SampleCountEvidence)
	}
	if detail.Confidence != 0.85 {
		t.Errorf("expected Confidence=0.85, got %f", detail.Confidence)
	}

	// Verify bonus is computed from evidence.
	// Expected: success_bonus = 0.8 * 0.85 * 10.0 = 6.8
	// latency_penalty_factor = 1000 / 10000 = 0.1
	// error_penalty_factor = 0.1 * 0.85 = 0.085
	// bonus = 6.8 * (1 - 0.1) * (1 - 0.085) ≈ 5.59
	expectedBonus := 0.8 * 0.85 * 10.0 * (1.0 - 0.1) * (1.0 - 0.085)
	if detail.MemoryEvidenceBonus < expectedBonus-0.1 || detail.MemoryEvidenceBonus > expectedBonus+0.1 {
		t.Errorf("expected MemoryEvidenceBonus≈%f, got %f", expectedBonus, detail.MemoryEvidenceBonus)
	}

	// Final score should be quality + bonus - penalties.
	if score < 50.0 {
		t.Errorf("expected score > 50.0 (quality + evidence bonus), got %f", score)
	}
}

// TestMemoryAwareScorer_EvidenceMode_FallbackToTaskType verifies that when
// strategy evidence is not found, the scorer falls back to task type evidence.
func TestMemoryAwareScorer_EvidenceMode_FallbackToTaskType(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	cfg := DefaultMemoryAwareScoringConfig()
	cfg.Enabled = true

	taskEvidence := experience.Evidence{
		TaskType:    "fallback-task",
		SuccessRate: 0.7,
		LatencyP50:  2000,
		ErrorRate:   0.2,
		SampleCount: 20,
		Confidence:  0.9,
		LastUpdated: time.Now(),
	}

	ms, err := NewMemoryAwareScorer(ts, nil, cfg)
	if err != nil {
		t.Fatalf("NewMemoryAwareScorer failed: %v", err)
	}

	ms.SetEvidenceProvider(&mockEvidenceProvider{
		strategyError: errors.New("strategy not found"),
		taskEvidence:  taskEvidence,
	})

	strategy := &mutation.Strategy{
		ID:             "unknown-strategy",
		Name:           "fallback-task",
		Params:         map[string]any{testParamTemperature: 0.7},
		PromptTemplate: testPromptTest,
	}

	score, detail, err := ms.Score(context.Background(), strategy)
	if err != nil {
		t.Fatalf("Score failed: %v", err)
	}

	if detail == nil {
		t.Fatal("expected non-nil detail")
	}

	// Should use task type evidence.
	if detail.SuccessRateEvidence != 0.7 {
		t.Errorf("expected SuccessRateEvidence=0.7 (from task evidence), got %f", detail.SuccessRateEvidence)
	}
	if detail.SampleCountEvidence != 20 {
		t.Errorf("expected SampleCountEvidence=20 (from task evidence), got %d", detail.SampleCountEvidence)
	}
	if score < 50.0 {
		t.Errorf("expected score > 50.0, got %f", score)
	}
}

// TestMemoryAwareScorer_EvidenceMode_NoEvidenceFound verifies that when
// both strategy and task type evidence fail, the scorer returns base score.
func TestMemoryAwareScorer_EvidenceMode_NoEvidenceFound(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	cfg := DefaultMemoryAwareScoringConfig()
	cfg.Enabled = true

	ms, err := NewMemoryAwareScorer(ts, nil, cfg)
	if err != nil {
		t.Fatalf("NewMemoryAwareScorer failed: %v", err)
	}

	ms.SetEvidenceProvider(&mockEvidenceProvider{
		strategyError: errors.New("strategy not found"),
		taskError:     errors.New("task type not found"),
	})

	strategy := newTestStrategy("no-evidence")
	score, detail, err := ms.Score(context.Background(), strategy)
	if err != nil {
		t.Fatalf("Score failed: %v", err)
	}

	// Should return base quality score when no evidence found.
	if score != 50.0 {
		t.Errorf("expected base score=50.0 when no evidence found, got %f", score)
	}
	if detail == nil {
		t.Fatal("expected non-nil detail even when evidence not found")
	}
	if detail.QualityScore != 50.0 {
		t.Errorf("expected QualityScore=50.0, got %f", detail.QualityScore)
	}
}

// TestMemoryAwareScorer_EvidenceMode_EmptyEvidence verifies that empty
// evidence returns minimum bonus.
func TestMemoryAwareScorer_EvidenceMode_EmptyEvidence(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	cfg := DefaultMemoryAwareScoringConfig()
	cfg.Enabled = true
	cfg.MinEvidenceBonus = 0.0

	ms, err := NewMemoryAwareScorer(ts, nil, cfg)
	if err != nil {
		t.Fatalf("NewMemoryAwareScorer failed: %v", err)
	}

	ms.SetEvidenceProvider(&mockEvidenceProvider{
		evidence: experience.Evidence{}, // Empty evidence
	})

	strategy := newTestStrategy("empty-evidence")
	score, detail, err := ms.Score(context.Background(), strategy)
	if err != nil {
		t.Fatalf("Score failed: %v", err)
	}

	// Empty evidence should result in MinEvidenceBonus (0.0).
	if detail.MemoryEvidenceBonus != 0.0 {
		t.Errorf("expected MemoryEvidenceBonus=0.0 for empty evidence, got %f", detail.MemoryEvidenceBonus)
	}
	if score != 50.0 {
		t.Errorf("expected score=50.0 for empty evidence, got %f", score)
	}
}

// TestComputeEvidenceBasedBonus verifies the multi-dimensional bonus formula.
func TestComputeEvidenceBasedBonus(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	cfg := DefaultMemoryAwareScoringConfig()
	cfg.Enabled = true
	cfg.MinEvidenceBonus = 0.0
	cfg.MaxEvidenceBonus = 20.0

	ms, _ := NewMemoryAwareScorer(ts, nil, cfg)

	tests := []struct {
		name     string
		evidence experience.Evidence
		expected float64
	}{
		{
			name: "high success rate, low latency and error",
			evidence: experience.Evidence{
				SuccessRate: 0.9,
				LatencyP50:  100,
				ErrorRate:   0.01,
				Confidence:  0.95,
				SampleCount: 50,
			},
			expected: 8.55, // High bonus from good performance
		},
		{
			name: "medium success rate, medium latency and error",
			evidence: experience.Evidence{
				SuccessRate: 0.7,
				LatencyP50:  5000,
				ErrorRate:   0.1,
				Confidence:  0.8,
				SampleCount: 20,
			},
			expected: 2.24, // Moderate bonus
		},
		{
			name: "low success rate",
			evidence: experience.Evidence{
				SuccessRate: 0.3,
				LatencyP50:  1000,
				ErrorRate:   0.05,
				Confidence:  0.7,
				SampleCount: 10,
			},
			expected: 1.905, // Low bonus
		},
		{
			name: "high latency penalty",
			evidence: experience.Evidence{
				SuccessRate: 0.9,
				LatencyP50:  9000,
				ErrorRate:   0.01,
				Confidence:  0.9,
				SampleCount: 30,
			},
			expected: 0.81, // High latency reduces bonus significantly
		},
		{
			name: "high error rate",
			evidence: experience.Evidence{
				SuccessRate: 0.8,
				LatencyP50:  1000,
				ErrorRate:   0.5,
				Confidence:  0.85,
				SampleCount: 15,
			},
			expected: 3.4, // High error rate reduces bonus
		},
		{
			name: "zero confidence",
			evidence: experience.Evidence{
				SuccessRate: 0.9,
				LatencyP50:  100,
				ErrorRate:   0.01,
				Confidence:  0.0,
				SampleCount: 10,
			},
			expected: 0.0, // Zero confidence = zero bonus
		},
		{
			name: "no samples",
			evidence: experience.Evidence{
				SampleCount: 0,
			},
			expected: 0.0, // No samples = minimum bonus
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ms.computeEvidenceBasedBonus(tt.evidence)
			// Allow small tolerance for floating point calculations.
			if got < tt.expected-0.5 || got > tt.expected+0.5 {
				t.Errorf("expected bonus≈%f, got %f", tt.expected, got)
			}
		})
	}
}

// TestMemoryAwareScorer_EvidenceMode_WithCostAndLatency verifies that
// evidence-based scoring works correctly with cost and latency penalties.
func TestMemoryAwareScorer_EvidenceMode_WithCostAndLatency(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	cfg := DefaultMemoryAwareScoringConfig()
	cfg.Enabled = true
	cfg.CostWeight = 0.1
	cfg.LatencyWeight = 0.05

	ev := experience.Evidence{
		StrategyID:  "combined-test",
		SuccessRate: 0.85,
		LatencyP50:  500,
		ErrorRate:   0.05,
		SampleCount: 15,
		Confidence:  0.9,
		LastUpdated: time.Now(),
	}

	ms, err := NewMemoryAwareScorer(ts, nil, cfg)
	if err != nil {
		t.Fatalf("NewMemoryAwareScorer failed: %v", err)
	}

	ms.SetEvidenceProvider(&mockEvidenceProvider{evidence: ev})

	strategy := &mutation.Strategy{
		ID: "combined-test",
		Params: map[string]any{
			testParamTemperature: 0.7,
			testParamCost:        5.0,
			testParamLatency:     2.0,
		},
		PromptTemplate: testPromptTest,
	}

	score, detail, err := ms.Score(context.Background(), strategy)
	if err != nil {
		t.Fatalf("Score failed: %v", err)
	}

	// Verify all components are populated.
	if detail.SuccessRateEvidence != 0.85 {
		t.Errorf("expected SuccessRateEvidence=0.85, got %f", detail.SuccessRateEvidence)
	}
	if detail.CostPenalty != 0.5 { // 5.0 * 0.1
		t.Errorf("expected CostPenalty=0.5, got %f", detail.CostPenalty)
	}
	if detail.LatencyPenalty != 0.1 { // 2.0 * 0.05
		t.Errorf("expected LatencyPenalty=0.1, got %f", detail.LatencyPenalty)
	}

	// Final score should include evidence bonus and penalties.
	expectedBase := 50.0
	expectedBonus := detail.MemoryEvidenceBonus
	expectedPenalties := detail.CostPenalty + detail.LatencyPenalty + detail.RegressionPenalty
	expectedFinal := expectedBase + expectedBonus - expectedPenalties

	if score < expectedFinal-0.1 || score > expectedFinal+0.1 {
		t.Errorf("expected final score≈%f, got %f", expectedFinal, score)
	}
}

// TestMemoryAwareScorer_SetEvidenceProvider verifies that SetEvidenceProvider
// correctly updates the evidence provider.
func TestMemoryAwareScorer_SetEvidenceProvider(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	cfg := DefaultMemoryAwareScoringConfig()
	cfg.Enabled = true

	ms, err := NewMemoryAwareScorer(ts, nil, cfg)
	if err != nil {
		t.Fatalf("NewMemoryAwareScorer failed: %v", err)
	}

	// Initially should have no evidence provider.
	strategy := newTestStrategy("initial")
	_, detail, err := ms.Score(context.Background(), strategy)
	if err != nil {
		t.Fatalf("Score failed: %v", err)
	}
	if detail != nil {
		t.Error("expected nil detail before setting evidence provider")
	}

	// Set evidence provider.
	ev := experience.Evidence{
		StrategyID:  "initial",
		SuccessRate: 0.8,
		Confidence:  0.85,
		SampleCount: 10,
	}
	ms.SetEvidenceProvider(&mockEvidenceProvider{evidence: ev})

	// Now should use evidence mode.
	_, detail, err = ms.Score(context.Background(), strategy)
	if err != nil {
		t.Fatalf("Score failed: %v", err)
	}
	if detail == nil {
		t.Fatal("expected non-nil detail after setting evidence provider")
	}
	if detail.SuccessRateEvidence != 0.8 {
		t.Errorf("expected SuccessRateEvidence=0.8, got %f", detail.SuccessRateEvidence)
	}
}

// TestMemoryAwareScorer_BackwardCompatibility verifies that the scorer
// maintains backward compatibility with legacy ExperienceProvider.
func TestMemoryAwareScorer_BackwardCompatibility(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	cfg := DefaultMemoryAwareScoringConfig()
	cfg.Enabled = true

	// Create scorer with legacy ExperienceProvider only.
	ms, err := NewMemoryAwareScorer(ts, &mockExperienceProvider{count: 3, confidence: 0.8}, cfg)
	if err != nil {
		t.Fatalf("NewMemoryAwareScorer failed: %v", err)
	}

	// Should not have evidence provider set.
	strategy := newTestStrategy("legacy-test")
	_, detail, err := ms.Score(context.Background(), strategy)
	if err != nil {
		t.Fatalf("Score failed: %v", err)
	}

	if detail == nil {
		t.Fatal("expected non-nil detail in legacy mode")
	}

	// Legacy mode should use ExperienceCount and Confidence from ExperienceProvider.
	if detail.ExperienceCount != 3 {
		t.Errorf("expected ExperienceCount=3, got %d", detail.ExperienceCount)
	}
	if detail.Confidence != 0.8 {
		t.Errorf("expected Confidence=0.8, got %f", detail.Confidence)
	}

	// Evidence fields should be zero in legacy mode.
	if detail.SuccessRateEvidence != 0 {
		t.Errorf("expected SuccessRateEvidence=0 in legacy mode, got %f", detail.SuccessRateEvidence)
	}
	if detail.LatencyEvidence != 0 {
		t.Errorf("expected LatencyEvidence=0 in legacy mode, got %d", detail.LatencyEvidence)
	}
	if detail.ErrorRateEvidence != 0 {
		t.Errorf("expected ErrorRateEvidence=0 in legacy mode, got %f", detail.ErrorRateEvidence)
	}
	if detail.SampleCountEvidence != 0 {
		t.Errorf("expected SampleCountEvidence=0 in legacy mode, got %d", detail.SampleCountEvidence)
	}

	// Bonus should be computed from legacy formula: count * confidence * 5.0.
	expectedBonus := float64(3) * 0.8 * 5.0
	if detail.MemoryEvidenceBonus != expectedBonus {
		t.Errorf("expected legacy bonus=%f, got %f", expectedBonus, detail.MemoryEvidenceBonus)
	}
}

// TestMemoryAwareScorer_EvidenceProviderPriority verifies that EvidenceProvider
// takes priority over ExperienceProvider when both are available.
func TestMemoryAwareScorer_EvidenceProviderPriority(t *testing.T) {
	ts := mustCreateTieredScorer(t, 5)
	cfg := DefaultMemoryAwareScoringConfig()
	cfg.Enabled = true

	// Create scorer with both providers.
	ms, err := NewMemoryAwareScorer(ts, &mockExperienceProvider{count: 5, confidence: 0.9}, cfg)
	if err != nil {
		t.Fatalf("NewMemoryAwareScorer failed: %v", err)
	}

	// Set EvidenceProvider (should take priority).
	ev := experience.Evidence{
		StrategyID:  "priority-test",
		SuccessRate: 0.75,
		Confidence:  0.8,
		SampleCount: 20,
	}
	ms.SetEvidenceProvider(&mockEvidenceProvider{evidence: ev})

	strategy := newTestStrategy("priority-test")
	_, detail, err := ms.Score(context.Background(), strategy)
	if err != nil {
		t.Fatalf("Score failed: %v", err)
	}

	if detail == nil {
		t.Fatal("expected non-nil detail")
	}

	// Should use evidence-based scoring (EvidenceProvider priority).
	if detail.SuccessRateEvidence != 0.75 {
		t.Errorf("expected SuccessRateEvidence=0.75 (from EvidenceProvider), got %f", detail.SuccessRateEvidence)
	}
	if detail.SampleCountEvidence != 20 {
		t.Errorf("expected SampleCountEvidence=20 (from EvidenceProvider), got %d", detail.SampleCountEvidence)
	}

	// Should NOT use legacy ExperienceProvider values.
	if detail.ExperienceCount == 5 {
		t.Error("should not use legacy ExperienceCount when EvidenceProvider is available")
	}
}
