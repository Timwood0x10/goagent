package evolution

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/coordinator"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

func TestNewLLMAdapter(t *testing.T) {
	a := NewLLMAdapter()
	require.NotNil(t, a)
}

func TestLLMAdapter_Parse_Empty(t *testing.T) {
	a := NewLLMAdapter()
	_, err := a.Parse(context.Background(), "")
	assert.Error(t, err)
}

func TestLLMAdapter_Parse_InsertNode(t *testing.T) {
	a := NewLLMAdapter()
	results, err := a.Parse(context.Background(), "insert node validator after A")
	require.NoError(t, err)
	require.Len(t, results, 1)

	prop := results[0].Proposal
	assert.Equal(t, coordinator.SourceLLM, prop.Source)
	assert.Equal(t, patch.PatchInsertNode, prop.Patch.Type)
	assert.Equal(t, "validator", prop.Patch.Target)
	assert.Equal(t, "A", prop.Patch.Value)
	assert.Equal(t, 4, prop.Priority)
}

func TestLLMAdapter_Parse_InsertNode_BadFormat(t *testing.T) {
	a := NewLLMAdapter()
	_, err := a.Parse(context.Background(), "insert node lonely")
	assert.Error(t, err)
}

func TestLLMAdapter_Parse_RemoveNode(t *testing.T) {
	a := NewLLMAdapter()
	results, err := a.Parse(context.Background(), "remove node C")
	require.NoError(t, err)
	require.Len(t, results, 1)

	assert.Equal(t, patch.PatchRemoveNode, results[0].Proposal.Patch.Type)
	assert.Equal(t, "C", results[0].Proposal.Patch.Target)
}

func TestLLMAdapter_Parse_RemoveNode_BadFormat(t *testing.T) {
	a := NewLLMAdapter()
	_, err := a.Parse(context.Background(), "remove node")
	assert.Error(t, err)
}

func TestLLMAdapter_Parse_ReplaceNode(t *testing.T) {
	a := NewLLMAdapter()
	results, err := a.Parse(context.Background(), "replace node B with quality-check")
	require.NoError(t, err)
	require.Len(t, results, 1)

	assert.Equal(t, patch.PatchReplaceNode, results[0].Proposal.Patch.Type)
	assert.Equal(t, "B", results[0].Proposal.Patch.Target)
	assert.Equal(t, "quality-check", results[0].Proposal.Patch.Value)
}

func TestLLMAdapter_Parse_ReplaceNode_BadFormat(t *testing.T) {
	a := NewLLMAdapter()
	_, err := a.Parse(context.Background(), "replace node lonely")
	assert.Error(t, err)
}

func TestLLMAdapter_Parse_AddEdge(t *testing.T) {
	a := NewLLMAdapter()
	results, err := a.Parse(context.Background(), "add edge A -> B")
	require.NoError(t, err)
	require.Len(t, results, 1)

	assert.Equal(t, patch.PatchAddEdge, results[0].Proposal.Patch.Type)
	assert.Equal(t, "A", results[0].Proposal.Patch.Target)
	assert.Equal(t, "B", results[0].Proposal.Patch.Value)
}

func TestLLMAdapter_Parse_AddEdge_BadFormat(t *testing.T) {
	a := NewLLMAdapter()
	_, err := a.Parse(context.Background(), "add edge lonely")
	assert.Error(t, err)
}

func TestLLMAdapter_Parse_RemoveEdge(t *testing.T) {
	a := NewLLMAdapter()
	results, err := a.Parse(context.Background(), "remove edge B -> C")
	require.NoError(t, err)
	require.Len(t, results, 1)

	assert.Equal(t, patch.PatchRemoveEdge, results[0].Proposal.Patch.Type)
	assert.Equal(t, "B", results[0].Proposal.Patch.Target)
	assert.Equal(t, "C", results[0].Proposal.Patch.Value)
}

func TestLLMAdapter_Parse_ChangeScheduler(t *testing.T) {
	a := NewLLMAdapter()
	results, err := a.Parse(context.Background(), "change scheduler to round_robin")
	require.NoError(t, err)
	require.Len(t, results, 1)

	assert.Equal(t, patch.PatchChangeScheduler, results[0].Proposal.Patch.Type)
	assert.Equal(t, "graph.scheduler", results[0].Proposal.Patch.Target)
	assert.Equal(t, "round_robin", results[0].Proposal.Patch.Value)
}

func TestLLMAdapter_Parse_ChangeScheduler_BadFormat(t *testing.T) {
	a := NewLLMAdapter()
	_, err := a.Parse(context.Background(), "change scheduler lonely")
	assert.Error(t, err)
}

func TestLLMAdapter_Parse_ChangeTopK(t *testing.T) {
	a := NewLLMAdapter()
	results, err := a.Parse(context.Background(), "change topk to 50")
	require.NoError(t, err)
	require.Len(t, results, 1)

	assert.Equal(t, patch.PatchChangeBudget, results[0].Proposal.Patch.Type)
	assert.Equal(t, "knowledge.planner.max_results", results[0].Proposal.Patch.Target)
	assert.Equal(t, 50, results[0].Proposal.Patch.Value)
}

func TestLLMAdapter_Parse_ChangeTopK_InvalidNumber(t *testing.T) {
	a := NewLLMAdapter()
	_, err := a.Parse(context.Background(), "change topk to abc")
	assert.Error(t, err)
}

func TestLLMAdapter_Parse_ChangeReducer(t *testing.T) {
	a := NewLLMAdapter()
	results, err := a.Parse(context.Background(), "change reducer to strict")
	require.NoError(t, err)
	require.Len(t, results, 1)

	assert.Equal(t, patch.PatchChangeReducer, results[0].Proposal.Patch.Type)
	assert.Equal(t, "knowledge.planner.reducer", results[0].Proposal.Patch.Target)
	assert.Equal(t, "strict", results[0].Proposal.Patch.Value)
}

func TestLLMAdapter_Parse_ChangePlanner(t *testing.T) {
	a := NewLLMAdapter()
	results, err := a.Parse(context.Background(), "change planner to memory-first")
	require.NoError(t, err)
	require.Len(t, results, 1)

	assert.Equal(t, patch.PatchChangePlanner, results[0].Proposal.Patch.Type)
	assert.Equal(t, "knowledge.planner.strategy", results[0].Proposal.Patch.Target)
	assert.Equal(t, "memory-first", results[0].Proposal.Patch.Value)
}

func TestLLMAdapter_Parse_ChangeRecovery(t *testing.T) {
	a := NewLLMAdapter()
	results, err := a.Parse(context.Background(), "change recovery to replace_node")
	require.NoError(t, err)
	require.Len(t, results, 1)

	assert.Equal(t, patch.PatchChangeRecoveryStrategy, results[0].Proposal.Patch.Type)
	assert.Equal(t, "recovery.strategy", results[0].Proposal.Patch.Target)
	assert.Equal(t, "replace_node", results[0].Proposal.Patch.Value)
}

func TestLLMAdapter_Parse_Unrecognized(t *testing.T) {
	a := NewLLMAdapter()
	_, err := a.Parse(context.Background(), "do something completely different")
	assert.Error(t, err)
}

func TestLLMAdapter_Parse_CaseInsensitive(t *testing.T) {
	a := NewLLMAdapter()
	// All formats should work with any casing.
	results, err := a.Parse(context.Background(), "REMOVE NODE X")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, patch.PatchRemoveNode, results[0].Proposal.Patch.Type)
	assert.Equal(t, "X", results[0].Proposal.Patch.Target)
}

func TestLLMAdapter_Parse_ExtraWhitespace(t *testing.T) {
	a := NewLLMAdapter()
	results, err := a.Parse(context.Background(), "  remove   node   Z  ")
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "Z", results[0].Proposal.Patch.Target)
}

func TestLLMAdapter_Proposal_HasSourceLLM(t *testing.T) {
	a := NewLLMAdapter()
	results, err := a.Parse(context.Background(), "replace node A with B")
	require.NoError(t, err)

	// Verify the proposal carries the LLM source, not GA or Chaos.
	assert.Equal(t, coordinator.SourceLLM, results[0].Proposal.Source)
	// Verify the patch also carries "llm" as its source string.
	assert.Equal(t, "llm", results[0].Proposal.Patch.Source)
}

func TestLLMAdapter_Proposal_HasTimestamp(t *testing.T) {
	a := NewLLMAdapter()
	results, err := a.Parse(context.Background(), "remove node A")
	require.NoError(t, err)
	assert.False(t, results[0].Proposal.Timestamp.IsZero(), "proposal should have a timestamp")
}
