// Package genome provides performance benchmarks for genetic algorithm operations.
// It measures crossover, selection, evolution cycle, and memory allocation costs
// across various population sizes and parameter configurations.
package genome

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// --- Helper types and functions for benchmarks ---

// randStrategy creates a random strategy with nParams parameters and tmplLen-length
// prompt template for benchmarking. Uses deterministic RNG seeded with caller's source.
func randStrategy(rng *rand.Rand, nParams int, tmplLen int) *mutation.Strategy {
	params := make(map[string]any, nParams)
	for i := 0; i < nParams; i++ {
		params[fmt.Sprintf("param_%d", i)] = rng.Float64()
	}

	tmpl := make([]byte, tmplLen)
	for i := range tmpl {
		tmpl[i] = byte('a' + rng.Intn(26))
	}

	return &mutation.Strategy{
		ID:             fmt.Sprintf("bench-%d", rng.Intn(1000000)),
		Version:        rng.Intn(10) + 1,
		Params:         params,
		PromptTemplate: string(tmpl),
		Score:          rng.Float64() * 100,
		CreatedAt:      time.Now(),
	}
}

// benchMutator is a fast mutator for benchmarking that minimizes overhead
// by avoiding UUID generation and complex parameter range lookups.
type benchMutator struct {
	rng *rand.Rand
}

// Mutate generates n mutated child strategies from the given parent strategy.
// Each child is a clone with one random parameter value flipped.
func (m *benchMutator) Mutate(ctx context.Context, parent *mutation.Strategy, n int) ([]*mutation.Strategy, error) {
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
			sort.Strings(keys)
			idx := m.rng.Intn(len(keys))
			cloned.Params[keys[idx]] = m.rng.Float64()
		}
		result[i] = cloned
	}
	return result, nil
}

// benchPopulation creates a population of given size with random strategies for benchmarking.
// All agents receive random scores in [0, 100) to simulate evaluation results.
func benchPopulation(b *testing.B, size int) *Population {
	base := randStrategy(rand.New(rand.NewSource(42)), 5, 100)
	base.Score = 50.0

	mutator := &benchMutator{rng: rand.New(rand.NewSource(42))}

	pop, err := NewPopulation(context.Background(), base, mutator,
		WithPopulationSize(size),
		WithSurvivalRate(0.6),
		WithMutationRate(0.2),
		WithEliteCount(1),
	)
	if err != nil {
		b.Fatal(err)
	}

	rng := rand.New(rand.NewSource(99))
	for _, a := range pop.Agents {
		a.Score = rng.Float64() * 100
	}

	return pop
}

// ==========================================================================
// Group 1: Crossover Benchmarks
// ==========================================================================

// BenchmarkCrossoverUniform benchmarks uniform crossover with default-sized strategies (~10 params).
// Measures per-operation cost including param map merging and prompt template selection.
func BenchmarkCrossoverUniform(b *testing.B) {
	b.ReportAllocs()

	crosser, err := NewCrossover(WithSeed(42))
	if err != nil {
		b.Fatal(err)
	}

	parentA := randStrategy(rand.New(rand.NewSource(10)), 10, 200)
	parentB := randStrategy(rand.New(rand.NewSource(20)), 10, 200)

	b.ResetTimer()
	ctx := context.Background()
	for i := 0; i < b.N; i++ {
		_, _ = crosser.Crossover(ctx, parentA, parentB)
	}
}

// BenchmarkCrossoverUniform_LargeParams benchmarks uniform crossover with 100 parameters.
// Measures how crossover cost scales with parameter count.
func BenchmarkCrossoverUniform_LargeParams(b *testing.B) {
	b.ReportAllocs()

	crosser, err := NewCrossover(WithSeed(42))
	if err != nil {
		b.Fatal(err)
	}

	parentA := randStrategy(rand.New(rand.NewSource(10)), 100, 200)
	parentB := randStrategy(rand.New(rand.NewSource(20)), 100, 200)

	b.ResetTimer()
	ctx := context.Background()
	for i := 0; i < b.N; i++ {
		_, _ = crosser.Crossover(ctx, parentA, parentB)
	}
}

// BenchmarkCrossoverParallel benchmarks concurrent crossover operations using RunParallel.
// Measures throughput under parallel load with GOMAXPROCS workers.
func BenchmarkCrossoverParallel(b *testing.B) {
	b.ReportAllocs()

	crosser, err := NewCrossover(WithSeed(42))
	if err != nil {
		b.Fatal(err)
	}

	parentA := randStrategy(rand.New(rand.NewSource(10)), 10, 200)
	parentB := randStrategy(rand.New(rand.NewSource(20)), 10, 200)

	ctx := context.Background()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = crosser.Crossover(ctx, parentA, parentB)
		}
	})
}

// ==========================================================================
// Group 2: Selection Benchmarks
// ==========================================================================

// BenchmarkTruncationSelection benchmarks top-N selection on various population sizes.
// Selects top 30% of individuals. Measures sort + slice cost as population scales.
func BenchmarkTruncationSelection(b *testing.B) {
	b.ReportAllocs()

	for _, size := range []int{10, 100, 500, 1000} {
		b.Run(fmt.Sprintf("pop_%d", size), func(b *testing.B) {
			population := make([]*mutation.Strategy, size)
			rng := rand.New(rand.NewSource(int64(size)))
			for i := range population {
				population[i] = randStrategy(rng, 5, 100)
			}

			selectN := size * 30 / 100
			if selectN < 1 {
				selectN = 1
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Inline: sort by score descending, take top N.
				sorted := make([]*mutation.Strategy, len(population))
				copy(sorted, population)
				SortByScore(sorted)
				_ = sorted[:selectN]
			}
		})
	}
}

// BenchmarkTournamentSelection benchmarks tournament selection with varying tournament sizes
// and population sizes. Larger tournament sizes increase selection pressure and comparison cost.
func BenchmarkTournamentSelection(b *testing.B) {
	b.ReportAllocs()

	ctx := context.Background()

	for _, popSize := range []int{50, 200} {
		b.Run(fmt.Sprintf("pop_%d", popSize), func(b *testing.B) {
			population := make([]*mutation.Strategy, popSize)
			rng := rand.New(rand.NewSource(int64(popSize)))
			for i := range population {
				population[i] = randStrategy(rng, 5, 100)
			}

			for _, k := range []int{2, 3, 5, 10} {
				b.Run(fmt.Sprintf("k=%d", k), func(b *testing.B) {
					sel, err := NewTournamentSelection(
						WithTournamentSize(k),
						WithTournamentSeed(42),
					)
					if err != nil {
						b.Fatal(err)
					}

					selectN := popSize / 2
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						_, _ = sel.Select(ctx, population, selectN)
					}
				})
			}
		})
	}
}

// BenchmarkRouletteWheelSelection benchmarks roulette wheel (fitness proportionate) selection.
// Compares against truncation performance — roulette has O(n) per spin vs truncation O(n log n) total.
func BenchmarkRouletteWheelSelection(b *testing.B) {
	b.ReportAllocs()

	ctx := context.Background()

	for _, size := range []int{10, 100, 500, 1000} {
		b.Run(fmt.Sprintf("pop_%d", size), func(b *testing.B) {
			population := make([]*mutation.Strategy, size)
			rng := rand.New(rand.NewSource(int64(size)))
			for i := range population {
				population[i] = randStrategy(rng, 5, 100)
			}

			sel, err := NewRouletteWheelSelection(WithRouletteSeed(42))
			if err != nil {
				b.Fatal(err)
			}

			selectN := size / 2
			if selectN < 1 {
				selectN = 1
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = sel.Select(ctx, population, selectN)
			}
		})
	}
}

// BenchmarkSortByScore benchmarks the SortByScore utility function.
// Tests various population sizes with mix of evaluated (score >= 0) and unevaluated (score == -1) strategies.
func BenchmarkSortByScore(b *testing.B) {
	b.ReportAllocs()

	for _, size := range []int{10, 100, 500, 1000} {
		b.Run(fmt.Sprintf("pop_%d", size), func(b *testing.B) {
			// Pre-generate data outside timer.
			rng := rand.New(rand.NewSource(int64(size)))

			b.StopTimer()
			strategies := make([]*mutation.Strategy, size)
			for i := range strategies {
				s := randStrategy(rng, 5, 100)
				// 20% of strategies are unevaluated.
				if rng.Float64() < 0.2 {
					s.Score = -1
				}
				strategies[i] = s
			}
			b.StartTimer()

			for i := 0; i < b.N; i++ {
				// Clone before each sort since SortByScore mutates in-place.
				strategiesCopy := make([]*mutation.Strategy, len(strategies))
				copy(strategiesCopy, strategies)
				SortByScore(strategiesCopy)
			}
		})
	}
}

// ==========================================================================
// Group 3: Evolution Cycle Benchmarks
// ==========================================================================

// BenchmarkEvolve_OneGeneration benchmarks a single Evolve() call measuring the full cycle:
// sort → select survivors → preserve elites → crossover → mutate → assemble next generation.
func BenchmarkEvolve_OneGeneration(b *testing.B) {
	b.ReportAllocs()

	mutator := &benchMutator{rng: rand.New(rand.NewSource(42))}
	crosser, err := NewCrossover(WithSeed(42))
	if err != nil {
		b.Fatal(err)
	}

	ctx := context.Background()

	for _, size := range []int{10, 20, 50, 100} {
		b.Run(fmt.Sprintf("pop_%d", size), func(b *testing.B) {
			b.StopTimer()
			pop := benchPopulation(b, size)
			b.StartTimer()

			for i := 0; i < b.N; i++ {
				_ = pop.Evolve(ctx, mutator, crosser)
			}
		})
	}
}

// BenchmarkEvolveOnIdle_OneGeneration benchmarks EvolveOnIdle() which uses simplified logic:
// fixed 60% survival rate, top-30% breeding pool, single elite preservation.
// Should be faster than Evolve() due to simpler selection strategy.
func BenchmarkEvolveOnIdle_OneGeneration(b *testing.B) {
	b.ReportAllocs()

	mutator := &benchMutator{rng: rand.New(rand.NewSource(42))}
	crosser, err := NewCrossover(WithSeed(42))
	if err != nil {
		b.Fatal(err)
	}

	ctx := context.Background()

	for _, size := range []int{10, 20, 50, 100} {
		b.Run(fmt.Sprintf("pop_%d", size), func(b *testing.B) {
			b.StopTimer()
			pop := benchPopulation(b, size)
			b.StartTimer()

			for i := 0; i < b.N; i++ {
				_ = pop.EvolveOnIdle(ctx, mutator, crosser)
			}
		})
	}
}

// BenchmarkEvolve_MultipleGenerations benchmarks N consecutive generations of Evolve().
// Reports total time divided across all generations to show amortized per-gen cost.
func BenchmarkEvolve_MultipleGenerations(b *testing.B) {
	b.ReportAllocs()

	mutator := &benchMutator{rng: rand.New(rand.NewSource(42))}
	crosser, err := NewCrossover(WithSeed(42))
	if err != nil {
		b.Fatal(err)
	}

	ctx := context.Background()

	for _, gens := range []int{10, 50, 100} {
		b.Run(fmt.Sprintf("%d_generations", gens), func(b *testing.B) {
			b.StopTimer()
			pop := benchPopulation(b, 20)
			b.StartTimer()

			for i := 0; i < b.N; i++ {
				for g := 0; g < gens; g++ {
					_ = pop.Evolve(ctx, mutator, crosser)
				}
			}
			b.ReportMetric(float64(gens), "generations")
		})
	}
}

// BenchmarkEvolve_Scaling benchmarks how evolve time scales with population size.
// Parametric sub-benchmarks from 5 to 500 agents. Ideal scaling is O(n log n) or better
// dominated by sorting cost in survivor selection.
func BenchmarkEvolve_Scaling(b *testing.B) {
	b.ReportAllocs()

	mutator := &benchMutator{rng: rand.New(rand.NewSource(42))}
	crosser, err := NewCrossover(WithSeed(42))
	if err != nil {
		b.Fatal(err)
	}

	ctx := context.Background()

	for _, size := range []int{5, 10, 20, 50, 100, 200, 500} {
		b.Run(fmt.Sprintf("pop_%d", size), func(b *testing.B) {
			b.StopTimer()
			pop := benchPopulation(b, size)
			b.StartTimer()

			for i := 0; i < b.N; i++ {
				_ = pop.Evolve(ctx, mutator, crosser)
			}
		})
	}
}

// ==========================================================================
// Group 4: Memory Allocation Benchmarks
// ==========================================================================

// BenchmarkPopulationCreation benchmarks NewPopulation initialization cost.
// Includes variant generation via mutator to fill target population size.
func BenchmarkPopulationCreation(b *testing.B) {
	b.ReportAllocs()

	base := randStrategy(rand.New(rand.NewSource(42)), 5, 100)
	base.Score = 50.0
	mutator := &benchMutator{rng: rand.New(rand.NewSource(42))}
	ctx := context.Background()

	for _, size := range []int{10, 20, 50, 100} {
		b.Run(fmt.Sprintf("size_%d", size), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, _ = NewPopulation(ctx, base, mutator,
					WithPopulationSize(size),
					WithSurvivalRate(0.6),
					WithMutationRate(0.2),
					WithEliteCount(1),
				)
			}
		})
	}
}

// BenchmarkBest benchmarks Best() method under read lock on large populations.
// Repeated calls measure lock acquisition + linear scan cost.
func BenchmarkBest(b *testing.B) {
	b.ReportAllocs()

	for _, size := range []int{100, 500, 1000} {
		b.Run(fmt.Sprintf("pop_%d", size), func(b *testing.B) {
			b.StopTimer()
			pop := benchPopulation(b, size)
			b.StartTimer()

			for i := 0; i < b.N; i++ {
				_ = pop.Best()
			}
		})
	}
}

// BenchmarkStats benchmarks Stats() computation on large populations.
// Measures full scan for min/max/avg score calculation under read lock.
func BenchmarkStats(b *testing.B) {
	b.ReportAllocs()

	for _, size := range []int{100, 500, 1000} {
		b.Run(fmt.Sprintf("pop_%d", size), func(b *testing.B) {
			b.StopTimer()
			pop := benchPopulation(b, size)
			b.StartTimer()

			for i := 0; i < b.N; i++ {
				_ = pop.Stats()
			}
		})
	}
}

// BenchmarkCloneStrategy benchmarks deep clone operation on strategies
// with varying parameter counts. Clone copies Params map and nested values.
func BenchmarkCloneStrategy(b *testing.B) {
	b.ReportAllocs()

	for _, nParams := range []int{5, 20, 50, 100} {
		b.Run(fmt.Sprintf("params_%d", nParams), func(b *testing.B) {
			strategy := randStrategy(rand.New(rand.NewSource(42)), nParams, 200)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = strategy.Clone()
			}
		})
	}
}

// ==========================================================================
// Group 5: Real-World Simulation Benchmark
// ==========================================================================

// BenchmarkRealWorldEvolution simulates a realistic evolution workload matching
// production usage patterns:
//   - Population of 20 agents with 5 param keys each
//   - Prompt templates of ~500 characters
//   - Runs 100 consecutive generations of EvolveOnIdle
//
// Reports total time, time per generation, and memory allocations.
func BenchmarkRealWorldEvolution(b *testing.B) {
	b.ReportAllocs()

	const (
		popSize      = 20
		nParams      = 5
		tmplLen      = 500
		nGenerations = 100
	)

	mutator := &benchMutator{rng: rand.New(rand.NewSource(42))}
	crosser, err := NewCrossover(WithSeed(42))
	if err != nil {
		b.Fatal(err)
	}

	ctx := context.Background()

	b.StopTimer()
	base := randStrategy(rand.New(rand.NewSource(42)), nParams, tmplLen)
	base.Score = 50.0

	pop, err := NewPopulation(ctx, base, mutator,
		WithPopulationSize(popSize),
		WithSurvivalRate(0.6),
		WithMutationRate(0.2),
		WithEliteCount(1),
	)
	if err != nil {
		b.Fatal(err)
	}

	// Assign initial scores simulating task evaluation results.
	scoreRng := rand.New(rand.NewSource(99))
	for _, agent := range pop.Agents {
		agent.Score = scoreRng.Float64() * 100
	}
	b.StartTimer()

	for i := 0; i < b.N; i++ {
		for g := 0; g < nGenerations; g++ {
			_ = pop.EvolveOnIdle(ctx, mutator, crosser)

			// Simulate evaluation: assign new scores after each generation.
			for _, agent := range pop.Agents {
				agent.Score = scoreRng.Float64() * 100
			}
		}
		b.ReportMetric(float64(nGenerations), "generations")
	}
}

// ==========================================================================
// Group 6: Fitness Sharing Benchmarks
// ==========================================================================

// createBenchmarkPopulation creates a population of given size with random
// strategies for fitness sharing benchmarking. All agents receive evaluated
// scores so they participate in fitness sharing.
func createBenchmarkPopulation(size int) *Population {
	base := randStrategy(rand.New(rand.NewSource(42)), 5, 100)
	base.Score = 50.0

	mutator := &benchMutator{rng: rand.New(rand.NewSource(42))}

	pop, err := NewPopulation(context.Background(), base, mutator,
		WithPopulationSize(size),
		WithSurvivalRate(0.6),
		WithMutationRate(0.2),
		WithEliteCount(1),
	)
	if err != nil {
		panic(fmt.Sprintf("createBenchmarkPopulation: %v", err))
	}

	rng := rand.New(rand.NewSource(99))
	for _, a := range pop.Agents {
		a.Score = rng.Float64() * 100
	}

	return pop
}

// BenchmarkApplyFitnessSharing benchmarks the fitness sharing penalty computation
// across different population sizes. Tests both exact mode (small populations,
// distance matrix caching) and sampled mode (large populations, randomized
// neighbor sampling at FitnessSharingSampleLimit threshold).
//
// Sub-benchmarks:
//   - pop_10 to pop_50: exact O(n²) mode with distance matrix cache
//   - pop_100 to pop_500: sampled O(n×k) mode with FitnessSharingSampleSize neighbors
func BenchmarkApplyFitnessSharing(b *testing.B) {
	b.ReportAllocs()

	for _, size := range []int{10, 50, 100, 200, 500} {
		b.Run(fmt.Sprintf("pop_%d", size), func(b *testing.B) {
			pop := createBenchmarkPopulation(size)
			eliteCount := size / 5

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Clone population before each iteration since applyFitnessSharing
				// modifies agent scores in-place.
				for _, a := range pop.Agents {
					a.Score = rand.New(rand.NewSource(int64(i)+int64(size))).Float64() * 100
				}
				pop.applyFitnessSharing(eliteCount)
			}
		})
	}
}

// BenchmarkApplyFitnessSharing_CustomSampling benchmarks fitness sharing with
// non-default sampling configuration. Verifies that custom limit/size values
// are correctly applied and perform as expected.
func BenchmarkApplyFitnessSharing_CustomSampling(b *testing.B) {
	b.ReportAllocs()

	base := randStrategy(rand.New(rand.NewSource(42)), 5, 100)
	base.Score = 50.0

	mutator := &benchMutator{rng: rand.New(rand.NewSource(42))}

	for _, cfg := range []struct {
		name  string
		limit int
		size  int
		pop   int
	}{
		{"limit20_size10", 20, 10, 100},
		{"limit100_size20", 100, 20, 200},
		{"limit0_exact", 0, 0, 50}, // limit=0 disables sampling entirely
	} {
		b.Run(cfg.name, func(b *testing.B) {
			b.StopTimer()
			pop, err := NewPopulation(context.Background(), base, mutator,
				WithPopulationSize(cfg.pop),
				WithSurvivalRate(0.6),
				WithMutationRate(0.2),
				WithEliteCount(1),
				WithFitnessSharingSampling(cfg.limit, cfg.size),
			)
			if err != nil {
				b.Fatal(err)
			}
			scoreRng := rand.New(rand.NewSource(99))
			for _, a := range pop.Agents {
				a.Score = scoreRng.Float64() * 100
			}
			b.StartTimer()

			eliteCount := cfg.pop / 5
			for i := 0; i < b.N; i++ {
				for _, a := range pop.Agents {
					a.Score = rand.New(rand.NewSource(int64(i))).Float64() * 100
				}
				pop.applyFitnessSharing(eliteCount)
			}
		})
	}
}
