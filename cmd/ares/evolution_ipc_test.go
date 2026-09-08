package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/agentipc"
	"github.com/Timwood0x10/ares/internal/agents/base"
	"github.com/Timwood0x10/ares/internal/agents/sub"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/runtime/protocol/ahp"
)

// fakeExecuteAgent satisfies sub.Agent with a recording Execute capability —
// the surface wireEvolutionIPC registers for collaboration topics. (The peer
// SendMessage surface was removed with the sub.Agent message queue; only
// collaboration topics deliver.)
type fakeExecuteAgent struct {
	id       string
	mu       sync.Mutex
	executed []string
}

func (a *fakeExecuteAgent) ID() string                    { return a.id }
func (a *fakeExecuteAgent) Type() models.AgentType        { return "specialist" }
func (a *fakeExecuteAgent) Start(_ context.Context) error { return nil }
func (a *fakeExecuteAgent) Stop(_ context.Context) error  { return nil }
func (a *fakeExecuteAgent) Status() models.AgentStatus    { return models.AgentStatusReady }
func (a *fakeExecuteAgent) Process(_ context.Context, _ any) (any, error) {
	return nil, nil
}
func (a *fakeExecuteAgent) ProcessStream(_ context.Context, _ any) (<-chan base.AgentEvent, error) {
	return nil, nil
}
func (a *fakeExecuteAgent) ExecuteStep(_ context.Context, _ *models.Task) (*sub.StepOutcome, error) {
	return nil, nil
}

// Execute satisfies the collaboration execute capability: records the task
// and returns a canned result.
func (a *fakeExecuteAgent) Execute(_ context.Context, task *models.Task) (*models.TaskResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.executed = append(a.executed, task.TaskID)
	return &models.TaskResult{
		TaskID:    task.TaskID,
		AgentType: task.TaskType,
		Success:   true,
		Metadata:  map[string]any{"executed": true, "on": a.id},
	}, nil
}

func (a *fakeExecuteAgent) executions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.executed))
	copy(out, a.executed)
	return out
}

// TestExecuteCollaboration_RunsTaskAndReturnsReply verifies the core helper
// behind the collaboration wiring: a delegate/pipeline/orchestrate message
// is bridged into a *models.Task, executed on the target agent, and the result
// is returned as the request/reply reply with the correlation id preserved.
func TestExecuteCollaboration_RunsTaskAndReturnsReply(t *testing.T) {
	ctx := context.Background()
	agent := &fakeExecuteAgent{id: "specialist-1"}

	reply, err := executeCollaboration(ctx, agent.id, &agentipc.Message{
		From:          "leader",
		CorrelationID: "corr-1",
		Topic:         topicDelegateTask,
		Payload: map[string]any{
			taskIDKey:  "t-delegate",
			"payload":  map[string]any{"question": "q1"},
			"payload2": "ignored",
		},
	}, agent.Execute)
	if err != nil {
		t.Fatalf("executeCollaboration: %v", err)
	}
	if reply == nil {
		t.Fatal("reply must not be nil")
	}
	if reply.CorrelationID != "corr-1" {
		t.Fatalf("reply correlation = %q, want corr-1", reply.CorrelationID)
	}
	if reply.From != "specialist-1" || reply.To != "leader" {
		t.Fatalf("reply from/to = %q/%q, want specialist-1/leader", reply.From, reply.To)
	}
	res, ok := reply.Payload.(*models.TaskResult)
	if !ok {
		t.Fatalf("reply payload = %T, want *models.TaskResult", reply.Payload)
	}
	if !res.Success || res.TaskID != "t-delegate" || res.AgentType != models.AgentType("specialist-1") {
		t.Fatalf("result mismatch: %+v", res)
	}
}

// TestExecuteCollaboration_RejectsMissingTaskID verifies a collaboration
// request without the task id is rejected, not silently executed.
func TestExecuteCollaboration_RejectsMissingTaskID(t *testing.T) {
	ctx := context.Background()
	agent := &fakeExecuteAgent{id: "specialist-1"}

	_, err := executeCollaboration(ctx, agent.id, &agentipc.Message{
		Topic:   topicDelegateTask,
		Payload: map[string]any{"payload": map[string]any{}},
	}, agent.Execute)
	if err == nil {
		t.Fatal("missing task_id must error")
	}
}

// TestExecuteCollaboration_RejectsNonMapPayload verifies a non-map
// collaboration payload (e.g. a raw string) is rejected.
func TestExecuteCollaboration_RejectsNonMapPayload(t *testing.T) {
	ctx := context.Background()
	agent := &fakeExecuteAgent{id: "specialist-1"}

	_, err := executeCollaboration(ctx, agent.id, &agentipc.Message{
		Topic:   topicPipelineStage,
		Payload: "not-a-map",
	}, agent.Execute)
	if err == nil {
		t.Fatal("non-map payload must error")
	}
}

// TestExecuteCollaboration_NoExecuteCapability verifies an agent without the
// Execute capability (e.g. the leader) rejects collaboration requests instead
// of silently dropping them.
func TestExecuteCollaboration_NoExecuteCapability(t *testing.T) {
	ctx := context.Background()
	target := &fakeExecuteAgent{id: "leader"}

	_, err := executeCollaboration(ctx, target.id, &agentipc.Message{
		Topic:   topicDelegateTask,
		Payload: map[string]any{taskIDKey: "t1"},
	}, nil)
	if err == nil {
		t.Fatal("nil execute capability must error")
	}
}

// TestWireEvolutionIPC_CollaborationExecutedOnSub verifies the end-to-end
// production wiring: wireEvolutionIPC registers every sub agent's Execute
// capability, so a delegate request through the wired bridge executes on the
// target agent and the result round-trips as the reply. No kernel fabric is
// wired here, so the direct executeCollaboration fallback path runs.
func TestWireEvolutionIPC_CollaborationExecutedOnSub(t *testing.T) {
	ctx := context.Background()
	agent := &fakeExecuteAgent{id: "specialist-1"}

	bridge, err := wireEvolutionIPC([]sub.Agent{agent}, nil, nil, nil)
	if err != nil {
		t.Fatalf("wireEvolutionIPC: %v", err)
	}

	reply, err := bridge.ipc.Bus().Request(ctx, "leader", "specialist-1", topicDelegateTask,
		map[string]any{
			taskIDKey:        "t-delegate-e2e",
			"payload":        map[string]any{"question": "q1"},
			"specialization": "research",
		}, 5*time.Second)
	if err != nil {
		t.Fatalf("delegate request: %v", err)
	}
	if reply == nil {
		t.Fatal("reply must not be nil")
	}
	res, ok := reply.Payload.(*models.TaskResult)
	if !ok {
		t.Fatalf("reply payload = %T, want *models.TaskResult", reply.Payload)
	}
	if !res.Success || res.TaskID != "t-delegate-e2e" {
		t.Fatalf("result mismatch: %+v", res)
	}
	if got := agent.executions(); len(got) != 1 || got[0] != "t-delegate-e2e" {
		t.Fatalf("executed tasks = %v, want [t-delegate-e2e]", got)
	}
}

// TestWireEvolutionIPC_PeerTopicRejectedNotExecuted locks the topic boundary
// after peer direct messaging was removed: a peer-topic message through the
// wired bridge fails loud with peerMessagingRemoved and is never executed as
// a task — only the collaboration topics execute.
func TestWireEvolutionIPC_PeerTopicRejectedNotExecuted(t *testing.T) {
	ctx := context.Background()
	agent := &fakeExecuteAgent{id: "specialist-1"}

	bridge, err := wireEvolutionIPC([]sub.Agent{agent}, nil, nil, nil)
	if err != nil {
		t.Fatalf("wireEvolutionIPC: %v", err)
	}

	err = bridge.ipc.Bus().Send(ctx, "leader", "specialist-1", peerTopic, &ahp.AHPMessage{
		MessageID: "m-peer",
		TaskID:    "t-peer",
		AgentID:   "leader",
		Method:    ahp.AHPMethodTask,
		Payload:   map[string]any{"k": "v"},
	})
	if err == nil {
		t.Fatal("peer-topic send must fail (peer direct messaging removed)")
	}
	if !strings.Contains(err.Error(), "peer direct messaging removed") {
		t.Fatalf("error must name the removal, got: %v", err)
	}
	if got := agent.executions(); len(got) != 0 {
		t.Fatalf("peer-topic message must not execute a task, executed: %v", got)
	}
}
