package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/agentipc"
	"github.com/Timwood0x10/ares/internal/agents/peer"
	"github.com/Timwood0x10/ares/internal/aresrecovery"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/runtime/protocol/ahp"
)

// fakeMessageAgent implements the SendMessage surface (interface assertion
// used by wireEvolutionIPC) with a recording delivery function.
type fakeMessageAgent struct {
	id  string
	mu  sync.Mutex
	got []*ahp.AHPMessage
}

func (a *fakeMessageAgent) ID() string { return a.id }

func (a *fakeMessageAgent) SendMessage(_ context.Context, msg *ahp.AHPMessage) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.got = append(a.got, msg)
	return nil
}

func (a *fakeMessageAgent) messages() []*ahp.AHPMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]*ahp.AHPMessage, len(a.got))
	copy(out, a.got)
	return out
}

// stubIPCProtocolSource returns a fixed IPC policy.
type stubIPCProtocolSource struct {
	policy aresrecovery.IPCProtocolPolicy
}

func (s *stubIPCProtocolSource) ActiveIPCProtocolPolicy(context.Context) (aresrecovery.IPCProtocolPolicy, error) {
	return s.policy, nil
}

// buildBridge mirrors wireEvolutionIPC's registration logic on a fresh bus so
// the test can exercise the full json+gzip round trip without constructing
// the large leader.Agent / sub.Agent interfaces. It creates the bus AND the
// policy-aware IPC on the SAME bus, so the peer send reaches the registered
// handler. A non-nil tracer records each peer send as a message span, exactly
// like the production wiring (v0.3.0 review: TraceMessage was library-only).
func buildBridge(target *fakeMessageAgent, policy aresrecovery.IPCProtocolPolicy, tracer *aresrecovery.GlobalTracer) *peer.Registry {
	bus := agentipc.NewBus()
	ipc := aresrecovery.NewEvolutionAwareIPC(bus, &stubIPCProtocolSource{policy: policy})
	_ = bus.Register(target.ID(), func(ctx context.Context, msg *agentipc.Message) (*agentipc.Message, error) {
		payload, err := aresrecovery.Decode(msg.Payload)
		if err != nil {
			return nil, err
		}
		ahpMsg, err := toAHPMessage(payload)
		if err != nil {
			return nil, err
		}
		return nil, target.SendMessage(ctx, ahpMsg)
	})
	reg := peer.NewRegistry()
	_ = reg.Register(target.ID(), func(ctx context.Context, m *ahp.AHPMessage) error {
		if tracer != nil {
			tracer.TraceMessage(m.MessageID, "sent", m.TaskID, map[string]any{
				"from": m.AgentID,
				"to":   target.ID(),
			})
		}
		return ipc.Send(ctx, m.AgentID, target.ID(), peerTopic, m)
	})
	return reg
}

// TestEvolutionIPCBridgeRoundTrip verifies the full round trip under
// json+gzip: the peer send is compressed on the wire and the target agent
// receives the original message content (proving Decode in the bus handler).
func TestEvolutionIPCBridgeRoundTrip(t *testing.T) {
	ctx := context.Background()
	target := &fakeMessageAgent{id: "sub-1"}

	reg := buildBridge(target, aresrecovery.IPCProtocolPolicy{
		Encoding: aresrecovery.WireJSONGzip, MinCompressSize: 1,
	}, nil)

	msg := &ahp.AHPMessage{
		MessageID: "m-roundtrip",
		AgentID:   "leader",
		Method:    ahp.AHPMethodACK,
		Payload:   map[string]any{"compressed": true, "n": 42},
	}
	if err := reg.Send(ctx, "sub-1", msg); err != nil {
		t.Fatalf("peer send: %v", err)
	}
	got := target.messages()
	if len(got) != 1 {
		t.Fatalf("want 1 delivered message, got %d", len(got))
	}
	if got[0].MessageID != "m-roundtrip" || got[0].Payload["compressed"] != true || got[0].Payload["n"] != float64(42) {
		t.Fatalf("round-trip content mismatch: %+v", got[0])
	}
}

// TestEvolutionIPCBridgePlainJSON verifies the default plain-json policy
// delivers the original message unchanged (backward compatible with the
// direct peer channel).
func TestEvolutionIPCBridgePlainJSON(t *testing.T) {
	ctx := context.Background()
	target := &fakeMessageAgent{id: "sub-1"}

	reg := buildBridge(target, aresrecovery.IPCProtocolPolicy{Encoding: aresrecovery.WireJSON}, nil)

	msg := &ahp.AHPMessage{
		MessageID: "m-plain",
		AgentID:   "leader",
		Method:    ahp.AHPMethodTask,
		Payload:   map[string]any{"hello": "world"},
	}
	if err := reg.Send(ctx, "sub-1", msg); err != nil {
		t.Fatalf("peer send: %v", err)
	}
	got := target.messages()
	if len(got) != 1 {
		t.Fatalf("want 1 delivered message, got %d", len(got))
	}
	if got[0].MessageID != "m-plain" || got[0].Payload["hello"] != "world" {
		t.Fatalf("unexpected delivered message %+v", got[0])
	}
}

// TestToAHPMessage verifies both decode paths: the original pointer passes
// through, and a json+gzip round-trip map is re-hydrated.
func TestToAHPMessage(t *testing.T) {
	original := &ahp.AHPMessage{MessageID: "m1", AgentID: "a", Method: ahp.AHPMethodACK}

	// Path 1: original pointer unchanged.
	got, err := toAHPMessage(original)
	if err != nil {
		t.Fatalf("toAHPMessage(original): %v", err)
	}
	if got != original {
		t.Fatal("original pointer must pass through unchanged")
	}

	// Path 2: a decoded json+gzip payload is a map; re-hydrate it.
	mapPayload := map[string]any{
		"message_id": "m1",
		"agent_id":   "a",
		"method":     "ACK",
		"payload":    map[string]any{"k": "v"},
	}
	got2, err := toAHPMessage(mapPayload)
	if err != nil {
		t.Fatalf("toAHPMessage(map): %v", err)
	}
	if got2.MessageID != "m1" || got2.AgentID != "a" || got2.Method != ahp.AHPMethodACK {
		t.Fatalf("re-hydrated message mismatch: %+v", got2)
	}
	if got2.Payload["k"] != "v" {
		t.Fatalf("payload lost in re-hydration: %+v", got2.Payload)
	}
}

// TestToAHPMessageRejectsGarbage verifies a non-AHPMessage payload surfaces
// as an error instead of silently producing an empty message.
func TestToAHPMessageRejectsGarbage(t *testing.T) {
	if _, err := toAHPMessage(42); err == nil {
		t.Fatal("non-message payload must error")
	}
}

// TestEvolutionIPCBridgeTracesMessage verifies the v0.3.0 review wiring: every
// peer send through the evolution-aware bus also records a cross-Fabric
// message span on the shared GlobalTracer (TraceMessage's production path,
// previously library-only). The span is keyed by the message id and links to
// the task it serves.
func TestEvolutionIPCBridgeTracesMessage(t *testing.T) {
	ctx := context.Background()
	target := &fakeMessageAgent{id: "sub-1"}
	tracer := aresrecovery.NewGlobalTracer()
	reg := buildBridge(target, aresrecovery.IPCProtocolPolicy{Encoding: aresrecovery.WireJSON}, tracer)

	msg := &ahp.AHPMessage{
		MessageID: "m-trace",
		TaskID:    "t1",
		AgentID:   "leader",
		Method:    ahp.AHPMethodTask,
		Payload:   map[string]any{"hello": "world"},
	}
	if err := reg.Send(ctx, "sub-1", msg); err != nil {
		t.Fatalf("peer send: %v", err)
	}

	span := tracer.Span("m-trace")
	if span == nil {
		t.Fatalf("peer send must open a message span for %q", msg.MessageID)
	}
	if span.Kind != aresrecovery.SpanMessage || span.ParentID != "t1" {
		t.Fatalf("span must be a message span linked to task t1, got %+v", span)
	}
	if len(span.Events) != 1 || span.Events[0].Name != "sent" {
		t.Fatalf("want a single 'sent' event, got %+v", span.Events)
	}
	if span.Events[0].Detail["from"] != "leader" || span.Events[0].Detail["to"] != "sub-1" {
		t.Fatalf("span detail must carry from/to, got %+v", span.Events[0].Detail)
	}
}

// ── M1 collaboration wiring (delegate/pipeline/orchestrate) ────────────────

// fakeExecuteAgent implements both the SendMessage surface (peer delivery) and
// sub.Agent Execute (collaboration execution), so wireEvolutionIPC wires it
// with an execute capability.
type fakeExecuteAgent struct {
	*fakeMessageAgent
}

// Execute satisfies sub.Agent: records the task and returns a canned result.
func (a *fakeExecuteAgent) Execute(_ context.Context, task *models.Task) (*models.TaskResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return &models.TaskResult{
		TaskID:    task.TaskID,
		AgentType: task.TaskType,
		Success:   true,
		Metadata:  map[string]any{"executed": true, "on": a.id},
	}, nil
}

// TestExecuteCollaboration_RunsTaskAndReturnsReply verifies the core helper
// behind the M1 collaboration wiring: a delegate/pipeline/orchestrate message
// is bridged into a *models.Task, executed on the target agent, and the result
// is returned as the request/reply reply with the correlation id preserved.
func TestExecuteCollaboration_RunsTaskAndReturnsReply(t *testing.T) {
	ctx := context.Background()
	agent := &fakeExecuteAgent{fakeMessageAgent: &fakeMessageAgent{id: "specialist-1"}}

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
	agent := &fakeExecuteAgent{fakeMessageAgent: &fakeMessageAgent{id: "specialist-1"}}

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
	agent := &fakeExecuteAgent{fakeMessageAgent: &fakeMessageAgent{id: "specialist-1"}}

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
	target := &fakeMessageAgent{id: "leader"}

	_, err := executeCollaboration(ctx, target.id, &agentipc.Message{
		Topic:   topicDelegateTask,
		Payload: map[string]any{taskIDKey: "t1"},
	}, nil)
	if err == nil {
		t.Fatal("nil execute capability must error")
	}
}

// TestWireEvolutionIPC_CollaborationExecutedOnSub verifies the end-to-end
// wiring: a DelegateToSpecialist request sent through a bus that mirrors the
// production wireEvolutionIPC registration (topic dispatch + Execute) reaches
// the sub agent's Execute capability and the result round-trips back as the
// reply. This proves M1 collaboration is no longer library-only — a bus
// handler for the delegate topic executes on the target agent.
func TestWireEvolutionIPC_CollaborationExecutedOnSub(t *testing.T) {
	ctx := context.Background()
	sub := &fakeExecuteAgent{fakeMessageAgent: &fakeMessageAgent{id: "specialist-1"}}

	// Mirror wireEvolutionIPC's registration for a sub agent: register a bus
	// handler that dispatches collaboration topics to executeCollaboration.
	bus := agentipc.NewBus()
	_ = bus.Register(sub.id, func(ctx context.Context, msg *agentipc.Message) (*agentipc.Message, error) {
		switch msg.Topic {
		case topicDelegateTask, topicPipelineStage, topicOrchestrateWrk:
			return executeCollaboration(ctx, sub.id, msg, sub.Execute)
		default:
			return deliverPeer(ctx, msg, sub.SendMessage)
		}
	})

	// A caller (leader) delegates via the request/reply primitive — exactly
	// what DelegateToSpecialist does under the hood.
	reply, err := bus.Request(ctx, "leader", "specialist-1", topicDelegateTask,
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
}

// TestWireEvolutionIPC_CollaborationNotInterferesWithPeer verifies a peer
// topic message still goes through the peer delivery path (not executed as a
// task) when collaboration topics share the same bus handler.
func TestWireEvolutionIPC_CollaborationNotInterferesWithPeer(t *testing.T) {
	ctx := context.Background()
	sub := &fakeExecuteAgent{fakeMessageAgent: &fakeMessageAgent{id: "specialist-1"}}

	bus := agentipc.NewBus()
	_ = bus.Register(sub.id, func(ctx context.Context, msg *agentipc.Message) (*agentipc.Message, error) {
		switch msg.Topic {
		case topicDelegateTask, topicPipelineStage, topicOrchestrateWrk:
			return executeCollaboration(ctx, sub.id, msg, sub.Execute)
		default:
			return deliverPeer(ctx, msg, sub.SendMessage)
		}
	})

	// A peer message (AHPMessage) on the peer topic must be delivered via
	// SendMessage, not executed as a task.
	_ = bus.Send(ctx, "leader", sub.id, peerTopic, &ahp.AHPMessage{
		MessageID: "m-peer",
		TaskID:    "t-peer",
		AgentID:   "leader",
		Method:    ahp.AHPMethodTask,
		Payload:   map[string]any{"k": "v"},
	})
	if got := sub.messages(); len(got) != 1 {
		t.Fatalf("peer messages = %d, want 1 (delivered via SendMessage)", len(got))
	}
}
