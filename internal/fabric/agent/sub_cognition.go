package agentfabric

import (
	"context"

	"github.com/Timwood0x10/ares/internal/agents/sub"
	"github.com/Timwood0x10/ares/internal/core/models"
)

// SubAgentCognition adapts a sub.Agent (the legacy quantum executor) to the
// agentfabric.Cognition contract. It is the A1 migration/parity adapter: the
// agentfabric default execution body is ChatCognition (the tool-loop logic
// moved down from the sub package — aresos-agentos-plan A1.4), and this
// adapter keeps the sub executor reachable so the migration parity test
// (TestSubAgentCognitionSemanticsParity) can assert StepOutcome semantics
// match by construction (Done/Checkpoint/Result).
//
// The PEER production path (createPeerAgents, the default) does NOT use this
// adapter: it spawns fabric agents with ChatCognition
// (internal/fabric/agent/chat_cognition.go), the tool-loop execution logic
// MOVED DOWN from the sub package (aresos-agentos-plan A1.4: tool-loop 下沉到
// agentfabric 作为默认实现).
//
// The legacy leader runtime that drove the sub executor through
// TaskPlanner/TaskDispatcher was removed in v0.4.0 (C1); this adapter now
// survives only as the parity-test fixture and a library-level fallback for
// callers that still construct a sub.Agent directly.
type SubAgentCognition struct {
	agent sub.Agent
}

// NewSubAgentCognition wraps a sub.Agent as an agentfabric.Cognition.
func NewSubAgentCognition(agent sub.Agent) *SubAgentCognition {
	return &SubAgentCognition{agent: agent}
}

// ExecuteStep delegates to the wrapped sub.Agent's ExecuteStep and converts
// the outcome. Semantics are identical by construction — the same executor
// runs the same quantum.
func (c *SubAgentCognition) ExecuteStep(ctx context.Context, task *models.Task) (*StepOutcome, error) {
	out, err := c.agent.ExecuteStep(ctx, task)
	if err != nil {
		return nil, err
	}
	return &StepOutcome{
		Done:       out.Done,
		Checkpoint: out.Checkpoint,
		Result:     out.Result,
	}, nil
}
