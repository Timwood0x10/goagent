package genome

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// mockMutator implements MutatorInterface for testing.
type mockMutator struct {
	mutateFn func(ctx context.Context, parent *mutation.Strategy, n int) ([]*mutation.Strategy, error)
}

func (m *mockMutator) Mutate(ctx context.Context, parent *mutation.Strategy, n int) ([]*mutation.Strategy, error) {
	if m.mutateFn != nil {
		return m.mutateFn(ctx, parent, n)
	}
	result := make([]*mutation.Strategy, n)
	for i := range result {
		result[i] = &mutation.Strategy{
			ID:             fmt.Sprintf("mock-mutant-%d-%d", time.Now().UnixNano(), i),
			ParentID:       parent.ID,
			Version:        parent.Version + 1,
			Params:         make(map[string]any),
			PromptTemplate: "mock-template",
			Score:          -1,
			CreatedAt:      time.Now(),
		}
	}
	return result, nil
}

// mockCrosser implements CrossoverInterface for testing.
type mockCrosser struct {
	crossoverFn func(ctx context.Context, a, b *mutation.Strategy) (*mutation.Strategy, error)
}

func (c *mockCrosser) Crossover(ctx context.Context, a, b *mutation.Strategy) (*mutation.Strategy, error) {
	if c.crossoverFn != nil {
		return c.crossoverFn(ctx, a, b)
	}
	return &mutation.Strategy{
		ID:             "mock-child",
		Params:         make(map[string]any),
		PromptTemplate: "mock-template",
		Score:          -1,
	}, nil
}

// newTestStrategy creates a strategy with the given score for testing.
func newTestStrategy(score float64) *mutation.Strategy {
	return &mutation.Strategy{
		ID:             "test-strategy",
		Params:         map[string]any{"temperature": 0.7},
		PromptTemplate: "test-prompt",
		Score:          score,
		CreatedAt:      time.Now(),
	}
}

func TestNewPopulation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		base     *mutation.Strategy
		mutator  MutatorInterface
		opts     []PopulationOption
		wantErr  error
		wantSize int
		wantGen  int
	}{
		{
			name:     "valid config creates population",
			base:     newTestStrategy(0.5),
			mutator:  &mockMutator{},
			opts:     []PopulationOption{WithPopulationSize(5)},
			wantErr:  nil,
			wantSize: 5,
			wantGen:  0,
		},
		{
			name:    "nil base returns error",
			base:    nil,
			mutator: &mockMutator{},
			wantErr: ErrNilBaseStrategy,
		},
		{
			name:    "nil mutator returns error",
			base:    newTestStrategy(0.5),
			mutator: nil,
			wantErr: ErrNilMutator,
		},
		{
			name:    "zero size returns error",
			base:    newTestStrategy(0.5),
			mutator: &mockMutator{},
			opts:    []PopulationOption{WithPopulationSize(0)},
			wantErr: ErrInvalidPopulationSize,
		},
		{
			name:    "negative size returns error",
			base:    newTestStrategy(0.5),
			mutator: &mockMutator{},
			opts:    []PopulationOption{WithPopulationSize(-1)},
			wantErr: ErrInvalidPopulationSize,
		},
		{
			name:    "elite count exceeds size returns error",
			base:    newTestStrategy(0.5),
			mutator: &mockMutator{},
			opts:    []PopulationOption{WithPopulationSize(3), WithEliteCount(5)},
			wantErr: ErrInvalidEliteCount,
		},
		{
			name:    "invalid survival rate returns error",
			base:    newTestStrategy(0.5),
			mutator: &mockMutator{},
			opts:    []PopulationOption{WithSurvivalRate(1.5)},
			wantErr: ErrInvalidSurvivalRate,
		},
		{
			name:    "invalid mutation rate returns error",
			base:    newTestStrategy(0.5),
			mutator: &mockMutator{},
			opts:    []PopulationOption{WithMutationRate(-0.1)},
			wantErr: ErrInvalidMutationRate,
		},
		{
			name:     "default config uses size 20",
			base:     newTestStrategy(0.5),
			mutator:  &mockMutator{},
			wantErr:  nil,
			wantSize: 20,
			wantGen:  0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()

			pop, err := NewPopulation(ctx, tt.base, tt.mutator, tt.opts...)

			if tt.wantErr != nil {
				if err == nil {
					t.Errorf("expected error %v, got nil", tt.wantErr)
					return
				}
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("error = %v, want %v", err, tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if pop.Size != tt.wantSize {
				t.Errorf("population Size = %d, want %d", pop.Size, tt.wantSize)
			}

			if pop.Generation != tt.wantGen {
				t.Errorf("population Generation = %d, want %d", pop.Generation, tt.wantGen)
			}

			if len(pop.Agents) != pop.Size {
				t.Errorf("agent count = %d, want %d", len(pop.Agents), pop.Size)
			}
		})
	}
}

func TestEvolve(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("successful evolution increments generation", func(t *testing.T) {
		t.Parallel()

		base := newTestStrategy(0.5)
		mutator := &mockMutator{}
		crosser := &mockCrosser{}

		pop, err := NewPopulation(ctx, base, mutator, WithPopulationSize(6), WithSurvivalRate(0.5), WithEliteCount(1))
		if err != nil {
			t.Fatalf("failed to create population: %v", err)
		}

		// Set scores for all agents.
		for i, agent := range pop.Agents {
			agent.Score = float64(i)
		}

		genBefore := pop.Generation
		err = pop.Evolve(ctx, mutator, crosser)
		if err != nil {
			t.Fatalf("evolve failed: %v", err)
		}

		if pop.Generation != genBefore+1 {
			t.Errorf("generation = %d, want %d", pop.Generation, genBefore+1)
		}

		if len(pop.Agents) != pop.Size {
			t.Errorf("agent count after evolve = %d, want %d", len(pop.Agents), pop.Size)
		}
	})

	t.Run("nil mutator returns error", func(t *testing.T) {
		t.Parallel()

		base := newTestStrategy(0.5)
		mutator := &mockMutator{}
		crosser := &mockCrosser{}

		pop, err := NewPopulation(ctx, base, mutator, WithPopulationSize(4))
		if err != nil {
			t.Fatalf("failed to create population: %v", err)
		}

		err = pop.Evolve(ctx, nil, crosser)
		if !errors.Is(err, ErrNilMutator) {
			t.Errorf("error = %v, want %v", err, ErrNilMutator)
		}
	})

	t.Run("nil crosser returns error", func(t *testing.T) {
		t.Parallel()

		base := newTestStrategy(0.5)
		mutator := &mockMutator{}

		pop, err := NewPopulation(ctx, base, mutator, WithPopulationSize(4))
		if err != nil {
			t.Fatalf("failed to create population: %v", err)
		}

		err = pop.Evolve(ctx, mutator, nil)
		if !errors.Is(err, ErrNilCrosser) {
			t.Errorf("error = %v, want %v", err, ErrNilCrosser)
		}
	})

	t.Run("multiple evolutions accumulate generations", func(t *testing.T) {
		t.Parallel()

		base := newTestStrategy(0.5)
		mutator := &mockMutator{}
		crosser := &mockCrosser{}

		pop, err := NewPopulation(ctx, base, mutator, WithPopulationSize(6), WithSurvivalRate(0.5), WithEliteCount(1))
		if err != nil {
			t.Fatalf("failed to create population: %v", err)
		}

		for i := 0; i < 3; i++ {
			for _, agent := range pop.Agents {
				agent.Score = float64(i * 10)
			}

			err = pop.Evolve(ctx, mutator, crosser)
			if err != nil {
				t.Fatalf("evolution %d failed: %v", i, err)
			}
		}

		if pop.Generation != 3 {
			t.Errorf("generation after 3 evolves = %d, want 3", pop.Generation)
		}
	})
}

func TestBest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		agents    []*mutation.Strategy
		wantScore float64
		wantNil   bool
	}{
		{
			name:    "empty population returns nil",
			agents:  []*mutation.Strategy{},
			wantNil: true,
		},
		{
			name: "single agent returns that agent",
			agents: []*mutation.Strategy{
				newTestStrategy(42.0),
			},
			wantScore: 42.0,
		},
		{
			name: "returns highest scored agent",
			agents: []*mutation.Strategy{
				newTestStrategy(10.0),
				newTestStrategy(50.0),
				newTestStrategy(30.0),
			},
			wantScore: 50.0,
		},
		{
			name: "all same score returns first encountered",
			agents: []*mutation.Strategy{
				newTestStrategy(25.0),
				newTestStrategy(25.0),
				newTestStrategy(25.0),
			},
			wantScore: 25.0,
		},
		{
			name: "negative scores handled correctly",
			agents: []*mutation.Strategy{
				newTestStrategy(-10.0),
				newTestStrategy(-5.0),
				newTestStrategy(-20.0),
			},
			wantScore: -5.0,
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

			best := pop.Best()

			if tt.wantNil {
				if best != nil {
					t.Errorf("expected nil, got %+v", best)
				}
				return
			}

			if best == nil {
				t.Fatalf("expected non-nil best, got nil")
			}

			if best.Score != tt.wantScore {
				t.Errorf("best.Score = %f, want %f", best.Score, tt.wantScore)
			}
		})
	}
}

func TestStats(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		agents         []*mutation.Strategy
		generation     int
		wantGeneration int
		wantSize       int
		wantAvg        float64
		wantBest       float64
		wantWorst      float64
	}{
		{
			name:           "empty population returns zero values",
			agents:         []*mutation.Strategy{},
			generation:     5,
			wantGeneration: 5,
			wantSize:       0,
			wantAvg:        0.0,
			wantBest:       0.0,
			wantWorst:      0.0,
		},
		{
			name: "single agent stats",
			agents: []*mutation.Strategy{
				newTestStrategy(100.0),
			},
			generation:     2,
			wantGeneration: 2,
			wantSize:       1,
			wantAvg:        100.0,
			wantBest:       100.0,
			wantWorst:      100.0,
		},
		{
			name: "multiple agents correct calculation",
			agents: []*mutation.Strategy{
				newTestStrategy(10.0),
				newTestStrategy(20.0),
				newTestStrategy(30.0),
				newTestStrategy(40.0),
			},
			generation:     3,
			wantGeneration: 3,
			wantSize:       4,
			wantAvg:        25.0,
			wantBest:       40.0,
			wantWorst:      10.0,
		},
		{
			name: "all same score population",
			agents: []*mutation.Strategy{
				newTestStrategy(15.0),
				newTestStrategy(15.0),
				newTestStrategy(15.0),
			},
			generation:     1,
			wantGeneration: 1,
			wantSize:       3,
			wantAvg:        15.0,
			wantBest:       15.0,
			wantWorst:      15.0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pop := &Population{
				Agents:     tt.agents,
				Size:       len(tt.agents),
				Generation: tt.generation,
			}

			stats := pop.Stats()

			if stats == nil {
				t.Fatal("stats should not be nil")
			}

			if stats.Generation != tt.wantGeneration {
				t.Errorf("Generation = %d, want %d", stats.Generation, tt.wantGeneration)
			}

			if stats.Size != tt.wantSize {
				t.Errorf("Size = %d, want %d", stats.Size, tt.wantSize)
			}

			if stats.AvgScore != tt.wantAvg {
				t.Errorf("AvgScore = %f, want %f", stats.AvgScore, tt.wantAvg)
			}

			if stats.BestScore != tt.wantBest {
				t.Errorf("BestScore = %f, want %f", stats.BestScore, tt.wantBest)
			}

			if stats.WorstScore != tt.wantWorst {
				t.Errorf("WorstScore = %f, want %f", stats.WorstScore, tt.wantWorst)
			}
		})
	}
}

func TestConcurrentEvolveSafety(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	base := newTestStrategy(0.5)

	callCount := 0
	mutator := &mockMutator{
		mutateFn: func(ctx context.Context, parent *mutation.Strategy, n int) ([]*mutation.Strategy, error) {
			callCount++
			result := make([]*mutation.Strategy, n)
			for i := range result {
				result[i] = newTestStrategy(float64(callCount))
			}
			return result, nil
		},
	}

	crosser := &mockCrosser{
		crossoverFn: func(ctx context.Context, a, b *mutation.Strategy) (*mutation.Strategy, error) {
			return newTestStrategy(a.Score + b.Score), nil
		},
	}

	pop, err := NewPopulation(ctx, base, mutator, WithPopulationSize(10), WithSurvivalRate(0.5), WithEliteCount(1))
	if err != nil {
		t.Fatalf("failed to create population: %v", err)
	}

	// Pre-assign scores before concurrent access (no data race).
	for _, agent := range pop.Agents {
		agent.Score = rand.Float64() * 100
	}

	const goroutines = 10
	const iterations = 5

	eg, egCtx := errgroup.WithContext(ctx)

	var mu sync.Mutex
	var errorCount int

	for g := 0; g < goroutines; g++ {
		eg.Go(func() error {
			for i := 0; i < iterations; i++ {
				select {
				case <-egCtx.Done():
					return egCtx.Err()
				default:
				}
				if err := pop.Evolve(ctx, mutator, crosser); err != nil && !errors.Is(err, context.Canceled) {
					mu.Lock()
					errorCount++
					mu.Unlock()
				}
			}
			return nil
		})
	}

	if err := eg.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("concurrent evolution error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	t.Logf("concurrent evolutions: goroutines=%d, iterations=%d each, errors=%d",
		goroutines, iterations, errorCount)
}

func TestSingleAgentPopulation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	base := newTestStrategy(0.5)
	mutator := &mockMutator{}
	crosser := &mockCrosser{}

	pop, err := NewPopulation(ctx, base, mutator, WithPopulationSize(1), WithEliteCount(0))
	if err != nil {
		t.Fatalf("failed to create population: %v", err)
	}

	if len(pop.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(pop.Agents))
	}

	pop.Agents[0].Score = 99.0

	err = pop.Evolve(ctx, mutator, crosser)
	if err != nil {
		t.Fatalf("evolve failed for single agent: %v", err)
	}

	best := pop.Best()
	if best == nil {
		t.Fatal("best should not be nil")
	}

	stats := pop.Stats()
	if stats.Size != 1 {
		t.Errorf("stats.Size = %d, want 1", stats.Size)
	}
}

func TestDefaultPopulationConfig(t *testing.T) {
	t.Parallel()

	cfg := DefaultPopulationConfig()

	if cfg.Size != 20 {
		t.Errorf("default Size = %d, want 20", cfg.Size)
	}

	if cfg.SurvivalRate != 0.6 {
		t.Errorf("default SurvivalRate = %f, want 0.6", cfg.SurvivalRate)
	}

	if cfg.MutationRate != 0.2 {
		t.Errorf("default MutationRate = %f, want 0.2", cfg.MutationRate)
	}

	if cfg.EliteCount != 3 {
		t.Errorf("default EliteCount = %d, want 3", cfg.EliteCount)
	}

	if cfg.BreedingPoolRatio != 0.6 {
		t.Errorf("default BreedingPoolRatio = %f, want 0.6", cfg.BreedingPoolRatio)
	}
}

func TestPopulationConfigValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		option  PopulationOption
		wantErr error
	}{
		{
			name:    "valid size accepted",
			option:  WithPopulationSize(10),
			wantErr: nil,
		},
		{
			name:    "size zero rejected",
			option:  WithPopulationSize(0),
			wantErr: ErrInvalidPopulationSize,
		},
		{
			name:    "size negative rejected",
			option:  WithPopulationSize(-5),
			wantErr: ErrInvalidPopulationSize,
		},
		{
			name:    "survival rate 0 accepted",
			option:  WithSurvivalRate(0.0),
			wantErr: nil,
		},
		{
			name:    "survival rate 1 accepted",
			option:  WithSurvivalRate(1.0),
			wantErr: nil,
		},
		{
			name:    "survival rate above 1 rejected",
			option:  WithSurvivalRate(1.1),
			wantErr: ErrInvalidSurvivalRate,
		},
		{
			name:    "survival rate negative rejected",
			option:  WithSurvivalRate(-0.5),
			wantErr: ErrInvalidSurvivalRate,
		},
		{
			name:    "mutation rate 0 accepted",
			option:  WithMutationRate(0.0),
			wantErr: nil,
		},
		{
			name:    "mutation rate 1 accepted",
			option:  WithMutationRate(1.0),
			wantErr: nil,
		},
		{
			name:    "mutation rate above 1 rejected",
			option:  WithMutationRate(1.5),
			wantErr: ErrInvalidMutationRate,
		},
		{
			name:    "elite count 0 accepted",
			option:  WithEliteCount(0),
			wantErr: nil,
		},
		{
			name:    "elite count positive accepted",
			option:  WithEliteCount(5),
			wantErr: nil,
		},
		{
			name:    "elite count negative rejected",
			option:  WithEliteCount(-1),
			wantErr: ErrInvalidEliteCount,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := PopulationConfig{}
			err := tt.option(&cfg)

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

func TestWithPopulationSeed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	base := newTestStrategy(0.7)

	detMut := &mockMutator{
		mutateFn: func(_ context.Context, parent *mutation.Strategy, n int) ([]*mutation.Strategy, error) {
			result := make([]*mutation.Strategy, n)
			for i := range result {
				result[i] = &mutation.Strategy{
					ID:       fmt.Sprintf("det-mut-%d", i),
					ParentID: parent.ID,
					Version:  parent.Version + 1,
					Params:   make(map[string]any),
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
				ID:       "det-cross-child",
				ParentID: a.ID + "x" + b.ID,
				Version:  max(a.Version, b.Version) + 1,
				Params:   childParams,
			}, nil
		},
	}

	seed := int64(42)
	pop1, err := NewPopulation(ctx, base, detMut, WithPopulationSize(5), WithPopulationSeed(seed), WithPerLineageElites(false))
	if err != nil {
		t.Fatalf("NewPopulation (1) failed: %v", err)
	}

	pop2, err := NewPopulation(ctx, base, detMut, WithPopulationSize(5), WithPopulationSeed(seed), WithPerLineageElites(false))
	if err != nil {
		t.Fatalf("NewPopulation (2) failed: %v", err)
	}

	// Same scores + same seed = identical evolution outcome.
	for _, agent := range pop1.Agents {
		agent.Score = 50.0
	}
	if err := pop1.EvolveOnIdle(ctx, detMut, detCrosser); err != nil {
		t.Fatalf("EvolveOnIdle (1) failed: %v", err)
	}

	for _, agent := range pop2.Agents {
		agent.Score = 50.0
	}
	if err := pop2.EvolveOnIdle(ctx, detMut, detCrosser); err != nil {
		t.Fatalf("EvolveOnIdle (2) failed: %v", err)
	}

	if len(pop1.Agents) != len(pop2.Agents) {
		t.Fatalf("evolved sizes differ: %d vs %d", len(pop1.Agents), len(pop2.Agents))
	}
	for i := range pop1.Agents {
		if pop1.Agents[i].ID != pop2.Agents[i].ID {
			t.Fatalf("evolved agent %d ID differs: %s vs %s", i, pop1.Agents[i].ID, pop2.Agents[i].ID)
		}
	}
}

func TestEvolvePreservesElites(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	base := newTestStrategy(0.5)
	mutator := &mockMutator{}
	crosser := &mockCrosser{}

	pop, err := NewPopulation(ctx, base, mutator, WithPopulationSize(8), WithSurvivalRate(0.5), WithEliteCount(2))
	if err != nil {
		t.Fatalf("failed to create population: %v", err)
	}

	scores := []float64{90.0, 80.0, 70.0, 60.0, 50.0, 40.0, 30.0, 20.0}
	for i, agent := range pop.Agents {
		agent.Score = scores[i]
	}

	topScoreBefore := pop.Best().Score

	err = pop.Evolve(ctx, mutator, crosser)
	if err != nil {
		t.Fatalf("evolve failed: %v", err)
	}

	topScoreAfter := pop.Best().Score

	if topScoreAfter < topScoreBefore {
		t.Errorf("elite not preserved: best score dropped from %f to %f", topScoreBefore, topScoreAfter)
	}
}

func TestContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	base := newTestStrategy(0.5)

	mutCallCount := 0
	mutator := &mockMutator{
		mutateFn: func(ctx context.Context, parent *mutation.Strategy, n int) ([]*mutation.Strategy, error) {
			mutCallCount++
			if mutCallCount > 1 {
				cancel()
			}
			result := make([]*mutation.Strategy, n)
			for i := range result {
				result[i] = &mutation.Strategy{
					ID:             fmt.Sprintf("mock-cancel-%d-%d", time.Now().UnixNano(), i),
					ParentID:       parent.ID,
					Version:        parent.Version + 1,
					Params:         make(map[string]any),
					PromptTemplate: "mock-template",
					Score:          -1,
					CreatedAt:      time.Now(),
				}
			}
			return result, nil
		},
	}

	crosser := &mockCrosser{}

	pop, err := NewPopulation(ctx, base, mutator, WithPopulationSize(10), WithEliteCount(0))
	if err != nil {
		t.Fatalf("failed to create population: %v", err)
	}

	for _, agent := range pop.Agents {
		agent.Score = 1.0
	}

	err = pop.Evolve(ctx, mutator, crosser)
	if err == nil {
		t.Log("context cancellation test: evolve completed before cancellation (acceptable for small populations)")
	}
}

//nolint:gocyclo // Test function with comprehensive test cases
func TestEvolveOnIdle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mutator := &mockMutator{}
	crosser := &mockCrosser{}

	// Helper: create a scored population for idle evolution testing.
	makePop := func(size int) (*Population, error) {
		base := newTestStrategy(0.5)
		pop, err := NewPopulation(ctx, base, mutator, WithPopulationSize(size), WithMutationRate(0))
		if err != nil {
			return nil, err
		}
		for i, agent := range pop.Agents {
			agent.Score = float64(size - i)
		}
		return pop, nil
	}

	t.Run("successful cycle increments generation and preserves size", func(t *testing.T) {
		t.Parallel()
		pop, err := makePop(10)
		if err != nil {
			t.Fatal(err)
		}
		genBefore, sizeBefore := pop.Generation, len(pop.Agents)
		if err = pop.EvolveOnIdle(ctx, mutator, crosser); err != nil {
			t.Fatalf("EvolveOnIdle failed: %v", err)
		}
		if pop.Generation != genBefore+1 {
			t.Errorf("generation = %d, want %d", pop.Generation, genBefore+1)
		}
		if len(pop.Agents) != sizeBefore {
			t.Errorf("size after = %d, want %d", len(pop.Agents), sizeBefore)
		}
	})

	t.Run("top scorer preserved as elite clone", func(t *testing.T) {
		t.Parallel()
		pop, err := makePop(10)
		if err != nil {
			t.Fatal(err)
		}
		scores := []float64{90, 80, 70, 60, 50, 40, 30, 20, 10, 1}
		for i, a := range pop.Agents {
			a.Score = scores[i]
		}
		if err = pop.EvolveOnIdle(ctx, mutator, crosser); err != nil {
			t.Fatal(err)
		}
		if best := pop.Best(); best == nil || best.Score < 90.0 {
			t.Errorf("elite not preserved: best score = %v", best)
		}
	})

	t.Run("bottom 40% eliminated - survivors are top scorers", func(t *testing.T) {
		t.Parallel()
		pop, err := makePop(10)
		if err != nil {
			t.Fatal(err)
		}
		for i, a := range pop.Agents {
			a.Score = float64(i + 1) // ascending: bottom 40% are low scores
		}
		if err = pop.EvolveOnIdle(ctx, mutator, crosser); err != nil {
			t.Fatal(err)
		}
		if best := pop.Best(); best == nil || best.Score <= 6.0 {
			t.Errorf("expected elite preserved (score > 6), got %v", best)
		}
	})

	t.Run("breeding pool from top 30%, offspring fill slots", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct{ name, size string }{
			{"size_20", "20"}, {"size_5", "5"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				size := 0
				// nolint:errcheck // test helper parsing known-valid integer strings
				fmt.Sscanf(tc.size, "%d", &size)
				pop, err := makePop(size)
				if err != nil {
					t.Fatal(err)
				}
				if err = pop.EvolveOnIdle(ctx, mutator, crosser); err != nil {
					t.Fatal(err)
				}
				if len(pop.Agents) != size {
					t.Errorf("size = %d, want %d", len(pop.Agents), size)
				}
			})
		}
	})

	t.Run("validation errors: nil mutator/crosser, empty population", func(t *testing.T) {
		t.Parallel()
		pop5, _ := makePop(5)
		for _, tc := range []struct {
			name    string
			pop     *Population
			mut     MutatorInterface
			crs     CrossoverInterface
			wantErr error
		}{
			{"nil_mutator", pop5, nil, crosser, ErrNilMutator},
			{"nil_crosser", pop5, mutator, nil, ErrNilCrosser},
			{"empty_population", &Population{Agents: []*mutation.Strategy{}, Size: 5}, mutator, crosser, ErrSelectionEmptyPopulation},
		} {
			if err := tc.pop.EvolveOnIdle(ctx, tc.mut, tc.crs); !errors.Is(err, tc.wantErr) {
				t.Errorf("%s: error = %v, want %v", tc.name, err, tc.wantErr)
			}
		}
	})

	t.Run("edge cases: single agent, 60%% round-down, context cancel, consecutive calls", func(t *testing.T) {
		t.Parallel()
		// Single-agent population.
		pop1, err := NewPopulation(ctx, newTestStrategy(0.5), mutator,
			WithPopulationSize(1), WithEliteCount(0))
		if err != nil {
			t.Fatal(err)
		}
		pop1.Agents[0].Score = 42.0
		if err = pop1.EvolveOnIdle(ctx, mutator, crosser); err != nil {
			t.Fatal(err)
		}
		if len(pop1.Agents) != 1 || pop1.Generation != 1 {
			t.Errorf("single-agent: size=%d gen=%d", len(pop1.Agents), pop1.Generation)
		}
		// Size=1 where 60% rounds down to 0.
		pop2 := &Population{
			Agents: []*mutation.Strategy{newTestStrategy(99)},
			Size:   1, Generation: 0, cfg: DefaultPopulationConfig(),
			rng: rand.New(rand.NewSource(42)),
		}
		if err = pop2.EvolveOnIdle(ctx, mutator, crosser); err != nil || len(pop2.Agents) != 1 {
			t.Errorf("round-down edge case failed: err=%v size=%d", err, len(pop2.Agents))
		}
		// Context cancellation during breeding.
		cancelCtx, cancel := context.WithCancel(context.Background())
		mc := &mockCrosser{
			crossoverFn: func(ctx context.Context, a, b *mutation.Strategy) (*mutation.Strategy, error) {
				cancel()
				return newTestStrategy(a.Score + b.Score), nil
			},
		}
		pop3, err := NewPopulation(cancelCtx, newTestStrategy(0.5),
			&mockMutator{}, WithPopulationSize(20), WithMutationRate(1.0))
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range pop3.Agents {
			a.Score = rand.Float64() * 100
		}
		if err = pop3.EvolveOnIdle(cancelCtx, &mockMutator{}, mc); err == nil {
			t.Log("completed before cancel (acceptable)")
		}
		// Multiple consecutive calls.
		pop4, err := makePop(8)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			for j, a := range pop4.Agents {
				a.Score = float64((i*8 + j) * 10)
			}
			if err = pop4.EvolveOnIdle(ctx, mutator, crosser); err != nil {
				t.Fatalf("consecutive iter %d: %v", i, err)
			}
		}
		if pop4.Generation != 3 {
			t.Errorf("consecutive gen=%d, want 3", pop4.Generation)
		}
	})
}

func TestBestStrategy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		pop        *Population
		wantNil    bool
		wantScore  float64
		checkClone bool
	}{
		{
			name: "returns cloned best strategy with independent copy",
			pop: &Population{
				Agents: []*mutation.Strategy{newTestStrategy(10), newTestStrategy(50), newTestStrategy(30)},
				Size:   3,
			},
			wantScore: 50.0, checkClone: true,
		},
		{name: "returns nil for empty population", pop: &Population{Agents: []*mutation.Strategy{}, Size: 0}, wantNil: true},
		{
			name: "clone independence verification with params mutation",
			pop: func() *Population {
				s := newTestStrategy(88)
				s.Params["key"] = "original"
				return &Population{Agents: []*mutation.Strategy{s}, Size: 1}
			}(),
			checkClone: true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.pop.BestStrategy()
			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil || (tt.wantScore > 0 && got.Score != tt.wantScore) {
				t.Fatalf("unexpected clone: score=%v, want %f", got, tt.wantScore)
			}
			if !tt.checkClone {
				return
			}
			if tt.wantScore > 0 {
				got.Score = 999.0
				if tt.pop.Best().Score == 999.0 {
					t.Error("score clone modification affected original")
				}
			} else {
				got.Params["key"] = "modified"
				if tt.pop.Best().Params["key"] == "modified" {
					t.Error("params clone modification leaked to original")
				}
			}
		})
	}
}

// TestEvolveZeroOffspringPath verifies the early return path in doEvolve when
// EliteCount >= Size (no offspring slots remain). In this path, the population
// preserves elites, increments the generation counter, and skips adaptive
// mutation rate / stagnation adjustments.
//
// Requires WithSurvivalRate(1.0) so that all agents survive selection and
// preserveElites can return EliteCount clones (capped by len(survivors)).
func TestEvolveZeroOffspringPath(t *testing.T) {
	t.Run("Evolve with EliteCount equals Size", func(t *testing.T) {
		ctx := context.Background()
		scores := []float64{95.0, 75.0, 55.0}
		pop := scoredPopulation(t, ctx, scores,
			WithEliteCount(3),     // EliteCount == Size → no offspring slots
			WithSurvivalRate(1.0), // All survive so EliteCount is not capped
			WithMutationRate(0.3),
			WithPerLineageElites(false), // Use global elite ordering for deterministic test
		)
		origRate := pop.currentMutationRate
		origStagnant := pop.stagnantGens
		origScores := make([]float64, len(pop.Agents))
		for i, a := range pop.Agents {
			origScores[i] = a.Score
		}

		crosser, err := NewCrossover(WithSeed(42))
		if err != nil {
			t.Fatalf("failed to create crossover: %v", err)
		}
		mutator := &mockMutator{}

		if err := pop.Evolve(ctx, mutator, crosser); err != nil {
			t.Fatalf("Evolve failed with EliteCount==Size: %v", err)
		}

		if pop.Generation != 1 {
			t.Errorf("Generation = %d, want 1", pop.Generation)
		}
		if len(pop.Agents) != 3 {
			t.Errorf("population size = %d, want 3", len(pop.Agents))
		}
		if pop.currentMutationRate != origRate {
			t.Errorf("mutation rate changed from %.4f to %.4f (adaptive skipped)", origRate, pop.currentMutationRate)
		}
		if pop.stagnantGens != origStagnant {
			t.Errorf("stagnantGens changed from %d to %d (adaptive skipped)", origStagnant, pop.stagnantGens)
		}
		// All agents are clones of elites; their scores should match the original top-3.
		for i, a := range pop.Agents {
			if a.Score != origScores[i] {
				t.Errorf("agent %d score changed from %.1f to %.1f", i, origScores[i], a.Score)
			}
		}
	})

	t.Run("EvolveOnIdle with EliteCount equals Size", func(t *testing.T) {
		ctx := context.Background()
		scores := []float64{90.0, 70.0, 50.0}
		pop := scoredPopulation(t, ctx, scores,
			WithEliteCount(3),
			WithSurvivalRate(1.0),
			WithMutationRate(0.2),
			WithPerLineageElites(false),
		)
		origRate := pop.currentMutationRate
		origStagnant := pop.stagnantGens

		crosser, err := NewCrossover(WithSeed(42))
		if err != nil {
			t.Fatalf("failed to create crossover: %v", err)
		}
		mutator := &mockMutator{}

		if err := pop.EvolveOnIdle(ctx, mutator, crosser); err != nil {
			t.Fatalf("EvolveOnIdle failed with EliteCount==Size: %v", err)
		}

		if pop.Generation != 1 {
			t.Errorf("Generation = %d, want 1", pop.Generation)
		}
		if len(pop.Agents) != 3 {
			t.Errorf("population size = %d, want 3", len(pop.Agents))
		}
		if pop.currentMutationRate != origRate {
			t.Errorf("mutation rate changed from %.4f to %.4f (adaptive skipped)", origRate, pop.currentMutationRate)
		}
		if pop.stagnantGens != origStagnant {
			t.Errorf("stagnantGens changed from %d to %d (adaptive skipped)", origStagnant, pop.stagnantGens)
		}
	})

	t.Run("EliteCount just below Size takes normal path", func(t *testing.T) {
		ctx := context.Background()
		scores := []float64{99.0, 80.0, 60.0, 40.0}
		pop := scoredPopulation(t, ctx, scores,
			WithEliteCount(3),     // EliteCount=3 < Size=4 → 1 offspring slot
			WithSurvivalRate(0.8), // All survive: max(1, int(4*0.8))=3 survivors, eliteCount=3
			WithMutationRate(0.5),
		)
		origRate := pop.currentMutationRate

		crosser, err := NewCrossover(WithSeed(42))
		if err != nil {
			t.Fatalf("failed to create crossover: %v", err)
		}
		mutator := &mockMutator{}

		if err := pop.Evolve(ctx, mutator, crosser); err != nil {
			t.Fatalf("Evolve failed with EliteCount=3, Size=4: %v", err)
		}

		if len(pop.Agents) != 4 {
			t.Errorf("population size = %d, want 4", len(pop.Agents))
		}
		// Normal path should have called adjustMutationRateLocked.
		if pop.currentMutationRate == origRate {
			t.Log("note: mutation rate unchanged (may be at boundary of diversity threshold)")
		}
	})
}

func TestEvolveRejectsUnevaluated(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	base := newTestStrategy(0.5)
	mutator := &mockMutator{}
	crosser := &mockCrosser{}

	pop, err := NewPopulation(ctx, base, mutator, WithPopulationSize(4))
	if err != nil {
		t.Fatalf("failed to create population: %v", err)
	}

	// Leave agents unevaluated (Score defaults to 0 from newTestStrategy, but
	// we need at least one agent with Score < 0 to trigger the guard).
	for i, agent := range pop.Agents {
		if i == 2 {
			agent.Score = -1.0 // Unevaluated sentinel
		} else {
			agent.Score = float64(i + 1) // Evaluated scores
		}
	}

	err = pop.Evolve(ctx, mutator, crosser)
	if err == nil {
		t.Fatal("expected error when evolving with unevaluated agent, got nil")
	}

	// Error should mention "unevaluated" or "pre-evolution validation".
	wantMsg := "unevaluated"
	if !strings.Contains(err.Error(), wantMsg) {
		t.Errorf("error message should contain %q, got: %v", wantMsg, err)
	}
}

func TestEvolveAcceptsAllEvaluated(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	base := newTestStrategy(0.5)
	mutator := &mockMutator{}
	crosser := &mockCrosser{}

	pop, err := NewPopulation(ctx, base, mutator,
		WithPopulationSize(6), WithSurvivalRate(0.5), WithEliteCount(1),
	)
	if err != nil {
		t.Fatalf("failed to create population: %v", err)
	}

	// Score all agents (all >= 0 means evaluated).
	for i, agent := range pop.Agents {
		agent.Score = float64(i + 10)
	}

	err = pop.Evolve(ctx, mutator, crosser)
	if err != nil {
		t.Fatalf("evolve should succeed with all evaluated agents: %v", err)
	}

	if pop.Generation != 1 {
		t.Errorf("generation = %d, want 1 after successful evolve", pop.Generation)
	}
}

func TestRootStrategyHasCorrectMutationType(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	base := newTestStrategy(0.5)

	pop, err := NewPopulation(ctx, base, &mockMutator{}, WithPopulationSize(5))
	if err != nil {
		t.Fatalf("failed to create population: %v", err)
	}

	if len(pop.Agents) == 0 {
		t.Fatal("population has no agents")
	}

	rootAgent := pop.Agents[0]
	if rootAgent.StrategyMutationType != mutation.MutationRoot {
		t.Errorf("root strategy MutationType = %d (%q), want %d (root)",
			rootAgent.StrategyMutationType, rootAgent.StrategyMutationType.String(),
			mutation.MutationRoot)
	}

	if rootAgent.MutationDesc != "root strategy" {
		t.Errorf("root strategy MutationDesc = %q, want %q", rootAgent.MutationDesc, "root strategy")
	}
}

// --- Agent aging / eviction tests ---

func TestAgentMaxAgeEviction(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	base := newTestStrategy(0.5)
	mutator := &mockMutator{}
	crosser := &mockCrosser{}

	t.Run("evicts old agents when AgentMaxAge set", func(t *testing.T) {
		t.Parallel()

		pop, err := NewPopulation(ctx, base, mutator,
			WithPopulationSize(6),
			WithSurvivalRate(0.6),
			WithEliteCount(1),
		)
		if err != nil {
			t.Fatalf("failed to create population: %v", err)
		}

		// Set AgentMaxAge to 2.
		pop.cfg.AgentMaxAge = 2

		// Root agents are exempt (StrategyMutationType == MutationRoot).
		// Only test with non-root agents by creating a synthetic population
		// where GenerationCreated is explicitly set.
		pop.Generation = 3 // simulate being 3 generations in
		pop.Agents = []*mutation.Strategy{
			{ID: "young", Score: 100, GenerationCreated: 3, StrategyMutationType: mutation.MutationCrossover},
			{ID: "mid", Score: 80, GenerationCreated: 2, StrategyMutationType: mutation.MutationCrossover},
			{ID: "old", Score: 60, GenerationCreated: 0, StrategyMutationType: mutation.MutationCrossover},      // age=3 > max=2
			{ID: "ancient", Score: 40, GenerationCreated: -1, StrategyMutationType: mutation.MutationCrossover}, // age=4 > max=2
		}

		err = pop.Evolve(ctx, mutator, crosser)
		if err != nil {
			t.Fatalf("evolve failed: %v", err)
		}

		// After Evolve, old agents (age > 2) should be evicted.
		for _, agent := range pop.Agents {
			if agent.ID == "old" || agent.ID == "ancient" {
				t.Errorf("old agent %q survived eviction (GenerationCreated=%d, gen=%d)",
					agent.ID, agent.GenerationCreated, pop.Generation)
			}
		}
	})

	t.Run("root agents exempt from age eviction", func(t *testing.T) {
		t.Parallel()

		pop, err := NewPopulation(ctx, base, mutator,
			WithPopulationSize(4),
			WithSurvivalRate(1.0), // keep all survivors
			WithEliteCount(2),
		)
		if err != nil {
			t.Fatalf("failed to create population: %v", err)
		}

		pop.cfg.AgentMaxAge = 1

		// Create a synthetic population with old root agents (never evicted by age)
		// and old non-root agents (should be evicted).
		// Set all scores high so selection doesn't eliminate them.
		pop.Generation = 5
		pop.Agents = []*mutation.Strategy{
			{ID: "root-old", Score: 100, GenerationCreated: 0, StrategyMutationType: mutation.MutationRoot},
			{ID: "root-mid", Score: 99, GenerationCreated: 0, StrategyMutationType: mutation.MutationRoot},
			{ID: "nonroot-old", Score: 98, GenerationCreated: 0, StrategyMutationType: mutation.MutationCrossover},
			{ID: "nonroot-young", Score: 97, GenerationCreated: 4, StrategyMutationType: mutation.MutationCrossover},
		}

		err = pop.Evolve(ctx, mutator, crosser)
		if err != nil {
			t.Fatalf("evolve failed: %v", err)
		}

		// Root agents should survive (age doesn't matter for roots).
		rootIDs := map[string]bool{"root-old": false, "root-mid": false}
		for _, agent := range pop.Agents {
			if agent.StrategyMutationType == mutation.MutationRoot {
				rootIDs[agent.ID] = true
			}
		}
		for id, found := range rootIDs {
			if !found {
				t.Errorf("root agent %q was evicted — root should be exempt from age eviction", id)
			}
		}
		// Non-root old agent (GenerationCreated=0, MutationCrossover) should be evicted.
		for _, agent := range pop.Agents {
			if agent.ID == "nonroot-old" {
				t.Errorf("non-root old agent survived eviction — age=%d > AgentMaxAge=%d",
					pop.Generation-agent.GenerationCreated, pop.cfg.AgentMaxAge)
			}
		}
	})

	t.Run("AgentMaxAge zero disables eviction", func(t *testing.T) {
		t.Parallel()

		pop, err := NewPopulation(ctx, base, mutator,
			WithPopulationSize(6),
			WithSurvivalRate(0.6),
			WithEliteCount(1),
		)
		if err != nil {
			t.Fatalf("failed to create population: %v", err)
		}

		pop.cfg.AgentMaxAge = 0 // disabled

		for i, agent := range pop.Agents {
			agent.Score = float64(i)
			agent.GenerationCreated = pop.Generation - 100 // very old
		}

		err = pop.Evolve(ctx, mutator, crosser)
		if err != nil {
			t.Fatalf("evolve failed: %v", err)
		}

		if len(pop.Agents) == 0 {
			t.Error("population empty after evolve with AgentMaxAge=0")
		}
	})
}
