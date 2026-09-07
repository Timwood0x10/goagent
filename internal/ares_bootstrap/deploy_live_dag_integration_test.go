package ares_bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	aresruntime "github.com/Timwood0x10/ares/internal/ares_runtime"
	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/deployment"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
	"github.com/Timwood0x10/ares/internal/workflow/engine"
)

// TestDeployLiveDAG_ObservableTopologyChangeAndRollback is the 7.2.2
// "observable change" acceptance test.
//
// It wires the production seam end to end: a live DAG registered at
// ares_runtime.AgentDAGLiveKey, an engine.DAGPatchExecutor bound to that exact
// *MutableDAG installed as the patch registry's fallback, and a staging+live
// deployment pipeline sharing that registry. A workflow structure patch (insert
// a node) is pushed through Deploy, which must:
//
//  1. Judge the candidate on staged evidence (candidate beats active baseline),
//  2. Promote the patch to the live DAG,
//  3. Make runtime.GetAgentDAG(AgentDAGLiveKey) reflect the change — the
//     observable topology delta,
//  4. Let the returned RollbackPatch restore the original topology in place.
//
// Pre-closure the coordinator's structure patch never reached the live DAG (no
// executor/fallback for a dynamic node ID), so the live topology was silently
// unchanged — exactly the "no observable change" failure this test pins.
func TestDeployLiveDAG_ObservableTopologyChangeAndRollback(t *testing.T) {
	ctx := context.Background()

	// The single live DAG shared by the runtime manager, the genome and the
	// structure executor. The pointer MUST stay stable across deploy/rollback
	// so every observer (manager, WorkflowGenome, executors) sees the change.
	liveDAG := mustDAG(t, []*engine.Step{
		{ID: "live-a", Name: "Live A", AgentType: "test", Input: "in-a"},
		{ID: "live-b", Name: "Live B", AgentType: "test", Input: "in-b", DependsOn: []string{"live-a"}},
	})

	// The live runtime manager holds the DAG under AgentDAGLiveKey — the single
	// key the evolution system reads to apply topology patches.
	mgr := aresruntime.New(nil, nil, nil)
	mgr.RegisterAgentDAG(aresruntime.AgentDAGLiveKey, liveDAG)
	got, ok := mgr.GetAgentDAG(aresruntime.AgentDAGLiveKey)
	require.True(t, ok, "live DAG must be visible under AgentDAGLiveKey")
	require.Same(t, liveDAG, got, "the manager must expose the exact live DAG pointer")

	// Shared patch registry pre-wired with the structure fallback bound to the
	// live DAG. Staging and live deploy through the SAME registry, as in
	// production, so a rejected staging patch could never mutate live topology.
	reg := patch.NewRegistry()
	reg.SetFallback(engine.NewDAGPatchExecutor(liveDAG))
	require.True(t, reg.CanApply("live-c"), "a structure patch target must be eligible via the fallback")

	store := evidence.NewMemoryStore()
	staging := newStagingRuntimeWithASM(t, store, reg, "active")
	staging.coldStartScore = 0.5
	live := &deploymentLiveRuntime{reg: reg}

	// Per-strategy evidence: the candidate improves on the active baseline, so
	// the deployment judge promotes rather than rejects.
	seedStrategyEvidence(t, store, "active", 0.6, 10)
	seedStrategyEvidence(t, store, "candidate", 0.8, 10)

	dp := deployment.NewDeploymentPipeline(deployment.DeploymentConfig{
		Enabled:            true,
		PromotionThreshold: 0.05,
		RollbackThreshold:  0.10,
	}, staging, live)

	rec, err := dp.Deploy(ctx, patch.RuntimePatch{
		ID:         "deploy-live-c",
		Type:       patch.PatchInsertNode,
		Target:     "live-c",
		Value:      "live-c",
		Source:     "candidate",
		StrategyID: "candidate",
	})
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.Equal(t, deployment.DeploymentPromoted, rec.Status, "candidate must be promoted: %s", rec.Reason)
	require.NotNil(t, rec.RollbackPatch, "a promoted patch must carry a live rollback handle")

	// OBSERVABLE CHANGE: the live DAG under the key gained the inserted node.
	got, ok = mgr.GetAgentDAG(aresruntime.AgentDAGLiveKey)
	require.True(t, ok, "the live DAG must still be visible after promotion")
	liveAfter := got.(*engine.MutableDAG)
	require.Same(t, liveDAG, liveAfter, "the live DAG pointer must be stable across deploy")
	_, exists := liveAfter.StepIndex()["live-c"]
	assert.True(t, exists, "deploy must add the node to the live DAG topology")
	assert.Equal(t, 3, liveAfter.NodeCount(), "live DAG must grow by one node after promotion")

	// ROLLBACK restores the original topology in place (no re-registration).
	require.NoError(t, live.Rollback(ctx, rec.RollbackPatch))
	_, exists = liveAfter.StepIndex()["live-c"]
	assert.False(t, exists, "rollback must remove the inserted node from the live DAG topology")
	assert.Equal(t, 2, liveAfter.NodeCount(), "live DAG must return to its pre-deploy size after rollback")

	// Post-rollback the same manager key resolves to the restored graph.
	got, ok = mgr.GetAgentDAG(aresruntime.AgentDAGLiveKey)
	require.True(t, ok)
	assert.Same(t, liveDAG, got, "rollback must keep the same live DAG object registered")
}
