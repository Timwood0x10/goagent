package engine

import (
	"context"

	"github.com/Timwood0x10/ares/internal/runtime"
)

// HITLFeedbackPlugin wraps an InterruptHandler and InterruptStore as a
// runtime.RuntimePlugin. It implements both RuntimePlugin and WorkflowHook,
// emitting lifecycle events for observability and evolution scoring.
type HITLFeedbackPlugin struct {
	name    string
	handler InterruptHandler
	store   InterruptStore
	bus     runtime.EventBus
}

// NewHITLFeedbackPlugin creates a HITLFeedbackPlugin.
// If handler is nil, the plugin observes but does not handle interrupts.
// If store is nil, feedback is not persisted.
func NewHITLFeedbackPlugin(name string, handler InterruptHandler, store InterruptStore) *HITLFeedbackPlugin {
	if name == "" {
		name = "hitl-feedback"
	}
	return &HITLFeedbackPlugin{
		name:    name,
		handler: handler,
		store:   store,
	}
}

func (p *HITLFeedbackPlugin) Name() string { return p.name }
func (p *HITLFeedbackPlugin) Capabilities() []runtime.Capability {
	return []runtime.Capability{runtime.CapObserver}
}
func (p *HITLFeedbackPlugin) Start(_ context.Context, bus runtime.EventBus) error {
	p.bus = bus
	return nil
}
func (p *HITLFeedbackPlugin) Stop(_ context.Context) error { return nil }

// InterruptHandler returns the wrapped handler, or nil if not set.
func (p *HITLFeedbackPlugin) InterruptHandler() InterruptHandler { return p.handler }

// InterruptStore returns the wrapped store, or nil if not set.
func (p *HITLFeedbackPlugin) InterruptStore() InterruptStore { return p.store }

// BeforeStep is a no-op.
func (p *HITLFeedbackPlugin) BeforeStep(_ context.Context, _ string, _ *runtime.Step) error {
	return nil
}

// AfterStep inspects step results for interrupt-related metadata and emits events.
func (p *HITLFeedbackPlugin) AfterStep(_ context.Context, executionID string, result *runtime.StepResult) error {
	if result.Metadata != nil {
		if action, ok := result.Metadata[runtime.PayloadKeyInterruptAction]; ok {
			feedback := result.Metadata[runtime.PayloadKeyInterruptFeedback]
			p.emitEvent(executionID, result.StepID, action, feedback)
			return nil
		}
	}
	if result.Status == runtime.StepStatusSkipped && result.Error != "" {
		p.emitEvent(executionID, result.StepID, "reject", result.Error)
	}
	return nil
}

func (p *HITLFeedbackPlugin) emitEvent(executionID, stepID, action, feedback string) {
	if p.bus == nil {
		return
	}
	p.bus.Emit(context.Background(), executionID, runtime.EventInterruptCreated, "workflow", map[string]any{
		runtime.PayloadKeyExecutionID: executionID,
		runtime.PayloadKeyStepID:      stepID,
		"action":                      action,
		"feedback":                    feedback,
	})
	log.Debug("hitl feedback plugin: recorded interrupt",
		"execution_id", executionID,
		"step_id", stepID,
		"action", action,
	)
}
