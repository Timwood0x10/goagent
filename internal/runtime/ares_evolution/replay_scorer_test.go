package evolution

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// memEvidenceStore is an in-memory evidence.Store honoring Since/Until so the
// windowed replay behaviour is verifiable without a database.
type memEvidenceStore struct {
	mu      sync.Mutex
	records []evidence.Evidence
	queries []evidence.Filter
	err     error
}

func (m *memEvidenceStore) Append(_ context.Context, ev evidence.Evidence) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, ev)
	return nil
}

func (m *memEvidenceStore) Query(_ context.Context, f evidence.Filter) ([]evidence.Evidence, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queries = append(m.queries, f)
	if m.err != nil {
		return nil, m.err
	}
	out := make([]evidence.Evidence, 0, len(m.records))
	for _, ev := range m.records {
		if f.Source != "" && ev.Source != f.Source {
			continue
		}
		if f.Kind != "" && ev.Kind != f.Kind {
			continue
		}
		// Time bounds are INCLUSIVE on both ends (since <= ts <= until),
		// matching the production MemoryStore/PostgresStore so a shared
		// boundary record is visible to the same windows it would hit in
		// production (review P1-4 — the old half-open Until hid that the
		// sampler's abutting windows overlap by one boundary instant).
		if !f.Since.IsZero() && ev.Timestamp.Before(f.Since) {
			continue
		}
		if !f.Until.IsZero() && ev.Timestamp.After(f.Until) {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}

func (m *memEvidenceStore) Aggregate(_ context.Context, _ evidence.Filter, _ evidence.AggregateFn) (float64, error) {
	return 0, nil
}

func (m *memEvidenceStore) windows() []evidence.Filter {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]evidence.Filter(nil), m.queries...)
}

// fitnessRecord builds one observer-shaped KindFitness record.
func fitnessRecord(strategyID string, value float64, at time.Time) evidence.Evidence {
	payload, _ := json.Marshal(map[string]any{
		"value":       value,
		"success":     value >= 0.5,
		"strategy_id": strategyID,
	})
	return evidence.Evidence{
		ID:        strategyID + "_" + at.Format("20060102150405.000000"),
		Source:    observerEvidenceSource,
		Kind:      evidence.KindFitness,
		Payload:   payload,
		Timestamp: at,
	}
}

func TestReplayScorerScoresPerStrategyMean(t *testing.T) {
	store := &memEvidenceStore{}
	now := time.Now()
	ctx := context.Background()
	// active averages 0.2, candidate averages 0.8 — the scorer must separate them.
	_ = store.Append(ctx, fitnessRecord("active", 0.1, now.Add(-time.Minute)))
	_ = store.Append(ctx, fitnessRecord("active", 0.3, now.Add(-2*time.Minute)))
	_ = store.Append(ctx, fitnessRecord("cand", 0.7, now.Add(-time.Minute)))
	_ = store.Append(ctx, fitnessRecord("cand", 0.9, now.Add(-2*time.Minute)))

	scorer := NewReplayScorer(store, func() float64 { return 0.5 })
	gotActive := scorer.Score(ctx, &mutation.Strategy{ID: "active"})
	gotCand := scorer.Score(ctx, &mutation.Strategy{ID: "cand"})

	if gotActive < 0.19 || gotActive > 0.21 {
		t.Fatalf("active mean = %.3f, want ~0.20", gotActive)
	}
	if gotCand < 0.79 || gotCand > 0.81 {
		t.Fatalf("candidate mean = %.3f, want ~0.80", gotCand)
	}
	if gotCand <= gotActive {
		t.Fatal("scorer must discriminate between strategies, got a tie")
	}
}

func TestReplayScorerColdStartUsesPrior(t *testing.T) {
	store := &memEvidenceStore{}
	scorer := NewReplayScorer(store, func() float64 { return 0.42 })
	got := scorer.Score(context.Background(), &mutation.Strategy{ID: "never-run"})
	if got < 0.419 || got > 0.421 {
		t.Fatalf("cold-start score = %.3f, want the prior 0.42", got)
	}
}

func TestReplayScorerPriorClampedAndNilSafe(t *testing.T) {
	store := &memEvidenceStore{}
	if got := NewReplayScorer(store, func() float64 { return 9 }).Score(context.Background(), &mutation.Strategy{ID: "x"}); got != 1 {
		t.Fatalf("prior above range = %v, want clamp to 1", got)
	}
	if got := NewReplayScorer(store, func() float64 { return -3 }).Score(context.Background(), &mutation.Strategy{ID: "x"}); got != 0 {
		t.Fatalf("prior below range = %v, want clamp to 0", got)
	}
	if got := NewReplayScorer(nil, nil).Score(context.Background(), nil); got != neutralPriorScore {
		t.Fatalf("nil store/strategy score = %v, want neutral prior", got)
	}
	var nilScorer *ReplayScorer
	if got := nilScorer.Score(context.Background(), &mutation.Strategy{ID: "x"}); got != neutralPriorScore {
		t.Fatalf("nil receiver score = %v, want neutral prior", got)
	}
	if nilScorer.HasStore() {
		t.Fatal("nil scorer must not claim a store")
	}
	if NewReplayScorer(nil, nil).HasStore() {
		t.Fatal("scorer without a store must not claim one")
	}
}

func TestReplayScorerStoreErrorFallsBackToPrior(t *testing.T) {
	store := &memEvidenceStore{err: context.DeadlineExceeded}
	got := NewReplayScorer(store, func() float64 { return 0.33 }).Score(context.Background(), &mutation.Strategy{ID: "x"})
	if got < 0.329 || got > 0.331 {
		t.Fatalf("store-error score = %.3f, want the prior 0.33", got)
	}
}

func TestReplayScorerIgnoresOtherStrategiesAndBadPayloads(t *testing.T) {
	store := &memEvidenceStore{}
	ctx := context.Background()
	now := time.Now()
	_ = store.Append(ctx, fitnessRecord("other", 1.0, now.Add(-time.Minute)))
	_ = store.Append(ctx, fitnessRecord("mine", 0.6, now.Add(-time.Minute)))
	// Out-of-range and malformed records must not pollute the mean.
	_ = store.Append(ctx, fitnessRecord("mine", 7.5, now.Add(-2*time.Minute)))
	_ = store.Append(ctx, evidence.Evidence{
		ID: "broken", Source: observerEvidenceSource, Kind: evidence.KindFitness,
		Payload: []byte("{not json"), Timestamp: now.Add(-time.Minute),
	})

	got := NewReplayScorer(store, func() float64 { return 0.5 }).Score(ctx, &mutation.Strategy{ID: "mine"})
	if got < 0.599 || got > 0.601 {
		t.Fatalf("score = %.3f, want 0.60 from the single valid own record", got)
	}
}

func TestReplayScorerHonorsWindowFromContext(t *testing.T) {
	store := &memEvidenceStore{}
	ctx := context.Background()
	now := time.Now()
	_ = store.Append(ctx, fitnessRecord("s", 0.9, now.Add(-5*time.Minute)))
	_ = store.Append(ctx, fitnessRecord("s", 0.1, now.Add(-25*time.Minute)))

	scorer := NewReplayScorer(store, func() float64 { return 0.5 })
	recent := scorer.Score(withReplayWindow(ctx, replayWindow{Since: now.Add(-10 * time.Minute), Until: now}), &mutation.Strategy{ID: "s"})
	older := scorer.Score(withReplayWindow(ctx, replayWindow{Since: now.Add(-30 * time.Minute), Until: now.Add(-20 * time.Minute)}), &mutation.Strategy{ID: "s"})

	if recent < 0.89 || recent > 0.91 {
		t.Fatalf("recent window score = %.3f, want ~0.90", recent)
	}
	if older < 0.09 || older > 0.11 {
		t.Fatalf("older window score = %.3f, want ~0.10", older)
	}
}

func TestReplayWindowFromNilContext(t *testing.T) {
	// Held in a variable so the linter's literal-nil-Context check does not
	// fire; the point is to prove the guard, not to model good practice.
	var nilCtx context.Context
	if w := replayWindowFrom(nilCtx); !w.Since.IsZero() || !w.Until.IsZero() {
		t.Fatal("nil context must yield the unbounded window")
	}
	if w := replayWindowFrom(context.Background()); !w.Since.IsZero() || !w.Until.IsZero() {
		t.Fatal("context without a window must yield the unbounded window")
	}
}

// TestReplayScorerSparseWindowScoresPrior pins the P1-1/P1-2 contract: a
// strategy with NO records inside its window scores the cold-start prior, NOT
// a full-history mean. Widening a sparse window to all history would (a) be
// silently truncated by replayQueryLimit on a production store (no server-side
// strategy filter), and (b) make every sparse window of the same strategy
// return the SAME mean — repeated evidence that satisfies MinSamples by
// repetition, the exact failure C3.2 forbids. The prior-vs-prior tie this
// produces is excluded from TotalComparisons by the evaluator (B-3).
func TestReplayScorerSparseWindowScoresPrior(t *testing.T) {
	store := &memEvidenceStore{}
	ctx := context.Background()
	now := time.Now()
	span := replayWindowSpan
	// The active strategy has records only in an OLD window (>> 4 spans back);
	// the sampled replay windows are the recent 4, where it has nothing.
	old := now.Add(-10 * span)
	_ = store.Append(ctx, fitnessRecord("active", 0.2, old.Add(-span/2)))
	_ = store.Append(ctx, fitnessRecord("active", 0.3, old.Add(-3*span/2)))

	scorer := NewReplayScorer(store, func() float64 { return 0.5 })
	// Score the active inside a recent window where it has no records of its
	// own. It must return the prior (no widening to full history).
	recentWindow := replayWindow{Since: now.Add(-4 * span), Until: now}
	got := scorer.Score(withReplayWindow(ctx, recentWindow), &mutation.Strategy{ID: "active"})
	if got < 0.499 || got > 0.501 {
		t.Fatalf("sparse-window active score = %.3f, want the prior 0.5 (no full-history widening)", got)
	}

	// The never-executed candidate also gets the prior, so the two sides of a
	// sparse-window comparison are an exact prior-vs-prior tie — which the
	// evaluator excludes from TotalComparisons rather than fabricating
	// evidence.
	cand := scorer.Score(withReplayWindow(ctx, recentWindow), &mutation.Strategy{ID: "cand"})
	if cand < 0.499 || cand > 0.501 {
		t.Fatalf("candidate cold-start score = %.3f, want the prior 0.5", cand)
	}
	if got != cand {
		t.Fatal("sparse-window comparison must be an exact prior-vs-prior tie")
	}
}

// TestShadowSamplerUsesDisjointReplayWindows is the C3.2 acceptance check:
// MinSamples must be satisfied by INDEPENDENT evidence, not by repeating one
// verdict. Each comparison must read a different, non-overlapping slice of
// history. The scorer does NOT widen a sparse window to full history (that
// would reintroduce repetition — see P1-1/P1-2), so every query is a bounded
// window query and there are no fallback reads.
func TestShadowSamplerUsesDisjointReplayWindows(t *testing.T) {
	store := &memEvidenceStore{}
	eval := NewShadowEvaluator(ShadowEvaluationConfig{MinSamples: 4, MinWinRate: 0.55})
	eval.SetShadowScorer(NewReplayScorer(store, func() float64 { return 0.5 }).Score)

	sampler := NewShadowSampler(eval, 4)
	sampler.Prime(context.Background(), &mutation.Strategy{ID: "cand"}, &mutation.Strategy{ID: "active"})

	// The scorer queries ONLY the requested window (no full-history fallback),
	// so every query is bounded and windowed. 4 comparisons × 2 strategies = 8.
	queries := store.windows()
	if len(queries) != 8 {
		t.Fatalf("evidence queries = %d, want 8 (4 comparisons x 2 strategies)", len(queries))
	}

	// Collect the distinct windows and assert they tile history without overlap.
	type span struct{ since, until time.Time }
	seen := make([]span, 0, 4)
	for i, f := range queries {
		if f.Since.IsZero() || f.Until.IsZero() {
			t.Fatalf("query %d used an unbounded window: replay must be windowed", i)
		}
		if i%2 == 0 {
			seen = append(seen, span{f.Since, f.Until})
			continue
		}
		// Both strategies in one comparison must share the same window,
		// otherwise the comparison would measure different time periods.
		prev := seen[len(seen)-1]
		if !f.Since.Equal(prev.since) || !f.Until.Equal(prev.until) {
			t.Fatalf("comparison %d scored its two strategies over different windows", i/2)
		}
	}
	if len(seen) != 4 {
		t.Fatalf("distinct comparison windows = %d, want 4", len(seen))
	}
	// P1-4: windows are half-open [since, until) and tile history without
	// overlap. The scorer passes `until-1ns` to the inclusive stores so the
	// shared boundary instant belongs to exactly one window; at the
	// semantic level the adjacent window's since must equal the previous
	// window's until (abutting). At the store-filter level (seen) the 1ns
	// adjustment makes the two abut: seen[i].until + 1ns == seen[i-1].since.
	for i := 1; i < len(seen); i++ {
		if !seen[i].until.Add(time.Nanosecond).Equal(seen[i-1].since) {
			t.Fatalf("window %d does not abut window %d: windows must be disjoint and contiguous (semantic half-open [since, until), store filter has 1ns gap)", i, i-1)
		}
	}
}

// TestShadowSamplerReplayProducesRealWinRate proves the tie deadlock is gone:
// with per-strategy history the win rate lands strictly between 0 and 1,
// reflecting the candidate winning in some windows and losing in others.
func TestShadowSamplerReplayProducesRealWinRate(t *testing.T) {
	store := &memEvidenceStore{}
	ctx := context.Background()
	now := time.Now()
	span := replayWindowSpan
	// Candidate wins the two most recent windows, loses the two before them.
	for i, pair := range []struct{ cand, active float64 }{
		{0.9, 0.2}, {0.8, 0.3}, {0.2, 0.9}, {0.1, 0.8},
	} {
		at := now.Add(-time.Duration(i)*span - span/2)
		_ = store.Append(ctx, fitnessRecord("cand", pair.cand, at))
		_ = store.Append(ctx, fitnessRecord("active", pair.active, at))
	}

	eval := NewShadowEvaluator(ShadowEvaluationConfig{MinSamples: 4, MinWinRate: 0.55})
	eval.SetShadowScorer(NewReplayScorer(store, func() float64 { return 0.5 }).Score)
	NewShadowSampler(eval, 4).Prime(ctx, &mutation.Strategy{ID: "cand"}, &mutation.Strategy{ID: "active"})

	ok, report := eval.ShouldDeploy()
	if report == nil {
		t.Fatal("expected a report")
	}
	if report.TotalComparisons != 4 {
		t.Fatalf("comparisons = %d, want 4", report.TotalComparisons)
	}
	if report.ShadowWins != 2 {
		t.Fatalf("shadow wins = %d, want 2 (candidate better in the 2 recent windows only)", report.ShadowWins)
	}
	if report.WinRate <= 0 || report.WinRate >= 1 {
		t.Fatalf("win rate = %.2f; a deterministic tie/repetition would give 0.0 or 1.0", report.WinRate)
	}
	if ok {
		t.Fatalf("win rate %.2f is below the 0.55 threshold, want reject", report.WinRate)
	}

	// A candidate that is better in EVERY window must pass — the gate is not
	// merely always-reject.
	store2 := &memEvidenceStore{}
	for i := 0; i < 4; i++ {
		at := now.Add(-time.Duration(i)*span - span/2)
		_ = store2.Append(ctx, fitnessRecord("cand", 0.9, at))
		_ = store2.Append(ctx, fitnessRecord("active", 0.2, at))
	}
	eval2 := NewShadowEvaluator(ShadowEvaluationConfig{MinSamples: 4, MinWinRate: 0.55})
	eval2.SetShadowScorer(NewReplayScorer(store2, func() float64 { return 0.5 }).Score)
	NewShadowSampler(eval2, 4).Prime(ctx, &mutation.Strategy{ID: "cand"}, &mutation.Strategy{ID: "active"})
	if pass, rep := eval2.ShouldDeploy(); !pass {
		t.Fatalf("uniformly better candidate rejected: %+v", rep)
	}
}
