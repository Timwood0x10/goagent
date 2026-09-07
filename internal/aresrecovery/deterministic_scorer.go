package aresrecovery

import (
	"math"
	"time"
)

// Scoring weights for the deterministic scorer.
//
// These are compile-time constants — no runtime config, no randomness.
// The weights are chosen so that:
//   - Success rate dominates (70%): a strategy that fails is penalized
//     more than one that is merely slow.
//   - Latency contributes 15%: normalized against a 30s ceiling (exceeding
//     it yields 0 latency score).
//   - Retries contribute 10%: each retry reduces the retry score linearly
//     (0 retries = 1.0, 5+ retries = 0.0).
//   - Recovery contributes 5%: each recovery replacement reduces the
//     recovery score linearly (0 recovers = 1.0, 3+ recovers = 0.0).
//
// The weights sum to 1.0 and the output is always in [0,1].
const (
	weightSuccess = 0.70
	weightLatency = 0.15
	weightRetries = 0.10
	weightRecover = 0.05

	// latencyCeiling is the wall-clock duration above which the latency
	// component is 0. 30 seconds covers typical LLM tool-call quanta;
	// longer quanta are penalized proportionally.
	latencyCeiling = 30 * time.Second

	// retryCeiling is the retry count at which the retry component is 0.
	retryCeiling = 5.0

	// recoverCeiling is the recovery count at which the recovery component is 0.
	recoverCeiling = 3.0
)

// DeterministicScorer is the zero-LLM scorer: it aggregates the
// extended attribution data (success rate, average latency, average
// retries, average recoveries) into a single [0,1] score using fixed
// weights and no random source.
//
// The scorer is deterministic: the same AttributionSnapshot always produces
// the same score. This is a hard requirement: the GA must not depend on
// an LLM or any random source for its fitness signal.
//
// Thread-safe: holds no mutable state.
type DeterministicScorer struct{}

// NewDeterministicScorer creates a zero-LLM scorer.
func NewDeterministicScorer() *DeterministicScorer {
	return &DeterministicScorer{}
}

// ScoreAgent aggregates one agent's outcome summary into a [0,1] score.
// An agent with no history (total == 0) returns the neutral prior 0.5
// so the GA keeps exploring without penalizing an untried strategy.
func (DeterministicScorer) ScoreAgent(r AgentResult) float64 {
	total := r.Success + r.Fail
	if total == 0 {
		return 0.5 // neutral prior
	}
	successRate := float64(r.Success) / float64(total)
	latencyScore := normalizeLatency(r.AvgLatency)
	retryScore := normalizeCount(r.AvgRetries, retryCeiling)
	recoverScore := normalizeCount(r.AvgRecovers, recoverCeiling)

	score := weightSuccess*successRate +
		weightLatency*latencyScore +
		weightRetries*retryScore +
		weightRecover*recoverScore

	return clamp01(score)
}

// ScoreCapability aggregates one (agent, capability) outcome summary into
// a [0,1] score. The formula is identical to ScoreAgent so the GA can
// compare per-capability and per-agent scores on the same scale.
func (DeterministicScorer) ScoreCapability(r CapabilityResult) float64 {
	total := r.Success + r.Fail
	if total == 0 {
		return 0.5
	}
	successRate := float64(r.Success) / float64(total)
	latencyScore := normalizeLatency(r.AvgLatency)
	retryScore := normalizeCount(r.AvgRetries, retryCeiling)
	recoverScore := normalizeCount(r.AvgRecovers, recoverCeiling)

	score := weightSuccess*successRate +
		weightLatency*latencyScore +
		weightRetries*retryScore +
		weightRecover*recoverScore

	return clamp01(score)
}

// ScoreSnapshot aggregates the entire attribution snapshot into a single
// [0,1] score by averaging per-agent scores (weighted equally). This is
// the entry point for the feedback loop: it reads the current attribution,
// computes one aggregate score, and writes it back to the active strategy.
//
// An empty snapshot returns the neutral prior 0.5.
func (s DeterministicScorer) ScoreSnapshot(snap AttributionSnapshot) float64 {
	if len(snap.PerAgent) == 0 {
		return 0.5
	}
	var sum float64
	for _, ar := range snap.PerAgent {
		sum += s.ScoreAgent(ar)
	}
	return clamp01(sum / float64(len(snap.PerAgent)))
}

// ScoreAttribution is a convenience method: it takes an ExecutionAttribution
// source, snapshots it, and scores the snapshot. This is the method the
// feedback loop calls.
func (s DeterministicScorer) ScoreAttribution(src ExecutionResultSource) float64 {
	if src == nil {
		return 0.5
	}
	return s.ScoreSnapshot(src.Snapshot())
}

// TaskScore implements the evolution.TaskScoreProvider interface.
// It returns the current aggregate deterministic score from the attribution
// source, regardless of the success/failure of the individual task event —
// the score reflects the OVERALL execution quality trend, not a single
// task's pass/fail. This is the key difference from the constant 1.0/0.0:
// a task that failed but whose agent has a 90% success rate still feeds
// ~0.9 into the window, so degradation detection sees real quality, not
// just the last task's binary outcome.
//
// The attribution source must be wired at construction time via
// NewTaskScoreProvider.
func (p *AttributionScoreProvider) TaskScore(_ bool) float64 {
	if p == nil || p.source == nil {
		return 0.5
	}
	return p.scorer.ScoreAttribution(p.source)
}

// AttributionScoreProvider adapts a DeterministicScorer + ExecutionResultSource
// into the evolution.TaskScoreProvider interface. It is the bridge
// that lets the EvolutionScheduler read attribution-derived scores without
// importing the aresrecovery package — the wiring layer (cmd/ares) constructs
// this adapter and injects it via evolution.WithScoreProvider.
//
// Thread-safe: the scorer is stateless and the attribution source is
// thread-safe (its Snapshot method acquires a mutex).
type AttributionScoreProvider struct {
	scorer *DeterministicScorer
	source ExecutionResultSource
}

// NewAttributionScoreProvider creates a TaskScoreProvider backed by the
// deterministic scorer reading from the given attribution source. Either
// may be nil (returns the neutral 0.5 for all calls).
func NewAttributionScoreProvider(source ExecutionResultSource) *AttributionScoreProvider {
	return &AttributionScoreProvider{
		scorer: NewDeterministicScorer(),
		source: source,
	}
}

// normalizeLatency converts a duration into a [0,1] score where shorter is
// better. 0 latency → 1.0; latency >= ceiling → 0.0; linear in between.
func normalizeLatency(d time.Duration) float64 {
	if d <= 0 {
		return 1.0
	}
	if d >= latencyCeiling {
		return 0.0
	}
	return 1.0 - float64(d)/float64(latencyCeiling)
}

// normalizeCount converts a count average into a [0,1] score where fewer is
// better. 0 → 1.0; >= ceiling → 0.0; linear in between.
func normalizeCount(avg, ceiling float64) float64 {
	if avg <= 0 {
		return 1.0
	}
	if avg >= ceiling {
		return 0.0
	}
	return 1.0 - avg/ceiling
}

// clamp01 clamps a float64 to [0,1]. NaN returns 0.0.
func clamp01(v float64) float64 {
	if math.IsNaN(v) {
		return 0.0
	}
	if v < 0.0 {
		return 0.0
	}
	if v > 1.0 {
		return 1.0
	}
	return v
}
