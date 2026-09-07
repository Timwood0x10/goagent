package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// FaultType classifies a fault to inject.
type FaultType string

const (
	FaultPluginPanic   FaultType = "plugin_panic"
	FaultPluginTimeout FaultType = "plugin_timeout"
	FaultPluginError   FaultType = "plugin_error"
	FaultBusStop       FaultType = "bus_stop"
)

// FaultEvent is emitted to trigger a fault injection.
type FaultEvent struct {
	Type       FaultType  `json:"type"`
	TargetCap  Capability `json:"target_cap,omitempty"`  // target plugin capability
	TargetName string     `json:"target_name,omitempty"` // or specific plugin name
}

// FaultError indicates a plugin fault was triggered for testing.
var ErrFaultInjected = errors.New("fault injected by arena")

// ArenaPlugin injects faults into the runtime plugin system for robustness
// validation. It subscribes to fault events on the EventBus and triggers
// controlled failures in target plugins.
type ArenaPlugin struct {
	mu      sync.Mutex
	name    string
	bus     EventBus
	faults  map[string]FaultType // target plugin name → fault type
	pending map[string]bool      // target plugin names with pending fault
}

// NewArenaPlugin creates an ArenaPlugin for fault injection testing.
func NewArenaPlugin(name string) *ArenaPlugin {
	if name == "" {
		name = "arena"
	}
	return &ArenaPlugin{
		name:    name,
		faults:  make(map[string]FaultType),
		pending: make(map[string]bool),
	}
}

// Name returns the plugin name.
func (a *ArenaPlugin) Name() string { return a.name }

// Capabilities returns the capabilities.
func (a *ArenaPlugin) Capabilities() []Capability { return nil }

// Start saves the EventBus reference and subscribes to fault events.
func (a *ArenaPlugin) Start(ctx context.Context, bus EventBus) error {
	a.mu.Lock()
	a.bus = bus
	a.mu.Unlock()
	return nil
}

// Stop shuts down the plugin.
func (a *ArenaPlugin) Stop(_ context.Context) error {
	a.mu.Lock()
	a.faults = make(map[string]FaultType)
	a.pending = make(map[string]bool)
	a.mu.Unlock()
	return nil
}

// BeforeStep is a WorkflowHook that injects faults before the step executes.
// If a fault is pending for the plugin being invoked, it triggers the fault.
func (a *ArenaPlugin) BeforeStep(ctx context.Context, _ string, _ *Step) error {
	return a.checkFault(ctx)
}

// AfterStep is a WorkflowHook that checks for pending faults.
func (a *ArenaPlugin) AfterStep(_ context.Context, _ string, _ *StepResult) error {
	return nil
}

// ScheduleFault schedules a fault for the given plugin name.
// On the next BeforeStep call, the fault is triggered.
func (a *ArenaPlugin) ScheduleFault(pluginName string, ft FaultType) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.faults[pluginName] = ft
	log.Info("arena: scheduled fault",
		"plugin", pluginName,
		"fault_type", ft,
	)
}

// CancelFault removes a pending fault for a plugin.
func (a *ArenaPlugin) CancelFault(pluginName string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.faults, pluginName)
	delete(a.pending, pluginName)
}

func (a *ArenaPlugin) checkFault(ctx context.Context) error {
	a.mu.Lock()
	// snapshot pending state under lock
	type faultEntry struct {
		name string
		ft   FaultType
	}
	var toTrigger []faultEntry
	for name, ft := range a.faults {
		if a.pending[name] {
			continue
		}
		a.pending[name] = true
		toTrigger = append(toTrigger, faultEntry{name, ft})
	}
	a.mu.Unlock()

	for _, f := range toTrigger {
		switch f.ft {
		case FaultPluginPanic:
			panic(fmt.Sprintf("arena: injected panic for plugin %q", f.name))
		case FaultPluginTimeout:
			// Block until context is cancelled — the bus's invokeWithTimeout
			// should interrupt this via context cancellation.
			<-ctx.Done()
			return ctx.Err()
		case FaultPluginError:
			return fmt.Errorf("%w: %s", ErrFaultInjected, f.name)
		case FaultBusStop:
			// Surface the bus-stop injection instead of silently swallowing it
			// (previously scheduled but never acted on). The EventBus interface
			// has no Stop method, so the injection is reported as an error.
			return fmt.Errorf("%w: bus stop injected for %q", ErrFaultInjected, f.name)
		}
	}
	return nil
}
