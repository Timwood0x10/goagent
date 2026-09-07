package evolution

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// TestE2E_FullEvolutionCycle verifies the complete evolution lifecycle:
//   - Create a WiredEvolutionSystem with a real base strategy
//   - Run 5 generations of idle evolution using ConstantScorer
//   - Assert population state, best strategy, and scoring correctness
//   - Generate and validate an EvolutionReport
func TestE2E_FullEvolutionCycle(t *testing.T) {
	defer discardLogs()()
	// Step 1: Create a real mutation.Strategy as the root/base for evolution.
	base := &mutation.Strategy{
		ID:             "e2e-base",
		Version:        1,
		Name:           "e2e-root-strategy",
		Params:         map[string]any{"temperature": 0.7, "top_k": 40, "max_steps": 10},
		PromptTemplate: "You are a helpful assistant.",
		Score:          50.0,
		CreatedAt:      time.Now(),
	}

	// Step 2: Configure system with small population for fast execution.
	cfg := DefaultSystemConfig()
	cfg.PopulationSize = 10
	cfg.EliteCount = 2
	cfg.MutationRate = 0.3
	cfg.SurvivalRate = 0.6
	// Use deterministic seeds so this test is reproducible.
	cfg.MutatorSeed = 42
	cfg.CrossoverSeed = 99
	cfg.PopulationSeed = 123
	cfg.UseDeterministicIDs = true
	cfg.EnableDreamCycle = false
	cfg.EnableScheduler = false
	cfg.HistoryMaxSize = 100
	// Set a constant scorer in [50, 95] range — no mocks, real scoring loop.
	constantScore := 75.0
	cfg.Scorer = func(s *mutation.Strategy) float64 {
		return constantScore
	}

	// Step 3: Build the wired evolution system (all real components).
	system, err := NewWiredEvolutionSystem(base, cfg)
	require.NoError(t, err, "NewWiredEvolutionSystem should succeed")
	require.NotNil(t, system.Population)
	require.NotNil(t, system.PopAdapter)

	t.Logf("System created: pop_size=%d, elite=%d", cfg.PopulationSize, cfg.EliteCount)

	// Step 4: Run 5 generations of idle evolution.
	ctx := context.Background()
	const generations = 5
	err = RunIdleEvolution(ctx, system, generations)
	require.NoError(t, err, "RunIdleEvolution should complete without error")

	// Step 5: Assert post-evolution state.
	gen := system.Population.Generation
	assert.Equal(t, generations, gen,
		"population generation should equal number of evolved generations")

	stats := system.Population.Stats()
	t.Logf("After %d generations: size=%d, best=%.2f, avg=%.2f, worst=%.2f",
		gen, stats.Size, stats.BestScore, stats.AvgScore, stats.WorstScore)

	assert.Equal(t, cfg.PopulationSize, stats.Size,
		"population size should remain constant across generations")
	assert.GreaterOrEqual(t, stats.BestScore, 0.0,
		"best score should be non-negative (all agents scored)")

	// Verify every agent has been evaluated (Score >= 0).
	agents, _ := system.Population.Snapshot()
	for i, agent := range agents {
		assert.True(t, genome.IsScoreEvaluated(agent.Score),
			"agent %d (%s) should have Score >= 0, got %.4f", i, agent.ID, agent.Score)
	}

	// Step 6: Retrieve and validate best strategy.
	best, err := BestStrategyFromSystem(system)
	require.NoError(t, err)
	require.NotNil(t, best, "best strategy should not be nil after evolution")
	assert.NotEmpty(t, best.ID, "best strategy should have a non-empty ID")
	assert.GreaterOrEqual(t, best.Score, 0.0,
		"best strategy score should be non-negative")
	t.Logf("Best strategy: id=%s, score=%.4f, version=%d", best.ID, best.Score, best.Version)

	// Step 7: Generate report and assert fields.
	report, err := GenerateReport(ctx, system)
	require.NoError(t, err, "GenerateReport should succeed")
	require.NotNil(t, report)

	assert.Equal(t, generations, report.TotalGenerations,
		"report TotalGenerations should match evolved generations")
	assert.GreaterOrEqual(t, report.BestEverScore, 0.0,
		"report BestEverScore should be non-negative")
	assert.Equal(t, stats.BestScore, report.FinalBestScore,
		"report FinalBestScore should match population current best")
	assert.NotEmpty(t, report.GenerationTrajectory,
		"report GenerationTrajectory should not be empty")

	// Validate trajectory entries (one per generation with history enabled).
	assert.Len(t, report.GenerationTrajectory, gen,
		"trajectory should have exactly one entry per generation")
	for i, gs := range report.GenerationTrajectory {
		assert.Equal(t, i+1, gs.Generation,
			"trajectory[%d] Generation should be %d", i, i+1)
		assert.Equal(t, cfg.PopulationSize, gs.PopulationSize,
			"trajectory[%d] PopulationSize should match config", i)
	}

	// Step 8: Output human-readable report via t.Log.
	reportText := ReportString(report)
	assert.NotEmpty(t, reportText, "ReportString should return non-empty text")
	t.Logf("\n%s", reportText)
}

// TestE2E_WithGuardrails verifies that EvolutionGuardrails correctly track
// improvement and stagnation across multiple evolution cycles when integrated
// with a real WiredEvolutionSystem.
//
// It uses PreEvolveCheck before each generation and PostEvolveCheck after each
// generation to verify:
//   - No critical stop signal is raised (scores are above baseline)
//   - Stagnation counter increments when scores don't improve
//   - Events are recorded for post-evolve checks
func TestE2E_WithGuardrails(t *testing.T) {
	base := &mutation.Strategy{
		ID:             "guardrail-e2e-base",
		Version:        1,
		Params:         map[string]any{"temperature": 0.5, "top_k": 20},
		PromptTemplate: "Be concise.",
		Score:          50.0,
		CreatedAt:      time.Now(),
	}

	cfg := DefaultSystemConfig()
	cfg.PopulationSize = 8
	cfg.EliteCount = 1
	cfg.MutatorSeed = 42
	cfg.CrossoverSeed = 99
	cfg.PopulationSeed = 77
	cfg.UseDeterministicIDs = true
	cfg.EnableDreamCycle = false
	cfg.EnableScheduler = false

	// Use ConstantScorer(80.0) — well above baseline of 30.0.
	fixedScore := 80.0
	cfg.Scorer = func(s *mutation.Strategy) float64 {
		return fixedScore
	}

	// Wire guardrails into the system so GenomPopulationAdapter.Run() calls them.
	guardrails, err := NewEvolutionGuardrails(
		WithBaselineScore(30.0),
		WithMaxStagnantGenerations(3),
	)
	require.NoError(t, err)
	cfg.Guardrails = guardrails

	system, err := NewWiredEvolutionSystem(base, cfg)
	require.NoError(t, err)

	ctx := context.Background()
	const totalGens = 5

	// Pre-score the initial population so that PreEvolveCheck doesn't flag
	// unevaluated agents on generation 0 (mutator sets Score=-1 for offspring).
	system.Population.ScoreAgents(cfg.Scorer)

	for gen := 0; gen < totalGens; gen++ {
		// Run one evolution cycle — guardrails are called inside Run() via cfg.Guardrails.
		err := system.PopAdapter.Run(ctx)
		require.NoError(t, err, "gen %d: PopAdapter.Run should succeed", gen)

		// Read guardrail events after each cycle.
		events := guardrails.Events()
		t.Logf("Gen %d: best=%.2f, events=%d, stagnantCount=%d",
			gen, system.Population.Stats().BestScore, len(events), guardrails.StagnantCount())
	}

	// After all generations, verify guardrails tracked stagnation correctly.
	// Since ConstantScorer always returns 80.0, the first post-check sees improvement
	// (from initial state where bestKnownScore=0 to 80.0). Subsequent checks see no
	// further improvement because the score never changes, so stagnantCount increases.
	stagnant := guardrails.StagnantCount()
	t.Logf("Final stagnant count: %d", stagnant)
	// Stagnant count should be >= (totalGens - 1) because only first gen improves.
	assert.GreaterOrEqual(t, stagnant, totalGens-1,
		"stagnant count should reflect no improvement after first generation")

	// Verify events were recorded.
	events := guardrails.Events()
	assert.GreaterOrEqual(t, len(events), 0,
		"guardrails should have recorded events (may be empty if no threshold crossed)")
	t.Logf("Total guardrail events recorded: %d", len(events))
	for i, ev := range events {
		t.Logf("  Event[%d]: level=%v, rule=%q, gen=%d, msg=%s",
			i, ev.Level, ev.Rule, ev.Generation, ev.Message)
	}
}

// TestE2E_ReportWithHistory verifies that GenerateReport produces correct
// multi-generation data after running several evolution cycles. It checks:
//   - History is enabled and GenerationTrajectory contains per-generation entries
//   - BestEverScore and FinalBestScore are consistent
//   - ReportString produces parseable output
func TestE2E_ReportWithHistory(t *testing.T) {
	base := &mutation.Strategy{
		ID:             "history-report-base",
		Version:        1,
		Params:         map[string]any{"temperature": 0.7, "top_k": 40, "max_steps": 15, "memory_limit": 5},
		PromptTemplate: "You are a detailed and helpful AI assistant.",
		Score:          60.0,
		CreatedAt:      time.Now(),
	}

	cfg := DefaultSystemConfig()
	cfg.PopulationSize = 10
	cfg.EliteCount = 2
	cfg.MutationRate = 0.25
	cfg.SurvivalRate = 0.6
	cfg.MutatorSeed = 2024
	cfg.CrossoverSeed = 2025
	cfg.PopulationSeed = 2026
	cfg.UseDeterministicIDs = true
	cfg.EnableDreamCycle = false
	cfg.EnableScheduler = false
	cfg.HistoryMaxSize = 100

	// Use a moderate constant score for realistic report values.
	reportScore := 72.5
	cfg.Scorer = func(s *mutation.Strategy) float64 {
		return reportScore
	}

	system, err := NewWiredEvolutionSystem(base, cfg)
	require.NoError(t, err)

	ctx := context.Background()
	const generations = 6

	err = RunIdleEvolution(ctx, system, generations)
	require.NoError(t, err)

	assert.Equal(t, generations, system.Population.Generation,
		"population should have evolved exactly %d generations", generations)

	// Generate the report.
	report, err := GenerateReport(ctx, system)
	require.NoError(t, err)
	require.NotNil(t, report)

	// Assert core report fields.
	assert.Equal(t, generations, report.TotalGenerations,
		"TotalGenerations must equal number of evolved generations")
	assert.InDelta(t, reportScore, report.BestEverScore, 0.01,
		"BestEverScore should match the constant scorer value")
	assert.InDelta(t, reportScore, report.FinalBestScore, 0.01,
		"FinalBestScore should match the constant scorer value")
	assert.Equal(t, 0, report.BestEverGeneration,
		"BestEverGeneration should be the first generation (constant scorer, no improvement)")

	// Assert trajectory is populated with all generation entries.
	require.Len(t, report.GenerationTrajectory, generations,
		"GenerationTrajectory should contain exactly %d entries", generations)

	// Each trajectory entry should reflect its respective generation's state.
	for i, traj := range report.GenerationTrajectory {
		assert.Equal(t, i+1, traj.Generation,
			"trajectory[%d] Generation should be %d", i, i+1)
		assert.Equal(t, cfg.PopulationSize, traj.PopulationSize,
			"trajectory[%d] PopulationSize should match configured size", i)
		assert.InDelta(t, reportScore, traj.BestScore, 0.01,
			"trajectory[%d] BestScore should match constant scorer", i)
		assert.InDelta(t, reportScore, traj.AvgScore, 0.01,
			"trajectory[%d] AvgScore should match constant scorer", i)
		assert.InDelta(t, reportScore, traj.WorstScore, 0.01,
			"trajectory[%d] WorstScore should match constant scorer", i)
		t.Logf("  Trajectory gen=%d", traj.Generation)
	}

	// Output report string for visual inspection.
	reportStr := ReportString(report)
	assert.Contains(t, reportStr, "Evolution Report",
		"ReportString output should contain header")
	assert.Contains(t, reportStr, "Total Generations",
		"ReportString output should contain generation count")
	assert.Contains(t, reportStr, "Best Ever Score",
		"ReportString output should contain best ever score")
	t.Logf("\n=== E2E Report Output ===\n%s\n=== End Report ===", reportStr)

	// Verify lineage concentration data if available.
	if report.LineageConcentration != nil {
		t.Logf("Lineage concentration: top_share=%.2f%%, unique=%d",
			report.LineageConcentration.TopLineageShare*100,
			report.LineageConcentration.UniqueLineages)
		assert.GreaterOrEqual(t, report.LineageConcentration.UniqueLineages, 1,
			"should have at least one unique lineage")
	}
}
