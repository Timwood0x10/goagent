package evolution

import (
	"context"
	"testing"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

func TestGenerateReport(t *testing.T) {
	ctx := context.Background()

	t.Run("nil system returns error", func(t *testing.T) {
		_, err := GenerateReport(ctx, nil)
		if err == nil {
			t.Fatal("expected error for nil system")
		}
	})

	t.Run("generates report from population data", func(t *testing.T) {
		pop := &genome.Population{
			Agents: []*mutation.Strategy{
				{ID: "a", Score: 100, PromptTemplate: "t1", ParentID: "p1"},
				{ID: "b", Score: 80, PromptTemplate: "t2", ParentID: "p2"},
				{ID: "c", Score: 60, PromptTemplate: "t1", ParentID: "p1"},
			},
			Generation: 5,
		}

		sys := &WiredEvolutionSystem{
			Population: pop,
		}

		report, err := GenerateReport(ctx, sys)
		if err != nil {
			t.Fatalf("GenerateReport: %v", err)
		}
		if report == nil {
			t.Fatal("report should not be nil")
		}

		if report.TotalGenerations != 5 {
			t.Errorf("TotalGenerations = %d, want 5", report.TotalGenerations)
		}
		if report.FinalBestScore != 100 {
			t.Errorf("FinalBestScore = %.1f, want 100", report.FinalBestScore)
		}
		if report.BestEverScore != 100 {
			t.Errorf("BestEverScore = %.1f, want 100", report.BestEverScore)
		}
		if len(report.GenerationTrajectory) != 1 {
			t.Errorf("GenerationTrajectory len = %d, want 1", len(report.GenerationTrajectory))
		}
	})

	t.Run("nil population does not panic", func(t *testing.T) {
		sys := &WiredEvolutionSystem{Population: nil}
		report, err := GenerateReport(ctx, sys)
		if err != nil {
			t.Fatalf("GenerateReport: %v", err)
		}
		if report.TotalGenerations != 0 {
			t.Errorf("expected 0 generations for nil pop, got %d", report.TotalGenerations)
		}
	})

	t.Run("scorer cost summary with WithScoringStats", func(t *testing.T) {
		pop := &genome.Population{
			Agents: []*mutation.Strategy{
				{Score: 100},
			},
		}
		sys := &WiredEvolutionSystem{Population: pop}

		report, err := GenerateReport(ctx, sys,
			WithScoringStats(map[string]int64{"llm_calls": 42}, 10, 50, 5, 2),
		)
		if err != nil {
			t.Fatalf("GenerateReport: %v", err)
		}

		if report.ScorerCostSummary == nil {
			t.Fatal("expected ScorerCostSummary")
		}
		if report.ScorerCostSummary.TotalLLMCalls != 42 {
			t.Errorf("TotalLLMCalls = %d, want 42", report.ScorerCostSummary.TotalLLMCalls)
		}
		if report.ScorerCostSummary.LLMBudgetUsed != 10 {
			t.Errorf("LLMBudgetUsed = %d, want 10", report.ScorerCostSummary.LLMBudgetUsed)
		}
		if report.ScorerCostSummary.LLMBudgetMax != 50 {
			t.Errorf("LLMBudgetMax = %d, want 50", report.ScorerCostSummary.LLMBudgetMax)
		}
		if report.ScorerCostSummary.TotalCacheHits != 5 {
			t.Errorf("TotalCacheHits = %d, want 5", report.ScorerCostSummary.TotalCacheHits)
		}
		if report.ScorerCostSummary.TotalFallbacks != 2 {
			t.Errorf("TotalFallbacks = %d, want 2", report.ScorerCostSummary.TotalFallbacks)
		}
	})

	t.Run("lineage concentration reported correctly", func(t *testing.T) {
		pop := &genome.Population{
			Agents: []*mutation.Strategy{
				{ID: "a1", Score: 100, ParentID: "A"},
				{ID: "a2", Score: 80, ParentID: "A"},
				{ID: "b1", Score: 60, ParentID: "B"},
			},
		}
		sys := &WiredEvolutionSystem{Population: pop}
		report, err := GenerateReport(ctx, sys)
		if err != nil {
			t.Fatalf("GenerateReport: %v", err)
		}

		if report.LineageConcentration == nil {
			t.Fatal("expected LineageConcentration")
		}
		if report.LineageConcentration.TopLineageShare != 2.0/3.0 {
			t.Errorf("TopLineageShare = %.2f, want ~0.67", report.LineageConcentration.TopLineageShare)
		}
		if report.LineageConcentration.UniqueLineages != 2 {
			t.Errorf("UniqueLineages = %d, want 2", report.LineageConcentration.UniqueLineages)
		}
		if report.LineageConcentration.LineageCounts["A"] != 2 {
			t.Errorf("LineageCounts[A] = %d, want 2", report.LineageConcentration.LineageCounts["A"])
		}
	})
}

func TestReportString(t *testing.T) {
	t.Run("nil returns placeholder", func(t *testing.T) {
		s := ReportString(nil)
		if s != "(nil report)" {
			t.Errorf("got %q, want %q", s, "(nil report)")
		}
	})

	t.Run("formats basic report", func(t *testing.T) {
		r := &EvolutionReport{
			TotalGenerations:   3,
			BestEverScore:      95.5,
			BestEverGeneration: 2,
			FinalBestScore:     90.0,
			GenerationTrajectory: []GenerationStats{
				{Generation: 1, PopulationSize: 10, BestScore: 80, AvgScore: 60, WorstScore: 40, Diversity: 0.7},
			},
		}
		s := ReportString(r)
		if s == "" {
			t.Error("expected non-empty string")
		}
	})

	t.Run("includes scorer cost when present", func(t *testing.T) {
		r := &EvolutionReport{
			GenerationTrajectory: []GenerationStats{{Generation: 1}},
			ScorerCostSummary: &ScorerCostSummary{
				TotalLLMCalls:  10,
				TotalCacheHits: 3,
				TotalFallbacks: 1,
				LLMBudgetUsed:  10,
				LLMBudgetMax:   100,
			},
		}
		s := ReportString(r)
		if s == "" {
			t.Error("expected non-empty string")
		}
	})
}

func TestWithScoringStats(t *testing.T) {
	t.Run("sets config correctly", func(t *testing.T) {
		cfg := &reportConfig{}
		opt := WithScoringStats(map[string]int64{"llm_calls": 5}, 10, 20, 2, 1)
		opt(cfg)
		if cfg.scoringStats["llm_calls"] != 5 {
			t.Errorf("llm_calls = %d, want 5", cfg.scoringStats["llm_calls"])
		}
		if len(cfg.budgetUsage) != 4 {
			t.Fatalf("expected 4 budget usage values, got %d", len(cfg.budgetUsage))
		}
		if cfg.budgetUsage[0] != 10 || cfg.budgetUsage[1] != 20 {
			t.Errorf("budget = %v, want [10 20 2 1]", cfg.budgetUsage)
		}
	})

	t.Run("partial budget usage accepted", func(t *testing.T) {
		cfg := &reportConfig{}
		opt := WithScoringStats(map[string]int64{}, 5, 100)
		opt(cfg)
		if len(cfg.budgetUsage) != 0 {
			t.Errorf("expected no budget usage for <4 values, got %d", len(cfg.budgetUsage))
		}
	})
}
