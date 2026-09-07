package evolution

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// --- LifecycleSnapshot tests ---

func TestCandidateState_String(t *testing.T) {
	tests := []struct {
		state CandidateState
		want  string
	}{
		{StateCandidate, "candidate"},
		{StateShadow, "shadow"},
		{StateActive, "active"},
		{StateDegraded, "degraded"},
		{CandidateState(99), "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.state.String())
		})
	}
}

// --- LifecycleConfig tests ---

func TestDefaultLifecycleConfig(t *testing.T) {
	cfg := DefaultLifecycleConfig()
	assert.True(t, cfg.Enabled)
	assert.Equal(t, 50, cfg.FitnessWindow)
	assert.Equal(t, 10, cfg.MinSamplesBeforeJudge)
	assert.InDelta(t, 0.5, cfg.ColdStartScore, 0.001)
	assert.Equal(t, defaultWatchInterval, cfg.WatchInterval)
	assert.InDelta(t, 0.7, cfg.Gates.EvalMinScore, 0.001)
	assert.False(t, cfg.Gates.RequireManualApproval)
}

// --- StrategyLifecycle tests ---

func newTestLifecycle(t *testing.T, cfg LifecycleConfig) (*StrategyLifecycle, *ActiveStrategyManager, evidence.Store) {
	t.Helper()
	store := evidence.NewMemoryStore()
	asm, err := NewActiveStrategyManager(newMockStrategyStore(), NewRollbackPolicy())
	require.NoError(t, err)

	aggCfg := DefaultAggregatorConfig()
	agg := NewRuntimeFitnessAggregator(store, aggCfg)

	lc := NewStrategyLifecycle(asm, agg, cfg,
		WithLifecycleEvidenceStore(store),
	)
	return lc, asm, store
}

func TestNewStrategyLifecycle_NilSafe(t *testing.T) {
	// nil lifecycle must not panic on any method
	var lc *StrategyLifecycle
	assert.NotPanics(t, func() { lc.Start(context.Background()) })
	assert.NotPanics(t, func() { lc.Stop() })
	assert.NotPanics(t, func() { lc.Submit(context.Background(), nil, 0) })
	assert.NotPanics(t, func() { lc.Approve() })

	snap := lc.Snapshot()
	assert.Equal(t, "disabled", snap.State)
}

func TestStrategyLifecycle_Snapshot_InitialState(t *testing.T) {
	lc, asm, _ := newTestLifecycle(t, DefaultLifecycleConfig())
	// Deploy a base strategy so Current() is non-nil.
	require.NoError(t, asm.Deploy(context.Background(),
		&mutation.Strategy{ID: "base", Version: 1, Score: 50.0},
	))

	snap := lc.Snapshot()
	assert.Equal(t, "active", snap.State)
	assert.Equal(t, "base", snap.ActiveID)
	assert.Equal(t, 0, snap.Generation)
	assert.False(t, snap.PendingApproval)
}

func TestStrategyLifecycle_LifecycleSnapshotMap(t *testing.T) {
	lc, asm, _ := newTestLifecycle(t, DefaultLifecycleConfig())
	require.NoError(t, asm.Deploy(context.Background(),
		&mutation.Strategy{ID: "base", Version: 1, Score: 50.0},
	))

	m := lc.LifecycleSnapshot()
	assert.Equal(t, "active", m["state"])
	assert.Equal(t, "base", m["active_id"])
	assert.Equal(t, 0, m["generation"])
	// last_decision should not be present when empty
	_, ok := m["last_decision"]
	assert.False(t, ok)
}

func TestStrategyLifecycle_Submit_Blacklisted_CrossGeneration(t *testing.T) {
	cfg := DefaultLifecycleConfig()
	cfg.BlacklistGenerations = 2
	lc, asm, store := newTestLifecycle(t, cfg)
	require.NoError(t, asm.Deploy(context.Background(),
		&mutation.Strategy{ID: "base", Version: 1, Score: 50.0},
	))

	candidate := &mutation.Strategy{ID: "bad", Version: 2, Score: 30.0}

	// Simulate a rollback at generation 0: the candidate is banned for
	// BlacklistGenerations (2) → banUntil = 2. Submissions at generations 0
	// and 1 must be rejected; the ban LIFTS at generation 2 (§9: an
	// N-generation damping window, not a 0-generation no-op).
	lc.mu.Lock()
	lc.blacklist[candidate.ID] = 2
	lc.mu.Unlock()

	lc.Submit(context.Background(), candidate, 0)
	assert.Equal(t, "base", asm.Current().ID, "banned at generation 0")

	lc.Submit(context.Background(), candidate, 1)
	assert.Equal(t, "base", asm.Current().ID, "banned at generation 1")

	lc.Submit(context.Background(), candidate, 2)
	assert.Equal(t, "bad", asm.Current().ID, "ban lifts at generation 2")

	// The expired entry was pruned during the accepted Submit.
	lc.mu.Lock()
	_, stillListed := lc.blacklist[candidate.ID]
	lc.mu.Unlock()
	assert.False(t, stillListed, "expired blacklist entry must be pruned")

	// Accepted submits write promote decision evidence.
	evs, err := store.Query(context.Background(), evidence.Filter{
		Source: "lifecycle", Kind: evidence.KindFitness, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, evs, 1, "exactly one promote decision (the accepted submit)")
}

func TestStrategyLifecycle_Submit_NoGates(t *testing.T) {
	lc, asm, _ := newTestLifecycle(t, DefaultLifecycleConfig())
	require.NoError(t, asm.Deploy(context.Background(),
		&mutation.Strategy{ID: "base", Version: 1, Score: 50.0},
	))

	candidate := &mutation.Strategy{ID: "better", Version: 2, Score: 80.0}

	// No gates configured → Submit promotes directly.
	lc.Submit(context.Background(), candidate, 1)

	assert.Equal(t, "better", asm.Current().ID)
	assert.Equal(t, "base", asm.Previous().ID)

	snap := lc.Snapshot()
	assert.Equal(t, "promoted", snap.LastDecision)
}

func TestStrategyLifecycle_Submit_GateReject(t *testing.T) {
	cfg := DefaultLifecycleConfig()
	lc, asm, _ := newTestLifecycle(t, cfg)
	require.NoError(t, asm.Deploy(context.Background(),
		&mutation.Strategy{ID: "base", Version: 1, Score: 50.0},
	))

	// Add a gate that always rejects.
	rejectGate := &mockGate{name: "always-reject", pass: false, reason: "too low"}
	WithLifecycleGates(rejectGate)(lc)

	candidate := &mutation.Strategy{ID: "cand", Version: 2, Score: 40.0}
	lc.Submit(context.Background(), candidate, 1)

	// Active strategy should remain "base".
	assert.Equal(t, "base", asm.Current().ID)

	snap := lc.Snapshot()
	assert.Equal(t, "active", snap.State)
}

func TestStrategyLifecycle_Submit_GatePass(t *testing.T) {
	cfg := DefaultLifecycleConfig()
	lc, asm, _ := newTestLifecycle(t, cfg)
	require.NoError(t, asm.Deploy(context.Background(),
		&mutation.Strategy{ID: "base", Version: 1, Score: 50.0},
	))

	passGate := &mockGate{name: "always-pass", pass: true, reason: "ok"}
	WithLifecycleGates(passGate)(lc)

	candidate := &mutation.Strategy{ID: "better", Version: 2, Score: 80.0}
	lc.Submit(context.Background(), candidate, 1)

	assert.Equal(t, "better", asm.Current().ID)
}

func TestStrategyLifecycle_ManualApproval_NonBlocking(t *testing.T) {
	cfg := DefaultLifecycleConfig()
	cfg.Gates.RequireManualApproval = true

	lc, asm, _ := newTestLifecycle(t, cfg)
	require.NoError(t, asm.Deploy(context.Background(),
		&mutation.Strategy{ID: "base", Version: 1, Score: 50.0},
	))

	candidate := &mutation.Strategy{ID: "approved-cand", Version: 2, Score: 80.0}

	// Submit must RETURN immediately — the candidate is held, never the
	// caller's goroutine (the ticker/adapter path must not block on human
	// latency).
	done := make(chan struct{})
	go func() {
		lc.Submit(context.Background(), candidate, 1)
		close(done)
	}()
	select {
	case <-done:
		// Submit returned without waiting for approval. Good.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Submit blocked on manual approval — the hold must be on the candidate, not the caller")
	}

	// The candidate is in SHADOW state, pending approval, NOT promoted.
	snap := lc.Snapshot()
	assert.Equal(t, "shadow", snap.State)
	assert.True(t, snap.PendingApproval)
	assert.Equal(t, "base", asm.Current().ID)

	// A second Submit while one is pending is rejected (replacing the held
	// candidate silently would defeat the gate).
	other := &mutation.Strategy{ID: "other-cand", Version: 3, Score: 90.0}
	lc.Submit(context.Background(), other, 2)
	assert.Equal(t, "base", asm.Current().ID, "submissions are rejected while approval is pending")
	lc.mu.Lock()
	assert.Equal(t, "approved-cand", lc.heldCandidate.ID, "the originally held candidate is kept")
	lc.mu.Unlock()

	// Approve promotes the HELD candidate.
	lc.Approve()
	assert.Equal(t, "approved-cand", asm.Current().ID)

	snap = lc.Snapshot()
	assert.False(t, snap.PendingApproval)
	assert.Equal(t, "promoted", snap.LastDecision)
}

func TestStrategyLifecycle_ManualApproval_HoldSurvivesCallerContext(t *testing.T) {
	cfg := DefaultLifecycleConfig()
	cfg.Gates.RequireManualApproval = true

	lc, asm, _ := newTestLifecycle(t, cfg)
	require.NoError(t, asm.Deploy(context.Background(),
		&mutation.Strategy{ID: "base", Version: 1, Score: 50.0},
	))

	candidate := &mutation.Strategy{ID: "cand-cancel", Version: 2, Score: 80.0}

	// The submitting caller's context dies (e.g. ticker tick cancelled) —
	// the HOLD must survive it: the candidate stays pending and can still
	// be approved. (The old blocking design cancelled the promotion on
	// ctx.Done() AND left a residue token in approvalCh that would silently
	// auto-approve the NEXT candidate.)
	ctx, cancel := context.WithCancel(context.Background())
	lc.Submit(ctx, candidate, 1)
	cancel()

	snap := lc.Snapshot()
	assert.True(t, snap.PendingApproval, "hold survives the caller's context")
	assert.Equal(t, "base", asm.Current().ID)

	lc.Approve()
	assert.Equal(t, "cand-cancel", asm.Current().ID, "Approve still promotes the held candidate")
}

// TestStrategyLifecycle_Approve_ConcurrentExactlyOnePromote locks the
// take-and-clear critical section: N concurrent approvals of the same held
// candidate must produce exactly ONE promote. The old two-phase Approve let
// two callers both promote, which set asm.previous = asm.current = the same
// strategy — later rollbacks would "restore" the strategy to itself and
// degradation could never be undone.
func TestStrategyLifecycle_Approve_ConcurrentExactlyOnePromote(t *testing.T) {
	cfg := DefaultLifecycleConfig()
	cfg.Gates.RequireManualApproval = true
	lc, asm, store := newTestLifecycle(t, cfg)
	require.NoError(t, asm.Deploy(context.Background(),
		&mutation.Strategy{ID: "base", Version: 1, Score: 50.0},
	))

	held := &mutation.Strategy{ID: "held-cand", Version: 2, Score: 80.0}
	lc.Submit(context.Background(), held, 1)
	require.True(t, lc.Snapshot().PendingApproval)

	const approvers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < approvers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			lc.Approve()
		}()
	}
	close(start)
	wg.Wait()

	assert.Equal(t, "held-cand", asm.Current().ID)
	assert.NotEqual(t, "held-cand", asm.Previous().ID,
		"previous must stay 'base': a double promote would make previous == current")
	assert.False(t, lc.Snapshot().PendingApproval)

	evs, err := store.Query(context.Background(), evidence.Filter{
		Source: "lifecycle", Kind: evidence.KindFitness, Limit: 10,
	})
	require.NoError(t, err)
	promotes := 0
	for _, ev := range evs {
		if strings.Contains(ev.ID, "promote") {
			promotes++
		}
	}
	assert.Equal(t, 1, promotes, "exactly one promote decision may be recorded")
}

func TestStrategyLifecycle_WriteDecisionEvidence(t *testing.T) {
	cfg := DefaultLifecycleConfig()
	lc, asm, store := newTestLifecycle(t, cfg)
	require.NoError(t, asm.Deploy(context.Background(),
		&mutation.Strategy{ID: "base", Version: 1, Score: 50.0},
	))

	candidate := &mutation.Strategy{ID: "better", Version: 2, Score: 80.0}
	lc.Submit(context.Background(), candidate, 1)

	// Check that promote evidence was written under the dedicated decision
	// source ("lifecycle", NOT "strategy"): decision events are 0-100 GA
	// scores and must never enter the [0,1] fitness window consumed by the
	// aggregator from the "strategy" source.
	evs, err := store.Query(context.Background(), evidence.Filter{
		Source: "lifecycle",
		Kind:   evidence.KindFitness,
		Limit:  10,
	})
	require.NoError(t, err)
	require.Len(t, evs, 1)

	// The evidence ID should contain "promote".
	assert.Contains(t, evs[0].ID, "promote")
	assert.Contains(t, evs[0].ID, "better")
}

func TestStrategyLifecycle_Approve_NoOpWhenNotPending(t *testing.T) {
	lc, _, _ := newTestLifecycle(t, DefaultLifecycleConfig())
	// Approve must be a no-op when no candidate is pending.
	assert.NotPanics(t, func() { lc.Approve() })
}

// newTestLifecycleWithShadow builds a lifecycle whose G2 shadow gate is
// registered (the production shape), backed by an empty ShadowEvaluator.
func newTestLifecycleWithShadow(t *testing.T, cfg LifecycleConfig) (*StrategyLifecycle, *ActiveStrategyManager, *ShadowEvaluator, evidence.Store) {
	t.Helper()
	store := evidence.NewMemoryStore()
	asm, err := NewActiveStrategyManager(newMockStrategyStore(), NewRollbackPolicy())
	require.NoError(t, err)
	agg := NewRuntimeFitnessAggregator(store, DefaultAggregatorConfig())
	se := NewShadowEvaluator(ShadowEvaluationConfig{Enabled: true, MinSamples: 2, MinWinRate: 0.6})
	lc := NewStrategyLifecycle(asm, agg, cfg,
		WithLifecycleShadowEvaluator(se),
		WithLifecycleEvidenceStore(store),
	)
	return lc, asm, se, store
}

// TestStrategyLifecycle_Submit_SeedDeployWhenNoActive locks the seed-deploy
// exception: with NO active strategy there is nothing to shadow-compare
// against, so the first candidate is promoted without gates and becomes the
// baseline that rollback relies on as "previous" (§9).
func TestStrategyLifecycle_Submit_SeedDeployWhenNoActive(t *testing.T) {
	lc, asm, _, _ := newTestLifecycleWithShadow(t, DefaultLifecycleConfig())

	seed := &mutation.Strategy{ID: "seed-v1", Version: 1, Score: 50.0}
	lc.Submit(context.Background(), seed, 0)

	assert.Equal(t, "seed-v1", asm.Current().ID,
		"first candidate must deploy as the seed baseline without gates")
	assert.Equal(t, "promoted", lc.Snapshot().LastDecision)

	// The seed deploy reset the rollback window (promote-side Reset).
	decision := asm.RollbackPolicy().Evaluate()
	require.NotNil(t, decision)
	assert.Contains(t, decision.Reason, "no score data",
		"rollback window must be clean right after a promote")
}

// TestStrategyLifecycle_Submit_SeedExemptionIsOneShot locks the seeded flag:
// after the one seed deployment, the gate-free path can NEVER re-open — not
// even when the ASM later reports no active strategy (store reset/emptied),
// which would otherwise let a candidate skip all verification a second time.
func TestStrategyLifecycle_Submit_SeedExemptionIsOneShot(t *testing.T) {
	lc, asm, _, store := newTestLifecycleWithShadow(t, DefaultLifecycleConfig())

	seed := &mutation.Strategy{ID: "seed-v1", Version: 1, Score: 50.0}
	lc.Submit(context.Background(), seed, 0)
	require.Equal(t, "seed-v1", asm.Current().ID)
	require.True(t, lc.seeded, "seed flag must flip on the first Submit")

	// Simulate the ASM losing its active strategy (reset / emptied store).
	asm.mu.Lock()
	asm.current = nil
	asm.previous = nil
	asm.mu.Unlock()

	// No shadow data → fail-closed gates. The candidate must NOT get a
	// second gate-free deploy, and the active stays gone.
	cand := &mutation.Strategy{ID: "cand-after-reset", Version: 2, Score: 90.0}
	lc.Submit(context.Background(), cand, 1)
	assert.Nil(t, asm.Current(), "seed exemption must be one-shot: no re-deploy after reset")

	// C3.3: the rejected candidate now ALSO writes a decision record
	// (gate=shadow reject), so the trail has 2 entries: 1 promote (seed)
	// + 1 reject (the gate-rejected candidate). Previously only the
	// promote was recorded, making gate rejections invisible in the
	// decision trail.
	evs, err := store.Query(context.Background(), evidence.Filter{
		Source: "lifecycle", Kind: evidence.KindFitness, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, evs, 2, "seed promote + gate reject = 2 decision records")
	// One of them is the seed promote.
	var hasPromote bool
	for _, ev := range evs {
		if strings.Contains(ev.ID, "promote_seed-v1") {
			hasPromote = true
		}
	}
	assert.True(t, hasPromote, "the seed promote evidence must be present")
}

// TestStrategyLifecycle_ShadowGate_FailClosedOnNoData locks review blocking
// item 1: with zero shadow comparisons the G2 gate REJECTS (design doc §3.1
// "fewer than MinSamples samples → the candidate stays in SHADOW and is NOT
// deployed"), it does NOT pass through. The previous
// pass-through made the whole verify pipeline a rubber stamp in default
// configs where nothing feeds comparisons.
func TestStrategyLifecycle_ShadowGate_FailClosedOnNoData(t *testing.T) {
	lc, asm, _, _ := newTestLifecycleWithShadow(t, DefaultLifecycleConfig())
	require.NoError(t, asm.Deploy(context.Background(),
		&mutation.Strategy{ID: "base", Version: 1, Score: 50.0},
	))

	candidate := &mutation.Strategy{ID: "unverified", Version: 2, Score: 90.0}
	lc.Submit(context.Background(), candidate, 1)

	assert.Equal(t, "base", asm.Current().ID,
		"zero shadow evidence must fail-closed, not promote")
	snap := lc.Snapshot()
	assert.Equal(t, "active", snap.State)
	assert.Empty(t, snap.ShadowID, "rejected candidate must not stay attached")

	// The rejection is recorded on the gate-reject counter path (no panic
	// without metrics wired) and no promote evidence was written.
	lc.mu.Lock()
	defer lc.mu.Unlock()
	assert.Nil(t, lc.heldCandidate)
}

// TestStrategyLifecycle_Sampler_PrimedGatePromotesWinningCandidate locks the
// P0-9 integration: when a ShadowSampler is wired (the default config path,
// DreamCycle disabled), Submit primes it before the G2 gate so a candidate
// that genuinely outperforms the active one earns promotion instead of being
// stuck fail-closed in SHADOW.
func TestStrategyLifecycle_Sampler_PrimedGatePromotesWinningCandidate(t *testing.T) {
	store := evidence.NewMemoryStore()
	asm, err := NewActiveStrategyManager(newMockStrategyStore(), NewRollbackPolicy())
	require.NoError(t, err)
	agg := NewRuntimeFitnessAggregator(store, DefaultAggregatorConfig())
	se := NewShadowEvaluator(ShadowEvaluationConfig{Enabled: true, MinSamples: 3, MinWinRate: 0.6})
	// Deterministic scorer: candidate always beats active.
	se.SetShadowScorer(func(_ context.Context, s *mutation.Strategy) float64 {
		if s.ID == "base" {
			return 0.6
		}
		return 0.9
	})
	lc := NewStrategyLifecycle(asm, agg, DefaultLifecycleConfig(),
		WithLifecycleShadowEvaluator(se),
		WithLifecycleShadowSampler(NewShadowSampler(se, 3)),
		WithLifecycleEvidenceStore(store),
	)
	require.NoError(t, asm.Deploy(context.Background(),
		&mutation.Strategy{ID: "base", Version: 1, Score: 50.0},
	))

	candidate := &mutation.Strategy{ID: "winner", Version: 2, Score: 80.0}
	lc.Submit(context.Background(), candidate, 1)

	assert.Equal(t, "winner", asm.Current().ID,
		"winning candidate must pass the primed G2 shadow gate and be promoted")
}

// TestStrategyLifecycle_Sampler_PrimedGateRejectsLosingCandidate locks the
// other side of the P0-9 contract: a candidate the sampler judges as WORSE is
// still rejected by the G2 gate (the sampler supplies evidence, it does not
// rubber-stamp).
func TestStrategyLifecycle_Sampler_PrimedGateRejectsLosingCandidate(t *testing.T) {
	store := evidence.NewMemoryStore()
	asm, err := NewActiveStrategyManager(newMockStrategyStore(), NewRollbackPolicy())
	require.NoError(t, err)
	agg := NewRuntimeFitnessAggregator(store, DefaultAggregatorConfig())
	se := NewShadowEvaluator(ShadowEvaluationConfig{Enabled: true, MinSamples: 3, MinWinRate: 0.6})
	// Deterministic scorer: candidate always loses to active.
	se.SetShadowScorer(func(_ context.Context, s *mutation.Strategy) float64 {
		if s.ID == "base" {
			return 0.9
		}
		return 0.2
	})
	lc := NewStrategyLifecycle(asm, agg, DefaultLifecycleConfig(),
		WithLifecycleShadowEvaluator(se),
		WithLifecycleShadowSampler(NewShadowSampler(se, 3)),
		WithLifecycleEvidenceStore(store),
	)
	require.NoError(t, asm.Deploy(context.Background(),
		&mutation.Strategy{ID: "base", Version: 1, Score: 50.0},
	))

	candidate := &mutation.Strategy{ID: "loser", Version: 2, Score: 80.0}
	lc.Submit(context.Background(), candidate, 1)

	assert.Equal(t, "base", asm.Current().ID,
		"losing candidate must be rejected by the primed G2 shadow gate")
}

// TestStrategyLifecycle_Promote_ResetsRollbackWindow locks §8 item 5: a
// promote resets the rollback score window so the new strategy is not judged
// against the OLD strategy's low scores on its first watch tick.
func TestStrategyLifecycle_Promote_ResetsRollbackWindow(t *testing.T) {
	lc, asm, _ := newTestLifecycle(t, DefaultLifecycleConfig())
	require.NoError(t, asm.Deploy(context.Background(),
		&mutation.Strategy{ID: "base", Version: 1, Score: 50.0},
	))

	// Simulate a degraded history on the OLD strategy.
	asm.RecordScore(0, 0.1)
	asm.RecordScore(0, 0.2)

	lc.Submit(context.Background(), &mutation.Strategy{ID: "better", Version: 2, Score: 80.0}, 1)
	require.Equal(t, "better", asm.Current().ID)

	decision := asm.RollbackPolicy().Evaluate()
	require.NotNil(t, decision)
	assert.Contains(t, decision.Reason, "no score data",
		"rollback window must be empty right after promote")
}

// TestStrategyLifecycle_Watch_DecorrelatesRepeatedTicks locks §8 item 6:
// evaluateAndMaybeRollback records a score only when the evidence window
// ADVANCED — re-running with the same evidence batch must not feed the same
// mean into RollbackPolicy again.
func TestStrategyLifecycle_Watch_DecorrelatesRepeatedTicks(t *testing.T) {
	cfg := DefaultLifecycleConfig()
	// The test drives the rollback feed directly, so the net must be armed
	// (E2: an explicitly disarmed rollback skips the watch loop entirely).
	cfg.RollbackArmed = true
	lc, asm, store := newTestLifecycle(t, cfg)
	require.NoError(t, asm.Deploy(context.Background(),
		&mutation.Strategy{ID: "base", Version: 1, Score: 50.0},
	))

	// Seed 12 strategy-source fitness samples (≥ MinSamplesBeforeJudge 10).
	ctx := context.Background()
	seedWindowFitness(t, store, "base", 1.0, 12)

	lc.evaluateAndMaybeRollback(ctx)
	require.Len(t, asm.RollbackPolicy().scoreHistory, 1, "first tick records once")

	lc.evaluateAndMaybeRollback(ctx)
	lc.evaluateAndMaybeRollback(ctx)
	assert.Len(t, asm.RollbackPolicy().scoreHistory, 1,
		"repeated ticks over the SAME evidence must not re-record")
}

// TestStrategyLifecycle_Watch_SaturatedWindowStillAdvances is the regression
// for the decorrelation-by-count bug: once every source saturates at
// WindowSize (50), the window's record count stays FLAT under steady-state
// churn ("one in, one out"). A count-based advance check would therefore
// never fire again and the rollback feed would silently die — no error, no
// warning, and /api/evolution/lifecycle would keep showing a healthy
// window_count (~250). The advance signal must be the window's newest
// evidence TIMESTAMP. Seeds 120 records (> WindowSize) to cover the
// saturation path the original 12-record test missed.
func TestStrategyLifecycle_Watch_SaturatedWindowStillAdvances(t *testing.T) {
	cfg := DefaultLifecycleConfig()
	// The test drives the rollback feed directly, so the net must be armed
	// (E2: an explicitly disarmed rollback skips the watch loop entirely).
	cfg.RollbackArmed = true
	lc, asm, store := newTestLifecycle(t, cfg)
	require.NoError(t, asm.Deploy(context.Background(),
		&mutation.Strategy{ID: "base", Version: 1, Score: 50.0},
	))

	ctx := context.Background()
	// Saturate the strategy source well past WindowSize (50): the window
	// count is pinned at 50 from here on.
	seedWindowFitness(t, store, "base", 1.0, 120)

	lc.evaluateAndMaybeRollback(ctx)
	require.Len(t, asm.RollbackPolicy().scoreHistory, 1, "first tick records once")

	// Steady-state churn: 10 NEW records enter, 10 oldest are evicted —
	// count stays 50 but the window content (and LastAt) advanced.
	seedWindowFitness(t, store, "base", 0.0, 10)

	lc.evaluateAndMaybeRollback(ctx)
	assert.Len(t, asm.RollbackPolicy().scoreHistory, 2,
		"saturated window with NEW evidence must still record — count is flat, the timestamp advanced")

	// No new evidence → still 2 (timestamp unchanged, no re-record).
	lc.evaluateAndMaybeRollback(ctx)
	assert.Len(t, asm.RollbackPolicy().scoreHistory, 2,
		"unchanged window must not re-record")
}

// seedWindowFitness writes n strategy-source fitness records with distinct,
// monotonically advancing timestamps (the evidence store orders and trims by
// timestamp, so distinct times are required for eviction semantics).
func seedWindowFitness(t *testing.T, store evidence.Store, strategyID string, value float64, n int) {
	t.Helper()
	ctx := context.Background()
	base := time.Now().Add(-time.Duration(n+1) * time.Second)
	for i := 0; i < n; i++ {
		payload, err := json.Marshal(map[string]any{
			"value": value, "strategy_id": strategyID,
		})
		require.NoError(t, err)
		require.NoError(t, store.Append(ctx, evidence.Evidence{
			ID:        fmt.Sprintf("fit-%s-%d-%d", strategyID, int(value*100), i),
			Source:    "strategy",
			Kind:      evidence.KindFitness,
			Payload:   payload,
			Timestamp: base.Add(time.Duration(i) * time.Second),
		}))
	}
}

func TestStrategyLifecycle_StartStop(t *testing.T) {
	cfg := DefaultLifecycleConfig()
	lc, _, _ := newTestLifecycle(t, cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	lc.Start(ctx)
	// Start is idempotent.
	lc.Start(ctx)

	// Stop is safe.
	lc.Stop()
	// Stop is idempotent.
	lc.Stop()
}

func TestStrategyLifecycle_Disabled(t *testing.T) {
	cfg := DefaultLifecycleConfig()
	cfg.Enabled = false
	lc, asm, _ := newTestLifecycle(t, cfg)
	require.NoError(t, asm.Deploy(context.Background(),
		&mutation.Strategy{ID: "base", Version: 1, Score: 50.0},
	))

	candidate := &mutation.Strategy{ID: "cand", Version: 2, Score: 80.0}
	// Submit must be a no-op when disabled.
	lc.Submit(context.Background(), candidate, 1)
	assert.Equal(t, "base", asm.Current().ID)

	// Start must be a no-op when disabled.
	lc.Start(context.Background())
}

// --- clamp01 tests ---

func TestClamp01(t *testing.T) {
	assert.Equal(t, 0.0, clamp01(-1))
	assert.Equal(t, 0.0, clamp01(0))
	assert.Equal(t, 0.5, clamp01(0.5))
	assert.Equal(t, 1.0, clamp01(1))
	assert.Equal(t, 1.0, clamp01(2))
}

// --- mockGate ---

type mockGate struct {
	name   string
	pass   bool
	score  float64
	reason string
}

func (g *mockGate) Name() string { return g.name }
func (g *mockGate) Check(_ context.Context, _, _ *mutation.Strategy) (bool, float64, string) {
	return g.pass, g.score, g.reason
}

// --- E2: open promote path + promote throttle + rollback reachability ---

// advanceResidency backs the activeSince clock up far enough that the
// MinActiveDuration throttle is satisfied for the next Submit. It simulates
// wall-clock time passing between generations without spawning a real watch
// ticker (the test drives evaluateAndMaybeRollback directly).
func advanceResidency(t *testing.T, lc *StrategyLifecycle) {
	t.Helper()
	lc.mu.Lock()
	lc.activeSince = time.Now().Add(-24 * time.Hour)
	lc.mu.Unlock()
}

// TestStrategyLifecycle_Submit_ProgressivePromotion_E2 is the main closed-loop
// acceptance for E2: in a no-scorer config (no shadow gate registered, no
// gates wired) TWO candidates are now promoted in sequence, and asm.Previous()
// points at the FIRST — i.e. a real second promote happens, not just the seed
// exemption. Before E2 this second candidate was rejected forever by the
// fail-closed G2 gate.
func TestStrategyLifecycle_Submit_ProgressivePromotion_E2(t *testing.T) {
	cfg := DefaultLifecycleConfig()
	cfg.RollbackArmed = true
	lc, asm, _ := newTestLifecycle(t, cfg)
	require.NoError(t, asm.Deploy(context.Background(),
		&mutation.Strategy{ID: "seed", Version: 1, Score: 50.0},
	))

	// First candidate: a gated promote (startResidency true) — the §9 baseline
	// was already deployed via asm.Deploy, so this is NOT the seed exemption.
	lc.Submit(context.Background(), &mutation.Strategy{ID: "cand-1", Version: 2, Score: 75.0}, 1)
	assert.Equal(t, "cand-1", asm.Current().ID, "first candidate must promote")
	require.NotNil(t, asm.Previous())
	assert.Equal(t, "seed", asm.Previous().ID)

	// Satisfy the promote throttle so the second candidate is judged.
	advanceResidency(t, lc)

	// Second candidate: THIS is the case that was rejected before E2.
	lc.Submit(context.Background(), &mutation.Strategy{ID: "cand-2", Version: 3, Score: 88.0}, 2)
	assert.Equal(t, "cand-2", asm.Current().ID, "second candidate must promote (E2 regression)")
	assert.Equal(t, "cand-1", asm.Previous().ID,
		"a real second promote must establish previous=cand-1")

	// Snapshot posture: two promotions, no shadow gate skipped.
	snap := lc.Snapshot()
	assert.Equal(t, "promoted", snap.LastDecision)
	assert.Empty(t, snap.ShadowGateSkipReason)
}

// TestStrategyLifecycle_Submit_ResidencyThrottle_E2 locks the promote throttle:
// a candidate submitted BEFORE MinActiveDuration elapses is rejected and the
// active strategy is unchanged; only after residency does the next candidate go
// through. This is the correctness precondition of the open promote path — it
// is what stops the GA ticker from rotating strategies faster than the rollback
// window can accumulate evidence.
func TestStrategyLifecycle_Submit_ResidencyThrottle_E2(t *testing.T) {
	cfg := DefaultLifecycleConfig()
	cfg.RollbackArmed = true
	lc, asm, _ := newTestLifecycle(t, cfg)
	require.NoError(t, asm.Deploy(context.Background(),
		&mutation.Strategy{ID: "seed", Version: 1, Score: 50.0},
	))

	lc.Submit(context.Background(), &mutation.Strategy{ID: "first", Version: 2, Score: 75.0}, 1)
	assert.Equal(t, "first", asm.Current().ID)

	// Immediately submit a better candidate: residency not yet elapsed.
	lc.Submit(context.Background(), &mutation.Strategy{ID: "too-soon", Version: 3, Score: 95.0}, 2)
	assert.Equal(t, "first", asm.Current().ID,
		"promote throttle must reject a candidate before MinActiveDuration elapses")
	assert.Equal(t, "seed", asm.Previous().ID,
		"rejected candidate must not corrupt the previous chain")

	// After advancing the residency clock the same candidate goes through.
	advanceResidency(t, lc)
	lc.Submit(context.Background(), &mutation.Strategy{ID: "too-soon", Version: 3, Score: 95.0}, 2)
	assert.Equal(t, "too-soon", asm.Current().ID, "once residency elapses the candidate promotes")
}

// TestStrategyLifecycle_RollbackReachable_E2 is the single most valuable
// assertion in the whole E2 plan: it proves the POST-deployment machinery
// (RuntimeObserver → Window → RecordScore → Rollback → restore-previous) runs
// for real. Before E2, the fail-closed G2 gate meant only the seed ever
// promoted, so asm.Previous() stayed nil and automatic rollback was
// permanently unreachable. Here we build a two-promote chain then feed a
// sustained degradation so the watch loop restores the previous strategy.
func TestStrategyLifecycle_RollbackReachable_E2(t *testing.T) {
	cfg := DefaultLifecycleConfig()
	cfg.RollbackArmed = true // the net must be armed for the watch loop to act
	lc, asm, store := newTestLifecycle(t, cfg)
	require.NoError(t, asm.Deploy(context.Background(),
		&mutation.Strategy{ID: "seed", Version: 1, Score: 50.0},
	))

	// Build a two-promote chain so previous is non-nil (the precondition the
	// fail-closed config could never satisfy).
	lc.Submit(context.Background(), &mutation.Strategy{ID: "gen-1", Version: 2, Score: 70.0}, 1)
	advanceResidency(t, lc)
	lc.Submit(context.Background(), &mutation.Strategy{ID: "gen-2", Version: 3, Score: 85.0}, 2)
	assert.Equal(t, "gen-2", asm.Current().ID)
	assert.Equal(t, "gen-1", asm.Previous().ID)

	// Seed a high baseline for gen-2, then drive sustained degradation. Each
	// batch advances the evidence timestamp so the decorrelation guard records
	// once per tick.
	ctx := context.Background()
	seedWindowFitness(t, store, "gen-2", 1.0, 12)
	lc.evaluateAndMaybeRollback(ctx)
	require.Len(t, asm.RollbackPolicy().scoreHistory, 1, "baseline recorded once")

	seedWindowFitness(t, store, "gen-2", 0.0, 12)
	lc.evaluateAndMaybeRollback(ctx)
	seedWindowFitness(t, store, "gen-2", 0.0, 12)
	lc.evaluateAndMaybeRollback(ctx)

	// 1.0 then 0.0, 0.0 → degradation 1.0 > 0.15 and history >= minSamples.
	assert.Equal(t, "gen-1", asm.Current().ID,
		"rollback must restore the previous strategy on sustained degradation")
	assert.Equal(t, "gen-2", asm.Previous().ID,
		"after rollback the degraded strategy becomes previous (re-rollback possible)")
}

// TestStrategyLifecycle_Snapshot_GateVisibility_E2 locks the E2 snapshot
// contract: the lifecycle must report the gate pipeline and, when the wiring
// layer folds the G2 gate (WithShadowGateDisabled), surface the skip reason so
// the absence is visible to an operator instead of emergent.
func TestStrategyLifecycle_Snapshot_GateVisibility_E2(t *testing.T) {
	cfg := DefaultLifecycleConfig()
	cfg.RollbackArmed = true
	asm, err := NewActiveStrategyManager(newMockStrategyStore(), NewRollbackPolicy())
	require.NoError(t, err)
	store := evidence.NewMemoryStore()
	agg := NewRuntimeFitnessAggregator(store, DefaultAggregatorConfig())

	lc := NewStrategyLifecycle(asm, agg, cfg,
		WithLifecycleEvidenceStore(store),
		WithShadowGateDisabled("no scorer; canary+rollback armed"),
		WithLifecycleGates(&mockGate{name: "eval", pass: true, reason: "ok"}),
	)
	require.NoError(t, asm.Deploy(context.Background(),
		&mutation.Strategy{ID: "seed", Version: 1, Score: 50.0},
	))

	snap := lc.Snapshot()
	// The explicit gate is registered; the shadow gate is NOT (folded by the
	// wiring decision). gateNamesLocked returns registered gate names only.
	assert.ElementsMatch(t, []string{"eval"}, snap.Gates,
		"shadow gate must NOT appear in gates when disabled via WithShadowGateDisabled")
	assert.Equal(t, "no scorer; canary+rollback armed", snap.ShadowGateSkipReason)
	assert.True(t, snap.RollbackArmed)
	assert.Greater(t, snap.MinActiveDuration, time.Duration(0),
		"promote-throttle posture must be visible in the snapshot")

	m := lc.LifecycleSnapshot()
	assert.Equal(t, "no scorer; canary+rollback armed", m["shadow_gate_skipped_reason"])
	assert.Equal(t, []string{"eval"}, m["gates"])
	assert.Equal(t, true, m["rollback_armed"])
}

// TestStrategyLifecycle_ThrottleBoundedChurn_E2 is the jitter/oscillation
// regression for E2: the GA ticker nominates a strictly better candidate on
// every generation, but the promote throttle couples promote frequency to
// MinActiveDuration. Without it the loop would rotate strategies every tick —
// faster than the rollback window accumulates evidence, making degradation
// undetectable in principle. Here, five consecutive better candidates within
// one residency window must NOT rotate the active strategy; only one promote is
// permitted, and a fresh promote restarts the residency clock.
func TestStrategyLifecycle_ThrottleBoundedChurn_E2(t *testing.T) {
	cfg := DefaultLifecycleConfig()
	cfg.RollbackArmed = true
	lc, asm, _ := newTestLifecycle(t, cfg)
	require.NoError(t, asm.Deploy(context.Background(),
		&mutation.Strategy{ID: "seed", Version: 1, Score: 50.0},
	))

	// Generation 1: the first judged promote (establishes the residency clock).
	lc.Submit(context.Background(), &mutation.Strategy{ID: "gen-1", Version: 2, Score: 70.0}, 1)
	assert.Equal(t, "gen-1", asm.Current().ID)

	// Generations 2..6: each tick nominates a strictly better candidate while
	// residency is still elapsing. None may replace gen-1 — the throttle must
	// bound promote churn to one promotion per residency window.
	for gen := 2; gen <= 6; gen++ {
		lc.Submit(context.Background(), &mutation.Strategy{
			ID:      fmt.Sprintf("gen-%d", gen),
			Version: gen + 1,
			Score:   float64(70 + gen),
		}, gen)
		assert.Equal(t, "gen-1", asm.Current().ID,
			"promote churn at generation %d must be throttled by MinActiveDuration", gen)
	}

	// Once residency elapses, one better candidate is allowed through.
	advanceResidency(t, lc)
	lc.Submit(context.Background(), &mutation.Strategy{ID: "gen-7", Version: 8, Score: 95.0}, 7)
	assert.Equal(t, "gen-7", asm.Current().ID, "after residency elapses one promote is permitted")

	// And a fresh promote restarts the residency clock: an immediate follow-up
	// in the same tick is throttled again.
	lc.Submit(context.Background(), &mutation.Strategy{ID: "gen-8", Version: 9, Score: 99.0}, 8)
	assert.Equal(t, "gen-7", asm.Current().ID, "a fresh promote must restart the residency clock")
}
