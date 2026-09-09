// Package tools is the DEPRECATED public alias of internal/apitools (M5).
// New code MUST import internal/apitools; this package exists only for
// external consumers and is scheduled for removal.
package tools

import (
	"github.com/Timwood0x10/ares/internal/apitools"
	"github.com/Timwood0x10/ares/internal/tools/planner"
)

// Result represents the outcome of a tool execution.
type Result = apitools.Result

// Tool is the interface that all tools must implement.
type Tool = apitools.Tool

// ToolFunc is a convenience type for creating tools from functions.
type ToolFunc = apitools.ToolFunc

// Registry manages tool registration and execution.
type Registry = apitools.Registry

// ToolInfo is a summary of a tool.
type ToolInfo = apitools.ToolInfo

// RegistryPlannerProvider adapts a Registry for use with the capability planner.
// It satisfies planner.ToolProvider via structural typing.
type RegistryPlannerProvider = apitools.RegistryPlannerProvider

// Planner is a re-export of planner.Planner for public use.
// See https://pkg.go.dev/github.com/Timwood0x10/ares/internal/tools/planner
type Planner = planner.Planner

// ExecutionPlan is a re-export of planner.ExecutionPlan for public use.
type ExecutionPlan = planner.ExecutionPlan

// Bridge is a re-export of planner.ToolExecutionBridge for public use.
type Bridge = planner.ToolExecutionBridge

// BuiltinToolsOption configures the built-in tools at registration time.
type BuiltinToolsOption = apitools.BuiltinToolsOption

// NewRegistry creates a new Registry pre-populated with all built-in tools.
func NewRegistry() *Registry { return apitools.NewRegistry() }

// NewEmptyRegistry creates a new empty Registry with no built-in tools.
func NewEmptyRegistry() *Registry { return apitools.NewEmptyRegistry() }

// NewPlanner creates a capability planner from a tool Registry.
func NewPlanner(r *Registry) (*Planner, error) { return apitools.NewPlanner(r) }

// NewBridge creates a ToolExecutionBridge with planner fallback.
func NewBridge(r *Registry, p *Planner) (*Bridge, error) { return apitools.NewBridge(r, p) }

// RegisterBuiltinTools registers all built-in tools into the given registry.
func RegisterBuiltinTools(r *Registry, opts ...BuiltinToolsOption) error {
	return apitools.RegisterBuiltinTools(r, opts...)
}

// WithFileSandboxDir restricts the file tool to operations under the given
// directory.
func WithFileSandboxDir(dir string) BuiltinToolsOption {
	return apitools.WithFileSandboxDir(dir)
}

// FilePath returns the absolute path of a file.
func FilePath(path string) (string, error) { return apitools.FilePath(path) }

// WithAllowedDir restricts file operations to paths under the given directory.
//
// Deprecated: forwarded for source compatibility only; the option type is
// unexported, so the returned value cannot be used outside internal/apitools.
var WithAllowedDir = apitools.WithAllowedDir
