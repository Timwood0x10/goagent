package agentfabric

import (
	"context"

	"github.com/Timwood0x10/ares/api/core"
	"github.com/Timwood0x10/ares/internal/core/models"
	resources "github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// Cognition is the execution contract for one quantum of cognitive work
// (design §13: "Agent decides. Kernel enforces."). Each invocation of
// ExecuteStep runs one quantum — a bounded reasoning/action step that either
// completes the task (Done), yields progress for resumption (Checkpoint), or
// fails.
//
// The interface is defined in the consumer package (agentfabric).
// Implementations live in agentfabric or are injected via
// CognitionFactory at spawn time.
type Cognition interface {
	// ExecuteStep runs one quantum and returns the outcome. The task carries
	// the shared state (Payload, Checkpoint envelope) and the agent's identity
	// is known from the caller.
	//
	// Args:
	//   - ctx: context for cancellation and deadlines.
	//   - task: the models.Task with its Payload and checkpoint state.
	//
	// Returns:
	//   - *StepOutcome with Done/Checkpoint/Result.
	//   - error on unrecoverable failure (recoverable failures are encoded in
	//     StepOutcome).
	ExecuteStep(ctx context.Context, task *models.Task) (*StepOutcome, error)
}

// StepOutcome is the result of one execution quantum (Execution Model).
// Semantic parity with sub.StepOutcome so the migration test suite can
// verify both produce equivalent results.
type StepOutcome struct {
	// Done is true when the task is complete (Result is set).
	Done bool
	// Checkpoint is the durable progress state for resumption (yield). It is
	// carried inside the taskfabric CheckpointEnvelope.StepCheckpoint so the
	// scheduler re-wraps it around the submission metadata.
	Checkpoint any
	// Result is the final task result, set only when Done is true.
	Result *models.TaskResult
}

// CognitionFactory is a function that, given a set of capabilities, produces
// a Cognition implementation. This is the mechanism by which SpawnSpec wires
// execution capability into a newly created agent — the Kernel validates the
// spec, then the factory constructs the binding (LLM, tools, prompts) for the
// declared capabilities.
type CognitionFactory func(capabilities []string) Cognition

// CognitionFunc adapts a plain function to the Cognition interface so any
// executor with the right signature (e.g. an agentsyscall.Executor, or a
// bound sub.Agent) can be injected as an agent's execution body without a
// new concrete type (spawn 的 agent 带执行体).
// The adapter lives in the owner package (agentfabric).
type CognitionFunc func(ctx context.Context, task *models.Task) (*StepOutcome, error)

// ExecuteStep implements Cognition.
func (f CognitionFunc) ExecuteStep(ctx context.Context, task *models.Task) (*StepOutcome, error) {
	return f(ctx, task)
}

// ChatClient is the minimal LLM chat surface the cognition bodies need
// (interface at the consumer). It sends chat messages with
// tool support; the optional params map carries per-call overrides
// (temperature, max_tokens, top_k) from the active evolution strategy so
// live plan growth can be steered at runtime. (Relocated from the retired
// chat loop; the contract is unchanged.)
type ChatClient interface {
	Chat(ctx context.Context, messages []*core.LLMMessage, tools []core.Tool, params map[string]any) (*core.GenerateResponse, error)
}

// ToolBinder is the minimal tool surface the cognition bodies need
// (interface at the consumer). The planner shows the LLM what
// tools exist (schemas); the tool body executes one call per quantum.
// (Relocated from the retired chat loop; the contract is unchanged.)
type ToolBinder interface {
	CallTool(ctx context.Context, name string, args map[string]any) (any, error)
	ListTools() []string
	IsToolIdempotent(name string) bool
	GetToolSchemas() []resources.ToolSchema
}

// ExecuteStep runs one quantum of cognitive work through the agent's injected
// execution body (spawn → ExecuteStep 直接可执行). It delegates to the
// Cognition produced by SpawnSpec.CognitionFactory, first stamping the task
// payload with the agent's identity (executingAgentKey) so a Cognition shared
// across agents can join the task back to its executor. An agent spawned
// without a factory returns ErrAgentNotExecutable — it is managed (spawn/kill/
// recover) but not schedulable.
//
// Args:
//   - ctx: context for the underlying execution.
//   - task: the models.Task with its Payload and checkpoint state.
//
// Returns:
//   - *StepOutcome (Done/Checkpoint/Result).
//   - error: ErrAgentNotExecutable when no Cognition was injected.
func (a *Agent) ExecuteStep(ctx context.Context, task *models.Task) (*StepOutcome, error) {
	a.mu.RLock()
	c := a.cognition
	a.mu.RUnlock()
	if c == nil {
		return nil, ErrAgentNotExecutable
	}
	if task != nil {
		task.Payload = withExecutingAgent(task.Payload, a.Identity)
	}
	return c.ExecuteStep(ctx, task)
}

// executingAgentKey is the reserved task-payload key carrying the identity of
// the agent EXECUTING the current quantum. One shared Cognition (the L2
// router/planner serves every agent), so the task is the only carrier of the
// executor's identity — the join key for reading the executing agent's
// cognitive state (the spawn-time experience prior, M4.3). Unprefixed like
// session_id, so it rides the envelope namespace and never reaches tool args
// (argsFromPayload extracts the arg. namespace only).
const executingAgentKey = "executing_agent_id"

// withExecutingAgent returns a copy of payload stamped with the executing
// agent's identity. The copy is load-bearing: the payload map the scheduler
// hands down is the SAME map referenced by the durable checkpoint envelope
// (ToModelTask → DecodeCheckpoint does not copy), so an in-place write would
// persist one quantum's executor into the envelope and mis-attribute later
// quanta to a dead or preempted agent. The stamp is quantum-scoped by
// construction: it lives only on the models.Task given to the Cognition.
func withExecutingAgent(payload map[string]any, agentID string) map[string]any {
	out := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		out[k] = v
	}
	out[executingAgentKey] = agentID
	return out
}
