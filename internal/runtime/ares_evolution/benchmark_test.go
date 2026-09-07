// Package evolution provides performance benchmarks for high-level evolution system
// operations including DreamCycle orchestration, WiredEvolutionSystem construction,
// idle evolution pipelines, and end-to-end workflows.
package evolution

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// --- Helper types for evolution benchmarks ---

// benchMutator is a fast mutator for benchmarking that minimizes overhead
// by avoiding UUID generation and complex parameter range lookups.
type benchMutator struct {
	rng *rand.Rand
}

// Mutate generates n mutated child strategies from the given parent strategy.
func (m *benchMutator) Mutate(_ context.Context, parent Strategy, n int) ([]Strategy, error) {
	result := make([]Strategy, n)
	for i := range result {
		cloned := cloneStrategy(parent)
		cloned.ID = fmt.Sprintf("bench-mut-%d", i)
		cloned.Version = parent.Version + 1
		cloned.Score = -1

		if len(cloned.Params) > 0 {
			keys := make([]string, 0, len(cloned.Params))
			for k := range cloned.Params {
				keys = append(keys, k)
			}
			idx := m.rng.Intn(len(keys))
			cloned.Params[keys[idx]] = m.rng.Float64()
		}
		result[i] = cloned
	}
	return result, nil
}

// cloneStrategy creates a deep copy of an evolution.Strategy.
func cloneStrategy(s Strategy) Strategy {
	params := make(map[string]any, len(s.Params))
	for k, v := range s.Params {
		params[k] = v
	}
	return Strategy{
		ID:                   s.ID,
		Name:                 s.Name,
		Version:              s.Version,
		Params:               params,
		ParentID:             s.ParentID,
		PromptTemplate:       s.PromptTemplate,
		StrategyMutationType: s.StrategyMutationType,
		MutationDesc:         s.MutationDesc,
		Score:                s.Score,
		CreatedAt:            s.CreatedAt,
	}
}

// benchMockTester is a mock TesterInterface for benchmarking that returns
// deterministic results without actual arena work.
type benchMockTester struct{}

// Run implements TesterInterface with a fast mock response.
func (t *benchMockTester) Run(_ context.Context, cfg RegressionConfig) (*RegressionResult, error) {
	return &RegressionResult{
		CandidateScore: 75.0,
		BaselineScore:  60.0,
		WinRate:        0.8,
		TotalTasks:     cfg.TaskSampleSize,
	}, nil
}

// ==========================================================================
// Group 6: Wired System & Dream Cycle Benchmarks
// ==========================================================================

// BenchmarkDreamCycle_SingleRun measures a single dream cycle orchestration:
// mutate(3 candidates) → arena_test(3 candidates) → select_best → record_lineage.
func BenchmarkDreamCycle_SingleRun(b *testing.B) {
	b.ReportAllocs()

	ctx := context.Background()

	b.StopTimer()
	mutator := &benchMutator{rng: rand.New(rand.NewSource(42))}
	tester := &benchMockTester{}
	genealogy := NewPopulationGenealogyRecorder()

	// Create a minimal scheduler for dream cycle (required dependency).
	scheduler := NewEvolutionScheduler(nil, nil)

	dreamCycle, err := NewDreamCycle(
		scheduler,
		mutator,
		tester,
		genealogy,
		WithDreamCycleConfig(DreamCycleConfig{
			Enabled:              true,
			MinTasksBeforeEvolve: 0,
			MaxMutations:         3,
			MinWinRate:           0.5,
			Cooldown:             0,
			TaskSampleSize:       5,
			QuickRejectRuns:      0,
		}),
	)
	if err != nil {
		b.Fatal(err)
	}
	b.StartTimer()

	data := CallbackData{AgentID: "bench-agent"}
	for i := 0; i < b.N; i++ {
		_ = dreamCycle.Run(ctx, data)
	}
}

// BenchmarkWiredSystem_Creation measures NewWiredEvolutionSystem() construction cost
// across different population sizes.
func BenchmarkWiredSystem_Creation(b *testing.B) {
	b.ReportAllocs()

	base := &mutation.Strategy{
		ID:             "bench-base",
		Version:        1,
		Params:         map[string]any{"temperature": 0.7, "max_tokens": 4096},
		PromptTemplate: "benchmark base strategy template",
		Score:          50.0,
		CreatedAt:      time.Now(),
	}

	for _, popSize := range []int{10, 20, 50, 100} {
		b.Run(fmt.Sprintf("pop_%d", popSize), func(b *testing.B) {
			cfg := DefaultSystemConfig()
			cfg.PopulationSize = popSize
			cfg.EnableDreamCycle = false
			cfg.EnableScheduler = false

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = NewWiredEvolutionSystem(base, cfg)
			}
		})
	}
}

// BenchmarkWiredSystem_IdleEvolution measures RunIdleEvolution() for N generations
// with lineage recording overhead per generation.
func BenchmarkWiredSystem_IdleEvolution(b *testing.B) {
	b.ReportAllocs()

	ctx := context.Background()
	base := &mutation.Strategy{
		ID:             "bench-base",
		Version:        1,
		Params:         map[string]any{"temperature": 0.7, "max_tokens": 4096},
		PromptTemplate: "benchmark base strategy template",
		Score:          50.0,
		CreatedAt:      time.Now(),
	}
	cfg := DefaultSystemConfig()
	cfg.PopulationSize = 20
	cfg.EnableDreamCycle = false
	cfg.EnableScheduler = false

	for _, gens := range []int{10, 50, 100} {
		b.Run(fmt.Sprintf("%d_generations", gens), func(b *testing.B) {
			b.StopTimer()
			system, err := NewWiredEvolutionSystem(base, cfg)
			if err != nil {
				b.Fatal(err)
			}

			// Assign initial scores so evolution has data to work with.
			scoreRng := rand.New(rand.NewSource(99))
			agents, _ := system.Population.Snapshot()
			for _, agent := range agents {
				agent.Score = scoreRng.Float64() * 100
			}
			b.StartTimer()

			for i := 0; i < b.N; i++ {
				_ = RunIdleEvolution(ctx, system, gens)
			}
			b.ReportMetric(float64(gens), "generations")
		})
	}
}

// BenchmarkFullPipeline measures end-to-end "create-evolve-extract" workflow:
// Create WiredSystem → Run 50 gens idle evolution → Get best strategy → Record lineage.
func BenchmarkFullPipeline(b *testing.B) {
	b.ReportAllocs()

	const nGenerations = 50

	ctx := context.Background()
	base := &mutation.Strategy{
		ID:             "bench-base",
		Version:        1,
		Params:         map[string]any{"temperature": 0.7, "max_tokens": 4096},
		PromptTemplate: "benchmark base strategy template",
		Score:          50.0,
		CreatedAt:      time.Now(),
	}
	cfg := DefaultSystemConfig()
	cfg.PopulationSize = 20
	cfg.EnableDreamCycle = false
	cfg.EnableScheduler = false

	b.StopTimer()
	system, err := NewWiredEvolutionSystem(base, cfg)
	if err != nil {
		b.Fatal(err)
	}
	scoreRng := rand.New(rand.NewSource(99))
	agents, _ := system.Population.Snapshot()
	for _, agent := range agents {
		agent.Score = scoreRng.Float64() * 100
	}
	b.StartTimer()

	for i := 0; i < b.N; i++ {
		// Step 1: Run idle evolution for N generations.
		_ = RunIdleEvolution(ctx, system, nGenerations)

		// Step 2: Extract best strategy.
		_, _ = BestStrategyFromSystem(system)

		// Step 3: Genealogy count reflects recorded lineages.
		_ = system.Genealogy.Count()

		b.ReportMetric(float64(nGenerations), "generations")
	}
}

// ==========================================================================
// Group 7: Adaptive Mutation Benchmarks (using genome Population directly)
// ==========================================================================

// benchGenomeMutator wraps a genome-compatible mutator for adaptive mutation benchmarks.
type benchGenomeMutator struct {
	rng *rand.Rand
}

// Mutate generates n mutated child strategies from the given parent strategy.
func (m *benchGenomeMutator) Mutate(_ context.Context, parent *mutation.Strategy, n int) ([]*mutation.Strategy, error) {
	result := make([]*mutation.Strategy, n)
	for i := range result {
		cloned := parent.Clone()
		cloned.ID = fmt.Sprintf("bench-mut-%d", i)
		cloned.Version = parent.Version + 1
		cloned.Score = -1

		if len(cloned.Params) > 0 {
			keys := make([]string, 0, len(cloned.Params))
			for k := range cloned.Params {
				keys = append(keys, k)
			}
			idx := m.rng.Intn(len(keys))
			cloned.Params[keys[idx]] = m.rng.Float64()
		}
		result[i] = cloned
	}
	return result, nil
}

// BenchmarkAdaptiveMutation measures adaptive mutation rate behavior over
// multiple generations, comparing diversity-aware vs fixed-rate mutation.
func BenchmarkAdaptiveMutation(b *testing.B) {
	b.ReportAllocs()

	const nGenerations = 50
	const popSize = 20

	mutator := &benchGenomeMutator{rng: rand.New(rand.NewSource(42))}
	crosser, err := genome.NewCrossover(genome.WithSeed(42))
	if err != nil {
		b.Fatal(err)
	}

	ctx := context.Background()

	// Fixed-rate baseline (no adaptive options).
	b.Run("fixed_rate", func(b *testing.B) {
		b.StopTimer()
		base := &mutation.Strategy{
			ID:      "adaptive-bench-base",
			Version: 1,
			Params:  map[string]any{"p1": 0.5, "p2": 0.5, "p3": 0.5, "p4": 0.5, "p5": 0.5},
			Score:   50.0,
		}
		pop, err := genome.NewPopulation(ctx, base, mutator,
			genome.WithPopulationSize(popSize),
			genome.WithSurvivalRate(0.6),
			genome.WithMutationRate(0.2),
			genome.WithEliteCount(1),
		)
		if err != nil {
			b.Fatal(err)
		}
		scoreRng := rand.New(rand.NewSource(99))
		agents, _ := pop.Snapshot()
		for _, agent := range agents {
			agent.Score = scoreRng.Float64() * 100
		}
		b.StartTimer()

		for i := 0; i < b.N; i++ {
			for g := 0; g < nGenerations; g++ {
				_ = pop.EvolveOnIdle(ctx, mutator, crosser)
				agents, _ = pop.Snapshot()
				for _, agent := range agents {
					agent.Score = scoreRng.Float64() * 100
				}
			}
		}
	})

	// Adaptive rate with diversity monitoring and stagnation handling.
	b.Run("adaptive_rate", func(b *testing.B) {
		b.StopTimer()
		base := &mutation.Strategy{
			ID:      "adaptive-bench-base",
			Version: 1,
			Params:  map[string]any{"p1": 0.5, "p2": 0.5, "p3": 0.5, "p4": 0.5, "p5": 0.5},
			Score:   50.0,
		}
		pop, err := genome.NewPopulation(ctx, base, mutator,
			genome.WithPopulationSize(popSize),
			genome.WithSurvivalRate(0.6),
			genome.WithMutationRate(0.2),
			genome.WithEliteCount(1),
			genome.WithMinMutationRate(0.05),
			genome.WithMaxMutationRate(0.5),
			genome.WithMaxStagnantGenerations(10),
			genome.WithDiversityThreshold(0.15),
		)
		if err != nil {
			b.Fatal(err)
		}
		scoreRng := rand.New(rand.NewSource(99))
		agents, _ := pop.Snapshot()
		for _, agent := range agents {
			agent.Score = scoreRng.Float64() * 100
		}
		b.StartTimer()

		for i := 0; i < b.N; i++ {
			for g := 0; g < nGenerations; g++ {
				_ = pop.EvolveOnIdle(ctx, mutator, crosser)
				agents, _ = pop.Snapshot()
				for _, agent := range agents {
					agent.Score = scoreRng.Float64() * 100
				}
			}
		}
	})
}
