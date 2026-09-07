package arena

import (
	"time"

	flight "github.com/Timwood0x10/ares/internal/runtime/observability/flight"
)

// FlightBridge connects arena events to the flight recorder.
// When arena executes a fault injection action, the bridge records
// it as a timeline event and diagnostic record in the flight recorder.
type FlightBridge struct {
	recorder *flight.FlightRecorder
}

// NewFlightBridge creates a new bridge between arena and flight recorder.
func NewFlightBridge(recorder *flight.FlightRecorder) *FlightBridge {
	if recorder == nil {
		log.Warn("NewFlightBridge: nil recorder, flight recording disabled")
	}
	return &FlightBridge{recorder: recorder}
}

// OnActionExecuted is called after an arena action completes.
// It records the action as both a timeline event and a diagnostic record.
func (b *FlightBridge) OnActionExecuted(action Action, result Result) {
	if b.recorder == nil {
		return
	}

	b.recordTimelineEvent(action, result)

	if !result.Success {
		b.recordDiagnostic(action, result)
	}
}

// recordTimelineEvent converts an arena action to a flight timeline event.
func (b *FlightBridge) recordTimelineEvent(action Action, result Result) {
	eventType := flight.EventToolCall
	name := "arena:" + string(action.Type)

	te := flight.TimelineEvent{
		ID:       action.ID,
		AgentID:  action.TargetID,
		Type:     eventType,
		Name:     name,
		StartAt:  action.CreatedAt,
		EndAt:    action.CreatedAt.Add(result.Duration),
		Duration: result.Duration,
		Metadata: map[string]any{
			"source":      "arena",
			"action_type": string(action.Type),
			"source_id":   action.SourceID,
			"success":     result.Success,
			"target_id":   action.TargetID,
		},
	}

	if tl := b.recorder.Timeline(); tl != nil {
		tl.Add(te)
	}
}

// recordDiagnostic converts a failed arena action to a diagnostic record.
func (b *FlightBridge) recordDiagnostic(action Action, result Result) {
	cat := arenaActionToCategory(action.Type)
	suggestions := flight.SuggestFix(cat)
	suggestion := ""
	if len(suggestions) > 0 {
		suggestion = suggestions[0]
	}

	diag := flight.DiagnosticRecord{
		ID:         action.ID + "-diag",
		AgentID:    action.TargetID,
		TaskID:     "arena-" + string(action.Type),
		Category:   cat,
		RootCause:  "fault_injection: " + string(action.Type),
		Suggestion: suggestion,
		Timestamp:  time.Now(),
		Duration:   result.Duration,
		Context: map[string]any{
			"action_type": string(action.Type),
			"target_id":   action.TargetID,
			"source_id":   action.SourceID,
			"error":       result.Error,
		},
	}

	if engine := b.recorder.Diagnostics(); engine != nil {
		engine.Record(diag)
	}
}

// arenaActionToCategory maps arena action types to diagnostic categories.
func arenaActionToCategory(at ActionType) flight.DiagnosticCategory {
	switch at {
	case ActionKillLeader, ActionKillAgent, ActionKillOrchestrator:
		return flight.DiagConcurrencyError
	case ActionNetworkPartition:
		return flight.DiagNetworkError
	case ActionRemoveNode, ActionRemoveEdge:
		return flight.DiagConfigError
	case ActionPauseAgent, ActionResumeAgent:
		return flight.DiagConcurrencyError
	case ActionSlowAgent:
		return flight.DiagToolTimeout
	case ActionToolTimeout:
		return flight.DiagToolTimeout
	case ActionMemoryCorrupt:
		return flight.DiagMemoryError
	case ActionMCPDisconnect:
		return flight.DiagNetworkError
	case ActionLLMFailure:
		return flight.DiagLLMError
	default:
		return flight.DiagUnknown
	}
}
