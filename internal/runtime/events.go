package runtime

import "github.com/Timwood0x10/ares/internal/ares_events"

// Workflow event types emitted by the PluginBus.
const (
	EventWorkflowStarted   ares_events.EventType = "workflow.started"
	EventWorkflowCompleted ares_events.EventType = "workflow.completed"
	EventWorkflowFailed    ares_events.EventType = "workflow.failed"
	EventStepStarted       ares_events.EventType = "step.started"
	EventStepCompleted     ares_events.EventType = "step.completed"
	EventStepFailed        ares_events.EventType = "step.failed"
)

// EventCheckpointSaved is emitted after a checkpoint is persisted.
const EventCheckpointSaved ares_events.EventType = "checkpoint.saved"

// EventRouteDecided is emitted when a routing decision is made.
const EventRouteDecided ares_events.EventType = "route.decided"

// EventToolCalled is emitted when a tool is invoked.
const EventToolCalled ares_events.EventType = "tool.called"

// EventMemoryHit is emitted when memory is retrieved.
const EventMemoryHit ares_events.EventType = "memory.hit"

// EventInterruptCreated is emitted when a HITL interrupt is created.
const EventInterruptCreated ares_events.EventType = "interrupt.created"

// EventInterruptResolved is emitted when a HITL interrupt is resolved.
const EventInterruptResolved ares_events.EventType = "interrupt.resolved"

// Plugin lifecycle event types emitted by the PluginBus.
const (
	EventPluginStarted ares_events.EventType = "plugin.started"
	EventPluginStopped ares_events.EventType = "plugin.stopped"
	EventPluginFailed  ares_events.EventType = "plugin.failed"
)

// Payload keys used in workflow ares_events.
const (
	PayloadKeyExecutionID        = "execution_id"
	PayloadKeyWorkflowID         = "workflow_id"
	PayloadKeyStepID             = "step_id"
	PayloadKeyStatus             = "status"
	PayloadKeyDuration           = "duration_ms"
	PayloadKeyError              = "error"
	PayloadKeyRouteReason        = "route_reason"
	PayloadKeyToolName           = "tool_name"
	PayloadKeyInterruptAction    = "interrupt_action"
	PayloadKeyInterruptFeedback  = "interrupt_feedback"
	PayloadKeyPluginName         = "plugin_name"
	PayloadKeyPluginCapabilities = "plugin_capabilities"
)
