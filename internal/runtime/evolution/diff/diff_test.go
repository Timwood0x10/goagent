package diff

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/genome"
	"github.com/Timwood0x10/ares/internal/workflow/engine"
)

func TestDifferInterface(t *testing.T) {
	var d Differ
	d = NewWorkflowDiffer()
	assert.Equal(t, "workflow", d.Name())

	d = NewKnowledgeDiffer()
	assert.Equal(t, genome.KnowledgeGenomeName, d.Name())

	d = NewRecoveryDiffer()
	assert.Equal(t, genome.RecoveryGenomeName, d.Name())
}

// ── Registry ───────────────────────────────

func TestDiffRegistry_Register(t *testing.T) {
	r := NewRegistry()
	err := r.Register(NewWorkflowDiffer())
	assert.NoError(t, err)
}

func TestDiffRegistry_Register_Duplicate(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(NewWorkflowDiffer()))
	err := r.Register(NewWorkflowDiffer())
	assert.Error(t, err)
}

func TestDiffRegistry_Register_Nil(t *testing.T) {
	r := NewRegistry()
	err := r.Register(nil)
	assert.Error(t, err)
}

func TestDiffRegistry_Get(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(NewWorkflowDiffer()))

	d, err := r.Get("workflow")
	require.NoError(t, err)
	assert.NotNil(t, d)
}

func TestDiffRegistry_Get_NotFound(t *testing.T) {
	r := NewRegistry()
	_, err := r.Get("nonexistent")
	assert.Error(t, err)
}

func TestDiffRegistry_List(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(NewWorkflowDiffer()))

	names := r.List()
	assert.ElementsMatch(t, []string{"workflow"}, names)
}

func TestDiffRegistry_DiffAll(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(NewWorkflowDiffer()))

	ctx := context.Background()
	snapshots := map[string]SnapshotPair{
		"workflow": {Old: oldDAG(t), New: newDAG(t)},
	}
	patches, err := r.DiffAll(ctx, snapshots)
	require.NoError(t, err)
	for _, p := range patches {
		assert.Equal(t, srcWorkflow, p.Source)
	}
}

func newDAG(t *testing.T) *engine.DAG {
	t.Helper()
	return &engine.DAG{
		Nodes: map[string]*engine.DAGNode{
			"A": {StepID: "A"}, "B": {StepID: "B"}, "C": {StepID: "C"}, "D": {StepID: "D"},
		},
		Edges: map[string][]string{
			"A": {"B"}, "B": {"C"}, "C": {"D"},
		},
	}
}

// oldDAG differs from newDAG by one node/edge so differs have something to report.
func oldDAG(t *testing.T) *engine.DAG {
	t.Helper()
	return &engine.DAG{
		Nodes: map[string]*engine.DAGNode{
			"A": {StepID: "A"}, "B": {StepID: "B"}, "C": {StepID: "C"},
		},
		Edges: map[string][]string{
			"A": {"B"}, "B": {"C"},
		},
	}
}
