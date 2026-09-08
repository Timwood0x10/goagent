package agentfabric

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/api/core"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/fabric/planprojection"
	"github.com/Timwood0x10/ares/internal/fabric/task"
	"github.com/Timwood0x10/ares/internal/fabric/task/workflow/engine"
)

// synthesisChat is the shared ChatClient fake for the M4.2 tests: it
// captures every call's messages, tool list and params, and answers from
// the scripted response (or error). The planner it is wired into never
// executes a quantum in these tests, so every captured call IS the answer
// body's synthesis call.
type synthesisChat struct {
	mu     sync.Mutex
	calls  int
	msgs   []*core.LLMMessage
	tools  []core.Tool
	params []map[string]any
	resp   *core.GenerateResponse
	err    error
}

func (c *synthesisChat) Chat(_ context.Context, msgs []*core.LLMMessage, tools []core.Tool, params map[string]any) (*core.GenerateResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.msgs = msgs
	c.tools = tools
	c.params = append(c.params, params)
	if c.err != nil {
		return nil, c.err
	}
	return c.resp, nil
}

// newSynthesisSession wires an L2 session whose graph already ran one tool round:
// root (completed, prompt on the envelope) → grep node (completed, output on
// the envelope) → plan node (ready) → content-less answer node (the node
// under test). This is the production shape a content-less answer grows
// from: the planner's final turn produced only tool calls the L1 constraints
// then skipped, or returned empty content.
func newSynthesisSession(t *testing.T, ctx context.Context, sessionID string) (*SessionRegistry, *taskfabric.Fabric, string) {
	t.Helper()

	fabric := taskfabric.NewFabric()
	coord := planprojection.NewCompileCoordinator(fabric, nil)
	reg := NewSessionRegistry()
	g, err := reg.InitSession(ctx, sessionID, "find the answer", nil,
		func(_ context.Context, dag *engine.MutableDAG) (stop func()) {
			return coord.SubscribeGraphEvents(ctx, dag)
		})
	require.NoError(t, err)

	// Admit + complete the root so the session prompt rides the envelope.
	rootStep := g.DAG().StepIndex()[g.Root()]
	_, err = fabric.CompileNode(ctx, planprojection.ProjectStep(rootStep))
	require.NoError(t, err)
	driveTaskToCompleted(t, ctx, fabric, g.Root(), "find the answer")

	// One executed tool round: grep with its output on the envelope.
	grepID := SessionNodeID(sessionID, 1, "grep", 0)
	require.NoError(t, g.AddToolNode(ctx, grepID, "grep",
		map[string]any{"query": "pattern", planMetadataKey: sessionID}, g.Root()))
	waitForTaskExists(t, fabric, grepID, 2*time.Second)
	driveTaskToCompleted(t, ctx, fabric, grepID, "echo(grep,pattern)")

	// A plan node the answer hangs off — same shape as production, where
	// the answer depends on the last plan node. The context walk must
	// skip it (non-tool) and still reach the grep round beneath.
	planID := SessionNodeID(sessionID, 1, "plan", 0)
	require.NoError(t, g.AddToolNode(ctx, planID, "plan",
		map[string]any{planMetadataKey: sessionID}, grepID))

	// The content-less terminal node under test.
	answerID := SessionNodeID(sessionID, 2, "answer", 0)
	require.NoError(t, g.AddToolNode(ctx, answerID, "answer",
		map[string]any{planMetadataKey: sessionID}, planID))

	return reg, fabric, answerID
}

// synthAnswerTask builds the answer node's task as the scheduler would restore
// it from the envelope: the node's Metadata (session_id, no content) merged
// with the projection's "input" plumbing.
func synthAnswerTask(sessionID, answerID string) *models.Task {
	task := models.NewTask(answerID, models.AgentType(answerAgentType), nil)
	task.SessionID = sessionID
	task.Payload = map[string]any{"input": "", planMetadataKey: sessionID}
	return task
}

// newSynthesisRouter builds the production-shaped router: a real plannerCognition
// over the session's registry + fabric (its ChatClient is the fake), so the
// router derives the answer synthesizer exactly as the cmd/ares wiring does.
func newSynthesisRouter(t *testing.T, reg *SessionRegistry, fabric *taskfabric.Fabric, chat *synthesisChat, logger *slog.Logger) Cognition {
	t.Helper()
	planner, err := NewPlannerCognition(PlannerDeps{
		ChatClient: chat,
		ToolBinder: &plannerTestBinder{},
		Sessions:   reg,
		Fabric:     fabric,
		Logger:     logger,
	})
	require.NoError(t, err)
	return NewRouterCognitionWithPlanner(&plannerTestBinder{}, planner, reg, logger)
}

// TestStampedContentBypassesSynthesis pins the normal path: when the
// planner stamped the LLM's final-turn content onto the answer node, that
// content passes through UNCHANGED and the synthesis path must not fire —
// no LLM call at all. This is the regression pin for the growAnswerNode
// contract (the final answer content rides the node).
func TestStampedContentBypassesSynthesis(t *testing.T) {
	ctx := context.Background()
	reg, fabric, answerID := newSynthesisSession(t, ctx, "synth-stamped")
	chat := &synthesisChat{resp: &core.GenerateResponse{Content: "synthesized must not run"}}
	router := newSynthesisRouter(t, reg, fabric, chat, slog.Default())

	task := synthAnswerTask("synth-stamped", answerID)
	task.Payload["arg.content"] = "the answer is 42"
	out, err := router.ExecuteStep(ctx, task)
	require.NoError(t, err)
	require.True(t, out.Done)
	require.Equal(t, "the answer is 42", out.Result.Items[0].Content,
		"stamped content passes through unchanged")

	chat.mu.Lock()
	defer chat.mu.Unlock()
	require.Equal(t, 0, chat.calls, "stamped content must bypass the synthesizer entirely")
}

// TestContentlessNodeSynthesizesFromPredecessors is the M4.2
// acceptance test: a content-less terminal node with full wiring makes
// exactly ONE LLM call — with NO tools advertised — over the predecessor
// history (root prompt + the grep round rebuilt as an assistant/tool pair),
// and the synthesized content becomes the session's answer.
func TestContentlessNodeSynthesizesFromPredecessors(t *testing.T) {
	ctx := context.Background()
	const sessionID = "synth-synth"
	reg, fabric, answerID := newSynthesisSession(t, ctx, sessionID)
	chat := &synthesisChat{resp: &core.GenerateResponse{Content: "the answer is 42"}}
	router := newSynthesisRouter(t, reg, fabric, chat, slog.Default())

	out, err := router.ExecuteStep(ctx, synthAnswerTask(sessionID, answerID))
	require.NoError(t, err)
	require.True(t, out.Done, "a synthesized answer still terminates the session in one quantum")
	require.Equal(t, "the answer is 42", out.Result.Items[0].Content,
		"the synthesized content is the session answer")

	_, err = reg.GetSession(sessionID)
	require.ErrorIs(t, err, ErrSessionNotFound, "the synthesized terminal node still releases the session")

	chat.mu.Lock()
	defer chat.mu.Unlock()
	require.Equal(t, 1, chat.calls, "the synthesizer makes exactly one LLM call")
	require.Empty(t, chat.tools, "the synthesis call must advertise NO tools: the session is terminating")
	require.Empty(t, chat.params[0], "no param overrides ride the terminal call")

	grepID := SessionNodeID(sessionID, 1, "grep", 0)
	msgs := chat.msgs
	require.Len(t, msgs, 4, "prior-less shape: root prompt + assistant/tool pair + synthesis instruction")
	require.Equal(t, "user", msgs[0].Role)
	require.Equal(t, "find the answer", msgs[0].Content, "the root prompt leads the context")
	require.Equal(t, "assistant", msgs[1].Role)
	require.Len(t, msgs[1].ToolCalls, 1)
	require.Equal(t, "grep", msgs[1].ToolCalls[0].Function.Name)
	require.Contains(t, msgs[1].ToolCalls[0].Function.Arguments, "pattern")
	require.Equal(t, roleTool, msgs[2].Role)
	require.Equal(t, "echo(grep,pattern)", msgs[2].Content,
		"the predecessor's envelope output must reach the LLM")
	require.Equal(t, grepID, msgs[2].ToolCallID, "the tool message links back to its node")
	require.Equal(t, "user", msgs[3].Role)
	require.Equal(t, answerSynthesisInstruction, msgs[3].Content,
		"the synthesis instruction closes the context")
}

// TestSynthesisFailureEmitsGapBody pins the failure contract: when the
// synthesis call fails (LLM error) or returns empty content, the quantum
// still COMPLETES with the gap body — returning an error instead would burn
// the fabric's retry budget on an LLM that is failing for every session and
// hold the session open for nothing. The session is released either way:
// the terminal node is the session's only exit.
func TestSynthesisFailureEmitsGapBody(t *testing.T) {
	failureModes := []struct {
		name string
		chat *synthesisChat
	}{
		{"llm_error", &synthesisChat{err: fmt.Errorf("provider down")}},
		{"empty_response", &synthesisChat{resp: &core.GenerateResponse{}}},
	}
	for _, fm := range failureModes {
		t.Run(fm.name, func(t *testing.T) {
			ctx := context.Background()
			var logged bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelWarn}))
			reg, fabric, answerID := newSynthesisSession(t, ctx, "synth-fail")
			router := newSynthesisRouter(t, reg, fabric, fm.chat, logger)

			out, err := router.ExecuteStep(ctx, synthAnswerTask("synth-fail", answerID))
			require.NoError(t, err, "a failed synthesis is a degraded answer, not a quantum failure")
			require.True(t, out.Done, "the session terminates: retrying a failing LLM burns budget")
			require.Equal(t, unansweredBody, out.Result.Items[0].Content)

			_, err = reg.GetSession("synth-fail")
			require.ErrorIs(t, err, ErrSessionNotFound,
				"the terminal node releases the session even on synthesis failure")

			fm.chat.mu.Lock()
			require.Equal(t, 1, fm.chat.calls, "synthesis was attempted exactly once")
			fm.chat.mu.Unlock()
			require.Contains(t, logged.String(), "answer synthesis",
				"the degraded output must be visible to operations, not silent")
		})
	}
}

// TestNoSynthesisWiringKeepsGapBody pins the zero-value contract: a
// router whose planner is nil has NO synthesizer, and a content-less answer
// node behaves exactly as before M4.2 — gap body, the pinned warning logged,
// session released. Tests and degraded wiring rely on this path.
func TestNoSynthesisWiringKeepsGapBody(t *testing.T) {
	ctx := context.Background()
	var logged bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelWarn}))
	reg, _, answerID := newSynthesisSession(t, ctx, "synth-nowire")

	router := NewRouterCognitionWithPlanner(&plannerTestBinder{}, nil, reg, logger)
	out, err := router.ExecuteStep(ctx, synthAnswerTask("synth-nowire", answerID))
	require.NoError(t, err)
	require.True(t, out.Done, "the session still terminates: a missing summary is not a task failure")
	require.Equal(t, unansweredBody, out.Result.Items[0].Content)
	require.Contains(t, logged.String(), "no summarizer is wired",
		"the unwired path keeps today's observable gap message")

	_, err = reg.GetSession("synth-nowire")
	require.ErrorIs(t, err, ErrSessionNotFound, "release semantics are unchanged by M4.2")
}
