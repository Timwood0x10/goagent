// Package evolution provides data-driven evolution reports derived from
// real run statistics, not template claims.
package evolution

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
)

// GenerationStats holds per-generation statistics collected during evolution.
type GenerationStats struct {
	Generation     int            `json:"generation"`
	PopulationSize int            `json:"population_size"`
	BestScore      float64        `json:"best_score"`
	AvgScore       float64        `json:"avg_score"`
	WorstScore     float64        `json:"worst_score"`
	Diversity      float64        `json:"diversity"`      // overall diversity metric
	NumDiverse     int            `json:"num_diverse"`    // count of diverse lineages
	MutationTypes  map[string]int `json:"mutation_types"` // mutation type distribution

	// RecoveryActions records diversity recovery actions taken this generation.
	// Keys are action names (e.g., "mutation_rate_boost", "fresh_injection"),
	// values are counts. Populated from population generation history.
	RecoveryActions map[string]int `json:"recovery_actions,omitempty"`
}

// EvolutionReport is a comprehensive data-driven report for an evolution run.
type EvolutionReport struct {
	// TotalGenerations is the total number of generations evolved.
	TotalGenerations int `json:"total_generations"`

	// BestEverScore is the highest score seen across all generations.
	BestEverScore float64 `json:"best_ever_score"`

	// BestEverGeneration is the generation where best-ever score appeared.
	BestEverGeneration int `json:"best_ever_generation"`

	// FinalBestScore is the best score in the final generation.
	FinalBestScore float64 `json:"final_best_score"`

	// GenerationTrajectory is per-generation stats in order.
	// When population history is enabled (WithHistoryEnabled), this contains
	// one entry per recorded generation. Otherwise, only the current generation
	// is included.
	GenerationTrajectory []GenerationStats `json:"generation_trajectory"`

	// ScorerCostSummary holds scoring cost information (if tiered scorer used).
	ScorerCostSummary *ScorerCostSummary `json:"scorer_cost_summary,omitempty"`

	// LineageConcentration tracks dominant lineage share (if available).
	LineageConcentration *LineageConcentration `json:"lineage_concentration,omitempty"`

	// WinnerStrategyID is the ID of the best strategy at run completion.
	WinnerStrategyID string `json:"winner_strategy_id,omitempty"`
	// WinnerScore is the final score of the best strategy.
	WinnerScore float64 `json:"winner_score,omitempty"`
	// PromotionState is the state of the winner (candidate/shadow/champion/demoted).
	PromotionState string `json:"promotion_state,omitempty"`
	// PromotionReason explains the promotion decision.
	PromotionReason string `json:"promotion_reason,omitempty"`
	// SuccessRate from evidence aggregation.
	SuccessRate float64 `json:"success_rate,omitempty"`
	// SampleCount from evidence aggregation.
	SampleCount int64 `json:"sample_count,omitempty"`
	// Confidence from evidence aggregation.
	Confidence float64 `json:"confidence,omitempty"`

	// MetaDecisionHistory records meta-controller decisions during the run.
	// Each entry documents a tuning action, its reason, and parameter change.
	MetaDecisionHistory []MetaDecision `json:"meta_decision_history,omitempty"`
}

// ScorerCostSummary summarizes scorer resource usage.
type ScorerCostSummary struct {
	TotalLLMCalls  int `json:"total_llm_calls"`
	TotalCacheHits int `json:"total_cache_hits"`
	TotalFallbacks int `json:"total_fallbacks"`
	LLMBudgetUsed  int `json:"llm_budget_used"`
	LLMBudgetMax   int `json:"llm_budget_max"`
}

// LineageConcentration tracks lineage distribution in the population.
type LineageConcentration struct {
	TopLineageShare float64        `json:"top_lineage_share"`
	TopLineageID    string         `json:"top_lineage_id"`
	LineageCounts   map[string]int `json:"lineage_counts"`
	UniqueLineages  int            `json:"unique_lineages"`
}

// MetaDecision records a single meta-controller decision during the evolution run.
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

// ReportOption configures GenerateReport behavior.
type ReportOption func(*reportConfig)

// reportConfig holds optional configuration for report generation.
type reportConfig struct {
	scoringStats map[string]int64
	budgetUsage  []int // [used, max, cacheHits, fallbacks]
}

// WithScoringStats injects scorer cost data into the report.
//
// Args:
//
//	stats - tiered scorer Stats() output (map[string]int64).
//	budgetUsage - budget.Usage() output as variadic: used, max, cacheHits, fallbacks.
//
// Returns:
//
//	ReportOption - the configuration function.
func WithScoringStats(stats map[string]int64, budgetUsage ...int) ReportOption {
	return func(rc *reportConfig) {
		rc.scoringStats = stats
		if len(budgetUsage) >= 4 {
			rc.budgetUsage = budgetUsage[:4]
		}
	}
}

// ErrNilSystem is returned when a nil WiredEvolutionSystem is passed to GenerateReport.
var ErrNilSystem = errors.New("system must not be nil")

// GenerateReport builds a data-driven evolution report from a wired system.
// It collects real statistics from the population, genealogy, and optionally
// the scoring infrastructure. Every claim in the report is backed by stored metrics.
//
// Args:
//
//	ctx - operation context.
//	system - the wired evolution system to report on (must not be nil).
//	opts - optional report configuration (e.g., WithScoringStats for scorer cost data).
//
// Returns:
//
//	*EvolutionReport - the generated report (never nil if error is nil).
//	error - non-nil if system is nil.
func GenerateReport(ctx context.Context, system *WiredEvolutionSystem, opts ...ReportOption) (*EvolutionReport, error) {
	if system == nil {
		return nil, fmt.Errorf("generate report: %w", ErrNilSystem)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	cfg := &reportConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	report := &EvolutionReport{}

	// Collect population statistics.
	pop := system.Population
	if pop != nil {
		report.TotalGenerations = pop.Generation

		// Current generation stats.
		popStats := pop.Stats()
		report.FinalBestScore = popStats.BestScore

		// Build generation trajectory from history if available.
		if hist := pop.History(); len(hist) > 0 {
			report.GenerationTrajectory = make([]GenerationStats, len(hist))
			for i, entry := range hist {
				report.GenerationTrajectory[i] = historyEntryToGenerationStats(entry, pop)
			}
		} else {
			// Fallback: just current generation when history is not enabled.
			report.GenerationTrajectory = []GenerationStats{
				buildGenerationStats(pop, popStats),
			}
		}

		// Best ever strategy.
		best := pop.BestStrategy()
		if best != nil {
			report.BestEverScore = best.Score
			report.BestEverGeneration = pop.BestEverGeneration()
		}

		// Diversity / lineage concentration from population snapshot.
		// Computed regardless of the Overall diversity score: a 0.0 diversity
		// metric still carries valid lineage data that must not be silently
		// omitted from the report.
		lineageMap := makeLineageCounts(pop)
		topID, topShare := "", 0.0
		if len(lineageMap) > 0 {
			maxCount := 0
			for id, count := range lineageMap {
				if count > maxCount {
					maxCount = count
					topID = id
				}
			}
			total := 0
			for _, count := range lineageMap {
				total += count
			}
			if total > 0 {
				topShare = float64(maxCount) / float64(total)
			}
		}
		report.LineageConcentration = &LineageConcentration{
			TopLineageShare: topShare,
			TopLineageID:    topID,
			LineageCounts:   lineageMap,
			UniqueLineages:  len(lineageMap),
		}
	}

	// Inject scorer cost summary if provided.
	if cfg.scoringStats != nil && len(cfg.budgetUsage) >= 2 {
		cacheHits := 0
		fallbacks := 0
		if len(cfg.budgetUsage) >= 4 {
			cacheHits = cfg.budgetUsage[2]
			fallbacks = cfg.budgetUsage[3]
		}
		report.ScorerCostSummary = &ScorerCostSummary{
			TotalLLMCalls:  int(cfg.scoringStats["llm_calls"]),
			TotalCacheHits: cacheHits,
			TotalFallbacks: fallbacks,
			LLMBudgetUsed:  cfg.budgetUsage[0],
			LLMBudgetMax:   cfg.budgetUsage[1],
		}
	}

	return report, nil
}

// buildGenerationStats constructs GenerationStats from population data.
//
// Args:
//
//	pop - the genome population.
//	stats - pre-computed PopulationStats.
//
// Returns:
//
//	GenerationStats - populated stats for the current generation.
func buildGenerationStats(pop *genome.Population, stats *genome.PopulationStats) GenerationStats {
	gs := GenerationStats{
		Generation:     stats.Generation,
		PopulationSize: stats.Size,
		BestScore:      stats.BestScore,
		AvgScore:       stats.AvgScore,
		WorstScore:     stats.WorstScore,
		Diversity:      stats.Diversity.Overall,
		MutationTypes:  make(map[string]int),
	}

	// Count mutation types from current agents.
	agents, _ := pop.Snapshot()
	for _, agent := range agents {
		mt := agent.StrategyMutationType.String()
		if mt == "" {
			mt = "unknown"
		}
		gs.MutationTypes[mt]++
	}

	// Count diverse lineages (agents with distinct ParentIDs).
	parentSet := make(map[string]struct{})
	for _, agent := range agents {
		if agent.ParentID != "" {
			parentSet[agent.ParentID] = struct{}{}
		}
	}
	gs.NumDiverse = len(parentSet)

	return gs
}

// historyEntryToGenerationStats converts a GenerationHistoryEntry to GenerationStats.
// Each history entry carries its own per-generation MutationTypes and NumDiverse,
// so no approximation from the current population is needed.
func historyEntryToGenerationStats(entry genome.GenerationHistoryEntry, pop *genome.Population) GenerationStats {
	gs := GenerationStats{
		Generation:      entry.Generation,
		PopulationSize:  entry.PopulationSize,
		BestScore:       entry.BestScore,
		AvgScore:        entry.AvgScore,
		WorstScore:      entry.WorstScore,
		Diversity:       entry.Diversity,
		MutationTypes:   entry.MutationTypes,
		NumDiverse:      entry.NumDiverse,
		RecoveryActions: entry.RecoveryActions,
	}

	// If the history entry lacks MutationTypes (e.g., recorded before field was added),
	// fall back to current population snapshot for backward compatibility.
	if gs.MutationTypes == nil {
		gs.MutationTypes = make(map[string]int)
		agents, _ := pop.Snapshot()
		for _, agent := range agents {
			mt := agent.StrategyMutationType.String()
			if mt == "" {
				mt = "unknown"
			}
			gs.MutationTypes[mt]++
		}

		parentSet := make(map[string]struct{})
		for _, agent := range agents {
			if agent.ParentID != "" {
				parentSet[agent.ParentID] = struct{}{}
			}
		}
		gs.NumDiverse = len(parentSet)
	}

	return gs
}

// makeLineageCounts returns a map of ParentID -> count for the current population.
// Agents with empty ParentID are excluded.
func makeLineageCounts(pop *genome.Population) map[string]int {
	agents, _ := pop.Snapshot()
	counts := make(map[string]int)
	for _, a := range agents {
		if a.ParentID != "" {
			counts[a.ParentID]++
		}
	}
	return counts
}

// ReportString returns a human-readable summary of the evolution report.
// Suitable for logging and CLI output.
//
// Args:
//
//	r - the evolution report (must not be nil).
//
// Returns:
//
//	string - formatted report text.
func ReportString(r *EvolutionReport) string {
	if r == nil {
		return "(nil report)"
	}

	var b strings.Builder

	b.WriteString("=== Evolution Report ===\n")
	fmt.Fprintf(&b, "Total Generations:    %d\n", r.TotalGenerations)
	fmt.Fprintf(&b, "Best Ever Score:      %.4f (gen %d)\n", r.BestEverScore, r.BestEverGeneration)
	fmt.Fprintf(&b, "Final Best Score:     %.4f\n", r.FinalBestScore)

	// Generation trajectory.
	for _, gs := range r.GenerationTrajectory {
		fmt.Fprintf(&b, "\n--- Generation %d ---\n", gs.Generation)
		fmt.Fprintf(&b, "  Generation:       %d\n", gs.Generation)
		fmt.Fprintf(&b, "  Population Size:  %d\n", gs.PopulationSize)
		fmt.Fprintf(&b, "  Best Score:       %.4f\n", gs.BestScore)
		fmt.Fprintf(&b, "  Avg Score:        %.4f\n", gs.AvgScore)
		fmt.Fprintf(&b, "  Worst Score:      %.4f\n", gs.WorstScore)
		fmt.Fprintf(&b, "  Diversity:        %.4f\n", gs.Diversity)
		fmt.Fprintf(&b, "  Diverse Lineages: %d\n", gs.NumDiverse)

		if len(gs.MutationTypes) > 0 {
			b.WriteString("  Mutation Types:\n")
			for mt, count := range gs.MutationTypes {
				fmt.Fprintf(&b, "    %s: %d\n", mt, count)
			}
		}

		if len(gs.RecoveryActions) > 0 {
			b.WriteString("  Recovery Actions:\n")
			for action, count := range gs.RecoveryActions {
				fmt.Fprintf(&b, "    %s: %d\n", action, count)
			}
		}
	}

	// Scorer cost summary.
	if r.ScorerCostSummary != nil {
		cs := r.ScorerCostSummary
		b.WriteString("\n--- Scorer Cost Summary ---\n")
		fmt.Fprintf(&b, "  LLM Calls:        %d / %d\n", cs.LLMBudgetUsed, cs.LLMBudgetMax)
		fmt.Fprintf(&b, "  Cache Hits:       %d\n", cs.TotalCacheHits)
		fmt.Fprintf(&b, "  Fallbacks:        %d\n", cs.TotalFallbacks)
	}

	// Lineage concentration.
	if r.LineageConcentration != nil {
		lc := r.LineageConcentration
		b.WriteString("\n--- Lineage Concentration ---\n")
		fmt.Fprintf(&b, "  Top Lineage Share: %.2f%%\n", lc.TopLineageShare*100)
		fmt.Fprintf(&b, "  Unique Lineages:   %d\n", lc.UniqueLineages)
	}

	// Promotion and evidence summary.
	if r.PromotionState != "" || r.WinnerStrategyID != "" {
		b.WriteString("\n--- Promotion Summary ---\n")
		if r.WinnerStrategyID != "" {
			fmt.Fprintf(&b, "  Winner:        %s\n", r.WinnerStrategyID)
		}
		if r.WinnerScore > 0 || r.WinnerStrategyID != "" {
			fmt.Fprintf(&b, "  Winner Score:  %.4f\n", r.WinnerScore)
		}
		if r.PromotionState != "" {
			fmt.Fprintf(&b, "  State:         %s\n", r.PromotionState)
		}
		if r.PromotionReason != "" {
			fmt.Fprintf(&b, "  Reason:        %s\n", r.PromotionReason)
		}
		if r.SampleCount > 0 {
			fmt.Fprintf(&b, "  Samples:       %d\n", r.SampleCount)
			fmt.Fprintf(&b, "  Success Rate:  %.2f%%\n", r.SuccessRate*100)
			fmt.Fprintf(&b, "  Confidence:    %.2f%%\n", r.Confidence*100)
		}
	}

	// Meta-controller decision timeline.
	if len(r.MetaDecisionHistory) > 0 {
		b.WriteString("\n--- Meta-Controller Decision Timeline ---\n")
		for _, d := range r.MetaDecisionHistory {
			fmt.Fprintf(&b, "  Gen %d: %s (%s) %s -> %s\n",
				d.Generation, d.Action, d.Reason, d.OldValue, d.NewValue)
		}
	}

	b.WriteString("========================\n")
	return b.String()
}
