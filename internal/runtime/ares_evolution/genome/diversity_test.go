package genome

import (
	"math/rand"
	"strings"
	"sync"
	"testing"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

func TestDiversityReportComponents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		giveAgents          []*mutation.Strategy
		wantNumericLow      bool
		wantCategoricalHigh bool
		wantLineageLow      bool
	}{
		{
			name: "identical_params_zero_numeric",
			giveAgents: []*mutation.Strategy{
				{Params: map[string]any{"temperature": 0.5, "top_k": 40}, PromptTemplate: "tmpl-a", ParentID: "p1"},
				{Params: map[string]any{"temperature": 0.5, "top_k": 40}, PromptTemplate: "tmpl-a", ParentID: "p1"},
				{Params: map[string]any{"temperature": 0.5, "top_k": 40}, PromptTemplate: "tmpl-a", ParentID: "p1"},
			},
			wantNumericLow:      true,
			wantCategoricalHigh: false,
			wantLineageLow:      true,
		},
		{
			name: "different_prompts_high_categorical",
			giveAgents: []*mutation.Strategy{
				{Params: map[string]any{"temperature": 0.5}, PromptTemplate: "tmpl-a", ParentID: "p1"},
				{Params: map[string]any{"temperature": 0.5}, PromptTemplate: "tmpl-b", ParentID: "p1"},
				{Params: map[string]any{"temperature": 0.5}, PromptTemplate: "tmpl-c", ParentID: "p1"},
			},
			wantNumericLow:      true,
			wantCategoricalHigh: true,
			wantLineageLow:      true,
		},
		{
			name: "different_params_high_numeric",
			giveAgents: []*mutation.Strategy{
				{Params: map[string]any{"temperature": 0.1}, PromptTemplate: "tmpl-a", ParentID: "p1"},
				{Params: map[string]any{"temperature": 0.5}, PromptTemplate: "tmpl-a", ParentID: "p2"},
				{Params: map[string]any{"temperature": 0.9}, PromptTemplate: "tmpl-a", ParentID: "p3"},
			},
			wantNumericLow:      false,
			wantCategoricalHigh: false,
			wantLineageLow:      false,
		},
		{
			name: "same_parent_low_lineage",
			giveAgents: []*mutation.Strategy{
				{Params: map[string]any{"temperature": 0.5}, PromptTemplate: "tmpl-a", ParentID: "same-parent"},
				{Params: map[string]any{"temperature": 0.6}, PromptTemplate: "tmpl-a", ParentID: "same-parent"},
				{Params: map[string]any{"temperature": 0.7}, PromptTemplate: "tmpl-a", ParentID: "same-parent"},
				{Params: map[string]any{"temperature": 0.8}, PromptTemplate: "tmpl-a", ParentID: "same-parent"},
			},
			wantNumericLow:      false,
			wantCategoricalHigh: false,
			wantLineageLow:      true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pop := &Population{Agents: tt.giveAgents}
			report := pop.measureDiversityReportLocked()

			if tt.wantNumericLow && report.Numeric > 0.1 {
				t.Errorf("expected Numeric < 0.1, got %f", report.Numeric)
			}
			if !tt.wantNumericLow && report.Numeric < 0.1 {
				t.Errorf("expected Numeric >= 0.1, got %f", report.Numeric)
			}
			if tt.wantCategoricalHigh && report.Categorical < 0.3 {
				t.Errorf("expected Categorical > 0.3, got %f", report.Categorical)
			}
			if !tt.wantCategoricalHigh && report.Categorical > 0.8 {
				t.Errorf("expected Categorical <= 0.8, got %f", report.Categorical)
			}
			if tt.wantLineageLow && report.Lineage > 0.5 {
				t.Errorf("expected Lineage < 0.5, got %f", report.Lineage)
			}
			if !tt.wantLineageLow && report.Lineage < 0.5 {
				t.Errorf("expected Lineage >= 0.5, got %f", report.Lineage)
			}
		})
	}
}

func TestLineageDiversityCalculation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		giveAgents        []*mutation.Strategy
		wantLineage       float64
		wantDominantShare float64
	}{
		{
			name: "all_same_parent",
			giveAgents: []*mutation.Strategy{
				{ParentID: "p1"},
				{ParentID: "p1"},
				{ParentID: "p1"},
				{ParentID: "p1"},
			},
			wantLineage:       0.25,
			wantDominantShare: 1.0,
		},
		{
			name: "all_different_parents",
			giveAgents: []*mutation.Strategy{
				{ParentID: "p1"},
				{ParentID: "p2"},
				{ParentID: "p3"},
				{ParentID: "p4"},
			},
			wantLineage:       1.0,
			wantDominantShare: 0.25,
		},
		{
			name: "mixed_parents_dominant_75pct",
			giveAgents: []*mutation.Strategy{
				{ParentID: "dominant"},
				{ParentID: "dominant"},
				{ParentID: "dominant"},
				{ParentID: "other"},
			},
			wantLineage:       0.5,
			wantDominantShare: 0.75,
		},
		{
			name: "empty_parent_treated_as_root",
			giveAgents: []*mutation.Strategy{
				{ParentID: ""},
				{ParentID: ""},
				{ParentID: "other"},
			},
			wantLineage:       0.6666666666666666,
			wantDominantShare: 0.6666666666666666,
		},
		{
			name: "single_agent_returns_max",
			giveAgents: []*mutation.Strategy{
				{ParentID: "only"},
			},
			wantLineage:       1.0,
			wantDominantShare: 1.0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pop := &Population{Agents: tt.giveAgents}
			lineage, dominant := pop.measureLineageDiversityLocked()

			const tolerance = 1e-9
			if absFloat(lineage-tt.wantLineage) > tolerance {
				t.Errorf("Lineage = %f, want %f", lineage, tt.wantLineage)
			}
			if absFloat(dominant-tt.wantDominantShare) > tolerance {
				t.Errorf("DominantLineageShare = %f, want %f", dominant, tt.wantDominantShare)
			}
		})
	}
}

func TestDiversityCollapseDetection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		giveAgents      []*mutation.Strategy
		giveThreshold   float64
		expectInjection bool
	}{
		{
			name: "low_overall_diversity_triggers_injection",
			giveAgents: []*mutation.Strategy{
				{Params: map[string]any{"temperature": 0.5}, Score: 10.0},
				{Params: map[string]any{"temperature": 0.5}, Score: 9.0},
				{Params: map[string]any{"temperature": 0.5}, Score: 8.0},
				{Params: map[string]any{"temperature": 0.5}, Score: 7.0},
				{Params: map[string]any{"temperature": 0.5}, Score: 6.0},
				{Params: map[string]any{"temperature": 0.5}, Score: 5.0},
			},
			giveThreshold:   0.15,
			expectInjection: true,
		},
		{
			name: "high_diversity_no_injection",
			giveAgents: []*mutation.Strategy{
				{Params: map[string]any{"temperature": 0.1}, Score: 10.0, ParentID: "a"},
				{Params: map[string]any{"temperature": 0.9}, Score: 9.0, ParentID: "b"},
				{Params: map[string]any{"temperature": 0.3}, Score: 8.0, ParentID: "c"},
				{Params: map[string]any{"temperature": 0.7}, Score: 7.0, ParentID: "d"},
				{Params: map[string]any{"temperature": 0.2}, Score: 6.0, ParentID: "e"},
				{Params: map[string]any{"temperature": 0.8}, Score: 5.0, ParentID: "f"},
			},
			giveThreshold:   0.15,
			expectInjection: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pop := &Population{
				Agents:     tt.giveAgents,
				Size:       len(tt.giveAgents),
				Generation: 1,
				rng:        rand.New(rand.NewSource(42)),
				cfg: PopulationConfig{
					DiversityThreshold: tt.giveThreshold,
					EliteCount:         2,
				},
			}

			report := pop.measureDiversityReportLocked()
			shouldInject := report.Overall < tt.giveThreshold || report.DominantLineageShare > 0.6

			if tt.expectInjection != shouldInject {
				t.Errorf("expectInjection=%v, but collapse check would inject=%v (overall=%.4f, dominant=%.4f)",
					tt.expectInjection, shouldInject, report.Overall, report.DominantLineageShare)
			}

			if shouldInject {
				preIDs := make(map[string]bool)
				for _, a := range pop.Agents {
					preIDs[a.ID] = true
				}
				pop.injectFreshMutantsLocked(2)

				freshCount := 0
				for _, a := range pop.Agents {
					if !preIDs[a.ID] {
						freshCount++
					}
				}
				if freshCount == 0 {
					t.Error("expected at least one fresh mutant after injection")
				}
			}
		})
	}
}

func TestDominantLineageGuard(t *testing.T) {
	t.Parallel()

	agents := make([]*mutation.Strategy, 10)
	for i := range agents {
		parentID := "dominant-lineage"
		if i >= 2 {
			parentID = "other-lineage"
		}
		agents[i] = &mutation.Strategy{
			ID:             string(rune('a' + i)),
			ParentID:       parentID,
			Score:          float64(10 - i),
			Params:         map[string]any{"temperature": float64(i)*0.1 + 0.1},
			PromptTemplate: "template",
		}
	}

	pop := &Population{
		Agents:     agents,
		Size:       10,
		Generation: 1,
		rng:        rand.New(rand.NewSource(42)),
		cfg: PopulationConfig{
			DiversityThreshold: 0.15,
			EliteCount:         2,
		},
	}

	report := pop.measureDiversityReportLocked()

	// 80% share same parent (8 out of 10).
	if report.DominantLineageShare < 0.7 {
		t.Errorf("expected DominantLineageShare >= 0.7 for 80%% same parent, got %.4f", report.DominantLineageShare)
	}

	// Should trigger injection due to dominant lineage > 0.6.
	if !(report.Overall < pop.cfg.DiversityThreshold || report.DominantLineageShare > 0.6) {
		t.Error("expected lineage guard to trigger injection")
	}

	// Verify injection actually changes agents.
	preIDs := make(map[string]bool)
	for _, a := range pop.Agents {
		preIDs[a.ID] = true
	}
	pop.injectFreshMutantsLocked(2)

	freshCount := 0
	for _, a := range pop.Agents {
		if !preIDs[a.ID] {
			freshCount++
		}
	}
	if freshCount == 0 {
		t.Error("expected fresh mutants injected for lineage recovery")
	}
}

func TestDiversityStatsThreadSafe(t *testing.T) {
	t.Parallel()

	agents := make([]*mutation.Strategy, 20)
	for i := range agents {
		agents[i] = &mutation.Strategy{
			ID:       string(rune('a' + i)),
			ParentID: string(rune('A' + i)),
			Score:    float64(i),
			Params:   map[string]any{"temperature": float64(i)*0.05 + 0.1},
		}
	}

	pop := &Population{
		Agents: agents,
		Size:   20,
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			report := pop.DiversityStats()
			if report.Overall < 0 || report.Overall > 1 {
				t.Errorf("overall diversity out of range: %f", report.Overall)
			}
			if report.Numeric < 0 || report.Numeric > 1 {
				t.Errorf("numeric diversity out of range: %f", report.Numeric)
			}
			if report.Categorical < 0 || report.Categorical > 1 {
				t.Errorf("categorical diversity out of range: %f", report.Categorical)
			}
			if report.Lineage < 0 || report.Lineage > 1 {
				t.Errorf("lineage diversity out of range: %f", report.Lineage)
			}
			if report.DominantLineageShare < 0 || report.DominantLineageShare > 1 {
				t.Errorf("dominant lineage share out of range: %f", report.DominantLineageShare)
			}
		}()
	}
	wg.Wait()
}

func TestBackwardCompatibilityMeasureDiversity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		giveAgents []*mutation.Strategy
	}{
		{
			name: "identical_agents",
			giveAgents: []*mutation.Strategy{
				{Params: map[string]any{"temperature": 0.5}},
				{Params: map[string]any{"temperature": 0.5}},
			},
		},
		{
			name: "divergent_agents",
			giveAgents: []*mutation.Strategy{
				{Params: map[string]any{"temperature": 0.1}},
				{Params: map[string]any{"temperature": 0.9}},
			},
		},
		{
			name: "single_agent",
			giveAgents: []*mutation.Strategy{
				{Params: map[string]any{"temperature": 0.5}},
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pop := &Population{Agents: tt.giveAgents}

			oldStyle := pop.measureDiversityLocked()
			newStyle := pop.measureDiversityReportLocked().Overall

			if oldStyle != newStyle {
				t.Errorf("backward compatibility broken: measureDiversityLocked()=%f, report.Overall=%f",
					oldStyle, newStyle)
			}
		})
	}
}

func TestInjectFreshMutantsLocked(t *testing.T) {
	t.Parallel()

	t.Run("replaces_bottom_agents", func(t *testing.T) {
		t.Parallel()
		agents := make([]*mutation.Strategy, 9)
		for i := range agents {
			agents[i] = &mutation.Strategy{
				ID:             string(rune('a' + i)),
				Score:          float64(10 - i),
				Params:         map[string]any{"temperature": 0.5, "top_k": 40},
				PromptTemplate: "template",
				Version:        1,
			}
		}

		pop := &Population{
			Agents:     agents,
			Size:       9,
			Generation: 5,
			rng:        rand.New(rand.NewSource(99)),
		}

		pop.injectFreshMutantsLocked(3)

		// Bottom 30% of 9 = ~3 agents replaced (indices 6,7,8).
		// Elite count is 3, so indices 0-2 are protected.
		freshCount := 0
		for i, a := range pop.Agents {
			if strings.HasPrefix(a.ID, "fresh-mut-") {
				freshCount++
			}
			if strings.HasPrefix(a.ID, "fresh-mut-") && a.Score != ScoreUnevaluated {
				t.Errorf("fresh mutant at index %d should have ScoreUnevaluated, got %f", i, a.Score)
			}
		}
		if freshCount == 0 {
			t.Error("expected at least one fresh mutant injected")
		}
	})

	t.Run("no_op_when_too_small", func(t *testing.T) {
		t.Parallel()
		agents := []*mutation.Strategy{
			{ID: "a", Params: map[string]any{"temp": 0.5}},
			{ID: "b", Params: map[string]any{"temp": 0.5}},
		}
		pop := &Population{Agents: agents, Size: 2, rng: rand.New(rand.NewSource(42))}
		preIDs := make(map[string]bool)
		for _, a := range pop.Agents {
			preIDs[a.ID] = true
		}
		pop.injectFreshMutantsLocked(1)
		for _, a := range pop.Agents {
			if !preIDs[a.ID] {
				t.Error("no mutation expected for tiny population")
			}
		}
	})

	t.Run("preserves_elites", func(t *testing.T) {
		t.Parallel()
		agents := make([]*mutation.Strategy, 12)
		for i := range agents {
			agents[i] = &mutation.Strategy{
				ID:     string(rune('a' + i)),
				Score:  float64(20 - i),
				Params: map[string]any{"temperature": 0.5},
			}
		}
		pop := &Population{
			Agents:     agents,
			Size:       12,
			Generation: 3,
			rng:        rand.New(rand.NewSource(42)),
		}

		eliteIDs := make(map[string]bool)
		for i := 0; i < 4; i++ {
			eliteIDs[agents[i].ID] = true
		}

		pop.injectFreshMutantsLocked(4)

		// Elites (first 4) should still have original IDs.
		for i := 0; i < 4; i++ {
			if !eliteIDs[pop.Agents[i].ID] {
				t.Errorf("elite agent at index %d was replaced: id=%s", i, pop.Agents[i].ID)
			}
		}
	})
}

func TestNumericParamDistance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		giveA    *mutation.Strategy
		giveB    *mutation.Strategy
		wantDist float64
	}{
		{
			name:     "identical_floats",
			giveA:    &mutation.Strategy{Params: map[string]any{"temp": 0.5}},
			giveB:    &mutation.Strategy{Params: map[string]any{"temp": 0.5}},
			wantDist: 0.0,
		},
		{
			name:     "max_separated",
			giveA:    &mutation.Strategy{Params: map[string]any{"temp": 0.0}},
			giveB:    &mutation.Strategy{Params: map[string]any{"temp": 1.0}},
			wantDist: 1.0,
		},
		{
			name:     "int_params",
			giveA:    &mutation.Strategy{Params: map[string]any{"top_k": 10}},
			giveB:    &mutation.Strategy{Params: map[string]any{"top_k": 90}},
			wantDist: 1.0,
		},
		{
			name:     "no_numeric_params",
			giveA:    &mutation.Strategy{Params: map[string]any{"name": "foo"}},
			giveB:    &mutation.Strategy{Params: map[string]any{"name": "bar"}},
			wantDist: 0.0,
		},
		{
			name:     "mixed_numeric_and_string",
			giveA:    &mutation.Strategy{Params: map[string]any{"temp": 0.2, "name": "a"}},
			giveB:    &mutation.Strategy{Params: map[string]any{"temp": 0.8, "name": "b"}},
			wantDist: 1.0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			keys := collectAgentParamKeys([]*mutation.Strategy{tt.giveA, tt.giveB})
			ranges := computeParamRanges([]*mutation.Strategy{tt.giveA, tt.giveB}, keys)
			dist := numericParamDistance(tt.giveA, tt.giveB, keys, ranges)

			const tolerance = 1e-6
			if absFloat(dist-tt.wantDist) > tolerance {
				t.Errorf("numericParamDistance = %f, want %f", dist, tt.wantDist)
			}
		})
	}
}

func TestCategoricalDiversityEdgeCases(t *testing.T) {
	t.Parallel()

	t.Run("single_agent_returns_1", func(t *testing.T) {
		t.Parallel()
		pop := &Population{
			Agents: []*mutation.Strategy{
				{PromptTemplate: "tmpl-a", Params: map[string]any{}},
			},
		}
		div := pop.measureCategoricalDiversityLocked()
		if div != 1.0 {
			t.Errorf("single agent categorical diversity = %f, want 1.0", div)
		}
	})

	t.Run("empty_population_returns_1", func(t *testing.T) {
		t.Parallel()
		pop := &Population{Agents: []*mutation.Strategy{}}
		div := pop.measureCategoricalDiversityLocked()
		if div != 1.0 {
			t.Errorf("empty population categorical diversity = %f, want 1.0", div)
		}
	})

	t.Run("different_tools_high_diversity", func(t *testing.T) {
		t.Parallel()
		pop := &Population{
			Agents: []*mutation.Strategy{
				{PromptTemplate: "tmpl", Params: map[string]any{"tools": "web_search"}},
				{PromptTemplate: "tmpl", Params: map[string]any{"tools": "code_exec"}},
			},
		}
		div := pop.measureCategoricalDiversityLocked()
		if div < 0.4 {
			t.Errorf("different tools categorical diversity = %f, want > 0.4", div)
		}
	})

	t.Run("missing_tools_key_treated_as_difference", func(t *testing.T) {
		t.Parallel()
		pop := &Population{
			Agents: []*mutation.Strategy{
				{PromptTemplate: "tmpl", Params: map[string]any{"tools": "web"}},
				{PromptTemplate: "tmpl", Params: map[string]any{}},
			},
		}
		div := pop.measureCategoricalDiversityLocked()
		if div < 0.25 {
			t.Errorf("mismatched tools presence diversity = %f, want > 0.25", div)
		}
	})
}

func TestDiversityStatsInPopulationStats(t *testing.T) {
	t.Parallel()

	agents := make([]*mutation.Strategy, 6)
	for i := range agents {
		agents[i] = &mutation.Strategy{
			ID:       string(rune('a' + i)),
			ParentID: string(rune('A' + i)),
			Score:    float64(i * 10),
			Params:   map[string]any{"temperature": float64(i)*0.1 + 0.1},
		}
	}

	pop := &Population{
		Agents:     agents,
		Size:       6,
		Generation: 3,
	}

	stats := pop.Stats()

	if stats.Generation != 3 {
		t.Errorf("Generation = %d, want 3", stats.Generation)
	}
	if stats.Size != 6 {
		t.Errorf("Size = %d, want 6", stats.Size)
	}

	// Verify diversity fields are populated.
	report := stats.Diversity
	if report.Overall < 0 || report.Overall > 1 {
		t.Errorf("Diversity.Overall out of range: %f", report.Overall)
	}
	if report.Numeric < 0 || report.Numeric > 1 {
		t.Errorf("Diversity.Numeric out of range: %f", report.Numeric)
	}
	if report.Categorical < 0 || report.Categorical > 1 {
		t.Errorf("Diversity.Categorical out of range: %f", report.Categorical)
	}
	if report.Lineage < 0 || report.Lineage > 1 {
		t.Errorf("Diversity.Lineage out of range: %f", report.Lineage)
	}
	if report.DominantLineageShare < 0 || report.DominantLineageShare > 1 {
		t.Errorf("Diversity.DominantLineageShare out of range: %f", report.DominantLineageShare)
	}
}
