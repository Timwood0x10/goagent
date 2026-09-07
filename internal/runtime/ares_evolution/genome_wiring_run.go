// genome_wiring_run.go contains the GenomePopulationAdapter runtime logic:
// the Run cycle, scorer construction, guardrail checkpoints, outcome
// recording, coordinator submission, and related helper functions.
package evolution

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/agents"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/scoring"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/coordinator"
	evogenome "github.com/Timwood0x10/ares/internal/runtime/evolution/genome"
)

// Run executes one atomic genome evolution cycle (EvolveAfterScoring) when
// triggered by scheduler. The atomic API handles pre-scoring, evolution, and
// post-scoring in a single call, eliminating the risk of evolving unevaluated agents.
//
// Args:
//
//	ctx - operation context for cancellation.
//
// Returns:
//
//	error - non-nil if evolution fails.
//
// Run executes one atomic genome evolution cycle (EvolveAfterScoring) when
// triggered by scheduler. The atomic API handles pre-scoring, evolution, and
// post-scoring in a single call, eliminating the risk of evolving unevaluated agents.
// After evolution, the best strategy is deployed to the active strategy store
// (when an ActiveStrategyManager is wired) so the live agent can consume it.
//
// Args:
//
//	ctx - operation context for cancellation.
//
// Returns:
//
//	error - non-nil if evolution fails.
func (a *GenomePopulationAdapter) Run(ctx context.Context) error {
	a.runMu.Lock()
	defer a.runMu.Unlock()

	// Mark the generation window for the live-chaos pause gate (#12 Phase 2).
	a.running.Store(true)
	defer a.running.Store(false)

	scorer := a.buildRunScorer(ctx)
	if a.tieredScorer != nil {
		// Log scoring stats once the cycle returns (mirrors prior defer semantics).
		defer a.logTieredStats(ctx)
	}

	// Capture pre-evolution snapshot for outcome recording when feedback
	// components are wired. This lets us compare offspring scores with
	// their parent scores after evolution.
	var agentsBefore []*mutation.Strategy
	if a.adaptiveDist != nil || a.feedbackRecorder != nil {
		agentsBefore, _ = a.pop.Snapshot()
	}

	if err := a.runPreGuardrails(ctx); err != nil {
		return err
	}

	if err := a.pop.EvolveAfterScoring(ctx, scorer, a.mutator, a.crosser); err != nil {
		return fmt.Errorf("adapter.Run: genome evolve on idle: %w", err)
	}

	// Record outcomes for adaptive distribution and feedback service.
	// This closes the feedback loop: evolution results flow back to
	// update probability distributions and experience rankings.
	if agentsBefore != nil {
		a.recordOutcomesLocked(ctx, agentsBefore)
	}

	if err := a.runPostGuardrails(ctx); err != nil {
		return err
	}

	// Submit evolution results to the new system's coordinator for decision.
	// The Coordinator evaluates and applies patches through the live PatchExecutors
	// (wired by UpdateLiveDAG in serve.go), so genome evolution results flow
	// directly into the running agent's DAG, scheduler, and knowledge config.
	if a.coordinator != nil && a.diffReg != nil && a.genomeReg != nil {
		a.submitToCoordinator(ctx)
	}

	// Submit the best-evolved strategy to the lifecycle orchestrator (B2 fix).
	// When a StrategyLifecycle is wired, it runs the verify-gate pipeline
	// before promoting to ACTIVE. When no lifecycle is wired, fall back to
	// the legacy direct-deploy path (backward compatible).
	if a.lifecycle != nil {
		if best := a.pop.BestStrategy(); best != nil {
			if a.toolSetRejected(ctx, best) {
				log.WarnContext(ctx, "best strategy jailed by tool-set guardrail, not submitted to lifecycle",
					"method", "Run", "strategy_id", best.ID)
			} else {
				a.lifecycle.Submit(ctx, best, a.pop.Stats().Generation)
			}
		}
	} else if a.activeStrategyMgr != nil {
		a.deployBestStrategy(ctx)
	}

	stats := a.pop.Stats()
	log.InfoContext(ctx, "evolution cycle completed", "method", "Run", "generation", stats.Generation,
		"population_size", stats.Size,
		"best_score", stats.BestScore,
		"avg_score", stats.AvgScore,
	)
	return nil
}

// buildRunScorer constructs the scorer used for this evolution cycle.
// It prefers the tiered (cache + budget-gated LLM + heuristic) pipeline and
// falls back to the plain configured scorer when no tiered pipeline exists.
//
// Args:
//
//	ctx - operation context for cancellation.
//
// Returns:
//
//	genome.ScorerFunc - the scorer to use for evolution.
func (a *GenomePopulationAdapter) buildRunScorer(ctx context.Context) genome.ScorerFunc {
	if a.tieredScorer == nil {
		return buildScorer(a.scorer)
	}

	// heuristicScorer is the fallback for when the tiered/memory-aware
	// scorer fails at runtime: we degrade to the adapter-level heuristic
	// instead of returning a constant 50.0, which would make every failed
	// strategy look identically average (M3).
	heuristicScorer := buildScorer(a.scorer)

	// Reset per-generation budget at the start of each cycle.
	a.tieredScorer.ResetForGeneration()

	// Pre-fill cache with batch-scored values before tiered scoring runs.
	// This turns N per-agent LLM calls into ceil(N/batchSize) batched calls.
	if a.batchScorer != nil && a.scoreCache != nil {
		agents, ver := a.pop.Snapshot()
		if len(agents) > 0 {
			scores := a.batchScorer(ctx, agents)
			n := min(len(scores), len(agents))
			for i := 0; i < n; i++ {
				hash, err := scoring.StrategyHash(agents[i])
				if err == nil {
					a.scoreCache.Put(hash, scoring.MakeEntry(hash, scores[i], "batch", 1, 0.9))
				}
			}
			log.DebugContext(ctx, "pre-filled score cache via batch scorer", "method", "Run", "count", n,
				"version", ver,
				"scored", len(scores),
			)
		}
	}

	return func(s *mutation.Strategy) float64 {
		// When memory-aware scorer is set, delegate through it to get
		// evidence-based bonuses and cost/latency penalties.
		if a.memoryScorer != nil {
			score, _, err := a.memoryScorer.Score(ctx, s)
			if err != nil {
				log.WarnContext(ctx, "memory-aware scorer failed, using heuristic", "method", "Run", "error", err,
					"strategy_id", s.ID,
				)
				return heuristicScorer(s)
			}
			return score
		}
		score, _, err := a.tieredScorer.Score(ctx, s)
		if err != nil {
			log.WarnContext(ctx, "tiered scorer failed, using heuristic", "method", "Run", "error", err,
				"strategy_id", s.ID,
			)
			return heuristicScorer(s)
		}
		return score
	}
}

// logTieredStats logs tiered scorer statistics after an evolution cycle.
//
// Args:
//
//	ctx - operation context for cancellation.
func (a *GenomePopulationAdapter) logTieredStats(ctx context.Context) {
	stats := a.tieredScorer.Stats()
	used, max, cacheHits, fallbacks := a.budget.Usage()
	log.InfoContext(ctx, "tiered scoring stats", "method", "Run", "llm_used", used,
		"llm_max", max,
		"cache_hits", cacheHits,
		"fallbacks", fallbacks,
		"tier_stats", stats,
	)
}

// runPreGuardrails executes the pre-evolution safety checkpoint.
// Returns an error (aborting the cycle) when the guardrails demand a stop.
//
// Args:
//
//	ctx - operation context for cancellation.
//
// Returns:
//
//	error - non-nil when the pre-evolve guardrail demands a stop.
func (a *GenomePopulationAdapter) runPreGuardrails(ctx context.Context) error {
	if a.guardrails == nil {
		return nil
	}

	preStats := a.pop.Stats()
	agents, _ := a.pop.Snapshot()
	unevaluated := countUnevaluated(agents)

	preResult := a.guardrails.PreEvolveCheck(ctx,
		preStats.BestScore,
		preStats.Generation,
		preStats.Size,
		unevaluated,
	)

	for _, evt := range preResult.Events {
		log.WarnContext(ctx, "pre-evolve guardrail triggered", "method", "Run", "rule", evt.Rule,
			"level", evt.Level,
			"message", evt.Message,
			"suggested_action", evt.SuggestedAction,
		)
		if a.metrics != nil {
			a.metrics.RecordEvolutionGuardrail(string(evt.ErrorCode))
		}
	}

	if preResult.ShouldStop {
		return fmt.Errorf("adapter.Run: pre-evolve guardrail check failed (generation %d): %d event(s), best_score=%.2f, unevaluated=%d/%d",
			preStats.Generation, len(preResult.Events), preStats.BestScore, unevaluated, preStats.Size)
	}
	return nil
}

// runPostGuardrails executes the post-evolution safety checkpoint.
// Returns an error when the guardrails demand a stop after evolution.
//
// Args:
//
//	ctx - operation context for cancellation.
//
// Returns:
//
//	error - non-nil when the post-evolve guardrail demands a stop.
func (a *GenomePopulationAdapter) runPostGuardrails(ctx context.Context) error {
	if a.guardrails == nil {
		return nil
	}

	postStats := a.pop.Stats()
	agents, _ := a.pop.Snapshot()
	lineageShares := computeLineageShares(agents)

	postResult := a.guardrails.PostEvolveCheckForSource(ctx, "genome",
		postStats.BestScore,
		postStats.Generation,
		lineageShares,
	)

	for _, evt := range postResult.Events {
		log.WarnContext(ctx, "post-evolve guardrail triggered", "method", "Run", "rule", evt.Rule,
			"level", evt.Level,
			"message", evt.Message,
			"suggested_action", evt.SuggestedAction,
		)
		if a.metrics != nil {
			a.metrics.RecordEvolutionGuardrail(string(evt.ErrorCode))
		}
	}

	if postResult.ShouldStop {
		// Evolution already completed; log warning but still return error.
		log.WarnContext(ctx, "post-evolve guardrail signals stop, but evolution already completed", "method", "Run", "generation", postStats.Generation,
			"event_count", len(postResult.Events),
		)
		return fmt.Errorf("adapter.Run: post-evolve guardrail check failed after evolution completed (generation %d): %d event(s), best_score=%.2f",
			postStats.Generation, len(postResult.Events), postStats.BestScore)
	}
	return nil
}

// toolSetRejected reports whether the C6 tool-set guardrail rejects a
// strategy's evolved tool whitelist. It closes the genome-path gap left by the
// dream_cycle-only wiring: the genome adapter promotes its own winner (lifecycle
// submit / direct deploy) without ever passing through findWinner, so an
// out-of-bounds or unregistered-name whitelist could reach the live agent from
// this path even though the dream path rejected the same shape.
//
// Parsing goes through the same agents helper the executors use, so the count
// the guardrail bounds is exactly the set the LLM would be shown.
//
// Returns false when no guardrails are wired (guardrails disabled = unchanged
// behavior).
func (a *GenomePopulationAdapter) toolSetRejected(ctx context.Context, s *mutation.Strategy) bool {
	if a.guardrails == nil || s == nil {
		return false
	}
	tools := agents.ToolNamesFromParams(s.Params)
	res := a.guardrails.ValidateToolSet(a.pop.Stats().Generation, tools)
	if !res.ShouldStop {
		return false
	}
	for _, evt := range res.Events {
		log.WarnContext(ctx, "tool-set guardrail triggered on genome winner",
			"method", "toolSetRejected",
			"strategy_id", s.ID,
			"rule", evt.Rule,
			"message", evt.Message,
			"suggested_action", evt.SuggestedAction,
		)
		if a.metrics != nil {
			a.metrics.RecordEvolutionGuardrail(string(evt.ErrorCode))
		}
	}
	return true
}

// deployBestStrategy persists the current best-evolved strategy to the active
// strategy store so the live agent can consume it. It is a no-op when no
// ActiveStrategyManager is wired or no evaluated strategy exists.
//
// Args:
//
//	ctx - operation context for cancellation.
func (a *GenomePopulationAdapter) deployBestStrategy(ctx context.Context) {
	if a.activeStrategyMgr == nil {
		return
	}
	best := a.pop.BestStrategy()
	if best == nil {
		log.DebugContext(ctx, "no evaluated strategy to deploy", "method", "deployBestStrategy")
		return
	}
	// C6: the direct-deploy path bypasses the lifecycle verify gates, so the
	// tool-set guardrail must be checked here too — otherwise disabling the
	// lifecycle would silently disable the guard.
	if a.toolSetRejected(ctx, best) {
		log.WarnContext(ctx, "best strategy jailed by tool-set guardrail, not deployed",
			"method", "deployBestStrategy", "strategy_id", best.ID)
		return
	}
	if err := a.activeStrategyMgr.Deploy(ctx, best); err != nil {
		log.WarnContext(ctx, "deploy failed", "method", "deployBestStrategy", "strategy_id", best.ID, "error", err)
	}
}

// recordOutcomesLocked records strategy outcomes to the adaptive distribution
// and feedback recorder after an evolution cycle. It compares offspring scores
// with their parent scores to determine wins and score deltas.
//
// Args:
//
//	ctx - operation context for cancellation.
//	agentsBefore - pre-evolution population snapshot for parent score lookup.
func (a *GenomePopulationAdapter) recordOutcomesLocked(
	ctx context.Context,
	agentsBefore []*mutation.Strategy,
) {
	parentScores := make(map[string]float64, len(agentsBefore))
	for _, parent := range agentsBefore {
		parentScores[parent.ID] = parent.Score
	}

	agentsAfter, _ := a.pop.Snapshot()

	for _, child := range agentsAfter {
		if child.ParentID == "" {
			continue
		}
		if child.Score < 0 {
			continue
		}

		parentScore, ok := parentScores[child.ParentID]
		if !ok {
			if parts := strings.Split(child.ParentID, "\u00d7"); len(parts) == 2 {
				if ps1, ok1 := parentScores[parts[0]]; ok1 {
					if ps2, ok2 := parentScores[parts[1]]; ok2 {
						parentScore = (ps1 + ps2) / 2
						ok = true
					}
				}
			}
		}
		if !ok {
			continue
		}
		scoreDelta := child.Score - parentScore
		won := scoreDelta > 0

		if a.adaptiveDist != nil {
			a.adaptiveDist.RecordOutcome(
				child.StrategyMutationType,
				scoreDelta,
				0,
				won,
			)
		}

		if a.feedbackRecorder != nil {
			outcome := StrategyOutcome{
				StrategyID: child.ID,
				Success:    won,
				Score:      child.Score,
			}
			if err := a.feedbackRecorder.Register(ctx, outcome); err != nil {
				log.WarnContext(ctx, "feedback recording failed", "method", "recordOutcomesLocked", "strategy_id", child.ID,
					"error", err,
				)
			}
		}
	}
}

// scorerWarningOnce ensures the missing-scorer warning is logged at most once
// per process lifetime, even when buildScorer is called repeatedly (e.g., once
// per evolution cycle in the scheduler loop).
var scorerWarningOnce sync.Once

// buildScorer constructs a ScorerFunc from the optional adapter-level scorer.
// When no scorer is available, returns a constant baseline scorer with a warning.
func buildScorer(scorer func(*mutation.Strategy) float64) genome.ScorerFunc {
	if scorer != nil {
		return scorer
	}
	scorerWarningOnce.Do(func() {
		log.WarnContext(context.Background(), "No scorer configured, using constant baseline (50.0). ", "method", "buildScorer"+
			"Configure a real scorer for production use.",
		)
	})
	// Note: TieredScorer is now available via SystemConfig options (MaxLLMCallsPerGeneration,
	// HeuristicScorer). When those are set, Run() uses the tiered pipeline instead of this
	// fallback path. The ConstantScorer default is retained for backward compatibility.
	return genome.ConstantScorer(50.0)
}

// countUnevaluated counts agents with Score == ScoreUnevaluated.
func countUnevaluated(agents []*mutation.Strategy) int {
	n := 0
	for _, a := range agents {
		if a.Score == genome.ScoreUnevaluated {
			n++
		}
	}
	return n
}

// submitToCoordinator generates diff patches from all registered genomes and
// submits them to the coordinator for decision and deployment. Each patch is
// attributed to the best-evolved strategy so the coordinator can measure it
// against the active strategy rather than invent a strategy ID.
func (a *GenomePopulationAdapter) submitToCoordinator(ctx context.Context) {
	strategyID := ""
	if best := a.pop.BestStrategy(); best != nil {
		strategyID = best.ID
	}
	patches, err := generateDiffPatches(ctx, a.genomeReg, a.diffReg, 3, strategyID)
	if err != nil {
		log.WarnContext(ctx, "diff engine failed", "method", "submitToCoordinator", "error", err)
		return
	}

	// Query all registered genomes that implement FitnessGenome and compute
	// an average fitness score. When no genome provides a fitness score, use
	// a baseline of 0.5 so patches pass through the coordinator's fitness gate
	// rather than bypassing it entirely (which Fitness=0 does).
	var fitnessSum float64
	var fitnessCount int
	for _, name := range a.genomeReg.List() {
		g, err := a.genomeReg.Get(name)
		if err != nil {
			continue
		}
		if f, ok := g.(evogenome.FitnessGenome); ok {
			score, scoreErr := f.Fitness(ctx)
			if scoreErr == nil {
				fitnessSum += score
				fitnessCount++
			}
		}
	}
	fitness := 0.5 // baseline when no FitnessGenome is available
	if fitnessCount > 0 {
		fitness = fitnessSum / float64(fitnessCount)
	}
	// Coordinator thresholds are 0-100 (see DefaultPolicy: ApplyFitnessThreshold=70,
	// MinFitnessThreshold=30). FitnessGenome scores are [0,1], so scale up.
	fitness *= 100.0
	if fitness > 100.0 {
		fitness = 100.0
	}

	for _, p := range patches {
		a.coordinator.Submit(coordinator.PatchProposal{
			Patch:     p,
			Source:    coordinator.SourceGA,
			Reason:    "GA: population evolution result",
			Priority:  6,
			Fitness:   fitness,
			Timestamp: time.Now(),
		})
	}
	if len(patches) > 0 {
		policy := a.coordinator.Policy()
		decidedBefore := len(a.coordinator.DecisionHistory())
		a.coordinator.Evaluate(ctx)
		decisions := a.coordinator.DecisionHistory()

		// Surface every coordinator decision for GA proposals so the
		// apply/reject/delay/drop outcome is observable instead of silently
		// discarded. The coordinator is a pure decision package (no logger);
		// the GA adapter owns observability for the patches it submits, and
		// surfaces the policy thresholds alongside each decision so an
		// operator can see exactly which gate produced the outcome.
		for i := decidedBefore; i < len(decisions); i++ {
			d := decisions[i]
			if d.Proposal.Source != coordinator.SourceGA {
				continue
			}
			logCoordinatorDecision(ctx, d, policy, fitness)
		}
	}
}

// logCoordinatorDecision emits a structured log entry for a single GA
// coordinator decision. Each branch carries the proposal's fitness, the
// policy thresholds that produced the decision, and the retry count so an
// operator can trace exactly why a patch was applied, rejected, delayed, or
// dropped. Apply failures (ApplyError) are elevated to Warn so a "successful
// decision but failed apply" is never silently swallowed.
//
// Args:
//
//	ctx - operation context for cancellation.
//	d - the coordinator decision to surface.
//	policy - the coordinator policy snapshot at decision time.
//	fitness - the fitness value the GA submitted with this proposal (0-100).
func logCoordinatorDecision(
	ctx context.Context,
	d coordinator.PatchDecision,
	policy coordinator.PolicyGenome,
	fitness float64,
) {
	baseAttrs := []any{
		"method", "Run",
		"type", d.Proposal.Patch.Type,
		"target", d.Proposal.Patch.Target,
		"fitness", d.Proposal.Fitness,
		"apply_threshold", policy.ApplyFitnessThreshold,
		"min_threshold", policy.MinFitnessThreshold,
		"retry_count", d.Proposal.RetryCount,
		"reason", d.Reason,
		"submitted_fitness", fitness,
	}
	switch d.Decision {
	case coordinator.DecisionApply:
		if d.ApplyError != nil {
			log.WarnContext(ctx, "GA patch apply failed by coordinator",
				append(baseAttrs, "error", d.ApplyError)...)
			return
		}
		log.InfoContext(ctx, "GA patch applied by coordinator", baseAttrs...)
	case coordinator.DecisionReject:
		log.WarnContext(ctx, "GA patch rejected by coordinator", baseAttrs...)
	case coordinator.DecisionDelay:
		log.InfoContext(ctx, "GA patch delayed by coordinator for review", baseAttrs...)
	case coordinator.DecisionDrop:
		// Drop is elevated to Warn: a permanently discarded patch is a
		// signal the GA is producing patches the coordinator won't act on,
		// which is exactly the "silent failure" the closure plan targets.
		log.WarnContext(ctx, "GA patch dropped by coordinator (retry budget exhausted)",
			baseAttrs...)
	default:
		log.WarnContext(ctx, "GA patch has unknown coordinator decision",
			append(baseAttrs, "decision", d.Decision.String())...)
	}
}

// computeLineageShares computes ParentID distribution from a population snapshot.
// Returns a map of parentID -> count. Root strategies (empty ParentID) are excluded.
func computeLineageShares(agents []*mutation.Strategy) map[string]int {
	shares := make(map[string]int)
	for _, a := range agents {
		if a.ParentID != "" {
			shares[a.ParentID]++
		}
	}
	return shares
}

// GenerationActive reports whether an adapter-driven evolution run is
// currently executing (live-chaos GA quiet-window probe, #12 Phase 2).
func (a *GenomePopulationAdapter) GenerationActive() bool {
	return a != nil && a.running.Load()
}
