package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Timwood0x10/ares/internal/agents/base"
	"github.com/Timwood0x10/ares/internal/ares_ctxutil"
	"github.com/Timwood0x10/ares/internal/ares_events"
	memory "github.com/Timwood0x10/ares/internal/runtime/memory"
)

// managedAgent holds an agent and its lifecycle metadata.
type managedAgent struct {
	agent    base.Agent
	factory  AgentFactory
	cancel   context.CancelFunc
	restarts int
	// stopped is set to true when the agent is intentionally stopped
	// (via StopAgent or RestartAgent). Prevents NotifyAgentDead from
	// triggering resurrection of an intentionally stopped agent.
	stopped bool
	// paused is set to true when PauseAgent is called. Distinguishes a
	// chaos-engineering pause from an intentional permanent stop.
	paused bool
	// resurrecting is set to true when NotifyAgentDead triggers RestoreAgent.
	// Prevents duplicate resurrection attempts for the same agent.
	resurrecting bool
	// operatorIntent records that the CURRENT entry was stopped/paused by an
	// explicit operator call AFTER a resurrection was already scheduled. The
	// async RestoreAgent re-checks this flag in its install critical section:
	// without it, an operator Stop/Pause landing inside the ~1s backoff window
	// was clobbered ~1s later by the pending resurrection installing a fresh
	// running instance (resurrection-after-kill).
	operatorIntent bool
}

// Manager implements the Runtime interface.
// It owns agent lifecycle: registration, start, stop, restart, and resurrection.
//
// Lifecycle methods are split across files for readability:
//   - manager_lifecycle.go — Start, Stop, Stats, replay, health check
//   - manager_chaos.go     — Arena fault injection, ListAgents, GetAgentInfo
type Manager struct {
	mu            sync.RWMutex
	agents        map[string]*managedAgent
	factories     map[string]AgentFactory
	eventStore    ares_events.EventStore
	memManager    memory.MemoryManager
	snapshotStore base.SnapshotStore
	g             *errgroup.Group
	gctx          context.Context
	gPtr          atomic.Pointer[errgroup.Group]
	gctxPtr       atomic.Pointer[context.Context]
	cancel        context.CancelFunc
	config        *Config
	totalRestarts int
	startTime     time.Time
	isStarted     bool
	isStopped     bool
	// chaosConfig stores per-agent fault injection settings for the arena.
	chaosConfig map[string]chaosEntry
	// dagStore maps agent IDs to their workflow DAGs.
	// Used by the evolution system to apply workflow patches to the live DAG.
	// The DAG type is any (engine.MutableDAG) to avoid importing workflow/engine.
	dagStore map[string]any
}

// chaosEntry holds fault injection settings for a single agent.
// All fields are write-once via the arena injectors; they are read at the
// agent Process/ProcessStream boundary (see chaosWrappedAgent) so injections
// take effect on the next execution without restarting the agent.
type chaosEntry struct {
	slowDelay          time.Duration // zero = no slow
	toolTimeout        time.Duration // zero = no timeout
	networkPartitioned bool          // true = Process fails with a network partition fault
	memoryCorrupt      bool          // true = Process fails with a memory corruption fault
	mcpDisconnected    bool          // true = Process fails with an MCP disconnect fault
	llmFailureType     string        // non-empty = Process fails with an LLM failure fault
}

// New creates a new Manager.
//
// Args:
//
//	config - runtime configuration. Uses defaults if nil.
//	eventStore - event store for operational recovery (may be nil).
//	memManager - memory manager for cognitive recovery (may be nil).
//
// Returns:
//
//	manager - the runtime manager.
func New(config *Config, eventStore ares_events.EventStore, memManager memory.MemoryManager) *Manager {
	if config == nil {
		config = DefaultConfig()
	}
	// Initialize errgroup with a labeled detached context so that m.g.Go() never
	// panics even if called before Start(). Start() will re-initialize with
	// the caller's context.
	g, gctx := errgroup.WithContext(ares_ctxutil.WithDetachedLabel("runtime:pre-start"))
	m := &Manager{
		agents:      make(map[string]*managedAgent),
		factories:   make(map[string]AgentFactory),
		eventStore:  eventStore,
		memManager:  memManager,
		config:      config,
		chaosConfig: make(map[string]chaosEntry),
		dagStore:    make(map[string]any),
		g:           g,
		gctx:        gctx,
	}
	m.gPtr.Store(g)
	m.gctxPtr.Store(&gctx)
	return m
}

// getG returns the current errgroup via atomic pointer load.
// Safe for concurrent access without holding m.mu.
func (m *Manager) getG() *errgroup.Group {
	return m.gPtr.Load()
}

// getGctx returns the current errgroup context via atomic pointer load.
// Safe for concurrent access without holding m.mu.
func (m *Manager) getGctx() context.Context {
	if p := m.gctxPtr.Load(); p != nil {
		return *p
	}
	return context.Background()
}

// WithSnapshotStore sets the snapshot store used for agent state recovery.
// Must be called before Start(). Snapshots provide a richer state recovery
// path than event replay alone and should be used when a resurrection plugin
// periodically captures snapshots. When set, recoverAgentState will attempt
// to load a snapshot first, then supplement with event replay for any state
// the snapshot may lack.
func (m *Manager) WithSnapshotStore(store base.SnapshotStore) *Manager {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshotStore = store
	return m
}

// RegisterAgent registers an agent and its factory for lifecycle management.
func (m *Manager) RegisterAgent(agent base.Agent, factory AgentFactory) {
	if agent == nil {
		log.Error("runtime: RegisterAgent called with nil agent")
		return
	}
	if factory == nil {
		log.Error("runtime: RegisterAgent called with nil factory", "agent_id", agent.ID())
		return
	}
	id := agent.ID()
	if id == "" {
		log.Error("runtime: RegisterAgent called with empty agent ID")
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.factories[id] = factory

	// Store agent entry if not already present.
	if _, exists := m.agents[id]; !exists {
		m.agents[id] = &managedAgent{
			agent:   &chaosWrappedAgent{Agent: agent, m: m, id: id},
			factory: factory,
		}
	}

	log.Info("runtime: agent registered", "agent_id", id, "type", agent.Type())
}

// AgentDAGEvolutionKey is the key under which Bootstrap registers the
// synthetic placeholder DAG that the evolution executors are bound to until a
// real agent DAG is injected (the synthetic graph is confined
// to this key and never masquerades as a live target).
const AgentDAGEvolutionKey = "evolution"

// AgentDAGLiveKey is the key under which the serve entry registers the real
// live agent DAG (built from the configured agent population). It is the
// single key the evolution system reads to apply workflow/recovery patches to
// the production topology (the serve side previously registered the live
// DAG under a third, orphaned key, so it never replaced the placeholder).
const AgentDAGLiveKey = "leader-live"

// RegisterAgentDAG associates a workflow DAG with an agent.
// The evolution system uses this to apply workflow patches to the live DAG.
// dag is typically an *engine.MutableDAG, stored as any to avoid importing
// workflow/engine at this layer.
func (m *Manager) RegisterAgentDAG(agentID string, dag any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dagStore == nil {
		m.dagStore = make(map[string]any)
	}
	m.dagStore[agentID] = dag
	log.Info("runtime: DAG registered for agent", "agent_id", agentID)
}

// GetAgentDAG returns the workflow DAG associated with an agent, if any.
func (m *Manager) GetAgentDAG(agentID string) (any, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	dag, ok := m.dagStore[agentID]
	return dag, ok
}

// StartAgent launches an agent in a managed goroutine with panic recovery.
func (m *Manager) StartAgent(ctx context.Context, agent base.Agent) error {
	if agent == nil {
		return ErrNilAgent
	}

	id := agent.ID()
	if id == "" {
		return errors.New("runtime: agent ID must not be empty")
	}

	m.mu.Lock()
	if m.isStopped {
		m.mu.Unlock()
		return ErrRuntimeStopped
	}

	// If agent already exists and is running (has cancel), reject.
	if existing, exists := m.agents[id]; exists && existing.cancel != nil {
		m.mu.Unlock()
		return ErrAgentAlreadyRegistered
	}

	agentCtx, agentCancel := context.WithCancel(m.getGctx())

	// Chaos engineering injections (SlowAgent, ToolTimeout, fault errors) are
	// NOT applied to the agent lifecycle context here. They are read from
	// chaosConfig at the Process/ProcessStream boundary by chaosWrappedAgent,
	// so they take effect on the next execution without restarting the agent.
	// Applying them to this lifecycle context would (a) require a restart to
	// change, contradicting the chaosEntry contract, and (b) let a short
	// ToolTimeout cancel the long-running Start goroutine and kill a healthy
	// agent. See chaosWrappedAgent.Process/ProcessStream.

	ma := &managedAgent{
		agent:  &chaosWrappedAgent{Agent: agent, m: m, id: id},
		cancel: agentCancel,
	}
	// Preserve factory if already registered via RegisterAgent.
	if f, ok := m.factories[id]; ok {
		ma.factory = f
	}
	m.agents[id] = ma

	// If runtime hasn't started yet, skip launching — Start() will re-launch
	// all agents with the real errgroup context (m.gctx). Launching now would
	// attach the goroutine to the pre-start errgroup which gets discarded,
	// creating an orphan agent whose context is never cancelled.
	if !m.isStarted {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	m.launchAgentGoroutine(agentCtx, id, agent)

	m.emitEvent(ctx, id, ares_events.EventAgentStarted, map[string]any{
		FieldAgentID: id,
		FieldType:    string(agent.Type()),
	})

	return nil
}

// StopAgent gracefully stops an agent by ID.
func (m *Manager) StopAgent(ctx context.Context, agentID string) error {
	// Mark as intentionally stopped before cancelling context.
	// This prevents NotifyAgentDead from triggering resurrection.
	m.mu.Lock()
	ma, exists := m.agents[agentID]
	if !exists {
		m.mu.Unlock()
		return ErrAgentNotFound
	}
	ma.stopped = true
	ma.operatorIntent = true
	cancel := ma.cancel
	agent := ma.agent
	m.mu.Unlock()

	// Cancel the agent's managed goroutine context.
	if cancel != nil {
		cancel()
	}

	// Gracefully stop the agent.
	if agent != nil {
		stopCtx, stopCancel := context.WithTimeout(ctx, m.config.AgentStopTimeout)
		defer stopCancel()
		if err := agent.Stop(stopCtx); err != nil {
			log.Warn("runtime: agent stop returned error",
				"agent_id", agentID, "error", err,
			)
		}
	}

	m.emitEvent(ctx, agentID, ares_events.EventAgentStopped, map[string]any{
		FieldAgentID: agentID,
		FieldReason:  "explicit_stop",
	})

	log.Info("runtime: agent stopped", "agent_id", agentID)
	return nil
}

// emitEvent appends a lifecycle event to the EventStore using the canonical
// ares_events.Emit helper. No-op if eventStore is nil.
func (m *Manager) emitEvent(ctx context.Context, streamID string, eventType ares_events.EventType, payload map[string]any) {
	if !ares_events.Emit(ctx, m.eventStore, streamID, eventType, "runtime", payload) {
		log.Warn("failed to emit event", "event_type", eventType, "stream_id", streamID)
	}
}

// GetAgent returns the current instance of a managed agent, or nil if not found.
func (m *Manager) GetAgent(agentID string) base.Agent {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if ma, ok := m.agents[agentID]; ok {
		return ma.agent
	}
	return nil
}

// RestartAgent stops and restarts an agent with fresh state.
func (m *Manager) RestartAgent(ctx context.Context, agentID string) error {
	// Use write lock for entire check-and-mutate to prevent NotifyAgentDead race.
	m.mu.Lock()
	if m.isStopped {
		m.mu.Unlock()
		return ErrRuntimeStopped
	}
	ma, exists := m.agents[agentID]
	if !exists {
		m.mu.Unlock()
		return ErrAgentNotFound
	}
	factory := m.factories[agentID]
	if factory == nil {
		m.mu.Unlock()
		return fmt.Errorf("runtime: no factory registered for agent %s", agentID)
	}

	// Mark as intentionally stopped to prevent NotifyAgentDead race.
	ma.stopped = true
	prevRestarts := ma.restarts
	// Capture the cancel func and agent handle UNDER the lock: ma.cancel /
	// ma.agent are written by ResumeAgent/PauseAgent under m.mu, so reading
	// them after Unlock would race a concurrent lifecycle transition.
	prevCancel := ma.cancel
	prevAgent := ma.agent
	m.mu.Unlock()

	m.emitEvent(ctx, agentID, ares_events.EventAgentStopped, map[string]any{
		FieldAgentID: agentID,
		FieldReason:  "restart",
	})

	// Stop the old agent.
	if prevCancel != nil {
		prevCancel()
	}
	if prevAgent != nil {
		stopCtx, stopCancel := context.WithTimeout(ctx, m.config.AgentStopTimeout)
		if err := prevAgent.Stop(stopCtx); err != nil {
			log.Warn("runtime: restart stop failed", "agent_id", agentID, "error", err)
		}
		stopCancel()
	}

	// Create a fresh instance from factory.
	newAgent := factory()
	if newAgent == nil {
		return fmt.Errorf("runtime: factory returned nil for agent %s", agentID)
	}

	// Re-register and start.
	m.mu.Lock()
	agentCtx, agentCancel := context.WithCancel(m.getGctx())
	m.agents[agentID] = &managedAgent{
		agent:    &chaosWrappedAgent{Agent: newAgent, m: m, id: agentID},
		factory:  factory,
		cancel:   agentCancel,
		restarts: prevRestarts + 1,
	}
	m.totalRestarts++
	m.mu.Unlock()

	m.launchAgentGoroutine(agentCtx, agentID, newAgent)

	m.emitEvent(ctx, agentID, ares_events.EventAgentStarted, map[string]any{
		FieldAgentID: agentID,
		FieldType:    "restart",
	})

	log.Info("runtime: agent restarted", "agent_id", agentID)
	return nil
}

// RestoreAgent creates a new agent from factory, replays ares_events, restores memory, and starts it.
func (m *Manager) RestoreAgent(ctx context.Context, agentID string, factory AgentFactory) error {
	if factory == nil {
		return ErrNilFactory
	}

	m.emitEvent(ctx, agentID, ares_events.EventFailoverTriggered, map[string]any{
		FieldAgentID: agentID,
	})

	oldMA, oldExists := m.stopOldRestoredAgent(ctx, agentID)

	newAgent, err := m.recoverAgentState(ctx, agentID, factory)
	if err != nil {
		return err
	}
	if newAgent == nil {
		return fmt.Errorf("recover returned nil agent for %s", agentID)
	}

	prevRestarts := 0
	superseded := false
	m.mu.Lock()
	if oldExists && oldMA != nil {
		prevRestarts = oldMA.restarts
		// Operator-intent recheck INSIDE the install critical section: the
		// resurrection was scheduled ~1s ago; if the operator explicitly
		// stopped/paused this entry in the meantime, installing a fresh
		// running instance would silently undo their decision.
		if oldMA.operatorIntent {
			superseded = true
		}
	}
	if superseded {
		m.mu.Unlock()
		log.Info("runtime: restore aborted — operator stopped/paused the agent after resurrection was scheduled",
			"agent_id", agentID,
		)
		return nil // not an error: the desired state is "stopped"
	}
	agentCtx, agentCancel := context.WithCancel(m.getGctx())
	m.agents[agentID] = &managedAgent{
		agent:    &chaosWrappedAgent{Agent: newAgent, m: m, id: agentID},
		factory:  factory,
		cancel:   agentCancel,
		restarts: prevRestarts,
	}
	m.mu.Unlock()

	m.launchAgentGoroutine(agentCtx, agentID, newAgent)

	m.emitEvent(ctx, agentID, ares_events.EventFailoverCompleted, map[string]any{
		FieldAgentID: agentID,
		FieldType:    newAgent.Type(),
	})

	log.Info("runtime: agent restored",
		"agent_id", agentID, "type", newAgent.Type(),
		"restarts", prevRestarts,
	)
	return nil
}

// stopOldRestoredAgent marks the old agent as stopped and gracefully shuts it down.
func (m *Manager) stopOldRestoredAgent(ctx context.Context, agentID string) (*managedAgent, bool) {
	m.mu.Lock()
	oldMA, oldExists := m.agents[agentID]
	if oldExists && oldMA != nil {
		oldMA.stopped = true
	}
	m.mu.Unlock()

	if oldExists && oldMA != nil {
		if oldMA.cancel != nil {
			oldMA.cancel()
		}
		stopCtx, stopCancel := context.WithTimeout(ctx, m.config.AgentStopTimeout)
		defer stopCancel()
		if err := oldMA.agent.Stop(stopCtx); err != nil {
			log.Warn("runtime: restore stop old agent failed",
				"agent_id", agentID, "error", err,
			)
		}
	}
	return oldMA, oldExists
}

// recoverAgentState creates a new agent from factory, replays ares_events for operational
// recovery, and enriches state with memory context for cognitive recovery.
func (m *Manager) recoverAgentState(ctx context.Context, agentID string, factory AgentFactory) (base.Agent, error) {
	newAgent := factory()
	if newAgent == nil {
		return nil, fmt.Errorf("runtime: factory returned nil for agent %s", agentID)
	}

	evts := m.replayEvents(ctx, agentID)
	if sa, ok := newAgent.(base.StatefulAgent); ok {
		m.mu.RLock()
		store := m.snapshotStore
		m.mu.RUnlock()

		// RecoverSnapshotOrEvents inlined — try snapshot store first,
		// then fall back to event replay.
		state := func() map[string]any {
			if store != nil {
				if snap, err := store.Load(ctx, agentID); err == nil && len(snap) > 0 {
					return snap
				}
			}
			state := buildStateFromEvents(evts)
			if m.memManager != nil {
				cognitiveState := m.buildCognitiveState(ctx, agentID, state)
				for k, v := range cognitiveState {
					state[k] = v
				}
			}
			return state
		}()

		if len(state) > 0 {
			if err := sa.RestoreState(state); err != nil {
				log.Warn("runtime: RestoreState failed",
					"agent_id", agentID, "error", err,
				)
			}
		}
		if len(evts) > 0 {
			if err := sa.ReplayEvents(evts); err != nil {
				log.Warn("runtime: ReplayEvents failed",
					"agent_id", agentID, "error", err,
				)
			}
		}
	}
	return newAgent, nil
}

// launchAgentGoroutine starts the agent in a managed goroutine with panic recovery.
func (m *Manager) launchAgentGoroutine(ctx context.Context, agentID string, agent base.Agent) {
	m.getG().Go(func() error {
		defer func() {
			if r := recover(); r != nil {
				log.Error("runtime: agent panicked",
					"agent_id", agentID, "panic", r,
				)
				m.NotifyAgentDead(agentID, fmt.Sprintf("panic: %v", r))
			}
		}()

		if err := agent.Start(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Error("runtime: agent start failed",
				"agent_id", agentID, "error", err,
			)
			m.NotifyAgentDead(agentID, fmt.Sprintf("start failed: %v", err))
			return nil
		}
		return nil
	})
}

// NotifyAgentDead is called when an agent dies. It triggers asynchronous restoration
// via errgroup if a factory is registered for the agent.
func (m *Manager) NotifyAgentDead(agentID string, reason string) {
	shouldRestore := func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()

		factory, hasFactory := m.factories[agentID]
		ma, hasAgent := m.agents[agentID]

		if m.isStopped || (hasAgent && (ma.stopped || ma.paused || ma.resurrecting)) {
			return false
		}
		if !hasFactory {
			log.Warn("runtime: agent dead but no factory registered, skipping restore",
				"agent_id", agentID, "reason", reason,
			)
			return false
		}
		if hasAgent && m.config.MaxRestartsPerAgent > 0 && ma.restarts >= m.config.MaxRestartsPerAgent {
			log.Error("runtime: max restarts exceeded, not restoring",
				"agent_id", agentID, "restarts", ma.restarts,
				"max", m.config.MaxRestartsPerAgent, "reason", reason,
			)
			return false
		}
		if hasAgent {
			ma.restarts++
			ma.resurrecting = true
		}
		m.totalRestarts++
		// Schedule the resurrection goroutine WHILE STILL HOLDING m.mu so the
		// errgroup Add is atomic with the isStopped check above. Stop() sets
		// isStopped under this same mutex before it calls m.g.Wait(), so once
		// Stop has transitioned no NotifyAgentDead can reach this m.g.Go —
		// eliminating the "WaitGroup reused before Wait returned" panic that a
		// post-unlock schedule would allow.
		m.scheduleResurrectionLocked(agentID, factory)
		return true
	}()
	if !shouldRestore {
		return
	}

	log.Warn("runtime: agent dead, scheduling restore",
		"agent_id", agentID, "reason", reason,
	)

	// The detached label registers a background job for observability; it
	// must be released when the emit completes, or the active-count
	// only ever grows.
	emitCtx := ares_ctxutil.WithDetachedLabel("runtime:notify-agent-dead")
	defer ares_ctxutil.DoneBackground("runtime:notify-agent-dead")
	m.emitEvent(emitCtx, agentID, ares_events.EventAgentStopped, map[string]any{
		FieldAgentID:   agentID,
		FieldReason:    reason,
		"auto_restore": true,
	})
}

// scheduleResurrectionLocked launches the resurrection goroutine. The caller
// MUST hold m.mu: the errgroup Add (m.g.Go) must be serialized with Stop()'s
// isStopped transition to avoid a WaitGroup reuse panic.
func (m *Manager) scheduleResurrectionLocked(agentID string, factory AgentFactory) {
	m.scheduleResurrection(agentID, factory)
}

func (m *Manager) scheduleResurrection(agentID string, factory AgentFactory) {
	gctx := m.getGctx()
	m.getG().Go(func() error {
		// Exponential backoff: 1s, 2s, 4s, capped at 30s.
		backoff := time.Second
		const maxBackoff = 30 * time.Second
		const maxAttempts = 5
		timer := time.NewTimer(backoff)
		defer timer.Stop()
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			restoreCtx, restoreCancel := context.WithTimeout(gctx, m.config.RestoreTimeout)
			err := m.RestoreAgent(restoreCtx, agentID, factory)
			restoreCancel()
			if err == nil {
				m.mu.Lock()
				if entry, exists := m.agents[agentID]; exists {
					entry.resurrecting = false
				}
				m.mu.Unlock()
				return nil
			}
			log.Error("runtime: restore failed",
				"agent_id", agentID, "attempt", attempt, "error", err,
			)
			if attempt < maxAttempts {
				timer.Reset(backoff)
				select {
				case <-gctx.Done():
					m.mu.Lock()
					if entry, exists := m.agents[agentID]; exists {
						entry.resurrecting = false
					}
					m.mu.Unlock()
					return nil
				case <-timer.C:
				}
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
		}
		m.mu.Lock()
		if entry, exists := m.agents[agentID]; exists {
			entry.resurrecting = false
		}
		m.mu.Unlock()
		log.Error("runtime: restore exhausted all retries",
			"agent_id", agentID, "max_attempts", maxAttempts,
		)
		return nil
	})
}
