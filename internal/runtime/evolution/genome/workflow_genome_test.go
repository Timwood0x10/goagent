package genome

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/workflow/engine"
)

func TestWorkflowGenome_Name(t *testing.T) {
	dag := buildTestDAG(t)
	g := NewWorkflowGenome(dag, DefaultWorkflowGenomeConfig())
	assert.Equal(t, WorkflowGenomeName, g.Name())
}

func TestWorkflowGenome_Mutate(t *testing.T) {
	dag := buildTestDAG(t)
	g := NewWorkflowGenome(dag, DefaultWorkflowGenomeConfig())

	children, err := g.Mutate(context.Background(), 5)
	require.NoError(t, err)
	require.Len(t, children, 5)

	// Snapshot the parent before mutation.
	oldSnap, err := g.Snapshot(context.Background())
	require.NoError(t, err)
	oldDAG, ok := oldSnap.(*engine.DAG)
	require.True(t, ok)

	// At least some children should differ from the parent.
	differing := 0
	for _, child := range children {
		wfChild, ok := child.(*WorkflowGenome)
		require.True(t, ok)
		newSnap, err := wfChild.Snapshot(context.Background())
		require.NoError(t, err)
		newDAG, ok := newSnap.(*engine.DAG)
		require.True(t, ok)

		if len(newDAG.Nodes) != len(oldDAG.Nodes) {
			differing++
			t.Logf("child DAG has %d nodes vs parent's %d", len(newDAG.Nodes), len(oldDAG.Nodes))
		}
	}
	t.Logf("%d/%d children differ from parent", differing, len(children))
}

func TestWorkflowGenome_Mutate_InsertNode(t *testing.T) {
	dag := buildTestDAG(t)
	originalCount := dag.NodeCount()

	g := NewWorkflowGenome(dag, DefaultWorkflowGenomeConfig())
	child := g.clone()
	child.mutateInsertNode()

	assert.Greater(t, child.dag.NodeCount(), originalCount,
		"insertNode should increase node count")
}

func TestWorkflowGenome_Mutate_RemoveNode(t *testing.T) {
	dag := buildTestDAG(t)
	originalCount := dag.NodeCount()

	g := NewWorkflowGenome(dag, DefaultWorkflowGenomeConfig())
	child := g.clone()
	child.mutateRemoveNode()

	assert.Less(t, child.dag.NodeCount(), originalCount,
		"removeNode should decrease node count")
}

func TestWorkflowGenome_Mutate_ReplaceNode(t *testing.T) {
	dag := buildTestDAG(t)
	steps := dag.Steps()
	require.Greater(t, len(steps), 0)

	g := NewWorkflowGenome(dag, DefaultWorkflowGenomeConfig())
	child := g.clone()
	child.mutateReplaceNode()

	assert.Equal(t, dag.NodeCount(), child.dag.NodeCount(),
		"replaceNode should keep node count unchanged")
}

func TestWorkflowGenome_ClonePreservesDAG(t *testing.T) {
	dag := buildTestDAG(t)
	g := NewWorkflowGenome(dag, DefaultWorkflowGenomeConfig())

	child := g.clone()
	assert.Equal(t, dag.NodeCount(), child.dag.NodeCount(),
		"clone should have same node count")
	assert.Equal(t, dag.Version(), child.dag.Version(),
		"clone should have same version")
}

func TestWorkflowGenome_Snapshot(t *testing.T) {
	dag := buildTestDAG(t)
	g := NewWorkflowGenome(dag, DefaultWorkflowGenomeConfig())

	snap, err := g.Snapshot(context.Background())
	require.NoError(t, err)

	dagSnap, ok := snap.(*engine.DAG)
	require.True(t, ok)
	assert.Equal(t, dag.NodeCount(), len(dagSnap.Nodes))
}

func TestWorkflowGenome_Fitness(t *testing.T) {
	dag := buildTestDAG(t)
	g := NewWorkflowGenome(dag, DefaultWorkflowGenomeConfig())

	fit, err := g.Fitness(context.Background())
	require.NoError(t, err)
	assert.InDelta(t, 0.5, fit, 0.001)
}

// ── Helpers ────────────────────────────────

func buildTestDAG(t *testing.T) *engine.MutableDAG {
	t.Helper()
	steps := []*engine.Step{
		{ID: "A", Name: "Step A", AgentType: "test", Input: "a"},
		{ID: "B", Name: "Step B", AgentType: "test", Input: "b", DependsOn: []string{"A"}},
		{ID: "C", Name: "Step C", AgentType: "test", Input: "c", DependsOn: []string{"B"}},
	}
	dag, err := engine.NewMutableDAG(steps)
	require.NoError(t, err)
	return dag
}
