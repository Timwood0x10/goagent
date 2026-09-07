package planprojection

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/fabric/task"
	"github.com/Timwood0x10/ares/internal/fabric/task/workflow/engine"
)

// TestC4_StructuralPatchTriggersRecompile verifies:
// a structural patch (AddNode) on the live MutableDAG triggers
// recompilation via the GraphEvent subscription, so the next
// scheduler drain sees the updated task set — without restart.
//
// This is the "补丁落 live DAG" acceptance: the patch executor
// mutates the SAME DAG the compile coordinator subscribes to, so
// the projection path is closed (no "two graphs").
func TestC4_StructuralPatchTriggersRecompile(t *testing.T) {
	dag, err := engine.NewMutableDAG([]*engine.Step{
		{ID: "a", AgentType: "x"},
		{ID: "b", AgentType: "y", DependsOn: []string{"a"}},
	})
	require.NoError(t, err)

	store := ares_events.NewMemoryEventStore()
	fabric := taskfabric.NewFabric()
	coord := NewCompileCoordinator(fabric, store)

	// Initial compile.
	_, err = coord.CompileDAG(context.Background(), dag)
	require.NoError(t, err)
	require.Equal(t, 2, coord.LastCompile().StepCount)

	// Subscribe to graph events (the recompile path).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	unsub := coord.SubscribeGraphEvents(ctx, dag)
	defer unsub()

	// Structural patch — add a node.
	addErr := dag.AddNode(ctx, &engine.Step{
		ID:        "c",
		AgentType: "z",
		DependsOn: []string{"b"},
	})
	require.NoError(t, addErr)

	// Wait for recompile.
	ok := waitFor(2, func() bool {
		return coord.LastCompile().StepCount == 3
	})
	require.True(t, ok, "recompile must fire after AddNode, step count must be 3")

	// DAG version must be reflected in the compile record.
	last := coord.LastCompile()
	assert.Equal(t, uint64(1), last.DAGVersion, "DAG version must be 1 after AddNode")
	assert.Len(t, last.PlanIDs, 3, "3 tasks compiled from 3 steps")
}

// TestC4_RemoveNodeTriggersRecompile verifies: a RemoveNode
// structural patch also triggers recompilation.
func TestC4_RemoveNodeTriggersRecompile(t *testing.T) {
	dag, err := engine.NewMutableDAG([]*engine.Step{
		{ID: "a", AgentType: "x"},
		{ID: "b", AgentType: "y", DependsOn: []string{"a"}},
		{ID: "c", AgentType: "z", DependsOn: []string{"b"}},
	})
	require.NoError(t, err)

	fabric := taskfabric.NewFabric()
	coord := NewCompileCoordinator(fabric, nil)

	_, err = coord.CompileDAG(context.Background(), dag)
	require.NoError(t, err)
	require.Equal(t, 3, coord.LastCompile().StepCount)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	unsub := coord.SubscribeGraphEvents(ctx, dag)
	defer unsub()

	// Remove node "c" (leaf, no dependents).
	rmErr := dag.RemoveNode(ctx, "c")
	require.NoError(t, rmErr)

	ok := waitFor(2, func() bool {
		return coord.LastCompile().StepCount == 2
	})
	require.True(t, ok, "recompile must fire after RemoveNode, step count must be 2")
}

// TestC4_IdempotentRecompile verifies: compiling the same DAG
// twice does not produce duplicate tasks (the coordinator cleans up
// old tasks before recompiling).
func TestC4_IdempotentRecompile(t *testing.T) {
	dag, err := engine.NewMutableDAG([]*engine.Step{
		{ID: "a", AgentType: "x"},
		{ID: "b", AgentType: "y", DependsOn: []string{"a"}},
	})
	require.NoError(t, err)

	fabric := taskfabric.NewFabric()
	coord := NewCompileCoordinator(fabric, nil)

	// First compile.
	rec1, err := coord.CompileDAG(context.Background(), dag)
	require.NoError(t, err)
	require.Len(t, rec1.PlanIDs, 2)

	// Second compile of the SAME DAG — should not error (idempotent
	// cleanup of old tasks).
	rec2, err := coord.CompileDAG(context.Background(), dag)
	require.NoError(t, err, "idempotent recompile must not error")
	assert.Len(t, rec2.PlanIDs, 2, "same DAG must produce same task count")
}

// TestC4_CompileRecordCarriesDAGVersion verifies: the compile
// record carries the DAG version from MutableDAG.Version().
func TestC4_CompileRecordCarriesDAGVersion(t *testing.T) {
	dag, err := engine.NewMutableDAG([]*engine.Step{
		{ID: "a", AgentType: "x"},
	})
	require.NoError(t, err)

	fabric := taskfabric.NewFabric()
	coord := NewCompileCoordinator(fabric, nil)

	// Before any mutation: version 0.
	rec, err := coord.CompileDAG(context.Background(), dag)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), rec.DAGVersion)

	// After AddNode: version 1.
	ctx := context.Background()
	require.NoError(t, dag.AddNode(ctx, &engine.Step{ID: "b", AgentType: "y"}))

	rec2, err := coord.CompileDAG(ctx, dag)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), rec2.DAGVersion, "DAG version must be 1 after AddNode")
}
