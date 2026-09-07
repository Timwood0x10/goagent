package evolution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Timwood0x10/ares/internal/agents"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// EvolutionMode selects the strategy evolution algorithm used by DreamCycle.
type EvolutionMode int

const (
	// ModeEvolutionStrategy uses the current (1+λ) evolution strategy:
	// mutate parent → test candidates → deploy best.
	ModeEvolutionStrategy EvolutionMode = iota

	// ModeGeneticAlgorithm uses the full genetic algorithm pipeline:
	// population → selection → crossover → mutation → score → next generation.
	// Requires genome.Population to be initialized via GA config fields.
	ModeGeneticAlgorithm
)

// ErrAllCandidatesRejected is returned by findWinner when no candidate
// passes the win-rate threshold during quick-reject or full evaluation.
// Callers should treat this as "no winner" rather than a hard error.
var ErrAllCandidatesRejected = errors.New("dream cycle: all candidates rejected")

// DreamCycleConfig holds configuration for the dream cycle orchestrator.
type DreamCycleConfig struct {
	// Enabled is the master switch for dream cycle execution.
	Enabled bool

	// MinTasksBeforeEvolve is the minimum number of completed tasks before first evolution.
	MinTasksBeforeEvolve int

	// MinScoreDrop is the score drop threshold to trigger evolution (e.g., 0.15 = 15% drop).
	MinScoreDrop float64

	// MaxMutations is the maximum number of candidate strategies generated per cycle.
	MaxMutations int

	// MinWinRate is the minimum win rate required to accept a mutation.
	MinWinRate float64

	// Cooldown is the minimum time between consecutive dream cycles.
	Cooldown time.Duration

	// TaskSampleSize is the number of scoring runs per strategy for the final evaluation.
	// Default 50. With adaptive batching, actual calls may be less.
	TaskSampleSize int

	// QuickRejectRuns is the number of runs for the first-pass screening.
	// Candidates below MinWinRate after this many runs are discarded without full eval.
	// Default 5. Set to 0 to skip quick rejection.
	QuickRejectRuns int

	// EvolutionMode selects the evolution algorithm: ModeEvolutionStrategy or ModeGeneticAlgorithm.
	// Default: ModeEvolutionStrategy (backward compatible).
	EvolutionMode EvolutionMode

	// GA config (only used when EvolutionMode == ModeGeneticAlgorithm):

	// PopulationSize is the number of individuals in the GA population.
	// Default: 20.
	PopulationSize int

	// EliteCount is the number of top individuals preserved each generation.
	// Default: 3.
	EliteCount int

	// MutationRate is the probability of mutating each offspring [0, 1].
	// Default: 0.2.
	MutationRate float64

	// SurvivalRate is the fraction of population that survives each generation [0, 1].
	// Default: 0.6.
	SurvivalRate float64

	// SelectionStrategy selects the parent selection algorithm.
	// Supported: "tournament", "rank", "roulette", "sus", "truncation", "" (random).
	// Default: "tournament".
	SelectionStrategy string

	// TournamentSize is the number of competitors per tournament (only for tournament selection).
	// Default: 3.
	TournamentSize int

	// MaxGenerations is the maximum number of GA generations to run.
	// 0 means unlimited (run until manually stopped).
	MaxGenerations int

	// TargetFitness stops evolution when the best score reaches this threshold.
	// 0 means no target (run until MaxGenerations).
	TargetFitness float64

	// CrossoverType selects the parameter recombination strategy for GA evolution.
	// Supported: "uniform", "two_point", "segment". Default: "uniform".
	CrossoverType string

	// SteadyState enables steady-state GA mode: each generation replaces only
	// a fraction of the population (SteadyStateReplaceRate) instead of full
	// generational replacement. Default: false (full generational GA).
	SteadyState bool

	// SteadyStateReplaceRate is the fraction of the population replaced each
	// generation in steady-state mode [0, 1]. Default: 0.3.
	// Only used when SteadyState is true.
	SteadyStateReplaceRate float64
}

// parseCrossoverType converts a string to the corresponding genome.CrossoverType.
// Returns CrossoverUniform for unknown values (safe default).
func parseCrossoverType(s string) genome.CrossoverType {
	switch s {
	case "two_point":
		return genome.CrossoverTwoPoint
	case "segment":
		return genome.CrossoverSegment
	default:
		return genome.CrossoverUniform
	}
}

// DefaultDreamCycleConfig returns sensible defaults for dream cycle configuration.
// ES mode is the default for backward compatibility.
func DefaultDreamCycleConfig() DreamCycleConfig {
	return DreamCycleConfig{
		Enabled:              false,
		MinTasksBeforeEvolve: 10,
		MinScoreDrop:         0.15,
		MaxMutations:         3,
		MinWinRate:           0.55,
		Cooldown:             5 * time.Minute,
		TaskSampleSize:       50,
		QuickRejectRuns:      5,

		// GA defaults (used when EvolutionMode == ModeGeneticAlgorithm)
		EvolutionMode:          ModeEvolutionStrategy,
		PopulationSize:         20,
		EliteCount:             3,
		MutationRate:           0.2,
		SurvivalRate:           0.6,
		SelectionStrategy:      "tournament",
		TournamentSize:         3,
		MaxGenerations:         0, // unlimited
		TargetFitness:          0, // no target
		CrossoverType:          "uniform",
		SteadyState:            false,
		SteadyStateReplaceRate: 0.3,
	}
}

// DreamCycleOption configures a DreamCycle instance.
type DreamCycleOption func(*DreamCycle) error

// WithDreamCycleConfig applies a full DreamCycleConfig to the DreamCycle.
//
// Args:
//
//	cfg - the configuration to apply.
//
// Returns:
//
//	DreamCycleOption - the option function.
func WithDreamCycleConfig(cfg DreamCycleConfig) DreamCycleOption {
	return func(dc *DreamCycle) error {
		dc.config = cfg
		return nil
	}
}

// DreamCycle orchestrates the full autonomous evolution loop.
// It connects: Callback trigger -> Flight->Exp Adapter -> Scheduler ->
// Mutator -> Arena Regression -> Genealogy recording.
// In GA mode, it uses genome.Population for full genetic algorithm cycles.
type DreamCycle struct {
	scheduler       *EvolutionScheduler
	mutator         MutatorInterface
	tester          TesterInterface
	genealogy       GenealogyRecorder
	strategyStore   StrategyStore
	guardrails      *EvolutionGuardrails
	shadowEvaluator *ShadowEvaluator
	stateManager    *ActiveStrategyManager
	metrics         MetricsRecorder
	hintProvider    mutation.HintProvider
	population      *genome.Population
	crosser         *genome.Crossover
	config          DreamCycleConfig
	mu              sync.Mutex
	runMu           sync.Mutex // serializes Run() to prevent double-evolution (EV-01)
	// generating reports whether an evolution cycle body is currently
	// executing. Read via GenerationActive by the live-chaos guard (#12:
	// GA quiet window) — chaos must not inject while GA is mid-generation.
	generating atomic.Bool
	taskCount  int64
	lastCycle  time.Time
}

// NewDreamCycle creates a new dream cycle orchestrator with required dependencies.
//
// All dependencies must be non-nil except genealogy which is optional (lineage
// recording will be skipped if nil).
//
// When EvolutionMode is ModeGeneticAlgorithm, a genome.Population is initialized
// automatically from the GA configuration fields in DreamCycleConfig.
//
// Args:
//
//	scheduler - the evolution scheduler that triggers this cycle.
//	mutator - the strategy mutator for generating candidate variants.
//	tester - the arena regression tester for evaluating candidates.
//	genealogy - optional recorder for strategy lineage (may be nil).
//	opts - optional configuration functions.
//
// Returns:
//
//	*DreamCycle - the configured dream cycle instance.
//	error - non-nil if required dependencies are missing or GA initialization fails.
func NewDreamCycle(
	scheduler *EvolutionScheduler,
	mutator MutatorInterface,
	tester TesterInterface,
	genealogy GenealogyRecorder,
	opts ...DreamCycleOption,
) (*DreamCycle, error) {
	if mutator == nil {
		return nil, errors.New("mutator is required")
	}
	// scheduler and tester may be nil at construction time and wired later
	// via direct field assignment (e.g., in NewWiredEvolutionSystem).
	// Run() checks them at invocation time before use.

	dc := &DreamCycle{
		scheduler: scheduler,
		mutator:   mutator,
		tester:    tester,
		genealogy: genealogy,
		config:    DefaultDreamCycleConfig(),
	}

	for _, opt := range opts {
		if err := opt(dc); err != nil {
			return nil, fmt.Errorf("dream cycle option: %w", err)
		}
	}

	// Initialize GA population and crosser if in GA mode.
	if dc.config.EvolutionMode == ModeGeneticAlgorithm {
		if err := dc.initGAPopulation(context.Background()); err != nil {
			return nil, fmt.Errorf("init GA population: %w", err)
		}
		crosser, err := genome.NewCrossover(genome.WithCrossoverType(parseCrossoverType(dc.config.CrossoverType)))
		if err != nil {
			return nil, fmt.Errorf("new crossover: %w", err)
		}
		dc.crosser = crosser
	}

	return dc, nil
}

// Run executes one full dream cycle when triggered by the scheduler.
//
// In ES mode (default): mutate parent → test candidates → deploy best.
// In GA mode: score population → evolve (selection/crossover/mutation) → deploy best.
//
// This is the main orchestration method that coordinates all evolution components.
//
// Args:
//
//	ctx - operation context for cancellation and timeout.
//	data - callback data from the triggering event.
//
// Returns:
//
//	error - non-nil if a critical error occurs during orchestration.
//
//nolint:gocyclo // Complex evolutionary cycle orchestration with multiple phases
func (dc *DreamCycle) Run(ctx context.Context, data CallbackData) error {
	// Serialize Run to prevent double-evolution (EV-01).
	dc.runMu.Lock()
	defer dc.runMu.Unlock()

	// Mark the generation window for the live-chaos pause gate.
	dc.generating.Store(true)
	defer dc.generating.Store(false)

	// Enforce a max duration for the entire cycle to prevent hangs.
	cycleCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	// Increment task counter unconditionally for threshold tracking.
	// Also read Enabled under lock to prevent data races.
	dc.mu.Lock()
	dc.taskCount++
	taskCount := dc.taskCount
	lastCycle := dc.lastCycle
	enabled := dc.config.Enabled
	dc.mu.Unlock()

	if !enabled {
		slog.DebugContext(ctx, "[DreamCycle] Disabled, skipping cycle")
		return nil
	}

	// Check cooldown between cycles.
	if !lastCycle.IsZero() && time.Since(lastCycle) < dc.config.Cooldown {
		slog.DebugContext(ctx, "[DreamCycle] Cooldown active, skipping",
			"last_cycle", lastCycle.Format(time.RFC3339),
			"cooldown", dc.config.Cooldown)
		return nil
	}

	// Runtime guard: scheduler and tester may be nil when wired lazily.
	if dc.scheduler == nil {
		slog.WarnContext(ctx, "[DreamCycle] Scheduler not wired yet, skipping cycle")
		return nil
	}
	if dc.tester == nil {
		slog.WarnContext(ctx, "[DreamCycle] Tester not wired yet, skipping cycle")
		return nil
	}

	if taskCount < int64(dc.config.MinTasksBeforeEvolve) {
		slog.DebugContext(ctx, "[DreamCycle] Not enough tasks yet",
			"task_count", taskCount,
			"min_required", dc.config.MinTasksBeforeEvolve)
		return nil
	}

	// Delegate evolution decision to scheduler's exported ShouldEvolve method.
	if !dc.scheduler.ShouldEvolve(ctx, data) {
		return nil
	}

	// Route to GA or ES path based on EvolutionMode.
	if dc.config.EvolutionMode == ModeGeneticAlgorithm {
		return dc.runGAEvolution(ctx, cycleCtx, data)
	}

	return dc.runESEvolution(ctx, cycleCtx, data, taskCount)
}

// candidateResult holds an evaluated candidate strategy with its test results.
type candidateResult struct {
	strategy         Strategy
	winRate          float64
	scoreImprovement float64
}

// initGAPopulation initializes a genome.Population from the current active strategy
// and GA configuration. Called during NewDreamCycle when EvolutionMode is GA.
func (dc *DreamCycle) initGAPopulation(ctx context.Context) error {
	parent, err := dc.getCurrentStrategy(ctx)
	if err != nil {
		return fmt.Errorf("get current strategy for GA population: %w", err)
	}

	base := evolutionToMutationStrategy(parent)
	genMutator := &genomeMutatorAdapter{inner: dc.mutator}
	pop, err := genome.NewPopulation(ctx, &base, genMutator,
		genome.WithPopulationSize(dc.config.PopulationSize),
		genome.WithEliteCount(dc.config.EliteCount),
		genome.WithMutationRate(dc.config.MutationRate),
		genome.WithSurvivalRate(dc.config.SurvivalRate),
		genome.WithSelectionStrategy(dc.config.SelectionStrategy),
		genome.WithTournamentSelection(dc.config.TournamentSize),
	)
	if err != nil {
		return fmt.Errorf("new population: %w", err)
	}
	dc.population = pop
	return nil
}

// runESEvolution executes the existing (1+λ) evolution strategy path.
// Mutate parent → test candidates → deploy best.
func (dc *DreamCycle) runESEvolution(ctx context.Context, cycleCtx context.Context, data CallbackData, taskCount int64) error {
	popGen := 0
	popSize := 0
	if dc.population != nil {
		popGen = dc.population.CurrentGeneration()
		// Stats().Size is read under the population RLock; doEvolve can
		// replace p.Agents wholesale under the write lock (REVIEW #55).
		popSize = dc.population.Stats().Size
	}
	slog.InfoContext(ctx, "[DreamCycle] Starting ES evolution cycle",
		"agent_id", data.AgentID,
		"task_count", taskCount,
		"trigger", dc.scheduler.TriggerMode().String(),
		"generation", popGen,
		"population_size", popSize)

	// Pre-evolution guardrail check.
	var currentBest float64
	if dc.stateManager != nil {
		if cur := dc.stateManager.Current(); cur != nil {
			currentBest = cur.Score
		}
	}
	if dc.guardrails != nil {
		gen := 0
		unevaluatedCount := 0
		popSize := 0
		if dc.population != nil {
			gen = dc.population.CurrentGeneration()
			agents, _ := dc.population.Snapshot()
			popSize = len(agents)
			for _, a := range agents {
				if !genome.IsScoreEvaluated(a.Score) {
					unevaluatedCount++
				}
			}
		}
		preResult := dc.guardrails.PreEvolveCheck(ctx, currentBest, gen, popSize, unevaluatedCount)
		if preResult.ShouldStop {
			slog.WarnContext(ctx, "[DreamCycle] Pre-evolution guardrails prevent cycle",
				"events", len(preResult.Events))
			return nil
		}
	}

	// Step 1: Get current active strategy as parent for mutation.
	parent, err := dc.getCurrentStrategy(ctx)
	if err != nil {
		slog.WarnContext(ctx, "[DreamCycle] Failed to get active strategy", "error", err)
		return nil
	}
	if parent.ID == "" {
		slog.WarnContext(ctx, "[DreamCycle] No active strategy available, skipping")
		return nil
	}

	// Step 2: Generate candidate mutations.
	candidates, err := dc.mutator.Mutate(cycleCtx, parent, dc.config.MaxMutations)
	if err != nil {
		return fmt.Errorf("mutate strategy: %w", err)
	}
	if len(candidates) == 0 {
		slog.InfoContext(ctx, "[DreamCycle] No candidates generated")
		return nil
	}

	// Step 3: Test each candidate in arena and find best winner.
	winner, err := dc.findWinner(ctx, candidates, parent, data.AgentID)
	if errors.Is(err, ErrAllCandidatesRejected) {
		slog.InfoContext(ctx, "[DreamCycle] No candidate passed win rate threshold",
			"min_win_rate", dc.config.MinWinRate)
		dc.recordFailure(ctx, parent)
		return nil
	}
	if err != nil {
		return fmt.Errorf("arena regression: %w", err)
	}
	if winner == nil {
		slog.InfoContext(ctx, "[DreamCycle] No candidate passed win rate threshold",
			"min_win_rate", dc.config.MinWinRate)
		dc.recordFailure(ctx, parent)
		return nil
	}

	// Step 4: Record lineage.
	if dc.genealogy != nil {
		lineage := StrategyLineage{
			ParentID:         parent.ID,
			ChildID:          winner.strategy.ID,
			MutationType:     "dream_cycle",
			WinRate:          winner.winRate,
			ScoreImprovement: winner.scoreImprovement,
			ParentScore:      parent.Score,
			ChildScore:       winner.scoreImprovement + parent.Score,
			Timestamp:        time.Now().Unix(),
		}
		if err := dc.genealogy.Record(ctx, lineage); err != nil {
			slog.ErrorContext(ctx, "[DreamCycle] Failed to record lineage",
				"error", err)
		}
	}

	// Convert parent Strategy to mutation.Strategy for deployWinner.
	parentMut := evolutionToMutationStrategy(parent)
	return dc.deployWinner(ctx, cycleCtx, data, winner, parentMut)
}

// deployWinner handles the common deployment logic for both ES and GA paths.
// It runs post-evolution guardrails, shadow evaluation, and deploys via stateManager.
func (dc *DreamCycle) deployWinner(
	ctx context.Context,
	cycleCtx context.Context,
	data CallbackData,
	winner *candidateResult,
	parent mutation.Strategy,
) error {
	if winner == nil {
		return nil
	}

	// Post-evolution guardrail check.
	if dc.guardrails != nil {
		gen := 0
		var lineageShares map[string]int
		if dc.population != nil {
			gen = dc.population.CurrentGeneration()
			if agents, _ := dc.population.Snapshot(); len(agents) > 0 {
				lineageShares = computeLineageShares(agents)
			}
		}
		postResult := dc.guardrails.PostEvolveCheckForSource(ctx, "dream_cycle", winner.winRate, gen, lineageShares)
		if postResult.ShouldStop {
			slog.WarnContext(ctx, "[DreamCycle] Post-evolution guardrails block deploy",
				"winner_id", winner.strategy.ID,
				"win_rate", winner.winRate,
				"events", len(postResult.Events))
			return nil
		}
	}

	// Shadow evaluation before deployment.
	if dc.shadowEvaluator != nil {
		mtnWinner := winnerToMutationStrategy(winner)
		if mtnWinner == nil {
			slog.ErrorContext(ctx, "[DreamCycle] winnerToMutationStrategy returned nil, skipping shadow")
			return nil
		}
		parentMutation := parent
		dc.shadowEvaluator.SetActiveStrategy(&parentMutation)
		dc.shadowEvaluator.StartShadow(mtnWinner)

		if dc.shadowEvaluator.HasIndependentScorer() {
			dc.shadowEvaluator.Evaluate(ctx)
		} else {
			dc.shadowEvaluator.RecordResult(parent.Score, winner.scoreImprovement+parent.Score)
		}

		shouldDeploy, report := dc.shadowEvaluator.ShouldDeployLoose()
		// Keep the shadow win-rate gauge current on every comparison
		// batch so /metrics reflects the live shadow gate state instead of a
		// permanently zero gauge.
		if dc.metrics != nil && report != nil {
			dc.metrics.SetEvolutionShadowWinRate(report.WinRate)
		}
		// LOOSE contract (DreamCycle defers on insufficient data;
		// the lifecycle shadow gate uses the STRICT ShouldDeploy): StartShadow
		// resets the results, so the first call here carries a single
		// comparison (total < MinSamples), and a strict reading would veto
		// every early deployment. ShouldDeployLoose therefore returns
		// shouldDeploy=true with an "insufficient samples" report when it
		// cannot judge yet — surface that explicitly, and only block
		// deployment when enough samples actually show the shadow strategy
		// underperforming.
		//
		// The insufficiency bar is RAW comparisons (decisive + ties).
		// TotalComparisons alone is decisive-only since B-3, so an all-tie
		// wall would read as 0 < MinSamples and flip to "proceed" — the
		// opposite of the pre-B-3 rejection it replaced. Counting ties in the
		// sample-size check keeps "not enough data → defer" separate from
		// "enough data, all ties → reject" (the latter is ShouldDeployLoose's
		// total==0 rejection).
		insufficient := report == nil || report.TotalComparisons+report.TieCount < dc.shadowEvaluator.minSamples
		switch {
		case insufficient:
			reason := "insufficient samples, proceeding with deployment"
			if report != nil && report.Recommendation != "" {
				reason = report.Recommendation
			}
			slog.InfoContext(ctx, "[DreamCycle] Shadow evaluation cannot conclude, proceeding",
				"candidate_id", winner.strategy.ID,
				"active_id", parent.ID,
				"reason", reason)
			if dc.metrics != nil {
				dc.metrics.RecordEvolutionShadow("insufficient-samples")
			}
		case !shouldDeploy:
			slog.InfoContext(ctx, "[DreamCycle] Shadow evaluation rejects deployment",
				"candidate_id", winner.strategy.ID,
				"active_id", parent.ID,
				"win_rate", report.WinRate,
				"threshold", dc.shadowEvaluator.minWinRate,
				"reason", report.Recommendation)
			if dc.metrics != nil {
				dc.metrics.RecordEvolutionShadow("rejected")
			}
			return nil
		default:
			if dc.metrics != nil {
				dc.metrics.RecordEvolutionShadow("promoted")
			}
		}
	}

	// Deploy via ActiveStrategyManager.
	if dc.stateManager != nil {
		mtnWinner := winnerToMutationStrategy(winner)
		if mtnWinner == nil {
			slog.ErrorContext(ctx, "[DreamCycle] winnerToMutationStrategy returned nil, skipping deploy")
			return nil
		}
		if err := ValidateStrategySize(mtnWinner); err != nil {
			slog.ErrorContext(ctx, "[DreamCycle] Winning strategy exceeds size limits",
				"winner_id", winner.strategy.ID,
				"error", err)
			return nil
		}
		if err := dc.stateManager.Deploy(cycleCtx, mtnWinner); err != nil {
			slog.ErrorContext(ctx, "[DreamCycle] Failed to deploy winning strategy",
				"winner_id", winner.strategy.ID, "error", err)
			return nil
		}
		slog.InfoContext(ctx, "[DreamCycle] Winning strategy deployed",
			"winner_id", winner.strategy.ID,
			"win_rate", winner.winRate)
		if dc.metrics != nil {
			dc.metrics.RecordEvolutionDeploy("success")
			dc.metrics.SetEvolutionScore(winner.strategy.ID, winner.winRate)
		}

		// Record outcome for hint provider.
		if dc.hintProvider != nil {
			outcome := mutation.StrategyOutcome{
				StrategyID:   winner.strategy.ID,
				TaskType:     data.AgentID,
				Success:      true,
				Score:        winner.winRate,
				MutationType: "ga_evolution",
				Timestamp:    time.Now(),
			}
			if err := dc.hintProvider.RecordStrategyOutcome(cycleCtx, outcome); err != nil {
				slog.WarnContext(ctx, "[DreamCycle] Failed to record strategy outcome",
					"error", err)
			}
		}
	}

	dc.mu.Lock()
	dc.lastCycle = time.Now()
	dc.mu.Unlock()

	slog.InfoContext(ctx, "[DreamCycle] Evolution cycle complete",
		"winner_id", winner.strategy.ID,
		"win_rate", winner.winRate,
		"score_improvement", winner.scoreImprovement,
		"trigger", dc.scheduler.TriggerMode().String())

	return nil
}

// WithDreamCycleGuardrails attaches a guardrail checker to the dream cycle.
//
// Args:
//
//	guardrails - the evolution guardrails instance (may be nil to disable).
//
// Returns:
//
//	DreamCycleOption - the option function.
func WithDreamCycleGuardrails(guardrails *EvolutionGuardrails) DreamCycleOption {
	return func(dc *DreamCycle) error {
		dc.guardrails = guardrails
		return nil
	}
}

// WithDreamCycleShadowEvaluator attaches a shadow evaluator for safe deployment.
//
// Args:
//
//	se - the shadow evaluator instance (may be nil to disable).
//
// Returns:
//
//	DreamCycleOption - the option function.
func WithDreamCycleShadowEvaluator(se *ShadowEvaluator) DreamCycleOption {
	return func(dc *DreamCycle) error {
		dc.shadowEvaluator = se
		return nil
	}
}

// WithDreamCycleMetrics attaches a metrics recorder for evolution event counters.
//
// Args:
//
//	metrics - the metrics recorder (may be nil to disable).
//
// Returns:
//
//	DreamCycleOption - the option function.
func WithDreamCycleMetrics(metrics MetricsRecorder) DreamCycleOption {
	return func(dc *DreamCycle) error {
		dc.metrics = metrics
		return nil
	}
}

// WithDreamCycleHintProvider attaches a hint provider for recording strategy
// outcomes after each evolution cycle. The hint provider learns from real
// execution outcomes and provides better hints for future mutations.
//
// Args:
//
//	provider - the hint provider (may be nil to disable).
//
// Returns:
//
//	DreamCycleOption - the option function.
func WithDreamCycleHintProvider(provider mutation.HintProvider) DreamCycleOption {
	return func(dc *DreamCycle) error {
		dc.hintProvider = provider
		return nil
	}
}

// WithDreamCycleTester attaches a regression tester for candidate evaluation.
//
// Args:
//
//	tester - the arena regression tester (may be nil to disable).
//
// Returns:
//
//	DreamCycleOption - the option function.
func WithDreamCycleTester(tester TesterInterface) DreamCycleOption {
	return func(dc *DreamCycle) error {
		dc.tester = tester
		return nil
	}
}

// WithDreamCycleStrategyManager attaches a strategy manager for deployment.
//
// Args:
//
//	mgr - the active strategy manager (may be nil to disable).
//
// Returns:
//
//	DreamCycleOption - the option function.
func WithDreamCycleStrategyManager(mgr *ActiveStrategyManager) DreamCycleOption {
	return func(dc *DreamCycle) error {
		dc.stateManager = mgr
		return nil
	}
}

// WithStrategyStore sets the strategy store for persisting evolved strategies.
//
// Args:
//
//	store - the strategy store implementation (may be nil to disable persistence).
//
// Returns:
//
//	DreamCycleOption - the option function.
func WithStrategyStore(store StrategyStore) DreamCycleOption {
	return func(dc *DreamCycle) error {
		dc.strategyStore = store
		return nil
	}
}

// getCurrentStrategy returns the currently deployed strategy from the strategy store.
// Falls back to a default root strategy if none has been stored yet.
//
// Args:
//
//	ctx - operation context for store lookup.
//
// Returns:
//
//	Strategy - the active strategy, or a default on first run.
//	error - non-nil if store lookup fails.
func (dc *DreamCycle) getCurrentStrategy(ctx context.Context) (Strategy, error) {
	if dc.strategyStore == nil {
		slog.WarnContext(ctx, "[DreamCycle] No strategy store configured; using default")
		return defaultRootStrategy(), nil
	}

	stored, err := dc.strategyStore.GetActive(ctx)
	if err != nil {
		if errors.Is(err, ErrNoActiveStrategy) {
			slog.InfoContext(ctx, "[DreamCycle] No stored strategy found; initializing with default")
			return defaultRootStrategy(), nil
		}
		return Strategy{}, fmt.Errorf("get active strategy: %w", err)
	}

	if stored == nil {
		slog.InfoContext(ctx, "[DreamCycle] No stored strategy found; initializing with default")
		return defaultRootStrategy(), nil
	}

	return *stored, nil
}

// defaultRootStrategy returns a sensible root strategy for first-time initialization.
func defaultRootStrategy() Strategy {
	return Strategy{
		ID:      "root-strategy-v1",
		Name:    "DefaultStrategy",
		Version: 1,
		Params: map[string]any{
			"temperature":  0.7,
			"max_tokens":   4096,
			"retry_count":  3,
			"timeout_secs": 120,
		},
		StrategyMutationType: "",
		Score:                -1,
		CreatedAt:            time.Now(),
	}
}

// currentGeneration returns the current GA generation when a population is
// attached, else 0. Used for guardrail event attribution.
func (dc *DreamCycle) currentGeneration() int {
	if dc.population != nil {
		return dc.population.CurrentGeneration()
	}
	return 0
}

// findWinner tests all candidates in arena and returns the best one above threshold.
//
// Uses a two-stage approach:
//  1. Quick reject: all candidates are screened in parallel with N=QuickRejectRuns (default 5).
//     Those below MinWinRate are discarded.
//  2. Full eval: survivors are evaluated in parallel with N=TaskSampleSize (default 50)
//     and adaptive batching. The best is returned.
func (dc *DreamCycle) findWinner(
	ctx context.Context,
	candidates []Strategy,
	baseline Strategy,
	taskType string,
) (*candidateResult, error) {
	if len(candidates) == 0 {
		return nil, errors.New("dream cycle: no candidates to evaluate")
	}

	// Tool-set guardrail: reject any candidate whose evolved tool whitelist exceeds the
	// guardrail's upper bound (or enables zero tools, when required) BEFORE it
	// is arena-tested. The toolset guard was previously dead code — the method
	// existed but nothing called it, so a mutated Params["tools"] larger than
	// the deployment intended was silently arena-tested and could win. Filtering
	// here is the selection path: a rejected candidate never consumes arena
	// runs and never becomes a winner.
	if dc.guardrails != nil {
		filtered := make([]Strategy, 0, len(candidates))
		rejected := 0
		for _, cand := range candidates {
			// Parsed by the same agents helper the executors use, so the
			// guardrail counts exactly the tools the LLM would be shown.
			tools := agents.ToolNamesFromParams(cand.Params)
			res := dc.guardrails.ValidateToolSet(dc.currentGeneration(), tools)
			if res.ShouldStop {
				rejected++
				slog.WarnContext(ctx, "[DreamCycle] candidate jailed by tool-set guardrail",
					"strategy_id", cand.ID,
					"tool_count", len(tools),
					"events", len(res.Events))
				continue
			}
			filtered = append(filtered, cand)
		}
		if rejected > 0 {
			slog.InfoContext(ctx, "[DreamCycle] tool-set guardrail rejected candidates",
				"rejected", rejected,
				"survivors", len(filtered))
		}
		candidates = filtered
		if len(candidates) == 0 {
			return nil, ErrAllCandidatesRejected
		}
	}

	// Stage 1: Quick reject — screen all candidates in parallel with small N.
	quickRejectN := dc.config.QuickRejectRuns
	survivors := candidates

	if quickRejectN > 0 {
		type quickResult struct {
			candidate Strategy
			winRate   float64
		}

		quickResults := make([]*quickResult, len(candidates))
		g, gCtx := errgroup.WithContext(ctx)

		for i, cand := range candidates {
			i, cand := i, cand
			g.Go(func() error {
				result, err := dc.tester.Run(gCtx, RegressionConfig{
					Candidate:         cand,
					Baseline:          baseline,
					TaskSampleSize:    quickRejectN,
					AdaptiveBatchSize: quickRejectN, // single batch, no adaptive benefit
				})
				if err != nil {
					slog.WarnContext(ctx, "[DreamCycle] Quick reject failed",
						"candidate_id", cand.ID, "error", err)
					return nil // skip on error
				}
				quickResults[i] = &quickResult{candidate: cand, winRate: result.WinRate}
				return nil
			})
		}

		if err := g.Wait(); err != nil {
			return nil, err
		}

		survivors = nil
		for _, qr := range quickResults {
			if qr == nil {
				continue
			}
			if qr.winRate >= dc.config.MinWinRate {
				survivors = append(survivors, qr.candidate)
			} else {
				slog.DebugContext(ctx, "[DreamCycle] Candidate rejected in quick pass",
					"candidate_id", qr.candidate.ID,
					"win_rate", qr.winRate,
					"threshold", dc.config.MinWinRate)
				// Record the rejection as a negative outcome so the hint
				// provider can learn which strategies fail the cheap screen
				// and avoid regenerating them. Best-effort: a recording
				// failure must not abort the cycle.
				dc.recordQuickReject(ctx, qr.candidate, qr.winRate, taskType)
			}
		}

		rejected := len(candidates) - len(survivors)
		if rejected > 0 {
			slog.InfoContext(ctx, "[DreamCycle] Quick reject filtered candidates",
				"total", len(candidates),
				"survivors", len(survivors),
				"rejected", rejected)
		}
	}

	if len(survivors) == 0 {
		return nil, ErrAllCandidatesRejected // all candidates rejected in quick pass (nilnil)
	}

	// Stage 2: Full evaluation — run survivors in parallel with full N.
	type evalResult struct {
		candidateResult
		err error
	}

	evalResults := make([]*evalResult, len(survivors))
	g, gCtx := errgroup.WithContext(ctx)

	for i, cand := range survivors {
		i, cand := i, cand
		g.Go(func() error {
			result, err := dc.tester.Run(gCtx, RegressionConfig{
				Candidate:         cand,
				Baseline:          baseline,
				TaskSampleSize:    dc.config.TaskSampleSize,
				AdaptiveBatchSize: 5,
			})
			if err != nil {
				evalResults[i] = &evalResult{err: err}
				return nil // skip on error
			}
			evalResults[i] = &evalResult{
				candidateResult: candidateResult{
					strategy:         cand,
					winRate:          result.WinRate,
					scoreImprovement: result.CandidateScore - result.BaselineScore,
				},
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	// Pick the best among survivors above threshold — by winRate (evolution semantics).
	var best *candidateResult
	for _, er := range evalResults {
		if er == nil || er.err != nil {
			continue
		}
		if er.winRate < dc.config.MinWinRate {
			continue
		}
		if best == nil || er.winRate > best.winRate {
			cr := er.candidateResult
			best = &cr
		}
	}

	return best, nil
}

// recordFailure logs a failed evolution cycle for future analysis and records
// the failure outcome for hint provider learning.
func (dc *DreamCycle) recordFailure(ctx context.Context, parent Strategy) {
	slog.InfoContext(ctx, "[DreamCycle] Evolution cycle produced no acceptable candidate",
		"parent_id", parent.ID,
		"max_mutations", dc.config.MaxMutations,
		"min_win_rate", dc.config.MinWinRate)

	if dc.hintProvider != nil {
		outcome := mutation.StrategyOutcome{
			StrategyID:   parent.ID,
			Success:      false,
			Score:        parent.Score,
			MutationType: "dream_cycle",
			Timestamp:    time.Now(),
		}
		if err := dc.hintProvider.RecordStrategyOutcome(ctx, outcome); err != nil {
			slog.WarnContext(ctx, "[DreamCycle] Failed to record failure outcome",
				"error", err)
		}
	}
}

// recordQuickReject records a candidate that was discarded by the cheap quick-
// reject screen as a negative StrategyOutcome, so the hint provider can learn
// which strategies fail early and steer future mutations away from them. It is
// best-effort: a recording error is logged and never propagated. No-op when no
// hint provider is wired.
func (dc *DreamCycle) recordQuickReject(ctx context.Context, candidate Strategy, winRate float64, taskType string) {
	if dc.hintProvider == nil {
		return
	}
	outcome := mutation.StrategyOutcome{
		StrategyID:   candidate.ID,
		TaskType:     taskType,
		Success:      false,
		Score:        winRate,
		MutationType: candidate.StrategyMutationType,
		Timestamp:    time.Now(),
	}
	if err := dc.hintProvider.RecordStrategyOutcome(ctx, outcome); err != nil {
		slog.WarnContext(ctx, "[DreamCycle] Failed to record quick-reject outcome",
			"candidate_id", candidate.ID, "error", err)
	}
}

// MetricsRecorder abstracts Prometheus metrics recording for evolution events.
// The observability.PrometheusMetrics type satisfies this interface.
type MetricsRecorder interface {
	RecordEvolutionDeploy(status string)
	RecordEvolutionShadow(result string)
	SetEvolutionScore(strategyID string, score float64)
	// SetEvolutionShadowWinRate publishes the current shadow win rate so
	// the /metrics gauge tracks the live shadow gate state.
	SetEvolutionShadowWinRate(rate float64)
}

// winnerToMutationStrategy converts a candidateResult to a mutation.Strategy
// pointer for deployment via ActiveStrategyManager.
//
// Args:
//
//	result - the evaluated candidate result.
//
// Returns:
//
//	*mutation.Strategy - pointer to the strategy ready for deployment, or nil.
func winnerToMutationStrategy(result *candidateResult) *mutation.Strategy {
	if result == nil {
		return nil
	}
	ms := evolutionToMutationStrategy(result.strategy)
	ms.Score = result.winRate
	return &ms
}

// SetEnabled enables or disables the dream cycle at runtime.
// Thread-safe: uses mutex to protect concurrent access to config.Enabled.
//
// Args:
//
//	enabled - true to enable, false to disable.
func (dc *DreamCycle) SetEnabled(enabled bool) {
	dc.mu.Lock()
	dc.config.Enabled = enabled
	dc.mu.Unlock()
}

// IsEnabled returns whether the dream cycle is currently enabled.
// Thread-safe: uses mutex to protect concurrent access to config.Enabled.
//
// Returns:
//
//	bool - true if enabled, false otherwise.
func (dc *DreamCycle) IsEnabled() bool {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	return dc.config.Enabled
}

// TaskCount returns the number of tasks processed since creation.
// Thread-safe: uses mutex to protect concurrent access.
//
// Returns:
//
//	int64 - the accumulated task count.
func (dc *DreamCycle) TaskCount() int64 {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	return dc.taskCount
}

// GenerationActive reports whether an evolution cycle is currently executing.
// The live-chaos loop polls this to honor the GA quiet window (pause injections
// while a generation is in flight).
func (dc *DreamCycle) GenerationActive() bool {
	return dc != nil && dc.generating.Load()
}
