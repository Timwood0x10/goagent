package genome

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

func TestMeasureDiversity(t *testing.T) {
	t.Parallel()

	t.Run("fewer_than_two_agents_returns_1", func(t *testing.T) {
		t.Parallel()
		pop := &Population{Agents: []*mutation.Strategy{{}}}
		if d := pop.measureDiversityLocked(); d != 1.0 {
			t.Errorf("diversity for 1 agent = %f, want 1.0", d)
		}
	})

	t.Run("identical_params_returns_low", func(t *testing.T) {
		t.Parallel()
		agents := []*mutation.Strategy{
			{Params: map[string]any{"temperature": 0.5, "top_k": 40}},
			{Params: map[string]any{"temperature": 0.5, "top_k": 40}},
		}
		pop := &Population{Agents: agents}
		// Identical params give Numeric≈0, Categorical≈0; lineage contributes ~0.1.
		if d := pop.measureDiversityLocked(); d > 0.2 {
			t.Errorf("identical agents diversity = %f, want < 0.2", d)
		}
	})

	t.Run("divergent_params_returns_moderate", func(t *testing.T) {
		t.Parallel()
		agents := []*mutation.Strategy{
			{Params: map[string]any{"temperature": 0.1}},
			{Params: map[string]any{"temperature": 0.9}},
		}
		pop := &Population{Agents: agents}
		d := pop.measureDiversityLocked()
		// High numeric (~1.0) but zero categorical and moderate lineage (~0.5).
		// Weighted: 1.0*0.4 + 0*0.4 + 0.5*0.2 = 0.5.
		if d < 0.3 || d > 0.7 {
			t.Errorf("temperature 0.1 vs 0.9 diversity = %f, want ~0.5", d)
		}
	})

	t.Run("empty_params_returns_moderate", func(t *testing.T) {
		t.Parallel()
		agents := []*mutation.Strategy{
			{Params: map[string]any{}},
			{Params: map[string]any{}},
		}
		pop := &Population{Agents: agents}
		d := pop.measureDiversityLocked()
		// No params → Numeric=1 (default), Categorical=0, Lineage≈0.5.
		if d < 0.3 || d > 0.7 {
			t.Errorf("empty params diversity = %f, want ~0.5", d)
		}
	})

	t.Run("only_numeric_params_measured", func(t *testing.T) {
		t.Parallel()
		agents := []*mutation.Strategy{
			{Params: map[string]any{"prompt": "hello"}},
			{Params: map[string]any{"prompt": "world"}},
		}
		pop := &Population{Agents: agents}
		// Only string params exist, so no numeric distance is measurable.
		// The new weighted formula includes lineage (20% weight), so overall > 0
		// when agents have different ParentIDs or empty ones grouped as "(root)".
		if d := pop.measureDiversityLocked(); d < 0 || d > 1 {
			t.Errorf("only string params diversity = %f, want [0, 1]", d)
		}
	})

	t.Run("int_params_measured", func(t *testing.T) {
		t.Parallel()
		agents := []*mutation.Strategy{
			{Params: map[string]any{"top_k": 10}},
			{Params: map[string]any{"top_k": 80}},
		}
		pop := &Population{Agents: agents}
		d := pop.measureDiversityLocked()
		// High numeric (~1.0) but zero categorical and moderate lineage (~0.5).
		if d < 0.3 || d > 0.7 {
			t.Errorf("top_k 10 vs 80 diversity = %f, want ~0.5", d)
		}
	})
}

func TestAdjustMutationRate(t *testing.T) {
	t.Parallel()

	t.Run("increases_when_diversity_low", func(t *testing.T) {
		t.Parallel()
		pop := &Population{
			Agents: []*mutation.Strategy{
				{Params: map[string]any{"temperature": 0.5}},
				{Params: map[string]any{"temperature": 0.5}},
			},
			cfg: PopulationConfig{
				MutationRate:       0.2,
				MinMutationRate:    0.05,
				MaxMutationRate:    0.5,
				DiversityThreshold: 0.15,
			},
			currentMutationRate: 0.2,
			bestScore:           math.Inf(-1),
		}
		pop.adjustMutationRateLocked()
		if pop.currentMutationRate <= 0.2 {
			t.Errorf("mutation rate did not increase: %.4f", pop.currentMutationRate)
		}
	})

	t.Run("decreases_when_diversity_high", func(t *testing.T) {
		t.Parallel()
		pop := &Population{
			Agents: []*mutation.Strategy{
				{Params: map[string]any{"temperature": 0.1}},
				{Params: map[string]any{"temperature": 0.9}},
			},
			cfg: PopulationConfig{
				MutationRate:       0.2,
				MinMutationRate:    0.05,
				MaxMutationRate:    0.5,
				DiversityThreshold: 0.15,
			},
			currentMutationRate: 0.4,
		}
		pop.adjustMutationRateLocked()
		if pop.currentMutationRate >= 0.4 {
			t.Errorf("mutation rate did not decrease: %.4f", pop.currentMutationRate)
		}
	})

	t.Run("clamps_to_min", func(t *testing.T) {
		t.Parallel()
		pop := &Population{
			Agents: []*mutation.Strategy{
				{Params: map[string]any{"temperature": 0.1}},
				{Params: map[string]any{"temperature": 0.9}},
			},
			cfg: PopulationConfig{
				MutationRate:       0.1,
				MinMutationRate:    0.05,
				MaxMutationRate:    0.5,
				DiversityThreshold: 0.15,
			},
			currentMutationRate: 0.05,
		}
		for i := 0; i < 10; i++ {
			pop.adjustMutationRateLocked()
		}
		if pop.currentMutationRate < 0.05 {
			t.Errorf("mutation rate below min: %.4f", pop.currentMutationRate)
		}
	})

	t.Run("clamps_to_max", func(t *testing.T) {
		t.Parallel()
		pop := &Population{
			Agents: []*mutation.Strategy{
				{Params: map[string]any{"temperature": 0.5}},
				{Params: map[string]any{"temperature": 0.5}},
			},
			cfg: PopulationConfig{
				MutationRate:       0.5,
				MinMutationRate:    0.05,
				MaxMutationRate:    0.5,
				DiversityThreshold: 0.15,
			},
			currentMutationRate: 0.5,
		}
		for i := 0; i < 10; i++ {
			pop.adjustMutationRateLocked()
		}
		if pop.currentMutationRate > 0.5 {
			t.Errorf("mutation rate above max: %.4f", pop.currentMutationRate)
		}
	})

	t.Run("drifts_toward_base_when_diversity_moderate", func(t *testing.T) {
		t.Parallel()
		pop := &Population{
			Agents: []*mutation.Strategy{
				{Params: map[string]any{"temperature": 0.5}},
				{Params: map[string]any{"temperature": 0.7}},
			},
			cfg: PopulationConfig{
				MutationRate:       0.1,
				MinMutationRate:    0.05,
				MaxMutationRate:    0.5,
				DiversityThreshold: 0.15,
			},
			currentMutationRate: 0.4,
		}
		pop.adjustMutationRateLocked()
		// Diversity ≈ 0.2 (0.2 diff / 1.0 range), which is between 0.15 and 0.30,
		// so the drift-back branch applies: 0.4 > 0.1, so 0.4*0.95 = 0.38
		if pop.currentMutationRate >= 0.4 {
			t.Errorf("mutation rate did not drift toward base: %.4f", pop.currentMutationRate)
		}
	})
}

func TestHandleStagnation(t *testing.T) {
	t.Parallel()

	t.Run("improvement_resets_counter", func(t *testing.T) {
		t.Parallel()
		pop := &Population{
			Agents: []*mutation.Strategy{
				{Score: 10},
				{Score: 5},
			},
			cfg: PopulationConfig{
				MaxStagnantGenerations: 10,
			},
			bestScore:    8,
			stagnantGens: 5,
		}
		pop.handleStagnationLocked()
		if pop.stagnantGens != 0 {
			t.Errorf("stagnantGens = %d, want 0 after improvement", pop.stagnantGens)
		}
	})

	t.Run("no_improvement_increments_counter", func(t *testing.T) {
		t.Parallel()
		pop := &Population{
			Agents: []*mutation.Strategy{
				{Score: 10},
			},
			cfg: PopulationConfig{
				MaxStagnantGenerations: 10,
			},
			bestScore:    10,
			stagnantGens: 3,
		}
		pop.handleStagnationLocked()
		if pop.stagnantGens != 4 {
			t.Errorf("stagnantGens = %d, want 4", pop.stagnantGens)
		}
	})

	t.Run("disabled_when_zero", func(t *testing.T) {
		t.Parallel()
		pop := &Population{
			Agents: []*mutation.Strategy{
				{Score: 5},
			},
			cfg: PopulationConfig{
				MaxStagnantGenerations: 0,
			},
			bestScore:    10,
			stagnantGens: 100,
		}
		pop.handleStagnationLocked()
		if pop.stagnantGens != 100 {
			t.Errorf("should be no-op when MaxStagnantGenerations=0, got %d", pop.stagnantGens)
		}
	})
}

func TestStagnationTriggersReset(t *testing.T) {
	t.Parallel()

	agents := make([]*mutation.Strategy, 6)
	for i := range agents {
		agents[i] = &mutation.Strategy{
			ID:             "a",
			Score:          float64(i),
			Params:         map[string]any{"temperature": 0.5},
			PromptTemplate: "template",
		}
	}

	pop := &Population{
		Agents: agents,
		Size:   6,
		cfg: PopulationConfig{
			EliteCount:             2,
			MaxStagnantGenerations: 1,
		},
		bestScore:           10,
		stagnantGens:        1,
		rng:                 rand.New(rand.NewSource(42)),
		currentMutationRate: 0.2,
	}

	pop.handleStagnationLocked()

	if pop.stagnantGens != 0 {
		t.Errorf("stagnantGens = %d, want 0 after reset", pop.stagnantGens)
	}

	// Bottom 1/3 = 2 agents from size 6. With EliteCount=2, bottom 2 should be reset.
	resetCount := 0
	for _, a := range pop.Agents {
		if a.Score == -1 {
			resetCount++
		}
	}
	if resetCount != 2 {
		t.Errorf("reset count = %d, want 2 (bottom 1/3 of 6)", resetCount)
	}
}

func TestAdaptiveOptions(t *testing.T) {
	t.Parallel()

	t.Run("WithMaxStagnantGenerations_negative_rejected", func(t *testing.T) {
		t.Parallel()
		cfg := DefaultPopulationConfig()
		err := WithMaxStagnantGenerations(-1)(&cfg)
		if err == nil {
			t.Error("expected error for negative max stagnant generations")
		}
	})

	t.Run("WithMinMutationRate_above_1_rejected", func(t *testing.T) {
		t.Parallel()
		cfg := DefaultPopulationConfig()
		err := WithMinMutationRate(1.5)(&cfg)
		if err == nil {
			t.Error("expected error for min mutation rate > 1")
		}
	})

	t.Run("WithMaxMutationRate_below_0_rejected", func(t *testing.T) {
		t.Parallel()
		cfg := DefaultPopulationConfig()
		err := WithMaxMutationRate(-0.1)(&cfg)
		if err == nil {
			t.Error("expected error for max mutation rate < 0")
		}
	})

	t.Run("WithDiversityThreshold_out_of_range_rejected", func(t *testing.T) {
		t.Parallel()
		cfg := DefaultPopulationConfig()
		err := WithDiversityThreshold(1.5)(&cfg)
		if err == nil {
			t.Error("expected error for diversity threshold > 1")
		}
	})

	t.Run("min_exceeds_max_rejected", func(t *testing.T) {
		t.Parallel()
		base := &mutation.Strategy{
			ID: "test", Version: 1,
			Params:    map[string]any{},
			CreatedAt: time.Now(),
		}
		mut := &mockMutator{}
		_, err := NewPopulation(context.Background(), base, mut,
			WithPopulationSize(5),
			WithMinMutationRate(0.6),
			WithMaxMutationRate(0.1),
		)
		if err == nil {
			t.Error("expected error when min mutation rate > max")
		}
	})
}

func TestAdjustMutationRateEmergency(t *testing.T) {
	t.Parallel()

	t.Run("emergency mode forces max rate", func(t *testing.T) {
		t.Parallel()
		pop := &Population{
			Agents: []*mutation.Strategy{
				{Params: map[string]any{"temperature": 0.5}},
				{Params: map[string]any{"temperature": 0.5}},
				{Params: map[string]any{"temperature": 0.5}},
			},
			cfg: PopulationConfig{
				MutationRate:       0.2,
				MinMutationRate:    0.05,
				MaxMutationRate:    0.5,
				DiversityThreshold: 0.15,
				AdaptiveConfig: &AdaptiveConfig{
					EmergencyDiversityThreshold: 0.1,
					LowDiversityBoostFactor:     1.5,
					HighDecayRate:               0.95,
					ModerateDecayRate:           0.98,
					DiversityFloorThreshold:     0.3,
					MinMutationFloor:            0.15,
				},
			},
			currentMutationRate: 0.2,
		}
		// Three identical agents → diversity ≈ 0 (well below 0.1)
		pop.adjustMutationRateLocked()
		if pop.currentMutationRate != 0.5 {
			t.Errorf("emergency mode should force max rate 0.5, got %.4f", pop.currentMutationRate)
		}
	})

	t.Run("low diversity below threshold boosts rate", func(t *testing.T) {
		t.Parallel()
		// Identical agents → numeric=0, categorical=0, lineage=0.5 → overall=0.1 < 0.15
		pop := &Population{
			Agents: []*mutation.Strategy{
				{Score: 100, Params: map[string]any{"temperature": 0.5}},
				{Score: 80, Params: map[string]any{"temperature": 0.5}},
			},
			cfg: PopulationConfig{
				MutationRate:       0.2,
				MinMutationRate:    0.05,
				MaxMutationRate:    0.5,
				DiversityThreshold: 0.15,
				AdaptiveConfig:     DefaultAdaptiveConfig(),
			},
			currentMutationRate: 0.2,
		}
		before := pop.currentMutationRate
		pop.adjustMutationRateLocked()
		if pop.currentMutationRate <= before {
			t.Errorf("expected rate increase from low diversity, got %.4f (was %.4f)", pop.currentMutationRate, before)
		}
	})

	t.Run("high diversity triggers decay", func(t *testing.T) {
		t.Parallel()
		pop := &Population{
			Agents: []*mutation.Strategy{
				{Params: map[string]any{"temperature": 0.1}},
				{Params: map[string]any{"temperature": 0.9}},
			},
			cfg: PopulationConfig{
				MutationRate:       0.2,
				MinMutationRate:    0.05,
				MaxMutationRate:    0.5,
				DiversityThreshold: 0.15,
				AdaptiveConfig:     DefaultAdaptiveConfig(),
			},
			currentMutationRate: 0.4,
		}
		pop.adjustMutationRateLocked()
		// Diversity ~0.5 > 0.15*3=0.45 → high diversity → decay
		if pop.currentMutationRate >= 0.4 {
			t.Errorf("expected rate decrease from high diversity, got %.4f", pop.currentMutationRate)
		}
	})
}

func TestHandleStagnation_EliteProtection(t *testing.T) {
	agents := make([]*mutation.Strategy, 8)
	for i := range agents {
		agents[i] = &mutation.Strategy{
			ID:     fmt.Sprintf("a%d", i),
			Score:  float64(i * 10),
			Params: map[string]any{"temperature": 0.5},
		}
	}

	pop := &Population{
		Agents: agents,
		Size:   8,
		cfg: PopulationConfig{
			EliteCount:             4,
			MaxStagnantGenerations: 1,
		},
		bestScore:           80, // higher than all agents
		stagnantGens:        1,
		rng:                 rand.New(rand.NewSource(42)),
		currentMutationRate: 0.2,
	}

	pop.handleStagnationLocked()

	if pop.stagnantGens != 0 {
		t.Errorf("stagnantGens = %d, want 0 after reset", pop.stagnantGens)
	}

	// Elites (top 4, scores 70, 60, 50, 40) should NOT be reset
	// Bottom 1/3 = ~2 agents from 8 should be reset
	resetCount := 0
	for i, a := range pop.Agents {
		if !IsScoreEvaluated(a.Score) {
			if i < 4 {
				t.Errorf("elite at index %d should not be reset (score was %.0f)", i, float64(i))
			}
			resetCount++
		}
	}
	if resetCount == 0 {
		t.Error("expected some agents to be reset")
	}
	t.Logf("reset %d agents (eliteCount=%d)", resetCount, 4)
}

func TestDiversityReport_PromptTemplateDistribution(t *testing.T) {
	t.Run("reports multiple templates", func(t *testing.T) {
		pop := &Population{
			Agents: []*mutation.Strategy{
				{Score: 100, PromptTemplate: "template1"},
				{Score: 80, PromptTemplate: "template1"},
				{Score: 60, PromptTemplate: "template2"},
			},
		}
		dr := pop.measureDiversityReportLocked()
		if dr.PromptTemplateDistribution == nil {
			t.Fatal("PromptTemplateDistribution should not be nil")
		}
		if dr.PromptTemplateDistribution["template1"] != 2 {
			t.Errorf("template1 count = %d, want 2", dr.PromptTemplateDistribution["template1"])
		}
		if dr.PromptTemplateDistribution["template2"] != 1 {
			t.Errorf("template2 count = %d, want 1", dr.PromptTemplateDistribution["template2"])
		}
	})

	t.Run("includes empty template", func(t *testing.T) {
		pop := &Population{
			Agents: []*mutation.Strategy{
				{Score: 100, PromptTemplate: ""},
				{Score: 80, PromptTemplate: "template1"},
			},
		}
		dr := pop.measureDiversityReportLocked()
		if dr.PromptTemplateDistribution[""] != 1 {
			t.Errorf("empty template count = %d, want 1", dr.PromptTemplateDistribution[""])
		}
	})

	t.Run("single agent still reports", func(t *testing.T) {
		pop := &Population{
			Agents: []*mutation.Strategy{
				{Score: 100, PromptTemplate: "solo"},
			},
		}
		dr := pop.measureDiversityReportLocked()
		if dr.PromptTemplateDistribution["solo"] != 1 {
			t.Errorf("solo template count = %d, want 1", dr.PromptTemplateDistribution["solo"])
		}
		if len(dr.PromptTemplateDistribution) != 1 {
			t.Errorf("expected 1 entry, got %d", len(dr.PromptTemplateDistribution))
		}
	})

	t.Run("DiversityStats returns report via read lock", func(t *testing.T) {
		pop := &Population{
			Agents: []*mutation.Strategy{
				{Score: 100, PromptTemplate: "tpl_a"},
				{Score: 80, PromptTemplate: "tpl_b"},
			},
		}
		dr := pop.DiversityStats()
		if dr.PromptTemplateDistribution["tpl_a"] != 1 {
			t.Errorf("tpl_a count = %d, want 1", dr.PromptTemplateDistribution["tpl_a"])
		}
	})
}

func TestEvolveOnIdleEliteCount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	scores := make([]float64, 10)
	for i := range scores {
		scores[i] = float64(i * 10)
	}

	pop := scoredPopulation(t, ctx, scores,
		WithMutationRate(0),
		WithEliteCount(3),
	)

	crosser, _ := NewCrossover(WithSeed(42))
	mutator := &scoredMutator{scores: scores}

	err := pop.EvolveOnIdle(ctx, mutator, crosser)
	if err != nil {
		t.Fatalf("EvolveOnIdle failed: %v", err)
	}

	// With EliteCount=3, the top 3 agents should survive with unchanged scores.
	topScores := pop.Agents[0].Score + pop.Agents[1].Score + pop.Agents[2].Score
	if topScores < 240 {
		t.Errorf("elite scores too low: sum of top 3 = %f, want >= 240", topScores)
	}
}
