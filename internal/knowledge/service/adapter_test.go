package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/knowledge/runtime"
	apiknowledge "github.com/Timwood0x10/ares/internal/knowledgeapi"
)

// TestNewServiceAdapter_NilRuntimeReturnsError verifies the nil guard.
func TestNewServiceAdapter_NilRuntimeReturnsError(t *testing.T) {
	_, err := NewServiceAdapter(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

// TestBuildGraph_EmptyGoalReturnsErrNilIntent verifies the input validation.
func TestBuildGraph_EmptyGoalReturnsErrNilIntent(t *testing.T) {
	adapter, err := NewServiceAdapter(runtime.New(nil, nil, nil, nil, nil, nil))
	require.NoError(t, err)

	_, err = adapter.BuildGraph(context.Background(), apiknowledge.Intent{})
	require.Error(t, err)
	assert.ErrorIs(t, err, apiknowledge.ErrNilIntent)
}

// TestCompileContext_NilGraphReturnsErrNilGraph verifies the nil graph guard.
func TestCompileContext_NilGraphReturnsErrNilGraph(t *testing.T) {
	adapter, err := NewServiceAdapter(runtime.New(nil, nil, nil, nil, nil, nil))
	require.NoError(t, err)

	_, err = adapter.CompileContext(context.Background(), nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, apiknowledge.ErrNilGraph)
}

// TestDistill_EmptyTenantIDReturnsErr verifies the tenant guard.
func TestDistill_EmptyTenantIDReturnsErr(t *testing.T) {
	adapter, err := NewServiceAdapter(runtime.New(nil, nil, nil, nil, nil, nil))
	require.NoError(t, err)

	_, err = adapter.Distill(context.Background(), []byte("data"), "")
	require.Error(t, err)
	assert.ErrorIs(t, err, apiknowledge.ErrEmptyTenantID)
}

// TestDistill_EmptyMemoryReturnsNil verifies the empty-input guard.
func TestDistill_EmptyMemoryReturnsNil(t *testing.T) {
	adapter, err := NewServiceAdapter(runtime.New(nil, nil, nil, nil, nil, nil))
	require.NoError(t, err)

	objs, err := adapter.Distill(context.Background(), []byte{}, "tenant-1")
	require.NoError(t, err)
	assert.Empty(t, objs)
}

// TestCompileContext_NonNilGraphProducesMarkdown verifies the happy path
// produces a non-empty markdown string.
func TestCompileContext_NonNilGraphProducesMarkdown(t *testing.T) {
	adapter, err := NewServiceAdapter(runtime.New(nil, nil, nil, nil, nil, nil))
	require.NoError(t, err)

	graph := &apiknowledge.WorkingGraph{
		Nodes: map[string]*apiknowledge.KnowledgeObject{
			"node-1": {
				ID:      "node-1",
				Type:    apiknowledge.ObjectMemory,
				Summary: "test summary",
			},
		},
	}
	out, err := adapter.CompileContext(context.Background(), graph)
	require.NoError(t, err)
	assert.Contains(t, out, "node-1")
	assert.Contains(t, out, "test summary")
}

// TestQuery_ReturnsEmpty verifies the stateless query path returns
// an empty slice (not nil, not an error).
func TestQuery_ReturnsEmpty(t *testing.T) {
	adapter, err := NewServiceAdapter(runtime.New(nil, nil, nil, nil, nil, nil))
	require.NoError(t, err)

	objs, err := adapter.Query(context.Background(), apiknowledge.Query{Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, objs)
}
