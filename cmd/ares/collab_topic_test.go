package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/agents/base"
	"github.com/Timwood0x10/ares/internal/agents/sub"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/fabric/agent"
	"github.com/Timwood0x10/ares/internal/fabric/planprojection"
	"github.com/Timwood0x10/ares/internal/fabric/task"
	llmcore "github.com/Timwood0x10/ares/internal/llmcore"
)

// collabStubAgent satisfies sub.Agent minimally: the session-routed
// collaboration path never calls Execute/ExecuteStep on it (work runs as an
// L2 session through the fabric scheduler), but the interface is required by
// wireEvolutionIPC's signature.
type collabStubAgent struct {
	id  string
	typ models.AgentType
}

func (a *collabStubAgent) ID() string                    { return a.id }
func (a *collabStubAgent) Type() models.AgentType        { return a.typ }
func (a *collabStubAgent) Start(_ context.Context) error { return nil }
func (a *collabStubAgent) Stop(_ context.Context) error  { return nil }
func (a *collabStubAgent) Status() models.AgentStatus    { return models.AgentStatusReady }
func (a *collabStubAgent) Process(_ context.Context, _ any) (any, error) {
	return nil, nil // session-routed path never calls this on the stub
}
func (a *collabStubAgent) ProcessStream(_ context.Context, _ any) (<-chan base.AgentEvent, error) {
	return nil, nil // ditto
}
func (a *collabStubAgent) Execute(_ context.Context, _ *models.Task) (*models.TaskResult, error) {
	return nil, nil // session-routed path never calls this on the stub
}
func (a *collabStubAgent) ExecuteStep(_ context.Context, _ *models.Task) (*sub.StepOutcome, error) {
	return nil, nil // session-routed path never calls this on the stub
}

// errChat is a ChatClient that always fails (drives the plan task to FAILED).
type errChat struct{ err error }

func (e *errChat) Chat(context.Context, []*llmcore.LLMMessage, []llmcore.Tool, map[string]any) (*llmcore.GenerateResponse, error) {
	return nil, e.err
}

// newL2TopicKernel builds a kernelHandle whose scheduler drains a scripted
// planner/router L2 stack (the only execution path). Sessions admitted
// through submitPeerTask terminate without any real LLM.
func newL2TopicKernel(t *testing.T, ctx context.Context, chat agentfabric.ChatClient) *kernelHandle {
	t.Helper()
	fabric := taskfabric.NewFabric()
	coord := planprojection.NewCompileCoordinator(fabric, nil)
	reg := agentfabric.NewSessionRegistry()
	binder := &canaryBinder{}
	planner, err := agentfabric.NewPlannerCognition(agentfabric.PlannerDeps{
		ChatClient: chat,
		ToolBinder: binder,
		Sessions:   reg,
		Fabric:     fabric,
	})
	if err != nil {
		t.Fatalf("planner: %v", err)
	}
	agents := agentfabric.NewFabric()
	if _, err := agents.Spawn(ctx, agentfabric.SpawnSpec{
		Identity:     "l2-peer",
		Capabilities: []string{"ares/root", "ares/plan", "ares/answer", "tool/echo"},
		CognitionFactory: func([]string) agentfabric.Cognition {
			return agentfabric.NewRouterCognitionWithPlanner(binder, planner, reg, nil)
		},
	}); err != nil {
		t.Fatalf("spawn l2-peer: %v", err)
	}
	sched := NewKernelScheduler(fabric, map[string]CapabilityExecutor{}, newLoadTracker())
	sched.PollInterval = 10 * time.Millisecond
	sched.WithAgentFabric(agents)
	go sched.Run(ctx)
	return &kernelHandle{fabric: fabric, scheduler: sched, sessionReg: reg, compileCoord: coord}
}

// TestCollabTopicAnsweredByL2Session locks the IPC contract: with the
// peer kernel wired, an IPC collaboration topic message executes as an L2
// SESSION and the reply preserves the TaskResult shape — protocol unchanged,
// engine unified on the single path.
func TestCollabTopicAnsweredByL2Session(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	kh := newL2TopicKernel(t, ctx, &canaryScript{responses: []llmcore.GenerateResponse{{Content: "session says hi"}}})

	bridge, err := wireEvolutionIPC(
		[]sub.Agent{&collabStubAgent{id: "peer-research", typ: "research"}},
		nil, nil, kh,
	)
	if err != nil {
		t.Fatalf("wireEvolutionIPC: %v", err)
	}

	reply, err := bridge.ipc.Bus().Request(ctx, "coordinator", "peer-research",
		"delegate-task",
		map[string]any{taskIDKey: "tk-77", "payload": map[string]any{"input": "do it"}},
		30*time.Second)
	if err != nil {
		t.Fatalf("delegate-task via bus: %v", err)
	}

	// Protocol preserved: reply payload is still a TaskResult.
	res, ok := reply.Payload.(*models.TaskResult)
	if !ok {
		t.Fatalf("reply payload type = %T, want *models.TaskResult", reply.Payload)
	}
	if res.Reason != "session says hi" {
		t.Fatalf("reply reason = %q, want the session answer", res.Reason)
	}
	// Reply topic convention unchanged.
	if reply.Topic != "delegate-task-reply" {
		t.Fatalf("reply topic = %q", reply.Topic)
	}
	// The question ran as an admitted session that was released on
	// completion (no leak: zero live sessions remain).
	if n := len(kh.sessionReg.SessionIDs()); n != 0 {
		t.Fatalf("want 0 live sessions after answered ask (released), got %d", n)
	}
}

// TestCollabTopicSessionsUniquePerInvocation locks the IPC-path session
// uniqueness (successor of the run-id fix): two collaboration requests
// sharing a task id must still produce DISTINCT sessions — otherwise the
// second request's plan task would hit ErrTaskExists. Sequential invocations
// with the SAME task id are enough: a deterministic id would repeat.
func TestCollabTopicSessionsUniquePerInvocation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	kh := newL2TopicKernel(t, ctx, &canaryScript{responses: []llmcore.GenerateResponse{{Content: "hi"}}})

	bridge, err := wireEvolutionIPC(
		[]sub.Agent{&collabStubAgent{id: "peer-research", typ: "research"}},
		nil, nil, kh,
	)
	if err != nil {
		t.Fatalf("wireEvolutionIPC: %v", err)
	}

	// Two sequential requests carrying the SAME task id.
	const sharedTaskID = "tk-dup"
	for i := 0; i < 2; i++ {
		reply, rerr := bridge.ipc.Bus().Request(ctx, "coordinator", "peer-research",
			topicDelegateTask,
			map[string]any{taskIDKey: sharedTaskID, "payload": map[string]any{"input": "do it"}},
			30*time.Second)
		if rerr != nil {
			t.Fatalf("request %d with shared task id must succeed (a collision would surface here): %v", i, rerr)
		}
		if reply == nil {
			t.Fatalf("request %d: nil reply", i)
		}
	}

	sessions := kh.sessionReg.SessionIDs()
	// Successor of the run-id uniqueness property: session ids carry a
	// process-wide atomic sequence (ipc-sess-<taskID>-<n>), so two requests
	// sharing a task id can neither collide on task creation (both replies
	// above succeeded) nor leak sessions afterwards.
	if len(sessions) != 0 {
		t.Fatalf("expected 0 live sessions after 2 answered asks (released), got %d: %v", len(sessions), sessions)
	}
	// Pin observable uniqueness: the shared task id produced two DISTINCT
	// session scopes (the sequence suffix is process-global, so match the
	// scope pattern, not fixed numbers).
	scopes := map[string]bool{}
	for _, id := range kh.fabric.IDs() {
		if idx := strings.Index(id, "ipc-sess-tk-dup-"); idx >= 0 {
			end := idx + len("ipc-sess-tk-dup-")
			num := ""
			for end < len(id) && id[end] >= '0' && id[end] <= '9' {
				num += string(id[end])
				end++
			}
			if num != "" {
				scopes["ipc-sess-tk-dup-"+num] = true
			}
		}
	}
	if len(scopes) != 2 {
		t.Fatalf("both session scopes must leave fabric traces (uniqueness regressed): ids=%v", kh.fabric.IDs())
	}
}

// TestCollabTopicPlannerFailurePropagatesError locks the IPC error path: when
// the session's plan fails, executeCollabViaKernel must return an ERROR that
// the bus surfaces to the caller — not a silent nil reply that would let a
// leader treat a failed delegation as success.
func TestCollabTopicPlannerFailurePropagatesError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	kh := newL2TopicKernel(t, ctx, &errChat{err: errors.New("llm down")})

	bridge, err := wireEvolutionIPC(
		[]sub.Agent{&collabStubAgent{id: "peer-flaky", typ: "flaky"}},
		nil, nil, kh,
	)
	if err != nil {
		t.Fatalf("wireEvolutionIPC: %v", err)
	}

	_, rerr := bridge.ipc.Bus().Request(ctx, "coordinator", "peer-flaky",
		topicDelegateTask,
		map[string]any{taskIDKey: "tk-fail", "payload": map[string]any{"input": "x"}},
		30*time.Second)
	if rerr == nil {
		t.Fatal("a failed delegation must propagate as an error, not a silent reply")
	}
}

// Interface-conformance guards for the stub used by wireEvolutionIPC.
var (
	_ base.Agent = (*collabStubAgent)(nil)
	_ sub.Agent  = (*collabStubAgent)(nil)
)
