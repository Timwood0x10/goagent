// Package runtime defines the plugin contract for extending workflow execution.
// Plugins are registered on a PluginBus which manages their lifecycle and
// invokes them at defined extension points (BeforeStep, AfterStep).
package runtime

import (
	"context"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// Capability represents a functional area a plugin provides.
type Capability string

const (
	CapObserver   Capability = "observer"
	CapCheckpoint Capability = "checkpoint"
	CapRouter     Capability = "router"
	CapLoop       Capability = "loop"
	CapMemory     Capability = "memory"
	CapEvolution  Capability = "evolution"
	CapTool       Capability = "tool"
	CapRecovery   Capability = "recovery"
	CapInterrupt  Capability = "interrupt"
)

// RuntimePlugin is the interface all plugins must implement.
type RuntimePlugin interface {
	// Name returns a unique identifier for this plugin instance.
	Name() string

	// Capabilities returns the set of capabilities this plugin provides.
	Capabilities() []Capability

	// Start initializes the plugin. The plugin receives the EventBus for
	// emitting and subscribing to workflow ares_events.
	// Start MUST be non-blocking; long-running work should use a goroutine.
	Start(ctx context.Context, bus EventBus) error

	// Stop shuts down the plugin and releases resources.
	Stop(ctx context.Context) error
}

// WorkflowHook is an optional interface a plugin may implement to intercept
// step-level lifecycle ares_events. Hooks are called synchronously by the bus
// before and after each step executes.
type WorkflowHook interface {
	// BeforeStep is called before a step executes. Returning an error
	// causes the bus to log the error and continue execution.
	BeforeStep(ctx context.Context, executionID string, step *Step) error

	// AfterStep is called after a step completes, regardless of success or
	// failure. The result parameter contains the final status and output.
	AfterStep(ctx context.Context, executionID string, result *StepResult) error
}

// MemoryPlugin provides memory-aware routing advice and task context
// for workflow execution. Implementations query the memory system for
// similar past executions and return routing suggestions.
type MemoryPlugin interface {
	RuntimePlugin
	// AdviseRoute returns routing suggestions based on similar past executions.
	AdviseRoute(ctx context.Context, state RouteState) ([]RouteAdvice, error)
}

// RouteAdvice is a single routing suggestion from a MemoryPlugin.
type RouteAdvice struct {
	NextStepID string  `json:"next_step_id"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// EvolutionPlugin provides runtime recommendations based on evolutionary
// computation (genome, scoring, mutation). It consumes execution outcomes
// and produces suggestions for agent selection, routing, and recovery.
type EvolutionPlugin interface {
	RuntimePlugin
	// Recommend returns a runtime recommendation based on execution state.
	Recommend(ctx context.Context, state ExecutionState) (*RuntimeRecommendation, error)
	// RecordOutcome ingests a completed execution outcome for offline learning.
	RecordOutcome(ctx context.Context, outcome ExecutionOutcome) error
}

// ExecutionState contains the inputs an EvolutionPlugin needs to make a
// recommendation.
type ExecutionState struct {
	ExecutionID    string
	WorkflowID     string
	CurrentStepID  string
	StepHistory    []StepResult
	RouteHistory   []RouteRecord
	ToolHistory    []ToolRecord
	MemoryHits     []MemoryHitRecord
	ScoringSignals []ScoringSignal
}

// RuntimeRecommendation is the output of an EvolutionPlugin.Recommend call.
type RuntimeRecommendation struct {
	PreferredAgent string  `json:"preferred_agent,omitempty"`
	RouterWeight   float64 `json:"router_weight,omitempty"`
	MutationHint   string  `json:"mutation_hint,omitempty"`
	Confidence     float64 `json:"confidence"`
}

// ExecutionOutcome represents the final state of a completed execution for
// evolution consumption.
type ExecutionOutcome struct {
	ExecutionID    string
	WorkflowID     string
	Status         string
	Duration       int64 // milliseconds
	TotalSteps     int
	FailedSteps    int
	SkippedSteps   int
	RouteCount     int
	ToolCount      int
	MemoryHitCount int
	InterruptCount int
	ErrorCount     int
}

// RecoveryPlugin provides step recovery decisions when a step fails.
type RecoveryPlugin interface {
	RuntimePlugin
	// ShouldRecover returns true if the step should be recovered. Plugins
	// may use the failure details and execution state to decide.
	ShouldRecover(ctx context.Context, failure StepFailure, state ExecutionState) bool
}

// StepFailure captures the context of a failed step for recovery decisions.
type StepFailure struct {
	ExecutionID string
	WorkflowID  string
	StepID      string
	Error       string
}

// EventBus is the event system exposed to plugins. It allows emitting
// structured ares_events that are fanned out to all subscribers.
type EventBus interface {
	// Emit publishes an event with the given stream ID to all subscribers.
	// moduleName identifies the emitting module for traceability.
	// Implementations MUST NOT block on slow subscribers (drop ares_events if
	// buffers are full).
	Emit(ctx context.Context, streamID string, eventType ares_events.EventType, moduleName string, payload map[string]any)

	// Subscribe returns a channel that receives ares_events matching the filter.
	// The channel is closed when the context is cancelled.
	Subscribe(ctx context.Context, filter ares_events.EventFilter) (<-chan *ares_events.Event, error)
}
