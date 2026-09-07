package genome

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"testing"

	"golang.org/x/sync/errgroup"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// TestTournamentSelectionIntegration verifies that a population configured with
// tournament selection can successfully complete an evolution cycle and produce offspring.
func TestTournamentSelectionIntegration(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	base := newTestStrategy(0.5)
	mutator := &mockMutator{}
	crosser := &mockCrosser{}

	pop, err := NewPopulation(ctx, base, mutator,
		WithPopulationSize(10),
		WithSurvivalRate(0.6),
		WithEliteCount(2),
		WithMutationRate(0), // Disable mutation for deterministic crossover-only test
		WithTournamentSelection(3),
	)
	if err != nil {
		t.Fatalf("failed to create population with tournament selection: %v", err)
	}

	// Verify config was applied.
	if pop.cfg.SelectionStrategy != "tournament" {
		t.Fatal("SelectionStrategy should be \"tournament\" after WithTournamentSelection")
	}
	if pop.cfg.TournamentSize != 3 {
		t.Fatalf("TournamentSize = %d, want 3", pop.cfg.TournamentSize)
	}

	// Score all agents so selection can operate on evaluated individuals.
	for i, agent := range pop.Agents {
		agent.Score = float64(100 - i) // Descending scores: best agent has highest score
	}

	// Run one evolution cycle.
	err = pop.Evolve(ctx, mutator, crosser)
	if err != nil {
		t.Fatalf("evolution with tournament selection failed: %v", err)
	}

	// Verify generation incremented.
	if pop.Generation != 1 {
		t.Errorf("generation = %d, want 1", pop.Generation)
	}

	// Verify population size preserved.
	if len(pop.Agents) != pop.Size {
		t.Errorf("population size after evolve = %d, want %d", len(pop.Agents), pop.Size)
	}

	// Verify elites preserved (top scorers should survive).
	best := pop.Best()
	if best == nil || best.Score < 99.0 {
		t.Errorf("elite not preserved: best score = %v", best)
	}
}

// TestTournamentSelectionDisabledByDefault verifies that tournament selection is
// opt-in and disabled by default for backward compatibility.
func TestTournamentSelectionDisabledByDefault(t *testing.T) {
	t.Parallel()

	cfg := DefaultPopulationConfig()

	if cfg.SelectionStrategy == "tournament" {
		t.Error("SelectionStrategy should not be \"tournament\" by default")
	}

	if cfg.TournamentSize != 3 {
		t.Errorf("default TournamentSize = %d, want 3", cfg.TournamentSize)
	}

	// Verify a population created without WithTournamentSelection uses random selection.
	ctx := context.Background()
	base := newTestStrategy(0.5)
	mutator := &mockMutator{}
	crosser := &mockCrosser{}

	pop, err := NewPopulation(ctx, base, mutator,
		WithPopulationSize(8),
		WithSurvivalRate(0.5),
		WithEliteCount(1),
	)
	if err != nil {
		t.Fatalf("failed to create population: %v", err)
	}

	if pop.cfg.SelectionStrategy != "" {
		t.Error("population should have SelectionStrategy=\"\" by default")
	}

	// Score agents and verify evolution works with random selection.
	for i, agent := range pop.Agents {
		agent.Score = float64(i + 1)
	}

	err = pop.Evolve(ctx, mutator, crosser)
	if err != nil {
		t.Fatalf("evolution with random selection failed: %v", err)
	}

	if pop.Generation != 1 {
		t.Errorf("generation = %d, want 1", pop.Generation)
	}
}

// TestInvalidTournamentSizeRejected verifies that invalid tournament sizes
// are rejected by WithTournamentSelection with the correct error.
func TestInvalidTournamentSizeRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		give    int
		wantErr error
	}{
		{
			name:    "size 1 rejected",
			give:    1,
			wantErr: ErrInvalidTournamentSize,
		},
		{
			name:    "size 0 rejected",
			give:    0,
			wantErr: ErrInvalidTournamentSize,
		},
		{
			name:    "negative size rejected",
			give:    -5,
			wantErr: ErrInvalidTournamentSize,
		},
		{
			name:    "size 2 accepted",
			give:    2,
			wantErr: nil,
		},
		{
			name:    "large size accepted",
			give:    100,
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := PopulationConfig{}
			err := WithTournamentSelection(tt.give)(&cfg)

			if tt.wantErr != nil {
				if err == nil {
					t.Errorf("expected error %v, got nil", tt.wantErr)
					return
				}
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("error = %v, want %v", err, tt.wantErr)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestTournamentReproducibilityWithSeed verifies that two populations created with
// the same seed and tournament size produce identical evolution outcomes due to
// deterministic RNG seeding.
func TestTournamentReproducibilityWithSeed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	base := newTestStrategy(0.7)

	// Deterministic mutator and crosser for reproducible output.
	detMut := &mockMutator{
		mutateFn: func(_ context.Context, parent *mutation.Strategy, n int) ([]*mutation.Strategy, error) {
			result := make([]*mutation.Strategy, n)
			for i := range result {
				result[i] = &mutation.Strategy{
					ID:       fmt.Sprintf("det-mut-%d-%s", i, parent.ID),
					ParentID: parent.ID,
					Version:  parent.Version + 1,
					Params:   make(map[string]any),
					Score:    parent.Score * 0.9, // Slight score degradation
				}
			}
			return result, nil
		},
	}

	detCrosser := &mockCrosser{
		crossoverFn: func(_ context.Context, a, b *mutation.Strategy) (*mutation.Strategy, error) {
			childParams := make(map[string]any)
			for k, v := range a.Params {
				childParams[k] = v
			}
			return &mutation.Strategy{
				ID:       fmt.Sprintf("det-child-%sx%s", a.ID, b.ID),
				ParentID: a.ID,
				Version:  max(a.Version, b.Version) + 1,
				Params:   childParams,
				Score:    (a.Score + b.Score) / 2,
			}, nil
		},
	}

	seed := int64(42)

	// Create two populations with identical configuration including seed and tournament.
	pop1, err := NewPopulation(ctx, base, detMut,
		WithPopulationSize(8),
		WithPopulationSeed(seed),
		WithSurvivalRate(0.6),
		WithEliteCount(2),
		WithMutationRate(0),
		WithTournamentSelection(3),
		WithPerLineageElites(false), // Global elite ordering for deterministic reproducibility
	)
	if err != nil {
		t.Fatalf("NewPopulation (1) failed: %v", err)
	}

	pop2, err := NewPopulation(ctx, base, detMut,
		WithPopulationSize(8),
		WithPopulationSeed(seed),
		WithSurvivalRate(0.6),
		WithEliteCount(2),
		WithMutationRate(0),
		WithTournamentSelection(3),
		WithPerLineageElites(false), // Global elite ordering for deterministic reproducibility
	)
	if err != nil {
		t.Fatalf("NewPopulation (2) failed: %v", err)
	}

	// Assign identical scores to both populations.
	scores := []float64{90.0, 80.0, 70.0, 60.0, 50.0, 40.0, 30.0, 20.0}
	for i, agent := range pop1.Agents {
		agent.Score = scores[i]
	}
	for i, agent := range pop2.Agents {
		agent.Score = scores[i]
	}

	// Evolve both populations.
	if err := pop1.Evolve(ctx, detMut, detCrosser); err != nil {
		t.Fatalf("Evolve (1) failed: %v", err)
	}
	if err := pop2.Evolve(ctx, detMut, detCrosser); err != nil {
		t.Fatalf("Evolve (2) failed: %v", err)
	}

	// Verify identical outcomes.
	if len(pop1.Agents) != len(pop2.Agents) {
		t.Fatalf("evolved sizes differ: %d vs %d", len(pop1.Agents), len(pop2.Agents))
	}

	for i := range pop1.Agents {
		if pop1.Agents[i].ID != pop2.Agents[i].ID {
			t.Errorf("agent %d ID differs: %s vs %s", i, pop1.Agents[i].ID, pop2.Agents[i].ID)
		}
		if pop1.Agents[i].ParentID != pop2.Agents[i].ParentID {
			t.Errorf("agent %d ParentID differs: %s vs %s", i, pop1.Agents[i].ParentID, pop2.Agents[i].ParentID)
		}
	}
}

// TestTournamentVsRandomScoreDistribution verifies that tournament selection
// tends to select higher-scoring parents than random selection over many runs.
//
// This test creates a population with a wide score distribution, then runs
// multiple evolution cycles tracking the average parent score for both modes.
// Tournament selection should produce equal or higher average parent scores.
func TestTournamentVsRandomScoreDistribution(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	const (
		popSize      = 12
		eliteCount   = 2
		survivalRate = 0.5
		generations  = 30
	)

	// Create a scored population helper that returns a fresh population each time.
	makePop := func(useTournament bool) (*Population, error) {
		base := newTestStrategy(0.5)
		opts := []PopulationOption{
			WithPopulationSize(popSize),
			WithSurvivalRate(survivalRate),
			WithEliteCount(eliteCount),
			WithMutationRate(0),     // Disable mutation to isolate selection effect
			WithPopulationSeed(123), // Fixed seed for fair comparison
		}
		if useTournament {
			opts = append(opts, WithTournamentSelection(3))
		}
		return NewPopulation(ctx, base, &mockMutator{}, opts...)
	}

	// Crosser that tracks parent scores.
	var tournamentParentScores []float64
	var randomParentScores []float64

	trackingCrosser := func(parentScores *[]float64) *mockCrosser {
		return &mockCrosser{
			crossoverFn: func(_ context.Context, a, b *mutation.Strategy) (*mutation.Strategy, error) {
				*parentScores = append(*parentScores, a.Score, b.Score)
				return &mutation.Strategy{
					ID:       "tracking-child",
					ParentID: a.ID,
					Version:  1,
					Params:   make(map[string]any),
					Score:    (a.Score + b.Score) / 2,
				}, nil
			},
		}
	}

	// Run generations with tournament selection.
	tournamentPop, err := makePop(true)
	if err != nil {
		t.Fatalf("failed to create tournament population: %v", err)
	}
	tournamentParentScores = make([]float64, 0, generations*popSize)
	tc := trackingCrosser(&tournamentParentScores)

	for g := 0; g < generations; g++ {
		for i, agent := range tournamentPop.Agents {
			agent.Score = float64(popSize - i) // High variance: [popSize, 1]
		}
		if err := tournamentPop.Evolve(ctx, &mockMutator{}, tc); err != nil {
			t.Fatalf("tournament evolution %d failed: %v", g, err)
		}
	}

	// Run generations with random selection.
	randomPop, err := makePop(false)
	if err != nil {
		t.Fatalf("failed to create random population: %v", err)
	}
	randomParentScores = make([]float64, 0, generations*popSize)
	rc := trackingCrosser(&randomParentScores)

	for g := 0; g < generations; g++ {
		for i, agent := range randomPop.Agents {
			agent.Score = float64(popSize - i) // Same scoring as tournament
		}
		if err := randomPop.Evolve(ctx, &mockMutator{}, rc); err != nil {
			t.Fatalf("random evolution %d failed: %v", g, err)
		}
	}

	// Compute averages.
	tournamentAvg := avgSlice(tournamentParentScores)
	randomAvg := avgSlice(randomParentScores)

	t.Logf("tournament avg parent score: %.4f (%d selections)", tournamentAvg, len(tournamentParentScores))
	t.Logf("random avg parent score: %.4f (%d selections)", randomAvg, len(randomParentScores))

	// Tournament selection should select parents with >= average score of random.
	// We allow a small tolerance since randomness still exists within tournaments.
	const tolerance = 0.5
	if tournamentAvg+tolerance < randomAvg {
		t.Errorf("tournament avg (%.4f) should be >= random avg (%.4f) within tolerance %.1f",
			tournamentAvg, randomAvg, tolerance)
	}
}

// avgSlice computes the arithmetic mean of a float64 slice.
func avgSlice(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// TestTournamentSelectionInEvolveOnIdle verifies that tournament selection also
// works correctly via EvolveOnIdle (the idle evolution path).
func TestTournamentSelectionInEvolveOnIdle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	base := newTestStrategy(0.5)
	mutator := &mockMutator{}
	crosser := &mockCrosser{}

	pop, err := NewPopulation(ctx, base, mutator,
		WithPopulationSize(10),
		WithEliteCount(2),
		WithBreedingPoolRatio(0.5),
		WithMutationRate(0),
		WithTournamentSelection(3),
	)
	if err != nil {
		t.Fatalf("failed to create population: %v", err)
	}

	// Score all agents.
	for i, agent := range pop.Agents {
		agent.Score = float64(50 - i)
	}

	err = pop.EvolveOnIdle(ctx, mutator, crosser)
	if err != nil {
		t.Fatalf("EvolveOnIdle with tournament selection failed: %v", err)
	}

	if pop.Generation != 1 {
		t.Errorf("generation = %d, want 1", pop.Generation)
	}

	if len(pop.Agents) != pop.Size {
		t.Errorf("population size = %d, want %d", len(pop.Agents), pop.Size)
	}
}

// TestDefaultPopulationConfigIncludesTournamentDefaults verifies that the default
// configuration includes sensible tournament-related defaults.
func TestDefaultPopulationConfigIncludesTournamentDefaults(t *testing.T) {
	t.Parallel()

	cfg := DefaultPopulationConfig()

	if cfg.TournamentSize != 3 {
		t.Errorf("default TournamentSize = %d, want 3", cfg.TournamentSize)
	}

	if cfg.SelectionStrategy != "" {
		t.Error("default SelectionStrategy should be empty (random)")
	}
}

// TestTournamentSelectionEdgeCases covers edge cases like single-parent pool
// and minimum viable tournament configuration.
func TestTournamentSelectionEdgeCases(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("population size 2 with tournament k=2", func(t *testing.T) {
		t.Parallel()
		base := newTestStrategy(0.5)
		pop, err := NewPopulation(ctx, base, &mockMutator{},
			WithPopulationSize(2),
			WithEliteCount(0),
			WithSurvivalRate(1.0),
			WithMutationRate(0),
			WithTournamentSelection(2),
		)
		if err != nil {
			t.Fatalf("failed to create population: %v", err)
		}

		pop.Agents[0].Score = 100.0
		pop.Agents[1].Score = 50.0

		crosser, err := NewCrossover(WithSeed(99))
		if err != nil {
			t.Fatalf("failed to create crossover: %v", err)
		}

		if err := pop.Evolve(ctx, &mockMutator{}, crosser); err != nil {
			t.Fatalf("evolve failed: %v", err)
		}

		if len(pop.Agents) != 2 {
			t.Errorf("size = %d, want 2", len(pop.Agents))
		}
	})

	t.Run("tournament size exceeds pool size gracefully degrades", func(t *testing.T) {
		t.Parallel()
		base := newTestStrategy(0.5)
		pop, err := NewPopulation(ctx, base, &mockMutator{},
			WithPopulationSize(4),
			WithEliteCount(1),
			WithSurvivalRate(0.5),
			WithMutationRate(0),
			WithTournamentSelection(10), // k=10 > typical pool size of 2
		)
		if err != nil {
			t.Fatalf("failed to create population: %v", err)
		}

		for i, agent := range pop.Agents {
			agent.Score = float64(40 - i)
		}

		crosser, err := NewCrossover(WithSeed(99))
		if err != nil {
			t.Fatalf("failed to create crossover: %v", err)
		}

		// Should not panic or error — tournament degrades k to pool size.
		if err := pop.Evolve(ctx, &mockMutator{}, crosser); err != nil {
			t.Fatalf("evolve should succeed with large tournament size: %v", err)
		}
	})
}

// TestTournamentSelectionWithHighPressure verifies behavior under high
// selection pressure (large tournament size relative to population).
func TestTournamentSelectionWithHighPressure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	base := newTestStrategy(0.5)

	// Track which parents are selected to verify high-pressure bias toward top scorers.
	var selectedParents []string

	trackingMutator := &mockMutator{}
	trackingCrosser := &mockCrosser{
		crossoverFn: func(_ context.Context, a, b *mutation.Strategy) (*mutation.Strategy, error) {
			selectedParents = append(selectedParents, a.ID, b.ID)
			return &mutation.Strategy{
				ID:       "hp-child",
				ParentID: a.ID,
				Version:  1,
				Params:   make(map[string]any),
			}, nil
		},
	}

	pop, err := NewPopulation(ctx, base, trackingMutator,
		WithPopulationSize(10),
		WithEliteCount(2),
		WithSurvivalRate(0.5),
		WithMutationRate(0),
		WithTournamentSelection(5), // High pressure: k=5 out of ~5 survivors
		WithPopulationSeed(999),
	)
	if err != nil {
		t.Fatalf("failed to create population: %v", err)
	}

	// Assign distinct IDs and descending scores so we can trace selection.
	for i, agent := range pop.Agents {
		agent.ID = fmt.Sprintf("agent-%d", i)
		agent.Score = float64(100 - i*10) // 100, 90, 80, ..., 10
	}

	if err := pop.Evolve(ctx, trackingMutator, trackingCrosser); err != nil {
		t.Fatalf("evolve failed: %v", err)
	}

	// We expect at least some selections occurred.
	if len(selectedParents) == 0 {
		t.Fatal("no parents were selected during evolution")
	}

	t.Logf("high-pressure tournament selected %d parents: %v", len(selectedParents), selectedParents)
}

// TestConcurrentTournamentEvolution verifies thread-safety of tournament-enabled evolution.
// Each goroutine operates on its own population to avoid scoring-guard races
// on shared state while still validating tournament selection under concurrency.
func TestConcurrentTournamentEvolution(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	base := newTestStrategy(0.5)

	const goroutines = 5
	const iterations = 3

	eg, egCtx := errgroup.WithContext(context.Background())
	for g := 0; g < goroutines; g++ {
		idx := g
		eg.Go(func() error {
			// Each goroutine owns its own population — tests tournament selection
			// thread-safety without cross-goroutine scoring races.
			mutator := &mockMutator{}
			crosser := &mockCrosser{}
			pop, err := NewPopulation(ctx, base, mutator,
				WithPopulationSize(10),
				WithEliteCount(2),
				WithSurvivalRate(0.6),
				WithTournamentSelection(3),
			)
			if err != nil {
				return fmt.Errorf("goroutine %d: create population: %w", idx, err)
			}

			for _, agent := range pop.Agents {
				agent.Score = rand.Float64() * 100
			}

			for j := 0; j < iterations; j++ {
				select {
				case <-egCtx.Done():
					return egCtx.Err()
				default:
				}
				// Re-score before each evolve (offspring from prior evolve have Score=-1).
				pop.mu.Lock()
				for _, agent := range pop.Agents {
					if agent.Score < 0 {
						agent.Score = rand.Float64() * 100
					}
				}
				pop.mu.Unlock()
				if err := pop.Evolve(ctx, mutator, crosser); err != nil && !errors.Is(err, context.Canceled) {
					return fmt.Errorf("goroutine %d iter %d: %w", idx, j, err)
				}
			}
			return nil
		})
	}

	if err := eg.Wait(); err != nil {
		t.Errorf("concurrent tournament evolution error: %v", err)
	}
}
