package runtime

import (
	"log/slog"
	"time"
)

const (
	defaultPluginTimeout = 30 * time.Second
)

// PluginBusOption configures a PluginBus.
type PluginBusOption func(*PluginBus)

// WithPluginTimeout sets the per-plugin invocation timeout.
func WithPluginTimeout(d time.Duration) PluginBusOption {
	return func(b *PluginBus) {
		b.pluginTimeout = d
	}
}

// WithLogger sets the logger for the PluginBus.
func WithLogger(l *slog.Logger) PluginBusOption {
	return func(b *PluginBus) {
		b.logger = l
	}
}
