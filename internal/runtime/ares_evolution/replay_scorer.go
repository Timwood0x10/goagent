// replay_scorer.go provides the zero-LLM ReplayScorer — the shadow gate's
// independent evidence source in the default (LLM scoring off) configuration.
//
// WHY THIS EXISTS: bootstrap declares
// DeterministicScorerEnabled so shadowGateMode registers the shadow gate as
// "independent scorer wired". That promise is only real if a scorer actually
// DISCRIMINATES between the candidate and the active strategy. A scorer that
// returns one global number for every strategy makes every comparison an exact
// tie, ShadowWon is never true, the win rate collapses to 0.0 and the gate
// rejects every candidate forever — a gate that claims evidence while
// gathering none.
// That failure mode — MinSamples satisfied by repeated identical scores
// rather than independent evidence — is exactly what this scorer forbids.
//
// The honest evidence source under a zero-token budget is REPLAY: the runtime
// already writes one KindFitness evidence record per finished task
// (RuntimeObserver.writeEvidence, source="strategy", payload carries
// strategy_id). Scoring a strategy by the mean of ITS OWN historical records,
// over a bounded time window, is a real per-strategy measurement that costs no
// LLM call. Slicing the history into disjoint windows turns MinSamples into
// independent evidence — each comparison reads a DIFFERENT task set — instead
// of counting repetitions of one verdict.
package evolution

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// observerEvidenceSource is the evidence source RuntimeObserver stamps on the
// per-task fitness records the replay scorer reads back. Shared as a constant
// so the writer and the reader can never drift apart — a mismatch would make
// every query return nothing and silently reduce the scorer to its prior.
const observerEvidenceSource = "strategy"

// replayWindowSpan is the duration of ONE replay window. Each shadow
// comparison replays a distinct span of history, so the sampler's MinSamples
// comparisons read disjoint task sets rather than re-reading one set.
//
// 10 minutes is chosen against the observer's write rate: one record per
// finished task, so a 10-minute span holds several tasks under normal load
// while staying short enough that MinSamples (10 by default) windows cover a
// ~100-minute horizon — recent enough to reflect current behaviour.
const replayWindowSpan = 10 * time.Minute

// replayWindow bounds one comparison's evidence read. A zero value means
// "no bound" (query the whole retained history).
type replayWindow struct {
	Since time.Time
	Until time.Time
}

// replayWindowCtxKey is the private context key carrying the current replay
// window. The window travels through the context because the ShadowEvaluator
// owns the scorer call sites (it calls scorer(ctx, active) and
// scorer(ctx, shadow) per comparison) and must not need to know that some
// scorers are window-aware.
type replayWindowCtxKey struct{}

// withReplayWindow returns a context carrying the given replay window.
func withReplayWindow(ctx context.Context, w replayWindow) context.Context {
	return context.WithValue(ctx, replayWindowCtxKey{}, w)
}

// replayWindowFrom extracts the replay window from ctx. The zero window
// (unbounded) is returned when none is set — a non-windowed caller then reads
// the full history, which is the correct degradation, not an error.
func replayWindowFrom(ctx context.Context) replayWindow {
	if ctx == nil {
		return replayWindow{}
	}
	w, _ := ctx.Value(replayWindowCtxKey{}).(replayWindow)
	return w
}

// replayWindowKey is the comparable identity of a replay window, used as the
// cold-start prior memo key (see priorMemo).
type replayWindowKey struct{ since, until time.Time }

// ReplayScorer scores a strategy by replaying its own historical execution
// evidence. It performs NO LLM call: the score is the mean of the strategy's
// KindFitness records inside the requested window.
//
// COLD START is the delicate part. A freshly generated candidate has no
// history, so its own evidence set is empty. Returning a fixed constant would
// resurrect the tie problem for the candidate side, and inventing a favourable
// number would make the shadow gate a rubber stamp. Instead the scorer falls
// back to the caller-supplied prior — in production the attribution-derived
// deterministic score, i.e. the CURRENT fleet-wide execution quality. The resulting
// verdict has a defensible reading: an untried candidate is promoted only when
// the fleet is currently performing better than the active strategy's own
// measured history, that is, when the active strategy is the thing holding
// quality back. Absent a live A/B execution path, that is the strongest honest
// statement available at zero token cost.
//
// EVIDENCE-INDEPENDENCE: the scorer deliberately does
// NOT widen a sparse window to the strategy's full history. A full-history
// read is bounded by replayQueryLimit with no server-side strategy filter, so
// under multi-strategy load it would silently truncate away the target
// strategy's records and return the prior anyway. Worse, returning one
// shared full-history mean for every sparse window would make MinSamples
// comparisons mutually REPEATED evidence — the same active value vs the same
// candidate prior, replayed N times with the same direction, which is
// exactly the "single score comparison wearing MinSamples' clothes" failure
// the windowed design exists to prevent. Each comparison therefore reads ONLY
// its own window: a window where the active has no records scores the prior,
// ties with the candidate, and is EXCLUDED from TotalComparisons rather
// than fabricating evidence.
//
// Thread-safe: all fields are read-only after construction and evidence.Store
// implementations are safe for concurrent use. priorMemo is guarded by its own
// mutex.
type ReplayScorer struct {
	// store is the shared evidence store the RuntimeObserver writes to.
	// A nil store makes every score fall back to the prior.
	store evidence.Store
	// prior supplies the cold-start score for a strategy with no history in
	// the requested window. Nil prior falls back to neutralPriorScore.
	prior func() float64
	// limit caps the records read per window query.
	limit int
	// priorMu guards the single-slot cold-start prior memo below.
	priorMu sync.Mutex
	// priorKey/priorVal/priorSet memoize the cold-start prior for the MOST
	// RECENT replay window. The ShadowEvaluator scores BOTH strategies of a
	// comparison inside the same ctx+window, and a volatile prior (e.g.
	// attribution-derived) could return slightly different values on two
	// successive reads — turning an intended prior-vs-prior tie into a fake
	// "decisive" comparison. Memoizing makes both sides of one
	// comparison see the SAME prior, so a genuine no-evidence comparison stays
	// an exact tie and is excluded.
	//
	// ONE SLOT, NOT A MAP: the memo only has to span the two Score
	// calls of a SINGLE comparison, which the sampler issues back-to-back for
	// one window before moving to the next. A per-window map would therefore
	// never serve a second hit — every Prime batch derives fresh windows from
	// `anchor = time.Now()` — while growing without bound for the lifetime of
	// the process. The single slot is O(1) and covers the only case that
	// matters; if an unexpected interleaving evicts the slot mid-comparison,
	// the evaluator's shadowTieEpsilon is the second layer that still reads
	// the two near-equal priors as a tie.
	priorKey replayWindowKey
	priorVal float64
	priorSet bool
}

// neutralPriorScore is used when no prior func is wired. It matches the
// deterministic scorer's neutral prior so the two agree on "no information".
const neutralPriorScore = 0.5

// replayQueryLimit caps one window's evidence read. Windows are short
// (replayWindowSpan), so this bounds a pathological burst rather than normal
// traffic.
const replayQueryLimit = 200

// ReplayScorerOption configures a ReplayScorer.
type ReplayScorerOption func(*ReplayScorer)

// WithReplayQueryLimit overrides the per-window query record cap. A
// non-positive value keeps the default replayQueryLimit (200), so a config
// error can never disable the scorer by setting limit to 0.
func WithReplayQueryLimit(limit int) ReplayScorerOption {
	return func(r *ReplayScorer) {
		if limit > 0 {
			r.limit = limit
		}
	}
}

// NewReplayScorer creates a zero-LLM per-strategy replay scorer.
//
// Args:
//
//	store - the shared evidence store (may be nil: every score is the prior).
//	prior - cold-start score source for strategies with no history in the
//	        window; nil uses the neutral 0.5.
//	opts  - optional configuration (see WithReplayQueryLimit).
//
// Returns:
//
//	*ReplayScorer - the configured scorer.
func NewReplayScorer(store evidence.Store, prior func() float64, opts ...ReplayScorerOption) *ReplayScorer {
	r := &ReplayScorer{store: store, prior: prior, limit: replayQueryLimit}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Score returns the strategy's mean historical fitness in [0,1] for the
// replay window carried by ctx, or the cold-start prior when the strategy has
// no records in that window.
//
// EVIDENCE-INDEPENDENCE: each comparison reads ONLY its own window.
// A strategy with no records in the window scores the prior. No full-history
// fallback — that would reintroduce repeated evidence across comparisons.
// The prior-vs-prior tie that results when both sides
// are cold is then excluded from TotalComparisons by the evaluator.
//
// COLD-START PRIOR MEMO: when both strategies in a comparison have no
// window records, both call this method with the same ctx (same window). The
// prior is evaluated ONCE per window and cached (priorMemo), so the second
// call returns the exact same value — an exact tie, not a near-equal score
// that could masquerade as decisive evidence.
//
// Args:
//
//	ctx - carries the replay window (see withReplayWindow) and cancellation.
//	s   - the strategy to score; nil returns the prior.
//
// Returns:
//
//	float64 - the score in [0,1].
func (r *ReplayScorer) Score(ctx context.Context, s *mutation.Strategy) float64 {
	if r == nil {
		return neutralPriorScore
	}
	if s == nil || s.ID == "" || r.store == nil {
		return r.coldStart()
	}
	w := replayWindowFrom(ctx)
	if mean, ok := r.strategyMean(ctx, s.ID, w.Since, w.Until); ok {
		return mean
	}
	return r.coldStartWithWindow(w)
}

// strategyMean queries the store for the strategy's fitness records within
// [since, until) and returns their mean. ok=false when the strategy has no
// valid records in range or the store errored (an error is missing
// information, not evidence of quality — both sides must degrade identically).
//
// Half-open vs inclusive stores: the window's upper bound is EXCLUSIVE
// ([since, until) — two abutting replay windows must not share a boundary
// record, or the same task's evidence would be counted twice across
// comparisons). Both production stores (MemoryStore, PostgresStore) treat
// Until INCLUSIVELY (`ts <= Until`), so the query passes `until-1ns` to push
// the shared instant out of THIS window and into the next one (whose Since
// equals this Until).
func (r *ReplayScorer) strategyMean(ctx context.Context, strategyID string, since, until time.Time) (float64, bool) {
	// Translate the half-open [since, until) into the stores' inclusive Until:
	// subtract one nanosecond so a record stamped exactly at `until` belongs to
	// the abutting window, not both.
	if !until.IsZero() {
		until = until.Add(-time.Nanosecond)
	}
	evs, err := r.store.Query(ctx, evidence.Filter{
		Source: observerEvidenceSource,
		Kind:   evidence.KindFitness,
		Since:  since,
		Until:  until,
		Limit:  r.limit,
	})
	if err != nil {
		return 0, false
	}
	sum, count := 0.0, 0
	for _, ev := range evs {
		if len(ev.Payload) == 0 {
			continue
		}
		var fe struct {
			Value      float64 `json:"value"`
			StrategyID string  `json:"strategy_id"`
		}
		if err := json.Unmarshal(ev.Payload, &fe); err != nil {
			continue
		}
		if fe.StrategyID != strategyID {
			continue
		}
		if fe.Value < 0 || fe.Value > 1 {
			continue
		}
		sum += fe.Value
		count++
	}
	if count == 0 {
		return 0, false
	}
	return sum / float64(count), true
}

// HasStore reports whether an evidence store is wired. Callers use it to
// decide whether replay is possible at all before advertising the scorer as
// an independent evidence source — a nil store would make every comparison a
// prior-vs-prior tie, i.e. the tie deadlock this scorer exists to remove.
func (r *ReplayScorer) HasStore() bool {
	return r != nil && r.store != nil
}

// coldStart returns the configured prior clamped to [0,1].
func (r *ReplayScorer) coldStart() float64 {
	if r == nil || r.prior == nil {
		return neutralPriorScore
	}
	v := r.prior()
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// coldStartWithWindow returns the prior clamped to [0,1], memoized for the
// current replay window. The evaluator calls Score twice per comparison
// (active + shadow) with the same ctx → same window. When both strategies
// cold-start, the memo ensures both calls return the EXACT same value,
// producing an exact tie that the evaluator correctly excludes from
// TotalComparisons.
//
// The memo is a SINGLE slot keyed by the window, not a map: the two calls of
// one comparison are back-to-back, and windows are never revisited (each Prime
// batch anchors on a fresh time.Now()), so a map would only ever grow. Moving
// to a new window overwrites the slot.
func (r *ReplayScorer) coldStartWithWindow(w replayWindow) float64 {
	if r == nil || r.prior == nil {
		return neutralPriorScore
	}
	key := replayWindowKey{since: w.Since, until: w.Until}
	r.priorMu.Lock()
	defer r.priorMu.Unlock()
	if r.priorSet && r.priorKey == key {
		return r.priorVal
	}
	v := r.coldStart()
	r.priorKey, r.priorVal, r.priorSet = key, v, true
	return v
}
