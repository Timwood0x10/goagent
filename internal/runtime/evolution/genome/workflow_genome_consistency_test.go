package genome

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/workflow/engine"
)

// assertDAGEdgeConsistency verifies the invariant the mutation operators must
// preserve: every step's DependsOn equals the authoritative edge map
// (m.dag.Edges via ReadDeps). This guards the "变异绕过 m.dag.Edges" bug class
// where an operator rewrites DependsOn directly and the change is silently
// lost in evolution snapshots.
func assertDAGEdgeConsistency(t *testing.T, g *WorkflowGenome) {
	t.Helper()
	for _, s := range g.dag.Steps() {
		deps := append([]string(nil), g.dag.ReadDeps(s.ID)...)
		got := append([]string(nil), s.DependsOn...)
		sort.Strings(deps)
		sort.Strings(got)
		require.ElementsMatch(t, got, deps, "step %s DependsOn diverged from dag edges", s.ID)
	}
}

// buildFanoutDAG builds A→B→C→D with a B→E branch, giving every operator a
// real topology to mutate (chain for serialize/split/merge, fan-out for
// parallelize, multiple nodes for swap).
func buildFanoutDAG(t *testing.T) *engine.MutableDAG {
	t.Helper()
	steps := []*engine.Step{
		{ID: "A", Name: "Step A", AgentType: "test", Input: "a"},
		{ID: "B", Name: "Step B", AgentType: "test", Input: "b", DependsOn: []string{"A"}},
		{ID: "C", Name: "Step C", AgentType: "test", Input: "c", DependsOn: []string{"B"}},
		{ID: "D", Name: "Step D", AgentType: "test", Input: "d", DependsOn: []string{"B", "C"}},
		{ID: "E", Name: "Step E", AgentType: "test", Input: "e", DependsOn: []string{"B"}},
	}
	dag, err := engine.NewMutableDAG(steps)
	require.NoError(t, err)
	return dag
}

// TestMutationKeepsEdgeMapConsistent runs every mutation operator repeatedly
// and asserts the edge map stays authoritative after each mutation.
func TestMutationKeepsEdgeMapConsistent(t *testing.T) {
	operators := []struct {
		name string
		run  func(g *WorkflowGenome)
	}{
		{"parallelize", func(g *WorkflowGenome) { g.mutateParallelize() }},
		{"serialize", func(g *WorkflowGenome) { g.mutateSerialize() }},
		{"swap", func(g *WorkflowGenome) { g.mutateSwapNodes() }},
		{"split", func(g *WorkflowGenome) { g.mutateSplitNode() }},
		{"merge", func(g *WorkflowGenome) { g.mutateMergeNodes() }},
	}
	for _, op := range operators {
		t.Run(op.name, func(t *testing.T) {
			for i := 0; i < 40; i++ {
				g := NewWorkflowGenome(buildFanoutDAG(t), DefaultWorkflowGenomeConfig())
				op.run(g)
				assertDAGEdgeConsistency(t, g)
			}
		})
	}
}

// TestMutationKeepsDAGExecutable verifies mutated DAGs stay acyclic and
// executable (a cycle or dead topology would break GetExecutionOrder).
func TestMutationKeepsDAGExecutable(t *testing.T) {
	for i := 0; i < 40; i++ {
		g := NewWorkflowGenome(buildFanoutDAG(t), DefaultWorkflowGenomeConfig())
		switch i % 5 {
		case 0:
			g.mutateParallelize()
		case 1:
			g.mutateSerialize()
		case 2:
			g.mutateSwapNodes()
		case 3:
			g.mutateSplitNode()
		case 4:
			g.mutateMergeNodes()
		}
		order, err := g.dag.GetExecutionOrder()
		require.NoError(t, err, "mutated DAG must stay acyclic")
		require.NotEmpty(t, order)
		assertDAGEdgeConsistency(t, g)
	}
}
