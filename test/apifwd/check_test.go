// Package apifwd verifies the M5 forwarding layers keep the full public API
// surface: every symbol and method that existed in api/tools, api/mcp, and
// api/service/llm before internalization must remain usable through the
// forwarding layer. The checks are compile-time typed assignments; the test
// body is a no-op.
package apifwd

import (
	"context"
	"testing"

	"github.com/Timwood0x10/ares/api/mcp"
	llmsvc "github.com/Timwood0x10/ares/api/service/llm"
	tools "github.com/Timwood0x10/ares/api/tools"
	llmcore "github.com/Timwood0x10/ares/internal/llmcore"
	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// api/tools function symbols.
var (
	_ func() *tools.Registry                                       = tools.NewRegistry
	_ func() *tools.Registry                                       = tools.NewEmptyRegistry
	_ func(*tools.Registry) (*tools.Planner, error)                = tools.NewPlanner
	_ func(*tools.Registry, *tools.Planner) (*tools.Bridge, error) = tools.NewBridge
	_ func(*tools.Registry, ...tools.BuiltinToolsOption) error     = tools.RegisterBuiltinTools
	_ func(string) tools.BuiltinToolsOption                        = tools.WithFileSandboxDir
	_ func(string) (string, error)                                 = tools.FilePath
)

// api/tools type symbols.
var (
	_ tools.Result
	_ tools.Tool
	_ tools.ToolFunc
	_ tools.Registry
	_ tools.ToolInfo
	_ tools.RegistryPlannerProvider
	_ tools.Planner
	_ tools.ExecutionPlan
	_ tools.Bridge
	_ tools.BuiltinToolsOption
)

// Registry method set (transmitted via type alias).
var (
	_ func(tools.Tool) error                                              = (*tools.Registry)(nil).Register
	_ func(string) error                                                  = (*tools.Registry)(nil).Unregister
	_ func(string) (tools.Tool, bool)                                     = (*tools.Registry)(nil).Get
	_ func(context.Context, string, map[string]any) (tools.Result, error) = (*tools.Registry)(nil).Execute
	_ func() []string                                                     = (*tools.Registry)(nil).List
	_ func() []tools.ToolInfo                                             = (*tools.Registry)(nil).ListTools
	_ func() []string                                                     = (*tools.Registry)(nil).ListToolNames
	_ func(string) ([]string, error)                                      = (*tools.Registry)(nil).GetToolCapabilities
	_ func() *tools.RegistryPlannerProvider                               = (*tools.Registry)(nil).PlannerProvider
	_ func() (*core.Registry, error)                                      = (*tools.Registry)(nil).CoreRegistry
)

// ToolFunc method set.
var (
	_ func() string                                               = tools.ToolFunc{}.Name
	_ func() string                                               = tools.ToolFunc{}.Description
	_ func() map[string]any                                       = tools.ToolFunc{}.Parameters
	_ func() []string                                             = tools.ToolFunc{}.Capabilities
	_ func(context.Context, map[string]any) (tools.Result, error) = tools.ToolFunc{}.Execute
)

// RegistryPlannerProvider method set.
var (
	_ func() []string                = (*tools.RegistryPlannerProvider)(nil).ListTools
	_ func(string) ([]string, error) = (*tools.RegistryPlannerProvider)(nil).GetToolCapabilities
)

// api/mcp function and type symbols.
var (
	_ func(context.Context, mcp.ServerConfig) (*mcp.Client, error)         = mcp.ConnectFromConfig
	_ func(context.Context, string, string) (*mcp.Client, error)           = mcp.ConnectSSE
	_ func(context.Context, string, string, []string) (*mcp.Client, error) = mcp.ConnectStdio
	_ func(string) []mcp.ServerConfig                                      = mcp.DiscoverServers

	_ mcp.Client
	_ mcp.ToolInfo
	_ mcp.CallResult
	_ mcp.ContentBlock
	_ mcp.ServerConfig
)

// Client method set.
var (
	_ func(context.Context) ([]mcp.ToolInfo, error)                          = (*mcp.Client)(nil).ListTools
	_ func(context.Context, string, map[string]any) (*mcp.CallResult, error) = (*mcp.Client)(nil).CallTool
	_ func() string                                                          = (*mcp.Client)(nil).Name
	_ func() error                                                           = (*mcp.Client)(nil).Close
)

// api/service/llm symbols and Service method set.
var (
	_ func(*llmsvc.Config) (*llmsvc.Service, error) = llmsvc.NewService

	_ llmsvc.Config
	_ llmsvc.Service

	_ func(context.Context, *llmcore.GenerateRequest) (*llmcore.GenerateResponse, error)   = (*llmsvc.Service)(nil).Generate
	_ func(context.Context, string) (string, error)                                        = (*llmsvc.Service)(nil).GenerateSimple
	_ func(context.Context, *llmcore.EmbeddingRequest) (*llmcore.EmbeddingResponse, error) = (*llmsvc.Service)(nil).GenerateEmbedding
	_ func() *llmcore.LLMConfig                                                            = (*llmsvc.Service)(nil).GetConfig
	_ func() bool                                                                          = (*llmsvc.Service)(nil).IsEnabled
	_ func() llmcore.LLMProvider                                                           = (*llmsvc.Service)(nil).GetProvider
	_ func() string                                                                        = (*llmsvc.Service)(nil).GetModel
	_ func()                                                                               = (*llmsvc.Service)(nil).Close
)

// TestForwardingSurface is a compile-time check; nothing to run.
func TestForwardingSurface(t *testing.T) {
	t.Log("forwarding surface verified at compile time")
}
