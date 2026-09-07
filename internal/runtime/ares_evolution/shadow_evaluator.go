// Package evolution provides automatic experience extraction from flight recorder diagnostics.
// It bridges the flight recording system with the experience store to enable
// continuous learning from agent execution failures and anomalies.
package evolution

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// shadowTieEpsilon is the tolerance within which two scores are treated as an
// exact tie. The production cold-start prior is attribution-derived
// (det.ScoreAttribution snapshots live attribution), and ShadowEvaluator.
// Evaluate scores active and shadow in two separate scorer calls; a concurrent
// quantum landing between the calls can shift the prior by a tiny amount. That
// micro-drift must NOT turn an intended prior-vs-prior tie into a "decisive"
// comparison — a random-direction jitter would report "evidence exists but
// win rate is thin" instead of "no evidence", and a monotonically-rising
// attribution could let the candidate systematically win a comparison that was
// really a tie (review P1-3). The ReplayScorer also memoizes its prior per
// window so the two calls agree exactly; the epsilon is the defensive second
// layer for any scorer that does not memoize.
const shadowTieEpsilon = 1e-9

// isTie reports whether two scores are within shadowTieEpsilon of each other.
// It is the single authoritative tie predicate used both at RecordResult time
// (ShadowWon) and at ShouldDeployLoose time (decisive-sample filter), so the
// two never disagree about what counts as a tie.
func isTie(a, b float64) bool {
	return math.Abs(a-b) <= shadowTieEpsilon
}

// ShadowComparison records the result of comparing active vs shadow strategy
// performance on a single evaluation.
type ShadowComparison struct {
	// ActiveScore is the score achieved by the active (current) strategy.
	ActiveScore float64

	// ShadowScore is the score achieved by the shadow (candidate) strategy.
	ShadowScore float64

	// ShadowWon indicates whether the shadow strategy outperformed the active one.
	ShadowWon bool

	// Timestamp records when this comparison was made.
	Timestamp time.Time
}

// ShadowReport summarizes shadow evaluation results and provides a deployment
// recommendation.
type ShadowReport struct {
	// TotalComparisons is the number of DECISIVE comparison results collected
	// (ties excluded — see ShouldDeployLoose). This is the sample count the
	// MinSamples gate is judged against: a tie carries no information about
	// which strategy is better, so counting it would let the gate pass on
	// evidence that is really "no evidence" (review B-3).
	TotalComparisons int

	// TieCount is the number of recorded comparisons that were exact ties
	// (activeScore == shadowScore). Kept for observability — the strict gate
	// treats a tie as neither a win nor a sample, so a high tie count with a
	// low TotalComparisons means the evidence is thin, not that the shadow
	// lost.
	TieCount int

	// ShadowWins is the count of decisive comparisons won by the shadow strategy.
	ShadowWins int

	// WinRate is the proportion of decisive comparisons won by the shadow strategy.
	WinRate float64

	// Recommendation describes the suggested action based on evaluation results.
	Recommendation string
}

// ShadowEvaluationConfig configures the shadow evaluation behavior for safe
// strategy deployment.
type ShadowEvaluationConfig struct {
	// Enabled enables shadow evaluation when true.
	Enabled bool `json:"enabled"`

	// MinSamples is the minimum number of comparison samples required before
	// making a deployment decision. Default is 10.
	MinSamples int `json:"min_samples"`

	// MinWinRate is the minimum win rate required for the shadow strategy to
	// be recommended for deployment. Default is 0.55.
	MinWinRate float64 `json:"min_win_rate"`

	// EvaluationInterval is the time between evaluation rounds.
	EvaluationInterval time.Duration `json:"evaluation_interval"`

	// DeterministicScorer reports whether the wired scorer always returns the
	// same score for the same strategy (e.g. LLM scorer with a fixed seed
	// forces temperature 0, or a heuristic scorer is in use). It is set by
	// the wiring layer, never by YAML: the sampler is honest about this
	// limitation — comparisons are identical, MinSamples is satisfied by
	// repetition, not by independent evidence. json:"-" keeps it out of the
	// HTTP config surface.
	DeterministicScorer bool `json:"-"`

	// ReplayWindowSpan is the width of ONE replay evidence window used by the
	// ReplayScorer (C3.2). Each comparison reads a distinct slice of history,
	// so MinSamples is satisfied by independent evidence. Zero falls back to
	// the replayWindowSpan default (10 minutes). Exposed so an operator can
	// tune the evidence granularity without touching code — this parameter
	// directly decides how independent the shadow evidence is.
	ReplayWindowSpan time.Duration `json:"replay_window_span,omitempty"`
}

// DefaultShadowEvaluationConfig returns sensible defaults for shadow evaluation.
//
// Returns:
//
//	ShadowEvaluationConfig - configuration with default values.
func DefaultShadowEvaluationConfig() ShadowEvaluationConfig {
	return ShadowEvaluationConfig{
		Enabled:            false,
		MinSamples:         10,
		MinWinRate:         0.55,
		EvaluationInterval: 10 * time.Minute,
	}
}

// ShadowEvaluator enables safe deployment comparison by running the active and
// a candidate strategy side by side, collecting comparison results, and
// recommending deployment only when the candidate demonstrates sufficient
// improvement.
type ShadowEvaluator struct {
	activeStrategy *mutation.Strategy
	shadowStrategy *mutation.Strategy
	shadowResults  []ShadowComparison
	minSamples     int
	minWinRate     float64
	shadowScorer   func(context.Context, *mutation.Strategy) float64 // optional independent scorer
	// deterministic mirrors ShadowEvaluationConfig.DeterministicScorer so
	// callers can inspect whether the comparison window is repetition rather
	// than independent evidence, instead of that fact living only in a log line.
	deterministic bool
	mu            sync.RWMutex
}

// NewShadowEvaluator creates a ShadowEvaluator for safe strategy comparison.
//
// Args:
//
//	cfg - configuration for shadow evaluation behavior.
//
// Returns:
//
//	*ShadowEvaluator - the configured evaluator instance.
func NewShadowEvaluator(cfg ShadowEvaluationConfig) *ShadowEvaluator {
	minSamples := cfg.MinSamples
	if minSamples <= 0 {
		minSamples = 10
	}
	minWinRate := cfg.MinWinRate
	if minWinRate <= 0 {
		minWinRate = 0.55
	}

	return &ShadowEvaluator{
		shadowResults: make([]ShadowComparison, 0),
		minSamples:    minSamples,
		minWinRate:    minWinRate,
		deterministic: cfg.DeterministicScorer,
	}
}

// IsDeterministicScorer reports whether the wired scorer returns the same score
// for the same strategy, in which case the comparison window is repetition and
// the win rate can only be 0.0 or 1.0.
//
// Returns:
//
//	bool - true when comparisons are not independent evidence.
func (e *ShadowEvaluator) IsDeterministicScorer() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.deterministic
}

// StartShadow begins shadow evaluation of a candidate strategy. The active
// strategy should be set before calling this.
//
// Args:
//
//	candidate - the candidate strategy to evaluate.
func (e *ShadowEvaluator) StartShadow(candidate *mutation.Strategy) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.shadowStrategy = candidate
	// Reset previous results when starting a new shadow evaluation.
	e.shadowResults = make([]ShadowComparison, 0)
}

// RecordResult records a comparison result between the active and shadow
// strategies.
//
// Args:
//
//	activeScore - the score from the active strategy.
//	shadowScore - the score from the shadow strategy.
func (e *ShadowEvaluator) RecordResult(activeScore, shadowScore float64) {
	e.mu.Lock()
	defer e.mu.Unlock()

	comparison := ShadowComparison{
		ActiveScore: activeScore,
		ShadowScore: shadowScore,
		// B-3 / P1-3: a score within shadowTieEpsilon of the active is a tie,
		// not a win. ShadowWon must agree with ShouldDeployLoose's decisive
		// filter (both use isTie), otherwise a near-tie could be recorded as a
		// win yet be filtered out of the sample count — or vice versa.
		ShadowWon: shadowScore > activeScore && !isTie(activeScore, shadowScore),
		Timestamp: time.Now(),
	}
	e.shadowResults = append(e.shadowResults, comparison)
}

// ShouldDeploy determines whether the shadow strategy should be deployed based
// on accumulated comparison results. It uses majority voting with a configurable
// minimum sample count and win rate threshold.
//
// SEMANTICS (review: one evaluator, two consumers, two readings — now
// explicit): ShouldDeploy is the STRICT judge, fail-closed — insufficient
// DECISIVE samples REJECT. It is the contract the StrategyLifecycle's G2
// verify gate relies on (design doc §3.1: "fewer than MinSamples samples →
// the candidate stays in SHADOW and is NOT deployed"). DreamCycle's internal
// deploy path uses ShouldDeployLoose instead, where insufficient data defers
// to the deployer rather than veto.
//
// Returns:
//
//	bool - true if the shadow strategy should be deployed.
//	*ShadowReport - detailed report, or nil when there are no comparisons at
//	                all (a caller must distinguish "no evidence" from "all
//	                ties"; both fail-closed but report nil only for the former).
func (e *ShadowEvaluator) ShouldDeploy() (bool, *ShadowReport) {
	pass, report := e.ShouldDeployLoose()
	if report == nil {
		// No comparisons at all → fail-closed with a nil report, so a caller
		// can tell "no evidence was gathered" (nil) from "comparisons were
		// gathered but every one was a tie" (report.TieCount > 0).
		return false, nil
	}
	// Strict judge: MinSamples means MinSamples DECISIVE comparisons. Ties
	// carry no signal, so a report whose decisive count is below the bar is a
	// fail-closed rejection — even if raw comparisons (incl. ties) look ample.
	if report.TotalComparisons < e.minSamples {
		report.Recommendation = fmt.Sprintf(
			"fail-closed: insufficient samples — %d decisive comparisons < required %d, candidate stays in SHADOW",
			report.TotalComparisons, e.minSamples,
		)
		return false, report
	}
	return pass, report
}

// ShouldDeployLoose is the DreamCycle-side contract: shadow evaluation must
// not VETO deployment while it has too few comparisons to reach a conclusion —
// insufficient data defers to the deployer instead of rejecting. With enough
// raw comparisons the verdict is identical to ShouldDeploy on the DECISIVE
// subset.
//
// B-3 (tie semantics): an exact tie (activeScore == shadowScore) carries no
// information about which strategy is better. It is neither a win nor a
// sample — it is excluded from TotalComparisons, so a run of cold-start
// prior-vs-prior comparisons (sparse-window active vs never-executed
// candidate, see replay_scorer.go) can no longer dilute the win rate toward
// the fail-closed boundary.
//
// P0-3 (tie-vs-no-evidence): a report is returned whenever ANY comparison was
// recorded — including the all-tie case — so the caller can distinguish "no
// comparisons gathered" (nil report) from "comparisons gathered but all were
// ties" (report with TieCount == TotalComparisons' complement). The all-tie
// verdict is a REJECTION (no decisive evidence the shadow is better), never a
// deferral — a full MinSamples wall of ties must not flip to "proceed".
//
// Returns:
//
//	bool - true if the shadow strategy should be deployed (or cannot be
//	       judged yet and the deployer may proceed).
//	*ShadowReport - detailed report, or nil when there are no comparisons.
func (e *ShadowEvaluator) ShouldDeployLoose() (bool, *ShadowReport) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// B-3: only decisive comparisons (shadow != active) count toward the
	// sample total. A tie is recorded for observability but adds nothing to
	// either the numerator or the denominator.
	total := 0
	shadowWins := 0
	tieCount := 0
	for _, r := range e.shadowResults {
		// P0 (review): the decisive filter MUST use the same predicate as
		// ShadowWon (isTie), not an exact `==`. A pair whose difference falls
		// in (0, shadowTieEpsilon] is a tie for ShadowWon but would be
		// "decisive" for an exact comparison — entering `total` while never
		// entering `shadowWins`, i.e. every near-tie silently counted as a
		// LOSS and dragging the win rate down.
		if isTie(r.ActiveScore, r.ShadowScore) {
			tieCount++
			continue
		}
		total++
		if r.ShadowWon {
			shadowWins++
		}
	}

	if total+tieCount == 0 {
		// No comparisons at all — nothing to judge. Nil report tells the
		// caller this is "no evidence", not "all ties".
		return false, nil
	}

	winRate := 0.0
	if total > 0 {
		winRate = float64(shadowWins) / float64(total)
	}
	report := &ShadowReport{
		TotalComparisons: total,
		TieCount:         tieCount,
		ShadowWins:       shadowWins,
		WinRate:          winRate,
	}

	// LOOSE contract, raw-comparison bar: DreamCycle defers (returns true)
	// when the total recorded comparisons — decisive AND ties — are below
	// MinSamples. Ties still count as "a comparison happened" for deciding
	// whether the sample size is adequate; they just never enter the win-rate
	// math.
	if total+tieCount < e.minSamples {
		report.Recommendation = fmt.Sprintf(
			"insufficient samples: need %d comparisons, have %d — cannot judge yet, deferring to deployer",
			e.minSamples, total+tieCount,
		)
		return true, report
	}

	// Enough raw comparisons but zero decisive ones: a MinSamples wall of
	// ties is a REJECTION, not a deferral. There is no decisive evidence the
	// shadow is better, so the gate must not let the candidate through.
	if total == 0 {
		report.Recommendation = fmt.Sprintf(
			"all %d comparisons were exact ties — no decisive evidence the shadow outperforms active",
			tieCount,
		)
		return false, report
	}

	if winRate >= e.minWinRate {
		report.Recommendation = "shadow strategy outperforms active, recommend deployment"
		return true, report
	}

	report.Recommendation = fmt.Sprintf(
		"shadow win rate %.1f%% below threshold %.1f%%, keep active",
		winRate*100, e.minWinRate*100,
	)
	return false, report
}

// ActiveStrategy returns the active strategy.
//
// Returns:
//
//	*mutation.Strategy - the active strategy, or nil if not set.
func (e *ShadowEvaluator) ActiveStrategy() *mutation.Strategy {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.activeStrategy
}

// SetActiveStrategy sets the active strategy for comparison.
//
// Args:
//
//	s - the active strategy.
func (e *ShadowEvaluator) SetActiveStrategy(s *mutation.Strategy) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.activeStrategy = s
}

// ShadowStrategy returns the shadow (candidate) strategy.
//
// Returns:
//
//	*mutation.Strategy - the shadow strategy, or nil if not set.
func (e *ShadowEvaluator) ShadowStrategy() *mutation.Strategy {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.shadowStrategy
}

// SetShadowScorer sets an independent scoring function for shadow evaluation.
// When set, Evaluate() uses this scorer to compare active vs shadow strategies
// independently of the caller-provided scores. The scorer receives a context
// for cancellation and tracing.
//
// Args:
//   - scorer: scoring function (use nil to clear). Signature: func(ctx, *Strategy) float64.
func (e *ShadowEvaluator) SetShadowScorer(scorer func(context.Context, *mutation.Strategy) float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.shadowScorer = scorer
}

// HasIndependentScorer returns true if an independent scorer is configured.
// When true, Evaluate() can be used instead of manual RecordResult() calls.
//
// Returns:
//
//	bool - true if an independent scorer is set.
func (e *ShadowEvaluator) HasIndependentScorer() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.shadowScorer != nil
}

// Evaluate scores both active and shadow strategies using the independent
// scorer (if set) and records the comparison result. Returns the active and
// shadow scores. If no scorer is set, returns (-1, -1) without recording.
//
// Args:
//   - ctx: operation context for cancellation.
//
// Returns:
//   - activeScore: the score from the active strategy.
//   - shadowScore: the score from the shadow strategy.
func (e *ShadowEvaluator) Evaluate(ctx context.Context) (float64, float64) {
	e.mu.RLock()
	scorer := e.shadowScorer
	active := e.activeStrategy
	shadow := e.shadowStrategy
	e.mu.RUnlock()

	if scorer == nil || active == nil || shadow == nil {
		return -1, -1
	}

	activeScore := scorer(ctx, active)
	shadowScore := scorer(ctx, shadow)

	e.RecordResult(activeScore, shadowScore)
	return activeScore, shadowScore
}

// Results returns a copy of all recorded comparison results.
//
// Returns:
//
//	[]ShadowComparison - copy of all recorded comparisons.
func (e *ShadowEvaluator) Results() []ShadowComparison {
	e.mu.RLock()
	defer e.mu.RUnlock()

	result := make([]ShadowComparison, len(e.shadowResults))
	copy(result, e.shadowResults)
	return result
}

// Reset clears all evaluation state.
func (e *ShadowEvaluator) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.shadowStrategy = nil
	e.shadowResults = make([]ShadowComparison, 0)
}
