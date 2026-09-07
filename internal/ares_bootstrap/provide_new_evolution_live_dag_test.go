package ares_bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/fabric/task/workflow/engine"
	wfgraph "github.com/Timwood0x10/ares/internal/fabric/task/workflow/graph"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

// TestUpdateLiveDAG_DoesNotFailOnRegisteredExecutors verifies the fix for the
// "UpdateLiveDAG always failed" defect.
//
// Pre-fix, UpdateLiveDAG rebuilt a new GraphPatchExecutor and called
// RegisterComponent + Register("graph.scheduler", ...). Because bootstrap had
// already registered those same keys, both calls returned an error on every
// invocation, so serve.go's consistency update always logged a failure and the
// graph executor's DAG was never actually swapped to the live one.
//
// Post-fix, UpdateLiveDAG updates the already-registered executor in place via
// GraphPatchExecutor.SetGraph, mirroring RecoveryPatchExecutor.SetDAG. It must
// return nil (not error) and both executors must reference the live DAG.
func TestUpdateLiveDAG_DoesNotFailOnRegisteredExecutors(t *testing.T) {
	ctx := context.Background()

	// Simulate the bootstrap state: a patch registry with graph.scheduler and
	// recovery executors already registered.
	patchReg := patch.NewRegistry()
	graphExec := wfgraph.NewGraphPatchExecutor(mustGraph(t, "bootstrap-graph"))
	require.NoError(t, patchReg.RegisterComponent(graphExec))
	require.NoError(t, patchReg.Register("graph.scheduler", graphExec))
	recoveryExec := engine.NewRecoveryPatchExecutor(&engine.MutableDAG{})
	require.NoError(t, patchReg.RegisterComponent(recoveryExec))

	components := &NewEvolutionComponents{
		PatchReg:     patchReg,
		graphExec:    graphExec,
		recoveryExec: recoveryExec,
	}

	// Live DAG the manager would hold after agents are created.
	liveDAG := mustDAG(t, []*engine.Step{
		{ID: "live-a", Name: "Live A", AgentType: "test", Input: "in-a"},
		{ID: "live-b", Name: "Live B", AgentType: "test", Input: "in-b", DependsOn: []string{"live-a"}},
	})

	// The bug: this returned an error on every call because the executor keys
	// were already registered.
	err := components.UpdateLiveDAG(liveDAG)
	require.NoError(t, err, "UpdateLiveDAG must not fail when executors are already registered")

	// The graph executor's snapshot must now reflect the live DAG's steps.
	snapshot, err := graphExec.Snapshot(ctx)
	require.NoError(t, err)
	g, ok := snapshot.(*wfgraph.Graph)
	require.True(t, ok, "graph executor snapshot should be the live graph")
	require.NotNil(t, g, "graph executor should hold the live graph")
	nodeIDs := g.NodeIDs()
	assert.ElementsMatch(t, []string{"live-a", "live-b"}, nodeIDs,
		"graph executor should expose the live DAG's nodes")

	// The recovery executor must also hold the live DAG.
	recSnapshot, err := recoveryExec.Snapshot(ctx)
	require.NoError(t, err)
	assert.Same(t, liveDAG, recSnapshot, "recovery executor should reference the live DAG")
}

// TestUpdateLiveDAG_FallbackAppliesStructurePatch pins the closure that makes
// workflow structure patches observable on the live DAG.
//
// The WorkflowDiffer emits insert/remove/replace-node and add/remove-edge
// patches whose Target is a dynamic node ID (e.g. "live-c"), NOT a registered
// component key. Pre-fix the patch registry had no executor/fallback for such a
// target, so the coordinator's structure patch died on "no executor registered
// for target <nodeID>" and the live DAG was never mutated.
//
// Post-fix UpdateLiveDAG installs an engine.DAGPatchExecutor bound to the live
// DAG as the registry's fallback: a structure patch now reaches and mutates the
// real runtime topology instead of erroring.
func TestUpdateLiveDAG_FallbackAppliesStructurePatch(t *testing.T) {
	ctx := context.Background()
	patchReg := patch.NewRegistry()
	components := &NewEvolutionComponents{PatchReg: patchReg}

	liveDAG := mustDAG(t, []*engine.Step{
		{ID: "live-a", Name: "Live A", AgentType: "test", Input: "in-a"},
		{ID: "live-b", Name: "Live B", AgentType: "test", Input: "in-b", DependsOn: []string{"live-a"}},
	})
	require.NoError(t, components.UpdateLiveDAG(liveDAG))
	require.NotNil(t, components.dagExec, "UpdateLiveDAG must install the DAG structure executor")

	// Preflight gate (deployment staging runtime): a structure patch targeting
	// a dynamic node ID must now be accepted, not rejected.
	assert.True(t, patchReg.CanApply("live-c"),
		"structure patch target must be eligible now that a fallback exists")

	// The previously-failing apply: insert a node. Pre-fix this errored.
	err := patchReg.Apply(ctx, patch.RuntimePatch{Type: patch.PatchInsertNode, Target: "live-c", Value: "live-c"})
	require.NoError(t, err, "insert structure patch must apply to the live DAG via fallback")
	_, ok := liveDAG.StepIndex()["live-c"]
	assert.True(t, ok, "inserted node must be observable on the live DAG")

	// Add an edge live-b → live-c.
	err = patchReg.Apply(ctx, patch.RuntimePatch{Type: patch.PatchAddEdge, Target: "live-b", Value: "live-c"})
	require.NoError(t, err)
	assert.Contains(t, liveDAG.ReadDeps("live-c"), "live-b")

	// Remove the node (its reverse edge drops with it).
	err = patchReg.Apply(ctx, patch.RuntimePatch{Type: patch.PatchRemoveNode, Target: "live-c"})
	require.NoError(t, err)
	_, ok = liveDAG.StepIndex()["live-c"]
	assert.False(t, ok, "removed node must no longer be observable on the live DAG")
}

// TestUpdateLiveDAG_NilLiveDAG verifies the nil guard still rejects a nil DAG.
func TestUpdateLiveDAG_NilLiveDAG(t *testing.T) {
	components := &NewEvolutionComponents{}
	err := components.UpdateLiveDAG(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be nil")
}

// TestUpdateLiveDAG_RepointsWorkflowGenome pins the repair: UpdateLiveDAG
// must repoint the evolving WorkflowGenome at the live DAG, so mutations and diffs
// are computed against the topology patches will actually touch.
//
// Pre-fix the genome kept evolving the bootstrap placeholder DAG (input → process →
// output) while patches were applied to the real agent DAG. The diff engine then
// emitted patches referencing nodes that do not exist on the live graph — either a
// silent no-op or a spurious error, both misattributed as a bad patch.
func TestUpdateLiveDAG_RepointsWorkflowGenome(t *testing.T) {
	// Bootstrap registers the WorkflowGenome on a synthetic placeholder DAG.
	placeholder := mustDAG(t, []*engine.Step{
		{ID: "input", Name: "Input", AgentType: "input", Input: "in"},
		{ID: "process", Name: "Process", AgentType: "process", Input: "in", DependsOn: []string{"input"}},
		{ID: "output", Name: "Output", AgentType: "output", Input: "in", DependsOn: []string{"process"}},
	})
	comps, err := ProvideNewEvolution(placeholder, nil, nil, nil)
	require.NoError(t, err)

	// Live DAG the manager holds once agents are created.
	liveDAG := mustDAG(t, []*engine.Step{
		{ID: "live-a", Name: "Live A", AgentType: "test", Input: "in-a"},
		{ID: "live-b", Name: "Live B", AgentType: "test", Input: "in-b", DependsOn: []string{"live-a"}},
	})
	require.NoError(t, comps.UpdateLiveDAG(liveDAG))

	g, err := comps.GenomeReg.Get(genome.WorkflowGenomeName)
	require.NoError(t, err)
	wf, ok := g.(*genome.WorkflowGenome)
	require.True(t, ok, "workflow genome must be registered and type-assertable")
	assert.Same(t, liveDAG, wf.DAG(),
		"UpdateLiveDAG must repoint the WorkflowGenome at the live DAG, not the bootstrap placeholder")
}

func mustGraph(t *testing.T, name string) *wfgraph.Graph {
	t.Helper()
	g, err := wfgraph.NewGraph(name)
	require.NoError(t, err)
	return g
}

func mustDAG(t *testing.T, steps []*engine.Step) *engine.MutableDAG {
	t.Helper()
	dag, err := engine.NewMutableDAG(steps)
	require.NoError(t, err)
	return dag
}
