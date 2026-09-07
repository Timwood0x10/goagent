package aresrecovery

import (
	"context"
	"fmt"
	"time"

	"github.com/Timwood0x10/ares/internal/fabric/agent"
	"github.com/Timwood0x10/ares/internal/fabric/task"
)

// Simulation sandbox (v0.3.0 M4-2): replay recorded events to verify recovery
// logic, or simulate failure scenarios to predict runtime behavior — all
// offline, on a scratch set of fabrics. The sandbox reuses the real Recovery
// subsystem so the replay exercises the same code paths as production.
//
// It is intentionally small: the sandbox drives the fabrics through the same
// primitives the Kernel uses (spawn/kill/acquire/expire/recover) and records
// the post-event task state, so a failure scenario's outcome is observable
// without a live runtime.

// Sandbox scripted event types and the scratch capability used by Replay /
// Simulate. Extracted as constants because they appear in the switch, the
// outcome records and the test/benchmark scripts (goconst).
const (
	// SandboxEventTaskCreate scripts task creation.
	SandboxEventTaskCreate = "task.create"
	// SandboxEventTaskAcquire scripts a task acquire.
	SandboxEventTaskAcquire = "task.acquire"
	// SandboxEventAgentSpawn scripts an agent spawn.
	SandboxEventAgentSpawn = "agent.spawn"
	// SandboxEventAgentKill scripts an agent kill (crash).
	SandboxEventAgentKill = "agent.kill"
	// SandboxEventLeaseExpire advances the clock past the lease TTL.
	SandboxEventLeaseExpire = "lease.expire"
	// SandboxEventRecoverAll runs the full recovery chain.
	SandboxEventRecoverAll = "recover.all"

	// sandboxCapability is the capability the scratch tasks/agents use.
	sandboxCapability = "code"
)

// SandboxEvent is one scripted event for Replay.
type SandboxEvent struct {
	// Type is one of the SandboxEvent* constants.
	Type string
	// TaskID / AgentID identify the event's subject.
	TaskID  string
	AgentID string
}

// SandboxOutcome records the task state after one replayed event.
type SandboxOutcome struct {
	// EventType echoes the scripted event.
	EventType string
	// TaskID / AgentID echo the scripted subject.
	TaskID  string
	AgentID string
	// TaskState is the fabric task state after the event ("" when the task
	// does not exist).
	TaskState string
	// Detail carries event-specific info (e.g. recovered count).
	Detail map[string]any
}

// Sandbox drives scratch fabrics through scripted or simulated events.
type Sandbox struct {
	tasks    *taskfabric.Fabric
	agents   *agentfabric.Fabric
	recovery *Recovery
	now      func() time.Time
}

// NewSandbox wires the sandbox to its own scratch fabrics and the recovery
// subsystem.
//
// Args:
//   - tasks: the scratch Task Fabric.
//   - agents: the scratch Agent Fabric.
//   - recovery: the Recovery subsystem wired to those fabrics.
//
// Returns:
//   - *Sandbox: ready to Replay / Simulate.
func NewSandbox(tasks *taskfabric.Fabric, agents *agentfabric.Fabric, recovery *Recovery) *Sandbox {
	return &Sandbox{tasks: tasks, agents: agents, recovery: recovery, now: time.Now}
}

// WithClock injects a controllable clock for deterministic lease-expiry
// simulation.
func (s *Sandbox) WithClock(now func() time.Time) *Sandbox {
	s.now = now
	s.tasks = s.tasks.WithClock(now)
	return s
}

// Replay executes a scripted event sequence and records the task state after
// every event. This verifies recovery logic offline: a scripted
// "agent.kill" → "lease.expire" → "recover.all" chain must leave the task
// recoverable (READY or re-acquired), proving Agent death ≠ Task death.
//
// Args:
//   - ctx: for event sinks.
//   - events: the scripted events, applied in order.
//
// Returns:
//   - []SandboxOutcome: one outcome per event.
//   - error: the first event that fails to apply.
func (s *Sandbox) Replay(ctx context.Context, events []SandboxEvent) ([]SandboxOutcome, error) {
	outcomes := make([]SandboxOutcome, 0, len(events))
	for _, ev := range events {
		detail := map[string]any{}
		var err error
		switch ev.Type {
		case SandboxEventTaskCreate:
			// Origin stays "" — sandbox scripted tasks are root tasks (no
			// agent caller); the sandbox replays fabric state, not provenance.
			err = s.tasks.Create(&taskfabric.Task{ID: ev.TaskID, Capability: sandboxCapability})
		case SandboxEventTaskAcquire:
			_, err = s.tasks.Acquire(ev.TaskID, ev.AgentID, time.Minute)
		case SandboxEventAgentSpawn:
			var a *agentfabric.Agent
			a, err = s.agents.Spawn(ctx, agentfabric.SpawnSpec{Identity: ev.AgentID, Capabilities: []string{sandboxCapability}})
			if err == nil {
				detail["spawned"] = a.Identity
			}
		case SandboxEventAgentKill:
			err = s.agents.Kill(ctx, ev.AgentID)
		case SandboxEventLeaseExpire:
			// Advance the clock past the lease TTL so the subsequent recovery
			// chain observes the expiry (deterministic via the injected
			// clock). The requeue itself happens inside "recover.all" through
			// RecoverFromAgentDeath, which sweeps expired leases first.
			s.tasks = s.tasks.WithClock(func() time.Time { return s.now().Add(10 * time.Minute) })
			detail["clock_advanced"] = true
		case SandboxEventRecoverAll:
			// RecoverFromAgentDeath runs the full chain: lease expiry requeue
			// → checkpoint resume → agent restart (it sweeps expired leases
			// internally, so the earlier "lease.expire" clock advance makes
			// the task recoverable here).
			detail["recovered"] = s.recovery.RecoverFromAgentDeath(ctx)
		default:
			return outcomes, fmt.Errorf("aresrecovery: unknown sandbox event %q", ev.Type)
		}
		if err != nil {
			return outcomes, fmt.Errorf("aresrecovery: sandbox %s: %w", ev.Type, err)
		}
		outcomes = append(outcomes, SandboxOutcome{
			EventType: ev.Type,
			TaskID:    ev.TaskID,
			AgentID:   ev.AgentID,
			TaskState: s.taskState(ev.TaskID),
			Detail:    detail,
		})
	}
	return outcomes, nil
}

// SimulationResult predicts the outcome of a failure scenario.
type SimulationResult struct {
	// Scenario is a human-readable description.
	Scenario string
	// DeadAgent is the simulated crashed agent.
	DeadAgent string
	// FinalTaskState is the task's state after the recovery chain ran
	// (expected: READY after lease expiry, or RUNNING/LEASED after re-acquire).
	FinalTaskState string
	// Recovered reports whether the recovery chain requeued the task.
	Recovered bool
}

// Simulate models an "agent death" scenario offline: the given agent is
// killed, its task's lease expires, and the recovery chain runs. The returned
// result predicts whether the task survives the agent's death (Agent death ≠
// Task death) and what state it ends in.
//
// Args:
//   - ctx: for event sinks.
//   - deadAgentID: the agent to simulate crashing.
//   - taskID: the task the dead agent was executing.
//
// Returns:
//   - *SimulationResult: the predicted outcome.
//   - error: when the scenario cannot be scripted.
func (s *Sandbox) Simulate(ctx context.Context, deadAgentID, taskID string) (*SimulationResult, error) {
	if err := s.agents.Kill(ctx, deadAgentID); err != nil {
		return nil, fmt.Errorf("aresrecovery: sandbox simulate kill: %w", err)
	}
	// Expire the dead agent's lease (advance the clock) and run recovery.
	// RecoverFromAgentDeath sweeps expired leases internally, so we call it
	// directly — calling RequeueExpiredLeases first would drain the expired
	// set, making the internal sweep see nothing (double-requeue bug).
	s.tasks = s.tasks.WithClock(func() time.Time { return s.now().Add(10 * time.Minute) })
	recovered := s.recovery.RecoverFromAgentDeath(ctx)

	return &SimulationResult{
		Scenario:       "agent death → lease expiry → recovery chain",
		DeadAgent:      deadAgentID,
		FinalTaskState: s.taskState(taskID),
		Recovered:      recovered > 0,
	}, nil
}

// taskState returns the fabric task state string ("" when unknown).
func (s *Sandbox) taskState(taskID string) string {
	t, err := s.tasks.Task(taskID)
	if err != nil {
		return ""
	}
	return string(t.State)
}
