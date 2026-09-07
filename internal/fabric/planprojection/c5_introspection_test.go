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

// TestC5_4_AttributionTriplet verifies C5.4: the triplet (generation,
// gate, compile_id) is non-empty and mutually correlatable after a
// compile. The test exercises the CompileCoordinator's introspection
// surface (CompileID, DAGVersion, CompileCount) which feeds the
// lifecycle snapshot's compile fields.
//
// The triplet answers "which generation, which gate, which compile" —
// the introspection acceptance contract (C5.2/C5.4).
func TestC5_4_AttributionTriplet(t *testing.T) {
	dag, err := engine.NewMutableDAG([]*engine.Step{
		{ID: "a", AgentType: "x"},
		{ID: "b", AgentType: "y", DependsOn: []string{"a"}},
	})
	require.NoError(t, err)

	store := ares_events.NewMemoryEventStore()
	fabric := taskfabric.NewFabric()
	coord := NewCompileCoordinator(fabric, store)
	coord.SetGeneration(5) // simulate generation 5

	// Before compile: all zero.
	assert.Equal(t, "", coord.CompileID())
	assert.Equal(t, uint64(0), coord.DAGVersion())
	assert.Equal(t, uint64(0), coord.CompileCount())

	// Compile.
	rec, err := coord.CompileDAG(context.Background(), dag)
	require.NoError(t, err)

	// After compile: triplet is non-empty and consistent.
	assert.Equal(t, uint64(1), coord.CompileCount(), "compile count must be 1")
	assert.Equal(t, rec.CompileID, coord.CompileID(), "CompileID must match the record")
	assert.Equal(t, rec.DAGVersion, coord.DAGVersion(), "DAGVersion must match the record")
	assert.NotEmpty(t, coord.CompileID(), "CompileID must be non-empty")
	assert.Equal(t, 5, rec.Generation, "generation must be 5")

	// Mutate the DAG → version increments.
	ctx := context.Background()
	require.NoError(t, dag.AddNode(ctx, &engine.Step{ID: "c", AgentType: "z", DependsOn: []string{"b"}}))

	// Recompile.
	rec2, err := coord.CompileDAG(ctx, dag)
	require.NoError(t, err)

	// Triplet reflects the new compile.
	assert.Equal(t, uint64(2), coord.CompileCount(), "compile count must be 2")
	assert.Equal(t, rec2.CompileID, coord.CompileID())
	assert.Equal(t, uint64(1), coord.DAGVersion(), "DAG version must be 1 after AddNode")
	assert.True(t, rec2.DAGVersion > rec.DAGVersion, "DAG version must increment")

	// The triplet is correlatable: (generation, compile_id, dag_version)
	// are all from the same compile and can answer "which generation,
	// which gate, which compile".
	assert.Equal(t, rec2.Generation, coord.LastCompile().Generation)
	assert.Equal(t, rec2.CompileID, coord.LastCompile().CompileID)
	assert.Equal(t, rec2.DAGVersion, coord.LastCompile().DAGVersion)
}

// TestC5_4_CompileEventRecorded verifies C5.4/C1.4: the compile event
// is recorded in the EventStore so the introspect layer can query it.
func TestC5_4_CompileEventRecorded(t *testing.T) {
	dag, err := engine.NewMutableDAG([]*engine.Step{
		{ID: "a", AgentType: "x"},
	})
	require.NoError(t, err)

	store := ares_events.NewMemoryEventStore()
	fabric := taskfabric.NewFabric()
	coord := NewCompileCoordinator(fabric, store)
	coord.SetGeneration(2)

	_, err = coord.CompileDAG(context.Background(), dag)
	require.NoError(t, err)

	// Read the compile events from the event store.
	events, err := store.Read(context.Background(), "evolution.compile", ares_events.ReadOptions{})
	require.NoError(t, err)
	require.Len(t, events, 1)

	evt := events[0]
	assert.Equal(t, ares_events.EventType("evolution.compile"), evt.Type)
	assert.Equal(t, "evolution.compile", evt.StreamID)

	// The event payload must carry the attribution triplet.
	payload := evt.Payload
	assert.Equal(t, 2, payload["generation"])
	assert.Equal(t, uint64(0), payload["dag_version"])
	assert.NotEmpty(t, payload["compile_id"])
}
