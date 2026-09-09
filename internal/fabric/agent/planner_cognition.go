package agentfabric

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/Timwood0x10/ares/internal/agents"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/fabric/task"
	"github.com/Timwood0x10/ares/internal/fabric/task/workflow/engine"
	llmcore "github.com/Timwood0x10/ares/internal/llmcore"
	resources "github.com/Timwood0x10/ares/internal/tools/resources/core"
	"github.com/Timwood0x10/ares/internal/truncate"
)

// DefaultMaxPlanDepth is the default growth-depth upper bound for an L2
// session graph (生长深度上界护栏). It caps how many plan-tool rounds a
// session can run before the planner is forced to answer — preventing
// unbounded graph growth from a runaway LLM that keeps requesting tools.
const DefaultMaxPlanDepth = 10

// PlannerDeps carries the plannerCognition's dependencies, injected at
// construction time. The planner reads predecessor outputs from the
// fabric (by node ID = task ID join), calls the LLM once, and grows tool/plan
// nodes into the L2 graph — it does NOT execute tools (the scheduler does).
type PlannerDeps struct {
	// ChatClient sends chat messages with tool support to the LLM.
	ChatClient ChatClient
	// ToolBinder advertises tool schemas to the LLM (the planner shows the
	// LLM what tools are available so it can decide which to call; execution
	// is the scheduler's job, not the planner's).
	ToolBinder ToolBinder
	// Sessions is the per-session L2 graph registry — the planner looks up
	// the graph by the task's SessionID.
	Sessions *SessionRegistry
	// Fabric is the task fabric used to read predecessor outputs by
	// node-ID = task-ID join (the node does not store Output).
	Fabric FabricReader
	// L1DAG is the L1 ToolClass capability graph. The planner reads
	// each tool's enabled/budget/prior metadata before growing an L2 tool
	// node — enabled=false blocks growth, budget=N caps instances per
	// session. Nil = no L1 graph (constraints default to permissive).
	L1DAG *engine.MutableDAG
	// MaxDepth caps the plan-tool growth depth (0 = DefaultMaxPlanDepth).
	MaxDepth int
	// StrategySource is the optional live evolution strategy (the
	// planner is the strategy actuator after ReAct — prompt template + LLM
	// param overrides steer plan growth the way they steered the chat loop).
	// Nil = no steering (same degrade contract as the retired chat body).
	StrategySource agents.StrategySource
	// AgentFabric is the agent population the executing agents live in.
	// When set, the planner reads the EXECUTING agent's cognitive Context —
	// the distilled experience prior injected at spawn (SpawnSpec.
	// ExperiencePrior) — and injects it as the leading context message: the
	// READ side of the experience loop (M4.3). Nil = no prior injection
	// (zero-value usable: tests and degraded wiring plan without priors).
	AgentFabric *Fabric
	// Logger is the shared logger.
	Logger *slog.Logger
}

// FabricReader is the minimal fabric surface the planner needs: read a task's
// checkpoint to extract its predecessor's output. Interface at the consumer
// so the planner stays decoupled from the fabric package.
type FabricReader interface {
	// Task returns a snapshot of the task (ErrTaskNotFound when unknown).
	Task(id string) (*taskfabric.Task, error)
}

// plannerCognition grows the L2 session graph by one plan-tool round per
// quantum. It is the replacement for the ReAct loop's "chatStep":
// instead of executing tools inside a cognition, it grows tool nodes into
// the L2 graph and lets the scheduler execute them as first-class tasks.
//
// Lifecycle per quantum:
//  1. Read SessionID from the task → look up the L2Graph from the registry.
//  2. Read the predecessor's output from the fabric envelope (by node ID =
//     task ID join) to assemble the LLM context.
//  3. Call the LLM once with the available tool schemas.
//  4. If the LLM returns tool calls: AddNode each one to the L2 graph
//     (the incremental compiler projects them to fabric tasks), then grow
//     a new plan node depending on them. Done:true — the next plan quantum
//     resumes when the scheduler drains the new tasks.
//  5. If the LLM returns no tool calls: grow an answer node carrying the
//     final response. Done:true — the session terminates.
//
// The planner NEVER executes tools ("plannerCognition 自己不执行工具，
// 只生长图"). Execution is the scheduler's job — tool nodes are dispatched
// to toolCognition through the same fabric → scheduler → routerCognition
// path as every other node.
type plannerCognition struct {
	chat     ChatClient
	binder   ToolBinder
	sessions *SessionRegistry
	fabric   FabricReader
	l1       *engine.MutableDAG // L1 ToolClass graph, nil = permissive
	maxDepth int
	strategy agents.StrategySource // live evolution strategy, nil = unsteered
	// agentFabric reads the EXECUTING agent's cognitive Context (the
	// spawn-time experience prior). Nil = no prior injection.
	agentFabric *Fabric
	logger      *slog.Logger
	// forcedAnswers counts quanta that hit the growth-depth guard and were
	// forced into an answer node (canary metric: the depth-exhaustion
	// rate). One shared planner serves every session, so the counter is
	// process-wide, not per-session.
	forcedAnswers atomic.Uint64
}

var _ Cognition = (*plannerCognition)(nil)

// ForcedAnswers reports how many quanta hit the growth-depth guard and were
// forced into an answer node (canary metric: the depth-exhaustion
// rate). One shared planner serves every session, so the count is
// process-wide. A rising rate under canary means the depth cap is binding
// real sessions, not just runaways.
func (c *plannerCognition) ForcedAnswers() uint64 {
	if c == nil {
		return 0
	}
	return c.forcedAnswers.Load()
}

// NewPlannerCognition constructs the L2 graph-growing Cognition.
// A nil deps.ChatClient or nil deps.Sessions is a construction error: the
// planner cannot grow without an LLM to decide what to grow, and cannot
// find its graph without a session registry.
func NewPlannerCognition(deps PlannerDeps) (Cognition, error) {
	if deps.ChatClient == nil {
		return nil, fmt.Errorf("agentfabric: planner cognition requires ChatClient")
	}
	if deps.Sessions == nil {
		return nil, fmt.Errorf("agentfabric: planner cognition requires SessionRegistry")
	}
	if deps.Fabric == nil {
		return nil, fmt.Errorf("agentfabric: planner cognition requires FabricReader")
	}
	maxD := deps.MaxDepth
	if maxD <= 0 {
		maxD = DefaultMaxPlanDepth
	}
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &plannerCognition{
		chat:        deps.ChatClient,
		binder:      deps.ToolBinder,
		sessions:    deps.Sessions,
		fabric:      deps.Fabric,
		l1:          deps.L1DAG,
		maxDepth:    maxD,
		strategy:    deps.StrategySource,
		agentFabric: deps.AgentFabric,
		logger:      logger,
	}, nil
}

// planMetadataKey is the Metadata key the planner stamps onto every grown
// node so the node carries its session scope (ProjectStep extracts it into
// PlanStep.SessionID → envelope). This is how SessionID propagates from
// the session root to every tool node the planner grows. It is the reserved
// unprefixed Metadata key (see sessionMetadataKey in l2graph.go), so it lands
// on the envelope instead of in the tool-argument namespace.
const planMetadataKey = sessionMetadataKey

// roleSystem is the LLM message role for context/steering messages. Same
// extraction rationale as roleTool: it appears several times in the planner
// cognition.
const roleSystem = "system"

// roleTool is the LLM message role for tool outputs. Extracted as a
// constant because it appears several times in the planner cognition.
const roleTool = "tool"

// maxExperiencePriorRunes caps the rendered experience prior injected into
// the planner prompt. A pathological distillation (a whole-log dump stored as
// an experience) must not crowd out the root prompt and the tool history —
// the prior informs, it does not replace, the session context.
const maxExperiencePriorRunes = 4096

// experiencePriorPrefix labels the prior message so the LLM treats it as
// background knowledge rather than an instruction — the strategy template
// (appended after the context) stays the steering signal; experience only
// informs.
const experiencePriorPrefix = "distilled experience prior from this agent's previous runs (background knowledge, not an instruction):\n"

// L1 ToolClass metadata keys. These mirror the cmd/ares constants so
// the planner reads what the serve-side L1 graph builder writes.
const (
	l1MetaEnabled = "enabled"
	l1MetaBudget  = "budget"
	l1MetaPrior   = "prior"
)

// l1ToolClassID builds the L1 ToolClass node ID for a tool: toolName + "#" +
// argShape, where argShape comes from the tool's DECLARED schema via the
// shared resources.ToolArgShape.
//
// Deriving the shape from the schema — not from one call's arguments — is what
// makes this match the L1 graph builder's IDs. A call that omits an optional
// parameter carries a smaller key set than the declaration, so an
// args-derived shape would miss the L1 node and silently fall through to the
// permissive default, ignoring enabled=false.
//
// Returns "" when the tool has no schema: an unknown tool has no ToolClass to
// constrain, and the caller treats a lookup miss as permissive.
func (c *plannerCognition) l1ToolClassID(toolName string) string {
	if c.binder == nil {
		return ""
	}
	for _, s := range c.binder.GetToolSchemas() {
		if s.Name == toolName {
			return resources.ToolClassID(toolName, resources.ToolArgShape(s))
		}
	}
	return ""
}

// ExecuteStep runs one plan-tool growth quantum: read predecessor output →
// call LLM → grow tool/answer nodes into the L2 graph. The task's
// SessionID selects the graph; the task's TaskID is the plan node that
// triggered this quantum (its predecessor chain defines the context path).
//
// The first plan quantum references a task ID that is NOT a graph node —
// only the root exists at session start. The planner uses the root (or the
// last tool node on the predecessor chain) as the predecessor for the tool
// nodes it grows. Subsequent plan quanta reference plan nodes that were
// grown by the previous quantum's growToolNodes, so they ARE in the graph.
func (c *plannerCognition) ExecuteStep(ctx context.Context, task *models.Task) (*StepOutcome, error) {
	if task == nil {
		return nil, fmt.Errorf("agentfabric: planner cognition: nil task")
	}

	sessionID := task.SessionID
	if sessionID == "" {
		return nil, fmt.Errorf("agentfabric: planner cognition: task %q has no session id", task.TaskID)
	}

	g, err := c.sessions.GetSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("agentfabric: planner cognition: %w", err)
	}

	// Determine the current plan-tool growth depth and check the guard
	// (生长深度上界护栏). PlanDepth counts plan nodes grown by
	// growToolNodes — the initial plan quantum has depth 0.
	depth := g.PlanDepth()
	if depth >= c.maxDepth {
		// Growth-depth upper bound reached: force an answer node so the
		// session terminates instead of growing unbounded.
		c.forcedAnswers.Add(1)
		c.logger.Warn("planner: max plan depth reached, forcing answer",
			"session", sessionID, "depth", depth, "max", c.maxDepth)
		return c.growAnswerNode(ctx, g, task, "max plan depth reached", nil)
	}

	// Assemble the LLM context from the predecessor path.
	prompt, err := c.assembleContext(ctx, task, g)
	if err != nil {
		return nil, fmt.Errorf("agentfabric: planner cognition: assemble context: %w", err)
	}

	// Inject L1 prior hints as a system message (prompt-only, never a
	// growth block). Deterministic order; absent when no priors are set.
	if priors := c.l1Priors(); len(priors) > 0 {
		prompt = append(prompt, &llmcore.LLMMessage{
			Role:    roleSystem,
			Content: "evolution priors (hints only, tool choice stays with you):\n- " + strings.Join(priors, "\n- "),
		})
	}

	// Steer plan growth with the live evolution strategy (the planner
	// is the strategy actuator after ReAct). Mirrors the retired chat loop's
	// renderPromptAndParams: template override rides a system message
	// (prompt-only, never a growth block), param overrides ride the LLM
	// call params. Absent/unreadable strategy = unsteered growth.
	llmParams := map[string]any{}
	if st := c.activeStrategy(ctx); st != nil {
		if strings.TrimSpace(st.Prompt) != "" {
			prompt = append(prompt, &llmcore.LLMMessage{
				Role:    roleSystem,
				Content: "evolution strategy (deployed " + st.ID + "):\n" + st.Prompt,
			})
		}
		for k, v := range st.Params {
			llmParams[k] = v
		}
	}

	// Build the tool schemas for the LLM.
	var llmTools []llmcore.Tool
	if c.binder != nil {
		schemas := c.binder.GetToolSchemas()
		llmTools = make([]llmcore.Tool, 0, len(schemas))
		for _, s := range schemas {
			llmTools = append(llmTools, toCoreTool(s))
		}
	}

	// Call the LLM once with the context and tool schemas.
	resp, err := c.chat.Chat(ctx, prompt, llmTools, llmParams)
	if err != nil {
		return nil, fmt.Errorf("agentfabric: planner cognition: chat: %w", err)
	}

	// Token accounting (the fitness cost channel, M4): this quantum's LLM
	// usage rides the StepOutcome result metadata; the scheduler's
	// re-wrap path accumulates it into the checkpoint envelope, so the
	// session's total spend surfaces on the terminal task.completed event
	// for the RuntimeObserver's cost penalty. A provider that reports no
	// usage contributes zero — the observer then scores on outcome and
	// latency alone.
	stampTokenUsage(resp)

	// No tool calls: the LLM gave a final answer. Grow an answer node.
	if len(resp.ToolCalls) == 0 {
		return c.growAnswerNode(ctx, g, task, resp.Content, resp)
	}

	// Tool calls: grow tool nodes + a new plan node depending on them.
	grown, err := c.growToolNodes(ctx, g, task, resp.ToolCalls, sessionID)
	if err != nil {
		return nil, fmt.Errorf("agentfabric: planner cognition: grow nodes: %w", err)
	}
	if grown == 0 {
		// All tool calls were skipped by L1 constraints (enabled=false
		// or budget exhausted). Force an answer so the session terminates
		// instead of looping on a planner that can never grow a tool.
		return c.growAnswerNode(ctx, g, task, resp.Content, resp)
	}

	result := models.NewTaskResult(task.TaskID, task.AgentType)
	result.SetSuccess(nil, "planner grew "+strconv.Itoa(grown)+" tool nodes")
	result.Metadata = tokenUsageMetadata(resp)
	return &StepOutcome{Done: true, Result: result}, nil
}

// activeStrategy fetches the currently-deployed evolution strategy, if any.
// Errors are logged and ignored so a missing store
// never breaks plan growth — same degrade contract as the retired chat loop.
func (c *plannerCognition) activeStrategy(ctx context.Context) *agents.ActiveStrategy {
	if c.strategy == nil {
		return nil
	}
	st, err := c.strategy.GetActiveStrategy(ctx)
	if err != nil {
		c.logger.Warn("planner: failed to read active strategy", "error", err)
		return nil
	}
	return st
}

// assembleContext builds the LLM message list from the predecessor path:
// the session prompt (root output) + the outputs of every tool node on the
// path from root to this plan node. Outputs are read from the fabric task
// envelopes by node ID = task ID join (nodes carry no Output of
// their own).
func (c *plannerCognition) assembleContext(ctx context.Context, task *models.Task, g *L2Graph) ([]*llmcore.LLMMessage, error) {
	// Read the session prompt from the root task's envelope.
	rootID := g.Root()
	rootPrompt, err := c.readNodeOutput(rootID)
	if err != nil || strings.TrimSpace(rootPrompt) == "" {
		// The root may not have completed yet (readNodeOutput reports an
		// incomplete task as empty, not as an error), or it completed
		// empty. Either way the payload's "input" is the prompt the
		// admission path stamped — never send an empty user message, real
		// providers reject it.
		//
		// The payload's "input" is asserted strictly: the admission path
		// (submitPeerTask → ensureSessionAdmission) always stamps a string
		// input, so a missing or non-string value is a wiring bug, not a
		// data condition — surface it as an error (the fabric retries the
		// task) instead of silently planning on an empty prompt.
		fallback, ok := task.Payload["input"].(string)
		if !ok {
			return nil, fmt.Errorf("agentfabric: planner cognition: task %q payload has no string %q (root %q unreadable: %v)",
				task.TaskID, "input", rootID, err)
		}
		rootPrompt = fallback
	}

	messages := []*llmcore.LLMMessage{
		{Role: "user", Content: rootPrompt},
	}

	// Walk the predecessor chain from this plan node back to the root. The
	// walk is newest-first, so the collected nodes are REVERSED before
	// being appended: the LLM must observe tool results in execution order,
	// the same order ReAct's Messages[] presented them. Appending in walk
	// order would invert the history and change what the model concludes.
	var chain []string
	for predID := g.Predecessor(task.TaskID); predID != "" && predID != rootID; predID = g.Predecessor(predID) {
		chain = append(chain, predID)
	}
	steps := g.DAG().StepIndex()
	for i := len(chain) - 1; i >= 0; i-- {
		nodeID := chain[i]
		step, ok := steps[nodeID]
		if !ok || !strings.HasPrefix(step.AgentType, "tool/") {
			continue
		}
		output, err := c.readNodeOutput(nodeID)
		switch {
		case err != nil:
			// A predecessor whose task is gone is a HOLE in the history, not
			// an empty step: the envelope is the only place output lives
			// (decision C), so losing it silently would let the model plan on
			// a truncated past. Surface it and keep walking.
			c.logger.Warn("planner: predecessor output unreadable; context has a hole",
				"node", nodeID, "error", err)
		case output != "":
			messages = append(messages, toolHistoryPair(step, nodeID, output)...)
		}
	}

	// M4.3 read side: the executing agent's distilled experience prior rides
	// as the FIRST message, ahead of the root prompt and the strategy
	// template appended by ExecuteStep — early grounding, late steering.
	if sys := c.experiencePriorSystemMessage(task); sys != nil {
		messages = append([]*llmcore.LLMMessage{sys}, messages...)
	}

	return messages, nil
}

// assembleAnswerMessages rebuilds the session's LLM context for the answer
// body's synthesis call (M4.2): the same message list the planner would send
// on its next quantum — root prompt, tool history (rebuilt as
// assistant+tool pairs from the fabric envelopes), experience prior —
// assembled from the ANSWER task's own predecessor chain. The answer node IS
// a graph node (node id = task id), so the same walk that feeds the planner
// feeds the synthesizer; non-tool nodes on the chain (plan nodes) are
// skipped by assembleContext already.
//
// It delegates to assembleContext wholesale so the synthesizer sees EXACTLY
// what the planner sees — a divergent context would make the synthesized
// answer inconsistent with the plan that produced it. The context is bounded
// by the same growth-depth guard that bounds every planner quantum (no
// extra truncation: parity with the planner's own view is the contract).
//
// The bool (not error) contract is deliberate: an assembly miss is a
// degraded condition (session already released, root prompt unreadable),
// not a retriable quantum failure — the answer body falls back to the gap
// body instead of letting the fabric burn retry budget on a failure no
// retry can fix.
func (c *plannerCognition) assembleAnswerMessages(ctx context.Context, task *models.Task) ([]*llmcore.LLMMessage, bool) {
	g, err := c.sessions.GetSession(task.SessionID)
	if err != nil {
		// Most commonly the session was already released (duplicate
		// answer execution): there is no history left to synthesize
		// from, which is a gap, not a failure.
		c.logger.Warn("answer synthesis: session graph unavailable",
			"session", task.SessionID, "task", task.TaskID, "error", err)
		return nil, false
	}
	msgs, err := c.assembleContext(ctx, task, g)
	if err != nil {
		c.logger.Warn("answer synthesis: context assembly failed",
			"session", task.SessionID, "task", task.TaskID, "error", err)
		return nil, false
	}
	return msgs, true
}

// experiencePriorSystemMessage renders the EXECUTING agent's distilled
// experience prior as the leading system message. The join key is the
// executingAgentKey stamp Agent.ExecuteStep puts on the task payload (the
// planner is shared across agents; the task is the only carrier of the
// executor's identity). Every failure mode — no fabric wired, no stamp
// (planner invoked directly), unknown agent, empty/unrenderable prior —
// degrades to nil: a missing prior must change nothing (zero-value usable),
// never fail the quantum.
func (c *plannerCognition) experiencePriorSystemMessage(task *models.Task) *llmcore.LLMMessage {
	if c.agentFabric == nil {
		return nil
	}
	agentID, _ := task.Payload[executingAgentKey].(string)
	if agentID == "" {
		return nil
	}
	cs, err := c.agentFabric.CognitiveState(agentID)
	if err != nil {
		// The executing agent vanished from the fabric (killed mid-lease):
		// log and plan without a prior — the quantum is still runnable.
		c.logger.Warn("planner: executing agent unreadable; planning without experience prior",
			"agent", agentID, "error", err)
		return nil
	}
	prior := truncate.WithEllipsis(renderExperiencePrior(cs.Context), maxExperiencePriorRunes)
	if prior == "" {
		return nil
	}
	return &llmcore.LLMMessage{
		Role:    roleSystem,
		Content: experiencePriorPrefix + prior,
	}
}

// renderExperiencePrior renders a cognitive Context value into prompt text.
// The production write side (cmd/ares loadExperiencePrior) stores a
// {type, problem, solution, constraints} map, which is JSON-encoded for
// readability; a plain-string prior (tests, Recover-restored states) is used
// as-is. Unrenderable values yield "" — the prior is informative, never
// load-bearing.
func renderExperiencePrior(v any) string {
	switch p := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(p)
	default:
		buf, err := json.MarshalIndent(p, "", "  ")
		if err != nil {
			return ""
		}
		return string(buf)
	}
}

// toolHistoryPair renders one executed tool node as the assistant+tool
// message pair the Chat API requires: the assistant message carries the
// original tool call (reconstructed from the node's arg.-namespaced
// Metadata), and the tool message carries its envelope output, linked by
// the node ID as the tool-call ID.
//
// A bare tool message with no preceding assistant tool_call violates the
// provider contract (OpenAI rejects orphan tool messages; lenient providers
// accept them but the model loses track of what it already did — observed
// live as the same grep call repeated every round until the depth cap).
// ReAct never has this problem because its history IS the live conversation
// that produced the calls; the planner rebuilds history from the graph, so
// it must rebuild the pairing too.
func toolHistoryPair(step *engine.Step, nodeID, output string) []*llmcore.LLMMessage {
	tool := strings.TrimPrefix(step.AgentType, "tool/")
	meta := make(map[string]any, len(step.Metadata))
	for k, v := range step.Metadata {
		meta[k] = v
	}
	argsJSON, err := json.Marshal(argsFromPayload(meta))
	if err != nil {
		argsJSON = []byte("{}")
	}
	return []*llmcore.LLMMessage{
		{
			Role:    "assistant",
			Content: "",
			ToolCalls: []llmcore.ToolCall{{
				ID:   nodeID,
				Type: "function",
				Function: llmcore.FunctionCall{
					Name:      tool,
					Arguments: string(argsJSON),
				},
			}},
		},
		{Role: roleTool, Content: output, ToolCallID: nodeID},
	}
}

// readNodeOutput reads one node's execution output from its fabric task
// envelope by node ID = task ID join. Returns "" when the task is not
// found or has no output yet (e.g. not yet completed).
func (c *plannerCognition) readNodeOutput(nodeID string) (string, error) {
	tk, err := c.fabric.Task(nodeID)
	if err != nil {
		return "", err
	}
	if tk.State != taskfabric.StateCompleted {
		return "", nil
	}
	dc, err := taskfabric.DecodeCheckpoint(tk.Checkpoint)
	if err != nil {
		return "", err
	}
	return extractOutputContent(dc.StepCheckpoint), nil
}

// growToolNodes grows one tool node per LLM tool call, then grows a new plan
// node depending on all of them. Each tool node carries its args in the
// arg. namespace and the session_id in Metadata (so ProjectStep
// propagates it to the envelope).
//
// The predecessor for the first tool node is the current plan node when it
// exists in the graph (subsequent quanta), or the root node when it doesn't
// (the first plan quantum — the initial plan task ID is not a graph node,
// only the root exists at session start). Tools within one round are chained
// sequentially (the next depends on the previous).
//
// Before growing each tool node, the planner checks the L1 ToolClass
// graph's enabled/budget metadata. enabled=false skips the node
// (constraint point: "节点长不长出来"). budget=N caps how many instances
// of this ToolClass can exist in the L2 graph per session. A nil L1 graph
// means no constraints (permissive default).
//
// Returns the number of tool nodes actually grown (0 when all calls were
// skipped by L1 constraints, in which case the caller forces an answer node).
func (c *plannerCognition) growToolNodes(
	ctx context.Context,
	g *L2Graph,
	task *models.Task,
	toolCalls []llmcore.ToolCall,
	sessionID string,
) (int, error) {
	depth := g.PlanDepth()
	grown := 0

	// Determine the predecessor for the first tool node: the current plan
	// node when it exists, otherwise the root (first plan quantum).
	prev := task.TaskID
	if !g.HasNode(prev) {
		prev = g.Root()
	}

	for seq, tc := range toolCalls {
		toolName := tc.Function.Name

		// Check L1 constraints before growing the node.
		if !c.isToolEnabled(toolName) {
			c.logger.Warn("planner: tool disabled by L1 constraint; skipping",
				"tool", toolName, "session", sessionID)
			continue
		}
		if !c.toolBudgetRemaining(g, toolName) {
			c.logger.Warn("planner: tool budget exhausted by L1 constraint; skipping",
				"tool", toolName, "session", sessionID)
			continue
		}

		nodeID := SessionNodeID(sessionID, depth+1, toolName, seq)

		// Parse the tool arguments.
		args := map[string]any{}
		if tc.Function.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				return grown, fmt.Errorf("parse tool %s args: %w", toolName, err)
			}
		}

		// Stamp session_id into Metadata so ProjectStep propagates it.
		metadata := args
		if metadata == nil {
			metadata = map[string]any{}
		}
		metadata[planMetadataKey] = sessionID

		if err := g.AddToolNode(ctx, nodeID, toolName, metadata, prev); err != nil {
			return grown, fmt.Errorf("add tool node %s: %w", nodeID, err)
		}
		// Chain sequential tools within the same round: the next tool
		// depends on this one (data flow).
		prev = nodeID
		grown++
	}

	// When every tool call was skipped by L1 constraints, do NOT grow a new
	// plan node — the caller forces an answer node instead. Growing a
	// plan node here would inflate PlanDepth and shift the answer node's
	// ID, breaking the "no tools grown → answer at this depth" contract.
	if grown == 0 {
		return grown, nil
	}

	// Grow a new plan node depending on the last tool node. The next plan
	// quantum reads the tool outputs and decides the next round.
	newPlanID := SessionNodeID(sessionID, depth+1, "plan", 0)
	planArgs := map[string]any{planMetadataKey: sessionID}
	if err := g.AddToolNode(ctx, newPlanID, "plan", planArgs, prev); err != nil {
		return grown, fmt.Errorf("add plan node %s: %w", newPlanID, err)
	}
	return grown, nil
}

// isToolEnabled reads the L1 ToolClass graph's "enabled" metadata for the
// given tool. Returns true (permissive) when the L1 graph is nil, the node
// is not found, or the metadata is missing/empty. Returns false only when
// the L1 node explicitly sets enabled="false". Reads through NodeMetadata
// (locked copy) so a concurrent evolution SetNodeMetadata cannot race.
func (c *plannerCognition) isToolEnabled(toolName string) bool {
	if c.l1 == nil {
		return true
	}
	nodeID := c.l1ToolClassID(toolName)
	md, ok := c.l1.NodeMetadata(nodeID)
	if !ok {
		return true // unknown ToolClass → permissive
	}
	val, ok := md[l1MetaEnabled]
	if !ok || val == "" {
		return true
	}
	return val != "false"
}

// toolBudgetRemaining checks whether the L2 graph still has budget for one
// more instance of the given ToolClass. budget=0 (or missing) = unlimited.
// budget=N means at most N instances of this ToolClass can exist in the L2
// graph at a time. The count is by ToolClass (toolName#argShape), not by
// tool name alone, so two different argument shapes of the same tool each
// get their own budget.
func (c *plannerCognition) toolBudgetRemaining(g *L2Graph, toolName string) bool {
	if c.l1 == nil {
		return true
	}
	nodeID := c.l1ToolClassID(toolName)
	md, ok := c.l1.NodeMetadata(nodeID)
	if !ok {
		return true // unknown ToolClass → permissive
	}
	budgetStr, ok := md[l1MetaBudget]
	if !ok || budgetStr == "" || budgetStr == "0" {
		return true // unlimited
	}
	budget, err := strconv.Atoi(budgetStr)
	if err != nil || budget <= 0 {
		return true // unparseable or unlimited
	}
	count := g.CountToolClass(toolName)
	return count < budget
}

// l1ToolPrior returns the L1 ToolClass "prior" hint for the given tool.
// Empty means no hint. prior is prompt-only: it never blocks growth,
// only guides the LLM. Reads through NodeMetadata (locked copy).
func (c *plannerCognition) l1ToolPrior(toolName string) string {
	if c.l1 == nil {
		return ""
	}
	nodeID := c.l1ToolClassID(toolName)
	md, ok := c.l1.NodeMetadata(nodeID)
	if !ok {
		return ""
	}
	return strings.TrimSpace(md[l1MetaPrior])
}

// l1Priors collects the non-empty prior hints for every known tool schema,
// sorted by tool name for determinism. Used to inject evolution guidance
// into the planner prompt (prior只进提示词).
func (c *plannerCognition) l1Priors() []string {
	if c.l1 == nil || c.binder == nil {
		return nil
	}
	var priors []string
	for _, s := range c.binder.GetToolSchemas() {
		if p := c.l1ToolPrior(s.Name); p != "" {
			priors = append(priors, s.Name+": "+p)
		}
	}
	return priors
}

// growAnswerNode grows a terminal answer node carrying the final response and
// completes the quantum with Done:true. The answer node's content is read by
// answerCognition when the scheduler executes it.
//
// resp is the LLM response this quantum produced (nil on the depth-guard
// path — no LLM call happened, so there is no usage to report); its token
// usage rides the answer node's task result metadata into the checkpoint
// envelope (the fitness cost channel, M4).
//
// The predecessor is the current plan node when it exists in the graph
// (subsequent quanta), or the root node when it doesn't (the first plan
// quantum). When the plan node IS in the graph, its predecessor (the last
// tool node) is the natural dependency for the answer.
func (c *plannerCognition) growAnswerNode(
	ctx context.Context,
	g *L2Graph,
	task *models.Task,
	content string,
	resp *llmcore.GenerateResponse,
) (*StepOutcome, error) {
	depth := g.PlanDepth()
	answerID := SessionNodeID(task.SessionID, depth+1, "answer", 0)

	// Determine predecessor: the current plan node when in graph, else root.
	pred := task.TaskID
	if !g.HasNode(pred) {
		pred = g.Root()
	}

	args := map[string]any{
		"content":       content,
		planMetadataKey: task.SessionID,
	}
	if err := g.AddToolNode(ctx, answerID, "answer", args, pred); err != nil {
		return nil, fmt.Errorf("agentfabric: planner cognition: add answer node: %w", err)
	}

	result := models.NewTaskResult(task.TaskID, task.AgentType)
	result.SetSuccess(nil, "planner grew answer node")
	result.Metadata = tokenUsageMetadata(resp)
	return &StepOutcome{Done: true, Result: result}, nil
}

// Token-usage metadata keys on StepOutcome.Result.Metadata — the quantum's
// LLM usage report. The scheduler's buildQuantumStep reads these keys when
// re-wrapping the envelope (accumulating into InputTokens/OutputTokens), so
// the key names are the cross-package contract (kernel/…/scheduler.go
// mirrors them in tokenUsageFromResult).
const (
	resultMetaInputTokens  = "input_tokens"
	resultMetaOutputTokens = "output_tokens"
)

// stampTokenUsage is the call-site marker for the M4 cost channel: the
// quantum that made an LLM call reports its usage. Kept as a no-op function
// (not inlined into the call sites) so the accounting point is grep-able and
// future metrics hook one place. resp may be nil (defensive; every call site
// has a non-nil response after the error check).
func stampTokenUsage(resp *llmcore.GenerateResponse) {}

// tokenUsageMetadata extracts the response's token usage into result
// metadata. A nil response or zero usage yields nil — the scheduler's
// accumulate step treats a missing map as "nothing to add", so a provider
// that reports no usage contributes nothing to the session total.
func tokenUsageMetadata(resp *llmcore.GenerateResponse) map[string]any {
	if resp == nil {
		return nil
	}
	in, out := resp.Usage.PromptTokens, resp.Usage.CompletionTokens
	if in <= 0 && out <= 0 {
		return nil
	}
	return map[string]any{
		resultMetaInputTokens:  in,
		resultMetaOutputTokens: out,
	}
}

// extractOutputContent reads the textual output from a step checkpoint. The
// scheduler's buildQuantumStep wraps the StepOutcome.Result into the
// checkpoint's StepCheckpoint as a {result, items, ...} map; this helper
// extracts the first item's Content field. Returns "" when the checkpoint
// does not carry a recognizable output shape.
func extractOutputContent(sc any) string {
	if sc == nil {
		return ""
	}
	m, ok := sc.(map[string]any)
	if !ok {
		return ""
	}
	// The scheduler stores {result: "ok", items: []*RecommendItem, ...} or
	// after a JSON round-trip {items: [{content: "...", ...}]}.
	raw, ok := m["items"]
	if !ok {
		return ""
	}
	switch items := raw.(type) {
	case []*models.RecommendItem:
		if len(items) > 0 {
			return items[0].Content
		}
	case []any:
		if len(items) > 0 {
			if im, ok := items[0].(map[string]any); ok {
				if c, ok := im["content"].(string); ok {
					return c
				}
			}
		}
	}
	return ""
}

// toCoreTool converts a resources.ToolSchema to a llmcore.Tool for the LLM.
func toCoreTool(s resources.ToolSchema) llmcore.Tool {
	return resources.ToolSchemaToLLMTool(s)
}
