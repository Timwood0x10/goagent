package arena

import (
	"context"
	"math/rand"
	"sync"
	"time"
)

// SurvivalConfig configures a survival test run.
type SurvivalConfig struct {
	Duration time.Duration `json:"duration"`
	Interval time.Duration `json:"interval"`
}

// defaultSurvivalConfig returns sensible defaults for a survival run.
func defaultSurvivalConfig() SurvivalConfig {
	return SurvivalConfig{
		Duration: 30 * time.Minute,
		Interval: 10 * time.Second,
	}
}

// SurvivalReport holds the result of a survival run.
type SurvivalReport struct {
	Duration   time.Duration   `json:"duration"`
	ActionsRun int             `json:"actions_run"`
	Score      ResilienceScore `json:"score"`
	Timeline   []SurvivalEvent `json:"timeline"`
}

// SurvivalEvent records a single chaos event.
type SurvivalEvent struct {
	Timestamp time.Time `json:"timestamp"`
	Action    Action    `json:"action"`
	Result    Result    `json:"result"`
}

// SurvivalStatus holds the current state of a running survival test.
type SurvivalStatus struct {
	Running    bool            `json:"running"`
	Config     SurvivalConfig  `json:"config"`
	ActionsRun int             `json:"actions_run"`
	StartedAt  time.Time       `json:"started_at,omitempty"`
	Elapsed    time.Duration   `json:"elapsed"`
	Timeline   []SurvivalEvent `json:"timeline"`
}

// survivalState tracks the in-progress survival run.
type survivalState struct {
	mu      sync.RWMutex
	running bool
	cancel  context.CancelFunc
	config  SurvivalConfig
	started time.Time
	events  []SurvivalEvent
}

// RunSurvival runs chaos actions at intervals for the configured duration.
// It randomly kills agents, removes edges, etc., and records everything.
// Only one survival run can be active at a time.
//
// Lock hierarchy (outer → inner): survival.mu → s.mu (via Execute).
// The survival.mu lock protects the survivalState fields (running, cancel,
// config, started, events) and is held briefly for state mutations and
// status queries. It must never be called while holding s.mu. Conversely,
// s.Execute acquires s.mu internally; it is called without holding any
// arena-level lock to avoid inversion. The shared events slice append at
// line ~134 is safe because it runs on the single goroutine inside the
// for-select loop while GetSurvivalStatus/StopSurvival only read under
// survival.mu.RLock/Lock respectively.
func (s *Service) RunSurvival(ctx context.Context, cfg SurvivalConfig) SurvivalReport {
	if cfg.Duration <= 0 {
		cfg = defaultSurvivalConfig()
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 10 * time.Second
	}

	// Create a cancellable context for this survival run.
	survCtx, cancel := context.WithCancel(ctx)

	// Mark survival as running.
	s.survival.mu.Lock()
	if s.survival.running {
		s.survival.mu.Unlock()
		cancel()
		log.Warn("arena: survival already running, returning empty report")
		return SurvivalReport{}
	}
	s.survival.running = true
	s.survival.cancel = cancel
	s.survival.config = cfg
	s.survival.started = time.Now()
	s.survival.events = make([]SurvivalEvent, 0)
	s.survival.mu.Unlock()

	defer func() {
		s.survival.mu.Lock()
		s.survival.running = false
		s.survival.mu.Unlock()
	}()

	start := time.Now()
	log.Info("arena: survival mode started",
		"duration", cfg.Duration,
		"interval", cfg.Interval,
	)

	timeline := make([]SurvivalEvent, 0)
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	deadline := start.Add(cfg.Duration)

	for {
		select {
		case <-survCtx.Done():
			log.Info("arena: survival mode cancelled")
			return s.buildSurvivalReport(time.Since(start), timeline)
		case now := <-ticker.C:
			if now.After(deadline) {
				log.Info("arena: survival mode completed",
					"actions", len(timeline),
					"duration", time.Since(start),
				)
				return s.buildSurvivalReport(time.Since(start), timeline)
			}

			action := s.randomChaosAction()
			result := s.Execute(survCtx, action)

			event := SurvivalEvent{
				Timestamp: time.Now(),
				Action:    action,
				Result:    result,
			}
			timeline = append(timeline, event)

			// Update shared survival state for status queries.
			s.survival.mu.Lock()
			s.survival.events = append(s.survival.events, event)
			s.survival.mu.Unlock()

			log.Info("arena: survival event",
				"type", action.Type,
				"target", action.TargetID,
				"success", result.Success,
				"elapsed", time.Since(start).Round(time.Second),
			)
		}
	}
}

// GetSurvivalStatus returns the current status of the survival run.
// The returned Timeline is a deep copy of the internal events slice to prevent
// data races between the survival goroutine (which appends events) and callers
// (which may iterate over the returned slice after the lock is released).
func (s *Service) GetSurvivalStatus() SurvivalStatus {
	s.survival.mu.RLock()
	defer s.survival.mu.RUnlock()

	// Copy the events slice to avoid returning a reference to internal state.
	timeline := make([]SurvivalEvent, len(s.survival.events))
	copy(timeline, s.survival.events)

	status := SurvivalStatus{
		Running:    s.survival.running,
		Config:     s.survival.config,
		ActionsRun: len(s.survival.events),
		Timeline:   timeline,
	}
	if s.survival.running {
		status.StartedAt = s.survival.started
		status.Elapsed = time.Since(s.survival.started)
	}
	return status
}

// StopSurvival cancels the currently running survival test.
func (s *Service) StopSurvival() {
	s.survival.mu.Lock()
	defer s.survival.mu.Unlock()
	if s.survival.running && s.survival.cancel != nil {
		s.survival.cancel()
		log.Info("arena: survival mode stop requested")
	}
}

// buildSurvivalReport constructs the final report from the timeline.
func (s *Service) buildSurvivalReport(duration time.Duration, timeline []SurvivalEvent) SurvivalReport {
	stats := s.Stats()
	avgRecovery := s.calculateAvgRecoveryTime(timeline)
	metrics := s.Metrics()
	score := CalculateScore(stats, avgRecovery, &metrics)

	return SurvivalReport{
		Duration:   duration,
		ActionsRun: len(timeline),
		Score:      score,
		Timeline:   timeline,
	}
}

// calculateAvgRecoveryTime computes the average duration of successful actions.
func (s *Service) calculateAvgRecoveryTime(events []SurvivalEvent) time.Duration {
	var total time.Duration
	var count int
	for _, ev := range events {
		if ev.Result.Success && ev.Result.Duration > 0 {
			total += ev.Result.Duration
			count++
		}
	}
	if count == 0 {
		return 0
	}
	return total / time.Duration(count)
}

// randomChaosAction generates a random chaos action targeting available resources.
func (s *Service) randomChaosAction() Action {
	actionTypes := []ActionType{
		ActionKillAgent,
		ActionKillLeader,
		ActionRemoveNode,
		ActionRemoveEdge,
		ActionPauseAgent,
		ActionResumeAgent,
		ActionSlowAgent,
		ActionKillOrchestrator,
		ActionNetworkPartition,
		ActionToolTimeout,
		ActionMemoryCorrupt,
		ActionMCPDisconnect,
		ActionLLMFailure,
	}
	actionType := actionTypes[rand.Intn(len(actionTypes))] // #nosec G404

	action := Action{
		ID:        randomID(),
		Type:      actionType,
		CreatedAt: time.Now(),
	}

	switch actionType {
	case ActionKillAgent:
		ids := s.injector.AvailableAgentIDs()
		if len(ids) > 0 {
			action.TargetID = ids[rand.Intn(len(ids))] // #nosec G404
		}
	case ActionRemoveNode:
		nodes := s.injector.DAGNodes(context.Background())
		if len(nodes) > 0 {
			action.TargetID = nodes[rand.Intn(len(nodes))] // #nosec G404
		}
	case ActionRemoveEdge:
		edges := s.injector.DAGEdges(context.Background())
		if len(edges) > 0 {
			chosen := edges[rand.Intn(len(edges))] // #nosec G404
			action.SourceID = chosen[0]
			action.TargetID = chosen[1]
		}
	case ActionKillLeader:
	case ActionKillOrchestrator:
	case ActionPauseAgent, ActionResumeAgent, ActionSlowAgent, ActionNetworkPartition,
		ActionToolTimeout, ActionMemoryCorrupt, ActionMCPDisconnect, ActionLLMFailure:
		ids := s.injector.AvailableAgentIDs()
		if len(ids) > 0 {
			action.TargetID = ids[rand.Intn(len(ids))] // #nosec G404
			if actionType == ActionSlowAgent {
				action.Metadata = map[string]any{
					"duration": time.Duration(5+rand.Intn(10)) * time.Second, // #nosec G404
				}
			}
			if actionType == ActionToolTimeout {
				action.Metadata = map[string]any{
					"duration": time.Duration(5+rand.Intn(10)) * time.Second, // #nosec G404
				}
			}
			if actionType == ActionLLMFailure {
				errTypes := []string{"rate_limit", "overloaded", "timeout"}
				action.Metadata = map[string]any{
					"error_type": errTypes[rand.Intn(len(errTypes))], // #nosec G404
				}
			}
		}
	}

	return action
}

// randomID generates a short random hex identifier.
func randomID() string {
	const hex = "0123456789abcdef"
	b := make([]byte, 16)
	for i := range b {
		b[i] = hex[rand.Intn(16)] // #nosec G404
	}
	return string(b)
}
