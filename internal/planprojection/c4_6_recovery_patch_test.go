package planprojection

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
	"github.com/Timwood0x10/ares/internal/taskfabric"
	"github.com/Timwood0x10/ares/internal/workflow/engine"
)

// TestC4_6_RecoveryPatchStrategy_VisibleInDAGSnapshot_NotInPlanStep verifies
// C4.6: PatchChangeRecoveryStrategy modifies Step.RecoveryPolicy on the live
// MutableDAG. The evidence of this change is visible in the
// RecoveryPatchExecutor's DAG snapshot (SnapshotWithSteps), NOT in the
// projected PlanStep — because PlanStep deliberately does not carry a
// RecoveryPolicy field (C1.1 design: recovery is handled by
// RecoveryPatchExecutor, not by the kernel's plan layer).
//
// This test asserts the separation of concerns:
//   - Structural patches (AddNode/RemoveNode/AddEdge) → visible in PlanStep.
//   - Recovery patches (ChangeRecoveryStrategy) → visible in DAG Step snapshot,
//     NOT in PlanStep.
//
// The test also verifies the round-trip: after applying the patch, the
// RecoveryPatchExecutor.Snapshot() returns the live DAG whose Steps()
// carry the updated RecoveryPolicy.
func TestC4_6_RecoveryPatchStrategy_VisibleInDAGSnapshot_NotInPlanStep(t *testing.T) {
	// Build a DAG with heterogeneous recovery policies.
	steps := []*engine.Step{
		{
			ID:        "a",
			AgentType: "x",
			RecoveryPolicy: &engine.RecoveryPolicy{
				Strategy:    engine.RecoveryRetry,
				MaxAttempts: 2,
				Backoff:     100 * time.Millisecond,
			},
		},
		{
			ID:        "b",
			AgentType: "y",
			DependsOn: []string{"a"},
			// No RecoveryPolicy on purpose.
		},
	}
	dag, err := engine.NewMutableDAG(steps)
	require.NoError(t, err)

	// Create the RecoveryPatchExecutor wrapping the live DAG.
	recExec := engine.NewRecoveryPatchExecutor(dag)
	require.NotNil(t, recExec)

	// Project the steps to PlanSteps — this is what the kernel sees.
	planSteps := ProjectSteps(dag.Steps())
	require.Len(t, planSteps, 2)

	// C4.6 assertion part 1: PlanStep does NOT carry RecoveryPolicy.
	// The projection deliberately discards RecoveryPolicy (C1.1).
	for _, ps := range planSteps {
		// PlanStep has no RecoveryPolicy field; verify the projection
		// does not smuggle it in via Payload.
		_, hasRecoveryInPayload := ps.Payload["recovery_policy"]
		assert.False(t, hasRecoveryInPayload,
			"PlanStep Payload must not carry recovery_policy (projected away by C1.1)")
	}

	// Apply PatchChangeRecoveryStrategy via the RecoveryPatchExecutor.
	// This changes every step's RecoveryPolicy.Strategy to ReplaceNode.
	rollback, err := recExec.Apply(context.Background(), patch.RuntimePatch{
		Type:   patch.PatchChangeRecoveryStrategy,
		Target: "recovery",
		Value:  string(engine.RecoveryReplaceNode),
	})
	require.NoError(t, err)
	require.NotNil(t, rollback)

	// C4.6 assertion part 2: the recovery patch's effect IS visible in the
	// RecoveryPatchExecutor's DAG snapshot. We read Steps() from the live DAG
	// (which is what Snapshot() returns a reference to).
	snap, err := recExec.Snapshot(context.Background())
	require.NoError(t, err)

	// The snapshot returns the live *MutableDAG.
	liveDAG, ok := snap.(*engine.MutableDAG)
	require.True(t, ok, "Snapshot must return *MutableDAG")
	require.NotNil(t, liveDAG)

	// Every step in the DAG snapshot now carries ReplaceNode strategy.
	for _, step := range liveDAG.Steps() {
		require.NotNil(t, step.RecoveryPolicy,
			"step %s must have a RecoveryPolicy after ChangeRecoveryStrategy patch", step.ID)
		assert.Equal(t, engine.RecoveryReplaceNode, step.RecoveryPolicy.Strategy,
			"step %s strategy must be ReplaceNode", step.ID)
	}

	// C4.6 assertion part 3: re-project the updated DAG into PlanSteps.
	// The PlanSteps should still NOT carry recovery info — the projection
	// gap is by design, not a bug. This is the separation-of-contracts test:
	// recovery patches are verified via the DAG snapshot, structural patches
	// via PlanStep.
	updatedPlanSteps := ProjectSteps(liveDAG.Steps())
	for _, ps := range updatedPlanSteps {
		_, hasRecoveryInPayload := ps.Payload["recovery_policy"]
		assert.False(t, hasRecoveryInPayload,
			"Updated PlanStep must still not carry recovery_policy")
	}

	// C4.6 assertion part 4: rollback restores the prior state.
	_, err = recExec.Apply(context.Background(), *rollback)
	require.NoError(t, err)

	restored := make(map[string]*engine.RecoveryPolicy)
	for _, s := range dag.Steps() {
		restored[s.ID] = s.RecoveryPolicy
	}
	require.NotNil(t, restored["a"])
	assert.Equal(t, engine.RecoveryRetry, restored["a"].Strategy,
		"step a strategy must be restored to Retry")
	assert.Nil(t, restored["b"],
		"step b policy must be removed on rollback (it had none before)")
}

// TestC4_6_RecoveryPatchMaxRetries_VisibleInDAGSnapshot verifies that
// PatchChangeMaxRetries is also visible in the DAG snapshot, not in PlanStep.
// PlanStep carries MaxRetries from Step.RetryPolicy.MaxAttempts, which is a
// DIFFERENT field from RecoveryPolicy.MaxAttempts. The two must not be confused.
func TestC4_6_RecoveryPatchMaxRetries_VisibleInDAGSnapshot(t *testing.T) {
	steps := []*engine.Step{
		{
			ID:        "a",
			AgentType: "x",
			RecoveryPolicy: &engine.RecoveryPolicy{
				Strategy:    engine.RecoveryRetry,
				MaxAttempts: 2,
			},
			// RetryPolicy is what PlanStep.MaxRetries maps from.
			// It is separate from RecoveryPolicy.MaxAttempts.
		},
	}
	dag, err := engine.NewMutableDAG(steps)
	require.NoError(t, err)

	recExec := engine.NewRecoveryPatchExecutor(dag)

	// Apply PatchChangeMaxRetries (recovery max retries, NOT plan max retries).
	_, err = recExec.Apply(context.Background(), patch.RuntimePatch{
		Type:   patch.PatchChangeMaxRetries,
		Target: "recovery",
		Value:  7,
	})
	require.NoError(t, err)

	// Verify the change is visible in the DAG snapshot.
	snap, err := recExec.Snapshot(context.Background())
	require.NoError(t, err)
	liveDAG, ok := snap.(*engine.MutableDAG)
	require.True(t, ok)

	for _, step := range liveDAG.Steps() {
		require.NotNil(t, step.RecoveryPolicy)
		assert.Equal(t, 7, step.RecoveryPolicy.MaxAttempts,
			"RecoveryPolicy.MaxAttempts must be 7 after patch")
	}

	// Verify PlanStep.MaxRetries is UNCHANGED — it maps from
	// Step.RetryPolicy.MaxAttempts, not RecoveryPolicy.MaxAttempts.
	// Since RetryPolicy is nil, PlanStep.MaxRetries should be 0.
	planSteps := ProjectSteps(liveDAG.Steps())
	for _, ps := range planSteps {
		assert.Equal(t, 0, ps.MaxRetries,
			"PlanStep.MaxRetries maps from RetryPolicy, not RecoveryPolicy — must be 0")
	}
}

// TestC4_6_RecoveryPatchAndStructuralPatch_Independent verifies that a
// recovery patch and a structural patch can be applied independently and
// their effects are visible in their respective projection surfaces:
//   - Structural patch → PlanStep count changes.
//   - Recovery patch → DAG Step.RecoveryPolicy changes, PlanStep unchanged.
//
// This is the C4.6 separation contract: the two patch categories operate on
// different fields and are verified through different assertion surfaces.
func TestC4_6_RecoveryPatchAndStructuralPatch_Independent(t *testing.T) {
	dag, err := engine.NewMutableDAG([]*engine.Step{
		{ID: "a", AgentType: "x"},
		{ID: "b", AgentType: "y", DependsOn: []string{"a"}},
	})
	require.NoError(t, err)

	fabric := taskfabric.NewFabric()
	coord := NewCompileCoordinator(fabric, nil)

	// Initial compile: 2 steps.
	rec, err := coord.CompileDAG(context.Background(), dag)
	require.NoError(t, err)
	assert.Equal(t, 2, rec.StepCount)

	recExec := engine.NewRecoveryPatchExecutor(dag)

	// Apply a recovery patch — does NOT change step count.
	_, err = recExec.Apply(context.Background(), patch.RuntimePatch{
		Type:   patch.PatchChangeRecoveryStrategy,
		Target: "recovery",
		Value:  string(engine.RecoveryFailFast),
	})
	require.NoError(t, err)

	// Re-compile: step count unchanged (recovery patch is not structural).
	rec2, err := coord.CompileDAG(context.Background(), dag)
	require.NoError(t, err)
	assert.Equal(t, 2, rec2.StepCount,
		"recovery patch must not change step count")

	// Verify recovery patch is visible in DAG Steps.
	for _, step := range dag.Steps() {
		require.NotNil(t, step.RecoveryPolicy)
		assert.Equal(t, engine.RecoveryFailFast, step.RecoveryPolicy.Strategy)
	}

	// Now apply a structural patch — DOES change step count.
	ctx := context.Background()
	require.NoError(t, dag.AddNode(ctx, &engine.Step{
		ID:        "c",
		AgentType: "z",
		DependsOn: []string{"b"},
	}))

	// Re-compile: step count is now 3.
	rec3, err := coord.CompileDAG(ctx, dag)
	require.NoError(t, err)
	assert.Equal(t, 3, rec3.StepCount,
		"structural patch (AddNode) must change step count to 3")

	// The new step "c" does NOT inherit the recovery patch (it was added
	// after the recovery patch was applied). Verify step c has no policy.
	for _, step := range dag.Steps() {
		if step.ID == "c" {
			assert.Nil(t, step.RecoveryPolicy,
				"step c was added after the recovery patch and must not have a policy")
		}
	}
}
