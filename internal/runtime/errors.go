package runtime

import (
	"errors"
	"fmt"
)

// Sentinel errors for the runtime package.
var (
	ErrPluginPanic       = errors.New("plugin panic")
	ErrPluginTimeout     = errors.New("plugin timeout")
	ErrPluginNotFound    = errors.New("plugin not found")
	ErrDuplicatePlugin   = errors.New("plugin name already registered")
	ErrBusNotStarted     = errors.New("plugin bus not started")
	ErrBusAlreadyStarted = errors.New("plugin bus already started")
	// ErrToolNotRegistered is returned by ToolPlugin.AfterStep when a step
	// invokes a tool that is not in the plugin's allowlist. The allowlist is
	// only enforced once at least one tool has been registered via
	// RegisterTool; an empty registry means "no allowlist configured" and
	// validation is skipped.
	ErrToolNotRegistered = errors.New("tool not registered")
)

// PluginError wraps an error with the plugin name and optional recovered panic value.
type PluginError struct {
	PluginName string
	Err        error
	Recovered  any
}

func (e *PluginError) Error() string {
	if e.Recovered != nil {
		return fmt.Sprintf("plugin %q: %v (panic: %v)", e.PluginName, e.Err, e.Recovered)
	}
	return fmt.Sprintf("plugin %q: %v", e.PluginName, e.Err)
}

func (e *PluginError) Unwrap() error {
	return e.Err
}
