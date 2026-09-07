package ares_bootstrap

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_config"
	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
)

// defaultFilledEvolution returns an EvolutionConfig exactly as LoadConfig hands
// it to Bootstrap: setDefaults has run, so every field is non-zero even when the
// operator configured nothing.
func defaultFilledEvolution() ares_config.EvolutionConfig {
	return ares_config.EvolutionConfig{
		PopulationSize:    ares_config.DefaultEvolutionPopulationSize,
		EliteCount:        ares_config.DefaultEvolutionEliteCount,
		SurvivalRate:      ares_config.DefaultEvolutionSurvivalRate,
		MutationRate:      ares_config.DefaultEvolutionMutationRate,
		MinMutationRate:   ares_config.DefaultEvolutionMinMutationRate,
		MaxMutationRate:   ares_config.DefaultEvolutionMaxMutationRate,
		BreedingPoolRatio: ares_config.DefaultEvolutionBreedingPoolRatio,
		SelectionStrategy: ares_config.DefaultEvolutionSelectionStrategy,
	}
}

// TestApplyGATuning is a contract test for how YAML evolution tuning maps onto
// the GA engine config. LoadConfig runs setDefaults before Bootstrap, so every
// cfg.Evolution field is non-zero; a plain non-zero guard cannot distinguish
// "operator tuned this field" from "setDefaults filled it in" and would
// silently replace the GA engine's own tuned defaults (e.g. EliteCount 3 with
// the config-layer default 2).
func TestApplyGATuning(t *testing.T) {
	engine := evolution.DefaultSystemConfig()

	tests := []struct {
		name string
		ec   func(*ares_config.EvolutionConfig)
		// gaCfgMutator, when non-nil, mutates the engine config before applyGATuning
		// runs; assertions compare against this mutated baseline.
		gaCfgMutator func(*evolution.SystemConfig)
		wantElite    int
		wantBreeding float64
		wantSurvival float64
	}{
		{
			name: "default_filled_config_keeps_ga_engine_defaults",
			ec:   func(ec *ares_config.EvolutionConfig) { *ec = defaultFilledEvolution() },
			// GA engine defaults must survive untouched: the config-layer
			// defaults (EliteCount 2, BreedingPoolRatio 0.5) differ from the
			// engine's tuned defaults (3 / 0.6).
			wantElite:    engine.EliteCount,
			wantBreeding: engine.BreedingPoolRatio,
			wantSurvival: engine.SurvivalRate,
		},
		{
			name: "explicit_tuned_values_override_ga_defaults",
			ec: func(ec *ares_config.EvolutionConfig) {
				*ec = defaultFilledEvolution()
				ec.EliteCount = 5
				ec.BreedingPoolRatio = 0.8
				ec.SurvivalRate = 0.9
			},
			wantElite:    5,
			wantBreeding: 0.8,
			wantSurvival: 0.9,
		},
		{
			name: "zero_fields_keep_ga_defaults",
			// Programmatic configs (tests constructing Config directly) skip
			// setDefaults; zero fields must not clobber the engine defaults.
			ec:           func(*ares_config.EvolutionConfig) {},
			wantElite:    engine.EliteCount,
			wantBreeding: engine.BreedingPoolRatio,
			wantSurvival: engine.SurvivalRate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gaCfg := evolution.DefaultSystemConfig()
			ec := &ares_config.EvolutionConfig{}
			tt.ec(ec)

			applyGATuning(&gaCfg, ec)

			require.Equal(t, tt.wantElite, gaCfg.EliteCount, "EliteCount")
			require.Equal(t, tt.wantBreeding, gaCfg.BreedingPoolRatio, "BreedingPoolRatio")
			require.Equal(t, tt.wantSurvival, gaCfg.SurvivalRate, "SurvivalRate")
		})
	}
}

// TestApplyGATuningExplicitDefaultValues documents the known tradeoff: an
// operator who explicitly sets a field equal to the config-layer default is
// indistinguishable from an unset field and keeps the GA engine default.
func TestApplyGATuningExplicitDefaultValues(t *testing.T) {
	gaCfg := evolution.DefaultSystemConfig()
	ec := defaultFilledEvolution()
	ec.EliteCount = ares_config.DefaultEvolutionEliteCount // explicit but equals default

	applyGATuning(&gaCfg, &ec)

	assert.Equal(t, evolution.DefaultSystemConfig().EliteCount, gaCfg.EliteCount,
		"explicit value equal to the config default is treated as untuned")
}
