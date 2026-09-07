package scoring

import (
	"context"
	"errors"
	"testing"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// Reuse testParamTemperature from cached_scorer_test.go (same package)

// newTestStrategy creates a simple strategy for testing.
func newTestStrategy(name string) *mutation.Strategy {
	return &mutation.Strategy{
		ID:             name,
		Name:           name,
		Version:        1,
		Params:         map[string]any{testParamTemperature: 0.7},
		PromptTemplate: "test prompt for " + name,
	}
}

// constantScorer returns a genome.ScorerFunc that always returns the given score.
func constantScorer(score float64) genome.ScorerFunc {
	return func(s *mutation.Strategy) float64 {
		return score
	}
}

// panickingScorer returns a scorer that panics with the given error.
func panickingScorer(err error) genome.ScorerFunc {
	return func(s *mutation.Strategy) float64 {
		panic(err)
	}
}

func TestNewTieredScorer(t *testing.T) {
	tests := []struct {
		name    string
		giveCfg TieredScorerConfig
		wantErr error
	}{
		{
			name: "nil cache rejected",
			giveCfg: TieredScorerConfig{
				Cache:           nil,
				Budget:          mustNewBudget(t, 5),
				HeuristicScorer: constantScorer(0.5),
			},
			wantErr: ErrNilTieredCache,
		},
		{
			name: "nil budget rejected",
			giveCfg: TieredScorerConfig{
				Cache:           NewScoreCache(0),
				Budget:          nil,
				HeuristicScorer: constantScorer(0.5),
			},
			wantErr: ErrNilBudget,
		},
		{
			name: "nil heuristic scorer rejected",
			giveCfg: TieredScorerConfig{
				Cache:           NewScoreCache(0),
				Budget:          mustNewBudget(t, 5),
				HeuristicScorer: nil,
			},
			wantErr: ErrNilHeuristicScorer,
		},
		{
			name: "valid config with LLM scorer",
			giveCfg: TieredScorerConfig{
				Cache:           NewScoreCache(0),
				Budget:          mustNewBudget(t, 5),
				HeuristicScorer: constantScorer(0.3),
				LLMScorer:       constantScorer(0.9),
			},
			wantErr: nil,
		},
		{
			name: "valid config without LLM scorer",
			giveCfg: TieredScorerConfig{
				Cache:           NewScoreCache(0),
				Budget:          mustNewBudget(t, 5),
				HeuristicScorer: constantScorer(0.3),
				LLMScorer:       nil,
			},
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts, err := NewTieredScorer(tt.giveCfg)
			if tt.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error %v, got nil", tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ts == nil {
				t.Fatal("expected non-nil tiered scorer")
			}
		})
	}
}

func TestScoreCacheHit(t *testing.T) {
	cache := NewScoreCache(0)
	budget := mustNewBudget(t, 5)
	s := newTestStrategy("cache-hit")

	// Pre-populate cache.
	hash, _ := StrategyHash(s)
	cache.Put(hash, MakeEntry(hash, 0.85, ScorerTypeLLM, 1, 1.0))

	ts, _ := NewTieredScorer(TieredScorerConfig{
		Cache:           cache,
		Budget:          budget,
		HeuristicScorer: constantScorer(0.3),
		LLMScorer:       constantScorer(0.9),
	})

	score, tier, err := ts.Score(context.Background(), s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if score != 0.85 {
		t.Errorf("score = %f, want 0.85", score)
	}
	if tier != TierCache {
		t.Errorf("tier = %s, want %s", tier, TierCache)
	}

	// Verify no LLM call was made.
	used, _, hits, _ := budget.Usage()
	if used != 0 {
		t.Errorf("LLM calls used = %d, want 0 (cache hit)", used)
	}
	if hits != 1 {
		t.Errorf("cache hits = %d, want 1", hits)
	}
}

func TestScoreUsesLLMWhenBudgetAvailable(t *testing.T) {
	cache := NewScoreCache(0)
	budget := mustNewBudget(t, 5)
	s := newTestStrategy("llm-score")

	ts, _ := NewTieredScorer(TieredScorerConfig{
		Cache:           cache,
		Budget:          budget,
		HeuristicScorer: constantScorer(0.3),
		LLMScorer:       constantScorer(0.92),
	})

	score, tier, err := ts.Score(context.Background(), s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if score != 0.92 {
		t.Errorf("score = %f, want 0.92 (LLM score)", score)
	}
	if tier != TierLLM {
		t.Errorf("tier = %s, want %s", tier, TierLLM)
	}

	used, _, _, _ := budget.Usage()
	if used != 1 {
		t.Errorf("LLM calls used = %d, want 1", used)
	}
}

func TestScoreFallsBackToHeuristicWhenBudgetExhausted(t *testing.T) {
	cache := NewScoreCache(0)
	budget := mustNewBudget(t, 1)
	s := newTestStrategy("budget-exhausted")

	ts, _ := NewTieredScorer(TieredScorerConfig{
		Cache:           cache,
		Budget:          budget,
		HeuristicScorer: constantScorer(0.4),
		LLMScorer:       constantScorer(0.95),
	})

	// First call uses LLM.
	_, tier1, err := ts.Score(context.Background(), s)
	if err != nil {
		t.Fatalf("first score error: %v", err)
	}
	if tier1 != TierLLM {
		t.Errorf("first tier = %s, want %s", tier1, TierLLM)
	}

	// Second strategy should fall back to heuristic.
	s2 := newTestStrategy("budget-exhausted-2")
	score2, tier2, err := ts.Score(context.Background(), s2)
	if err != nil {
		t.Fatalf("second score error: %v", err)
	}
	if score2 != 0.4 {
		t.Errorf("second score = %f, want 0.4 (heuristic)", score2)
	}
	if tier2 != TierHeuristic {
		t.Errorf("second tier = %s, want %s", tier2, TierHeuristic)
	}
}

func TestScoreUsesHeuristicWhenLLMScorerIsNil(t *testing.T) {
	cache := NewScoreCache(0)
	budget := mustNewBudget(t, 10)
	s := newTestStrategy("no-llm")

	ts, _ := NewTieredScorer(TieredScorerConfig{
		Cache:           cache,
		Budget:          budget,
		HeuristicScorer: constantScorer(0.6),
		LLMScorer:       nil,
	})

	score, tier, err := ts.Score(context.Background(), s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if score != 0.6 {
		t.Errorf("score = %f, want 0.6 (heuristic)", score)
	}
	if tier != TierHeuristic {
		t.Errorf("tier = %s, want %s", tier, TierHeuristic)
	}

	used, _, _, _ := budget.Usage()
	if used != 0 {
		t.Errorf("LLM calls used = %d, want 0 (no LLM scorer)", used)
	}
}

func TestScoreFallsBackOnLLMPanic(t *testing.T) {
	cache := NewScoreCache(0)
	budget := mustNewBudget(t, 5)
	s := newTestStrategy("llm-panic")
	panicErr := errors.New("LLM service unavailable")

	ts, _ := NewTieredScorer(TieredScorerConfig{
		Cache:           cache,
		Budget:          budget,
		HeuristicScorer: constantScorer(0.35),
		LLMScorer:       panickingScorer(panicErr),
	})

	score, tier, err := ts.Score(context.Background(), s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if score != 0.35 {
		t.Errorf("score = %f, want 0.35 (heuristic fallback)", score)
	}
	if tier != TierHeuristic {
		t.Errorf("tier = %s, want %s (fallback)", tier, TierHeuristic)
	}

	// Fallback should be recorded.
	_, _, _, fallbacks := budget.Usage()
	if fallbacks != 1 {
		t.Errorf("fallbacks = %d, want 1", fallbacks)
	}

	// LLM call should still be counted (it was attempted).
	used, _, _, _ := budget.Usage()
	if used != 1 {
		t.Errorf("LLM calls used = %d, want 1 (attempted)", used)
	}
}

func TestStats(t *testing.T) {
	cache := NewScoreCache(0)
	budget := mustNewBudget(t, 3) // Only 3 LLM calls allowed.

	ts, _ := NewTieredScorer(TieredScorerConfig{
		Cache:           cache,
		Budget:          budget,
		HeuristicScorer: constantScorer(0.3),
		LLMScorer:       constantScorer(0.9),
	})

	ctx := context.Background()

	// Score 3 strategies via LLM (exhausts budget).
	for i := 0; i < 3; i++ {
		s := newTestStrategy(string(rune('a' + i)))
		_, _, _ = ts.Score(ctx, s)
	}

	// Score 7 more that exhaust budget → heuristic.
	for i := 0; i < 7; i++ {
		s := newTestStrategy(string(rune('d' + i)))
		_, _, _ = ts.Score(ctx, s)
	}

	stats := ts.Stats()
	if stats["llm_calls"] != int64(3) {
		t.Errorf("llm_calls = %d, want 3", stats["llm_calls"])
	}
	if stats["heuristic_calls"] != int64(7) {
		t.Errorf("heuristic_calls = %d, want 7", stats["heuristic_calls"])
	}
	if stats["total_scored"] != int64(10) {
		t.Errorf("total_scored = %d, want 10", stats["total_scored"])
	}
}

func TestResetForGeneration(t *testing.T) {
	cache := NewScoreCache(0)
	budget := mustNewBudget(t, 5)

	ts, _ := NewTieredScorer(TieredScorerConfig{
		Cache:           cache,
		Budget:          budget,
		HeuristicScorer: constantScorer(0.3),
		LLMScorer:       constantScorer(0.9),
	})

	ctx := context.Background()
	_, _, _ = ts.Score(ctx, newTestStrategy("gen1-a"))
	_, _, _ = ts.Score(ctx, newTestStrategy("gen1-b"))

	// Pre-reset: should have stats.
	preStats := ts.Stats()
	if preStats["total_scored"] == 0 {
		t.Error("expected non-zero stats before reset")
	}

	ts.ResetForGeneration()

	// Post-reset: stats should be zeroed, budget usable again.
	postStats := ts.Stats()
	if postStats["total_scored"] != 0 {
		t.Errorf("after reset total_scored = %d, want 0", postStats["total_scored"])
	}
	if postStats["llm_calls"] != 0 {
		t.Errorf("after reset llm_calls = %d, want 0", postStats["llm_calls"])
	}

	// Budget should be fresh.
	if !budget.CanCallLLM() {
		t.Error("budget should allow LLM calls after reset")
	}
}

func TestFullPipelineIntegration(t *testing.T) {
	cache := NewScoreCache(100)
	budget := mustNewBudget(t, 3)

	llmCallCount := 0
	llmScorer := func(s *mutation.Strategy) float64 {
		llmCallCount++
		return 0.95 - float64(llmCallCount)*0.01 // decreasing scores for variety
	}

	ts, _ := NewTieredScorer(TieredScorerConfig{
		Cache:           cache,
		Budget:          budget,
		HeuristicScorer: constantScorer(0.5),
		LLMScorer:       llmScorer,
	})

	ctx := context.Background()
	strategies := []*mutation.Strategy{
		newTestStrategy("alpha"),
		newTestStrategy("beta"),
		newTestStrategy("gamma"),   // 3rd LLM call — uses last budget slot
		newTestStrategy("delta"),   // budget exhausted → heuristic
		newTestStrategy("epsilon"), // budget exhausted → heuristic
		newTestStrategy("alpha"),   // duplicate of alpha → cache hit
	}

	tiers := make([]Tier, 0, len(strategies))
	scores := make([]float64, 0, len(strategies))

	for _, s := range strategies {
		score, tier, err := ts.Score(ctx, s)
		if err != nil {
			t.Fatalf("error scoring %s: %v", s.Name, err)
		}
		scores = append(scores, score)
		tiers = append(tiers, tier)
	}

	// Verify tier assignments.
	expectedTiers := []Tier{TierLLM, TierLLM, TierLLM, TierHeuristic, TierHeuristic, TierCache}
	for i, got := range tiers {
		if got != expectedTiers[i] {
			t.Errorf("strategy %d: tier = %s, want %s", i+1, got, expectedTiers[i])
		}
	}

	// Alpha's cached score should match its original LLM score.
	if scores[0] != scores[5] {
		t.Errorf("alpha cached score (%f) != original score (%f)", scores[5], scores[0])
	}

	// Heuristic scores for delta and epsilon.
	if scores[3] != 0.5 || scores[4] != 0.5 {
		t.Errorf("heuristic scores incorrect: delta=%f, epsilon=%f", scores[3], scores[4])
	}

	// Stats verification.
	stats := ts.Stats()
	if stats["cache_hits"] != 1 {
		t.Errorf("cache_hits = %d, want 1", stats["cache_hits"])
	}
	if stats["llm_calls"] != 3 {
		t.Errorf("llm_calls = %d, want 3", stats["llm_calls"])
	}
	if stats["heuristic_calls"] != 2 {
		t.Errorf("heuristic_calls = %d, want 2", stats["heuristic_calls"])
	}
	if stats["total_scored"] != 6 {
		t.Errorf("total_scored = %d, want 6", stats["total_scored"])
	}
}

func TestScoreNilStrategy(t *testing.T) {
	cache := NewScoreCache(0)
	budget := mustNewBudget(t, 5)

	ts, _ := NewTieredScorer(TieredScorerConfig{
		Cache:           cache,
		Budget:          budget,
		HeuristicScorer: constantScorer(0.5),
	})

	_, _, err := ts.Score(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil strategy")
	}
}

// mustNewBudget is a test helper that creates a Budget or fails t.
func mustNewBudget(t *testing.T, maxLLMCalls int) *Budget {
	t.Helper()
	b, err := NewBudget(maxLLMCalls)
	if err != nil {
		t.Fatalf("failed to create budget: %v", err)
	}
	return b
}
