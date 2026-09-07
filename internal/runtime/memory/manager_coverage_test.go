package memory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	memctx "github.com/Timwood0x10/ares/internal/runtime/memory/context"
)

// TestConvertRawToToolCalls is a table-driven contract for the JSON-metadata
// → typed ToolCall conversion used when replaying persisted messages.
//
// Bug scenarios:
//  1. A non-map entry inside the raw array must be skipped, not panic.
//  2. A tool call without a "function" object must still surface its id/type
//     with an empty function part (callers rely on ID for correlation).
func TestConvertRawToToolCalls(t *testing.T) {
	cases := []struct {
		name     string
		raw      []interface{}
		wantLen  int
		checkIdx int
		wantID   string
		wantType string
		wantName string
		wantArgs string
	}{
		{
			name:    "nil raw yields empty slice",
			raw:     nil,
			wantLen: 0,
		},
		{
			name:     "non-map entries are skipped",
			raw:      []interface{}{"junk", 42, map[string]interface{}{"id": "tc1", "type": "function"}},
			wantLen:  1,
			checkIdx: 0,
			wantID:   "tc1",
			wantType: "function",
		},
		{
			name: "full shape maps id/type/function.name/arguments",
			raw: []interface{}{map[string]interface{}{
				"id":   "call_9",
				"type": "function",
				"function": map[string]interface{}{
					"name":      "search",
					"arguments": `{"q":"go"}`,
				},
			}},
			wantLen:  1,
			checkIdx: 0,
			wantID:   "call_9",
			wantType: "function",
			wantName: "search",
			wantArgs: `{"q":"go"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := convertRawToToolCalls(tc.raw)
			assert.Len(t, got, tc.wantLen)
			if tc.wantLen == 0 {
				return
			}
			c := got[tc.checkIdx]
			assert.Equal(t, tc.wantID, c.ID)
			assert.Equal(t, tc.wantType, c.Type)
			assert.Equal(t, tc.wantName, c.Function.Name)
			assert.Equal(t, tc.wantArgs, c.Function.Arguments)
		})
	}
}

// TestAddStructuredMessageRoundTrip locks structured persistence basics:
// role and content survive AddStructuredMessage → BuildPromptMessages.
// (Typed tool-call replay is covered by TestConvertRawToToolCalls on the
// conversion contract itself.)
func TestAddStructuredMessageRoundTrip(t *testing.T) {
	ctx := context.Background()
	mgr, err := NewMemoryManager(DefaultMemoryConfig())
	require.NoError(t, err)
	defer func() { _ = mgr.Stop(ctx) }()

	sessionID, err := mgr.CreateSession(ctx, "structured-user")
	require.NoError(t, err)

	userMsg := Message{Role: "user", Content: "find ares docs"}
	asstMsg := Message{Role: "assistant", Content: "calling search"}

	require.NoError(t, mgr.AddStructuredMessage(ctx, sessionID, userMsg))
	require.NoError(t, mgr.AddStructuredMessage(ctx, sessionID, asstMsg))

	promptMsgs, err := mgr.BuildPromptMessages(ctx, sessionID)
	require.NoError(t, err)
	require.NotEmpty(t, promptMsgs)

	sawUser, sawAssistant := false, false
	for _, m := range promptMsgs {
		if m.Role == "user" && m.Content == "find ares docs" {
			sawUser = true
		}
		if m.Role == "assistant" && m.Content == "calling search" {
			sawAssistant = true
		}
	}
	assert.True(t, sawUser, "user message lost in round-trip")
	assert.True(t, sawAssistant, "assistant message lost in round-trip")
}

// TestDeleteSessionRemovesMessages verifies DeleteSession makes the history
// unreachable: a follow-up Get on the deleted session returns no messages.
func TestDeleteSessionRemovesMessages(t *testing.T) {
	ctx := context.Background()
	mgr, err := NewMemoryManager(DefaultMemoryConfig())
	require.NoError(t, err)
	defer func() { _ = mgr.Stop(ctx) }()

	sessionID, err := mgr.CreateSession(ctx, "doomed")
	require.NoError(t, err)
	require.NoError(t, mgr.AddMessage(ctx, sessionID, "user", "bye"))

	require.NoError(t, mgr.DeleteSession(ctx, sessionID))

	// Reading a deleted session must fail with the SENTINEL
	// ErrSessionNotFound — never with an arbitrary error, and never with a
	// silent success masking stale data. The previous
	// `if err == nil { assert.Empty }` shape passed even when deletion broke
	// so badly that GetMessages errored arbitrarily — a swallowed failure mode.
	msgs, err := mgr.GetMessages(ctx, sessionID)
	require.ErrorIs(t, err, memctx.ErrSessionNotFound,
		"reading a deleted session must return the session-not-found sentinel")
	assert.Empty(t, msgs)
}

// TestCreateTaskWithIDAndUpdateOutput covers the task-tracking pair that the
// 0%-coverage report flagged: CreateTaskWithID persists a task under a
// caller-chosen id, and UpdateTaskOutput records its result for distillation.
func TestCreateTaskWithIDAndUpdateOutput(t *testing.T) {
	ctx := context.Background()
	mgr, err := NewMemoryManager(DefaultMemoryConfig())
	require.NoError(t, err)
	defer func() { _ = mgr.Stop(ctx) }()

	sessionID, err := mgr.CreateSession(ctx, "task-user")
	require.NoError(t, err)

	require.NoError(t, mgr.CreateTaskWithID(ctx, "task-e2e-1", sessionID, "user-x", "analyse logs"))
	require.NoError(t, mgr.UpdateTaskOutput(ctx, "task-e2e-1", "analysis complete: 3 findings"))

	// Round-trip the output through DistillTask (the manager's task read
	// path) so the test actually verifies persistence — an UpdateTaskOutput
	// that silently wrote "" would have passed the old assertions.
	distilled, err := mgr.DistillTask(ctx, "task-e2e-1")
	require.NoError(t, err)
	require.NotNil(t, distilled.Payload)
	assert.Equal(t, "analysis complete: 3 findings", distilled.Payload["output"],
		"UpdateTaskOutput must persist the output string")
	assert.Equal(t, "analyse logs", distilled.Payload["input"],
		"CreateTaskWithID must persist the input string")
}
