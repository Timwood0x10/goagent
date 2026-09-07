// Package genome provides population management for genetic algorithm evolution.
package genome

import (
	"context"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// GenerationHistoryEntry captures a per-generation snapshot for trajectory reporting.
type GenerationHistoryEntry struct {
	Generation     int
	PopulationSize int
	BestScore      float64
	AvgScore       float64
	WorstScore     float64
	Diversity      float64 // overall diversity metric

	NumericDiversity     float64
	CategoricalDiversity float64
	LineageDiversity     float64
	DominantLineageShare float64
	NumDiverse           int
	MutationTypes        map[string]int
	RecoveryActions      map[string]int
}

// DiversityWeightConfig holds relative weights for diversity metric components.
type DiversityWeightConfig struct {
	Numeric     float64 `json:"numeric"`
	Categorical float64 `json:"categorical"`
	Lineage     float64 `json:"lineage"`
}

func (w DiversityWeightConfig) normalize() DiversityWeightConfig {
	result := w
	if result.Numeric == 0 && result.Categorical == 0 && result.Lineage == 0 {
		result.Numeric = 0.4
		result.Categorical = 0.4
		result.Lineage = 0.2
		return result
	}
	if result.Numeric == 0 {
		result.Numeric = 0.4
	}
	if result.Categorical == 0 {
		result.Categorical = 0.4
	}
	if result.Lineage == 0 {
		result.Lineage = 0.2
	}
	total := result.Numeric + result.Categorical + result.Lineage
	if total > 0 && !approxEqual(total, 1.0) {
		result.Numeric /= total
		result.Categorical /= total
		result.Lineage /= total
	}
	return result
}

func approxEqual(a, b float64) bool {
	const eps = 1e-9
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < eps
}

const (
	FitnessSharingSigma       = 0.3
	FitnessNicheRadius        = 0.15
	FitnessSharingSampleLimit = 50
	FitnessSharingSampleSize  = 30
)

// MutatorInterface wraps mutation.Strategy mutation for the genome package.
type MutatorInterface interface {
	Mutate(ctx context.Context, parent *mutation.Strategy, n int) ([]*mutation.Strategy, error)
}

// GenerationCallback is called after each generation completes.
type GenerationCallback func(ctx context.Context, stats PopulationStats)

// FitnessCallback is called after each agent is scored.
type FitnessCallback func(ctx context.Context, agent *mutation.Strategy, fitness float64)

// EvolveCallbacks groups all optional lifecycle hooks for the evolution loop.
// All fields are optional — nil callbacks are silently skipped.
type EvolveCallbacks struct {
	OnGeneration GenerationCallback
	OnFitness    FitnessCallback
	OnMutation   FitnessCallback
	OnCrossover  FitnessCallback
}

// PopulationConfig holds configuration for creating a population.
type PopulationConfig struct {
	Size                        int                   `json:"size"`
	SurvivalRate                float64               `json:"survival_rate"`
	MutationRate                float64               `json:"mutation_rate"`
	EliteCount                  int                   `json:"elite_count"`
	BreedingPoolRatio           float64               `json:"breeding_pool_ratio"`
	Seed                        int64                 `json:"seed,omitempty"`
	MinMutationRate             float64               `json:"min_mutation_rate"`
	MaxMutationRate             float64               `json:"max_mutation_rate"`
	MaxStagnantGenerations      int                   `json:"max_stagnant_generations"`
	DiversityThreshold          float64               `json:"diversity_threshold"`
	SelectionStrategy           string                `json:"selection_strategy"`
	TournamentSize              int                   `json:"tournament_size"`
	DiversityWeights            DiversityWeightConfig `json:"diversity_weights"`
	DiversitySampleSize         int                   `json:"diversity_sample_size"`
	FitnessSharingSampleLimit   int                   `json:"fitness_sharing_sample_limit"`
	FitnessSharingSampleSize    int                   `json:"fitness_sharing_size"`
	SpatialIndexThreshold       int                   `json:"spatial_index_threshold"`
	HistoryMaxSize              int                   `json:"history_max_size"`
	PerLineageElites            bool                  `json:"per_lineage_elites"`
	PerLineageEliteCount        int                   `json:"per_lineage_elite_count"`
	AdaptiveConfig              *AdaptiveConfig       `json:"adaptive_config,omitempty"`
	DisablePromptDiversityGuard bool                  `json:"disable_prompt_diversity_guard,omitempty"`
	AgentMaxAge                 int                   `json:"agent_max_age"`
	Callbacks                   EvolveCallbacks       `json:"-"`
	AllowDuplicate              bool                  `json:"allow_duplicate"`
}

func DefaultPopulationConfig() PopulationConfig {
	return PopulationConfig{
		Size:                        20,
		SurvivalRate:                0.6,
		MutationRate:                0.2,
		EliteCount:                  3,
		BreedingPoolRatio:           0.6,
		MinMutationRate:             0.05,
		MaxMutationRate:             0.5,
		MaxStagnantGenerations:      10,
		DiversityThreshold:          0.15,
		SelectionStrategy:           "",
		TournamentSize:              3,
		DiversityWeights:            DiversityWeightConfig{},
		FitnessSharingSampleLimit:   50,
		FitnessSharingSampleSize:    30,
		SpatialIndexThreshold:       500,
		DiversitySampleSize:         200,
		DisablePromptDiversityGuard: false,
		PerLineageElites:            true,
		PerLineageEliteCount:        1,
		AgentMaxAge:                 0,
		AllowDuplicate:              true,
	}
}

// PopulationStats holds statistical information about a population's state.
type PopulationStats struct {
	Generation int
	Size       int
	BestScore  float64
	AvgScore   float64
	WorstScore float64
	Diversity  DiversityReport
}

// ScorerFunc is a function that assigns a fitness score to a strategy.
type ScorerFunc func(agent *mutation.Strategy) float64

// MultiObjectiveScorerFunc scores an agent across multiple dimensions.
type MultiObjectiveScorerFunc func(agent *mutation.Strategy) (dims map[string]float64, aggregate float64)

func NoopScorer(agent *mutation.Strategy) float64 {
	return agent.Score
}

func ConstantScorer(score float64) ScorerFunc {
	return func(*mutation.Strategy) float64 { return score }
}

// evolveConfig is an internal configuration struct for doEvolve.
type evolveConfig struct {
	survivalRate float64
	parentPoolFn func([]*mutation.Strategy) []*mutation.Strategy
	eliteFn      func([]*mutation.Strategy) []*mutation.Strategy
	logLabel     string
	// maxOffspring limits the number of offspring generated per generation.
	// 0 means unlimited (fill to Size - eliteCount). Used by steady-state GA.
	maxOffspring int
}
