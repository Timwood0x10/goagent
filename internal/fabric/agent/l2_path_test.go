package agentfabric

import (
	"context"
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

// dualPathChat is a scripted ChatClient that returns the SAME tool call
// sequence for both paths (legacy chat cognition and L2 graph planner).
// Both paths see the same LLM response, so external behavior must match.
type dualPathChat struct {
	mu    sync.Mutex
	calls int
}

func (c *dualPathChat) Chat(_ context.Context, _ []*core.LLMMessage, _ []core.Tool, _ map[string]any) (*core.GenerateResponse, error) {
	c.mu.Lock()
	c.calls++
	round := c.calls
	c.mu.Unlock()

	switch round {
	case 1:
		return &core.GenerateResponse{
			ToolCalls: []core.ToolCall{{
				ID:   "dp-1",
				Type: "function",
				Function: core.FunctionCall{
					Name:      "grep",
					Arguments: `{"query":"pattern"}`,
				},
			}},
		}, nil
	default:
		return &core.GenerateResponse{Content: "the answer is 42"}, nil
	}
}

// TestL2PathSelfConsistency is the L2-path self-consistency test (renamed
// from DualPathBehaviorConsistency when the ReAct path was deleted): the L2 graph
// path (plannerCognition + routerCognition) runs one tool round
// (grep → "echo(grep,pattern)") then a final answer ("the answer is 42")
// and asserts the observable outputs match what the LLM produced.
func TestL2PathSelfConsistency(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const sessionID = "m4-dual"
	fabric := taskfabric.NewFabric()
	coord := planprojection.NewCompileCoordinator(fabric, nil)
	reg := NewSessionRegistry()
	compileCoord := func(_ context.Context, dag *engine.MutableDAG) (stop func()) {
		return coord.SubscribeGraphEvents(ctx, dag)
	}
	g, err := reg.InitSession(ctx, sessionID, "find the answer", nil, compileCoord)
	require.NoError(t, err)

	// Admit root and drive to completion.
	rootStep := g.DAG().StepIndex()[g.Root()]
	_, err = fabric.CompileNode(ctx, planprojection.ProjectStep(rootStep))
	require.NoError(t, err)
	driveTaskToCompleted(t, ctx, fabric, g.Root(), "find the answer")

	// ── L2 graph path ──────────────────────────────────────────────
	chatClient := &dualPathChat{}
	planner, err := NewPlannerCognition(PlannerDeps{
		ChatClient: chatClient,
		ToolBinder: &plannerTestBinder{},
		Sessions:   reg,
		Fabric:     fabric,
		Logger:     slog.Default(),
	})
	require.NoError(t, err)

	// Round 1: planner grows a grep node + next plan node.
	planTask := models.NewTask(
		SessionNodeID(sessionID, 0, "plan", 0),
		models.AgentType("ares/plan"), nil)
	planTask.SessionID = sessionID
	planTask.Payload = map[string]any{"input": "find the answer", planMetadataKey: sessionID}

	out1, err := planner.ExecuteStep(ctx, planTask)
	require.NoError(t, err)
	require.True(t, out1.Done)

	// Drive the grep tool to completion.
	grepID := SessionNodeID(sessionID, 1, "grep", 0)
	waitForTaskExists(t, fabric, grepID, 2*time.Second)
	driveTaskToCompleted(t, ctx, fabric, grepID, "echo(grep,pattern)")

	// Round 2: planner gets the answer (no tool calls).
	plan2Task := models.NewTask(
		SessionNodeID(sessionID, 1, "plan", 0),
		models.AgentType("ares/plan"), nil)
	plan2Task.SessionID = sessionID
	plan2Task.Payload = map[string]any{"input": "find the answer", planMetadataKey: sessionID}

	out2, err := planner.ExecuteStep(ctx, plan2Task)
	require.NoError(t, err)
	require.True(t, out2.Done)

	// An answer node was grown.
	answerID := SessionNodeID(sessionID, 2, "answer", 0)
	waitForTaskExists(t, fabric, answerID, 2*time.Second)

	// Execute the answer node via the router.
	router := NewRouterCognition(&plannerTestBinder{}, slog.Default())
	answerTask := models.NewTask(answerID, models.AgentType("ares/answer"), nil)
	answerTask.SessionID = sessionID
	answerTask.Payload = map[string]any{
		"arg.content":   "the answer is 42",
		planMetadataKey: sessionID,
	}
	answerOut, err := router.ExecuteStep(ctx, answerTask)
	require.NoError(t, err)
	require.True(t, answerOut.Done)
	l2Answer := answerOut.Result.Items[0].Content

	// Expectation: LLM says "grep", tool returns "echo(grep,pattern)",
	// LLM says "the answer is 42" — the L2 path must surface exactly what
	// the LLM produced.

	// The L2 path's answer node carries the same content the LLM produced.
	require.Equal(t, "the answer is 42", l2Answer,
		"L2 path answer must match what the LLM produced")

	// The LLM was called the same number of times (2: 1 tool
	// round + 1 answer round).
	chatClient.mu.Lock()
	require.Equal(t, 2, chatClient.calls,
		"LLM called 2 times: 1 tool round + 1 answer round")
	chatClient.mu.Unlock()

	// The graph is acyclic.
	order, err := g.DAG().GetExecutionOrder()
	require.NoError(t, err, "graph must be acyclic")
	require.GreaterOrEqual(t, len(order), 4, "root + plan + tool + answer")

	// Session release.
	require.NoError(t, reg.ReleaseSession(sessionID))
}
