package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	tools "github.com/Timwood0x10/ares/internal/apitools"
	"github.com/Timwood0x10/ares/internal/knowledge/compiler"
	mcp "github.com/Timwood0x10/ares/internal/knowledge/mcp"
	khruntime "github.com/Timwood0x10/ares/internal/knowledge/runtime"
)

// akfToolAdapter wraps an internal/knowledge/mcp.Tool so it satisfies the
// public tools.Tool interface. The AKF tools accept a JSON-encoded string
// input, while tools.Tool passes a map[string]any — this adapter marshals
// the map to JSON before delegating, and wraps the string result in a
// tools.Result.
type akfToolAdapter struct {
	tool mcp.Tool
}

// Name returns the wrapped AKF tool's name.
func (a *akfToolAdapter) Name() string { return a.tool.Name }

// Description returns the wrapped AKF tool's human-readable description.
func (a *akfToolAdapter) Description() string { return a.tool.Description }

// Parameters returns nil; AKF tools accept a free-form JSON string input and
// do not declare a JSON Schema here.
func (a *akfToolAdapter) Parameters() map[string]any { return nil }

// Capabilities returns nil; AKF knowledge tools do not declare planner
// capabilities.
func (a *akfToolAdapter) Capabilities() []string { return nil }

// Execute marshals the params map to a JSON string, delegates to the wrapped
// AKF tool's Execute, and wraps the string result in a tools.Result. A tool
// execution error is reported via Result.Success=false (not a Go error) so the
// agent loop can surface it to the LLM; only marshalling failures return a Go
// error.
//
// Args:
//
//	ctx    - request context, forwarded to the AKF tool.
//	params - tool parameters; marshalled to JSON before delegation.
//
// Returns:
//
//	tools.Result - Success=true with the tool's string output as Data, or
//	               Success=false with the error message as Data on failure.
//	error        - non-nil only when params cannot be marshalled to JSON.
func (a *akfToolAdapter) Execute(ctx context.Context, params map[string]any) (tools.Result, error) {
	input, err := json.Marshal(params)
	if err != nil {
		return tools.Result{}, fmt.Errorf("akf tool %s marshal params: %w", a.tool.Name, err)
	}
	out, err := a.tool.Execute(ctx, string(input))
	if err != nil {
		return tools.Result{Success: false, Data: err.Error()}, nil
	}
	return tools.Result{Success: true, Data: out}, nil
}

// Ensure akfToolAdapter implements tools.Tool at compile time.
var _ tools.Tool = (*akfToolAdapter)(nil)

// registerAKFTools creates the AKF MCP service from the live KnowledgeRuntime
// and registers each knowledge tool (build_graph, compile_context,
// query_knowledge, distill_memory) into the SDK tool registry so the agent
// can invoke them during its ReAct loop.
//
// Args:
//
//	reg - the SDK tool registry; tools are registered by name.
//	rt  - the live KnowledgeRuntime; must be non-nil.
//
// Returns:
//
//	error - wrapped with context if any registration fails, or if rt is nil.
func registerAKFTools(reg *tools.Registry, rt *khruntime.KnowledgeRuntime) error {
	if rt == nil {
		return errors.New("akf tools: knowledge runtime is nil")
	}
	svc := mcp.NewAKFService(rt, compiler.NewDefaultCompiler())
	for _, t := range svc.Tools() {
		if err := reg.Register(&akfToolAdapter{tool: t}); err != nil {
			return fmt.Errorf("akf tools: register %s: %w", t.Name, err)
		}
	}
	return nil
}
