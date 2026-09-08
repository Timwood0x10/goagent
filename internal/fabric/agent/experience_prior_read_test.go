package agentfabric

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/agents"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/fabric/planprojection"
	"github.com/Timwood0x10/ares/internal/fabric/task"
	"github.com/Timwood0x10/ares/internal/fabric/task/workflow/engine"
)

// priorReadPrompt is the session prompt every prior-read harness sessions use.
const priorReadPrompt = "diagnose the ffi crash"

// newPriorReadHarness wires ONE agent-fabric agent (optionally carrying a distilled
// experience prior) to a planner over a live L2 session, mirroring the
// production cmd/ares wiring: the shared router dispatches ares/plan to a
// planner whose AgentFabric is the same fabric the agent was spawned into.
// Returns the agent, the planner (for direct-call subtests) and the recording
// chat so tests can assert the assembled LLM messages.
func newPriorReadHarness(
	t *testing.T,
	ctx context.Context,
	sessionID string,
	prior any,
	strategy agents.StrategySource,
) (*Agent, Cognition, *recordingChat) {
	t.Helper()

	// L2 session over the task fabric: root admitted + completed so the
	// prompt rides the envelope (same shape as the strategy-steering test).
	tf := taskfabric.NewFabric()
	coord := planprojection.NewCompileCoordinator(tf, nil)
	reg := NewSessionRegistry()
	g, err := reg.InitSession(ctx, sessionID, priorReadPrompt, nil,
		func(_ context.Context, dag *engine.MutableDAG) (stop func()) {
			return coord.SubscribeGraphEvents(ctx, dag)
		})
	require.NoError(t, err)
	rootStep := g.DAG().StepIndex()[g.Root()]
	_, err = tf.CompileNode(ctx, planprojection.ProjectStep(rootStep))
	require.NoError(t, err)
	driveTaskToCompleted(t, ctx, tf, g.Root(), priorReadPrompt)

	// The agent fabric: one spawned peer wired to the shared L2 router.
	af := NewFabric()
	chat := &recordingChat{}
	planner, err := NewPlannerCognition(PlannerDeps{
		ChatClient:     chat,
		ToolBinder:     &plannerTestBinder{},
		Sessions:       reg,
		Fabric:         tf,
		StrategySource: strategy,
		AgentFabric:    af,
		Logger:         slog.Default(),
	})
	require.NoError(t, err)
	router := NewRouterCognitionWithPlanner(&plannerTestBinder{}, planner, reg, slog.Default())
	a, err := af.Spawn(ctx, SpawnSpec{
		Identity: "ffi-expert",
		// The declared capability matches the plan quantum the test runs.
		Capabilities: []string{"ares/plan"},
		CognitionFactory: func([]string) Cognition {
			return router
		},
		ExperiencePrior: prior,
	})
	require.NoError(t, err)

	return a, planner, chat
}

// priorReadPlanTask builds the first plan quantum's task for the given session.
func priorReadPlanTask(sessionID string) *models.Task {
	task := models.NewTask(SessionNodeID(sessionID, 0, "plan", 0), models.AgentType("ares/plan"), nil)
	task.SessionID = sessionID
	task.Payload = map[string]any{"input": priorReadPrompt, planMetadataKey: sessionID}
	return task
}

// readPrior mirrors the shape cmd/ares loadExperiencePrior writes at spawn:
// a {type, problem, solution, constraints} map from the experience store.
func readPrior() map[string]any {
	return map[string]any{
		"type":        "ffi-safety",
		"problem":     "segfault when passing unsized types across the ABI",
		"solution":    "prefer checked accessors over raw FFI pointers",
		"constraints": []string{"never cross the ABI boundary with unsized types"},
	}
}

// TestExecutingAgentPriorInjectedBeforeStrategy locks the READ side of
// the experience loop end to end: the agent spawned with an ExperiencePrior
// executes a plan quantum through Agent.ExecuteStep (the identity stamp) →
// router → planner → assembleContext, and the distilled prior rides as the
// FIRST system message — ahead of the root prompt and of the strategy
// template (experience informs early, strategy steers late).
func TestExecutingAgentPriorInjectedBeforeStrategy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a, _, chat := newPriorReadHarness(t, ctx, "prior-join", readPrior(),
		&stubStrategySource{st: &agents.ActiveStrategy{
			ID:     "s-prior",
			Prompt: "PREFER GREP OVER READ",
		}})

	out, err := a.ExecuteStep(ctx, priorReadPlanTask("prior-join"))
	require.NoError(t, err)
	require.True(t, out.Done, "prior-carrying quantum must still complete")

	chat.mu.Lock()
	defer chat.mu.Unlock()
	require.Len(t, chat.msgs, 1, "LLM called once")
	msgs := chat.msgs[0]
	require.Len(t, msgs, 3, "prior + root prompt + strategy")

	// The prior leads: system role, labeled, carrying the distilled content.
	require.Equal(t, "system", msgs[0].Role)
	require.Contains(t, msgs[0].Content, experiencePriorPrefix)
	require.Contains(t, msgs[0].Content, "prefer checked accessors over raw FFI pointers",
		"the prior's distilled solution must reach the LLM")
	require.Contains(t, msgs[0].Content, "segfault when passing unsized types",
		"the prior's distilled problem must reach the LLM")

	// The session prompt follows, unchanged.
	require.Equal(t, "user", msgs[1].Role)
	require.Equal(t, priorReadPrompt, msgs[1].Content)

	// The strategy template still rides after the prior: steering, not
	// replaced by experience.
	require.Equal(t, "system", msgs[2].Role)
	require.Contains(t, msgs[2].Content, "PREFER GREP OVER READ")
}

// TestAbsentPriorLeavesMessagesUnchanged pins the zero-value contract:
// a missing prior (no fabric wired, no executing-agent stamp, or an agent
// spawned without a prior) must leave the assembled messages IDENTICAL to
// the pre-M4.3 shape — [user root prompt] for a first quantum.
func TestAbsentPriorLeavesMessagesUnchanged(t *testing.T) {
	t.Run("nil agent fabric", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		tf := taskfabric.NewFabric()
		coord := planprojection.NewCompileCoordinator(tf, nil)
		reg := NewSessionRegistry()
		g, err := reg.InitSession(ctx, "prior-nofab", priorReadPrompt, nil,
			func(_ context.Context, dag *engine.MutableDAG) (stop func()) {
				return coord.SubscribeGraphEvents(ctx, dag)
			})
		require.NoError(t, err)
		rootStep := g.DAG().StepIndex()[g.Root()]
		_, err = tf.CompileNode(ctx, planprojection.ProjectStep(rootStep))
		require.NoError(t, err)
		driveTaskToCompleted(t, ctx, tf, g.Root(), priorReadPrompt)

		chat := &recordingChat{}
		planner, err := NewPlannerCognition(PlannerDeps{
			ChatClient: chat,
			ToolBinder: &plannerTestBinder{},
			Sessions:   reg,
			Fabric:     tf,
			Logger:     slog.Default(),
		})
		require.NoError(t, err)

		out, err := planner.ExecuteStep(ctx, priorReadPlanTask("prior-nofab"))
		require.NoError(t, err)
		require.True(t, out.Done)

		chat.mu.Lock()
		defer chat.mu.Unlock()
		require.Len(t, chat.msgs, 1)
		require.Len(t, chat.msgs[0], 1, "no prior message without an agent fabric")
		require.Equal(t, "user", chat.msgs[0][0].Role)
		require.Equal(t, priorReadPrompt, chat.msgs[0][0].Content)
	})

	t.Run("no executing-agent stamp", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// Fabric wired and a prior-carrying agent exists, but the planner is
		// invoked directly (no Agent.ExecuteStep stamp) — the shape every
		// pre-M4.3 planner test uses. No join key, no injection.
		_, planner, chat := newPriorReadHarness(t, ctx, "prior-nostamp", readPrior(), nil)

		out, err := planner.ExecuteStep(ctx, priorReadPlanTask("prior-nostamp"))
		require.NoError(t, err)
		require.True(t, out.Done)

		chat.mu.Lock()
		defer chat.mu.Unlock()
		require.Len(t, chat.msgs, 1)
		require.Len(t, chat.msgs[0], 1, "no prior message without the executing-agent stamp")
		require.Equal(t, "user", chat.msgs[0][0].Role)
		require.Equal(t, priorReadPrompt, chat.msgs[0][0].Content)
	})

	t.Run("agent without prior", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		a, _, chat := newPriorReadHarness(t, ctx, "prior-blank", nil, nil)

		out, err := a.ExecuteStep(ctx, priorReadPlanTask("prior-blank"))
		require.NoError(t, err)
		require.True(t, out.Done)

		chat.mu.Lock()
		defer chat.mu.Unlock()
		require.Len(t, chat.msgs, 1)
		require.Len(t, chat.msgs[0], 1, "agent spawned without a prior injects nothing")
		require.Equal(t, "user", chat.msgs[0][0].Role)
		require.Equal(t, priorReadPrompt, chat.msgs[0][0].Content)
	})
}

// TestLargePriorTruncated verifies the guard: a pathologically large
// prior (a whole-log dump stored as an experience) is truncated to
// maxExperiencePriorRunes so it cannot crowd out the session context.
func TestLargePriorTruncated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	huge := strings.Repeat("x", maxExperiencePriorRunes*2)
	a, _, chat := newPriorReadHarness(t, ctx, "prior-huge", huge, nil)

	out, err := a.ExecuteStep(ctx, priorReadPlanTask("prior-huge"))
	require.NoError(t, err)
	require.True(t, out.Done)

	chat.mu.Lock()
	defer chat.mu.Unlock()
	require.Len(t, chat.msgs, 1)
	msgs := chat.msgs[0]
	require.Len(t, msgs, 2, "truncated prior + root prompt")
	require.Equal(t, "system", msgs[0].Role)
	require.Contains(t, msgs[0].Content, "...", "truncated prior must carry the ellipsis marker")
	require.LessOrEqual(t, len([]rune(msgs[0].Content)),
		len(experiencePriorPrefix)+maxExperiencePriorRunes,
		"prior message is bounded by the cap")
}

// TestExecutingAgentStampIsQuantumScoped locks the stamp's durability
// contract: Agent.ExecuteStep stamps a COPY of the payload map — the caller's
// map (shared with the durable checkpoint envelope via ToModelTask) must stay
// clean, or a preemption would attribute later quanta to a stale agent. A nil
// payload must not panic.
func TestExecutingAgentStampIsQuantumScoped(t *testing.T) {
	ctx := context.Background()
	af := NewFabric()

	var mu sync.Mutex
	var got *models.Task
	a, err := af.Spawn(ctx, SpawnSpec{
		Identity:     "stamp-agent",
		Capabilities: []string{"code"},
		CognitionFactory: func([]string) Cognition {
			return CognitionFunc(func(_ context.Context, task *models.Task) (*StepOutcome, error) {
				mu.Lock()
				got = task
				mu.Unlock()
				res := models.NewTaskResult(task.TaskID, task.AgentType)
				res.SetSuccess(nil, "ok")
				return &StepOutcome{Done: true, Result: res}, nil
			})
		},
	})
	require.NoError(t, err)

	orig := map[string]any{"input": "must stay durable"}
	task := models.NewTask("t-stamp", models.AgentType("code"), nil)
	task.Payload = orig
	out, err := a.ExecuteStep(ctx, task)
	require.NoError(t, err)
	require.True(t, out.Done)

	mu.Lock()
	stamped := got
	mu.Unlock()
	require.Equal(t, "stamp-agent", stamped.Payload[executingAgentKey],
		"the cognition must see the executing agent's identity")
	require.NotContains(t, orig, executingAgentKey,
		"the stamp must never leak into the caller's (durable-envelope-shared) payload map")

	// A task with no payload map: the stamp creates one instead of panicking.
	nilPayload := models.NewTask("t-nil", models.AgentType("code"), nil)
	nilPayload.Payload = nil
	_, err = a.ExecuteStep(ctx, nilPayload)
	require.NoError(t, err)
	mu.Lock()
	stamped = got
	mu.Unlock()
	require.Equal(t, "stamp-agent", stamped.Payload[executingAgentKey],
		"nil payload is stamped onto a fresh map")
}
