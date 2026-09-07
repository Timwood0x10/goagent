package evolution

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// FlightToExperienceAdapter converts Flight Recorder diagnostic data into Experience entries.
// It subscribes to task completion/failure ares_events and generates experiences from diagnostics,
// but only for tasks that have diagnostic issues (normal executions are not learned from).
type FlightToExperienceAdapter struct {
	flight  FlightRecorder
	expRepo ExperienceRepository
}

// NewFlightToExperienceAdapter creates a new adapter with required dependencies.
//
// Args:
//
//	flight - the flight recorder interface for accessing diagnostics.
//	expRepo - the experience repository for persisting experiences.
//
// Returns:
//
//	*FlightToExperienceAdapter - the configured adapter instance.
func NewFlightToExperienceAdapter(flight FlightRecorder, expRepo ExperienceRepository) *FlightToExperienceAdapter {
	return &FlightToExperienceAdapter{
		flight:  flight,
		expRepo: expRepo,
	}
}

// Run starts the adapter's event consumption loop.
// It subscribes to task failure ares_events and generates experiences from diagnostics.
// The loop runs until ctx is cancelled.
//
// Args:
//
//	ctx - operation context. Cancelling it stops the event consumption loop.
//
// Returns:
//
//	error - any error encountered during subscription or processing.
func (a *FlightToExperienceAdapter) Run(ctx context.Context) error {
	if a.flight == nil || a.expRepo == nil {
		return errors.New("flight recorder and experience repo are required")
	}

	subscriber := a.flight.EventStore()
	if subscriber == nil {
		return errors.New("event store subscriber is not available")
	}

	ch, err := subscriber.Subscribe(ctx, ares_events.EventFilter{
		Types: []ares_events.EventType{
			ares_events.EventTaskFailed,
			ares_events.EventStepFailed,
			ares_events.EventStepRecoveryFailed,
		},
	})
	if err != nil {
		return fmt.Errorf("subscribe to ares_events: %w", err)
	}

	slog.InfoContext(ctx, "[Evolution] Adapter started, listening for failure ares_events")

	for evt := range ch {
		if evt == nil {
			continue
		}
		a.processEvent(ctx, evt)
	}

	slog.InfoContext(ctx, "[Evolution] Adapter stopped")
	return nil
}

// processEvent handles a single event by extracting diagnostics and creating experiences.
//
// Args:
//
//	ctx - operation context.
//	event - the event to process.
func (a *FlightToExperienceAdapter) processEvent(ctx context.Context, event *ares_events.Event) {
	agentID := event.StreamID
	if agentID == "" {
		slog.DebugContext(ctx, "[Evolution] Skipping event without agent ID")
		return
	}

	diagnostics := a.flight.Diagnostics()
	if diagnostics == nil {
		return
	}

	report := diagnostics.Get(agentID)
	if report == nil || !report.HasIssues {
		return
	}

	for _, record := range report.Records {
		if record.TaskID == "" || record.TaskID != extractTaskID(event) {
			continue
		}

		exp := a.buildExperience(record, agentID)
		if exp == nil {
			continue
		}

		if err := a.expRepo.Create(ctx, exp); err != nil {
			slog.ErrorContext(ctx, "[Evolution] Failed to store experience",
				"agent_id", agentID,
				"record_id", record.ID,
				"error", err)
			continue
		}

		slog.InfoContext(ctx, "[Evolution] Experience created from diagnostic",
			"agent_id", agentID,
			"experience_type", exp.Type,
			"score", exp.Score)
	}
}

// buildExperience converts a diagnostic record into an Experience entry.
// Returns nil if the record has no learnable issues (e.g., low severity).
//
// Args:
//
//	record - the diagnostic record to convert.
//	agentID - the agent identifier.
//
// Returns:
//
//	*Experience - the converted experience, or nil if not learnable.
func (a *FlightToExperienceAdapter) buildExperience(record DiagnosticRecord, agentID string) *Experience {
	// Skip records with low severity (not worth learning from).
	if record.Severity < 3 {
		return nil
	}

	// Skip records without meaningful content.
	if record.RootCause == "" && record.Category == "" {
		return nil
	}

	// Score is inversely related to severity: more severe = lower score
	// because severe failures indicate problems we want to avoid, not repeat.
	score := severityToScore(record.Severity)

	solution := record.Suggestion
	if solution == "" {
		solution = fmt.Sprintf("Avoid %s: %s", record.Category, record.RootCause)
	}

	return &Experience{
		Type:     TypeFailure,
		Problem:  fmt.Sprintf("[%s] %s", record.Category, record.RootCause),
		Solution: solution,
		Score:    score,
		Source:   "flight_recorder",
		AgentID:  agentID,
		Metadata: map[string]interface{}{
			"diagnostic_id":       record.ID,
			"task_id":             record.TaskID,
			"category":            record.Category,
			"severity":            record.Severity,
			"original_cause":      record.RootCause,
			"original_suggestion": record.Suggestion,
		},
	}
}

// extractTaskID extracts the task ID from an event payload.
//
// Args:
//
//	event - the event to extract from.
//
// Returns:
//
//	string - the task ID, or empty string if not found.
func extractTaskID(event *ares_events.Event) string {
	if event == nil || event.Payload == nil {
		return ""
	}
	// nolint:errcheck // Type assertion returns false for non-string types; empty string is an acceptable default.
	taskID, _ := event.Payload["task_id"].(string)
	return taskID
}

// severityToScore converts a severity value (1-10) to a score (0-1).
// Higher severity results in lower scores because severe failures indicate
// patterns to avoid rather than solutions to replicate.
//
// Args:
//
//	severity - the severity level (1-10).
//
// Returns:
//
//	float64 - the corresponding score between 0 and 1.
func severityToScore(severity int) float64 {
	if severity <= 0 {
		return 1.0
	}
	if severity >= 10 {
		return 0.1
	}
	// Inverse linear mapping: severity 1 -> 1.0, severity 10 -> 0.1
	return float64(11-severity) / 10.0
}
