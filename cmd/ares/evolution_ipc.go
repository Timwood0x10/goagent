// Evolution-aware IPC wiring (v0.3.0 M2-3): bridges the agent peer channel
// through aresrecovery.EvolutionAwareIPC so the active evolution strategy's
// wire policy (ipc.encoding = json | json+gzip) shapes real agent-to-agent
// messages — "Evolution decides; Kernel enforces", same as the spawn gate and
// quota manager.
//
// The peer registry (internal/agents/peer) is the production agent-messaging
// channel: agents register SendMessage delivery functions and send directly
// without routing through the leader. Instead of replacing that channel, this
// wiring interposes the evolution-aware IPC bus between the registry and the
// agents' send functions, so every peer message passes through the policy
// (plain json by default — backward compatible — or json+gzip when the
// evolution strategy deploys it).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Timwood0x10/ares/internal/agentipc"
	"github.com/Timwood0x10/ares/internal/agents/peer"
	"github.com/Timwood0x10/ares/internal/agents/sub"
	"github.com/Timwood0x10/ares/internal/ares_bootstrap"
	"github.com/Timwood0x10/ares/internal/aresrecovery"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/logger"
	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
	"github.com/Timwood0x10/ares/internal/runtime/protocol/ahp"
	"github.com/Timwood0x10/ares/internal/taskfabric"
)

// peerTopic is the bus topic used for peer-channel messages routed through the
// evolution-aware IPC bus.
const peerTopic = "peer"

// Collaboration topics carried by the agentipc collaboration primitives
// (internal/agentipc/collaboration.go). The evolution IPC bridge registers a
// handler for each so a production peer message on these topics has a real
// responder that executes the delegated task on the target agent — closing the
// "library-ready, not wired" gap for M1 collaboration (v0.4.0 review).
const (
	topicDelegateTask   = "delegate-task"
	topicPipelineStage  = "pipeline-stage"
	topicOrchestrateWrk = "orchestrate-worker"
)

// taskIDKey mirrors agentipc.taskIDKey — the payload key carrying the task id
// in collaboration requests. Kept in sync with internal/agentipc/primitives.go.
const taskIDKey = "task_id"

// evolutionIPCBridge holds the evolution-aware IPC bus and the peer registry
// wired to it. The bridge is created once per serve; a nil policy source (no
// evolution store) keeps plain json encoding, which is behaviorally identical
// to the direct peer channel. Cross-Fabric message tracing (M4-1) is applied
// inline by the registry's send wrapper using the tracer passed to
// wireEvolutionIPC, so it is not stored here.
type evolutionIPCBridge struct {
	ipc *aresrecovery.EvolutionAwareIPC
	reg *peer.Registry
}

// wireEvolutionIPC builds the evolution-aware peer bridge: a Bus whose Send
// applies the active evolution IPC policy, with one bus handler per agent that
// decodes the wire payload and forwards the original AHPMessage to the agent's
// delivery function. The returned registry routes every peer send through the
// bus. When tracer is non-nil, every peer send is also recorded as a
// cross-Fabric message span (v0.3.0 M4-1 TraceMessage — the v0.3.0 review
// flagged it as library-only; this is its production write path).
//
// Args:
//   - subAgents: the sub agents (registered under their IDs).
//   - store: the evolution strategy store (nil → plain json, no-op policy).
//   - tracer: the shared GlobalTracer (nil → no message tracing).
//
// Returns:
//   - *evolutionIPCBridge: the wired bridge (registry + ipc).
//   - error: when a bus handler cannot be registered.
func wireEvolutionIPC(subAgents []sub.Agent, store evolution.StrategyStore, tracer *aresrecovery.GlobalTracer, kernel *kernelHandle) (*evolutionIPCBridge, error) {
	// Capability lookup for kernel-routed collaboration: a topic addressed to
	// agent X executes as a fabric task whose capability is X's declared type
	// — the SAME matching rule the scheduler uses for every other task.
	capByAgent := make(map[string]string, len(subAgents))
	for _, sa := range subAgents {
		if sa != nil {
			capByAgent[sa.ID()] = string(sa.Type())
		}
	}
	// WithLogger: a handler panic is contained at the goroutine boundary (P1-3)
	// and surfaces as ErrHandlerPanic, but the bus never prints on its own
	// (code_rules §9.1). Without a logger the containment would be invisible to
	// operators — the caller sees a failed request and nothing explains why.
	bus := agentipc.NewBus().WithLogger(logger.Module("agentipc"))
	ipc := aresrecovery.NewEvolutionAwareIPC(bus, ares_bootstrap.NewIPCProtocolPolicySource(store))
	reg := peer.NewRegistry()

	register := func(agentID string, send func(context.Context, *ahp.AHPMessage) error, execute func(context.Context, *models.Task) (*models.TaskResult, error)) {
		if agentID == "" || send == nil {
			return
		}
		targetID := agentID
		// Bus handler: dispatch by topic. Peer messages (the production
		// channel) are decoded and delivered to the agent unchanged; M1
		// collaboration topics (delegate/pipeline/orchestrate) are executed on
		// the target agent via its Execute capability, with the result
		// returned as the request/reply reply. This closes the v0.4.0
		// "library-ready, not wired" gap for collaboration.
		_ = bus.Register(targetID, func(ctx context.Context, msg *agentipc.Message) (*agentipc.Message, error) {
			switch msg.Topic {
			case topicDelegateTask, topicPipelineStage, topicOrchestrateWrk:
				// Fusion plan C2: collaboration requests execute through the
				// KERNEL fabric DAG (single engine). The direct-execute path
				// remains only as the fallback when no fabric is wired.
				if kernel != nil && kernel.fabric != nil && capByAgent[targetID] != "" {
					return executeCollabViaKernel(ctx, kernel, targetID, capByAgent[targetID], msg)
				}
				if execute == nil {
					return nil, fmt.Errorf("agentipc: agent %s cannot execute collaboration task", targetID)
				}
				return executeCollaboration(ctx, targetID, msg, execute)
			default:
				return deliverPeer(ctx, msg, send)
			}
		})
		// Peer registry entry: route the peer send through the evolution-aware
		// bus. The sender's identity comes from the message itself.
		_ = reg.Register(targetID, func(ctx context.Context, m *ahp.AHPMessage) error {
			if tracer != nil {
				tracer.TraceMessage(m.MessageID, "sent", m.TaskID, map[string]any{
					"from":       m.AgentID,
					"to":         targetID,
					"topic":      peerTopic,
					"method":     string(m.Method),
					"session_id": m.SessionID,
				})
			}
			return ipc.Send(ctx, m.AgentID, targetID, peerTopic, m)
		})
	}

	// SendMessage is exposed via interface assertion (same discovery as
	// buildPeerRegistry); agents that do not implement it are skipped, not an
	// error. Sub-agents additionally expose Execute — the capability that
	// lets collaboration topics (delegate/pipeline/orchestrate) run a task on
	// them and return the result.
	for _, sa := range subAgents {
		if sender, ok := sa.(interface {
			SendMessage(context.Context, *ahp.AHPMessage) error
		}); ok {
			// sa is already sub.Agent (the wireEvolutionIPC signature types it
			// as such), so its Execute capability is directly available.
			register(sa.ID(), sender.SendMessage, sa.Execute)
		}
	}
	return &evolutionIPCBridge{ipc: ipc, reg: reg}, nil
}

// deliverPeer decodes the wire payload and delivers it to the agent unchanged
// (the production peer path). Plain json sends pass through unchanged; json+
// gzip sends are restored here so the agent always sees the original message.
func deliverPeer(ctx context.Context, msg *agentipc.Message, send func(context.Context, *ahp.AHPMessage) error) (*agentipc.Message, error) {
	payload, err := aresrecovery.Decode(msg.Payload)
	if err != nil {
		return nil, fmt.Errorf("evolution IPC decode: %w", err)
	}
	ahpMsg, err := toAHPMessage(payload)
	if err != nil {
		return nil, err
	}
	if err := send(ctx, ahpMsg); err != nil {
		return nil, err
	}
	// agentipc.Handler contract: a nil reply + nil error means the message
	// was delivered and no reply is expected (fire-and-forget peer delivery).
	return nil, nil //nolint:nilnil // documented Handler "no reply" contract.
}

// executeCollaboration runs an M1 collaboration request (delegate/pipeline/
// orchestrate) on the target agent and returns the result as the reply. The
// payload shape is the agentipc collaboration body: a map with "task_id" and
// "payload" (and optionally "specialization" for delegate-task). The payload
// is bridged into a *models.Task with the agent id set as the task type, so
// the agent's Execute runs it through its normal executor path.
func executeCollaboration(ctx context.Context, targetID string, msg *agentipc.Message, execute func(context.Context, *models.Task) (*models.TaskResult, error)) (*agentipc.Message, error) {
	if execute == nil {
		return nil, fmt.Errorf("agentipc: agent %s cannot execute collaboration task (no Execute capability)", targetID)
	}
	body, ok := msg.Payload.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("agentipc: collaboration payload must be a map, got %T", msg.Payload)
	}
	taskID, _ := body[taskIDKey].(string)
	if taskID == "" {
		return nil, fmt.Errorf("agentipc: collaboration task missing %q", taskIDKey)
	}
	// The task payload is the nested "payload" field (or the whole body for
	// pipeline stages, which carry the previous stage's output inline).
	taskPayload := body
	if p, ok := body["payload"]; ok {
		if pm, ok := p.(map[string]any); ok {
			taskPayload = pm
		}
	}
	task := &models.Task{
		TaskID:   taskID,
		TaskType: models.AgentType(targetID),
		Payload:  taskPayload,
	}
	result, err := execute(ctx, task)
	if err != nil {
		return nil, fmt.Errorf("agentipc: execute collaboration task %s on %s: %w", taskID, targetID, err)
	}
	return &agentipc.Message{
		ID:            "collab-" + taskID,
		From:          targetID,
		To:            msg.From,
		Topic:         msg.Topic + "-reply",
		CorrelationID: msg.CorrelationID,
		Payload:       result,
		At:            msg.At,
	}, nil
}

// executeCollabViaKernel routes one M1 collaboration request through an L2
// session (M4-D): the target capability is nominal — no peer advertises
// primary caps anymore — so the question runs as a plan session and the
// terminal answer comes back as the reply, byte-compatible with the legacy
// direct-execution reply shape.
func executeCollabViaKernel(ctx context.Context, k *kernelHandle, targetID, capability string, msg *agentipc.Message) (*agentipc.Message, error) {
	_ = capability // nominal post-D; kept for signature stability.
	body, ok := msg.Payload.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("agentipc: collaboration payload must be a map, got %T", msg.Payload)
	}
	taskID, _ := body[taskIDKey].(string)
	if taskID == "" {
		return nil, fmt.Errorf("agentipc: collaboration task missing %q", taskIDKey)
	}
	taskPayload := body
	if p, ok := body["payload"].(map[string]any); ok {
		taskPayload = p
	}
	// Prompt precedence: explicit input, task description, then the topic.
	prompt, _ := taskPayload["input"].(string)
	if prompt == "" {
		prompt, _ = taskPayload["task_desc"].(string)
	}
	if prompt == "" {
		prompt = msg.Topic
	}
	answer, err := executeAskViaSession(ctx, k, taskID, prompt, taskPayload)
	if err != nil {
		return nil, fmt.Errorf("agentipc: kernel execution of %s on %s: %w", taskID, targetID, err)
	}
	result := models.NewTaskResult(taskID, models.AgentType(targetID))
	result.SetSuccess(nil, answer)
	return &agentipc.Message{
		ID:            "collab-" + taskID,
		From:          targetID,
		To:            msg.From,
		Topic:         msg.Topic + "-reply",
		CorrelationID: msg.CorrelationID,
		Payload:       result,
		At:            msg.At,
	}, nil
}

// askSessionSeq makes ask_agent session ids unique per invocation (same
// collision class as the HTTP run ids: two requests sharing a task id —
// a retry, or two leaders delegating the same logical task — must not share
// a session; taskID is kept in the id purely for traceability).
var askSessionSeq atomic.Uint64

// executeAskViaSession submits one collaboration question as an L2 plan
// session and waits for its terminal answer (M4-D: the L2 form of ask_agent
// execution). The session is released before returning so the reaper can
// harvest its tasks.
func executeAskViaSession(ctx context.Context, k *kernelHandle, taskID, prompt string, payload map[string]any) (string, error) {
	if k == nil || k.fabric == nil || k.sessionReg == nil {
		return "", fmt.Errorf("agentipc: ask_agent session execution needs a wired kernel (fabric + session registry)")
	}
	sessionID := fmt.Sprintf("ipc-sess-%s-%d", taskID, askSessionSeq.Add(1))
	sessPayload := make(map[string]any, len(payload)+2)
	for key, val := range payload {
		sessPayload[key] = val
	}
	sessPayload["session_id"] = sessionID
	sessPayload["input"] = prompt
	planTaskID, err := submitPeerTask(ctx, k, planCapability, sessPayload)
	if err != nil {
		return "", err
	}

	waitCtx := ctx
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, collabTimeout)
		defer cancel()
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		// Fast failure: a failed plan means the session can never answer.
		if tk, err := k.fabric.Task(planTaskID); err == nil && tk.State == taskfabric.StateFailed {
			releaseSessionQuietly(k, sessionID)
			return "", fmt.Errorf("agentipc: ask session %s plan task failed", sessionID)
		}
		if answer, ok := completedSessionAnswer(k, sessionID); ok {
			releaseSessionQuietly(k, sessionID)
			if answer == "" {
				return "", fmt.Errorf("agentipc: ask session %s answered empty", sessionID)
			}
			return answer, nil
		}
		select {
		case <-waitCtx.Done():
			releaseSessionQuietly(k, sessionID)
			if err := waitCtx.Err(); err != nil {
				return "", fmt.Errorf("agentipc: ask session %s: %w", sessionID, err)
			}
			return "", fmt.Errorf("agentipc: ask session %s timed out", sessionID)
		case <-ticker.C:
		}
	}
}

// completedSessionAnswer returns the first COMPLETED answer task's content
// for a session. It scans fabric task ids (sess/<sid>/…/answer#…) rather
// than the session graph: the answer body releases its session on success
// (B2-2), so the registry entry is already gone by the time we poll.
func completedSessionAnswer(k *kernelHandle, sessionID string) (string, bool) {
	for _, id := range k.fabric.IDs() {
		if !strings.Contains(id, sessionID) || !strings.Contains(id, "/answer#") {
			continue
		}
		tk, err := k.fabric.Task(id)
		if err != nil || tk.State != taskfabric.StateCompleted {
			continue
		}
		content, err := sessionAnswerContent(tk)
		if err != nil || content == "" {
			continue
		}
		return content, true
	}
	return "", false
}

// sessionAnswerContent reads the terminal answer body from its envelope
// (same read path as the canary harness: items[0].Content).
func sessionAnswerContent(tk *taskfabric.Task) (string, error) {
	dc, err := taskfabric.DecodeCheckpoint(tk.Checkpoint)
	if err != nil {
		return "", err
	}
	sc, ok := dc.StepCheckpoint.(map[string]any)
	if !ok {
		return "", fmt.Errorf("agentipc: answer checkpoint carries no result map")
	}
	raw, ok := sc["items"]
	if !ok {
		return "", fmt.Errorf("agentipc: answer envelope carries no items")
	}
	items, ok := raw.([]*models.RecommendItem)
	if !ok || len(items) == 0 {
		return "", fmt.Errorf("agentipc: answer items unreadable, got %T", raw)
	}
	return items[0].Content, nil
}

// toAHPMessage restores an *ahp.AHPMessage from a decoded payload. Plain
// sends deliver the original pointer unchanged; json+gzip sends round-trip
// through JSON, so the decoded value is a map that must be re-hydrated.
//
// KNOWN LIMITATION (JSON round-trip): under the json+gzip wire policy the
// payload is serialized to JSON, so values inside AHPMessage.Payload that
// JSON cannot represent faithfully are type-drifted on delivery — e.g. int
// becomes float64, non-string map keys are coerced, and custom structs are
// flattened to plain maps. This is a JSON wire-format limitation, not a bug
// in this function; the plain-json policy (the default) delivers the original
// pointer unchanged and has no such drift. Payload values should therefore be
// JSON-friendly (string/float64/bool/arrays/maps with string keys) when the
// evolution strategy enables json+gzip compression.
//
// Args:
//   - payload: the decoded payload (either *ahp.AHPMessage or a JSON map).
//
// Returns:
//   - *ahp.AHPMessage: the restored message.
//   - error: when the payload is not an AHPMessage or cannot be re-hydrated.
func toAHPMessage(payload any) (*ahp.AHPMessage, error) {
	if m, ok := payload.(*ahp.AHPMessage); ok {
		return m, nil
	}
	// Re-hydrate from the JSON map produced by a json+gzip round-trip.
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("evolution IPC re-marshal: %w", err)
	}
	var m ahp.AHPMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("evolution IPC re-hydrate: %w", err)
	}
	return &m, nil
}
