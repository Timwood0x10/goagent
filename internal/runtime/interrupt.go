package runtime

import (
	"context"
	"sync"
)

// InterruptPlugin observes HITL lifecycle events and records them via the
// ExecutionCollector and EventBus. It implements both RuntimePlugin and
// WorkflowHook.
//
// The plugin observes the unified Runner's HITL lifecycle. It does not make
// approval decisions; it records them for observability, checkpointing,
// memory distillation, and evolution scoring.
type InterruptPlugin struct {
	mu        sync.Mutex // guards collector and bus
	name      string
	collector *ExecutionCollector // optional; if set, interrupts are recorded
	bus       EventBus
}

// NewInterruptPlugin creates an InterruptPlugin.
func NewInterruptPlugin(name string) *InterruptPlugin {
	if name == "" {
		name = "interrupt"
	}
	return &InterruptPlugin{name: name}
}

// WithCollector sets the execution collector for interrupt recording.
// Thread-safe: the collector may be swapped while the bus is running.
func (p *InterruptPlugin) WithCollector(c *ExecutionCollector) *InterruptPlugin {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.collector = c
	return p
}

// Name returns the plugin name.
func (p *InterruptPlugin) Name() string { return p.name }

// Capabilities returns the capabilities. InterruptPlugin advertises
// CapInterrupt so it is discoverable via PluginsByCap(CapInterrupt).
func (p *InterruptPlugin) Capabilities() []Capability { return []Capability{CapInterrupt} }

// Start saves the EventBus reference for emitting events.
func (p *InterruptPlugin) Start(_ context.Context, bus EventBus) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bus = bus
	return nil
}

// Stop shuts down the plugin.
func (p *InterruptPlugin) Stop(_ context.Context) error { return nil }

// BeforeStep is a no-op for this plugin.
func (p *InterruptPlugin) BeforeStep(_ context.Context, _ string, _ *Step) error { return nil }

// AfterStep inspects the step result for interrupt-related metadata and
// records the outcome via collector and EventBus.
func (p *InterruptPlugin) AfterStep(ctx context.Context, executionID string, result *StepResult) error {
	p.mu.Lock()
	collector := p.collector
	bus := p.bus
	p.mu.Unlock()

	// Check for interrupt metadata from the step result (set by the executor
	// when an interrupt was handled before step execution).
	if result.Metadata != nil {
		if action, ok := result.Metadata[PayloadKeyInterruptAction]; ok {
			feedback := result.Metadata[PayloadKeyInterruptFeedback]
			p.emitInterruptEvent(ctx, bus, executionID, result.StepID, action, feedback)
			if collector != nil {
				collector.RecordInterrupt(result.StepID, action, feedback)
			}
			return nil
		}
	}

	// Fallback: detect rejected interrupts by status and error pattern.
	if result.Status == StepStatusSkipped && result.Error != "" {
		p.emitInterruptEvent(ctx, bus, executionID, result.StepID, "reject", result.Error)
		if collector != nil {
			collector.RecordInterrupt(result.StepID, "reject", result.Error)
		}
	}

	return nil
}

// emitInterruptEvent publishes an interrupt lifecycle event on the bus. The
// bus and collector are passed in by the caller (which snapshots them under
// the lock) so this helper stays lock-free.
func (p *InterruptPlugin) emitInterruptEvent(ctx context.Context, bus EventBus, executionID, stepID, action, feedback string) {
	if bus == nil {
		return
	}
	bus.Emit(ctx, executionID, EventInterruptCreated, "runtime", map[string]any{
		PayloadKeyExecutionID: executionID,
		PayloadKeyStepID:      stepID,
		"action":              action,
		"feedback":            feedback,
	})
	log.Debug("interrupt plugin: recorded interrupt",
		"execution_id", executionID,
		"step_id", stepID,
		"action", action,
	)
}

var _ RuntimePlugin = (*InterruptPlugin)(nil)
var _ WorkflowHook = (*InterruptPlugin)(nil)
