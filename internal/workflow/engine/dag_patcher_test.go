package engine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

// TestDAGPatchExecutor_AppliesLiveTopology verifies the structure executor
// mutates the live DAG for every structure patch type emitted by the
// WorkflowDiffer (InsertNode, AddEdge, RemoveNode).
func TestDAGPatchExecutor_AppliesLiveTopology(t *testing.T) {
	ctx := context.Background()
	dag := newTestMutableDAG(t, "a", "b")

	exec := NewDAGPatchExecutor(dag)

	// Insert a node (the differ emits Value as the node's StepID string).
	_, err := exec.Apply(ctx, patch.RuntimePatch{Type: patch.PatchInsertNode, Target: "c", Value: "c"})
	require.NoError(t, err)
	_, ok := dag.StepIndex()["c"]
	assert.True(t, ok, "insert must reach the live DAG")

	// Add an edge a → c.
	_, err = exec.Apply(ctx, patch.RuntimePatch{Type: patch.PatchAddEdge, Target: "a", Value: "c"})
	require.NoError(t, err)
	assert.Contains(t, dag.ReadDeps("c"), "a")

	// Remove node c (no dependents → edges drop with it).
	_, err = exec.Apply(ctx, patch.RuntimePatch{Type: patch.PatchRemoveNode, Target: "c"})
	require.NoError(t, err)
	_, ok = dag.StepIndex()["c"]
	assert.False(t, ok, "remove must reach the live DAG")
}

// TestDAGPatchExecutor_SnapshotRestore verifies the rollback primitive: a
// snapshot captured before mutation restores the live DAG to its pre-apply
// topology, keeping the *MutableDAG identity stable so the runtime manager,
// WorkflowGenome and the other executors all observe the reverted graph.
func TestDAGPatchExecutor_SnapshotRestore(t *testing.T) {
	ctx := context.Background()
	dag := newTestMutableDAG(t, "a", "b")

	exec := NewDAGPatchExecutor(dag)
	snap, err := exec.Snapshot(ctx)
	require.NoError(t, err)

	_, err = exec.Apply(ctx, patch.RuntimePatch{Type: patch.PatchInsertNode, Target: "c", Value: "c"})
	require.NoError(t, err)
	_, err = exec.Apply(ctx, patch.RuntimePatch{Type: patch.PatchAddEdge, Target: "a", Value: "c"})
	require.NoError(t, err)
	assert.Equal(t, 3, dag.NodeCount())

	err = exec.Restore(ctx, snap)
	require.NoError(t, err)
	assert.Equal(t, 2, dag.NodeCount(), "restore must revert the topology")
	assert.ElementsMatch(t, []string{"a", "b"}, dagStepIDs(dag), "restore must return to the captured steps")
	_, ok := dag.StepIndex()["c"]
	assert.False(t, ok, "restore must remove the inserted node")
}

func dagStepIDs(dag *MutableDAG) []string {
	ids := make([]string, 0, dag.NodeCount())
	for _, s := range dag.Steps() {
		ids = append(ids, s.ID)
	}
	return ids
}

func newTestMutableDAG(t *testing.T, ids ...string) *MutableDAG {
	t.Helper()
	steps := make([]*Step, 0, len(ids))
	for i, id := range ids {
		steps = append(steps, &Step{ID: id, Name: id, AgentType: "test"})
		if i > 0 {
			steps[len(steps)-1].DependsOn = []string{ids[i-1]}
		}
	}
	dag, err := NewMutableDAG(steps)
	require.NoError(t, err)
	return dag
}
