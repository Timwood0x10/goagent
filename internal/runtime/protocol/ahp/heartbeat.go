package ahp

import (
	"context"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/errors"
)

// HeartbeatConfig holds the configuration for heartbeat mechanism.
type HeartbeatConfig struct {
	Interval  time.Duration
	Timeout   time.Duration
	MaxMissed int
}

// DefaultHeartbeatConfig returns the default heartbeat configuration.
func DefaultHeartbeatConfig() *HeartbeatConfig {
	return &HeartbeatConfig{
		Interval:  5 * time.Second,
		Timeout:   30 * time.Second,
		MaxMissed: 3,
	}
}

// TimeoutCallback is invoked when an agent is marked offline by CheckTimeouts.
type TimeoutCallback func(agentID string)

// HeartbeatMonitor monitors heartbeat signals from agents.
type HeartbeatMonitor struct {
	mu          sync.RWMutex
	agentStatus map[string]*AgentHeartbeat
	config      *HeartbeatConfig
	callbacks   []TimeoutCallback
}

// AgentHeartbeat holds the heartbeat state for an agent.
type AgentHeartbeat struct {
	AgentID     string
	LastSeen    time.Time
	Status      models.AgentStatus
	MissedCount int
}

// NewHeartbeatMonitor creates a new HeartbeatMonitor.
func NewHeartbeatMonitor(config *HeartbeatConfig) *HeartbeatMonitor {
	if config == nil {
		config = DefaultHeartbeatConfig()
	}
	return &HeartbeatMonitor{
		agentStatus: make(map[string]*AgentHeartbeat),
		config:      config,
	}
}

// RecordHeartbeat records a heartbeat from an agent.
func (m *HeartbeatMonitor) RecordHeartbeat(agentID string) {
	if agentID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if hb, ok := m.agentStatus[agentID]; ok {
		hb.LastSeen = time.Now()
		hb.MissedCount = 0
		hb.Status = models.AgentStatusReady
	} else {
		m.agentStatus[agentID] = &AgentHeartbeat{
			AgentID:  agentID,
			LastSeen: time.Now(),
			Status:   models.AgentStatusReady,
		}
	}
}

// GetStatus returns the status of an agent.
func (m *HeartbeatMonitor) GetStatus(agentID string) (models.AgentStatus, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if hb, ok := m.agentStatus[agentID]; ok {
		return hb.Status, true
	}
	return "", false
}

// CheckTimeouts checks for agents that have missed heartbeats.
// Returns a list of agent IDs that were just marked offline.
// Invokes registered callbacks for each timed-out agent (outside the lock).
func (m *HeartbeatMonitor) CheckTimeouts() []string {
	timedOut := m.checkAndMarkOffline()

	// Notify callbacks outside the lock to prevent deadlocks.
	for _, agentID := range timedOut {
		m.notifyCallbacks(agentID)
	}

	return timedOut
}

// checkAndMarkOffline marks timed-out agents as offline under the write lock.
func (m *HeartbeatMonitor) checkAndMarkOffline() []string {
	m.mu.Lock()
	defer m.mu.Unlock()

	var timedOut []string
	now := time.Now()

	for agentID, hb := range m.agentStatus {
		// Skip agents already marked offline to avoid repeated timeout reporting.
		if hb.Status == models.AgentStatusOffline {
			continue
		}
		if now.Sub(hb.LastSeen) > m.config.Timeout {
			hb.MissedCount++
			if hb.MissedCount >= m.config.MaxMissed {
				hb.Status = models.AgentStatusOffline
				timedOut = append(timedOut, agentID)
			}
		}
	}

	return timedOut
}

// RemoveAgent removes an agent from monitoring.
func (m *HeartbeatMonitor) RemoveAgent(agentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.agentStatus, agentID)
}

// ListAgents returns all monitored agent IDs.
func (m *HeartbeatMonitor) ListAgents() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	agents := make([]string, 0, len(m.agentStatus))
	for agentID := range m.agentStatus {
		agents = append(agents, agentID)
	}
	return agents
}

// RegisterCallback adds a callback that is invoked when an agent times out.
//
// Args:
//
//	fn - callback function, must be non-blocking.
func (m *HeartbeatMonitor) RegisterCallback(fn TimeoutCallback) {
	if fn == nil {
		log.Warn("RegisterCallback: nil callback")
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callbacks = append(m.callbacks, fn)
}

// notifyCallbacks invokes all registered callbacks for a timed-out agent.
// Caller must NOT hold m.mu (callbacks may re-register or inspect state).
func (m *HeartbeatMonitor) notifyCallbacks(agentID string) {
	// Copy callbacks under read lock to avoid holding lock during callback execution.
	m.mu.RLock()
	callbacks := make([]TimeoutCallback, len(m.callbacks))
	copy(callbacks, m.callbacks)
	m.mu.RUnlock()

	for _, fn := range callbacks {
		fn(agentID)
	}
}

// HeartbeatSender sends periodic heartbeat messages.
// Start and Stop are safe to call from multiple goroutines and the sender
// can be restarted after a Stop.
type HeartbeatSender struct {
	agentID  string
	interval time.Duration
	queue    *MessageQueue
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	started  bool
	mu       sync.Mutex
}

// NewHeartbeatSender creates a new HeartbeatSender.
func NewHeartbeatSender(agentID string, interval time.Duration, queue *MessageQueue) *HeartbeatSender {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if queue == nil {
		log.Warn("NewHeartbeatSender: nil queue", "agent_id", agentID)
	}
	noOpCancel := func() {}
	return &HeartbeatSender{
		agentID:  agentID,
		interval: interval,
		queue:    queue,
		cancel:   noOpCancel,
	}
}

// Validate ensures the HeartbeatSender is properly configured.
func (s *HeartbeatSender) Validate() error {
	if s.queue == nil {
		return errors.ErrQueueNotInitialized
	}
	return nil
}

// Start starts sending heartbeats.
// This method can be called again after Stop.
func (s *HeartbeatSender) Start(ctx context.Context) {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.ctx, s.cancel = context.WithCancel(ctx)
	s.wg.Add(1)
	s.started = true
	s.mu.Unlock()

	go s.run()
}

// run is the main heartbeat sending loop.
func (s *HeartbeatSender) run() {
	defer s.wg.Done()

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.sendHeartbeat()
		}
	}
}

// sendHeartbeat sends a heartbeat message.
func (s *HeartbeatSender) sendHeartbeat() {
	if s.queue == nil {
		log.Error("heartbeat sender queue is nil", "agent_id", s.agentID)
		return
	}
	msg := NewHeartbeatMessage(s.agentID)
	if err := s.queue.Enqueue(s.ctx, msg); err != nil {
		log.Warn("heartbeat send failed", "agent_id", s.agentID, "error", err)
	}
}

// Stop stops sending heartbeats and waits for the run goroutine to exit.
func (s *HeartbeatSender) Stop() {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	s.started = false
	cancel := s.cancel
	s.mu.Unlock()

	cancel()
	s.wg.Wait()
}
