// Package evolution provides a high-level API for autonomous genetic algorithm
// based strategy evolution. It wraps internal evolution components into a clean,
// import-and-use abstraction.
package evolution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sync/errgroup"

	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/experience"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/promotion"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/scoring"
)

const (
	// concurrentScoreLimit is the maximum number of concurrent LLM scoring calls.
	concurrentScoreLimit = 15

	// maxLineages caps the total recorded lineage entries to prevent
	// unbounded memory growth during long-running evolution sessions.
	maxLineages = 1000
)

// scoreContextKey is used to pass the population best score through context
// for promotion evaluation callbacks that need score improvement information.
type scoreContextKey struct{}

// Service provides high-level genetic algorithm evolution operations.
// It wraps either a fully wired evolution system (WiredEvolutionSystem) or
// a raw population with mutator and crosser, exposing a simple API for
// running evolution, retrieving results, and managing lifecycle.
type Service struct {
	wiredSystem *evolution.WiredEvolutionSystem
	population  *genome.Population
	mutator     *mutation.Mutator
	crosser     *genome.Crossover
	config      *SystemConfig

	// lineages tracks recorded parent-child lineages for non-wired mode.
	lineages []StrategyLineage
}

// ReportPath returns the configured report file path, or empty string if not set.
func (s *Service) ReportPath() string {
	if s.config == nil {
		return ""
	}
	return s.config.ReportPath
}

// NewService creates a new evolution service instance with the given configuration.
//
// Args:
//
//	cfg - service configuration (use DefaultConfig() for sensible defaults).
//
// Returns:
//
//	*Service - the initialized evolution service instance.
//	error - ErrNilConfig if cfg is nil, ErrNilBaseStrategy if base strategy is nil,
//	        ErrInvalidRate if any rate is outside [0,1], or internal creation error.
func NewService(cfg *SystemConfig) (*Service, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}

	if cfg.BaseStrategy == nil {
		return nil, ErrNilBaseStrategy
	}

	if cfg.SurvivalRate < 0 || cfg.SurvivalRate > 1 {
		return nil, fmt.Errorf("%w: survival_rate=%v", ErrInvalidRate, cfg.SurvivalRate)
	}
	if cfg.MutationRate < 0 || cfg.MutationRate > 1 {
		return nil, fmt.Errorf("%w: mutation_rate=%v", ErrInvalidRate, cfg.MutationRate)
	}
	if cfg.MinMutationRate < 0 || cfg.MinMutationRate > 1 {
		return nil, fmt.Errorf("%w: min_mutation_rate=%v", ErrInvalidRate, cfg.MinMutationRate)
	}
	if cfg.MaxMutationRate < 0 || cfg.MaxMutationRate > 1 {
		return nil, fmt.Errorf("%w: max_mutation_rate=%v", ErrInvalidRate, cfg.MaxMutationRate)
	}
	if cfg.MinMutationRate > cfg.MaxMutationRate {
		return nil, fmt.Errorf("min_mutation_rate=%v > max_mutation_rate=%v", cfg.MinMutationRate, cfg.MaxMutationRate)
	}
	if cfg.BreedingPoolRatio < 0 || cfg.BreedingPoolRatio > 1 {
		return nil, fmt.Errorf("%w: breeding_pool_ratio=%v", ErrInvalidRate, cfg.BreedingPoolRatio)
	}

	s := &Service{
		config: cfg,
	}

	if cfg.EnableWiredMode {
		wired, err := s.CreateWiredSystem(cfg)
		if err != nil {
			return nil, fmt.Errorf("create wired system: %w", err)
		}
		s.wiredSystem = wired
	} else {
		pop, mut, cross, err := s.createRawComponents(cfg)
		if err != nil {
			return nil, fmt.Errorf("create raw components: %w", err)
		}
		s.population = pop
		s.mutator = mut
		s.crosser = cross
	}

	log.Info("evolution service created",
		"population_size", cfg.PopulationSize,
		"elite_count", cfg.EliteCount,
		"wired_mode", cfg.EnableWiredMode,
	)

	return s, nil
}

// createWiredSystem creates a fully wired evolution system from the API config.
//
//nolint:gocyclo // Complex wired system creation with multiple components
//nolint:gocyclo
func (s *Service) CreateWiredSystem(cfg *SystemConfig) (*evolution.WiredEvolutionSystem, error) {
	baseStrategy := toInternalStrategy(cfg.BaseStrategy)

	internalCfg := evolution.DefaultSystemConfig()
	internalCfg.PopulationSize = cfg.PopulationSize
	internalCfg.EliteCount = cfg.EliteCount
	internalCfg.SurvivalRate = cfg.SurvivalRate
	internalCfg.MutationRate = cfg.MutationRate
	internalCfg.MinMutationRate = cfg.MinMutationRate
	internalCfg.MaxMutationRate = cfg.MaxMutationRate
	internalCfg.MaxStagnantGenerations = cfg.MaxStagnantGenerations
	internalCfg.DiversityThreshold = cfg.DiversityThreshold
	internalCfg.BreedingPoolRatio = cfg.BreedingPoolRatio
	internalCfg.SelectionStrategy = cfg.SelectionStrategy
	internalCfg.HistoryMaxSize = cfg.HistoryMaxSize
	internalCfg.MutatorSeed = cfg.Seed
	internalCfg.CrossoverSeed = cfg.Seed
	internalCfg.PopulationSeed = cfg.Seed
	internalCfg.PromptCrossoverMode = cfg.PromptCrossoverMode
	internalCfg.PromptTemplates = cfg.PromptPool

	// Thread the API-level scorer into the wired system's adapter so the
	// scheduler path automatically scores new offspring after each generation.
	// This ensures both Service.Evolve and scheduler-triggered paths have
	// a closed scoring loop.
	if cfg.Scorer != nil {
		apiScorer := cfg.Scorer
		internalCfg.Scorer = func(agent *mutation.Strategy) float64 {
			return apiScorer(toAPIStrategy(agent))
		}
	}

	// Thread the API-level batch scorer into the wired system's adapter.
	// When set, the adapter pre-fills the score cache with batch-scored values
	// before EvolveAfterScoring, so the tiered scorer finds cache hits for all
	// agents — turning N per-agent LLM calls into ceil(N/batchSize) batched calls.
	if cfg.BatchScorer != nil {
		apiBatch := cfg.BatchScorer
		internalCfg.BatchScorer = func(_ context.Context, agents []*mutation.Strategy) []float64 {
			apiStrats := make([]*Strategy, len(agents))
			for i, ag := range agents {
				apiStrats[i] = toAPIStrategy(ag)
			}
			return apiBatch.BatchScore(apiStrats)
		}
	}

	// Wire guardrails when enabled.
	if cfg.Guardrails != nil && cfg.Guardrails.Enabled {
		var guardrailOpts []evolution.GuardrailOption
		if cfg.Guardrails.BaselineScore > 0 {
			guardrailOpts = append(guardrailOpts, evolution.WithBaselineScore(cfg.Guardrails.BaselineScore))
		}
		if cfg.Guardrails.MaxStagnantGenerations > 0 {
			guardrailOpts = append(guardrailOpts, evolution.WithMaxStagnantGenerations(cfg.Guardrails.MaxStagnantGenerations))
		}
		if cfg.Guardrails.MaxLineageShare > 0 {
			guardrailOpts = append(guardrailOpts, evolution.WithMaxLineageShare(cfg.Guardrails.MaxLineageShare))
		}
		if len(cfg.Guardrails.KnownTools) > 0 {
			guardrailOpts = append(guardrailOpts, evolution.WithKnownTools(cfg.Guardrails.KnownTools))
		}
		guardrails, err := evolution.NewEvolutionGuardrails(guardrailOpts...)
		if err != nil {
			return nil, fmt.Errorf("new evolution guardrails: %w", err)
		}
		internalCfg.Guardrails = guardrails
	}

	// Wire guidance provider for experience-guided mutation.
	if cfg.GuidanceProvider != nil {
		internalCfg.GuidanceProvider = &apiGuidanceBridge{provider: cfg.GuidanceProvider}
		internalCfg.EnableExperienceGuidedMutation = cfg.EnableExperienceGuidedMutation
	}

	// Wire LLM hint provider for DreamCycle outcome recording and
	// LLM-based hint generation. Only applies when EnableLLMHints is set
	// and an LLMClient is configured.
	if cfg.EnableLLMHints && cfg.LLMClient != nil {
		maxHistory := cfg.MaxHintHistory
		if maxHistory <= 0 {
			maxHistory = 10
		}
		hintProvider, err := mutation.NewLLMHintProvider(&llmClientAdapter{inner: cfg.LLMClient}, &mutation.LLMHintProviderConfig{
			MaxHistory: maxHistory,
		})
		if err != nil {
			return nil, fmt.Errorf("create LLM hint provider: %w", err)
		}
		internalCfg.HintProvider = hintProvider
	}

	// Wire memory experience provider for memory-aware scoring.
	if cfg.MemoryExperienceProvider != nil {
		internalCfg.MemoryExperienceProvider = &apiMemoryBridge{provider: cfg.MemoryExperienceProvider}
	}

	// Map memory-aware scoring config.
	if cfg.MemoryAwareScoringConfig.Enabled {
		internalCfg.MemoryAwareScoringConfig = scoring.MemoryAwareScoringConfig{
			Enabled:               cfg.MemoryAwareScoringConfig.Enabled,
			MemoryWeight:          cfg.MemoryAwareScoringConfig.MemoryWeight,
			CostWeight:            cfg.MemoryAwareScoringConfig.CostWeight,
			LatencyWeight:         cfg.MemoryAwareScoringConfig.LatencyWeight,
			RegressionWeight:      cfg.MemoryAwareScoringConfig.RegressionWeight,
			MinEvidenceBonus:      cfg.MemoryAwareScoringConfig.MinEvidenceBonus,
			MaxEvidenceBonus:      cfg.MemoryAwareScoringConfig.MaxEvidenceBonus,
			ExperienceLookupLimit: cfg.MemoryAwareScoringConfig.ExperienceLookupLimit,
		}
	}

	if cfg.Seed != 0 {
		internalCfg.UseDeterministicIDs = true
	}
	if cfg.UseDeterministicIDs != nil {
		internalCfg.UseDeterministicIDs = *cfg.UseDeterministicIDs
	}

	system, err := evolution.NewWiredEvolutionSystem(baseStrategy, internalCfg)
	if err != nil {
		return nil, fmt.Errorf("new wired evolution system: %w", err)
	}

	// Wire intelligence pipeline: reflection → hypothesis → meta.
	// Only when EnableIntelligence is set, LLMClient is available, and
	// history tracking is enabled (required for meta-controller).
	if cfg.EnableIntelligence && cfg.LLMClient != nil && cfg.HistoryMaxSize > 0 {
		llmAdapter := &llmClientAdapter{inner: cfg.LLMClient}
		system.Reflector = genome.NewLLMReflector(llmAdapter)
		system.HypothesisGen = genome.NewHypothesisGenerator(0.3)
		system.MetaCtrl = genome.NewMetaController(genome.DefaultMetaConfig())
	}

	// Wire post-evolution hook for promotion evaluation and reporting.
	evidenceAgg := resolveEvidenceAggregator(cfg.EvidenceAggregator)
	promoter := resolvePromotionLogic(cfg.PromotionLogic)
	if evidenceAgg != nil || promoter != nil {
		system.AfterGeneration = func(ctx context.Context, gen int, sys *evolution.WiredEvolutionSystem) error {
			best := sys.Population.Best()
			if best == nil {
				return nil
			}

			// Register the best strategy's evidence key so that the evidence
			// aggregator can resolve GA-generated UUIDs to phenotype keys for
			// fallback lookup. This enables evidence-based evaluation even when
			// the exact strategy ID is unknown to the store.
			if reg, ok := cfg.EvidenceAggregator.(interface{ RegisterStrategyKey(string, string) }); ok {
				reg.RegisterStrategyKey(best.ID, best.ComputeEvidenceKey())
			}

			var ev Evidence
			if evidenceAgg != nil {
				var err error
				ev, err = evidenceAgg(ctx, best.ID)
				if err != nil {
					log.WarnContext(ctx, "post-generation: evidence aggregation failed",
						"generation", sys.Population.Generation, "run_iteration", gen, "strategy_id", best.ID, "error", err)
				}
			}

			if promoter != nil && ev.SampleCount > 0 {
				// Pass population best score through context for downstream
				// promotion logic that needs score improvement info.
				scoreCtx := context.WithValue(ctx, scoreContextKey{}, best.Score)
				state, reason, err := promoter(scoreCtx, best.ID, ev)
				if err != nil {
					log.WarnContext(ctx, "post-generation: promotion evaluation failed",
						"generation", sys.Population.Generation, "run_iteration", gen, "strategy_id", best.ID, "error", err)
				} else {
					log.InfoContext(ctx, "post-generation promotion evaluation",
						"generation", sys.Population.Generation,
						"winner", best.ID,
						"fitness", best.Score,
						"success_rate", ev.SuccessRate,
						"sample_count", ev.SampleCount,
						"confidence", ev.Confidence,
						"promotion_state", state,
						"reason", reason,
					)
				}
			} else {
				log.InfoContext(ctx, "generation complete",
					"generation", sys.Population.Generation,
					"winner", best.ID,
					"fitness", best.Score,
				)
			}
			return nil
		}
	}

	// Wire the post-run report hook (only when ReportPath is set).
	wireAfterRunReport(system, cfg, evidenceAgg, promoter)

	return system, nil
}

// createRawComponents creates raw population, mutator, and crosser (non-wired mode).
func (s *Service) createRawComponents(cfg *SystemConfig) (*genome.Population, *mutation.Mutator, *genome.Crossover, error) {
	baseStrategy := toInternalStrategy(cfg.BaseStrategy)

	useDetIDs := cfg.Seed != 0
	if cfg.UseDeterministicIDs != nil {
		useDetIDs = *cfg.UseDeterministicIDs
	}

	var mutatorOpts []mutation.MutatorOption
	if cfg.Seed != 0 {
		mutatorOpts = append(mutatorOpts, mutation.WithSeed(cfg.Seed))
	}
	if useDetIDs {
		mutatorOpts = append(mutatorOpts, mutation.WithDeterministicIDs(true))
	}
	if len(cfg.PromptPool) > 0 {
		mutatorOpts = append(mutatorOpts, mutation.WithPromptPool(cfg.PromptPool))
	}

	rawMutator, err := mutation.NewMutator(mutatorOpts...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("new mutator: %w", err)
	}

	genomeMutator, err := evolution.NewGenomeMutatorAdapter(rawMutator)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("new genome mutator adapter: %w", err)
	}

	var crosserOpts []genome.CrossoverOption
	if cfg.Seed != 0 {
		crosserOpts = append(crosserOpts, genome.WithSeed(cfg.Seed))
	}
	if useDetIDs {
		crosserOpts = append(crosserOpts, genome.WithDeterministicIDs(true))
	}
	switch cfg.PromptCrossoverMode {
	case 1:
		crosserOpts = append(crosserOpts, genome.WithPromptMode(genome.PromptHalfSplit))
	case 2:
		crosserOpts = append(crosserOpts, genome.WithPromptMode(genome.PromptUniform))
	}

	crosser, err := genome.NewCrossover(crosserOpts...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("new crossover: %w", err)
	}

	popOpts := []genome.PopulationOption{
		genome.WithPopulationSize(cfg.PopulationSize),
		genome.WithEliteCount(cfg.EliteCount),
		genome.WithMutationRate(cfg.MutationRate),
		genome.WithSurvivalRate(cfg.SurvivalRate),
		genome.WithMinMutationRate(cfg.MinMutationRate),
		genome.WithMaxMutationRate(cfg.MaxMutationRate),
		genome.WithMaxStagnantGenerations(cfg.MaxStagnantGenerations),
		genome.WithDiversityThreshold(cfg.DiversityThreshold),
		genome.WithBreedingPoolRatio(cfg.BreedingPoolRatio),
	}
	if cfg.HistoryMaxSize > 0 {
		popOpts = append(popOpts, genome.WithHistoryEnabled(cfg.HistoryMaxSize))
	}
	if cfg.SelectionStrategy != "" {
		popOpts = append(popOpts, genome.WithSelectionStrategy(cfg.SelectionStrategy))
	}
	if cfg.Seed != 0 {
		popOpts = append(popOpts, genome.WithPopulationSeed(cfg.Seed))
	}

	pop, err := genome.NewPopulation(context.Background(), baseStrategy, genomeMutator, popOpts...)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("new population: %w", err)
	}

	return pop, rawMutator, crosser, nil
}

// RunIdleEvolution runs idle evolution with the wired system.
// When ReportPath is configured, the report is saved after all generations.
//
// Args:
//
//	ctx - operation context.
//	generations - number of generations to run.
//
// Returns:
//
//	error - non-nil if system creation or evolution fails.
func (s *Service) RunIdleEvolution(ctx context.Context, generations int) error {
	system, err := s.CreateWiredSystem(s.config)
	if err != nil {
		return fmt.Errorf("run idle evolution: %w", err)
	}
	return evolution.RunIdleEvolution(ctx, system, generations)
}

// Evolve runs N generations of evolution and returns the complete result.
// This is the primary entry point for executing evolution cycles.
//
// Args:
//
//	ctx - operation context for cancellation.
//	generations - number of generations to run (overrides config.Generations if > 0).
//
// Returns:
//
//	*EvolutionResult - the result containing best strategy, per-generation stats, and lineages.
//	error - ErrNotInitialized if system not ready, or evolution execution error.
func (s *Service) Evolve(ctx context.Context, generations int) (*EvolutionResult, error) {
	if generations <= 0 {
		generations = s.config.Generations
	}

	result := &EvolutionResult{
		Stats:    make([]Stats, 0, generations),
		Lineages: make([]StrategyLineage, 0),
	}

	// Initialize scores before first generation so selection has meaningful data.
	s.initScores(ctx)

	// Lineage cursor: collectLineages()/s.lineages return the FULL
	// accumulated genealogy each generation; appending the whole slice every
	// generation made result.Lineages O(G × total) with massive duplication.
	// Only the tail grown since the previous generation is appended.
	wiredLineageLen := 0

	for i := 0; i < generations; i++ {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		switch {
		case s.wiredSystem != nil:
			if err := evolution.RunIdleEvolution(ctx, s.wiredSystem, 1); err != nil {
				return nil, fmt.Errorf("evolve generation %d: %w", i+1, err)
			}

			// RunIdleEvolution → adapter.Run() → EvolveAfterScoring already
			// post-scores all agents after evolving. No need to re-score here.

			stats := s.collectStats()
			result.Stats = append(result.Stats, stats)

			lineages := s.collectLineages()
			result.Lineages = append(result.Lineages, lineages[wiredLineageLen:]...)
			wiredLineageLen = len(lineages)
		case s.population != nil && s.mutator != nil && s.crosser != nil:
			prevSnapshot, _ := s.population.Snapshot()
			prevBest := bestFromStrategies(prevSnapshot)

			mutAdapter, err := s.genomeMutatorAdapter()
			if err != nil {
				return nil, fmt.Errorf("get mutator adapter gen %d: %w", i+1, err)
			}
			if err := s.population.EvolveOnIdle(ctx, mutAdapter, s.crosser); err != nil {
				return nil, fmt.Errorf("evolve generation %d: %w", i+1, err)
			}

			// Re-score after each evolution so next generation selects on fresh data.
			s.initScores(ctx)

			// Record lineages for non-wired mode: link parent→child.
			s.recordGenealogy(prevBest)

			// Record lineages for non-wired mode: track each offspring's
			// parent-child relationship. Only this generation's additions go
			// into the result.
			result.Lineages = append(result.Lineages, s.recordLineages()...)

			stats := s.collectStats()
			result.Stats = append(result.Stats, stats)
		default:
			return nil, ErrNotInitialized
		}

		result.TotalGens++
	}

	best, err := s.BestStrategy()
	if err != nil {
		log.WarnContext(ctx, "failed to get best strategy after evolution", "error", err)
	} else {
		result.BestStrategy = best
	}

	// Count raw improvements vs significant improvements for reporting.
	rawCount := 0
	sigCount := 0
	threshold := s.config.MinLineageImprovement
	if threshold <= 0 {
		threshold = 0.01
	}
	for _, l := range result.Lineages {
		if l.ScoreDelta > 0 {
			rawCount++
		}
		if l.ScoreDelta >= threshold {
			sigCount++
		}
	}

	log.InfoContext(ctx, "evolution completed",
		"total_generations", result.TotalGens,
		"raw_improvements", rawCount,
		"significant_improvements", sigCount,
		"improvement_threshold", threshold,
	)

	return result, nil
}

// BestStrategy returns the current best strategy from the evolution system.
//
// Returns:
//
//	*Strategy - cloned best strategy, or error if not available.
//	error - ErrNotInitialized if no system is active.
func (s *Service) BestStrategy() (*Strategy, error) {
	if s.wiredSystem != nil {
		internalBest, err := evolution.BestStrategyFromSystem(s.wiredSystem)
		if err != nil {
			return nil, fmt.Errorf("get best strategy from wired system: %w", err)
		}
		return toAPIStrategy(internalBest), nil
	}

	if s.population != nil {
		internalBest := s.population.BestStrategy()
		if internalBest == nil {
			return nil, errors.New("population has no strategies")
		}
		return toAPIStrategy(internalBest), nil
	}

	return nil, ErrNotInitialized
}

// Stats returns current population statistics.
//
// Returns:
//
//	*Stats - snapshot of population statistics.
//	error - ErrNotInitialized if no system is active.
func (s *Service) Stats() (*Stats, error) {
	if s.wiredSystem != nil && s.wiredSystem.Population != nil {
		stats := collectStatsFromPopulation(s.wiredSystem.Population)
		return &stats, nil
	}

	if s.population != nil {
		stats := collectStatsFromPopulation(s.population)
		return &stats, nil
	}

	return nil, ErrNotInitialized
}

// Lineages returns all recorded strategy lineages from evolution history.
//
// Returns:
//
//	[]StrategyLineage - copy of recorded lineages.
//	error - ErrNotInitialized if no system is active.
func (s *Service) Lineages() ([]StrategyLineage, error) {
	if s.wiredSystem != nil && s.wiredSystem.Genealogy != nil {
		internalLineages := s.wiredSystem.Genealogy.Lineages()
		apiLineages := make([]StrategyLineage, 0, len(internalLineages))
		for _, l := range internalLineages {
			apiLineages = append(apiLineages, toAPILineage(l))
		}
		return apiLineages, nil
	}
	// Non-wired mode: return tracked lineages.
	result := make([]StrategyLineage, len(s.lineages))
	copy(result, s.lineages)
	return result, nil
}

// Shutdown gracefully stops the evolution system and releases resources.
// It is safe to call Shutdown multiple times; subsequent calls are no-ops.
func (s *Service) Shutdown() {
	if s.wiredSystem != nil {
		evolution.Shutdown(s.wiredSystem)
	}
	log.Info("evolution service shut down")
}

// SaveBestStrategy persists the best strategy to a JSON file at the given path.
// Returns an error if serialization or file I/O fails.
func (s *Service) SaveBestStrategy(path string) error {
	best, err := s.BestStrategy()
	if err != nil {
		return fmt.Errorf("get best strategy: %w", err)
	}
	if best == nil {
		return errors.New("no best strategy available")
	}

	dir := filepath.Dir(path)
	if dir != "" {
		if err := os.MkdirAll(dir, 0750); err != nil {
			return fmt.Errorf("create directory %s: %w", dir, err)
		}
	}

	data, err := json.MarshalIndent(best, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal strategy: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	log.Info("best strategy saved", "path", path, "id", best.ID, "score", best.Score)
	return nil
}

// LoadBestStrategy loads a best strategy from a JSON file at the given path.
// Returns the loaded strategy, or an error if the file cannot be read.
func LoadBestStrategy(path string) (*Strategy, error) {
	data, err := os.ReadFile(path) //nolint:gosec // intentional file read for best strategy loading
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var s Strategy
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("unmarshal strategy: %w", err)
	}
	return &s, nil
}

// ──────────────────────────────────────────────
// Bridge adapters: convert API provider interfaces
// into internal interfaces for wired system wiring.
// ──────────────────────────────────────────────

// apiGuidanceBridge adapts an API GuidanceProvider into an internal
// evolution.GuidanceProvider so the wired system can use it for
// experience-guided mutation.

// genomeMutatorAdapter creates a genome-compatible mutator adapter from the raw mutator.
//
// Returns:
//
//	genome.MutatorInterface - the adapter instance.
//	error - non-nil if adapter creation fails.
func (s *Service) genomeMutatorAdapter() (genome.MutatorInterface, error) {
	if s.mutator == nil {
		return nil, ErrNotInitialized
	}
	adapter, err := evolution.NewGenomeMutatorAdapter(s.mutator)
	if err != nil {
		return nil, fmt.Errorf("create genome mutator adapter: %w", err)
	}
	return adapter, nil
}

// collectStats gathers current statistics from the active system.
func (s *Service) collectStats() Stats {
	if s.wiredSystem != nil && s.wiredSystem.Population != nil {
		return collectStatsFromPopulation(s.wiredSystem.Population)
	}
	if s.population != nil {
		return collectStatsFromPopulation(s.population)
	}
	return Stats{}
}

// collectStatsFromPopulation extracts stats from a genome.Population into API Stats.
func collectStatsFromPopulation(pop *genome.Population) Stats {
	ps := pop.Stats()
	s := Stats{
		Generation: ps.Generation,
		Size:       ps.Size,
		BestScore:  ps.BestScore,
		AvgScore:   ps.AvgScore,
		WorstScore: ps.WorstScore,
	}
	if d := ps.Diversity; d.Overall != 0 || d.Numeric != 0 || d.Categorical != 0 {
		s.Diversity = &DiversityReporter{
			Overall:              d.Overall,
			Numeric:              d.Numeric,
			Categorical:          d.Categorical,
			Lineage:              d.Lineage,
			DominantLineageShare: d.DominantLineageShare,
		}
	}
	return s
}

// collectLineages gathers all recorded lineages from the genealogy recorder.
func (s *Service) collectLineages() []StrategyLineage {
	if s.wiredSystem != nil && s.wiredSystem.Genealogy != nil {
		internal := s.wiredSystem.Genealogy.Lineages()
		result := make([]StrategyLineage, 0, len(internal))
		threshold := s.config.MinLineageImprovement
		if threshold <= 0 {
			threshold = 0.01
		}
		for _, l := range internal {
			apiLineage := toAPILineage(l)
			// Set significance flag: absolute delta must meet or exceed threshold.
			apiLineage.ImprovementSignificant = l.ScoreImprovement >= threshold
			result = append(result, apiLineage)
		}
		return result
	}
	return []StrategyLineage{}
}

// scoreAgents assigns fitness scores to agents in the population.
// If s.config.Scorer is set, it delegates to that. Otherwise it uses a
// deterministic parameter-aware heuristic (no random noise):
//   - temperature: lower is better (0.0→+25, 1.0→+0)
//   - top_k near 30 balances focus vs breadth (penalty dist²/10)
//   - "precise" prompt template earns a bonus (+15)
func (s *Service) scoreAgents(ctx context.Context, pop *genome.Population) {
	// Fast path: deterministic scorer — no clone/concurrency overhead.
	if s.config.Scorer == nil {
		pop.ScoreAgents(func(agent *mutation.Strategy) float64 {
			return DeterministicScore(toAPIStrategy(agent))
		})
		return
	}

	snap, _ := pop.Snapshot()

	// Batch path: if BatchScorer is set, score all agents in one call.
	if bs := s.config.BatchScorer; bs != nil {
		apiStrategies := make([]*Strategy, len(snap))
		for i, a := range snap {
			apiStrategies[i] = toAPIStrategy(a)
		}
		scores := bs.BatchScore(apiStrategies)
		scoreMap := make(map[string]float64, len(snap))
		for i, a := range snap {
			if i < len(scores) {
				scoreMap[a.ID] = scores[i]
			}
		}
		pop.ScoreAgents(func(agent *mutation.Strategy) float64 {
			if sc, ok := scoreMap[agent.ID]; ok {
				return sc
			}
			return 0
		})
		return
	}

	// Slow path: per-agent scoring with concurrency limit.
	// Use errgroup with SetLimit for structured concurrency. The ScorerFunc
	// signature does not take a context, so goroutines cannot be interrupted
	// mid-call; errgroup still ensures deterministic Wait and bounded
	// parallelism equivalent to the previous semaphore+WaitGroup pattern.
	scores := make([]float64, len(snap))
	g, _ := errgroup.WithContext(ctx)
	g.SetLimit(concurrentScoreLimit)

	for i, agent := range snap {
		g.Go(func() error {
			scores[i] = s.config.Scorer(toAPIStrategy(agent))
			return nil
		})
	}
	_ = g.Wait()

	scoreMap := make(map[string]float64, len(snap))
	for i, a := range snap {
		scoreMap[a.ID] = scores[i]
	}

	pop.ScoreAgents(func(agent *mutation.Strategy) float64 {
		if sc, ok := scoreMap[agent.ID]; ok {
			return sc
		}
		return 0
	})
}

// initScores initializes scores for all agents in the population.
func (s *Service) initScores(ctx context.Context) {
	if s.wiredSystem != nil && s.wiredSystem.Population != nil {
		s.scoreAgents(ctx, s.wiredSystem.Population)
	} else if s.population != nil {
		s.scoreAgents(ctx, s.population)
	}
}

// bestFromStrategies returns the strategy with the highest score from a slice.
func bestFromStrategies(agents []*mutation.Strategy) *mutation.Strategy {
	if len(agents) == 0 {
		return nil
	}
	best := agents[0]
	for _, a := range agents[1:] {
		if a.Score > best.Score {
			best = a
		}
	}
	return best
}

// recordGenealogy records a lineage entry between the previous generation's best
// and the current best if they differ and both exist (non-wired mode).
func (s *Service) recordGenealogy(prevBest *mutation.Strategy) {
	agents, _ := s.population.Snapshot()
	if len(agents) == 0 || prevBest == nil {
		return
	}
	currentBest := bestFromStrategies(agents)
	if currentBest == nil || currentBest.ID == prevBest.ID {
		return
	}
	scoreDelta := currentBest.Score - prevBest.Score
	s.lineages = append(s.lineages, StrategyLineage{
		ParentID:     prevBest.ID,
		ChildID:      currentBest.ID,
		MutationType: "evolution",
		WinRate:      0,
		ScoreDelta:   scoreDelta,
		Timestamp:    time.Now().UnixMilli(),
	})
	// Cap total lineage entries (same bound as recordLineages): this path
	// appends at most one entry per generation, but a long-running evolution
	// session would still grow s.lineages without bound. Trim oldest first.
	if len(s.lineages) > maxLineages {
		excess := len(s.lineages) - maxLineages
		s.lineages = append(s.lineages[:0], s.lineages[excess:]...)
	}
}

// resolveEvidenceAggregator unwraps cfg.EvidenceAggregator, which can be
// either a local EvidenceAggregator or an internal experience.EvidenceAggregator.
// Returns nil when v is nil.
func resolveEvidenceAggregator(v interface{}) func(ctx context.Context, strategyID string) (Evidence, error) {
	if v == nil {
		return nil
	}
	// Local interface.
	if local, ok := v.(EvidenceAggregator); ok {
		return local.Aggregate
	}
	// Internal experience.EvidenceAggregator — wrap with adapter.
	if internal, ok := v.(experience.EvidenceAggregator); ok {
		return func(ctx context.Context, strategyID string) (Evidence, error) {
			ev, err := internal.Aggregate(ctx, strategyID)
			if err != nil {
				return Evidence{}, err
			}
			return Evidence{
				StrategyID:  ev.StrategyID,
				SuccessRate: ev.SuccessRate,
				LatencyP50:  ev.LatencyP50,
				ErrorRate:   ev.ErrorRate,
				SampleCount: ev.SampleCount,
				Confidence:  ev.Confidence,
			}, nil
		}
	}
	log.Warn("evidence aggregator: unrecognised type, ignoring",
		"type", fmt.Sprintf("%T", v))
	return nil
}

// resolvePromotionLogic unwraps cfg.PromotionLogic, which can be
// either a local PromotionLogic or an internal promotion.PromotionLogic.
// Returns nil when v is nil.
func resolvePromotionLogic(v interface{}) func(ctx context.Context, strategyID string, evidence Evidence) (string, string, error) {
	if v == nil {
		return nil
	}
	// Local interface.
	if local, ok := v.(PromotionLogic); ok {
		return local.Evaluate
	}
	// Internal promotion.PromotionLogic — wrap with adapter.
	if internal, ok := v.(promotion.PromotionLogic); ok {
		return func(ctx context.Context, strategyID string, evidence Evidence) (string, string, error) {
			ev, err := toInternalEvidence(evidence)
			if err != nil {
				return "", "", err
			}
			state, reason, err := internal.Evaluate(ctx, strategyID, ev)
			if err != nil {
				return "", "", err
			}
			return string(state), reason, nil
		}
	}
	log.Warn("promotion logic: unrecognised type, ignoring",
		"type", fmt.Sprintf("%T", v))
	return nil
}

// toInternalEvidence converts a local Evidence to internal experience.Evidence.
func toInternalEvidence(ev Evidence) (experience.Evidence, error) {
	return experience.Evidence{
		StrategyID:  ev.StrategyID,
		SuccessRate: ev.SuccessRate,
		LatencyP50:  ev.LatencyP50,
		ErrorRate:   ev.ErrorRate,
		SampleCount: ev.SampleCount,
		Confidence:  ev.Confidence,
	}, nil
}

// recordLineages records parent→child lineages for the current generation
// into s.lineages (capped at maxLineages to prevent unbounded growth) and
// returns exactly the entries added this generation, so callers append the
// delta instead of the whole accumulated history (avoids O(G × total)
// duplication).
func (s *Service) recordLineages() []StrategyLineage {
	if s.population == nil {
		return nil
	}
	snapshot, _ := s.population.Snapshot()
	added := make([]StrategyLineage, 0)
	for _, agent := range snapshot {
		if agent.ParentID != "" {
			l := StrategyLineage{
				ParentID:     agent.ParentID,
				ChildID:      agent.ID,
				MutationType: agent.StrategyMutationType.String(),
				WinRate:      0, // Not measured in simple GA mode
				ScoreDelta:   agent.Score,
				Timestamp:    time.Now().Unix(),
			}
			s.lineages = append(s.lineages, l)
			added = append(added, l)
		}
	}

	// Trim oldest entries if over cap.
	if len(s.lineages) > maxLineages {
		excess := len(s.lineages) - maxLineages
		s.lineages = append(s.lineages[:0], s.lineages[excess:]...)
	}
	return added
}
