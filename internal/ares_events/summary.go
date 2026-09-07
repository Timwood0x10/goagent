package ares_events

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// EventSummary represents a compacted/summarized snapshot of a range of events
// for a given stream (agent). It is stored in a relational database (not vector DB)
// and provides a high-level view of what happened during an event window.
//
// Design goal: "Agent X ran task Y, called N tools/functions, emitted M events,
// bound to user Z request" — a detailed but compact summary.
type EventSummary struct {
	ID        string `json:"id"`
	StreamID  string `json:"stream_id"`  // Agent ID (same as event stream ID)
	AgentID   string `json:"agent_id"`   // Agent identifier
	TaskID    string `json:"task_id"`    // Primary task ID in this window
	SessionID string `json:"session_id"` // Associated session ID
	UserID    string `json:"user_id"`    // User who initiated the request

	// SummaryText is a human-readable summary of the event sequence.
	// Example: "Agent agent-1 ran task task-42, called 5 tools (search, book, weather),
	// emitted 23 events over 3m12s, completed successfully."
	SummaryText string `json:"summary_text"`

	// Aggregated metrics from the compacted event window.
	EventCount   int       `json:"event_count"`   // Number of events compacted into this summary
	StartVersion int64     `json:"start_version"` // First event version in this window
	EndVersion   int64     `json:"end_version"`   // Last event version in this window
	StartTime    time.Time `json:"start_time"`    // First event timestamp
	EndTime      time.Time `json:"end_time"`      // Last event timestamp

	// EventTypeCounts maps event type to occurrence count in this window.
	EventTypeCounts map[string]int `json:"event_type_counts,omitempty"`

	// Key payload extracts preserved from events for quick lookup.
	TasksCreated []string `json:"tasks_created,omitempty"` // Task IDs created in this window
	ToolsCalled  []string `json:"tools_called,omitempty"`  // Tool/function names called
	Errors       []string `json:"errors,omitempty"`        // Error messages encountered

	// RequestSummary is the user's original request text (from session.created / message.added).
	RequestSummary string `json:"request_summary,omitempty"`

	// Outcome describes the final state: "completed", "failed", "partial".
	Outcome string `json:"outcome"`

	Metadata  json.RawMessage `json:"metadata,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

// NewEventSummaryID generates a new unique summary identifier.
func NewEventSummaryID() string {
	return uuid.New().String()
}

// Duration returns the time span covered by this summary.
func (s *EventSummary) Duration() time.Duration {
	return s.EndTime.Sub(s.StartTime)
}

// SummaryRepository defines the persistence interface for event summaries.
// Implementations store summaries in a relational database (PostgreSQL).
type SummaryRepository interface {
	// Save persists an event summary to the database.
	Save(ctx context.Context, summary *EventSummary) error

	// FindByStreamID returns all summaries for a stream, ordered by start_version ascending.
	FindByStreamID(ctx context.Context, streamID string) ([]*EventSummary, error)

	// FindByAgentAndTask returns summaries matching both agent and task.
	FindByAgentAndTask(ctx context.Context, agentID, taskID string) ([]*EventSummary, error)

	// FindByAgentID returns all summaries for an agent across all streams/tasks.
	FindByAgentID(ctx context.Context, agentID string) ([]*EventSummary, error)

	// FindLatestByStreamID returns the most recent summary for a stream.
	FindLatestByStreamID(ctx context.Context, streamID string) (*EventSummary, error)

	// Delete removes a summary by ID.
	Delete(ctx context.Context, id string) error

	// DeleteOlderThan removes summaries created before the given threshold.
	DeleteOlderThan(ctx context.Context, threshold time.Time) (int64, error)
}

// CompactionConfig controls when and how event compaction is triggered.
type CompactionConfig struct {
	// Threshold is the number of events in a stream that triggers compaction.
	// When a stream exceeds this count, older events are compacted into summaries.
	Threshold int `json:"threshold"`

	// KeepRecent is the number of most recent events to retain in the live stream
	// after compaction. Events before this window are candidates for compaction.
	KeepRecent int `json:"keep_recent"`

	// MaxSummariesPerStream is the maximum number of summaries to keep per stream.
	// Older summaries are merged or pruned when this limit is exceeded.
	MaxSummariesPerStream int `json:"max_summaries_per_stream"`

	// SummaryTTL defines how long summaries are retained before being eligible for cleanup.
	SummaryTTL time.Duration `json:"summary_ttl"`

	// EnableTrimming controls whether compacted events are deleted from the events table.
	EnableTrimming bool `json:"enable_trimming"`
}

// DefaultCompactionConfig returns sensible defaults for production use.
func DefaultCompactionConfig() CompactionConfig {
	return CompactionConfig{
		Threshold:             500,
		KeepRecent:            100,
		MaxSummariesPerStream: 50,
		SummaryTTL:            30 * 24 * time.Hour, // 30 days
		// enable trimming so compacted events are actually deleted from
		// the events table. Without this, SummaryTTL is never enforced and the
		// store grows without bound. The trimStore is wired by the caller
		// (NewCompactableEventStore) — when nil, trimming is a no-op but the
		// flag is correct for callers that do wire one.
		EnableTrimming: true,
	}
}
