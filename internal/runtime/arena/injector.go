package arena

import (
	"context"
	"errors"
	"fmt"
	"time"

	ares_runtime "github.com/Timwood0x10/ares/internal/ares_runtime"
)

var (
	// ErrRuntimeNil indicates the ares_runtime dependency was not provided.
	ErrRuntimeNil = errors.New("arena: ares_runtime is nil")
	// ErrDAGNil indicates the DAG dependency was not provided.
	ErrDAGNil = errors.New("arena: dag is nil")
	// ErrLeaderNotFound indicates no agent with type "leader" was found.
	ErrLeaderNotFound = errors.New("arena: leader agent not found")
	// ErrOrchestratorNotFound indicates no agent with type "orchestrator" was found.
	ErrOrchestratorNotFound = errors.New("arena: orchestrator agent not found")
)

// RuntimeProvider is the subset of ares_runtime capabilities needed by the arena.
type RuntimeProvider interface {
	StopAgent(ctx context.Context, agentID string) error
	ListAgents() []ares_runtime.AgentInfo
	PauseAgent(ctx context.Context, agentID string) error
	ResumeAgent(ctx context.Context, agentID string) error
	SlowAgent(ctx context.Context, agentID string, delay time.Duration) error
	PartitionNetwork(ctx context.Context, agentID string) error
	ToolTimeout(ctx context.Context, agentID string, timeout time.Duration) error
	CorruptMemory(ctx context.Context, agentID string) error
	DisconnectMCP(ctx context.Context, agentID string) error
	InjectLLMFailure(ctx context.Context, agentID string, errType string) error
}

// DAGProvider is the subset of DAG mutation capabilities needed by the arena.
type DAGProvider interface {
	ListNodes(ctx context.Context) []string
	ListEdges(ctx context.Context) [][2]string
	RemoveNode(ctx context.Context, id string) error
	RemoveEdge(ctx context.Context, from, to string) error
}

// Injector wraps existing ares_runtime/DAG APIs to inject chaos.
// It does NOT implement recovery; the existing resurrection plugin and
// failover handle that automatically.
type Injector struct {
	ares_runtime RuntimeProvider
	dag          DAGProvider
}

// NewInjector creates an Injector with the given dependencies.
// Either dependency may be nil; calling the corresponding methods will return
// ErrRuntimeNil or ErrDAGNil in that case.
func NewInjector(rt RuntimeProvider, dag DAGProvider) *Injector {
	return &Injector{
		ares_runtime: rt,
		dag:          dag,
	}
}

// KillAgent stops an agent by ID via the ares_runtime.
func (in *Injector) KillAgent(ctx context.Context, id string) error {
	if in.ares_runtime == nil {
		return ErrRuntimeNil
	}
	log.Warn("arena: killing agent", "agent_id", id)
	if err := in.ares_runtime.StopAgent(ctx, id); err != nil {
		return fmt.Errorf("arena: kill agent %s: %w", id, err)
	}
	return nil
}

// KillOrchestrator finds the orchestrator agent and stops it.
func (in *Injector) KillOrchestrator(ctx context.Context) (string, error) {
	if in.ares_runtime == nil {
		return "", ErrRuntimeNil
	}
	orchID := ""
	for _, info := range in.ares_runtime.ListAgents() {
		if info.Type == "orchestrator" {
			orchID = info.ID
			break
		}
	}
	if orchID == "" {
		return "", ErrOrchestratorNotFound
	}
	log.Warn("arena: assassinating orchestrator", "agent_id", orchID)
	if err := in.ares_runtime.StopAgent(ctx, orchID); err != nil {
		return "", fmt.Errorf("arena: kill orchestrator %s: %w", orchID, err)
	}
	return orchID, nil
}

// NetworkPartition isolates an agent from the network.
func (in *Injector) NetworkPartition(ctx context.Context, id string) error {
	if in.ares_runtime == nil {
		return ErrRuntimeNil
	}
	log.Warn("arena: partitioning network for agent", "agent_id", id)
	if err := in.ares_runtime.PartitionNetwork(ctx, id); err != nil {
		return fmt.Errorf("arena: network partition %s: %w", id, err)
	}
	return nil
}

// KillLeader finds the leader agent and stops it.
func (in *Injector) KillLeader(ctx context.Context) (string, error) {
	if in.ares_runtime == nil {
		return "", ErrRuntimeNil
	}
	leaderID := ""
	for _, info := range in.ares_runtime.ListAgents() {
		if info.Type == "leader" {
			leaderID = info.ID
			break
		}
	}
	if leaderID == "" {
		return "", ErrLeaderNotFound
	}
	log.Warn("arena: assassinating leader", "agent_id", leaderID)
	if err := in.ares_runtime.StopAgent(ctx, leaderID); err != nil {
		return "", fmt.Errorf("arena: kill leader %s: %w", leaderID, err)
	}
	return leaderID, nil
}

// RemoveNode removes a node from the DAG.
func (in *Injector) RemoveNode(ctx context.Context, id string) error {
	if in.dag == nil {
		return ErrDAGNil
	}
	log.Warn("arena: removing node from DAG", "node_id", id)
	if err := in.dag.RemoveNode(ctx, id); err != nil {
		return fmt.Errorf("arena: remove node %s: %w", id, err)
	}
	return nil
}

// DAGNodes returns the list of current DAG node IDs.
func (in *Injector) DAGNodes(ctx context.Context) []string {
	if in.dag == nil {
		return nil
	}
	return in.dag.ListNodes(ctx)
}

// DAGEdges returns the list of current DAG edges as from/to pairs.
func (in *Injector) DAGEdges(ctx context.Context) [][2]string {
	if in.dag == nil {
		return nil
	}
	return in.dag.ListEdges(ctx)
}

// RemoveEdge removes a directed edge from the DAG.
func (in *Injector) RemoveEdge(ctx context.Context, from, to string) error {
	if in.dag == nil {
		return ErrDAGNil
	}
	log.Warn("arena: removing edge from DAG", "from", from, "to", to)
	if err := in.dag.RemoveEdge(ctx, from, to); err != nil {
		return fmt.Errorf("arena: remove edge %s->%s: %w", from, to, err)
	}
	return nil
}

// PauseAgent suspends an agent temporarily via the ares_runtime.
func (in *Injector) PauseAgent(ctx context.Context, id string) error {
	if in.ares_runtime == nil {
		return ErrRuntimeNil
	}
	log.Warn("arena: pausing agent", "agent_id", id)
	if err := in.ares_runtime.PauseAgent(ctx, id); err != nil {
		return fmt.Errorf("arena: pause agent %s: %w", id, err)
	}
	return nil
}

// ResumeAgent resumes a previously paused agent via the ares_runtime.
func (in *Injector) ResumeAgent(ctx context.Context, id string) error {
	if in.ares_runtime == nil {
		return ErrRuntimeNil
	}
	log.Warn("arena: resuming agent", "agent_id", id)
	if err := in.ares_runtime.ResumeAgent(ctx, id); err != nil {
		return fmt.Errorf("arena: resume agent %s: %w", id, err)
	}
	return nil
}

// SlowAgent makes an agent artificially slow by adding a processing delay.
func (in *Injector) SlowAgent(ctx context.Context, id string, delay time.Duration) error {
	if in.ares_runtime == nil {
		return ErrRuntimeNil
	}
	log.Warn("arena: slowing agent", "agent_id", id, "delay", delay)
	if err := in.ares_runtime.SlowAgent(ctx, id, delay); err != nil {
		return fmt.Errorf("arena: slow agent %s: %w", id, err)
	}
	return nil
}

// ToolTimeout injects a tool timeout fault on an agent via the ares_runtime.
func (in *Injector) ToolTimeout(ctx context.Context, id string, timeout time.Duration) error {
	if in.ares_runtime == nil {
		return ErrRuntimeNil
	}
	log.Warn("arena: injecting tool timeout", "agent_id", id, "timeout", timeout)
	if err := in.ares_runtime.ToolTimeout(ctx, id, timeout); err != nil {
		return fmt.Errorf("arena: tool timeout %s: %w", id, err)
	}
	return nil
}

// CorruptMemory injects a memory corruption fault on an agent via the ares_runtime.
func (in *Injector) CorruptMemory(ctx context.Context, id string) error {
	if in.ares_runtime == nil {
		return ErrRuntimeNil
	}
	log.Warn("arena: corrupting memory", "agent_id", id)
	if err := in.ares_runtime.CorruptMemory(ctx, id); err != nil {
		return fmt.Errorf("arena: corrupt memory %s: %w", id, err)
	}
	return nil
}

// DisconnectMCP injects an MCP disconnection fault on an agent via the ares_runtime.
func (in *Injector) DisconnectMCP(ctx context.Context, id string) error {
	if in.ares_runtime == nil {
		return ErrRuntimeNil
	}
	log.Warn("arena: disconnecting MCP", "agent_id", id)
	if err := in.ares_runtime.DisconnectMCP(ctx, id); err != nil {
		return fmt.Errorf("arena: disconnect MCP %s: %w", id, err)
	}
	return nil
}

// InjectLLMFailure injects an LLM failure fault on an agent via the ares_runtime.
func (in *Injector) InjectLLMFailure(ctx context.Context, id string, errType string) error {
	if in.ares_runtime == nil {
		return ErrRuntimeNil
	}
	log.Warn("arena: injecting LLM failure", "agent_id", id, "error_type", errType)
	if err := in.ares_runtime.InjectLLMFailure(ctx, id, errType); err != nil {
		return fmt.Errorf("arena: inject LLM failure %s: %w", id, err)
	}
	return nil
}

// AvailableAgentIDs returns the IDs of all agents known to the ares_runtime.
// Returns an empty slice if the ares_runtime is nil.
func (in *Injector) AvailableAgentIDs() []string {
	if in.ares_runtime == nil {
		return nil
	}
	infos := in.ares_runtime.ListAgents()
	ids := make([]string, 0, len(infos))
	for _, info := range infos {
		ids = append(ids, info.ID)
	}
	return ids
}
