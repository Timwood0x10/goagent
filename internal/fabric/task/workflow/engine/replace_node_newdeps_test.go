package engine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReplaceNode_DifferentID_NewDepsBecomeEdges pins DEEP_CODE_REVIEW_2026
// CRITICAL 1.1: a different-ID ReplaceNode whose replacement step DECLARES
// dependencies the old step did not have must grow those edges. The
// regression: the different-ID branch only migrated existing edges, so the
// new DependsOn entries never became DAG edges — GetExecutionOrder
// under-counted prerequisites and the replacement could be scheduled before
// its declared dependencies.
func TestReplaceNode_DifferentID_NewDepsBecomeEdges(t *testing.T) {
	m, err := NewMutableDAG([]*Step{
		makeStep("plan"),
		makeStep("tool_a", "plan"),
		makeStep("tool_b", "plan"),
		makeStep("analyze", "tool_a", "tool_b"),
	})
	require.NoError(t, err)

	// tool_a depends only on plan; the replacement ALSO declares tool_b —
	// an edge that never existed before (old tool_a had no tool_b edge to
	// migrate).
	replacement := makeStep("tool_a_recovery", "plan", "tool_b")
	require.NoError(t, m.ReplaceNode(context.Background(), "tool_a", replacement))

	// The declared dependency must be a real edge: tool_b → tool_a_recovery.
	dag := m.Snapshot()
	targets := dag.Edges["tool_b"]
	found := false
	for _, tgt := range targets {
		if tgt == "tool_a_recovery" {
			found = true
			break
		}
	}
	assert.True(t, found,
		"newly declared DependsOn (tool_b) must become an edge to the replacement, got tool_b edges %v", targets)

	// And the topological order must respect it: tool_a_recovery strictly
	// after tool_b.
	order, err := m.GetExecutionOrder()
	require.NoError(t, err)
	posRecovery, posToolB := -1, -1
	for i, id := range order {
		switch id {
		case "tool_a_recovery":
			posRecovery = i
		case "tool_b":
			posToolB = i
		}
	}
	require.NotEqual(t, -1, posRecovery)
	require.NotEqual(t, -1, posToolB)
	assert.Greater(t, posRecovery, posToolB,
		"the replacement must be ordered after its declared dependency (order: %v)", order)
}
