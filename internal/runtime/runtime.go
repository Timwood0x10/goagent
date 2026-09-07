// Package runtime provides lifecycle management for agents.
// Agents are disposable executors; the Runtime owns their birth, death, and resurrection.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Timwood0x10/ares/internal/agents/base"
	apperrors "github.com/Timwood0x10/ares/internal/errors"
)

// Sentinel errors for the runtime package.
var (
	// ErrAgentNotFound indicates the requested agent is not registered.
	// Wraps apperrors.ErrNotFound for generic checks via errors.Is(err, apperrors.ErrNotFound).
	ErrAgentNotFound = fmt.Errorf("agent not found: %w", apperrors.ErrNotFound)
	// ErrAgentAlreadyRegistered indicates an agent with the same ID is already registered.
	ErrAgentAlreadyRegistered = errors.New("agent already registered")
	// ErrRuntimeStopped indicates the runtime has been stopped and cannot accept new work.
	ErrRuntimeStopped = errors.New("runtime stopped")
	// ErrNilAgent indicates a nil agent was provided.
	ErrNilAgent = errors.New("nil agent")
	// ErrNilFactory indicates a nil factory was provided.
	ErrNilFactory = errors.New("nil factory")
)

// AgentFactory creates new agent instances for resurrection.
type AgentFactory func() base.Agent

// RuntimeStats holds runtime statistics.
type RuntimeStats struct {
	// ActiveAgents is the number of currently running agents.
	ActiveAgents int
	// TotalRestarts is the cumulative number of agent restarts.
	TotalRestarts int
	// Uptime is the duration since the runtime was started.
	Uptime time.Duration
	// BackgroundTasks is a snapshot of active detached-context operations,
	// sourced from ctxutil.BackgroundStats. Labels may include
	// "runtime:notify-agent-dead", "runtime:stop", "resurrection:stop-old", etc.
	BackgroundTasks map[string]int64
}

// Config holds runtime configuration.
type Config struct {
	// HealthCheckInterval is the interval between agent liveness checks.
	HealthCheckInterval time.Duration
	// MaxRestartsPerAgent is the maximum number of restarts allowed per agent.
	// A value of 0 means unlimited restarts.
	MaxRestartsPerAgent int
	// MaxReplayEvents caps the number of events loaded during replay.
	// Prevents unbounded memory usage on long-running agents.
	MaxReplayEvents int
	// AgentStopTimeout is the timeout for stopping a single agent.
	AgentStopTimeout time.Duration
	// OverallStopTimeout is the timeout for stopping all agents during shutdown.
	OverallStopTimeout time.Duration
	// RestoreTimeout is the timeout for a single agent restoration attempt.
	RestoreTimeout time.Duration
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() *Config {
	return &Config{
		HealthCheckInterval: 10 * time.Second,
		MaxRestartsPerAgent: 10,
		MaxReplayEvents:     10000,
		AgentStopTimeout:    10 * time.Second,
		OverallStopTimeout:  30 * time.Second,
		RestoreTimeout:      60 * time.Second,
	}
}

// Runtime manages agent lifecycle. Agents are disposable executors;
// Runtime owns their birth, death, and resurrection.
type Runtime interface {
	// StartAgent launches an agent in a managed goroutine.
	StartAgent(ctx context.Context, agent base.Agent) error

	// StopAgent gracefully stops an agent by ID.
	StopAgent(ctx context.Context, agentID string) error

	// RestartAgent stops and restarts an agent with fresh state.
	RestartAgent(ctx context.Context, agentID string) error

	// RestoreAgent creates a new agent from factory, replays events, and starts it.
	RestoreAgent(ctx context.Context, agentID string, factory AgentFactory) error

	// NotifyAgentDead is called by agents or safety nets when an agent dies.
	// It triggers asynchronous restoration.
	NotifyAgentDead(agentID string, reason string)

	// RegisterAgent registers an agent and its factory for lifecycle management.
	RegisterAgent(agent base.Agent, factory AgentFactory)

	// Start begins the runtime's monitoring loop.
	Start(ctx context.Context) error

	// Stop gracefully shuts down all agents and the runtime.
	Stop() error

	// Stats returns runtime statistics.
	Stats() RuntimeStats
}
