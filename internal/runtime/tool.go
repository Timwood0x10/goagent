package runtime

import (
	"context"
	"fmt"
	"sync"
)

// ToolPlugin records tool invocations via the ExecutionCollector and validates
// that tools used by steps are registered in its allowlist.
//
// Allowlist contract: validation is only enforced once at least one tool has
// been registered via RegisterTool. An empty registry means "no allowlist
// configured" and every tool invocation is accepted (recorded without error).
// Once any tool is registered, a step invoking a tool that is NOT in the
// registry causes AfterStep to return ErrToolNotRegistered (wrapped with the
// tool name). The bus logs the error and continues execution — validation is
// observational, not blocking.
type ToolPlugin struct {
	mu        sync.Mutex
	name      string
	collector *ExecutionCollector
	registry  map[string]bool // registered tool names; empty = no allowlist
}

// NewToolPlugin creates a ToolPlugin.
func NewToolPlugin(name string) *ToolPlugin {
	if name == "" {
		name = "tool"
	}
	return &ToolPlugin{
		name:     name,
		registry: make(map[string]bool),
	}
}

// WithCollector sets the execution collector for tool recording.
func (p *ToolPlugin) WithCollector(c *ExecutionCollector) *ToolPlugin {
	p.collector = c
	return p
}

// RegisterTool adds a tool name to the allowed registry.
func (p *ToolPlugin) RegisterTool(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.registry[name] = true
}

// IsRegistered returns true if the tool name is in the registry.
func (p *ToolPlugin) IsRegistered(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.registry[name]
}

// hasAllowlist reports whether any tool has been registered, meaning the
// allowlist is configured and should be enforced. Callers must hold p.mu.
func (p *ToolPlugin) hasAllowlist() bool { return len(p.registry) > 0 }

// Name returns the plugin name.
func (p *ToolPlugin) Name() string { return p.name }

// Capabilities returns the capabilities.
func (p *ToolPlugin) Capabilities() []Capability { return []Capability{CapTool} }

// Start initializes the tool plugin.
func (p *ToolPlugin) Start(_ context.Context, _ EventBus) error { return nil }

// Stop shuts down the tool plugin.
func (p *ToolPlugin) Stop(_ context.Context) error { return nil }

// BeforeStep is a no-op for this plugin.
func (p *ToolPlugin) BeforeStep(_ context.Context, _ string, _ *Step) error { return nil }

// AfterStep inspects step metadata for tool invocation information, validates
// the tool against the registry (when an allowlist is configured), and records
// tool calls via the collector.
func (p *ToolPlugin) AfterStep(_ context.Context, executionID string, result *StepResult) error {
	if result.Metadata == nil {
		return nil
	}
	toolName, ok := result.Metadata[PayloadKeyToolName]
	if !ok || toolName == "" {
		return nil
	}

	// Validate against the allowlist. Only enforce when at least one tool has
	// been registered; an empty registry means "no allowlist configured".
	p.mu.Lock()
	enforce := p.hasAllowlist()
	registered := p.registry[toolName]
	collector := p.collector
	p.mu.Unlock()
	if enforce && !registered {
		log.Warn("tool plugin: unregistered tool invoked",
			"execution_id", executionID,
			"step_id", result.StepID,
			"tool", toolName,
		)
		return fmt.Errorf("tool %q: %w", toolName, ErrToolNotRegistered)
	}

	if collector != nil {
		success := result.Status != StepStatusFailed
		output := result.Output
		if !success {
			output = result.Error
		}
		collector.RecordTool(result.StepID, toolName, "", output, result.Duration, success)
		log.Debug("tool plugin: recorded tool call",
			"execution_id", executionID,
			"step_id", result.StepID,
			"tool", toolName,
		)
	}
	return nil
}

var _ WorkflowHook = (*ToolPlugin)(nil)
