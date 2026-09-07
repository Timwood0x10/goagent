package genome

import (
	"context"
	"fmt"
	"math"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// MetaParams are the evolution hyperparameters that the MetaController
// can self-adapt based on observed performance and diversity metrics.
type MetaParams struct {
	// MutationRate is the probability of mutating offspring [0, 1].
	MutationRate float64

	// SurvivalRate is the fraction of top performers to keep [0, 1].
	SurvivalRate float64

	// EliteCount is the number of best individuals preserved unchanged.
	EliteCount int

	// TournamentSize is the number of competitors per tournament (when using tournament selection).
	TournamentSize int

	// SelectionStrategy is the current parent selection algorithm name.
	SelectionStrategy string

	// BreedingPoolRatio is the fraction of survivors eligible as parents [0, 1].
	BreedingPoolRatio float64
}

// MetaConfig controls the behavior of the meta-controller.
type MetaConfig struct {
	// Enabled enables self-adaptation of evolution parameters.
	Enabled bool

	// AdjustmentRate controls how aggressively parameters are tuned [0, 1].
	// Higher values = faster adaptation but less stability. Default 0.1.
	AdjustmentRate float64

	// TargetDiversity is the ideal population diversity level [0, 1].
	// The controller adjusts mutation rate to maintain this target. Default 0.3.
	TargetDiversity float64

	// MinImprovementRate is the minimum acceptable ratio of generations
	// that should show improvement before adjusting other params.
	MinImprovementRate float64

	// ExplorationBias biases the system toward exploration [0, 1].
	// 0 = pure exploitation, 1 = pure exploration. Default 0.5.
	ExplorationBias float64
}

// DefaultMetaConfig returns sensible defaults for meta-evolution.
func DefaultMetaConfig() MetaConfig {
	return MetaConfig{
		Enabled:            true, // Control loop active by default
		AdjustmentRate:     0.1,
		TargetDiversity:    0.3,
		MinImprovementRate: 0.3,
		ExplorationBias:    0.5,
	}
}

// MetaDecision records a single meta-controller decision for reporting.
type MetaDecision struct {
	// Generation when the decision was made.
	Generation int `json:"generation"`

	// Action describes what was changed (e.g., "mutation_rate", "selection_strategy").
	Action string `json:"action"`

	// Reason explains why the change was made.
	Reason string `json:"reason"`

	// OldValue is the parameter value before the change.
	OldValue string `json:"old_value"`

	// NewValue is the parameter value after the change.
	NewValue string `json:"new_value"`
}

// MetaController adjusts PopulationConfig parameters based on observed
// evolution metrics (diversity, score improvement, stagnation, etc.).
// This enables the system to self-tune its own evolution hyperparameters.
type MetaController struct {
	cfg        MetaConfig
	generation int

	// DecisionHistory records all meta-controller decisions for reporting.
	DecisionHistory []MetaDecision
}

// NewMetaController creates a meta-controller for self-adapting evolution.
func NewMetaController(cfg MetaConfig) *MetaController {
	return &MetaController{cfg: cfg}
}

// Tune adjusts the population config based on current evolution state.
// Returns true if any parameter was modified.
func (mc *MetaController) Tune(popCfg *PopulationConfig, report DiversityReport, scoreImprovement float64, stagnantGens int) bool {
	if !mc.cfg.Enabled {
		return false
	}

	mc.generation++
	modified := false
	ar := mc.cfg.AdjustmentRate
	exp := mc.cfg.ExplorationBias

	// 1. Adjust mutation rate based on diversity gap.
	diversityGap := report.Overall - mc.cfg.TargetDiversity
	if math.Abs(diversityGap) > 0.05 {
		oldRate := popCfg.MutationRate
		if diversityGap < 0 {
			// Below target: increase mutation rate to boost exploration.
			delta := ar * (1 + exp) * (-diversityGap)
			popCfg.MutationRate = mutation.Clamp(
				popCfg.MutationRate*(1+delta),
				popCfg.MinMutationRate,
				popCfg.MaxMutationRate,
			)
		} else {
			// Above target: slightly reduce mutation rate.
			popCfg.MutationRate = mutation.Clamp(
				popCfg.MutationRate*(1-ar*diversityGap),
				popCfg.MinMutationRate,
				popCfg.MaxMutationRate,
			)
		}
		if oldRate != popCfg.MutationRate {
			modified = true
			mc.recordDecision("mutation_rate", "diversity gap", fmt.Sprintf("%.4f", oldRate), fmt.Sprintf("%.4f", popCfg.MutationRate))
		}
	}

	// 2. Adjust survival rate based on score improvement trend.
	if scoreImprovement < mc.cfg.MinImprovementRate {
		// Low improvement: be more selective (lower survival rate).
		oldRate := popCfg.SurvivalRate
		newRate := popCfg.SurvivalRate * (1 - ar)
		if newRate >= 0.2 {
			popCfg.SurvivalRate = newRate
			modified = true
			mc.recordDecision("survival_rate", "low improvement", fmt.Sprintf("%.4f", oldRate), fmt.Sprintf("%.4f", popCfg.SurvivalRate))
		}
	} else if scoreImprovement > mc.cfg.MinImprovementRate*2 {
		// High improvement: be more generous (higher survival rate).
		oldRate := popCfg.SurvivalRate
		newRate := popCfg.SurvivalRate * (1 + ar)
		if newRate <= 0.9 {
			popCfg.SurvivalRate = newRate
			modified = true
			mc.recordDecision("survival_rate", "high improvement", fmt.Sprintf("%.4f", oldRate), fmt.Sprintf("%.4f", popCfg.SurvivalRate))
		}
	}

	// 3. Adjust elite count based on stagnation.
	if stagnantGens > 3 {
		// Stagnating: reduce elite count to allow more exploration.
		if popCfg.EliteCount > 1 {
			oldCount := popCfg.EliteCount
			popCfg.EliteCount--
			modified = true
			mc.recordDecision("elite_count", "stagnation", fmt.Sprintf("%d", oldCount), fmt.Sprintf("%d", popCfg.EliteCount))
		}
	} else if stagnantGens == 0 && popCfg.EliteCount < 5 {
		// Improving: increase elite preservation.
		if popCfg.EliteCount < popCfg.Size/4 {
			oldCount := popCfg.EliteCount
			popCfg.EliteCount++
			modified = true
			mc.recordDecision("elite_count", "improving", fmt.Sprintf("%d", oldCount), fmt.Sprintf("%d", popCfg.EliteCount))
		}
	}

	// 4. Switch selection strategy based on diversity and improvement trends.
	// Check more frequently: every 5 generations or when conditions change.
	if mc.generation%5 == 0 || mc.generation == 1 {
		oldStrategy := popCfg.SelectionStrategy
		newStrategy := mc.selectBestStrategy(popCfg, report, scoreImprovement, stagnantGens)
		if newStrategy != oldStrategy {
			popCfg.SelectionStrategy = newStrategy
			modified = true
			reason := mc.buildStrategyReason(report, scoreImprovement, stagnantGens)
			mc.recordDecision("selection_strategy", reason, oldStrategy, newStrategy)
		}
	}

	if modified {
		el.DebugContext(context.Background(), "meta-evolution: adjusted parameters",
			"mutation_rate", popCfg.MutationRate,
			"survival_rate", popCfg.SurvivalRate,
			"elite_count", popCfg.EliteCount,
			"selection", popCfg.SelectionStrategy,
			"generation", mc.generation,
		)
	}

	return modified
}

// buildStrategyReason constructs a human-readable reason for a strategy change.
func (mc *MetaController) buildStrategyReason(report DiversityReport, scoreImprovement float64, stagnantGens int) string {
	var reasons []string
	if report.Lineage < 0.3 {
		reasons = append(reasons, "low_lineage_diversity")
	}
	if report.Overall < 0.15 {
		reasons = append(reasons, "low_overall_diversity")
	}
	if scoreImprovement > 0.5 {
		reasons = append(reasons, "high_improvement")
	}
	if stagnantGens > 5 {
		reasons = append(reasons, "persistent_stagnation")
	}
	if len(reasons) == 0 {
		return "balanced"
	}
	return reasons[0]
}

// recordDecision stores a meta-controller decision in the decision history.
func (mc *MetaController) recordDecision(action, reason, oldValue, newValue string) {
	mc.DecisionHistory = append(mc.DecisionHistory, MetaDecision{
		Generation: mc.generation,
		Action:     action,
		Reason:     reason,
		OldValue:   oldValue,
		NewValue:   newValue,
	})
}

// selectBestStrategy chooses the best selection strategy based on current state.
// Considers diversity metrics, score improvement, and stagnation to pick
// the most appropriate selection strategy for the current conditions.
func (mc *MetaController) selectBestStrategy(popCfg *PopulationConfig, report DiversityReport, scoreImprovement float64, stagnantGens int) string {
	exp := mc.cfg.ExplorationBias
	div := report.Overall
	lineageDiv := report.Lineage

	// Low lineage diversity: use lineage_rank to promote diverse parent selection.
	if lineageDiv < 0.3 && div < 0.3 {
		return "lineage_rank"
	}

	if div < 0.15 {
		// Low diversity: use rank selection (reduces pressure, allows more diversity).
		return "rank"
	}

	// High score improvement: use tournament for more pressure to exploit.
	if scoreImprovement > 0.5 && exp < 0.3 {
		return "tournament"
	}

	// Persistent stagnation: try rank for gentler pressure.
	if stagnantGens > 5 {
		return "rank"
	}

	if div > 0.5 && exp < 0.3 {
		// High diversity + low exploration bias: use tournament for pressure.
		return "tournament"
	}
	if exp > 0.7 {
		// High exploration: use SUS for uniform sampling.
		return "sus"
	}
	if exp > 0.4 {
		return "roulette"
	}
	return popCfg.SelectionStrategy // Keep current.
}

// ApplyMetaToPopulation applies the meta-tuned parameters to a running population.
// This is called during the evolution cycle to dynamically adjust parameters.
func ApplyMetaToPopulation(pop *Population, controller *MetaController) bool {
	if controller == nil || !controller.cfg.Enabled {
		return false
	}
	pop.mu.Lock()
	defer pop.mu.Unlock()

	// Need enough history entries for score improvement trend analysis.
	// Without history (HistoryMaxSize==0), pop.history will be empty and
	// scoreImprovement defaults to 0, which incorrectly triggers survival
	// rate degradation in Tune().
	if pop.Generation < 3 || len(pop.history) < 3 {
		return false
	}

	report := pop.measureDiversityReportLocked()

	// Calculate score improvement rate over last few generations.
	recent := pop.history[len(pop.history)-3:]
	improvements := 0
	for i := 1; i < len(recent); i++ {
		if recent[i].BestScore > recent[i-1].BestScore {
			improvements++
		}
	}
	scoreImprovement := float64(improvements) / float64(len(recent)-1)

	modified := controller.Tune(&pop.cfg, report, scoreImprovement, pop.stagnantGens)

	// Sync runtime field from tuned config.
	if modified {
		pop.currentMutationRate = pop.cfg.MutationRate
	}

	return modified
}
