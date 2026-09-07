package planprojection

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/fabric/task"
	"github.com/Timwood0x10/ares/internal/fabric/task/workflow/engine"
)

// waitFor polls cond until it returns true or the deadline passes.
// Returns true if cond became true, false on timeout.
func waitFor(seconds int, cond func() bool) bool {
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// TestProjectStep_MapsAllFields pins the projection contract: every
// PlanStep field is derived from the correct engine.Step source.
func TestProjectStep_MapsAllFields(t *testing.T) {
	s := &engine.Step{
		ID:        "step-1",
		AgentType: "code",
		Input:     "write code",
		DependsOn: []string{"step-0"},
		RetryPolicy: &engine.RetryPolicy{
			MaxAttempts: 3,
		},
		Metadata: map[string]string{
			"priority": "5",
			"custom":   "value",
		},
	}

	ps := ProjectStep(s)

	assert.Equal(t, "step-1", ps.ID)
	assert.Equal(t, "code", ps.Capability)
	assert.Equal(t, []string{"step-0"}, ps.DependsOn)
	assert.Equal(t, 3, ps.MaxRetries)
	assert.Equal(t, 5, ps.Priority)
	assert.Equal(t, "", ps.Origin, "Origin must not be filled by projection")
	assert.Equal(t, "write code", ps.Payload["input"])
	assert.Equal(t, "value", ps.Payload["custom"])
}

// TestProjectStep_NilRetryPolicy verifies nil RetryPolicy yields MaxRetries=0.
func TestProjectStep_NilRetryPolicy(t *testing.T) {
	s := &engine.Step{
		ID:        "step-1",
		AgentType: "code",
		DependsOn: []string{},
	}
	ps := ProjectStep(s)
	assert.Equal(t, 0, ps.MaxRetries, "nil RetryPolicy → MaxRetries=0 (kernel default 2)")
}

// TestProjectStep_MissingPriority verifies missing/invalid priority → 0.
func TestProjectStep_MissingPriority(t *testing.T) {
	cases := []struct {
		name string
		meta map[string]string
	}{
		{"nil_metadata", nil},
		{"missing_key", map[string]string{}},
		{"empty_value", map[string]string{"priority": ""}},
		{"non_numeric", map[string]string{"priority": "high"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ps := ProjectStep(&engine.Step{
				ID:       "s",
				Metadata: tc.meta,
			})
			assert.Equal(t, 0, ps.Priority, "priority must be 0 for %s", tc.name)
		})
	}
}

// TestProjectStep_DiscardsExecutionState verifies that execution-time
// fields (Name, Status, Output, etc.) are NOT carried into PlanStep.
func TestProjectStep_DiscardsExecutionState(t *testing.T) {
	s := &engine.Step{
		ID:        "s",
		AgentType: "code",
		Name:      "should-be-discarded",
		Status:    engine.StepStatusRunning,
		Output:    "in-progress",
	}
	ps := ProjectStep(s)
	// Payload only carries input + metadata, never Name/Status/Output.
	assert.NotContains(t, ps.Payload, "should-be-discarded")
	assert.NotContains(t, ps.Payload, "in-progress")
}

// TestProjectSteps_PreservesOrder verifies batch projection preserves
// input order (CompilePlan relies on it for deterministic task IDs).
func TestProjectSteps_PreservesOrder(t *testing.T) {
	steps := []*engine.Step{
		{ID: "a", AgentType: "x"},
		{ID: "b", AgentType: "y"},
		{ID: "c", AgentType: "z"},
	}
	result := ProjectSteps(steps)
	require.Len(t, result, 3)
	assert.Equal(t, "a", result[0].ID)
	assert.Equal(t, "b", result[1].ID)
	assert.Equal(t, "c", result[2].ID)
}

// TestProjectSteps_DependencyEquivalence verifies the projected PlanSteps
// have the same dependency topology as the source steps.
func TestProjectSteps_DependencyEquivalence(t *testing.T) {
	steps := []*engine.Step{
		{ID: "a", AgentType: "x"},
		{ID: "b", AgentType: "y", DependsOn: []string{"a"}},
		{ID: "c", AgentType: "z", DependsOn: []string{"a", "b"}},
	}
	result := ProjectSteps(steps)

	// Build a dependency map from the projected steps.
	depMap := make(map[string][]string, len(result))
	for _, ps := range result {
		depMap[ps.ID] = ps.DependsOn
	}

	assert.Equal(t, []string{}, depMap["a"])
	assert.Equal(t, []string{"a"}, depMap["b"])
	assert.Equal(t, []string{"a", "b"}, depMap["c"])
}

// TestCompileCoordinator_CompileDAG_RecordsProvenance verifies:
// the compile coordinator records generation, DAG version, compile ID,
// and plan IDs for introspection.
func TestCompileCoordinator_CompileDAG_RecordsProvenance(t *testing.T) {
	dag, err := engine.NewMutableDAG([]*engine.Step{
		{ID: "a", AgentType: "x"},
		{ID: "b", AgentType: "y", DependsOn: []string{"a"}},
	})
	require.NoError(t, err)

	store := ares_events.NewMemoryEventStore()
	fabric := taskfabric.NewFabric()
	coord := NewCompileCoordinator(fabric, store)
	coord.SetGeneration(3)

	record, err := coord.CompileDAG(context.Background(), dag)
	require.NoError(t, err)
	assert.Equal(t, 3, record.Generation)
	assert.Equal(t, uint64(0), record.DAGVersion, "DAG version is 0 before any mutation")
	assert.Equal(t, 2, record.StepCount)
	assert.Len(t, record.PlanIDs, 2)
	assert.NotEmpty(t, record.CompileID)

	// LastCompile returns the same record.
	last := coord.LastCompile()
	assert.Equal(t, record.CompileID, last.CompileID)
}

// TestCompileCoordinator_DoesNotSwallowCompileError verifies:
// the projection layer must not silently swallow CompilePlan errors.
// An empty DAG (0 steps) is rejected by CompilePlan with "empty step
// batch" — the projection must surface that, not swallow it.
func TestCompileCoordinator_DoesNotSwallowCompileError(t *testing.T) {
	dag, err := engine.NewMutableDAG(nil)
	require.NoError(t, err)

	fabric := taskfabric.NewFabric()
	coord := NewCompileCoordinator(fabric, nil)

	_, err = coord.CompileDAG(context.Background(), dag)
	require.Error(t, err, "empty DAG must produce a surfaced error, not be swallowed")
}

// TestCompileCoordinator_GraphEventTriggersRecompile verifies:
// a structural mutation (AddNode) triggers recompilation via the
// GraphEvent subscription, so the next scheduler drain sees the new task.
func TestCompileCoordinator_GraphEventTriggersRecompile(t *testing.T) {
	dag, err := engine.NewMutableDAG([]*engine.Step{
		{ID: "a", AgentType: "x"},
	})
	require.NoError(t, err)

	store := ares_events.NewMemoryEventStore()
	fabric := taskfabric.NewFabric()
	coord := NewCompileCoordinator(fabric, store)

	// Initial compile.
	_, err = coord.CompileDAG(context.Background(), dag)
	require.NoError(t, err)
	require.Equal(t, 1, coord.LastCompile().StepCount)

	// Subscribe to graph events.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	unsub := coord.SubscribeGraphEvents(ctx, dag)
	defer unsub()

	// Add a node — should trigger recompile.
	addErr := dag.AddNode(ctx, &engine.Step{
		ID:        "b",
		AgentType: "y",
		DependsOn: []string{"a"},
	})
	require.NoError(t, addErr)

	// Wait for the recompile to land (poll, no time.Sleep).
	deadline := waitFor(2, func() bool {
		return coord.LastCompile().StepCount == 2
	})
	if !deadline {
		t.Fatal("recompile did not fire after GraphEvent")
	}

	// The new compile record must reflect the updated DAG version.
	last := coord.LastCompile()
	assert.Equal(t, uint64(1), last.DAGVersion, "DAG version must be 1 after AddNode")
	assert.Len(t, last.PlanIDs, 2)
}
