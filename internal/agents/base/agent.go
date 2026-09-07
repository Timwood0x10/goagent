package base

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/runtime/protocol/ahp"
)

var agentIDSeq atomic.Int64

// EventType represents the type of agent event.
type EventType int

const (
	// EventPlanning indicates the agent is planning.
	EventPlanning EventType = iota
	// EventTaskStart indicates a task has started.
	EventTaskStart
	// EventTaskProgress indicates progress on a task.
	EventTaskProgress
	// EventTaskComplete indicates a task has completed.
	EventTaskComplete
	// EventAggregating indicates the agent is aggregating results.
	EventAggregating
	// EventComplete indicates the agent has completed processing.
	EventComplete
	// EventError indicates an error occurred during processing.
	EventError
)

// AgentEvent represents an event emitted during agent processing.
type AgentEvent struct {
	// Type is the type of event.
	Type EventType
	// Source is the agent ID that emitted this event.
	Source string
	// Data is the event payload. Type depends on the event type.
	Data any
	// Err contains any error that occurred. Non-nil only for error ares_events.
	Err error
}

// Agent represents the base interface for all agents.
type Agent interface {
	// ID returns the unique identifier of the agent.
	ID() string
	// Type returns the type of the agent.
	Type() models.AgentType
	// Status returns the current status of the agent.
	Status() models.AgentStatus
	// Start starts the agent.
	Start(ctx context.Context) error
	// Stop stops the agent.
	Stop(ctx context.Context) error
	// Process handles input and returns result.
	Process(ctx context.Context, input any) (any, error)
	// ProcessStream handles input and returns a stream of ares_events.
	// The returned channel is closed when processing completes.
	ProcessStream(ctx context.Context, input any) (<-chan AgentEvent, error)
}

// Messenger defines message passing capabilities.
type Messenger interface {
	// SendMessage sends a message to another agent.
	SendMessage(ctx context.Context, msg *ahp.AHPMessage) error
	// ReceiveMessage receives a message from the message queue.
	ReceiveMessage(ctx context.Context) (*ahp.AHPMessage, error)
}

// Heartbeater defines heartbeat capabilities.
type Heartbeater interface {
	// Heartbeat sends a heartbeat signal.
	Heartbeat(ctx context.Context) error
	// IsAlive checks if the agent is alive.
	IsAlive() bool
}

// StatefulAgent can be restored from persisted state and ares_events.
// Agents that support resurrection should implement this interface.
type StatefulAgent interface {
	// RestoreState restores the agent's state from a snapshot map.
	// Called after factory creation during resurrection.
	RestoreState(state map[string]any) error

	// ReplayEvents replays a sequence of ares_events to reconstruct state.
	// Called after RestoreState to apply incremental changes.
	ReplayEvents(ares_events []*ares_events.Event) error

	// Snapshot returns a serializable snapshot of current state.
	// Snapshots are periodically captured and used during resurrection
	// to restore the agent's full state.
	Snapshot() (map[string]any, error)
}

// SnapshotStore persists agent snapshots for resurrection recovery.
// Implementations must be safe for concurrent use.
type SnapshotStore interface {
	// Save persists a snapshot for the given agent.
	Save(ctx context.Context, agentID string, snapshot map[string]any) error

	// Load retrieves the latest snapshot for the given agent.
	// Returns nil, nil if no snapshot exists.
	Load(ctx context.Context, agentID string) (map[string]any, error)

	// Delete removes the snapshot for the given agent.
	Delete(ctx context.Context, agentID string) error
}

// Config holds common agent configuration.
type Config struct {
	ID                string
	Type              models.AgentType
	HeartbeatInterval time.Duration
	MaxRetries        int
	Timeout           time.Duration
}

// DefaultConfig returns default agent configuration.
func DefaultConfig(agentType models.AgentType) *Config {
	return &Config{
		ID:                fmt.Sprintf("agent-%d", agentIDSeq.Add(1)),
		Type:              agentType,
		HeartbeatInterval: 30 * time.Second,
		MaxRetries:        3,
		Timeout:           5 * time.Minute,
	}
}
