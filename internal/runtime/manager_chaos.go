package runtime

import (
	"context"
	"fmt"
	"time"

	"github.com/Timwood0x10/ares/internal/agents/base"
	"github.com/Timwood0x10/ares/internal/ares_events"
)

// AgentInfo holds agent metadata for external consumers like the dashboard.
type AgentInfo struct {
	ID       string
	Type     string
	Status   string
	Restarts int
	Paused   bool
}

// ListAgents returns metadata for all managed agents.
func (m *Manager) ListAgents() []AgentInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	infos := make([]AgentInfo, 0, len(m.agents))
	for id, ma := range m.agents {
		if ma.agent == nil {
			continue
		}
		infos = append(infos, AgentInfo{
			ID:       id,
			Type:     string(ma.agent.Type()),
			Status:   string(ma.agent.Status()),
			Restarts: ma.restarts,
			Paused:   ma.paused,
		})
	}

	return infos
}

// GetAgentInfo returns metadata for a specific agent.
func (m *Manager) GetAgentInfo(agentID string) (*AgentInfo, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ma, ok := m.agents[agentID]
	if !ok || ma.agent == nil {
		return nil, false
	}

	return &AgentInfo{
		ID:       agentID,
		Type:     string(ma.agent.Type()),
		Status:   string(ma.agent.Status()),
		Restarts: ma.restarts,
		Paused:   ma.paused,
	}, true
}

// ── Arena Chaos Engineering Fault Injection ───────────────────────────

// PauseAgent suspends an agent's goroutine without destroying its state, so
// ResumeAgent can relaunch the SAME in-memory instance. Unlike StopAgent it
// does NOT set the permanent `stopped` flag: the managedAgent entry stays in
// m.agents and the agent object is preserved. Paused agents are skipped by
// healthCheck and NotifyAgentDead (no resurrection while paused).
func (m *Manager) PauseAgent(ctx context.Context, agentID string) error {
	log.Info("[arena] PauseAgent", "agent", agentID)
	m.mu.Lock()
	ma, exists := m.agents[agentID]
	if !exists {
		m.mu.Unlock()
		return ErrAgentNotFound
	}
	ma.paused = true
	ma.operatorIntent = true
	cancel := ma.cancel
	agent := ma.agent
	m.mu.Unlock()

	// Cancel the managed goroutine context first, then stop the agent
	// gracefully. The agent instance is intentionally NOT replaced, so the
	// in-memory state survives for ResumeAgent.
	if cancel != nil {
		cancel()
	}
	if agent != nil {
		stopCtx, stopCancel := context.WithTimeout(ctx, m.config.AgentStopTimeout)
		defer stopCancel()
		if err := agent.Stop(stopCtx); err != nil {
			log.Warn("runtime: agent pause stop failed", "agent_id", agentID, "error", err)
		}
	}

	m.emitEvent(ctx, agentID, ares_events.EventAgentStopped, map[string]any{
		FieldAgentID: agentID,
		FieldReason:  "pause",
	})

	log.Info("runtime: agent paused", "agent_id", agentID)
	return nil
}

// ResumeAgent relaunches a previously paused agent using its SAME in-memory
// instance: no factory rebuild, no restart counter increment, and any state
// accumulated before the pause is preserved. It is a no-op for agents that
// are not paused. A fresh cancellable context is stored on the managedAgent
// so a later StopAgent/PauseAgent can cancel the new goroutine.
func (m *Manager) ResumeAgent(ctx context.Context, agentID string) error {
	log.Info("[arena] ResumeAgent", "agent", agentID)
	m.mu.Lock()
	ma, exists := m.agents[agentID]
	if !exists {
		m.mu.Unlock()
		return ErrAgentNotFound
	}
	if !ma.paused {
		m.mu.Unlock()
		return nil // not paused: nothing to resume
	}
	agentCtx, agentCancel := context.WithCancel(m.getGctx())
	ma.paused = false
	ma.operatorIntent = false // resume clears operator intent: future deaths may resurrect again
	ma.cancel = agentCancel
	agent := ma.agent
	m.mu.Unlock()

	if agent != nil {
		m.launchAgentGoroutine(agentCtx, agentID, agent)
	}

	m.emitEvent(ctx, agentID, ares_events.EventAgentStarted, map[string]any{
		FieldAgentID: agentID,
		FieldReason:  "resume",
	})

	log.Info("runtime: agent resumed", "agent_id", agentID)
	return nil
}

// SlowAgent adds an artificial latency for an agent's operations. The delay is
// read per-execution at the Process/ProcessStream boundary by
// chaosWrappedAgent, so it takes effect on the next execution without
// restarting the agent.
func (m *Manager) SlowAgent(_ context.Context, agentID string, delay time.Duration) error {
	log.Info("[arena] SlowAgent", "agent", agentID, "delay", delay.String())
	m.setChaosConfig(agentID, func(e *chaosEntry) { e.slowDelay = delay })
	return nil
}

// chaosFault returns the active fault error for the agent, or nil when no
// fault is configured. It is read at the Process boundary by
// chaosWrappedAgent so injections take effect on the next execution without
// restarting the agent. Read under RLock; config is written by the arena
// injectors under Lock.
func (m *Manager) chaosFault(agentID string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c := m.chaosConfig[agentID]
	switch {
	case c.networkPartitioned:
		return fmt.Errorf("chaos: network partition injected for agent %s", agentID)
	case c.memoryCorrupt:
		return fmt.Errorf("chaos: memory corruption injected for agent %s", agentID)
	case c.mcpDisconnected:
		return fmt.Errorf("chaos: MCP disconnected injected for agent %s", agentID)
	case c.llmFailureType != "":
		return fmt.Errorf("chaos: LLM failure (%s) injected for agent %s", c.llmFailureType, agentID)
	default:
		return nil
	}
}

// chaosSlowDelay returns the configured SlowAgent delay for the agent, or zero
// when no slow injection is configured. Read at the Process/ProcessStream
// boundary by chaosWrappedAgent so the delay takes effect on the next execution
// without restarting the agent. Read under RLock; config is written by
// SlowAgent under Lock.
func (m *Manager) chaosSlowDelay(agentID string) time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.chaosConfig[agentID].slowDelay
}

// chaosToolTimeout returns the configured ToolTimeout deadline for the agent, or
// zero when no timeout is configured. Read at the Process/ProcessStream
// boundary by chaosWrappedAgent so the deadline takes effect on the next
// execution without restarting the agent, and is scoped to a single execution
// (never the agent's Start/lifecycle context). Read under RLock; config is
// written by ToolTimeout under Lock.
func (m *Manager) chaosToolTimeout(agentID string) time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.chaosConfig[agentID].toolTimeout
}

// chaosWait blocks for d, respecting ctx cancellation. Returns ctx.Err() if the
// context is cancelled before the delay elapses, nil otherwise. Used by
// chaosWrappedAgent to inject SlowAgent latency in a ctx-aware way (a bare
// time.Sleep would ignore cancellation and delay agent shutdown).
func chaosWait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// chaosWrappedAgent decorates a base.Agent so fault injections take effect at
// the Process/ProcessStream boundary. All other methods are promoted from the
// embedded base.Agent unchanged. Only agents registered via StartAgent (or
// rebuilt by RestartAgent) are wrapped; the wrap is transparent to callers.
type chaosWrappedAgent struct {
	base.Agent
	m  *Manager
	id string
}

// Process injects the configured fault (if any), SlowAgent latency, and a
// per-call ToolTimeout deadline before delegating to the wrapped agent. All
// injections are read from chaosConfig at this boundary so they take effect on
// the next execution without restarting the agent, and are scoped to this
// single call — never the agent's Start/lifecycle context.
func (w *chaosWrappedAgent) Process(ctx context.Context, input any) (any, error) {
	if err := w.m.chaosFault(w.id); err != nil {
		return nil, err
	}
	// SlowAgent: inject artificial latency before execution.
	if err := chaosWait(ctx, w.m.chaosSlowDelay(w.id)); err != nil {
		return nil, err
	}
	// ToolTimeout: derive a per-call deadline so a short tool timeout cannot
	// kill the long-running agent goroutine (Start), only this execution.
	if timeout := w.m.chaosToolTimeout(w.id); timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
		return w.Agent.Process(ctx, input)
	}
	return w.Agent.Process(ctx, input)
}

// ProcessStream injects the configured fault (if any), SlowAgent latency, and a
// per-call ToolTimeout deadline before delegating. On fault injection the
// returned channel is closed immediately and the fault error is returned,
// matching the ProcessStream contract.
func (w *chaosWrappedAgent) ProcessStream(ctx context.Context, input any) (<-chan base.AgentEvent, error) {
	if err := w.m.chaosFault(w.id); err != nil {
		ch := make(chan base.AgentEvent)
		close(ch)
		return ch, err
	}
	// SlowAgent: inject artificial latency before opening the stream.
	if err := chaosWait(ctx, w.m.chaosSlowDelay(w.id)); err != nil {
		ch := make(chan base.AgentEvent)
		close(ch)
		return ch, err
	}
	timeout := w.m.chaosToolTimeout(w.id)
	if timeout <= 0 {
		return w.Agent.ProcessStream(ctx, input)
	}
	// ToolTimeout: scope a per-call deadline to this stream so the fault takes
	// effect on the next execution without restarting the agent, and never
	// affects the agent's Start/lifecycle context.
	streamCtx, cancel := context.WithTimeout(ctx, timeout)
	out, err := w.Agent.ProcessStream(streamCtx, input)
	if err != nil {
		cancel()
		return out, err
	}
	// The returned channel outlives this call, so cancel cannot be deferred
	// here. relay is a managed worker with a stop signal: it exits (and
	// releases the timeout context) when the upstream channel closes (agent
	// completion) or streamCtx is cancelled (timeout/parent cancel). Bounded —
	// it cannot leak indefinitely.
	relay := make(chan base.AgentEvent)
	go func() {
		defer cancel()
		defer close(relay)
		for {
			select {
			case ev, ok := <-out:
				if !ok {
					return
				}
				select {
				case relay <- ev:
				case <-streamCtx.Done():
					return
				}
			case <-streamCtx.Done():
				return
			}
		}
	}()
	return relay, nil
}

// Unwrap returns the underlying agent, exposing the raw instance so callers
// can assert optional concrete types (e.g. StatefulAgent subtypes) through the
// transparent chaos-injection decorator. It is the exported counterpart of
// chaosUnwrap and lets external packages reach the inner agent without depending
// on the unexported chaosWrappedAgent type.
func (w *chaosWrappedAgent) Unwrap() base.Agent {
	return w.Agent
}

// UnwrappableAgent is an agent that decorates another agent and can expose the
// inner instance. Callers that need the concrete underlying agent (for example
// to assert a StatefulAgent subtype) should unwrap transparent decorators such
// as the chaos-injection wrapper. Unwrap is called repeatedly until the agent
// is no longer an UnwrappableAgent, peeling off all decorator layers.
type UnwrappableAgent interface {
	base.Agent
	Unwrap() base.Agent
}

// UnwrapAgent peels every transparent decorator layer off a, returning the
// innermost concrete agent. Agents that do not implement UnwrappableAgent are
// returned unchanged. This keeps optional-interface assertions (StatefulAgent,
// Heartbeater, Messenger) working on the raw instance regardless of how many
// decorators the runtime applied.
func UnwrapAgent(a base.Agent) base.Agent {
	for a != nil {
		u, ok := a.(UnwrappableAgent)
		if !ok {
			break
		}
		a = u.Unwrap()
	}
	return a
}

// chaosUnwrap returns the underlying agent when wrapped by chaosWrappedAgent,
// so optional-interface assertions (StatefulAgent, Heartbeater, Messenger)
// keep working on the raw instance. Returns the input unchanged otherwise.
func chaosUnwrap(a base.Agent) base.Agent {
	if w, ok := a.(*chaosWrappedAgent); ok {
		return w.Unwrap()
	}
	return a
}

// setChaosConfig writes a per-agent chaos entry under the manager write lock,
// initializing the map on first use.
func (m *Manager) setChaosConfig(agentID string, mutate func(*chaosEntry)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.chaosConfig == nil {
		m.chaosConfig = make(map[string]chaosEntry)
	}
	entry := m.chaosConfig[agentID]
	mutate(&entry)
	m.chaosConfig[agentID] = entry
}

// PartitionNetwork marks the agent as network-partitioned: its next
// Process/ProcessStream call fails with an injected network fault.
func (m *Manager) PartitionNetwork(_ context.Context, agentID string) error {
	log.Info("[arena] PartitionNetwork", "agent", agentID)
	m.setChaosConfig(agentID, func(e *chaosEntry) { e.networkPartitioned = true })
	return nil
}

// ToolTimeout sets a short execution deadline for an agent's tools. The
// deadline is read per-execution at the Process/ProcessStream boundary by
// chaosWrappedAgent, so it takes effect on the next execution without
// restarting the agent and is scoped to a single execution (never the agent's
// Start/lifecycle context).
func (m *Manager) ToolTimeout(_ context.Context, agentID string, timeout time.Duration) error {
	log.Info("[arena] ToolTimeout", "agent", agentID, "timeout", timeout.String())
	m.setChaosConfig(agentID, func(e *chaosEntry) { e.toolTimeout = timeout })
	return nil
}

// CorruptMemory marks the agent's memory as corrupted: its next
// Process/ProcessStream call fails with an injected memory fault.
func (m *Manager) CorruptMemory(_ context.Context, agentID string) error {
	log.Info("[arena] CorruptMemory", "agent", agentID)
	m.setChaosConfig(agentID, func(e *chaosEntry) { e.memoryCorrupt = true })
	return nil
}

// DisconnectMCP marks the agent's MCP connection as disconnected: its next
// Process/ProcessStream call fails with an injected MCP fault.
func (m *Manager) DisconnectMCP(_ context.Context, agentID string) error {
	log.Info("[arena] DisconnectMCP", "agent", agentID)
	m.setChaosConfig(agentID, func(e *chaosEntry) { e.mcpDisconnected = true })
	return nil
}

// InjectLLMFailure marks the agent's LLM as failing with the given error
// type: its next Process/ProcessStream call fails with an injected LLM fault.
func (m *Manager) InjectLLMFailure(_ context.Context, agentID string, errType string) error {
	log.Info("[arena] InjectLLMFailure", "agent", agentID, "error_type", errType)
	m.setChaosConfig(agentID, func(e *chaosEntry) { e.llmFailureType = errType })
	return nil
}
