package ares_bootstrap

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_config"
	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
	"github.com/Timwood0x10/ares/internal/runtime/observability"
)

// guardrailCounterValue reads ARES_evolution_guardrail_total{code} off the
// default registry. It gathers rather than using promtestutil so the test adds
// no new module dependency (code_rules §10.1: prefer what is already there).
func guardrailCounterValue(t *testing.T, code string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, mf := range families {
		if mf.GetName() != "ARES_evolution_guardrail_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "code" && lp.GetValue() == code {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0 // the label pair has not been touched yet
}

// TestBuildEvolutionGuardrailsMapsYAML is the B2 mapping contract: the two YAML
// knobs that were previously dead config (Generations, TargetFitness) must now
// reach the guardrail, and TargetFitness must be rescaled from its documented
// 0-100 range to the [0,1] scale the guardrail compares against.
func TestBuildEvolutionGuardrailsMapsYAML(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name          string
		ec            *ares_config.EvolutionConfig
		wantNil       bool
		wantStagnant  int
		wantBaseline  float64
		wantLineage   float64
		baselineUnset bool
	}{
		{
			name:    "nil_config_yields_no_guardrails",
			ec:      nil,
			wantNil: true,
		},
		{
			name: "generations_drives_max_stagnant_generations",
			ec: &ares_config.EvolutionConfig{
				Generations: 7,
			},
			wantStagnant:  7,
			baselineUnset: true,
			wantLineage:   0.8,
		},
		{
			name: "zero_generations_falls_back_to_config_default",
			ec:   &ares_config.EvolutionConfig{},
			// setDefaults normally fills Generations; a programmatic config
			// that skips it must not produce a zero threshold, which
			// PreEvolveCheck treats as "stagnation detection disabled".
			wantStagnant:  ares_config.DefaultEvolutionGenerations,
			baselineUnset: true,
			wantLineage:   0.8,
		},
		{
			name: "target_fitness_rescaled_to_unit_interval",
			ec: &ares_config.EvolutionConfig{
				Generations:   5,
				TargetFitness: 85, // documented 0-100 scale
			},
			wantStagnant: 5,
			wantBaseline: 0.85,
			wantLineage:  0.8,
		},
		{
			name: "zero_target_fitness_leaves_baseline_adaptive",
			ec: &ares_config.EvolutionConfig{
				Generations:   5,
				TargetFitness: 0,
			},
			wantStagnant: 5,
			// BaselineScore stays 0 so postEvolveCheckForSource falls back to
			// "baseline = best seen so far for this source" instead of
			// rejecting everything below a fabricated floor.
			baselineUnset: true,
			wantLineage:   0.8,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := buildEvolutionGuardrails(ctx, tt.ec, nil)
			if tt.wantNil {
				assert.Nil(t, g)
				return
			}
			require.NotNil(t, g)
			assert.Equal(t, tt.wantStagnant, g.MaxStagnantGenerations)
			if tt.baselineUnset {
				assert.Zero(t, g.BaselineScore,
					"an unset target_fitness must keep the adaptive baseline")
			} else {
				assert.InDelta(t, tt.wantBaseline, g.BaselineScore, 0.0001)
			}
			assert.InDelta(t, tt.wantLineage, g.MaxLineageShare, 0.0001,
				"MaxLineageShare has no YAML key and must keep the constructor default")
		})
	}
}

// TestBuildEvolutionGuardrailsForwardsEventsToMetrics locks the observability
// half of B2: guardrail events used to be log-only, so a critical trigger was
// invisible to monitoring. With a metrics sink wired every event increments
// ARES_evolution_guardrail_total{code}.
func TestBuildEvolutionGuardrailsForwardsEventsToMetrics(t *testing.T) {
	ctx := context.Background()
	// NewPrometheusMetrics registers on the default registry and is idempotent,
	// so the counter may already carry values from other tests in this package.
	// Measure the delta rather than the absolute value.
	metrics, err := observability.NewPrometheusMetrics()
	require.NoError(t, err)
	code := string(evolution.ErrCodeUnevaluatedPopulation)
	before := guardrailCounterValue(t, code)

	g := buildEvolutionGuardrails(ctx, &ares_config.EvolutionConfig{Generations: 1}, metrics)
	require.NotNil(t, g)

	// A majority-unevaluated population is the guardrail's Critical case.
	res := g.PreEvolveCheck(ctx, 0.5, 1, 10, 9)
	require.True(t, res.ShouldStop, "majority unevaluated must demand a stop")
	require.NotEmpty(t, res.Events)

	after := guardrailCounterValue(t, code)
	assert.InDelta(t, 1.0, after-before, 0.0001,
		"each guardrail event must increment ARES_evolution_guardrail_total{code}")
}

// TestBuildEvolutionGuardrailsReturnsDistinctInstances is a guard against a
// tempting "simplification". The adapter layer and the legacy scheduler each
// build their own guardrail, and they MUST stay separate: the type carries
// mutable stagnantCount / bestBySource state, and the two paths advance
// generations on different clocks with different score scales. One shared
// instance would let each path's stagnation count and baseline corrupt the
// other's — a silent, slow-burn bug that no existing test would catch.
func TestBuildEvolutionGuardrailsReturnsDistinctInstances(t *testing.T) {
	ctx := context.Background()
	ec := &ares_config.EvolutionConfig{Generations: 3}

	adapterSide := buildEvolutionGuardrails(ctx, ec, nil)
	schedulerSide := buildEvolutionGuardrails(ctx, ec, nil)
	require.NotNil(t, adapterSide)
	require.NotNil(t, schedulerSide)
	assert.NotSame(t, adapterSide, schedulerSide,
		"each driving path must own its guardrail instance (mutable stagnation state)")

	// Prove the isolation behaviourally, not just by pointer identity: driving
	// one path's stagnation counter must not move the other's.
	//
	// The first PostEvolveCheck registers an improvement (0.1 > the initial 0)
	// and resets the counter, so reaching MaxStagnantGenerations=3 needs four
	// calls: one improvement plus three no-improvement generations.
	for i := 0; i < 4; i++ {
		adapterSide.PostEvolveCheck(ctx, 0.1, i, nil)
	}
	adapterStagnated := adapterSide.PreEvolveCheck(ctx, 0.1, 4, 10, 0)
	schedulerStagnated := schedulerSide.PreEvolveCheck(ctx, 0.1, 4, 10, 0)
	assert.NotEmpty(t, adapterStagnated.Events,
		"the driven guardrail must report stagnation")
	assert.Empty(t, schedulerStagnated.Events,
		"the untouched guardrail must not inherit the other path's stagnation")
}

// TestFindUnknownPoolTools is the P2b contract: a tool_pool entry naming tools
// outside known_tools must be reported at wiring time, or every candidate from
// that entry is silently jailed by the unknown-name guard and evolution looks
// stalled rather than misconfigured.
func TestFindUnknownPoolTools(t *testing.T) {
	known := []string{"web_search", "calculator"}

	t.Run("clean_pool_reports_nothing", func(t *testing.T) {
		pool := []string{"web_search,calculator", "web_search"}
		assert.Nil(t, findUnknownPoolTools(pool, known))
	})

	t.Run("unknown_names_reported_per_entry", func(t *testing.T) {
		pool := []string{"web_search,ghost_tool", "web_search"}
		bad := findUnknownPoolTools(pool, known)
		require.Len(t, bad, 1)
		assert.Equal(t, []string{"ghost_tool"}, bad["web_search,ghost_tool"])
	})

	t.Run("empty_sides_disable_the_check", func(t *testing.T) {
		assert.Nil(t, findUnknownPoolTools(nil, known))
		assert.Nil(t, findUnknownPoolTools([]string{"ghost"}, nil))
		assert.Nil(t, findUnknownPoolTools([]string{"ghost"}, []string{"  "}))
	})

	t.Run("parses_like_the_executor", func(t *testing.T) {
		// "a,a," is ONE name to the executor and the guardrail (dedup); the
		// cross-check must agree, or it would cry wolf on a compliant entry.
		pool := []string{"web_search,web_search,"}
		assert.Nil(t, findUnknownPoolTools(pool, known))
	})
}
