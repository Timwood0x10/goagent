package coordinator

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

// ── PatchSource constants ───────────────────

func TestPatchSourceConstants(t *testing.T) {
	assert.Equal(t, PatchSource("genome"), SourceGA)
	assert.Equal(t, PatchSource("chaos"), SourceChaos)
	assert.Equal(t, PatchSource("akf"), SourceAKF)
	assert.Equal(t, PatchSource("human"), SourceHuman)
	assert.Equal(t, PatchSource("llm"), SourceLLM)
	assert.Equal(t, PatchSource("k8s"), SourceK8s)
	assert.Equal(t, PatchSource("rule"), SourceRule)
}

// ── Decision ────────────────────────────────

func TestDecision_String(t *testing.T) {
	assert.Equal(t, "apply", DecisionApply.String())
	assert.Equal(t, "reject", DecisionReject.String())
	assert.Equal(t, "delay", DecisionDelay.String())
	assert.Equal(t, "drop", DecisionDrop.String())
}

// ── Policy ──────────────────────────────────

func TestDefaultPolicy(t *testing.T) {
	p := DefaultPolicy()
	assert.Equal(t, 8, p.AutoApplyThreshold)
	assert.Equal(t, 4, p.MaxPatchesPerMinute)
	assert.Equal(t, 30.0, p.MinFitnessThreshold)
	// Post-calibration (fitness pipeline now evidence-backed): 70 enables
	// GA auto-apply for high-fitness patches instead of 100 which disabled.
	assert.Equal(t, 70.0, p.ApplyFitnessThreshold)
}

// ── EvolutionCoordinator ────────────────────

func TestNewEvolutionCoordinator(t *testing.T) {
	patchReg := patch.NewRegistry()
	coord := NewEvolutionCoordinator(DefaultPolicy(), patchReg)
	require.NotNil(t, coord)
	assert.Equal(t, 0, coord.PendingCount())
}

func TestCoordinator_Submit(t *testing.T) {
	patchReg := patch.NewRegistry()
	coord := NewEvolutionCoordinator(DefaultPolicy(), patchReg)

	coord.Submit(PatchProposal{
		Patch:     patch.RuntimePatch{Type: patch.PatchInsertNode, Target: "test"},
		Source:    SourceGA,
		Reason:    "test",
		Priority:  5,
		Timestamp: time.Now(),
	})
	assert.Equal(t, 1, coord.PendingCount())
}

func TestCoordinator_Submit_Multiple(t *testing.T) {
	patchReg := patch.NewRegistry()
	coord := NewEvolutionCoordinator(DefaultPolicy(), patchReg)

	for i := 0; i < 5; i++ {
		coord.Submit(PatchProposal{
			Patch:    patch.RuntimePatch{Type: patch.PatchInsertNode, Target: "test"},
			Source:   SourceGA,
			Reason:   "test",
			Priority: i,
		})
	}
	assert.Equal(t, 5, coord.PendingCount())
}

// ── Evaluation ──────────────────────────────

func TestCoordinator_Evaluate_AppliesPatches(t *testing.T) {
	patchReg := patch.NewRegistry()
	exec := &recordingExecutor{}
	require.NoError(t, patchReg.Register("test-target", exec))

	coord := NewEvolutionCoordinator(DefaultPolicy(), patchReg)
	coord.Submit(PatchProposal{
		Patch:    patch.RuntimePatch{Type: patch.PatchInsertNode, Target: "test-target"},
		Source:   SourceGA,
		Reason:   "test",
		Priority: 8, // >= AutoApplyThreshold(8) so it gets applied
	})

	coord.Evaluate(context.Background())
	assert.Equal(t, 0, coord.PendingCount())
	assert.Len(t, exec.applied, 1)
}

func TestCoordinator_Evaluate_AutoApplyHighPriority(t *testing.T) {
	patchReg := patch.NewRegistry()
	exec := &recordingExecutor{}
	require.NoError(t, patchReg.Register("urgent", exec))

	coord := NewEvolutionCoordinator(PolicyGenome{AutoApplyThreshold: 8, MaxPatchesPerMinute: 100}, patchReg)

	// Priority 10 >= threshold 8 → auto-apply.
	coord.Submit(PatchProposal{
		Patch:    patch.RuntimePatch{Type: patch.PatchInsertNode, Target: "urgent"},
		Source:   SourceChaos,
		Priority: 10,
	})

	coord.Evaluate(context.Background())
	assert.Len(t, exec.applied, 1)
}

func TestCoordinator_Evaluate_DelaysOnRateLimit(t *testing.T) {
	patchReg := patch.NewRegistry()
	exec := &recordingExecutor{}
	require.NoError(t, patchReg.Register("rate-test", exec))

	coord := NewEvolutionCoordinator(PolicyGenome{MaxPatchesPerMinute: 0}, patchReg)

	coord.Submit(PatchProposal{
		Patch:    patch.RuntimePatch{Type: patch.PatchInsertNode, Target: "rate-test"},
		Priority: 1,
	})

	coord.Evaluate(context.Background())
	decisions := coord.DecisionHistory()
	require.Len(t, decisions, 1)
	assert.Equal(t, DecisionDelay, decisions[0].Decision,
		"should delay when rate limit is 0")
}

// TestCoordinator_Evaluate_DelayedProposalRequeued verifies that a delayed
// proposal is re-queued for later review rather than silently discarded, and
// that once the retry cap is reached the coordinator records an explicit
// DecisionDrop (instead of a silent disappear) so the discard is observable
// in DecisionHistory.
func TestCoordinator_Evaluate_DelayedProposalRequeued(t *testing.T) {
	patchReg := patch.NewRegistry()
	exec := &recordingExecutor{}
	require.NoError(t, patchReg.Register("delayed", exec))

	// GA patch with fitness in the delay band (30 < 50 < 70).
	coord := NewEvolutionCoordinator(DefaultPolicy(), patchReg)
	coord.Submit(PatchProposal{
		Patch:    patch.RuntimePatch{Type: patch.PatchInsertNode, Target: "delayed"},
		Source:   SourceGA,
		Priority: 5,
		Fitness:  50.0,
	})

	// First evaluation: delayed, re-queued (not applied, not dropped).
	coord.Evaluate(context.Background())
	assert.Equal(t, 1, coord.PendingCount(), "delayed proposal should be re-queued")
	assert.Len(t, exec.applied, 0, "delayed proposal must not be applied")
	require.Len(t, coord.DecisionHistory(), 1)
	assert.Equal(t, DecisionDelay, coord.DecisionHistory()[0].Decision)

	// Subsequent evaluations re-review it. When the retry budget is
	// exhausted, decide() returns DecisionDrop so the discard is observable
	// in DecisionHistory rather than a silent disappear.
	for i := 0; i < maxProposalRetries; i++ {
		coord.Evaluate(context.Background())
	}
	assert.Equal(t, 0, coord.PendingCount(), "proposal must be dropped after retry cap")
	assert.Len(t, exec.applied, 0, "delayed proposal must never be applied")

	decisions := coord.DecisionHistory()
	require.NotEmpty(t, decisions)
	last := decisions[len(decisions)-1]
	assert.Equal(t, DecisionDrop, last.Decision,
		"final decision for exhausted retries must be DecisionDrop (not silent Delay)")
	assert.Contains(t, last.Reason, "dropped",
		"drop reason must be observable in the decision reason string")
	assert.Contains(t, last.Reason, "retries",
		"drop reason must mention retry exhaustion")
}

// ── Fitness-gated evaluation ───────────────

func TestCoordinator_Evaluate_GA_FitnessAboveThreshold_Applies(t *testing.T) {
	patchReg := patch.NewRegistry()
	exec := &recordingExecutor{}
	require.NoError(t, patchReg.Register("ga-fit", exec))

	coord := NewEvolutionCoordinator(PolicyGenome{
		AutoApplyThreshold:    8,
		MaxPatchesPerMinute:   100,
		MinFitnessThreshold:   30.0,
		ApplyFitnessThreshold: 60.0,
	}, patchReg)

	// GA patch with fitness 80 >= 60 → apply.
	coord.Submit(PatchProposal{
		Patch:    patch.RuntimePatch{Type: patch.PatchInsertNode, Target: "ga-fit"},
		Source:   SourceGA,
		Priority: 5,
		Fitness:  80.0,
	})

	coord.Evaluate(context.Background())
	decisions := coord.DecisionHistory()
	require.Len(t, decisions, 1)
	assert.Equal(t, DecisionApply, decisions[0].Decision,
		"GA patch with fitness >= threshold should apply")
	assert.Len(t, exec.applied, 1)
}

func TestCoordinator_Evaluate_GA_FitnessBelowFloor_Rejects(t *testing.T) {
	patchReg := patch.NewRegistry()
	exec := &recordingExecutor{}
	require.NoError(t, patchReg.Register("ga-poor", exec))

	coord := NewEvolutionCoordinator(PolicyGenome{
		AutoApplyThreshold:    8,
		MaxPatchesPerMinute:   100,
		MinFitnessThreshold:   30.0,
		ApplyFitnessThreshold: 60.0,
	}, patchReg)

	// GA patch with fitness 20 < 30 → reject.
	coord.Submit(PatchProposal{
		Patch:    patch.RuntimePatch{Type: patch.PatchInsertNode, Target: "ga-poor"},
		Source:   SourceGA,
		Priority: 5,
		Fitness:  20.0,
	})

	coord.Evaluate(context.Background())
	decisions := coord.DecisionHistory()
	require.Len(t, decisions, 1)
	assert.Equal(t, DecisionReject, decisions[0].Decision,
		"GA patch with fitness < floor should reject")
	assert.Len(t, exec.applied, 0, "rejected patch should not be applied")
}

func TestCoordinator_Evaluate_GA_FitnessMiddleGround_Delays(t *testing.T) {
	patchReg := patch.NewRegistry()
	exec := &recordingExecutor{}
	require.NoError(t, patchReg.Register("ga-ok", exec))

	coord := NewEvolutionCoordinator(PolicyGenome{
		AutoApplyThreshold:    8,
		MaxPatchesPerMinute:   100,
		MinFitnessThreshold:   30.0,
		ApplyFitnessThreshold: 60.0,
	}, patchReg)

	// GA patch with fitness 45 between 30 and 60 → delay.
	coord.Submit(PatchProposal{
		Patch:    patch.RuntimePatch{Type: patch.PatchInsertNode, Target: "ga-ok"},
		Source:   SourceGA,
		Priority: 5,
		Fitness:  45.0,
	})

	coord.Evaluate(context.Background())
	decisions := coord.DecisionHistory()
	require.Len(t, decisions, 1)
	assert.Equal(t, DecisionDelay, decisions[0].Decision,
		"GA patch with fitness between threshold and floor should delay")
	assert.Len(t, exec.applied, 0, "delayed patch should not be applied")
}

func TestCoordinator_Evaluate_NonGA_FitnessZero_FallsBackToPriority(t *testing.T) {
	patchReg := patch.NewRegistry()
	exec := &recordingExecutor{}
	require.NoError(t, patchReg.Register("human", exec))

	coord := NewEvolutionCoordinator(PolicyGenome{
		AutoApplyThreshold:    8,
		MaxPatchesPerMinute:   100,
		MinFitnessThreshold:   30.0,
		ApplyFitnessThreshold: 60.0,
	}, patchReg)

	// Human source with Fitness=0 → should NOT be rejected by fitness gate.
	coord.Submit(PatchProposal{
		Patch:    patch.RuntimePatch{Type: patch.PatchInsertNode, Target: "human"},
		Source:   SourceHuman,
		Priority: 8, // >= AutoApplyThreshold(8) so priority fallback applies it
		Fitness:  0,
	})

	coord.Evaluate(context.Background())
	decisions := coord.DecisionHistory()
	require.Len(t, decisions, 1)
	assert.Equal(t, DecisionApply, decisions[0].Decision,
		"non-GA source with Fitness=0 should fall back to priority rules")
	assert.Len(t, exec.applied, 1)
}

func TestCoordinator_Evaluate_GA_FitnessZero_FallsBackToPriority(t *testing.T) {
	patchReg := patch.NewRegistry()
	exec := &recordingExecutor{}
	require.NoError(t, patchReg.Register("ga-zero", exec))

	coord := NewEvolutionCoordinator(PolicyGenome{
		AutoApplyThreshold:    8,
		MaxPatchesPerMinute:   100,
		MinFitnessThreshold:   30.0,
		ApplyFitnessThreshold: 60.0,
	}, patchReg)

	// GA source with Fitness=0 (unset) → should fall back to priority rules.
	coord.Submit(PatchProposal{
		Patch:    patch.RuntimePatch{Type: patch.PatchInsertNode, Target: "ga-zero"},
		Source:   SourceGA,
		Priority: 8, // above AutoApplyThreshold(8) so priority fallback applies it
		Fitness:  0,
	})

	coord.Evaluate(context.Background())
	decisions := coord.DecisionHistory()
	require.Len(t, decisions, 1)
	assert.Equal(t, DecisionApply, decisions[0].Decision,
		"GA patch with Fitness=0 should fall back to priority rules")
	assert.Len(t, exec.applied, 1)
}

func TestCoordinator_DecisionHistory(t *testing.T) {
	patchReg := patch.NewRegistry()
	coord := NewEvolutionCoordinator(DefaultPolicy(), patchReg)

	coord.Submit(PatchProposal{
		Patch:    patch.RuntimePatch{Type: patch.PatchInsertNode, Target: "t"},
		Source:   SourceGA,
		Priority: 5,
	})
	coord.Evaluate(context.Background())

	assert.Len(t, coord.DecisionHistory(), 1)
}

func TestCoordinator_PatchHistory(t *testing.T) {
	patchReg := patch.NewRegistry()
	exec := &recordingExecutor{}
	require.NoError(t, patchReg.Register("test", exec))

	coord := NewEvolutionCoordinator(DefaultPolicy(), patchReg)
	coord.Submit(PatchProposal{
		Patch:    patch.RuntimePatch{Type: patch.PatchInsertNode, Target: "test"},
		Priority: 5,
	})
	coord.Evaluate(context.Background())

	// Priority(5) < AutoApplyThreshold(8): patch is delayed, not applied.
	// This verifies the fix against the old fallthrough-to-apply behaviour.
	assert.Len(t, coord.PatchHistory(), 0)
}

// ── Mock executor ───────────────────────────

type recordingExecutor struct {
	applied []patch.RuntimePatch
}

func (e *recordingExecutor) Apply(_ context.Context, p patch.RuntimePatch) (*patch.RuntimePatch, error) {
	e.applied = append(e.applied, p)
	return &patch.RuntimePatch{Type: patch.PatchRemoveNode, Target: p.Target}, nil
}

func (e *recordingExecutor) CanApply(_ context.Context, _ patch.RuntimePatch) error { return nil }

// TestCoordinator_Policy exposes the live policy snapshot so the GA adapter
// can log the thresholds that produced a decision alongside the decision
// itself. The snapshot must reflect updates made after construction.
func TestCoordinator_Policy(t *testing.T) {
	patchReg := patch.NewRegistry()
	coord := NewEvolutionCoordinator(DefaultPolicy(), patchReg)
	p := coord.Policy()
	assert.Equal(t, 70.0, p.ApplyFitnessThreshold)
	assert.Equal(t, 30.0, p.MinFitnessThreshold)
}

// TestCoordinator_Evaluate_DefaultPolicyAppliesHighFitness verifies the
// post-calibration default (ApplyFitnessThreshold=70) actually applies a GA
// patch whose fitness is high enough — the loop is no longer gated off.
func TestCoordinator_Evaluate_DefaultPolicyAppliesHighFitness(t *testing.T) {
	patchReg := patch.NewRegistry()
	exec := &recordingExecutor{}
	require.NoError(t, patchReg.Register("high-fit", exec))

	coord := NewEvolutionCoordinator(DefaultPolicy(), patchReg)
	coord.Submit(PatchProposal{
		Patch:    patch.RuntimePatch{Type: patch.PatchInsertNode, Target: "high-fit"},
		Source:   SourceGA,
		Priority: 5,
		Fitness:  85.0, // >= 70 → apply under the calibrated default
	})

	coord.Evaluate(context.Background())
	decisions := coord.DecisionHistory()
	require.Len(t, decisions, 1)
	assert.Equal(t, DecisionApply, decisions[0].Decision,
		"fitness 85 >= calibrated default 70 must auto-apply (loop is closed)")
	assert.Len(t, exec.applied, 1)
	assert.NoError(t, decisions[0].ApplyError)
}

// ── Bug-scenario tests ──────────────────────
// These tests cover the three concrete bugs that the GA loop closure
// addresses; each is intentionally named after the bug it guards against so
// a regression is visible from the test name alone.

// Bug 1: decisionReason used to hardcode "30.0" in the reject reason string
// instead of using the configured MinFitnessThreshold. A policy with a
// non-default floor must surface that floor in the reason so an operator
// knows which gate rejected the patch.
func TestCoordinator_Bug_RejectReasonUsesPolicyMinFitness(t *testing.T) {
	patchReg := patch.NewRegistry()
	exec := &recordingExecutor{}
	require.NoError(t, patchReg.Register("ga-low", exec))

	// Non-default floor so a hardcoded "30" in the format string would lie.
	policy := PolicyGenome{
		AutoApplyThreshold:    8,
		MaxPatchesPerMinute:   100,
		MinFitnessThreshold:   25.0,
		ApplyFitnessThreshold: 70.0,
	}
	coord := NewEvolutionCoordinator(policy, patchReg)
	coord.Submit(PatchProposal{
		Patch:    patch.RuntimePatch{Type: patch.PatchInsertNode, Target: "ga-low"},
		Source:   SourceGA,
		Priority: 5,
		Fitness:  20.0, // below 25 floor → reject
	})

	coord.Evaluate(context.Background())
	decisions := coord.DecisionHistory()
	require.Len(t, decisions, 1)
	assert.Equal(t, DecisionReject, decisions[0].Decision)
	assert.Contains(t, decisions[0].Reason, "25",
		"reject reason must reflect the policy's MinFitnessThreshold (25), not a hardcoded 30")
	assert.NotContains(t, decisions[0].Reason, "30",
		"reject reason must not leak the old hardcoded 30 when the floor is 25")
}

// Bug 2: when the executor returned an error, ApplyError was previously
// recorded only in PatchHistory. Callers reading DecisionHistory could not
// see the apply failure, so a "decision to apply" that actually failed was
// indistinguishable from a successful apply. ApplyError must now be on the
// decision itself.
func TestCoordinator_Bug_ApplyErrorObservableOnDecision(t *testing.T) {
	patchReg := patch.NewRegistry()
	exec := &failingExecutor{}
	require.NoError(t, patchReg.Register("fails", exec))

	coord := NewEvolutionCoordinator(PolicyGenome{
		AutoApplyThreshold:    8,
		MaxPatchesPerMinute:   100,
		MinFitnessThreshold:   30.0,
		ApplyFitnessThreshold: 60.0,
	}, patchReg)
	coord.Submit(PatchProposal{
		Patch:    patch.RuntimePatch{Type: patch.PatchInsertNode, Target: "fails"},
		Source:   SourceGA,
		Priority: 5,
		Fitness:  80.0, // above threshold → apply, but executor fails
	})

	coord.Evaluate(context.Background())
	decisions := coord.DecisionHistory()
	require.Len(t, decisions, 1)
	assert.Equal(t, DecisionApply, decisions[0].Decision,
		"the decision itself is still Apply — the executor failure is separate")
	require.Error(t, decisions[0].ApplyError,
		"ApplyError must be surfaced on the decision so callers reading DecisionHistory see the failure")
	assert.Contains(t, decisions[0].ApplyError.Error(), "boom",
		"ApplyError must carry the executor's actual error message")

	// PatchHistory must still record the result too (no regression).
	history := coord.PatchHistory()
	require.Len(t, history, 1)
	assert.Error(t, history[0].Error)
}

// Bug 3: the silent-drop case. Before the closure, a delayed proposal that
// exhausted maxProposalRetries was recorded as DecisionDelay and then
// disappeared from the pending queue with no further signal. The decision
// history showed "Delay" even for the proposal that was actually discarded.
// DecisionDrop must now appear exactly once when the budget is exhausted,
// and the pending queue must stay empty afterwards (no resurrection).
func TestCoordinator_Bug_DropOnRetryExhaustionIsObservable(t *testing.T) {
	patchReg := patch.NewRegistry()
	exec := &recordingExecutor{}
	require.NoError(t, patchReg.Register("stuck", exec))

	coord := NewEvolutionCoordinator(PolicyGenome{
		AutoApplyThreshold:    8,
		MaxPatchesPerMinute:   100, // disable rate-limit so we exercise the fitness delay band
		MinFitnessThreshold:   30.0,
		ApplyFitnessThreshold: 70.0,
	}, patchReg)
	coord.Submit(PatchProposal{
		Patch:    patch.RuntimePatch{Type: patch.PatchInsertNode, Target: "stuck"},
		Source:   SourceGA,
		Priority: 5,
		Fitness:  50.0, // in [30, 70) → delay until budget exhausted
	})

	// Drive through the full retry budget: 1 initial delay, then 3 more
	// delays, then the 4th evaluation drops.
	totalEvaluations := maxProposalRetries + 1
	var drops int
	var lastDecision Decision
	for i := 0; i < totalEvaluations; i++ {
		coord.Evaluate(context.Background())
	}
	// After the budget is exhausted the pending queue must be empty and
	// must stay empty — a drop must not be re-queued.
	assert.Equal(t, 0, coord.PendingCount(),
		"pending queue must be empty after retry exhaustion (no resurrection)")
	// Evaluate once more — a dropped proposal must not reappear.
	coord.Evaluate(context.Background())
	assert.Equal(t, 0, coord.PendingCount(),
		"re-evaluating after a drop must not re-add the proposal")

	for _, d := range coord.DecisionHistory() {
		if d.Decision == DecisionDrop {
			drops++
		}
		lastDecision = d.Decision
	}
	assert.Equal(t, 1, drops,
		"exactly one DecisionDrop must be recorded (no duplicate drops for the same proposal)")
	assert.Equal(t, DecisionDrop, lastDecision,
		"the last decision must be the drop, not a Delay that silently disappeared")
	assert.Len(t, exec.applied, 0,
		"a dropped proposal must never have been applied")
}

// ── Additional mock executors ───────────────

// failingExecutor always returns an error from Apply. Used to verify that
// ApplyError is propagated onto the PatchDecision (not just PatchHistory).
type failingExecutor struct{}

func (e *failingExecutor) Apply(_ context.Context, _ patch.RuntimePatch) (*patch.RuntimePatch, error) {
	return nil, fmt.Errorf("executor boom: apply failed")
}

func (e *failingExecutor) CanApply(_ context.Context, _ patch.RuntimePatch) error { return nil }
