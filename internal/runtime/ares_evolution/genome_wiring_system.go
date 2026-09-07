package evolution

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/scoring"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/coordinator"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/diff"
	evogenome "github.com/Timwood0x10/ares/internal/runtime/evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
	aresExperience "github.com/Timwood0x10/ares/internal/runtime/memory/experience"
	"github.com/Timwood0x10/ares/internal/runtime/observability"
)

// WiredEvolutionSystem holds a fully wired autonomous evolution system.
type WiredEvolutionSystem struct {
	Scheduler             *EvolutionScheduler
	DreamCycle            *DreamCycle
	PopAdapter            *GenomePopulationAdapter
	Population            *genome.Population
	Genealogy             *PopulationGenealogyRecorder
	StrategyStore         StrategyStore
	ActiveStrategyManager *ActiveStrategyManager
	ShadowEvaluator       *ShadowEvaluator
	FeedbackRecorder      *FeedbackRecorder
	AdaptiveDist          *mutation.AdaptiveDistribution
	TieredScorer          *scoring.TieredScorer
	Budget                *scoring.Budget
	ScoreCache            *scoring.ScoreCache
	Metrics               *observability.PrometheusMetrics

	// Intelligence components (Phase 3-5). Set to nil to disable.
	Reflector     *genome.LLMReflector        `json:"-"`
	HypothesisGen *genome.HypothesisGenerator `json:"-"`
	MetaCtrl      *genome.MetaController      `json:"-"`

	// Lifecycle is the strategy orchestrator (B1/B2/B3 fix). When set,
	// it is the sole entry point for promoting a candidate strategy.
	// Run() submits to Lifecycle.Submit instead of Deploy directly.
	Lifecycle *StrategyLifecycle `json:"-"`

	// Phase 6: Diff Engine + Coordinator for graph structure evolution.
	// When set, each generation's mutation is diffed and patches submitted.
	DiffReg     *diff.Registry                    `json:"-"`
	Coordinator *coordinator.EvolutionCoordinator `json:"-"`
	GenomeReg   *evogenome.Registry               `json:"-"`

	// AfterGeneration is called after each idle evolution generation with
	// the generation index and the system. When non-nil, it receives the
	// fully evolved state (population already scored, lineage recorded).
	// Can be used for promotion evaluation, report generation, or metrics.
	// Returning an error is non-fatal — the error is logged and evolution
	// continues to the next generation.
	AfterGeneration func(ctx context.Context, gen int, system *WiredEvolutionSystem) error `json:"-"`

	// AfterRun is called once after RunIdleEvolution completes all generations.
	// When non-nil, it receives the final system state after the evolution loop
	// ends. Can be used for final report generation, persistence, or cleanup.
	// Returning an error is non-fatal — the error is logged but not propagated.
	AfterRun func(ctx context.Context, system *WiredEvolutionSystem) error `json:"-"`
}

// ScoringConfig groups scorer pipeline settings.
type ScoringConfig struct {
	Scorer                   genome.ScorerFunc                `json:"-"`
	HeuristicScorer          genome.ScorerFunc                `json:"-"`
	BatchScorer              BatchScorer                      `json:"-"`
	MaxLLMCallsPerGeneration int                              `json:"max_llm_calls_per_generation,omitempty"`
	ScoreCacheSize           int                              `json:"score_cache_size,omitempty"`
	MemoryAwareScoringConfig scoring.MemoryAwareScoringConfig `json:"memory_aware_scoring,omitempty"`
	MemoryExperienceProvider scoring.ExperienceProvider       `json:"-"`
	// DeterministicScorerEnabled indicates that a zero-LLM deterministic
	// scorer is wired (C2.6). When true, the shadow gate's hasScorer check
	// passes even without an LLM scorer, so the G2 gate stays registered
	// and can produce shadow comparison evidence from execution attribution
	// alone. This breaks the "zero-token ⇒ no G2" deadlock.
	DeterministicScorerEnabled bool `json:"deterministic_scorer_enabled,omitempty"`
}

// MutationConfig groups mutation and crossover settings.
type MutationConfig struct {
	MutatorSeed         int64    `json:"mutator_seed,omitempty"`
	CrossoverSeed       int64    `json:"crossover_seed,omitempty"`
	PromptCrossoverMode int      `json:"prompt_crossover_mode"`
	PromptTemplates     []string `json:"prompt_templates,omitempty"`
	// ToolPool is the set of tool-whitelist configurations the mutator may emit
	// as Params["tools"] (each entry is a comma-separated whitelist string). It
	// is wired to mutation.WithToolPool so the elite/random mutation path can
	// actually produce tool choices from the deployment's config; without it the
	// pool was dead configuration (only guided mutation produced tool choices).
	ToolPool                       []string                            `json:"tool_pool,omitempty"`
	EnableExperienceGuidedMutation bool                                `json:"enable_experience_guided_mutation,omitempty"`
	GuidanceProvider               GuidanceProvider                    `json:"-"`
	AdaptiveDistConfig             mutation.AdaptiveDistributionConfig `json:"adaptive_distribution,omitempty"`
}

// GenomeConfig groups population-level genetic algorithm settings.
type GenomeConfig struct {
	PopulationSize         int     `json:"population_size"`
	EliteCount             int     `json:"elite_count"`
	MutationRate           float64 `json:"mutation_rate"`
	MinMutationRate        float64 `json:"min_mutation_rate,omitempty"`
	MaxMutationRate        float64 `json:"max_mutation_rate,omitempty"`
	SurvivalRate           float64 `json:"survival_rate"`
	PopulationSeed         int64   `json:"population_seed,omitempty"`
	UseDeterministicIDs    bool    `json:"use_deterministic_ids,omitempty"`
	MaxStagnantGenerations int     `json:"max_stagnant_generations"`
	DiversityThreshold     float64 `json:"diversity_threshold"`
	BreedingPoolRatio      float64 `json:"breeding_pool_ratio"`
	HistoryMaxSize         int     `json:"history_max_size"`
	SelectionStrategy      string  `json:"selection_strategy,omitempty"`
}

// SchedulerConfig groups scheduler and dream cycle settings.
type SchedulerConfig struct {
	EnableScheduler      bool                 `json:"enable_scheduler"`
	EnableDreamCycle     bool                 `json:"enable_dream_cycle"`
	SchedulerTrigger     EvolutionTrigger     `json:"scheduler_trigger"`
	MinTasksBeforeEvolve int                  `json:"min_tasks_before_evolve"`
	MaxMutations         int                  `json:"max_mutations"`
	EventStore           EventStoreSubscriber `json:"-"`
}

// DependencyConfig groups externally injected dependencies.
type DependencyConfig struct {
	StrategyStore        StrategyStore                    `json:"-"`
	Guardrails           *EvolutionGuardrails             `json:"-"`
	Metrics              *observability.PrometheusMetrics `json:"-"`
	FeedbackService      *aresExperience.FeedbackService  `json:"-"`
	HintProvider         mutation.HintProvider            `json:"-"`
	RollbackPolicyConfig RollbackPolicyConfig             `json:"rollback_policy,omitempty"`
	ShadowEvalConfig     ShadowEvaluationConfig           `json:"shadow_eval_config,omitempty"`
}

// SystemConfig holds configuration for creating a wired evolution system.
// Sub-configs are anonymous-embedded so all fields are accessible directly.
type SystemConfig struct {
	GenomeConfig
	ScoringConfig
	MutationConfig
	SchedulerConfig
	DependencyConfig

	// Lifecycle, when non-nil, overrides the default StrategyLifecycle and
	// RuntimeFitnessAggregator configuration (fitness window, judge
	// thresholds, weights, watch interval, gate settings). Bootstrap wires
	// it from the evolution YAML config (design doc §7); nil keeps the
	// code defaults from DefaultLifecycleConfig.
	Lifecycle *LifecycleConfig
}

// DefaultSystemConfig returns sensible defaults.
func DefaultSystemConfig() SystemConfig {
	return SystemConfig{
		GenomeConfig: GenomeConfig{
			PopulationSize:         20,
			EliteCount:             3,
			MutationRate:           0.2,
			SurvivalRate:           0.6,
			MaxStagnantGenerations: 10,
			DiversityThreshold:     0.15,
			BreedingPoolRatio:      0.6,
		},
		ScoringConfig: ScoringConfig{
			MemoryAwareScoringConfig: scoring.DefaultMemoryAwareScoringConfig(),
		},
		MutationConfig: MutationConfig{
			EnableExperienceGuidedMutation: true,
		},
		SchedulerConfig: SchedulerConfig{
			EnableDreamCycle:     false,
			EnableScheduler:      false,
			MinTasksBeforeEvolve: 10,
			SchedulerTrigger:     TriggerOnIdle,
		},
	}
}

// mutatorResult holds the output of buildMutator.
type mutatorResult struct {
	rawMutator   *mutation.Mutator
	adaptiveDist *mutation.AdaptiveDistribution
	genomeMut    genome.MutatorInterface
	crosser      *genome.Crossover
}

// buildMutator creates the mutation pipeline from config.
func buildMutator(cfg SystemConfig) (*mutatorResult, error) {
	var mutatorOpts []mutation.MutatorOption
	if len(cfg.PromptTemplates) > 0 {
		mutatorOpts = append(mutatorOpts, mutation.WithPromptPool(cfg.PromptTemplates))
	}
	// Wire the deployment-configured tool whitelist pool so the elite/random
	// mutation path can actually emit Params["tools"] choices. Previously this
	// option was never supplied here, making the pool path dead configuration —
	// only guided mutation produced tool choices (and always from registered-name
	// aliases). With a pool wired, both paths share the deployment's config as
	// the single source for the tool vocabulary. An empty pool keeps tool mutation
	// disabled (unchanged behavior).
	if len(cfg.ToolPool) > 0 {
		mutatorOpts = append(mutatorOpts, mutation.WithToolPool(cfg.ToolPool))
	}
	if cfg.MutatorSeed != 0 {
		mutatorOpts = append(mutatorOpts, mutation.WithSeed(cfg.MutatorSeed))
	}

	rawMutator, err := mutation.NewMutator(mutatorOpts...)
	if err != nil {
		return nil, fmt.Errorf("create mutator: %w", err)
	}

	var genomeMut genome.MutatorInterface = rawMutator

	if cfg.EnableExperienceGuidedMutation && cfg.GuidanceProvider != nil {
		log.InfoContext(context.Background(), "experience-guided mutation requested; provider wired", "method", "buildMutator",
			"hint_provider", fmt.Sprintf("%T", cfg.GuidanceProvider))
		genomeMut = wrapGuidanceProvider(cfg.GuidanceProvider, rawMutator)
	} else if cfg.EnableExperienceGuidedMutation && cfg.GuidanceProvider == nil {
		log.WarnContext(context.Background(), "experience-guided mutation requested but no GuidanceProvider set", "method", "buildMutator")
	}

	var adaptiveDist *mutation.AdaptiveDistribution
	if cfg.AdaptiveDistConfig.Enabled {
		var err error
		adaptiveDist, err = mutation.NewAdaptiveDistribution(rawMutator, cfg.AdaptiveDistConfig)
		if err != nil {
			return nil, fmt.Errorf("create adaptive distribution: %w", err)
		}
		genomeMut = adaptiveDist
	}

	crosserOpts := []genome.CrossoverOption{}
	if cfg.CrossoverSeed != 0 {
		crosserOpts = append(crosserOpts, genome.WithSeed(cfg.CrossoverSeed))
	}
	if cfg.PromptCrossoverMode != 0 {
		crosserOpts = append(crosserOpts, genome.WithPromptMode(
			genome.PromptCrossoverMode(cfg.PromptCrossoverMode),
		))
	}
	crosser, err := genome.NewCrossover(crosserOpts...)
	if err != nil {
		return nil, fmt.Errorf("create crossover: %w", err)
	}

	return &mutatorResult{
		rawMutator:   rawMutator,
		adaptiveDist: adaptiveDist,
		genomeMut:    genomeMut,
		crosser:      crosser,
	}, nil
}

// buildPopulation creates the genome population from config.
func buildPopulation(ctx context.Context, base *mutation.Strategy, cfg SystemConfig, mutResult *mutatorResult) (*genome.Population, error) {
	popOpts := []genome.PopulationOption{
		genome.WithPopulationSize(cfg.PopulationSize),
		genome.WithEliteCount(cfg.EliteCount),
		genome.WithMutationRate(cfg.MutationRate),
		genome.WithSurvivalRate(cfg.SurvivalRate),
		genome.WithDiversityThreshold(cfg.DiversityThreshold),
		genome.WithBreedingPoolRatio(cfg.BreedingPoolRatio),
		genome.WithFitnessSharingSampling(50, 30),
	}
	if cfg.PopulationSeed != 0 {
		popOpts = append(popOpts, genome.WithPopulationSeed(cfg.PopulationSeed))
	}
	if cfg.MinMutationRate > 0 {
		popOpts = append(popOpts, genome.WithMinMutationRate(cfg.MinMutationRate))
	}
	if cfg.MaxMutationRate > 0 {
		popOpts = append(popOpts, genome.WithMaxMutationRate(cfg.MaxMutationRate))
	}
	if cfg.MaxStagnantGenerations > 0 {
		popOpts = append(popOpts, genome.WithMaxStagnantGenerations(cfg.MaxStagnantGenerations))
	}
	if cfg.HistoryMaxSize > 0 {
		popOpts = append(popOpts, genome.WithHistoryEnabled(cfg.HistoryMaxSize))
	}
	if cfg.SelectionStrategy != "" {
		popOpts = append(popOpts, genome.WithSelectionStrategy(cfg.SelectionStrategy))
	}

	return genome.NewPopulation(ctx, base, mutResult.genomeMut, popOpts...)
}

// buildAdapterOptions creates GenomePopulationAdapter options from config.
func buildAdapterOptions(ctx context.Context, cfg SystemConfig) ([]GenomeAdapterOption, *scoring.TieredScorer, *scoring.Budget, *scoring.ScoreCache, error) {
	var opts []GenomeAdapterOption

	if cfg.Scorer != nil {
		opts = append(opts, WithAdapterScorer(cfg.Scorer))
	}

	heuristic := cfg.HeuristicScorer
	if heuristic == nil && cfg.Scorer != nil {
		heuristic = cfg.Scorer
	}
	if heuristic == nil {
		// Zero-token mode: no LLM scorer and no heuristic wired. The constant
		// baseline gives the GA a flat fitness landscape — evolution selection
		// is effectively random. This is the accepted backward-compat default
		// (review P0-2: the shadow gate is NOT affected by this heuristic —
		// buildShadowEvaluator only wires a scorer when cfg.Scorer != nil, so
		// the gate stays fail-closed in zero-token mode or uses the ReplayScorer
		// when a store is present).
		log.WarnContext(ctx, "No heuristic scorer configured; evolution runs with a constant baseline (50.0). "+
			"The G2 shadow gate is unaffected (fail-closed until ReplayScorer is wired).",
			"method", "buildAdapterOptions",
		)
		heuristic = genome.ConstantScorer(50.0)
	}

	cache := scoring.NewScoreCache(cfg.ScoreCacheSize)
	cache.SetMaxCacheAge(2) // re-evaluate strategies every 3 generations
	if cfg.MaxLLMCallsPerGeneration <= 0 {
		cfg.MaxLLMCallsPerGeneration = 100
	}
	budget, err := scoring.NewBudget(cfg.MaxLLMCallsPerGeneration)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("create budget: %w", err)
	}

	var llmScorer genome.ScorerFunc
	if cfg.Scorer != nil {
		llmScorer = cfg.Scorer
	}

	tieredCfg := scoring.TieredScorerConfig{
		Cache:           cache,
		Budget:          budget,
		HeuristicScorer: heuristic,
		LLMScorer:       llmScorer,
	}
	tiered, err := scoring.NewTieredScorer(tieredCfg)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("create tiered scorer: %w", err)
	}
	opts = append(opts, WithAdapterTieredScoring(tiered, budget, cache))

	if cfg.MemoryAwareScoringConfig.Enabled && cfg.MemoryExperienceProvider != nil {
		memScorer, err := scoring.NewMemoryAwareScorer(tiered, cfg.MemoryExperienceProvider,
			cfg.MemoryAwareScoringConfig)
		if err != nil {
			log.WarnContext(context.Background(), "failed to create memory-aware scorer, skipping", "method", "buildAdapterOptions",
				"error", err)
		} else {
			opts = append(opts, WithAdapterMemoryAwareScoring(memScorer))
		}
	}

	if cfg.BatchScorer != nil {
		opts = append(opts, WithAdapterBatchScoring(cfg.BatchScorer))
	}

	if cfg.Guardrails != nil {
		opts = append(opts, WithAdapterGuardrails(cfg.Guardrails))
	}

	return opts, tiered, budget, cache, nil
}

// mutatorAdapter adapts a genome.MutatorInterface to evolution.MutatorInterface
// by converting between Strategy types.
type mutatorAdapter struct {
	inner genome.MutatorInterface
}

func (a *mutatorAdapter) Mutate(ctx context.Context, parent Strategy, n int) ([]Strategy, error) {
	ms, err := a.inner.Mutate(ctx, strategyToMutation(&parent), n)
	if err != nil {
		return nil, fmt.Errorf("mutator adapter: %w", err)
	}
	res := make([]Strategy, len(ms))
	for i, m := range ms {
		res[i] = *mutationToStrategy(m)
	}
	return res, nil
}

func strategyToMutation(s *Strategy) *mutation.Strategy {
	if s == nil {
		return nil
	}
	params := make(map[string]any, len(s.Params))
	for k, v := range s.Params {
		params[k] = v
	}
	return &mutation.Strategy{
		ID:                   s.ID,
		Version:              s.Version,
		Params:               params,
		ParentID:             s.ParentID,
		PromptTemplate:       s.PromptTemplate,
		StrategyMutationType: parseMutationType(s.StrategyMutationType),
		MutationDesc:         s.MutationDesc,
		Score:                s.Score,
		CreatedAt:            s.CreatedAt,
	}
}

func mutationToStrategy(s *mutation.Strategy) *Strategy {
	if s == nil {
		return nil
	}
	params := make(map[string]any, len(s.Params))
	for k, v := range s.Params {
		params[k] = v
	}
	return &Strategy{
		ID:                   s.ID,
		Version:              s.Version,
		Params:               params,
		ParentID:             s.ParentID,
		PromptTemplate:       s.PromptTemplate,
		StrategyMutationType: s.StrategyMutationType.String(),
		MutationDesc:         s.MutationDesc,
		Score:                s.Score,
		CreatedAt:            s.CreatedAt,
	}
}

func parseMutationType(s string) mutation.MutationType {
	for _, mt := range []mutation.MutationType{
		mutation.MutationParameter,
		mutation.MutationPrompt,
		mutation.MutationTool,
	} {
		if mt.String() == s {
			return mt
		}
	}
	return mutation.MutationParameter
}

// buildDreamCycle creates the dream cycle orchestrator from config.
// Returns nil without error when dream cycle is not used.
func buildDreamCycle(mutator MutatorInterface, cfg SystemConfig) (*DreamCycle, error) {
	dreamCfg := DefaultDreamCycleConfig()
	dreamCfg.MinTasksBeforeEvolve = cfg.MinTasksBeforeEvolve
	dreamCfg.MaxMutations = cfg.MaxMutations

	var tester TesterInterface
	if cfg.Scorer != nil {
		var err error
		tester, err = NewRegressionTester(cfg.Scorer)
		if err != nil {
			return nil, fmt.Errorf("create regression tester: %w", err)
		}
	}

	dreamOpts := []DreamCycleOption{
		WithDreamCycleConfig(dreamCfg),
	}
	if cfg.Guardrails != nil {
		dreamOpts = append(dreamOpts, WithDreamCycleGuardrails(cfg.Guardrails))
	}
	if cfg.StrategyStore != nil {
		dreamOpts = append(dreamOpts, WithStrategyStore(cfg.StrategyStore))
	}
	if cfg.Metrics != nil {
		dreamOpts = append(dreamOpts, WithDreamCycleMetrics(cfg.Metrics))
	}
	if cfg.HintProvider != nil {
		dreamOpts = append(dreamOpts, WithDreamCycleHintProvider(cfg.HintProvider))
	}

	return NewDreamCycle(nil, mutator, tester, nil, dreamOpts...)
}

// buildScheduler creates the evolution scheduler from config.
func buildScheduler(cfg SystemConfig, popAdapter *GenomePopulationAdapter, dreamCycle *DreamCycle) *EvolutionScheduler {
	schedulerOpts := []SchedulerOption{
		WithTrigger(cfg.SchedulerTrigger),
	}
	if cfg.Guardrails != nil {
		schedulerOpts = append(schedulerOpts, WithSchedulerGuardrails(cfg.Guardrails))
	}

	scheduler := NewEvolutionScheduler(cfg.EventStore, popAdapter, schedulerOpts...)
	scheduler.SetDreamCycle(dreamCycle)
	return scheduler
}

// buildFeedbackRecorder creates a FeedbackRecorder if FeedbackService is set.
func buildFeedbackRecorder(cfg SystemConfig) *FeedbackRecorder {
	if cfg.FeedbackService == nil {
		return nil
	}
	return NewFeedbackRecorder(cfg.FeedbackService)
}

// GenerationActive reports whether any evolution generation is currently
// executing — the GA dream cycle or a population-adapter run. The live-chaos
// loop polls this to honor the GA quiet window (#12 Phase 2).
func (s *WiredEvolutionSystem) GenerationActive() bool {
	if s == nil {
		return false
	}
	if s.DreamCycle != nil && s.DreamCycle.GenerationActive() {
		return true
	}
	return s.PopAdapter != nil && s.PopAdapter.GenerationActive()
}

// NewWiredEvolutionSystem creates and wires a complete evolution system.
func NewWiredEvolutionSystem(base *mutation.Strategy, cfg SystemConfig) (*WiredEvolutionSystem, error) {
	ctx := context.Background()

	mutResult, err := buildMutator(cfg)
	if err != nil {
		return nil, fmt.Errorf("build mutator: %w", err)
	}

	pop, err := buildPopulation(ctx, base, cfg, mutResult)
	if err != nil {
		return nil, fmt.Errorf("build population: %w", err)
	}

	system := &WiredEvolutionSystem{
		Population:   pop,
		Genealogy:    NewPopulationGenealogyRecorder(),
		AdaptiveDist: mutResult.adaptiveDist,
	}

	adapterOpts, tiered, budget, cache, err := buildAdapterOptions(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("build adapter options: %w", err)
	}
	system.TieredScorer = tiered
	system.Budget = budget
	system.ScoreCache = cache

	popAdapter, err := NewGenomePopulationAdapter(pop, mutResult.genomeMut, mutResult.crosser, adapterOpts...)
	if err != nil {
		return nil, fmt.Errorf("create population adapter: %w", err)
	}
	system.PopAdapter = popAdapter

	needDreamCycle := cfg.EnableDreamCycle || cfg.EnableScheduler
	var dreamCycle *DreamCycle
	if needDreamCycle {
		dreamMutator := &mutatorAdapter{inner: mutResult.genomeMut}
		dreamCycle, err = buildDreamCycle(dreamMutator, cfg)
		if err != nil {
			return nil, fmt.Errorf("build dream cycle: %w", err)
		}
		system.DreamCycle = dreamCycle
		dreamCycle.genealogy = system.Genealogy
		dreamCycle.population = pop
	}

	if cfg.StrategyStore != nil {
		system.StrategyStore = cfg.StrategyStore
	}

	// E2: the ASM is built whenever a strategy store exists — no longer
	// gated on RollbackPolicyConfig.Enabled. The rollback policy stays armed
	// or disarmed via LifecycleConfig.RollbackArmed (watch-loop behavior),
	// because the lifecycle AND its gate pipeline must exist even when the
	// rollback net is disarmed: that is exactly the posture where the G2
	// shadow gate re-arms fail-closed (see shadowGateMode in ares_bootstrap).
	if cfg.StrategyStore != nil {
		asm, err := buildActiveStrategyManager(cfg)
		if err != nil {
			return nil, fmt.Errorf("build active strategy manager: %w", err)
		}
		system.ActiveStrategyManager = asm
		popAdapter.activeStrategyMgr = asm
		if system.DreamCycle != nil {
			system.DreamCycle.stateManager = asm
		}
	}

	// B3 fix: ShadowEvaluator is built whenever shadow evaluation is
	// enabled — not only when an LLM scorer exists. buildShadowEvaluator
	// is nil-scorer-safe, and the StrategyLifecycle's G2 shadow gate needs
	// the evaluator instance to exist so it can judge comparisons fed by
	// whichever sampler is active (DreamCycle when enabled). With the old
	// `&& cfg.Scorer != nil` condition the G2 gate silently vanished in
	// every default config (LLM scoring off) — a gate that doesn't exist
	// cannot even pass through.
	if cfg.ShadowEvalConfig.Enabled {
		se := buildShadowEvaluator(cfg, tiered, base)
		system.ShadowEvaluator = se
		// B3 fix: ShadowEvaluator is no longer exclusively tied to DreamCycle.
		// When DreamCycle exists it still gets the evaluator for its internal
		// deploy path, but the StrategyLifecycle also gets it so the G2 gate
		// works independently of whether DreamCycle is enabled.
		if system.DreamCycle != nil {
			system.DreamCycle.shadowEvaluator = se
		}
	}

	if cfg.EnableScheduler && cfg.EventStore != nil {
		system.Scheduler = buildScheduler(cfg, popAdapter, dreamCycle)
		system.Scheduler.SetEnabled(true)
		if dreamCycle != nil {
			dreamCycle.scheduler = system.Scheduler
		}
	}

	// B1/B2/B3 fix: construct the StrategyLifecycle when an
	// ActiveStrategyManager is wired. It wraps the ASM so it is the sole
	// caller of Deploy/Rollback. The lifecycle is injected into the
	// population adapter so Run() submits to it instead of deploying
	// directly. The ShadowEvaluator and (optional) evaluator gates are
	// attached so the verify pipeline works without DreamCycle.
	if system.ActiveStrategyManager != nil {
		aggCfg := DefaultAggregatorConfig()
		lcCfg := DefaultLifecycleConfig()
		if cfg.Lifecycle != nil {
			lcCfg = *cfg.Lifecycle
			// The aggregator mirrors the lifecycle's judging knobs so both
			// stages apply the same cold-start/window semantics from YAML.
			aggCfg = AggregatorConfig{
				WindowSize:            lcCfg.FitnessWindow,
				MinSamplesBeforeJudge: lcCfg.MinSamplesBeforeJudge,
				ColdStartScore:        lcCfg.ColdStartScore,
				Weights:               lcCfg.Weights,
			}
		}
		agg := NewRuntimeFitnessAggregator(nil, aggCfg) // store set later by bootstrap
		lcOpts := []LifecycleOption{}
		// E2: propagate the wiring layer's explicit shadow-gate decision. The
		// gate's absence must be a decision (WithShadowGateDisabled), never an
		// emergent property of nil-checking — the reason travels with it and is
		// reported by the snapshot.
		if cfg.Lifecycle != nil && cfg.Lifecycle.DisableShadowGate {
			lcOpts = append(lcOpts, WithShadowGateDisabled(cfg.Lifecycle.ShadowGateSkipReason))
		}
		if system.ShadowEvaluator != nil {
			lcOpts = append(lcOpts, WithLifecycleShadowEvaluator(system.ShadowEvaluator))
			// P0-9: wire the task-level shadow feeder so the G2 gate has
			// candidate-vs-active comparison evidence when DreamCycle does
			// not feed any. Exactly ONE feeder may own StartShadow/
			// RecordResult: wiring both would let the sampler's StartShadow
			// reset DreamCycle's accumulated comparisons on every Submit.
			//
			// The condition is cfg.EnableDreamCycle, NOT system.DreamCycle
			// == nil: a DreamCycle INSTANCE is built whenever
			// EnableDreamCycle OR EnableScheduler is set (see needDreamCycle
			// above), and bootstrap runs EnableDreamCycle=false with
			// EnableScheduler=true — so a nil-check would skip the sampler in
			// every production config, i.e. exactly the case P0-9 exists to
			// fix. Locked by TestWiring_ShadowSampler_WiredInBootstrapShape.
			if !cfg.EnableDreamCycle {
				// W2: the replay evidence window width is configurable
				// (ShadowEvaluationConfig.ReplayWindowSpan). Zero keeps the
				// scorer's 10-minute default — an operator who never sets it
				// gets the same evidence granularity as before.
				lcOpts = append(lcOpts, WithLifecycleShadowSampler(
					NewShadowSampler(system.ShadowEvaluator, cfg.ShadowEvalConfig.MinSamples,
						WithReplayWindowSpan(cfg.ShadowEvalConfig.ReplayWindowSpan)),
				))
			}
		}
		if cfg.Metrics != nil {
			lcOpts = append(lcOpts, WithLifecycleMetrics(cfg.Metrics))
		}
		lc := NewStrategyLifecycle(system.ActiveStrategyManager, agg, lcCfg, lcOpts...)
		system.Lifecycle = lc
		popAdapter.lifecycle = lc
	}

	system.FeedbackRecorder = buildFeedbackRecorder(cfg)
	if system.FeedbackRecorder != nil {
		popAdapter.feedbackRecorder = system.FeedbackRecorder
	}

	if cfg.Metrics != nil {
		popAdapter.metrics = cfg.Metrics
		system.Metrics = cfg.Metrics
		if system.DreamCycle != nil {
			system.DreamCycle.metrics = cfg.Metrics
		}
	}

	return system, nil
}

// buildActiveStrategyManager creates the active strategy manager.
func buildActiveStrategyManager(cfg SystemConfig) (*ActiveStrategyManager, error) {
	rpc := cfg.RollbackPolicyConfig
	var rbOpts []RollbackOption
	if rpc.DegradationThreshold > 0 {
		rbOpts = append(rbOpts, WithDegradationThreshold(rpc.DegradationThreshold))
	}
	if rpc.WindowSize > 0 {
		rbOpts = append(rbOpts, WithRollbackWindowSize(rpc.WindowSize))
	}
	if rpc.MinSamples > 0 {
		rbOpts = append(rbOpts, WithMinRollbackSamples(rpc.MinSamples))
	}
	rollbackPolicy := NewRollbackPolicy(rbOpts...)

	asmOpts := []ASMOption{}
	if cfg.Guardrails != nil {
		asmOpts = append(asmOpts, WithASMGuardrails(cfg.Guardrails))
	}
	return NewActiveStrategyManager(cfg.StrategyStore, rollbackPolicy, asmOpts...)
}

// buildShadowEvaluator creates the shadow evaluator with optional scorer.
func buildShadowEvaluator(cfg SystemConfig, tiered *scoring.TieredScorer, baseStrategy *mutation.Strategy) *ShadowEvaluator {
	// The tiered scorer caches by strategy hash for the whole generation, so the
	// FIRST comparison populates the cache and every later one is a cache hit
	// returning the identical score. That makes the scorer effectively
	// deterministic even with a temperature>0 LLM behind it, which the previous
	// code only flagged when an explicit seed was configured. Record it here so
	// the warning below reflects what actually happens.
	if tiered != nil && cfg.Scorer != nil {
		cfg.ShadowEvalConfig.DeterministicScorer = true
	}
	shadowEval := NewShadowEvaluator(cfg.ShadowEvalConfig)
	shadowEval.SetActiveStrategy(baseStrategy)
	// Shadow scoring is budget-gated ONLY when an LLM scorer is actually
	// wired (cfg.Scorer != nil ⇔ evolution.llm_scoring enabled). A raw
	// cfg.Scorer here would let every Submit's Prime run minSamples×2 LLM
	// calls with zero accounting against MaxLLMCallsPerGeneration (review
	// finding #1); TieredScorer instead enforces the budget
	// (TryRecordLLMCall), reuses the per-generation score cache, and falls
	// back to the heuristic when the budget is exhausted.
	//
	// Without an LLM scorer the tiered scorer is heuristic-only
	// (ConstantScorer 50): every comparison would be an exact tie, which is
	// meaningless evidence. With cfg.Scorer==nil we therefore do NOT wire the
	// tiered heuristic here. In zero-LLM mode the independent evidence source
	// is the DETERMINISTIC scorer (C2.6): bootstrap sets
	// DeterministicScorerEnabled so the G2 gate registers, and the serve layer
	// (cmd/ares/peer_mode.go) wires that scorer onto this evaluator once the
	// runtime ExecutionAttribution exists — the attribution is created after
	// NewWiredEvolutionSystem, so it cannot be injected through cfg here.
	// Until that runtime wiring, the scorer is UNSET and the sampler no-ops
	// (G2 stays fail-closed); this also keeps the manual-RecordResult test
	// path (unit + closure) working.
	if cfg.Scorer != nil {
		if tiered != nil {
			shadowEval.SetShadowScorer(func(ctx context.Context, s *mutation.Strategy) float64 {
				score, _, err := tiered.Score(ctx, s)
				if err != nil {
					log.WarnContext(ctx, "shadow scorer failed, treating as score 0", "method", "buildShadowEvaluator", "strategy_id", s.ID, "error", err)
					return 0
				}
				return score
			})
		} else {
			scorer := cfg.Scorer
			shadowEval.SetShadowScorer(func(_ context.Context, s *mutation.Strategy) float64 {
				return scorer(s)
			})
		}
	}
	if cfg.ShadowEvalConfig.DeterministicScorer {
		log.Warn("shadow evaluator: scorer is deterministic — comparisons are identical, MinSamples is satisfied by repetition, not by independent evidence",
			"min_samples", cfg.ShadowEvalConfig.MinSamples,
			"reason", "per-generation score cache and/or fixed LLM seed",
		)
	}
	log.InfoContext(context.Background(), "shadow evaluation enabled", "method", "buildShadowEvaluator",
		"min_samples", cfg.ShadowEvalConfig.MinSamples,
		"min_win_rate", cfg.ShadowEvalConfig.MinWinRate,
		"active_strategy", baseStrategy.ID,
		"budget_gated", cfg.Scorer != nil && tiered != nil,
	)
	return shadowEval
}

// guidanceHintAdapter adapts an evolution.GuidanceProvider to mutation.HintProvider.
type guidanceHintAdapter struct {
	inner GuidanceProvider
}

func (a *guidanceHintAdapter) HintsForTask(ctx context.Context, taskType string, limit int) ([]mutation.EvolutionHint, error) {
	hints, err := a.inner.HintsForTask(ctx, taskType, limit)
	if err != nil {
		return nil, err
	}
	res := make([]mutation.EvolutionHint, len(hints))
	for i, h := range hints {
		res[i] = mutation.EvolutionHint{
			ID:                  h.ID,
			TaskType:            h.TaskType,
			Problem:             h.Problem,
			Solution:            h.Solution,
			Constraints:         h.Constraints,
			FailedPatterns:      h.FailedPatterns,
			PreferredTools:      h.PreferredTools,
			PromptSnippets:      h.PromptSnippets,
			ParamHints:          h.ParamHints,
			Confidence:          h.Confidence,
			SourceExperienceIDs: h.SourceExperienceIDs,
		}
	}
	return res, nil
}

func (a *guidanceHintAdapter) RecordStrategyOutcome(ctx context.Context, outcome mutation.StrategyOutcome) error {
	return a.inner.RecordStrategyOutcome(ctx, StrategyOutcome{
		StrategyID:    outcome.StrategyID,
		TaskType:      outcome.TaskType,
		Success:       outcome.Success,
		Score:         outcome.Score,
		Cost:          outcome.Cost,
		LatencyMs:     outcome.LatencyMs,
		MutationType:  outcome.MutationType,
		ExperienceIDs: outcome.ExperienceIDs,
		Timestamp:     outcome.Timestamp,
	})
}

// wrapGuidanceProvider wraps an evolution GuidanceProvider around a raw mutator
// using mutation.NewExperienceGuidedMutator.
func wrapGuidanceProvider(provider GuidanceProvider, raw *mutation.Mutator) genome.MutatorInterface {
	adaptedProvider := &guidanceHintAdapter{inner: provider}
	guided, err := mutation.NewExperienceGuidedMutator(raw, adaptedProvider)
	if err != nil {
		log.WarnContext(context.Background(), "failed to create ExperienceGuidedMutator, falling back to raw mutator", "method", "wrapGuidanceProvider",
			"error", err)
		return raw
	}
	log.InfoContext(context.Background(), "experience-guided mutation enabled", "method", "wrapGuidanceProvider",
		"provider", fmt.Sprintf("%T", provider),
	)
	return guided
}

// RegisterScheduler attaches the system's scheduler to its EventStore by
// subscribing for agent lifecycle events. Returns nil if no scheduler is
// configured.
func RegisterScheduler(system *WiredEvolutionSystem) error {
	if system == nil || system.Scheduler == nil {
		return nil
	}
	system.Scheduler.Register()
	return nil
}

// Shutdown gracefully shuts down the evolution scheduler if configured.
func Shutdown(system *WiredEvolutionSystem) {
	if system != nil && system.Scheduler != nil {
		system.Scheduler.Shutdown()
	}
}

// BestStrategyFromSystem returns the highest-scoring strategy from the population.
func BestStrategyFromSystem(system *WiredEvolutionSystem) (*mutation.Strategy, error) {
	if system == nil || system.Population == nil {
		return nil, errors.New("system or population is nil")
	}
	stats := system.Population.Stats()
	if stats.Size == 0 {
		return nil, errors.New("population is empty")
	}
	return system.Population.Best(), nil
}

// RunIdleEvolution runs N generations of idle evolution on the wired system.
func RunIdleEvolution(ctx context.Context, system *WiredEvolutionSystem, n int) error {
	if system == nil || system.PopAdapter == nil || system.Population == nil {
		return errors.New("system, pop adapter, and population must not be nil")
	}

	for gen := 0; gen < n; gen++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Capture parent snapshot BEFORE evolving so lineage can reference
		// pre-evolution agent scores for ScoreImprovement computation.
		var parentSnapshot []*mutation.Strategy
		if system.Genealogy != nil {
			parentSnapshot, _ = system.Population.Snapshot()
		}

		if err := system.PopAdapter.Run(ctx); err != nil {
			log.WarnContext(ctx, "generation produced guardrail warning, continuing", "method", "RunIdleEvolution", "generation", system.Population.Generation,
				"run_iteration", gen,
				"error", err,
			)
		}

		if system.Genealogy != nil {
			_, err := RecordPopulationLineage(ctx, system.Population, system.Genealogy, parentSnapshot, gen)
			if err != nil {
				log.WarnContext(ctx, "failed to record lineage", "method", "RunIdleEvolution", "generation", system.Population.Generation,
					"run_iteration", gen,
					"error", err,
				)
			}
		}

		// Run reflection cycle to analyze evolution patterns.
		if system.Reflector != nil && system.HypothesisGen != nil {
			history := system.Population.History()
			if len(history) > 0 {
				agents, _ := system.Population.Snapshot()
				ref, err := system.Reflector.Reflect(ctx, history, agents)
				if err != nil {
					log.WarnContext(ctx, "reflection failed, skipping", "method", "RunIdleEvolution", "generation", system.Population.Generation,
						"run_iteration", gen,
						"error", err,
					)
				} else if ref != nil && len(ref.Recommendations) > 0 {
					hyps := system.HypothesisGen.Generate(ctx, ref)
					if len(hyps) > 0 {
						log.InfoContext(ctx, "generated hypotheses from reflection", "method", "RunIdleEvolution", "generation", system.Population.Generation,
							"run_iteration", gen,
							"count", len(hyps),
						)
					}
				}
			}
		}

		// Apply meta-controller tuning to self-adapt evolution hyperparameters.
		if system.MetaCtrl != nil {
			genome.ApplyMetaToPopulation(system.Population, system.MetaCtrl)
		}

		// Phase 6: Diff Engine — compare old/new snapshots, generate patches,
		// and submit to Coordinator for evaluation and application. Every
		// patch is attributed to the current best strategy so the coordinator
		// and runtime can A/B compare it against the active one.
		if system.DiffReg != nil && system.Coordinator != nil && system.GenomeReg != nil {
			strategyID := ""
			if best := system.Population.BestStrategy(); best != nil {
				strategyID = best.ID
			}
			diffPatches, dErr := generateDiffPatches(ctx, system.GenomeReg, system.DiffReg, 3, strategyID)
			if dErr != nil {
				log.WarnContext(ctx, "diff engine failed, continuing", "method", "RunIdleEvolution", "error", dErr)
			} else {
				for _, dp := range diffPatches {
					system.Coordinator.Submit(coordinator.PatchProposal{
						Patch:     dp,
						Source:    coordinator.SourceGA,
						Reason:    "GA: evolution generated structural change",
						Priority:  6,
						Fitness:   system.Population.Stats().BestScore,
						Timestamp: time.Now(),
					})
				}
				if len(diffPatches) > 0 {
					system.Coordinator.Evaluate(ctx)
				}
			}
		}

		// Run the post-generation hook (promotion, report, etc.).
		if system.AfterGeneration != nil {
			if err := system.AfterGeneration(ctx, gen, system); err != nil {
				log.WarnContext(ctx, "AfterGeneration hook failed", "method", "RunIdleEvolution", "generation", system.Population.Generation,
					"run_iteration", gen,
					"error", err,
				)
			}
		}
	}

	// Run the post-run hook for final report generation.
	if system.AfterRun != nil {
		if err := system.AfterRun(ctx, system); err != nil {
			log.WarnContext(ctx, "AfterRun hook failed", "method", "RunIdleEvolution", "error", err)
		}
	}

	return nil
}

// generateDiffPatches mutates each registered genome, snapshots each mutated
// candidate, and diffs the candidate snapshot against the parent snapshot to
// produce RuntimePatches.
//
// Algorithm per genome:
//  1. Snapshot parent (old).
//  2. Mutate → nChildren candidates.
//  3. For each candidate: Snapshot candidate (new), Diff(old, new).
//  4. Collect non-empty patches.
//
// Args:
//   - ctx        - timeout and cancellation context.
//   - genomeReg  - registry of evolvable genomes.
//   - diffReg    - registry of genome-specific differs.
//   - nChildren  - number of mutation candidates per genome (must be > 0).
//   - strategyID - the mutation.Strategy ID these patches are attributed to.
//     Must be non-empty; a patch without a strategy cannot be A/B compared,
//     so generateDiffPatches fails fast rather than emit unattributable
//     patches (deployment pipelines MUST NOT invent one).
//
// Returns:
//   - patches - non-empty RuntimePatches from successful mutations, each stamped
//     with the supplied strategyID.
//   - err     - non-nil if nChildren is invalid or strategyID is empty.
func generateDiffPatches(
	ctx context.Context,
	genomeReg *evogenome.Registry,
	diffReg *diff.Registry,
	nChildren int,
	strategyID string,
) ([]patch.RuntimePatch, error) {
	if nChildren <= 0 {
		return nil, fmt.Errorf("generateDiffPatches: nChildren must be > 0, got %d", nChildren)
	}
	if strategyID == "" {
		return nil, fmt.Errorf("generateDiffPatches: strategyID must not be empty")
	}

	var allPatches []patch.RuntimePatch

	for _, name := range genomeReg.List() {
		g, err := genomeReg.Get(name)
		if err != nil {
			continue
		}

		differ, err := diffReg.Get(name)
		if err != nil {
			continue
		}

		// Step 1: Snapshot parent.
		oldSnap, err := g.Snapshot(ctx)
		if err != nil {
			log.WarnContext(ctx, "parent snapshot failed, skipping", "method", "generateDiffPatches",
				"genome", name, "error", err)
			continue
		}
		if oldSnap == nil {
			continue
		}

		// Step 2: Mutate → nChildren candidates.
		children, err := g.Mutate(ctx, nChildren)
		if err != nil {
			log.WarnContext(ctx, "mutate failed, skipping", "method", "generateDiffPatches",
				"genome", name, "error", err)
			continue
		}

		// Step 3: For each candidate, Snapshot + Diff against parent.
		for _, child := range children {
			newSnap, err := child.Snapshot(ctx)
			if err != nil {
				log.WarnContext(ctx, "child snapshot failed, skipping", "method", "generateDiffPatches",
					"genome", name, "error", err)
				continue
			}
			if newSnap == nil {
				continue
			}

			patches, err := differ.Diff(ctx, oldSnap, newSnap)
			if err != nil {
				log.WarnContext(ctx, "diff failed, skipping", "method", "generateDiffPatches",
					"genome", name, "error", err)
				continue
			}

			for i := range patches {
				patches[i].StrategyID = strategyID
			}
			allPatches = append(allPatches, patches...)
		}
	}

	return allPatches, nil
}
