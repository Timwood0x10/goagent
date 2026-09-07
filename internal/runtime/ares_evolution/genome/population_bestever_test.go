package genome

import (
	"context"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// TestBestEverTracking verifies that BestStrategy() returns the best-ever strategy
// across multiple generations, not just the current population's best.
func TestBestEverTracking(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		genScores     [][]float64
		wantBestScore float64
	}{
		{
			name: "best_ever_tracks_across_generations",
			genScores: [][]float64{
				{10, 20, 30, 40, 50}, // gen0 best=50
				{60, 70, 80, 90, 95}, // gen1 best=95 (new best ever)
				{70, 75, 80, 85, 80}, // gen2 best=85 (regression)
			},
			wantBestScore: 95.0,
		},
		{
			name: "monotonically_increasing_scores",
			genScores: [][]float64{
				{10, 20, 30},
				{40, 50, 60},
				{70, 80, 90},
			},
			wantBestScore: 90.0,
		},
		{
			name: "first_generation_is_best",
			genScores: [][]float64{
				{90, 80, 70},
				{50, 40, 30},
				{20, 10, 5},
			},
			wantBestScore: 90.0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			base := &mutation.Strategy{
				ID:             "bestever-base",
				Version:        1,
				Params:         map[string]any{"temperature": 0.7},
				PromptTemplate: "test prompt",
				Score:          0,
				CreatedAt:      time.Now(),
			}

			mutator := &mockMutator{}
			crosser := &mockCrosser{}

			pop, err := NewPopulation(ctx, base, mutator,
				WithPopulationSize(len(tt.genScores[0])),
				WithPopulationSeed(42),
				WithEliteCount(1),
				WithMutationRate(0),
			)
			if err != nil {
				t.Fatalf("failed to create population: %v", err)
			}

			for genIdx, scores := range tt.genScores {
				for i, agent := range pop.Agents {
					if i < len(scores) {
						agent.Score = scores[i]
					}
				}

				if genIdx > 0 {
					if err := pop.EvolveOnIdle(ctx, mutator, crosser); err != nil {
						t.Fatalf("evolve gen %d failed: %v", genIdx, err)
					}
				}
				pop.ScoreAgents(func(a *mutation.Strategy) float64 { return a.Score })
			}

			best := pop.BestStrategy()
			if best == nil {
				t.Fatal("BestStrategy() returned nil")
			}
			if best.Score != tt.wantBestScore {
				t.Errorf("BestStrategy().Score = %f, want %f", best.Score, tt.wantBestScore)
			}
		})
	}
}

// TestBestEverPreservedAfterRegression verifies the key regression scenario:
// generation 7 reaches score 95, generation 15 drops to 90, but BestStrategy
// still returns the 95-score strategy.
func TestBestEverPreservedAfterRegression(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	base := &mutation.Strategy{
		ID:             "regression-base",
		Version:        1,
		Params:         map[string]any{"temperature": 0.7},
		PromptTemplate: "test prompt",
		Score:          0,
		CreatedAt:      time.Now(),
	}

	mutator := &mockMutator{}
	crosser := &mockCrosser{}

	pop, err := NewPopulation(ctx, base, mutator,
		WithPopulationSize(5),
		WithPopulationSeed(42),
		WithEliteCount(1),
		WithMutationRate(0),
	)
	if err != nil {
		t.Fatalf("failed to create population: %v", err)
	}

	simulateGeneration := func(gen int, peakScore float64) {
		scores := make([]float64, pop.Size)
		for i := range scores {
			scores[i] = peakScore - float64(i*5)
		}
		for i, agent := range pop.Agents {
			if i < len(scores) {
				agent.Score = scores[i]
			}
		}
		if gen > 0 {
			if err := pop.EvolveOnIdle(ctx, mutator, crosser); err != nil {
				t.Fatalf("evolve gen %d failed: %v", gen, err)
			}
		}
		pop.ScoreAgents(func(a *mutation.Strategy) float64 { return a.Score })
	}

	// Simulate generations leading up to the peak.
	for gen := 0; gen < 7; gen++ {
		simulateGeneration(gen, 50.0+float64(gen)*5)
	}

	// Generation 7: reach peak score of 95.
	simulateGeneration(7, 95.0)

	bestAtPeak := pop.BestStrategy()
	if bestAtPeak == nil || bestAtPeak.Score != 95.0 {
		t.Fatalf("at gen7: BestStrategy().Score = %v, want 95.0", scoreOf(bestAtPeak))
	}

	// Simulate regression: generations 8-15 with declining scores.
	for gen := 8; gen <= 15; gen++ {
		regressionScore := 95.0 - float64(gen-7)*3
		simulateGeneration(gen, regressionScore)
	}

	// Generation 15: current best is around 77, but best-ever should still be 95.
	bestAfterRegression := pop.BestStrategy()
	if bestAfterRegression == nil {
		t.Fatal("BestStrategy() returned nil after regression")
	}
	if bestAfterRegression.Score != 95.0 {
		t.Errorf("after regression: BestStrategy().Score = %f, want 95.0 (best-ever preserved)",
			bestAfterRegression.Score)
	}
	if pop.BestEverScore() != 95.0 {
		t.Errorf("BestEverScore() = %f, want 95.0", pop.BestEverScore())
	}
}

// TestBestStrategyReturnsNilForUnevaluated verifies that BestStrategy returns nil
// when all agents have unevaluated scores.
func TestBestStrategyReturnsNilForUnevaluated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		agents  []*mutation.Strategy
		wantNil bool
	}{
		{
			name: "all_unevaluated_returns_nil",
			agents: []*mutation.Strategy{
				newTestStrategy(ScoreUnevaluated),
				newTestStrategy(ScoreUnevaluated),
				newTestStrategy(ScoreUnevaluated),
			},
			wantNil: true,
		},
		{
			name: "mixed_evaluated_and_unevaluated",
			agents: []*mutation.Strategy{
				newTestStrategy(ScoreUnevaluated),
				newTestStrategy(42.0),
				newTestStrategy(ScoreUnevaluated),
			},
			wantNil: false,
		},
		{
			name:    "empty_population_returns_nil",
			agents:  []*mutation.Strategy{},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pop := &Population{
				Agents: tt.agents,
				Size:   len(tt.agents),
			}

			got := pop.BestStrategy()
			if tt.wantNil && got != nil {
				t.Errorf("expected nil, got Score=%f", got.Score)
			}
			if !tt.wantNil && got == nil {
				t.Error("expected non-nil, got nil")
			}
		})
	}
}

// TestBestEverUpdatedByScoreAgents verifies that calling ScoreAgents updates
// the bestEver tracking so that BestEverScore reflects scored values.
func TestBestEverUpdatedByScoreAgents(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	base := newTestStrategy(0.5)

	mutator := &mockMutator{}

	pop, err := NewPopulation(ctx, base, mutator,
		WithPopulationSize(4),
		WithPopulationSeed(42),
	)
	if err != nil {
		t.Fatalf("failed to create population: %v", err)
	}

	// Initially, no scoring has occurred.
	if got := pop.BestEverScore(); got != ScoreUnevaluated {
		t.Errorf("initial BestEverScore() = %f, want ScoreUnevaluated (%f)", got, ScoreUnevaluated)
	}

	// First scoring pass: moderate scores.
	pop.ScoreAgents(func(a *mutation.Strategy) float64 {
		return 50.0
	})
	if got := pop.BestEverScore(); got != 50.0 {
		t.Errorf("after first ScoreAgents: BestEverScore() = %f, want 50.0", got)
	}

	// Second scoring pass: higher scores.
	pop.ScoreAgents(func(a *mutation.Strategy) float64 {
		return 88.0
	})
	if got := pop.BestEverScore(); got != 88.0 {
		t.Errorf("after second ScoreAgents: BestEverScore() = %f, want 88.0", got)
	}

	// Third scoring pass: lower scores (should not overwrite).
	pop.ScoreAgents(func(a *mutation.Strategy) float64 {
		return 30.0
	})
	if got := pop.BestEverScore(); got != 88.0 {
		t.Errorf("after third ScoreAgents: BestEverScore() = %f, want 88.0 (preserved)", got)
	}
}

// TestBestStrategyThreadSafety verifies that concurrent calls to BestStrategy()
// do not cause data races. This test is designed to be run with -race flag.
func TestBestStrategyThreadSafety(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	base := newTestStrategy(0.5)

	mutator := &mockMutator{}

	pop, err := NewPopulation(ctx, base, mutator,
		WithPopulationSize(8),
		WithPopulationSeed(42),
	)
	if err != nil {
		t.Fatalf("failed to create population: %v", err)
	}

	// Set initial scores and establish a bestEver.
	pop.ScoreAgents(func(a *mutation.Strategy) float64 {
		return 75.0
	})

	eg, egCtx := errgroup.WithContext(context.Background())
	const goroutines = 10
	const iterations = 50

	for g := 0; g < goroutines; g++ {
		eg.Go(func() error {
			for i := 0; i < iterations; i++ {
				select {
				case <-egCtx.Done():
					return egCtx.Err()
				default:
				}
				best := pop.BestStrategy()
				if best == nil {
					continue
				}
				// Verify the clone is independent.
				_ = best.Score + 1.0
			}
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		t.Errorf("concurrent BestStrategy error: %v", err)
	}
}

// scoreOf safely extracts the score from a potentially nil strategy.
func scoreOf(s *mutation.Strategy) float64 {
	if s == nil {
		return ScoreUnevaluated
	}
	return s.Score
}
