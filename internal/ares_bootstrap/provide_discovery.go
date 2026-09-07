// Package ares_bootstrap wires discovery engine construction.
//
// provide_discovery.go constructs the optional service discovery engine that
// auto-detects MCP servers and agent runtimes. The engine is opt-in via the
// Discovery config section; when disabled, ProvideDiscovery returns nil and
// the discovery packages remain unused.
package ares_bootstrap

import (
	"context"
	"errors"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/discovery"
	"github.com/Timwood0x10/ares/internal/discovery/providers"
)

// DiscoveryComponents holds the wired discovery engine. It is nil when
// discovery is disabled in config.
type DiscoveryComponents struct {
	Engine *discovery.Engine
}

// ErrDiscoveryDisabled is returned by ProvideDiscovery when the discovery
// engine is disabled in configuration. Callers should check for this sentinel
// ErrDiscoveryDisabled with errors.Is and treat it as a non-error no-op.
var ErrDiscoveryDisabled = errors.New("discovery disabled in config")

// ProvideDiscovery constructs the discovery engine with the default provider
// set (ARES, Claude, Cursor, VSCode configs + PATH binary probe), starts
// auto-discovery, and bridges every discovery event onto the shared
// EventStore (previously the engine ran with zero consumers, so
// detected services were written to an in-memory store nobody read). Returns
// ErrDiscoveryDisabled when cfg is nil or discovery is disabled, so callers
// can ignore the component entirely in the default configuration.
//
// Args:
//
//	ctx        - lifecycle context for the auto-discovery loop; cancels on shutdown.
//	cfg        - discovery configuration; nil or Enabled=false yields ErrDiscoveryDisabled.
//	eventStore - shared event bus receiving forwarded discovery events (may be nil,
//	             in which case findings stay local to the engine).
//
// Returns:
//
//	comp   - DiscoveryComponents with a started Engine, or nil when disabled.
//	err    - non-nil only on provider construction failure (currently always
//	         nil because each provider constructor is infallible).
func ProvideDiscovery(ctx context.Context, cfg *ares_config.DiscoveryConfig, eventStore ares_events.EventStore) (*DiscoveryComponents, error) {
	if cfg == nil || !cfg.Enabled {
		return nil, ErrDiscoveryDisabled
	}

	eng := discovery.NewEngine(discovery.NewMemoryStore(), nil)
	// Provider constructors vary in signature: ARES, Cursor, and the binary
	// probe take no args (they derive paths from $HOME or $PATH), while Claude
	// and VSCode take a project directory to scan for project-local config.
	eng.AddProvider(providers.NewARESProvider())
	eng.AddProvider(providers.NewClaudeProvider(cfg.ProjectDir))
	eng.AddProvider(providers.NewCursorProvider())
	eng.AddProvider(providers.NewVSCodeProvider(cfg.ProjectDir))
	eng.AddProvider(providers.NewBinaryProbeProvider())

	if eventStore != nil {
		eng.AddHandler(discovery.EventHandlerFunc(func(evt discovery.Event) {
			forwardDiscoveryEvent(ctx, eventStore, evt)
		}))
	}

	interval := cfg.Interval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	eng.StartAutoDiscovery(ctx, interval)
	return &DiscoveryComponents{Engine: eng}, nil
}

// forwardDiscoveryEvent maps one discovery Engine event onto the shared
// EventStore under the "discovery" stream. Best-effort: Emit already logs
// append failures; a full store must not break the discovery loop.
func forwardDiscoveryEvent(ctx context.Context, store ares_events.EventStore, evt discovery.Event) {
	payload := map[string]any{
		"service_id": evt.ServiceID,
		"source":     evt.Source,
		"message":    evt.Message,
	}
	if evt.Service != nil {
		payload["name"] = evt.Service.Identity.Name
		payload["endpoint"] = evt.Service.Identity.Endpoint
		payload["healthy"] = evt.Service.Healthy
	}
	ares_events.Emit(ctx, store, "discovery", discoveryEventType(evt.Type), "discovery", payload)
}

// discoveryEventType maps internal/discovery event types to their shared
// ares_events counterparts. Unknown types fall back to the cycle-complete
// marker so no finding is silently dropped.
func discoveryEventType(t discovery.EventType) ares_events.EventType {
	switch t {
	case discovery.EventServiceAdded:
		return ares_events.EventDiscoveryServiceAdded
	case discovery.EventServiceRemoved:
		return ares_events.EventDiscoveryServiceRemoved
	case discovery.EventServiceUpdated:
		return ares_events.EventDiscoveryServiceUpdated
	case discovery.EventHealthChanged:
		return ares_events.EventDiscoveryHealthChanged
	default:
		return ares_events.EventDiscoveryCycleCompleted
	}
}
