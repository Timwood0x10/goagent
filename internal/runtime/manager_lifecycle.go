package runtime

//nolint: errcheck // best-effort operations: ResponseWriter writes, cleanup Close/Wait, deferred shutdown
import (
	"context"
	"errors"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Timwood0x10/ares/internal/agents/base"
	"github.com/Timwood0x10/ares/internal/ares_ctxutil"
	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/core/models"
)

// Start begins the runtime's monitoring loop and launches all registered agents.
func (m *Manager) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("runtime: context must not be nil")
	}

	m.mu.Lock()
	if m.isStarted {
		m.mu.Unlock()
		return errors.New("runtime: already started")
	}
	if m.isStopped {
		m.mu.Unlock()
		return ErrRuntimeStopped
	}
	m.isStarted = true
	m.startTime = time.Now()

	childCtx, childCancel := context.WithCancel(ctx)
	m.cancel = childCancel
	m.g, m.gctx = errgroup.WithContext(childCtx)
	m.gPtr.Store(m.g)
	m.gctxPtr.Store(&m.gctx)
	m.mu.Unlock()

	// Wire EventStore into MemoryManager if both are available.
	if m.memManager != nil && m.eventStore != nil {
		m.memManager.SetEventStore(m.eventStore, "memory-manager")
	}

	// Collect agent launch info under write lock because we mutate ma.cancel
	// for each agent. Launching goroutines is done outside the lock to avoid
	// blocking concurrent agent registration during goroutine creation.
	type agentLaunch struct {
		id    string
		agent base.Agent
		ctx   context.Context
	}
	m.mu.Lock()
	launches := make([]agentLaunch, 0, len(m.agents))
	for id, ma := range m.agents {
		if ma.agent != nil {
			agentCtx, agentCancel := context.WithCancel(m.getGctx())
			ma.cancel = agentCancel
			launches = append(launches, agentLaunch{id: id, agent: ma.agent, ctx: agentCtx})
		}
	}
	m.mu.Unlock()

	for _, l := range launches {
		m.launchAgentGoroutine(l.ctx, l.id, l.agent)
	}

	// Emit EventAgentStarted for agents that were registered before Start().
	// Without this, the event store has no record of these agents starting and
	// any downstream event consumer (evolution, flight recorder) sees a gap.
	for _, l := range launches {
		m.emitEvent(m.getGctx(), l.id, ares_events.EventAgentStarted, map[string]any{
			FieldAgentID: l.id,
			FieldType:    string(l.agent.Type()),
		})
	}

	// Background health check loop.
	m.getG().Go(func() error {
		ticker := time.NewTicker(m.config.HealthCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-m.getGctx().Done():
				return nil
			case <-ticker.C:
				m.healthCheck()
			}
		}
	})

	log.Info("runtime: started", "agents", len(launches))
	return nil
}

// Stop gracefully shuts down all agents and the runtime.
func (m *Manager) Stop() error {
	m.mu.Lock()
	if !m.isStarted || m.isStopped {
		m.mu.Unlock()
		return nil
	}
	m.isStopped = true
	m.cancel()
	m.mu.Unlock()

	// Stop all agents concurrently with an overall timeout.
	// Use a detached context because m.gctx is already cancelled by m.cancel() above.
	stopCtx, stopCancel := ares_ctxutil.WithDetachedTimeout("runtime:stop", m.config.OverallStopTimeout)
	defer stopCancel()
	defer ares_ctxutil.DoneBackground("runtime:stop") // balance the label registration

	// Capture final snapshots for stateful agents, then mark all as stopped.
	type agentStopInfo struct {
		id     string
		agent  base.Agent
		cancel context.CancelFunc
		// snapAgent is non-nil if a final snapshot should be captured (I/O done outside lock).
		snapAgent base.StatefulAgent
	}
	var toStop []agentStopInfo
	m.mu.Lock()
	store := m.snapshotStore
	for id, ma := range m.agents {
		if ma.stopped {
			continue
		}
		info := agentStopInfo{id: id, agent: ma.agent, cancel: ma.cancel}
		// Collect StatefulAgent reference so snapshot+save I/O can run outside the lock.
		if store != nil {
			if sa, ok := chaosUnwrap(ma.agent).(base.StatefulAgent); ok {
				info.snapAgent = sa
			}
		}
		ma.stopped = true
		toStop = append(toStop, info)
	}
	m.mu.Unlock()

	// Snapshot and Save I/O moved outside the write lock to prevent
	// blocking all other lifecycle operations when state is large.
	if store != nil {
		for i := range toStop {
			info := &toStop[i]
			if info.snapAgent == nil {
				continue
			}
			snap, err := info.snapAgent.Snapshot()
			if err != nil {
				log.Warn("runtime: final snapshot failed",
					"agent_id", info.id, "error", err,
				)
				continue
			}
			if snap == nil {
				continue
			}
			if err := store.Save(stopCtx, info.id, snap); err != nil {
				log.Warn("runtime: final snapshot save failed",
					"agent_id", info.id, "error", err,
				)
			}
		}
	}

	g, _ := errgroup.WithContext(stopCtx)
	for _, info := range toStop {
		info := info
		g.Go(func() error {
			if info.cancel != nil {
				info.cancel()
			}
			if info.agent != nil {
				agentStopCtx, agentStopCancel := context.WithTimeout(stopCtx, m.config.AgentStopTimeout)
				defer agentStopCancel()
				if err := info.agent.Stop(agentStopCtx); err != nil {
					log.Warn("runtime: failed to stop agent",
						"agent_id", info.id, "error", err,
					)
				}
			}
			return nil
		})
	}

	_ = g.Wait()
	ares_ctxutil.DoneBackground("runtime:pre-start") // group drained; release the label

	// Wait for all errgroup goroutines.
	if g := m.getG(); g != nil {
		_ = g.Wait()
	}

	log.Info("runtime: stopped", "total_restarts", m.totalRestarts)
	return nil
}

// Stats returns runtime statistics.
func (m *Manager) Stats() RuntimeStats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Count agents that are managed (registered and not intentionally stopped).
	active := 0
	for _, ma := range m.agents {
		if ma.agent != nil && !ma.stopped {
			active++
		}
	}

	uptime := time.Duration(0)
	if !m.startTime.IsZero() {
		uptime = time.Since(m.startTime)
	}

	return RuntimeStats{
		ActiveAgents:    active,
		TotalRestarts:   m.totalRestarts,
		Uptime:          uptime,
		BackgroundTasks: ares_ctxutil.BackgroundStats(),
	}
}

// replayEvents reads ares_events for the given agent stream from EventStore.
// Limits the number of ares_events to prevent unbounded memory usage.
// Returns nil if eventStore is nil or an error occurs.
// Verifies event stream integrity; logs warnings on gaps or corruption.
func (m *Manager) replayEvents(ctx context.Context, agentID string) []*ares_events.Event {
	if m.eventStore == nil {
		return nil
	}
	// Use agentID directly as stream ID — matches how agents emit ares_events
	// via emitEvent(ctx, eventType, payload) which uses a.id as stream ID.
	streamID := agentID
	limit := m.config.MaxReplayEvents
	if limit <= 0 {
		limit = 10000
	}
	evts, err := m.eventStore.Read(ctx, streamID, ares_events.ReadOptions{
		Direction: ares_events.ReadAscending,
		Limit:     limit,
	})
	if err != nil {
		log.Warn("runtime: failed to read ares_events for replay",
			"agent_id", agentID, "error", err,
		)
		return nil
	}

	if len(evts) > 1 {
		if err := ares_events.VerifyStreamIntegrity(evts); err != nil {
			log.Error("runtime: event stream integrity check failed",
				"agent_id", agentID,
				"event_count", len(evts),
				"error", err,
				"hash", ares_events.StreamHash(evts),
			)
		}

		// Semantic completeness: detect truncation by comparing last replayed
		// version against the store's stream version.
		if streamVersion, svErr := m.eventStore.StreamVersion(ctx, streamID); svErr == nil {
			lastVersion := evts[len(evts)-1].Version
			if lastVersion != streamVersion {
				log.Error("runtime: event stream truncated",
					"agent_id", agentID,
					"last_replayed", lastVersion,
					"stream_version", streamVersion,
					"missing_events", streamVersion-lastVersion,
					"hash", ares_events.StreamHash(evts),
				)
			}
		} else if svErr != ares_events.ErrStreamNotFound {
			log.Warn("runtime: failed to check stream version",
				"agent_id", agentID, "error", svErr,
			)
		}
	}

	return evts
}

// buildStateFromEvents constructs a state map from ares_events for RestoreState.
// Currently extracts session_id from EventSessionCreated ares_events.
func buildStateFromEvents(evts []*ares_events.Event) map[string]any {
	state := make(map[string]any)
	for _, ev := range evts {
		if ev == nil {
			continue
		}
		if ev.Type == ares_events.EventSessionCreated {
			if sid, ok := ev.Payload["session_id"].(string); ok && sid != "" {
				state["session_id"] = sid
			}
		}
	}
	return state
}

// buildCognitiveState loads conversation history from Memory manager for cognitive recovery.
// It enriches the operational state with session messages so the restored agent
// has context about prior conversations.
//
// Design note: The conversation_history is loaded here for completeness, but
// agent implementations (RestoreState) typically only consume session_id.
// The agent's initMemoryContext() loads conversation history on-demand via
// the MemoryManager using the restored session_id. This avoids duplicating
// the full conversation in the state map.
//
// Args:
//
//	ctx - context for memory operations.
//	agentID - the agent being restored.
//	operationalState - state built from ares_events; used to find session_id if present.
//
// Returns:
//
// state - map with cognitive recovery data (session_id, conversation_history).
func (m *Manager) buildCognitiveState(ctx context.Context, agentID string, operationalState map[string]any) map[string]any {
	state := make(map[string]any)

	// Try to find session_id from operational state first, then from memory manager.
	sessionID, _ := operationalState["session_id"].(string)
	if sessionID == "" {
		// No session from ares_events; try the memory manager checkpoint.
		// Use a bounded timeout to prevent hanging on slow DB.
		sessionCtx, sessionCancel := context.WithTimeout(ctx, 5*time.Second)
		sid, err := m.memManager.GetLatestSessionForAgent(sessionCtx, agentID)
		sessionCancel()
		if err != nil {
			log.Warn("runtime: cognitive recovery: failed to get latest session",
				"agent_id", agentID, "error", err,
			)
			return state
		}
		sessionID = sid
	}

	if sessionID == "" {
		return state
	}

	// Load conversation history for the session with bounded timeout.
	msgCtx, msgCancel := context.WithTimeout(ctx, 5*time.Second)
	defer msgCancel()
	messages, err := m.memManager.GetMessages(msgCtx, sessionID)
	if err != nil {
		log.Warn("runtime: cognitive recovery: failed to get messages",
			"agent_id", agentID, "session_id", sessionID, "error", err,
		)
		return state
	}

	if len(messages) > 0 {
		state["session_id"] = sessionID
		state["conversation_history"] = messages
		log.Debug("runtime: cognitive recovery loaded",
			"agent_id", agentID,
			"session_id", sessionID,
			"messages", len(messages),
		)
	}

	return state
}

// healthCheck checks all agents for liveness. If an agent is offline
// unexpectedly, NotifyAgentDead is triggered.
func (m *Manager) healthCheck() {
	type agentCheck struct {
		id      string
		agent   base.Agent
		factory AgentFactory
	}

	m.mu.RLock()
	checks := make([]agentCheck, 0, len(m.agents))
	for id, ma := range m.agents {
		if ma.stopped || ma.paused {
			continue
		}
		checks = append(checks, agentCheck{
			id:      id,
			agent:   ma.agent,
			factory: ma.factory,
		})
	}
	m.mu.RUnlock()

	for _, c := range checks {
		if c.agent == nil {
			continue
		}
		// Prefer Heartbeater interface for liveness check if available.
		if h, ok := chaosUnwrap(c.agent).(base.Heartbeater); ok {
			if !h.IsAlive() {
				if c.factory != nil {
					log.Warn("runtime: health check: agent heartbeat failed",
						"agent_id", c.id,
					)
					m.NotifyAgentDead(c.id, "health check: heartbeat failed")
				}
			}
			continue
		}
		// Fall back to status-based check.
		status := c.agent.Status()
		if status == models.AgentStatusOffline || status == models.AgentStatusStopping {
			if c.factory != nil {
				log.Warn("runtime: health check: agent status abnormal",
					"agent_id", c.id, "status", status,
				)
				m.NotifyAgentDead(c.id, "health check: status="+string(status))
			}
		}
	}
}
