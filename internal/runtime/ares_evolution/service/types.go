// Package evolution provides a high-level API for autonomous genetic algorithm
// based strategy evolution. External users can import this single package to
// create, configure, and run evolution systems without depending on internal packages.
package evolution

import (
	"context"
	"time"
)

// ──────────────────────────────────────────────
// Provider interfaces and supporting types
// ──────────────────────────────────────────────

// EvolutionHint represents a distilled guidance hint derived from past
// experiences. It captures what worked, what failed, and what parameter
// biases should be applied during strategy mutation.
type EvolutionHint struct {
	// ID is the unique identifier of this hint.
	ID string

	// TaskType is the type of task this hint applies to.
	TaskType string

	// Problem is the abstract problem statement.
	Problem string

	// Solution is the concise solution approach that worked.
	Solution string

	// Constraints are important constraints or context for the solution.
	Constraints []string

	// FailedPatterns are patterns that led to failure and should be avoided.
	FailedPatterns []string

	// PreferredTools are tool configurations that worked well.
	PreferredTools []string

	// PromptSnippets are prompt text snippets that contributed to success.
	PromptSnippets []string

	// ParamHints maps parameter names to suggested values.
	ParamHints map[string]float64

	// Confidence is the confidence score for this hint (0.0 to 1.0).
	Confidence float64

	// SourceExperienceIDs are the IDs of the experiences that produced this hint.
	SourceExperienceIDs []string
}

// StrategyOutcome records the result of deploying a strategy mutation,
// enabling the experience provider to learn from real execution outcomes.
type StrategyOutcome struct {
	// StrategyID is the ID of the strategy that was deployed.
	StrategyID string

	// TaskType is the type of task this strategy was used for.
	TaskType string

	// Success indicates whether the strategy deployment was successful.
	Success bool

	// Score is the fitness score achieved by this strategy.
	Score float64

	// Cost is the computational cost incurred.
	Cost float64

	// LatencyMs is the execution latency in milliseconds.
	LatencyMs int64

	// MutationType describes what kind of mutation produced this strategy.
	MutationType string

	// ExperienceIDs are the IDs of experiences that influenced this strategy.
	ExperienceIDs []string

	// Timestamp is when this outcome was recorded.
	Timestamp time.Time
}

// GuidanceProvider provides evolution hints for guided mutation.
// Implementations should return relevant hints for a given task type,
// or an empty slice when no hints are available. This interface is
// structurally identical to the internal GuidanceProvider so that
// adapters in service.go can convert between them.
type GuidanceProvider interface {
	// HintsForTask returns evolution hints relevant to the given task type.
	// Returns up to limit hints, ordered by relevance.
	// An empty slice with nil error means no hints are available.
	HintsForTask(ctx context.Context, taskType string, limit int) ([]EvolutionHint, error)

	// RecordStrategyOutcome persists a strategy outcome for future learning.
	RecordStrategyOutcome(ctx context.Context, outcome StrategyOutcome) error
}

// EvidenceAggregator provides aggregated evidence for a strategy,
// used by the post-evolution promotion/report hook.
type EvidenceAggregator interface {
	// Aggregate returns aggregated multi-dimensional evidence for a strategy.
	Aggregate(ctx context.Context, strategyID string) (Evidence, error)
}

// PromotionLogic evaluates strategies for champion/candidate/demotion
// decisions after each evolution generation.
type PromotionLogic interface {
	// Evaluate checks whether a strategy should be promoted, demoted,
	// or kept at the current level based on its evidence.
	// Returns the suggested state, a human-readable reason, and any error.
	Evaluate(ctx context.Context, strategyID string, evidence Evidence) (state string, reason string, err error)
}

// Evidence holds aggregated statistics for a strategy, used by the
// post-evolution promotion/report hook.
type Evidence struct {
	StrategyID  string
	SuccessRate float64
	LatencyP50  int64
	ErrorRate   float64
	SampleCount int64
	Confidence  float64
}

// MemoryExperienceProvider provides access to past experiences for
// memory-aware scoring. Implementations may query a vector database,
// keyword index, or other experience store.
type MemoryExperienceProvider interface {
	// FindSimilar returns the count of similar experiences for the given
	// task type along with a confidence factor (0-1) indicating how well
	// the matched experiences align with the current context.
	FindSimilar(ctx context.Context, taskType string, limit int) (int, float64, error)
}

// GuardrailConfig configures safety checks for the evolution system.
// When enabled, guardrails run before and after each evolution cycle
// to detect dangerous conditions (stagnation, regression, lineage
// concentration, etc.).
type GuardrailConfig struct {
	// Enabled enables guardrail checks.
	Enabled bool

	// BaselineScore is the minimum acceptable strategy score.
	BaselineScore float64

	// MaxStagnantGenerations triggers a warning when best score plateaus
	// for this many consecutive generations (default 10).
	MaxStagnantGenerations int

	// MaxLineageShare is the maximum fraction [0-1] of the population
	// that can belong to a single lineage (default 0.8).
	MaxLineageShare float64

	// KnownTools is the registered tool vocabulary used to reject an evolved
	// tool whitelist that names tools which do not exist. Empty disables the
	// check. Supplying it matters because an all-unknown whitelist does not
	// narrow the tool set at runtime — it intersects to zero and the executors
	// fall back to the FULL set, so the strategy silently becomes the broadest
	// one instead of the narrowest.
	KnownTools []string
}

// MemoryAwareScoringConfig configures memory-aware scoring that adjusts
// fitness scores based on historical evidence from past experiences.
type MemoryAwareScoringConfig struct {
	// Enabled enables memory-aware scoring adjustments.
	Enabled bool

	// MemoryWeight controls the contribution of memory evidence bonus (default 0.2).
	MemoryWeight float64

	// CostWeight controls the penalty multiplier for strategy cost (default 0.1).
	CostWeight float64

	// LatencyWeight controls the penalty multiplier for latency in seconds (default 0.05).
	LatencyWeight float64

	// RegressionWeight controls the penalty for score regression vs baseline (default 0.1).
	RegressionWeight float64

	// MinEvidenceBonus is the minimum memory evidence bonus (default 0.0).
	MinEvidenceBonus float64

	// MaxEvidenceBonus is the maximum memory evidence bonus (default 20.0).
	MaxEvidenceBonus float64

	// ExperienceLookupLimit is the max similar experiences to retrieve per call (default 10).
	ExperienceLookupLimit int
}

// ──────────────────────────────────────────────
// Core types
// ──────────────────────────────────────────────

// Strategy represents an evolved agent decision strategy.
type Strategy struct {
	// ID is the unique identifier of this strategy.
	ID string `json:"id"`

	// Name is the human-readable name of the strategy.
	Name string `json:"name,omitempty"`

	// Version is the version number of this strategy (monotonically increasing).
	Version int `json:"version"`

	// Params holds the configurable parameters of the strategy.
	Params map[string]any `json:"params,omitempty"`

	// ParentID references the parent strategy this was evolved from (empty for root strategies).
	ParentID string `json:"parent_id,omitempty"`

	// PromptTemplate is the behavior prompt template for the agent.
	PromptTemplate string `json:"prompt_template,omitempty"`

	// MutationType records the mutation type that created this strategy.
	MutationType string `json:"mutation_type"`

	// EvidenceKey is the stable evidence key derived from strategy parameters,
	// used to trace this strategy back to real execution evidence.
	EvidenceKey string `json:"evidence_key,omitempty"`

	// Score is the current evaluation score (-1 = unevaluated).
	Score float64 `json:"score"`

	// DimensionScores holds per-dimension fitness scores for multi-objective
	// optimization. When non-nil, Score is typically a weighted aggregate.
	// Nil means single-objective mode.
	DimensionScores map[string]float64 `json:"dimension_scores,omitempty"`

	// CreatedAt is the timestamp when this strategy was created.
	CreatedAt time.Time `json:"created_at"`
}

// StrategyLineage records parent-child relationships in evolution history.
type StrategyLineage struct {
	// ParentID is the ID of the parent (source) strategy.
	ParentID string `json:"parent_id"`

	// ChildID is the ID of the new (mutated) strategy.
	ChildID string `json:"child_id"`

	// MutationType describes what kind of mutation was applied.
	MutationType string `json:"mutation_type"`

	// WinRate achieved by the child strategy in arena testing.
	WinRate float64 `json:"win_rate"`

	// ScoreDelta is the delta between child and parent scores.
	ScoreDelta float64 `json:"score_delta"`

	// ParentScore is the evaluated score of the parent strategy.
	ParentScore float64 `json:"parent_score"`

	// ChildScore is the evaluated score of the child strategy.
	ChildScore float64 `json:"child_score"`

	// ImprovementSignificant indicates whether the score improvement
	// meets the configured significance threshold (MinLineageImprovement).
	ImprovementSignificant bool `json:"improvement_significant"`

	// Timestamp when this lineage record was created.
	Timestamp int64 `json:"timestamp"`
}

// ScorerFunc evaluates an agent strategy and returns its fitness score.
// Higher scores are better. The score range is [0, 100].
type ScorerFunc func(agent *Strategy) float64

// BatchScorer is an optional interface that ScorerFunc implementations can
// satisfy to score multiple strategies in a single API call. When the scorer
// implements this interface, scoreAgents uses batch scoring instead of
// per-agent calls — reducing API calls from N to 1 per generation.
type BatchScorer interface {
	// BatchScore evaluates all strategies at once and returns scores in the
	// same order as the input slice.
	BatchScore(strategies []*Strategy) []float64
}

// LLMClient defines the interface for LLM-based scoring.
// Implementations can wrap internal/llm.Client or provide mock implementations.
type LLMClient interface {
	Generate(ctx context.Context, prompt string) (string, error)
}

// SystemConfig holds configuration for creating an EvolutionSystem via NewService.
type SystemConfig struct {
	// BaseStrategy is the root strategy to evolve from.
	BaseStrategy *Strategy

	// PopulationSize is the number of agents per generation (default 20).
	PopulationSize int

	// EliteCount is how many top strategies to preserve unchanged (default 3).
	EliteCount int

	// SurvivalRate is fraction of top performers to keep [0-1] (default 0.6).
	SurvivalRate float64

	// MutationRate is probability of mutating each offspring [0-1] (default 0.2).
	MutationRate float64

	// MinMutationRate is the floor for adaptive mutation rate (default 0.05).
	MinMutationRate float64

	// MaxMutationRate is the ceiling for adaptive mutation rate (default 0.5).
	MaxMutationRate float64

	// MaxStagnantGenerations triggers bottom-1/3 reset when best score plateaus (default 10).
	MaxStagnantGenerations int

	// DiversityThreshold below which mutation rate is boosted (default 0.15).
	DiversityThreshold float64

	// BreedingPoolRatio limits breeding to the top fraction of survivors (default 0.6).
	BreedingPoolRatio float64

	// SelectionStrategy selects the parent selection algorithm.
	// Supported values: "tournament", "rank", "sus", "roulette", "truncation", "random", "".
	// Empty or "random" = random parent selection (backward compatible).
	// Default: "" (random).
	SelectionStrategy string

	// HistoryMaxSize enables generation history tracking when > 0.
	// History is required for meta-controller tuning and score improvement analysis.
	// Default: 0 (disabled).
	HistoryMaxSize int

	// PromptCrossoverMode controls how PromptTemplate is combined during crossover.
	// 0 = inherit from higher-scoring parent (default), 1 = half-sentence split,
	// 2 = random parent pick (uniform).
	PromptCrossoverMode int

	// Generations is total generations to run (default 15).
	Generations int

	// Seed makes evolution deterministic when > 0 (default 0 = random).
	Seed int64

	// UseDeterministicIDs enables counter-based strategy IDs (default: true when Seed != 0).
	UseDeterministicIDs *bool

	// PromptPool is candidate prompt templates for mutation.
	PromptPool []string

	// EnableWiredMode uses WiredEvolutionSystem (full wiring) vs raw Population.
	EnableWiredMode bool

	// Scorer evaluates agent fitness. When nil, a temperature-proximity scorer is used.
	Scorer ScorerFunc

	// BatchScorer is an optional interface for scoring all agents in a single
	// API call. When set, scoreAgents uses batch scoring instead of per-agent
	// calls — reducing API calls from N to 1 per generation. Takes priority
	// over Scorer when both are set.
	BatchScorer BatchScorer

	// Guardrails configures safety checks (pre/post evolution). When
	// GuardrailConfig.Enabled is true, guardrails are constructed in
	// createWiredSystem and wired into the internal system. Nil (default)
	// means guardrails are disabled.
	Guardrails *GuardrailConfig

	// GuidanceProvider provides evolution hints for guided mutation. When
	// non-nil AND EnableExperienceGuidedMutation is true, the mutator is
	// wrapped with an ExperienceGuidedMutator that biases mutation decisions
	// using past experience data.
	GuidanceProvider GuidanceProvider

	// EnableExperienceGuidedMutation enables experience-guided mutation when
	// true AND GuidanceProvider is non-nil. When hints are available,
	// the mutator biases its decisions toward patterns that worked in the past.
	EnableExperienceGuidedMutation bool

	// EnableLLMHints enables automatic outcome recording and LLM-based hint
	// generation for the DreamCycle evolution path. When true AND LLMClient
	// is set, an LLMHintProvider is automatically constructed and wired into
	// the DreamCycle for recording strategy outcomes and generating
	// mutation hints from past experiences via LLM reflection.
	EnableLLMHints bool

	// MaxHintHistory limits the number of strategy outcomes retained for
	// LLM-based hint generation (default 10). Only applies when
	// EnableLLMHints is true.
	MaxHintHistory int

	// LLMClient provides the LLM interface for hint generation and other
	// LLM-dependent features. When EnableLLMHints is true, this client is
	// used to automatically construct an LLMHintProvider that generates
	// mutation hints from past strategy outcomes.
	LLMClient LLMClient

	// EnableIntelligence enables the Phase 3-5 intelligence pipeline:
	// reflection → hypothesis generation → meta-controller tuning.
	// When true AND LLMClient is set, an LLMReflector is wired into the
	// evolution cycle to analyze patterns, generate hypotheses, and
	// self-tune hyperparameters. Requires HistoryMaxSize > 0.
	EnableIntelligence bool

	// MemoryExperienceProvider provides access to past experiences for
	// memory-aware scoring. When non-nil AND MemoryAwareScoringConfig.Enabled
	// is true, the scorer adjusts fitness scores based on historical evidence.
	MemoryExperienceProvider MemoryExperienceProvider

	// MemoryAwareScoringConfig configures memory-aware scoring. When
	// MemoryAwareScoringConfig.Enabled is true and MemoryExperienceProvider
	// is non-nil, the tiered scorer wraps with memory adjustments.
	MemoryAwareScoringConfig MemoryAwareScoringConfig

	// EvidenceAggregator provides aggregated evidence for post-evolution
	// promotion evaluation and reporting. Accepts either:
	//   - experience.EvidenceAggregator (internal)
	//   - evolution.EvidenceAggregator (local interface)
	// When non-nil, the AfterGeneration hook uses it to produce evidence
	// for the best strategy each generation.
	EvidenceAggregator interface{}

	// PromotionLogic evaluates strategies for promotion after each evolution
	// generation. Accepts either:
	//   - promotion.PromotionLogic (internal)
	//   - evolution.PromotionLogic (local interface)
	// When non-nil AND EvidenceAggregator is also set, the AfterGeneration
	// hook calls Evaluate on the best strategy each round and logs the result.
	PromotionLogic interface{}

	// ReportPath is the optional path to write the evolution report after
	// RunIdleEvolution completes. When non-empty, a human-readable report
	// is saved to this file. Parent directories are created automatically.
	// Example: "var/evolution/report.txt"
	ReportPath string

	// MinLineageImprovement is the minimum absolute score delta required
	// for a lineage improvement to be considered significant (default 0.01).
	// Deltas below this threshold are treated as noise (scorer variance).
	MinLineageImprovement float64
}

// DefaultConfig returns a sensible default configuration for the evolution system.
//
// Returns:
//
//	*SystemConfig - configuration with default values applied.
func DefaultConfig() *SystemConfig {
	return &SystemConfig{
		PopulationSize:         20,
		EliteCount:             3,
		SurvivalRate:           0.6,
		MutationRate:           0.2,
		MinMutationRate:        0.05,
		MaxMutationRate:        0.5,
		MaxStagnantGenerations: 10,
		DiversityThreshold:     0.15,
		BreedingPoolRatio:      0.6,
		Generations:            15,
		Seed:                   0,
		PromptPool:             []string{},
		EnableWiredMode:        true,
		MinLineageImprovement:  0.01,
	}
}

// DiversityReporter is the interface for population diversity information.
// It mirrors genome.DiversityReport without importing the genome package.
type DiversityReporter struct {
	// Overall is the weighted average of all diversity components [0, 1].
	Overall float64 `json:"overall"`

	// Numeric is the average pairwise normalized parameter distance [0, 1].
	Numeric float64 `json:"numeric"`

	// Categorical measures differences in prompt templates, tool configs, etc. [0, 1].
	Categorical float64 `json:"categorical"`

	// Lineage measures parent ID concentration; 1.0 = all different parents, 0.0 = same.
	Lineage float64 `json:"lineage"`

	// DominantLineageShare is the fraction sharing the most common ParentID [0, 1].
	DominantLineageShare float64 `json:"dominant_lineage_share"`
}

// Stats holds population statistics after each evolution generation.
type Stats struct {
	// Generation is the current generation number.
	Generation int

	// Size is the number of agents in the population.
	Size int

	// BestScore is the highest score among all agents.
	BestScore float64

	// AvgScore is the average score across all agents.
	AvgScore float64

	// WorstScore is the lowest score among all agents.
	WorstScore float64

	// Diversity is the population diversity breakdown, if computed.
	Diversity *DiversityReporter `json:"diversity,omitempty"`
}

// EvolutionResult holds the result of a complete evolution run.
type EvolutionResult struct {
	// BestStrategy is the best strategy found across all generations.
	BestStrategy *Strategy

	// Stats contains per-generation statistics collected during evolution.
	Stats []Stats

	// Lineages contains recorded parent-child relationships from evolution.
	Lineages []StrategyLineage

	// TotalGens is the total number of generations that were executed.
	TotalGens int
}
