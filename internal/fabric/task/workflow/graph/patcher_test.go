package graph

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

func buildPatcherTestGraph(t *testing.T) *Graph {
	t.Helper()
	g, err := NewGraph("test")
	require.NoError(t, err)

	nodeA, err := NewFuncNode("A", func(_ context.Context, _ *State) error { return nil })
	require.NoError(t, err)
	nodeB, err := NewFuncNode("B", func(_ context.Context, _ *State) error { return nil })
	require.NoError(t, err)
	nodeC, err := NewFuncNode("C", func(_ context.Context, _ *State) error { return nil })
	require.NoError(t, err)

	_, err = g.Node("A", nodeA)
	require.NoError(t, err)
	_, err = g.Node("B", nodeB)
	require.NoError(t, err)
	_, err = g.Node("C", nodeC)
	require.NoError(t, err)

	_, err = g.Edge("A", "B")
	require.NoError(t, err)
	_, err = g.Edge("B", "C")
	require.NoError(t, err)

	_, err = g.Start("A")
	require.NoError(t, err)

	return g
}

func TestNewGraphPatchExecutor(t *testing.T) {
	g := buildPatcherTestGraph(t)
	exec := NewGraphPatchExecutor(g)
	require.NotNil(t, exec)
	assert.Same(t, g, exec.graph)
}

func TestGraphPatchExecutor_Apply_InsertNode(t *testing.T) {
	g := buildPatcherTestGraph(t)
	exec := NewGraphPatchExecutor(g)

	fn, err := NewFuncNode("D", func(_ context.Context, _ *State) error { return nil })
	require.NoError(t, err)

	rollback, err := exec.Apply(context.Background(), patch.RuntimePatch{
		Type:   patch.PatchInsertNode,
		Target: "D",
		Value:  fn,
	})
	require.NoError(t, err)
	require.NotNil(t, rollback)
	assert.Equal(t, patch.PatchRemoveNode, rollback.Type)
	assert.Equal(t, "D", rollback.Target)

	// Verify node was added.
	_, exists := g.nodes["D"]
	assert.True(t, exists)
}

func TestGraphPatchExecutor_Apply_RemoveNode(t *testing.T) {
	g := buildPatcherTestGraph(t)
	exec := NewGraphPatchExecutor(g)

	rollback, err := exec.Apply(context.Background(), patch.RuntimePatch{
		Type:   patch.PatchRemoveNode,
		Target: "C",
	})
	require.NoError(t, err)
	require.NotNil(t, rollback)
	assert.Equal(t, patch.PatchInsertNode, rollback.Type)
	assert.Equal(t, "C", rollback.Target)

	// Verify node was removed.
	_, exists := g.nodes["C"]
	assert.False(t, exists)
}

func TestGraphPatchExecutor_Apply_RemoveNode_NotFound(t *testing.T) {
	g := buildPatcherTestGraph(t)
	exec := NewGraphPatchExecutor(g)

	_, err := exec.Apply(context.Background(), patch.RuntimePatch{
		Type:   patch.PatchRemoveNode,
		Target: "NONEXISTENT",
	})
	assert.Error(t, err)
}

func TestGraphPatchExecutor_Apply_ReplaceNode(t *testing.T) {
	g := buildPatcherTestGraph(t)
	exec := NewGraphPatchExecutor(g)

	newNode, err := NewFuncNode("A-v2", func(_ context.Context, _ *State) error { return nil })
	require.NoError(t, err)

	rollback, err := exec.Apply(context.Background(), patch.RuntimePatch{
		Type:   patch.PatchReplaceNode,
		Target: "A",
		Value:  newNode,
	})
	require.NoError(t, err)
	require.NotNil(t, rollback)
	assert.Equal(t, patch.PatchReplaceNode, rollback.Type)

	// Verify node was replaced (still exists with same key).
	_, exists := g.nodes["A"]
	assert.True(t, exists)
}

func TestGraphPatchExecutor_Apply_AddEdge(t *testing.T) {
	g := buildPatcherTestGraph(t)
	exec := NewGraphPatchExecutor(g)

	rollback, err := exec.Apply(context.Background(), patch.RuntimePatch{
		Type:   patch.PatchAddEdge,
		Target: "A",
		Value:  "C",
	})
	require.NoError(t, err)
	require.NotNil(t, rollback)
	assert.Equal(t, patch.PatchRemoveEdge, rollback.Type)

	// Verify edge was added.
	found := false
	for _, edge := range g.edges["A"] {
		if edge.to == "C" {
			found = true
			break
		}
	}
	assert.True(t, found, "edge A→C should exist")
}

func TestGraphPatchExecutor_Apply_RemoveEdge(t *testing.T) {
	g := buildPatcherTestGraph(t)
	exec := NewGraphPatchExecutor(g)

	rollback, err := exec.Apply(context.Background(), patch.RuntimePatch{
		Type:   patch.PatchRemoveEdge,
		Target: "A",
		Value:  "B",
	})
	require.NoError(t, err)
	require.NotNil(t, rollback)
	assert.Equal(t, patch.PatchAddEdge, rollback.Type)

	// Verify edge was removed.
	for _, edge := range g.edges["A"] {
		assert.NotEqual(t, "B", edge.to, "edge A→B should be removed")
	}
}

func TestGraphPatchExecutor_Apply_ChangeScheduler(t *testing.T) {
	g := buildPatcherTestGraph(t)
	exec := NewGraphPatchExecutor(g)

	newSched := NewRoundRobinScheduler()

	rollback, err := exec.Apply(context.Background(), patch.RuntimePatch{
		Type:  patch.PatchChangeScheduler,
		Value: newSched,
	})
	require.NoError(t, err)
	require.NotNil(t, rollback)
	assert.Equal(t, patch.PatchChangeScheduler, rollback.Type)

	// Verify scheduler was changed.
	assert.IsType(t, &RoundRobinScheduler{}, g.scheduler)

	// Verify rollback restores original scheduler.
	_, ok := rollback.Value.(*DefaultScheduler)
	assert.True(t, ok, "rollback value should be the original DefaultScheduler")
}

func TestGraphPatchExecutor_Apply_UnsupportedType(t *testing.T) {
	g := buildPatcherTestGraph(t)
	exec := NewGraphPatchExecutor(g)

	_, err := exec.Apply(context.Background(), patch.RuntimePatch{
		Type: patch.PatchType(999),
	})
	assert.Error(t, err)
}

func TestGraphPatchExecutor_CanApply(t *testing.T) {
	g := buildPatcherTestGraph(t)
	exec := NewGraphPatchExecutor(g)

	tests := []struct {
		name  string
		patch patch.RuntimePatch
		want  bool
	}{
		{"insert node valid", patch.RuntimePatch{Type: patch.PatchInsertNode, Target: "D"}, true},
		{"insert node empty target", patch.RuntimePatch{Type: patch.PatchInsertNode, Target: ""}, false},
		{"remove node valid", patch.RuntimePatch{Type: patch.PatchRemoveNode, Target: "A"}, true},
		{"remove node empty target", patch.RuntimePatch{Type: patch.PatchRemoveNode, Target: ""}, false},
		{"add edge valid", patch.RuntimePatch{Type: patch.PatchAddEdge, Target: "A", Value: "C"}, true},
		{"add edge empty value", patch.RuntimePatch{Type: patch.PatchAddEdge, Target: "A", Value: ""}, false},
		{"add edge non-string value", patch.RuntimePatch{Type: patch.PatchAddEdge, Target: "A", Value: 42}, false},
		{"change scheduler valid", patch.RuntimePatch{Type: patch.PatchChangeScheduler}, true},
		{"unsupported type", patch.RuntimePatch{Type: patch.PatchType(999)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := exec.CanApply(context.Background(), tt.patch)
			if tt.want {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestGraphPatchExecutor_CanApply_NilGraph(t *testing.T) {
	exec := &GraphPatchExecutor{graph: nil}
	err := exec.CanApply(context.Background(), patch.RuntimePatch{
		Type: patch.PatchInsertNode, Target: "A",
	})
	assert.Error(t, err)
}

// TestGraphPatchExecutor_PlaceholderNode_IsHonestNotSilent verifies the fix:
// when an InsertNode/ReplaceNode patch carries no real Node implementation, the
// executor does NOT silently return success doing nothing. Instead the inserted
// node writes a distinguishable PlaceholderResult into state so callers can tell
// no real work was performed. The node still returns nil so the DAG stays valid.
func TestGraphPatchExecutor_PlaceholderNode_IsHonestNotSilent(t *testing.T) {
	g := buildPatcherTestGraph(t)
	exec := NewGraphPatchExecutor(g)

	// Insert a node with NO Node implementation (Value omitted) — this is the
	// evolution path that previously produced a silent no-op.
	rollback, err := exec.Apply(context.Background(), patch.RuntimePatch{
		Type:   patch.PatchInsertNode,
		Target: "D",
	})
	require.NoError(t, err)
	require.NotNil(t, rollback)

	inserted, exists := g.nodes["D"]
	require.True(t, exists, "placeholder node D should be inserted")

	// Executing the placeholder must succeed (DAG stays valid) but must NOT be
	// a silent empty success: state must carry an explicit PlaceholderResult.
	state := NewState()
	err = inserted.Execute(context.Background(), state)
	require.NoError(t, err, "placeholder Execute must not fail the DAG")

	val, ok := state.Get("node.D")
	require.True(t, ok, "placeholder must write node.D rather than leaving state empty")
	placeholder, isPlaceholder := val.(PlaceholderResult)
	require.True(t, isPlaceholder, "node.D value must be a PlaceholderResult")
	assert.True(t, placeholder.Placeholder, "Placeholder flag must be true")
	assert.Equal(t, "D", placeholder.NodeID)
	assert.NotEmpty(t, placeholder.Reason, "Reason must explain why no work was done")
}

// TestGraphPatchExecutor_ReplaceNode_PlaceholderFallback verifies the same
// honest-placeholder behavior on the ReplaceNode path when no real Node is
// supplied.
func TestGraphPatchExecutor_ReplaceNode_PlaceholderFallback(t *testing.T) {
	g := buildPatcherTestGraph(t)
	exec := NewGraphPatchExecutor(g)

	// Replace A with a placeholder (no Node implementation supplied).
	_, err := exec.Apply(context.Background(), patch.RuntimePatch{
		Type:   patch.PatchReplaceNode,
		Target: "A",
	})
	require.NoError(t, err)

	replaced, exists := g.nodes["A"]
	require.True(t, exists)

	state := NewState()
	require.NoError(t, replaced.Execute(context.Background(), state))

	val, ok := state.Get("node.A")
	require.True(t, ok)
	_, isPlaceholder := val.(PlaceholderResult)
	assert.True(t, isPlaceholder, "replaced node A must produce a PlaceholderResult")
}

// TestGraphPatchExecutor_Apply_ChangeScheduler_Concurrent_NoRace exercises the
// data race between applyChangeScheduler (which reads e.graph.scheduler) and
// SetScheduler (which writes it under the write lock). Under -race this must
// not flag a concurrent read/write on the scheduler field.
func TestGraphPatchExecutor_Apply_ChangeScheduler_Concurrent_NoRace(t *testing.T) {
	g := buildPatcherTestGraph(t)
	exec := NewGraphPatchExecutor(g)

	priorities := map[string]int{"A": 1, "B": 2, "C": 3}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = exec.Apply(context.Background(), patch.RuntimePatch{
				Type:  patch.PatchChangeScheduler,
				Value: NewRoundRobinScheduler(),
			})
		}()
		go func() {
			defer wg.Done()
			_, _ = g.SetScheduler(NewPriorityScheduler(priorities))
		}()
	}
	wg.Wait()

	// Final read must go through the locked accessor to stay race-free.
	_ = g.RuntimeScheduler()
}
