// genome_wiring.go provides wiring between the genome population system
// and the DreamCycle/EvolutionScheduler orchestration layer.
//
// This file bridges the type gap between genome.Population (which operates
// on *mutation.Strategy) and the evolution package (which uses evolution.Strategy).
// It provides the core GenomePopulationAdapter type, its constructor, and
// configuration options. Runtime logic lives in genome_wiring_run.go and
// genealogy/mutator helpers live in genome_wiring_genealogy.go.

package evolution

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/scoring"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/coordinator"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/diff"
	evogenome "github.com/Timwood0x10/ares/internal/runtime/evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/observability"
)

// BatchScorer scores multiple internal strategies in a single call.
// Used to reduce LLM API calls by batching strategies together.
// The returned slice length must match the input slice length.
type BatchScorer func(ctx context.Context, strategies []*mutation.Strategy) []float64

// GenomePopulationAdapter wraps a genome.Population to implement AdapterRunner.
// It allows the EvolutionScheduler to trigger genome-based evolution cycles
// when agents complete tasks.
//
// When a scorer is set, new offspring (IsScoreEvaluated() == false) are automatically scored
// after each evolution cycle, closing the scoring loop for the scheduler path.
type GenomePopulationAdapter struct {

	// running reports whether Run() is mid-generation; probed by the
	// live-chaos pause gate (#12). See GenerationActive.
	running atomic.Bool
	pop     *genome.Population
	mutator genome.MutatorInterface
	crosser genome.CrossoverInterface
	scorer  func(*mutation.Strategy) float64

	// Scoring infrastructure for cost-controlled evaluation (optional).
	// When set via WithAdapterTieredScoring, Run() uses TieredScorer pipeline
	// instead of the plain scorer path.
	tieredScorer *scoring.TieredScorer
	budget       *scoring.Budget
	scoreCache   *scoring.ScoreCache

	// batchScorer scores all agents in a single call (optional).
	// When set together with tieredScorer and scoreCache, Run() pre-fills
	// the cache with batch-scored values before EvolveAfterScoring, so
	// Phase 1 finds cache hits for all agents — turning N per-agent LLM
	// calls into ceil(N/batchSize) batched calls.
	batchScorer BatchScorer

	// Guardrails for pre/post evolution safety checks (optional).
	// When set via WithAdapterGuardrails, Run() runs safety checks before
	// and after each evolution cycle.
	guardrails *EvolutionGuardrails

	// Memory-aware scorer for evidence-based scoring adjustments (optional).
	// When set via WithAdapterMemoryAwareScoring, Run() wraps the tiered
	// scorer pipeline with memory-aware adjustments, preserving tiered
	// scoring stats and context propagation.
	memoryScorer *scoring.MemoryAwareScorer

	// AdaptiveDist adjusts mutation type probabilities based on observed
	// outcomes from previous evolution cycles (optional). When set, Run()
	// records outcome feedback after each evolution cycle.
	adaptiveDist *mutation.AdaptiveDistribution

	// FeedbackRecorder records strategy outcomes to the experience feedback
	// system for experience reinforcement (optional). When set, Run()
	// records outcome feedback after each evolution cycle.
	feedbackRecorder *FeedbackRecorder

	// ActiveStrategyManager deploys the best-evolved strategy to the
	// runtime so the live agent can consume it. When set, Run() deploys
	// the current best strategy after each evolution cycle. Optional.
	activeStrategyMgr *ActiveStrategyManager

	// Metrics records Prometheus counters for evolution events (optional).
	metrics *observability.PrometheusMetrics

	// lifecycle is the strategy orchestrator (B2 fix). When set, Run()
	// submits the best strategy to the lifecycle instead of deploying
	// directly. The lifecycle runs verify gates before promoting.
	lifecycle *StrategyLifecycle

	// Coordinator bridge — when set, Run() submits evolution results
	// to the new system's coordinator for decision and deployment.
	coordinator *coordinator.EvolutionCoordinator
	diffReg     *diff.Registry
	genomeReg   *evogenome.Registry

	// runMu serializes concurrent Run() calls from the background ticker and
	// the scheduler's OnAgentEnd callback to prevent race conditions on shared
	// mutable state (scorer, coordinator, population).
	runMu sync.Mutex
}

// NewGenomePopulationAdapter creates an adapter around a genome population.
//
// Args:
//
//	pop - the managed population (must not be nil).
//	mutator - the genome-compatible mutator (must not be nil).
//	crosser - the genome-compatible crossover engine (must not be nil).
//
// Returns:
//
//	*GenomePopulationAdapter - the configured adapter.
//	error - non-nil if any required dependency is nil.
func NewGenomePopulationAdapter(
	pop *genome.Population,
	mutator genome.MutatorInterface,
	crosser genome.CrossoverInterface,
	opts ...GenomeAdapterOption,
) (*GenomePopulationAdapter, error) {
	if pop == nil {
		return nil, errors.New("population must not be nil")
	}
	if mutator == nil {
		return nil, errors.New("mutator must not be nil")
	}
	if crosser == nil {
		return nil, errors.New("crosser must not be nil")
	}
	adapter := &GenomePopulationAdapter{
		pop:     pop,
		mutator: mutator,
		crosser: crosser,
	}
	for _, opt := range opts {
		opt(adapter)
	}
	return adapter, nil
}

// GenomeAdapterOption configures a GenomePopulationAdapter.
type GenomeAdapterOption func(*GenomePopulationAdapter)

// WithAdapterScorer sets a scoring function that is called after each evolution
// cycle to assign scores to newly generated offspring (IsScoreEvaluated() == false).
// Without this, the scheduler path produces unevaluated agents that distort
// selection and diversity metrics.
//
// Args:
//
//	scorer - function that takes an internal strategy and returns its fitness score.
//
// Returns:
//
//	GenomeAdapterOption - the configuration function.
func WithAdapterScorer(scorer func(*mutation.Strategy) float64) GenomeAdapterOption {
	return func(a *GenomePopulationAdapter) {
		a.scorer = scorer
	}
}

// WithAdapterTieredScoring configures the adapter to use a TieredScorer pipeline
// instead of the plain scorer. This enables LLM budget control, score caching,
// and automatic fallback from LLM to heuristic scoring.
//
// Args:
//
//	ts - the configured tiered scorer (must not be nil).
//	budget - the budget tracker (must not be nil).
//	cache - the shared score cache (must not be nil).
//
// Returns:
//
//	GenomeAdapterOption - the configuration function.
func WithAdapterTieredScoring(ts *scoring.TieredScorer, budget *scoring.Budget, cache *scoring.ScoreCache) GenomeAdapterOption {
	return func(a *GenomePopulationAdapter) {
		a.tieredScorer = ts
		a.budget = budget
		a.scoreCache = cache
	}
}

// WithAdapterGuardrails sets the evolution guardrails for pre/post safety checks.
// When set, Run() calls PreEvolveCheck before evolution and PostEvolveCheck after.
// Without this, guardrails are disabled and behavior is unchanged.
//
// Args:
//
//	g - the configured guardrails instance (may be nil to disable).
//
// Returns:
//
//	GenomeAdapterOption - the configuration function.
func WithAdapterGuardrails(g *EvolutionGuardrails) GenomeAdapterOption {
	return func(a *GenomePopulationAdapter) {
		a.guardrails = g
	}
}

// WithAdapterMemoryAwareScoring configures the adapter to wrap the tiered
// scorer with memory-aware scoring adjustments. The MemoryAwareScorer adds
// evidence-based bonuses and cost/latency penalties to the fitness score.
//
// This must be used together with WithAdapterTieredScoring. The memory-aware
// scorer wraps the tiered pipeline, preserving all tiered scoring stats
// (cache hits, LLM calls, fallbacks) and proper context propagation.
//
// Args:
//
//	ms - the configured memory-aware scorer (must not be nil).
//
// Returns:
//
//	GenomeAdapterOption - the configuration function.
func WithAdapterMemoryAwareScoring(ms *scoring.MemoryAwareScorer) GenomeAdapterOption {
	return func(a *GenomePopulationAdapter) {
		a.memoryScorer = ms
	}
}

// WithAdapterAdaptiveDistribution sets the adaptive mutation distribution
// for outcome-driven probability adjustment. When set, Run() records
// outcome feedback after each evolution cycle.
//
// Args:
//
//	ad - the adaptive distribution instance (may be nil to disable).
//
// Returns:
//
//	GenomeAdapterOption - the configuration function.
func WithAdapterAdaptiveDistribution(ad *mutation.AdaptiveDistribution) GenomeAdapterOption {
	return func(a *GenomePopulationAdapter) {
		a.adaptiveDist = ad
	}
}

// WithAdapterFeedbackRecorder sets the feedback recorder for experience
// reinforcement. When set, Run() records strategy outcomes to the feedback
// service after each evolution cycle.
//
// Args:
//
//	fr - the feedback recorder instance (may be nil to disable).
//
// Returns:
//
//	GenomeAdapterOption - the configuration function.
func WithAdapterFeedbackRecorder(fr *FeedbackRecorder) GenomeAdapterOption {
	return func(a *GenomePopulationAdapter) {
		a.feedbackRecorder = fr
	}
}

// WithAdapterMetrics sets the metrics recorder for evolution event counters.
//
// Args:
//
//	metrics - the Prometheus metrics instance (may be nil).
//
// Returns:
//
//	GenomeAdapterOption - the configuration function.
func WithAdapterMetrics(metrics *observability.PrometheusMetrics) GenomeAdapterOption {
	return func(a *GenomePopulationAdapter) {
		a.metrics = metrics
	}
}

// WithActiveStrategyManager attaches an ActiveStrategyManager to the adapter.
// When set, Run() deploys the current best strategy to the active strategy
// store after each evolution cycle, enabling the live agent to consume it.
// Without this, evolved strategies are never persisted for runtime use.
//
// Args:
//
//	mgr - the active strategy manager (must not be nil).
//
// Returns:
//
//	GenomeAdapterOption - the configuration function.
func WithActiveStrategyManager(mgr *ActiveStrategyManager) GenomeAdapterOption {
	return func(a *GenomePopulationAdapter) {
		a.activeStrategyMgr = mgr
	}
}

// WithAdapterCoordinator attaches the new system's coordinator bridge to the adapter.
// When set, Run() generates diff patches from the GA population's evolution results
// and submits them to the coordinator for decision and deployment.
//
// Args:
//
//	coord - the evolution coordinator to submit patches to.
//	diffReg - the diff registry for generating patches from genome snapshots.
//	genomeReg - the genome registry with all registered genomes.
//
// Returns:
//
//	GenomeAdapterOption - the configuration function.
func WithAdapterCoordinator(coord *coordinator.EvolutionCoordinator, diffReg *diff.Registry, genomeReg *evogenome.Registry) GenomeAdapterOption {
	return func(a *GenomePopulationAdapter) {
		a.coordinator = coord
		a.diffReg = diffReg
		a.genomeReg = genomeReg
	}
}

// WithAdapterBatchScoring sets a batch scorer that scores all unevaluated
// strategies in a single call before the tiered scorer runs. This pre-fills
// the score cache so the tiered scorer finds cache hits during Phase 1 of
// EvolveAfterScoring, reducing N per-agent LLM calls to ceil(N/batchSize)
// batched calls.
//
// Requires tieredScorer and scoreCache to be set (via WithAdapterTieredScoring).
//
// Args:
//
//	bs - the batch scorer function.
//
// Returns:
//
//	GenomeAdapterOption - the configuration function.
func WithAdapterBatchScoring(bs BatchScorer) GenomeAdapterOption {
	return func(a *GenomePopulationAdapter) {
		a.batchScorer = bs
	}
}

// Population returns the underlying genome population for direct access.
//
// Returns:
//
//	*genome.Population - the managed population.
func (a *GenomePopulationAdapter) Population() *genome.Population {
	return a.pop
}

// PopulationSize returns the current population size for guardrail checks.
func (a *GenomePopulationAdapter) PopulationSize() int {
	if a.pop == nil {
		return 0
	}
	// Stats() reads len(Agents) under the population RLock; doEvolve can
	// replace p.Agents wholesale while holding the write lock, so a bare
	// len(a.pop.Agents) here is a data race (REVIEW #55).
	return a.pop.Stats().Size
}

// PopulationUnevaluated returns how many individuals still carry
// genome.ScoreUnevaluated. It completes the populationInspector contract the
// legacy EvolutionScheduler needs: PreEvolveCheck's only blocking condition is
// an unevaluated majority, and before B2 the scheduler passed a hardcoded 0 —
// so its guardrail could never block regardless of configuration.
func (a *GenomePopulationAdapter) PopulationUnevaluated() int {
	if a.pop == nil {
		return 0
	}
	agents, _ := a.pop.Snapshot()
	return countUnevaluated(agents)
}

// PopulationGeneration returns the current generation number so guardrail
// events carry the real generation instead of a hardcoded 0.
func (a *GenomePopulationAdapter) PopulationGeneration() int {
	if a.pop == nil {
		return 0
	}
	return a.pop.Stats().Generation
}
