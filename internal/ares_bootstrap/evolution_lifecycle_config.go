// evolution_lifecycle_config.go maps the evolution YAML blocks onto the
// ares_evolution control-plane
// config structs. Keeping the mapping in one place makes the YAML contract
// auditable against the design in a single file.
package ares_bootstrap

import (
	"time"

	"github.com/Timwood0x10/ares/internal/ares_config"
	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
)

// defaultFloat returns v when positive, otherwise def. YAML float knobs are
// all lower-bounded positive thresholds, so 0 means "unset".
func defaultFloat(v, def float64) float64 {
	if v > 0 {
		return v
	}
	return def
}

// defaultInt returns v when positive, otherwise def.
func defaultInt(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

// lifecycleConfigFromYAML builds the evolution.LifecycleConfig from the
// evolution.{lifecycle,gates} YAML blocks. Zero-value YAML
// fields fall back to DefaultLifecycleConfig so an absent YAML section
// preserves the code defaults.
func lifecycleConfigFromYAML(lc ares_config.EvolutionLifecycleConfig, gc ares_config.EvolutionGateConfig, cf ares_config.ChannelFeedbackConfig) *evolution.LifecycleConfig {
	cfg := evolution.DefaultLifecycleConfig()
	cfg.FitnessWindow = defaultInt(lc.FitnessWindow, cfg.FitnessWindow)
	cfg.MinSamplesBeforeJudge = defaultInt(lc.MinSamplesBeforeJudge, cfg.MinSamplesBeforeJudge)
	cfg.ColdStartScore = defaultFloat(lc.ColdStartScore, cfg.ColdStartScore)
	cfg.Weights = lifecycleWeightsFromYAML(lc, cf)
	if lc.WatchInterval != "" {
		if d, err := time.ParseDuration(lc.WatchInterval); err == nil && d > 0 {
			cfg.WatchInterval = d
		}
		// Invalid or non-positive watch_interval falls back to the default:
		// a broken YAML knob must never stop the watch loop entirely.
	}
	cfg.BlacklistGenerations = defaultInt(lc.BlacklistGenerations, cfg.BlacklistGenerations)
	// Promote throttle. Invalid or non-positive values fall back to the
	// default (3 × watch_interval) — a broken knob must never disable the
	// throttle, it is a correctness precondition of the open promote path.
	if lc.MinActiveDuration != "" {
		if d, perr := time.ParseDuration(lc.MinActiveDuration); perr == nil && d > 0 {
			cfg.MinActiveDuration = d
		}
	}
	cfg.Gates.EvalMinScore = defaultFloat(gc.EvalMinScore, cfg.Gates.EvalMinScore)
	cfg.Gates.RequireManualApproval = gc.RequireManualApproval
	return &cfg
}

// lifecycleWeightsFromYAML maps the flat weight knobs onto FitnessWeights.
// When no weight is set at all, the code defaults apply. A partial spec is
// used as-is (zero = excluded from the aggregate because the aggregator
// normalizes by the weight sum at query time) — silently mixing partial
// specs with defaults would produce a blend the operator did not specify.
//
// The channel weights come from a DIFFERENT YAML block
// (evolution.channel_feedback) and are applied on top either way: they are not
// part of the "did the operator specify any weights" question, because a
// channel weight without the channel's `enabled` switch records nothing to
// weigh. Leaving them out of that test also means arming a channel cannot
// accidentally zero out the five classic weights.
func lifecycleWeightsFromYAML(lc ares_config.EvolutionLifecycleConfig, cf ares_config.ChannelFeedbackConfig) evolution.FitnessWeights {
	var w evolution.FitnessWeights
	if lc.OutcomeWeight == 0 && lc.DimensionEvalWeight == 0 && lc.WorkflowWeight == 0 &&
		lc.SchedulerWeight == 0 && lc.RecoveryWeight == 0 {
		w = evolution.DefaultFitnessWeights()
	} else {
		w = evolution.FitnessWeights{
			Outcome:       lc.OutcomeWeight,
			DimensionEval: lc.DimensionEvalWeight,
			Workflow:      lc.WorkflowWeight,
			Scheduler:     lc.SchedulerWeight,
			Recovery:      lc.RecoveryWeight,
		}
	}
	// A weight only counts when its channel is actually recording: a weighted
	// source with no producer would contribute nothing while looking configured.
	if cf.CollabEnabled {
		w.Collaboration = cf.CollabWeight
	}
	if cf.ToolEnabled {
		w.ToolCall = cf.ToolWeight
	}
	return w
}
