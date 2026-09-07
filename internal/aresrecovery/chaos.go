package aresrecovery

import (
	"context"
	"fmt"
	"sync"

	"github.com/Timwood0x10/ares/internal/fabric/agent"
)

// Chaos is the Failure Injection + Recovery Verification harness (design §10 +
// P5): it deliberately kills agents to prove the Runtime (Recovery subsystem)
// can restore their tasks. Chaos is SEPARATE from Recovery — Chaos breaks,
// Recovery fixes. This harness wires the two so a chaos injection is followed
// by a recovery verification.
//
// Unlike the existing ares_runtime arena (which does fault injection on the
// old leader/sub agents), this Chaos targets the new Kernel model
// (agentfabric + taskfabric): it kills agentfabric agents and verifies
// taskfabric tasks survive via the Recovery subsystem.
type Chaos struct {
	agents   *agentfabric.Fabric
	recovery *Recovery
	mu       sync.Mutex
	injected map[string]FailureType // agentID -> injected failure
}

// FailureType enumerates the chaos failures this harness can inject.
type FailureType string

const (
	// FailureKill is a hard kill (crash): the agent is removed immediately.
	FailureKill FailureType = "kill"
	// FailureSuspend is a soft pause: the agent is suspended (simulates a
	// hang/stall, not a crash).
	FailureSuspend FailureType = "suspend"
)

// NewChaos wires the chaos harness to the agent fabric and recovery subsystem.
func NewChaos(agents *agentfabric.Fabric, recovery *Recovery) *Chaos {
	return &Chaos{
		agents:   agents,
		recovery: recovery,
		injected: make(map[string]FailureType),
	}
}

// InjectFailure deliberately kills or suspends an agent (design: Failure
// Injection). The recovery is NOT triggered here — call VerifyRecovery to
// prove the Runtime survives. This separation lets tests assert "task is
// stranded after injection" before "task is recovered after VerifyRecovery".
//
// Args:
//   - ctx: for event sinks.
//   - agentID: the target agent.
//   - failure: the failure type to inject.
//
// Returns:
//   - error: agentfabric.ErrAgentNotFound / agentfabric.ErrAgentRunning.
func (c *Chaos) InjectFailure(ctx context.Context, agentID string, failure FailureType) error {
	switch failure {
	case FailureKill:
		if err := c.agents.Kill(ctx, agentID); err != nil {
			return fmt.Errorf("chaos: inject kill %s: %w", agentID, err)
		}
	case FailureSuspend:
		if err := c.agents.Suspend(ctx, agentID); err != nil {
			return fmt.Errorf("chaos: inject suspend %s: %w", agentID, err)
		}
	default:
		return fmt.Errorf("chaos: unknown failure type %q", failure)
	}
	c.mu.Lock()
	c.injected[agentID] = failure
	c.mu.Unlock()
	return nil
}

// VerifyRecovery triggers the Recovery subsystem and proves the Runtime
// survives the injected failures: expired leases are requeued, checkpoints
// are resumed, agents are restarted. Returns the number of tasks fully
// recovered. This is the "Recovery Verification" half of Chaos.
//
// Args:
//   - ctx: for event sinks.
//
// Returns:
//   - int: number of tasks recovered.
func (c *Chaos) VerifyRecovery(ctx context.Context) int {
	return c.recovery.RecoverFromAgentDeath(ctx)
}

// InjectedFailures returns a copy of the injected failure map (agentID ->
// FailureType) for verification.
func (c *Chaos) InjectedFailures() map[string]FailureType {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]FailureType, len(c.injected))
	for k, v := range c.injected {
		out[k] = v
	}
	return out
}

// EvolutionAdapter is the Runtime Adaptation surface (design P5: Evolution
// — Runtime Adaptation: change scheduling policy / agent population / spawn
// decisions based on observed behavior). It lets the Evolution system swap
// the active scheduling policy and request agent population changes
// (spawn/retire) without the Runtime understanding evolution semantics.
//
// "Agent decides; Kernel enforces" — Evolution decides what to adapt; the
// Kernel enforces the changes through the existing primitives.
type EvolutionAdapter struct {
	tasks  *agentfabric.Fabric
	agents *agentfabric.Fabric
}

// NewEvolutionAdapter wires the evolution adapter to the fabrics.
func NewEvolutionAdapter(tasks *agentfabric.Fabric, agents *agentfabric.Fabric) *EvolutionAdapter {
	// tasks is intentionally unused for now — the adapter's surface is
	// population changes (spawn/retire); scheduling policy changes will plug
	// into taskfabric's Schedule in a future iteration.
	return &EvolutionAdapter{tasks: tasks, agents: agents}
}

// AdaptPopulation spawns or retires agents based on the Evolution system's
// decision (design: agent population adaptation). The Evolution system
// computes the desired population delta; this adapter applies it through the
// existing spawn/retire primitives — the Kernel enforces, Evolution decides.
//
// Args:
//   - ctx: for event sinks.
//   - spawn: the specs of agents to spawn.
//   - retire: the ids of agents to retire.
//
// Returns:
//   - []string: the spawned agent ids.
//   - error: the first retire/spawn error encountered.
func (e *EvolutionAdapter) AdaptPopulation(ctx context.Context, spawn []agentfabric.SpawnSpec, retire []string) ([]string, error) {
	spawned := make([]string, 0, len(spawn))
	for _, spec := range spawn {
		a, err := e.agents.Spawn(ctx, spec)
		if err != nil {
			return spawned, fmt.Errorf("evolution: spawn %s: %w", spec.Identity, err)
		}
		spawned = append(spawned, a.Identity)
	}
	for _, id := range retire {
		if err := e.agents.Retire(ctx, id); err != nil {
			return spawned, fmt.Errorf("evolution: retire %s: %w", id, err)
		}
	}
	return spawned, nil
}
