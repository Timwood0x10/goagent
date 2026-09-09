package agentfabric

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/fabric/planprojection"
	taskfabric "github.com/Timwood0x10/ares/internal/fabric/task"
	"github.com/Timwood0x10/ares/internal/fabric/task/workflow/engine"
	llmcore "github.com/Timwood0x10/ares/internal/llmcore"
)

// The M4 cost channel's cognition half: every LLM-carrying quantum reports
// its token usage on the StepOutcome result metadata (input_tokens/
// output_tokens — the key contract the kernel scheduler's re-wrap path
// accumulates into the checkpoint envelope). The depth-guard quantum makes
// no LLM call and reports nothing.

// usageChat returns one scripted tool-call response with usage, then plain
// content with usage.
type usageChat struct {
	calls int
}

func (c *usageChat) Chat(_ context.Context, _ []*llmcore.LLMMessage, _ []llmcore.Tool, _ map[string]any) (*llmcore.GenerateResponse, error) {
	c.calls++
	if c.calls == 1 {
		return &llmcore.GenerateResponse{
			ToolCalls: []llmcore.ToolCall{{
				ID:       "tc-u1",
				Type:     "function",
				Function: llmcore.FunctionCall{Name: "grep", Arguments: `{"query":"x"}`},
			}},
			Usage: llmcore.TokenUsage{PromptTokens: 100, CompletionTokens: 40, TotalTokens: 140},
		}, nil
	}
	return &llmcore.GenerateResponse{
		Content: "done",
		Usage:   llmcore.TokenUsage{PromptTokens: 25, CompletionTokens: 15, TotalTokens: 40},
	}, nil
}

func TestPlannerQuantumStampsTokenUsage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const sessionID = "token-usage-test"
	fabric := taskfabric.NewFabric()
	coord := planprojection.NewCompileCoordinator(fabric, nil)
	reg := NewSessionRegistry()
	compileCoord := func(_ context.Context, dag *engine.MutableDAG) (stop func()) {
		return coord.SubscribeGraphEvents(ctx, dag)
	}
	g, err := reg.InitSession(ctx, sessionID, "find the answer", nil, compileCoord)
	require.NoError(t, err)

	rootStep := g.DAG().StepIndex()[g.Root()]
	_, err = fabric.CompileNode(ctx, planprojection.ProjectStep(rootStep))
	require.NoError(t, err)
	driveTaskToCompleted(t, ctx, fabric, g.Root(), "find the answer")

	planner, err := NewPlannerCognition(PlannerDeps{
		ChatClient: &usageChat{},
		ToolBinder: &plannerTestBinder{},
		Sessions:   reg,
		Fabric:     fabric,
	})
	require.NoError(t, err)

	initialPlanID := SessionNodeID(sessionID, 0, "plan", 0)
	planTask := models.NewTask(initialPlanID, models.AgentType(planAgentType), nil)
	planTask.SessionID = sessionID
	planTask.Payload = map[string]any{
		"input":         "find the answer",
		planMetadataKey: sessionID,
	}

	out, err := planner.ExecuteStep(ctx, planTask)
	require.NoError(t, err)
	require.NotNil(t, out.Result)

	// The growth quantum carried the LLM response's usage into its result
	// metadata — the cross-package key contract the scheduler accumulates.
	require.NotNil(t, out.Result.Metadata, "the LLM-carrying quantum must report usage metadata")
	assert.Equal(t, 100, out.Result.Metadata["input_tokens"])
	assert.Equal(t, 40, out.Result.Metadata["output_tokens"])
}

func TestTokenUsageMetadataDegraded(t *testing.T) {
	assert.Nil(t, tokenUsageMetadata(nil), "nil response → nil metadata")
	assert.Nil(t, tokenUsageMetadata(&llmcore.GenerateResponse{}), "zero usage → nil metadata")
	assert.NotNil(t, tokenUsageMetadata(&llmcore.GenerateResponse{
		Usage: llmcore.TokenUsage{PromptTokens: 1},
	}), "any positive usage → metadata present")
}
