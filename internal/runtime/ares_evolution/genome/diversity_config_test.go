package genome

import (
	"math/rand"
	"testing"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// TestDiversityWeightConfigNormalize verifies that normalize() correctly handles
// all edge cases: all zeros, partial zeros, custom values, and non-unit sums.
func TestDiversityWeightConfigNormalize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		give            DiversityWeightConfig
		wantNumeric     float64
		wantCategorical float64
		wantLineage     float64
	}{
		{
			name:            "all_zeros_uses_defaults",
			give:            DiversityWeightConfig{},
			wantNumeric:     0.4,
			wantCategorical: 0.4,
			wantLineage:     0.2,
		},
		{
			name: "custom_values_preserved_when_sum_to_one",
			give: DiversityWeightConfig{
				Numeric:     0.5,
				Categorical: 0.3,
				Lineage:     0.2,
			},
			wantNumeric:     0.5,
			wantCategorical: 0.3,
			wantLineage:     0.2,
		},
		{
			name: "partial_zero_numeric_gets_default_then_normalized",
			give: DiversityWeightConfig{
				Numeric:     0,
				Categorical: 0.5,
				Lineage:     0.5,
			},
			// Numeric gets default 0.4, total=1.4, normalized.
			wantNumeric:     0.4 / 1.4,
			wantCategorical: 0.5 / 1.4,
			wantLineage:     0.5 / 1.4,
		},
		{
			name: "partial_zero_categorical_gets_default_then_normalized",
			give: DiversityWeightConfig{
				Numeric:     0.6,
				Categorical: 0,
				Lineage:     0.4,
			},
			// Categorical gets default 0.4, total=1.4, normalized.
			wantNumeric:     0.6 / 1.4,
			wantCategorical: 0.4 / 1.4,
			wantLineage:     0.4 / 1.4,
		},
		{
			name: "partial_zero_lineage_gets_default_then_normalized",
			give: DiversityWeightConfig{
				Numeric:     0.7,
				Categorical: 0.3,
				Lineage:     0,
			},
			// Lineage gets default 0.2, total=1.2, normalized.
			wantNumeric:     0.7 / 1.2,
			wantCategorical: 0.3 / 1.2,
			wantLineage:     0.2 / 1.2,
		},
		{
			name: "non_summing_values_normalized",
			give: DiversityWeightConfig{
				Numeric:     8.0,
				Categorical: 1.0,
				Lineage:     1.0,
			},
			wantNumeric:     0.8,
			wantCategorical: 0.1,
			wantLineage:     0.1,
		},
		{
			name: "already_normalized_unchanged",
			give: DiversityWeightConfig{
				Numeric:     0.33,
				Categorical: 0.33,
				Lineage:     0.34,
			},
			wantNumeric:     0.33,
			wantCategorical: 0.33,
			wantLineage:     0.34,
		},
		{
			name: "numeric_dominant_weights",
			give: DiversityWeightConfig{
				Numeric:     0.8,
				Categorical: 0.1,
				Lineage:     0.1,
			},
			wantNumeric:     0.8,
			wantCategorical: 0.1,
			wantLineage:     0.1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.give.normalize()

			const tolerance = 1e-9
			if absFloat(got.Numeric-tt.wantNumeric) > tolerance {
				t.Errorf("Numeric = %f, want %f", got.Numeric, tt.wantNumeric)
			}
			if absFloat(got.Categorical-tt.wantCategorical) > tolerance {
				t.Errorf("Categorical = %f, want %f", got.Categorical, tt.wantCategorical)
			}
			if absFloat(got.Lineage-tt.wantLineage) > tolerance {
				t.Errorf("Lineage = %f, want %f", got.Lineage, tt.wantLineage)
			}

			// Verify normalized weights sum to ~1.0.
			sum := got.Numeric + got.Categorical + got.Lineage
			if absFloat(sum-1.0) > 1e-9 {
				t.Errorf("normalized weights sum = %f, want 1.0", sum)
			}
		})
	}
}

// TestApproxEqual verifies the approxEqual helper function.
func TestApproxEqual(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		giveA float64
		giveB float64
		want  bool
	}{
		{name: "equal_values", giveA: 1.0, giveB: 1.0, want: true},
		{name: "within_epsilon", giveA: 1.0, giveB: 1.0 + 1e-10, want: true},
		{name: "outside_epsilon", giveA: 1.0, giveB: 1.0 + 1e-8, want: false},
		{name: "negative_diff", giveA: 1.0, giveB: 1.0 - 1e-10, want: true},
		{name: "zero_both", giveA: 0, giveB: 0, want: true},
		{name: "large_equal", giveA: 1000, giveB: 1000, want: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := approxEqual(tt.giveA, tt.giveB)
			if got != tt.want {
				t.Errorf("approxEqual(%f, %f) = %v, want %v", tt.giveA, tt.giveB, got, tt.want)
			}
		})
	}
}

// TestCustomDiversityWeightsApplied verifies that custom weights configured via
// WithDiversityWeights are actually used when computing DiversityReport.Overall.
func TestCustomDiversityWeightsApplied(t *testing.T) {
	t.Parallel()

	customWeights := DiversityWeightConfig{
		Numeric:     0.8,
		Categorical: 0.1,
		Lineage:     0.1,
	}

	agents := []*mutation.Strategy{
		{Params: map[string]any{"temperature": 0.1}, PromptTemplate: "tmpl-a", ParentID: "p1"},
		{Params: map[string]any{"temperature": 0.9}, PromptTemplate: "tmpl-b", ParentID: "p2"},
		{Params: map[string]any{"temperature": 0.5}, PromptTemplate: "tmpl-c", ParentID: "p3"},
		{Params: map[string]any{"temperature": 0.3}, PromptTemplate: "tmpl-a", ParentID: "p4"},
		{Params: map[string]any{"temperature": 0.7}, PromptTemplate: "tmpl-b", ParentID: "p5"},
	}

	popWithCustom := &Population{
		Agents: agents,
		Size:   len(agents),
		cfg: PopulationConfig{
			DiversityThreshold: 0.15,
			DiversityWeights:   customWeights,
		},
	}

	popWithDefault := &Population{
		Agents: agents,
		Size:   len(agents),
		cfg: PopulationConfig{
			DiversityThreshold: 0.15,
			DiversityWeights:   DiversityWeightConfig{}, // zero → defaults
		},
	}

	reportCustom := popWithCustom.measureDiversityReportLocked()
	reportDefault := popWithDefault.measureDiversityReportLocked()

	// Component scores must be identical (weights don't affect components).
	const tol = 1e-9
	if absFloat(reportCustom.Numeric-reportDefault.Numeric) > tol ||
		absFloat(reportCustom.Categorical-reportDefault.Categorical) > tol ||
		absFloat(reportCustom.Lineage-reportDefault.Lineage) > tol {
		t.Error("component diversity scores should be identical regardless of weights")
	}

	// Overall scores should differ because weights differ.
	if absFloat(reportCustom.Overall-reportDefault.Overall) < 1e-6 {
		t.Errorf("custom and default Overall should differ: custom=%f, default=%f",
			reportCustom.Overall, reportDefault.Overall)
	}

	// Verify effective weights reflect custom config (normalized).
	wantNorm := customWeights.normalize()
	if absFloat(reportCustom.EffectiveWeights.Numeric-wantNorm.Numeric) > tol {
		t.Errorf("EffectiveWeights.Numeric = %f, want %f", reportCustom.EffectiveWeights.Numeric, wantNorm.Numeric)
	}
	if absFloat(reportCustom.EffectiveWeights.Categorical-wantNorm.Categorical) > tol {
		t.Errorf("EffectiveWeights.Categorical = %f, want %f", reportCustom.EffectiveWeights.Categorical, wantNorm.Categorical)
	}
	if absFloat(reportCustom.EffectiveWeights.Lineage-wantNorm.Lineage) > tol {
		t.Errorf("EffectiveWeights.Lineage = %f, want %f", reportCustom.EffectiveWeights.Lineage, wantNorm.Lineage)
	}

	// Manually verify: Overall_custom ≈ N*0.8 + C*0.1 + L*0.1
	expectedCustom := reportDefault.Numeric*wantNorm.Numeric +
		reportDefault.Categorical*wantNorm.Categorical +
		reportDefault.Lineage*wantNorm.Lineage
	if absFloat(reportCustom.Overall-expectedCustom) > 1e-9 {
		t.Errorf("Overall mismatch: got %f, expected computed %f", reportCustom.Overall, expectedCustom)
	}
}

// TestInjectFreshMutantsBoundaryCases verifies boundary condition handling in
// injectFreshMutantsLocked for various eliteCount/population size combinations.
func TestInjectFreshMutantsBoundaryCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		giveN               int
		giveEliteCount      int
		wantReplacedMin     int // minimum number of agents we expect replaced
		wantReplacedMax     int // maximum number of agents we expect replaced
		wantElitesPreserved bool
	}{
		{
			name:                "elite_0_n_10_replaces_bottom_3_4",
			giveN:               10,
			giveEliteCount:      0,
			wantReplacedMin:     3,
			wantReplacedMax:     4,
			wantElitesPreserved: true, // no elites to protect
		},
		{
			name:                "elite_3_n_10_replaces_bottom_3_protects_0_3",
			giveN:               10,
			giveEliteCount:      3,
			wantReplacedMin:     2,
			wantReplacedMax:     3,
			wantElitesPreserved: true,
		},
		{
			name:                "elite_8_n_10_reduces_replacement_to_2",
			giveN:               10,
			giveEliteCount:      8,
			wantReplacedMin:     1,
			wantReplacedMax:     2,
			wantElitesPreserved: true,
		},
		{
			name:                "elite_9_n_10_no_op_too_small",
			giveN:               10,
			giveEliteCount:      9,
			wantReplacedMin:     0,
			wantReplacedMax:     0,
			wantElitesPreserved: true,
		},
		{
			name:                "elite_10_n_10_no_op_all_elite",
			giveN:               10,
			giveEliteCount:      10,
			wantReplacedMin:     0,
			wantReplacedMax:     0,
			wantElitesPreserved: true,
		},
		{
			name:                "elite_5_n_6_no_op_too_small",
			giveN:               6,
			giveEliteCount:      5,
			wantReplacedMin:     0,
			wantReplacedMax:     0,
			wantElitesPreserved: true,
		},
		{
			name:                "elite_1_n_3_replaces_1_safe",
			giveN:               3,
			giveEliteCount:      1,
			wantReplacedMin:     1,
			wantReplacedMax:     1,
			wantElitesPreserved: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			agents := make([]*mutation.Strategy, tt.giveN)
			eliteIDs := make(map[string]bool, tt.giveEliteCount)
			for i := range agents {
				id := string(rune('a' + i))
				agents[i] = &mutation.Strategy{
					ID:             id,
					Score:          float64(tt.giveN - i),
					Params:         map[string]any{"temperature": 0.5},
					PromptTemplate: "template",
					Version:        1,
				}
				if i < tt.giveEliteCount {
					eliteIDs[id] = true
				}
			}

			pop := &Population{
				Agents:     agents,
				Size:       tt.giveN,
				Generation: 1,
				rng:        rand.New(rand.NewSource(42)),
			}

			pop.injectFreshMutantsLocked(tt.giveEliteCount)

			replacedCount := 0
			for _, a := range pop.Agents {
				if a.ID == "" || a.Score == ScoreUnevaluated {
					replacedCount++
				}
			}

			if replacedCount < tt.wantReplacedMin || replacedCount > tt.wantReplacedMax {
				t.Errorf("replaced count = %d, want [%d, %d]", replacedCount, tt.wantReplacedMin, tt.wantReplacedMax)
			}

			if tt.wantElitesPreserved && tt.giveEliteCount > 0 {
				for i := 0; i < tt.giveEliteCount && i < len(pop.Agents); i++ {
					if !eliteIDs[pop.Agents[i].ID] {
						t.Errorf("elite at index %d was overwritten: id=%s (expected one of original elites)", i, pop.Agents[i].ID)
					}
				}
			}
		})
	}
}

// TestElitesNeverOverwrittenByInjection verifies that after injection, all agents
// in [0, eliteCount) retain their original IDs and Scores regardless of population
// size or elite count configuration.
func TestElitesNeverOverwrittenByInjection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		giveN          int
		giveEliteCount int
	}{
		{name: "n20_elite3", giveN: 20, giveEliteCount: 3},
		{name: "n15_elite5", giveN: 15, giveEliteCount: 5},
		{name: "n10_elite4", giveN: 10, giveEliteCount: 4},
		{name: "n8_elite3", giveN: 8, giveEliteCount: 3},
		{name: "n12_elite8", giveN: 12, giveEliteCount: 8},
		{name: "n7_elite4", giveN: 7, giveEliteCount: 4},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			agents := make([]*mutation.Strategy, tt.giveN)
			eliteOriginalIDs := make([]string, tt.giveEliteCount)
			eliteOriginalScores := make([]float64, tt.giveEliteCount)
			for i := range agents {
				score := float64(tt.giveN - i)
				agents[i] = &mutation.Strategy{
					ID:             string(rune('a' + i)),
					Score:          score,
					Params:         map[string]any{"temperature": 0.5, "top_k": 40},
					PromptTemplate: "template",
					Version:        1,
				}
				if i < tt.giveEliteCount {
					eliteOriginalIDs[i] = agents[i].ID
					eliteOriginalScores[i] = agents[i].Score
				}
			}

			pop := &Population{
				Agents:     agents,
				Size:       tt.giveN,
				Generation: 2,
				rng:        rand.New(rand.NewSource(99)),
			}

			pop.injectFreshMutantsLocked(tt.giveEliteCount)

			// Verify every elite agent retains its original ID and Score.
			for i := 0; i < tt.giveEliteCount; i++ {
				if pop.Agents[i].ID != eliteOriginalIDs[i] {
					t.Errorf("elite[%d]: ID changed from %s to %s", i, eliteOriginalIDs[i], pop.Agents[i].ID)
				}
				if pop.Agents[i].Score != eliteOriginalScores[i] {
					t.Errorf("elite[%d]: Score changed from %f to %f", i, eliteOriginalScores[i], pop.Agents[i].Score)
				}
			}
		})
	}
}

// TestWithDiversityWeightsOption verifies the functional option correctly sets
// the DiversityWeights field on PopulationConfig.
func TestWithDiversityWeightsOption(t *testing.T) {
	t.Parallel()

	custom := DiversityWeightConfig{
		Numeric:     0.7,
		Categorical: 0.2,
		Lineage:     0.1,
	}

	cfg := DefaultPopulationConfig()
	opt := WithDiversityWeights(custom)
	if err := opt(&cfg); err != nil {
		t.Fatalf("WithDiversityWeights returned error: %v", err)
	}

	// Verify raw config stores the unnormalized value (normalize() is called at use time).
	if cfg.DiversityWeights.Numeric != custom.Numeric {
		t.Errorf("cfg.DiversityWeights.Numeric = %f, want %f", cfg.DiversityWeights.Numeric, custom.Numeric)
	}
	if cfg.DiversityWeights.Categorical != custom.Categorical {
		t.Errorf("cfg.DiversityWeights.Categorical = %f, want %f", cfg.DiversityWeights.Categorical, custom.Categorical)
	}
	if cfg.DiversityWeights.Lineage != custom.Lineage {
		t.Errorf("cfg.DiversityWeights.Lineage = %f, want %f", cfg.DiversityWeights.Lineage, custom.Lineage)
	}
}

// TestDefaultDiversityWeightsInReport verifies that when no custom weights are set,
// the EffectiveWeights in DiversityReport reflects the documented defaults.
func TestDefaultDiversityWeightsInReport(t *testing.T) {
	t.Parallel()

	agents := []*mutation.Strategy{
		{Params: map[string]any{"temperature": 0.2}, PromptTemplate: "a", ParentID: "x"},
		{Params: map[string]any{"temperature": 0.8}, PromptTemplate: "b", ParentID: "y"},
	}

	pop := &Population{
		Agents: agents,
		Size:   2,
		cfg:    DefaultPopulationConfig(),
	}

	report := pop.measureDiversityReportLocked()

	// Default weights should be (0.4, 0.4, 0.2).
	const tol = 1e-9
	if absFloat(report.EffectiveWeights.Numeric-0.4) > tol {
		t.Errorf("default EffectiveWeights.Numeric = %f, want 0.4", report.EffectiveWeights.Numeric)
	}
	if absFloat(report.EffectiveWeights.Categorical-0.4) > tol {
		t.Errorf("default EffectiveWeights.Categorical = %f, want 0.4", report.EffectiveWeights.Categorical)
	}
	if absFloat(report.EffectiveWeights.Lineage-0.2) > tol {
		t.Errorf("default EffectiveWeights.Lineage = %f, want 0.2", report.EffectiveWeights.Lineage)
	}
}
