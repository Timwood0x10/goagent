package arena

//nolint: errcheck // best-effort operations: ResponseWriter writes, cleanup Close/Wait, deferred shutdown
import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/evidence"
)

// EventStore is the subset of event store capabilities needed by the arena.
type EventStore interface {
	Append(ctx context.Context, streamID string, ares_events []*ares_events.Event, expectedVersion int64) error
	StreamVersion(ctx context.Context, streamID string) (int64, error)
	Subscribe(ctx context.Context, filter ares_events.EventFilter) (<-chan *ares_events.Event, error)
}

const (
	// arenaStreamID is the event stream identifier for arena actions.
	arenaStreamID = "arena"
)

// Service orchestrates chaos actions, records results, and emits ares_events.
type Service struct {
	injector *Injector
	store    EventStore
	evStore  evidence.Store // optional: unified Evidence Store
	actions  []Result
	stats    Stats
	mu       sync.RWMutex
	metrics  *MetricsCollector
	bridge   *FlightBridge

	evolutionBridge *EvolutionBridge

	survival survivalState
}

// NewService creates a Service with the given injector and optional event store.
func NewService(injector *Injector, store EventStore, evStore evidence.Store) *Service {
	if injector == nil {
		log.Warn("NewService: nil injector")
	}
	if store == nil {
		log.Warn("NewService: nil event store")
	}
	return &Service{
		injector: injector,
		store:    store,
		evStore:  evStore,
		actions:  make([]Result, 0),
		metrics:  NewMetricsCollector(),
	}
}

// SetFlightBridge attaches a flight bridge for arena-flight integration.
func (s *Service) SetFlightBridge(b *FlightBridge) {
	s.bridge = b
}

// SetEvolutionBridge attaches an evolution bridge for chaos→Coordinator integration.
func (s *Service) SetEvolutionBridge(b *EvolutionBridge) {
	s.evolutionBridge = b
}

// Execute runs the given action, records the result, and emits an event.
func (s *Service) Execute(ctx context.Context, action Action) Result {
	start := time.Now()

	var err error
	switch action.Type {
	case ActionKillLeader:
		leaderID, killErr := s.injector.KillLeader(ctx)
		if killErr == nil {
			action.Metadata = mergeMap(action.Metadata, map[string]any{
				"killed_leader_id": leaderID,
			})
		}
		err = killErr
	case ActionKillAgent:
		err = s.injector.KillAgent(ctx, action.TargetID)
	case ActionRemoveNode:
		err = s.injector.RemoveNode(ctx, action.TargetID)
	case ActionRemoveEdge:
		err = s.injector.RemoveEdge(ctx, action.SourceID, action.TargetID)
	case ActionPauseAgent:
		err = s.injector.PauseAgent(ctx, action.TargetID)
	case ActionResumeAgent:
		err = s.injector.ResumeAgent(ctx, action.TargetID)
	case ActionSlowAgent:
		delay, parseErr := parseDuration(action, 5*time.Second)
		if parseErr != nil {
			err = parseErr
			break
		}
		err = s.injector.SlowAgent(ctx, action.TargetID, delay)
	case ActionKillOrchestrator:
		orchID, killErr := s.injector.KillOrchestrator(ctx)
		if killErr == nil {
			action.Metadata = mergeMap(action.Metadata, map[string]any{
				"killed_orchestrator_id": orchID,
			})
		}
		err = killErr
	case ActionNetworkPartition:
		err = s.injector.NetworkPartition(ctx, action.TargetID)
	case ActionToolTimeout:
		timeout, parseErr := parseDuration(action, 5*time.Second)
		if parseErr != nil {
			err = parseErr
			break
		}
		err = s.injector.ToolTimeout(ctx, action.TargetID, timeout)
	case ActionMemoryCorrupt:
		err = s.injector.CorruptMemory(ctx, action.TargetID)
	case ActionMCPDisconnect:
		err = s.injector.DisconnectMCP(ctx, action.TargetID)
	case ActionLLMFailure:
		errType, ok := action.Metadata["error_type"].(string)
		if !ok || errType == "" {
			errType = "rate_limit"
		}
		err = s.injector.InjectLLMFailure(ctx, action.TargetID, errType)
	default:
		err = fmt.Errorf("arena: unknown action type: %s", action.Type)
	}

	duration := time.Since(start)
	result := Result{
		Success:  err == nil,
		Action:   action,
		Duration: duration,
	}
	if err != nil {
		result.Error = err.Error()
	}

	s.mu.Lock()
	s.actions = append(s.actions, result)
	s.stats.TotalActions++
	if result.Success {
		s.stats.SuccessfulActions++
	} else {
		s.stats.FailedActions++
	}
	s.stats.LastAction = time.Now()
	s.mu.Unlock()

	// Record action result in the metrics collector.
	s.metrics.RecordActionResult(action.Type, result.Success, duration)

	s.emitEvent(ctx, action, result)

	// Emit failure evidence to the unified Evidence Store.
	if !result.Success && s.evStore != nil {
		_ = s.evStore.Append(ctx, evidence.NewEvidence(
			"chaos",
			evidence.KindFailure,
			map[string]any{
				"action_id":   action.ID,
				"action_type": string(action.Type),
				"target_id":   action.TargetID,
				"error":       result.Error,
				"duration_ms": duration.Milliseconds(),
			},
			evidence.WithMetadata("action_type", string(action.Type)),
			evidence.WithMetadata("target_id", action.TargetID),
		))
	}

	if result.Success {
		log.Info("arena: action executed",
			"action_id", action.ID,
			"type", action.Type,
			"target", action.TargetID,
			"duration", duration,
		)
	} else {
		log.Error("arena: action failed",
			"action_id", action.ID,
			"type", action.Type,
			"target", action.TargetID,
			"error", result.Error,
			"duration", duration,
		)
	}

	if s.bridge != nil {
		s.bridge.OnActionExecuted(action, result)
	}

	if s.evolutionBridge != nil {
		s.evolutionBridge.OnActionExecuted(action, result)
	}

	return result
}

// History returns all past results.
func (s *Service) History() []Result {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Result, len(s.actions))
	copy(out, s.actions)
	return out
}

// Stats returns aggregate statistics.
func (s *Service) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.stats
}

// Metrics returns the metrics collector's snapshot.
func (s *Service) Metrics() MetricsSnapshot {
	return s.metrics.Snapshot()
}

// Reset clears history, stats, and metrics.
func (s *Service) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.actions = make([]Result, 0)
	s.stats = Stats{}
	s.metrics.Reset()
}

// Subscribe returns a channel of arena ares_events from the event store.
// Returns nil channel if no event store is configured.
func (s *Service) Subscribe(ctx context.Context) (<-chan *ares_events.Event, error) {
	if s.store == nil {
		return nil, errors.New("arena: event store not configured")
	}
	ch, err := s.store.Subscribe(ctx, ares_events.EventFilter{
		StreamIDs: []string{arenaStreamID},
	})
	if err != nil {
		return nil, fmt.Errorf("arena: subscribe: %w", err)
	}
	return ch, nil
}

// emitEvent publishes an arena event using the canonical ares_events.Emit.
func (s *Service) emitEvent(ctx context.Context, action Action, result Result) {
	if s.store == nil {
		return
	}

	eventType := "arena.action.executed"
	if !result.Success {
		eventType = "arena.action.failed"
	}

	if !ares_events.Emit(ctx, s.store, arenaStreamID, ares_events.EventType(eventType), "arena", map[string]any{
		"action_id": action.ID,
		"type":      string(action.Type),
		"target_id": action.TargetID,
		"source_id": action.SourceID,
		"success":   result.Success,
		"error":     result.Error,
		"duration":  result.Duration.String(),
	}) {
		log.Warn("failed to emit event", "event_type", eventType, "stream_id", arenaStreamID)
	}
}

// mergeMap combines two maps into a new map. The second map's values win on conflict.
func mergeMap(base, override map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}

// parseDuration extracts a duration from action.Metadata["duration"] or returns a default.
func parseDuration(action Action, defaultDuration time.Duration) (time.Duration, error) {
	if action.Metadata == nil {
		return defaultDuration, nil
	}
	durVal, ok := action.Metadata["duration"]
	if !ok {
		return defaultDuration, nil
	}
	switch v := durVal.(type) {
	case time.Duration:
		return v, nil
	case string:
		d, err := time.ParseDuration(v)
		if err != nil {
			return 0, fmt.Errorf("arena: invalid duration %q: %w", v, err)
		}
		return d, nil
	case float64:
		// JSON numbers decode as float64; interpret the value as seconds so
		// that 5.0 means 5s rather than 5ns (time.Duration is int64 nanoseconds).
		return time.Duration(v * float64(time.Second)), nil
	default:
		return 0, fmt.Errorf("arena: unsupported duration type %T", durVal)
	}
}
