package main

import (
	"context"
	"fmt"

	api_tools "github.com/Timwood0x10/ares/api/tools"
	"github.com/Timwood0x10/ares/internal/runtime/protocol/mcp"
	builtintools "github.com/Timwood0x10/ares/internal/tools/resources/builtin"
	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// setupMCP registers builtin and MCP tools into the internal registry and
// bridges them into the public registry. It reuses the MCP manager created
// by Bootstrap (comp.MCP) instead of creating a second manager, so server
// connections are not duplicated and the single manager's Stop hook (already
// registered at shutdown) covers every connection.
func setupMCP(_ context.Context, mcpMgr *ares_mcp.MCPManager, registry *api_tools.Registry, deps builtintools.GeneralToolsDeps) (*core.Registry, error) {
	internalReg := core.NewRegistry()

	// Register builtin general tools into the internal registry so sub-agents
	// receive them through the ToolBinder (closure of the tools module, P2.1).
	// Real backends (knowledge store adapter, memory manager, LLM client) are
	// injected via deps so the knowledge/memory/planning tools are usable,
	// not just nil-guarded.
	if err := builtintools.RegisterGeneralTools(internalReg, deps); err != nil {
		return internalReg, fmt.Errorf("register general tools: %w", err)
	}

	// Copy tools from the bootstrap-created MCP manager into the internal
	// registry so sub-agents and the dashboard see MCP tools. The manager was
	// already started by Bootstrap; no second manager is created here.
	if mcpMgr != nil {
		for _, tool := range mcpMgr.RegisteredTools() {
			t := tool
			if err := internalReg.Register(t); err != nil {
				fmt.Printf("MCP bridge: failed to register tool %s: %v\n", t.Name(), err)
			}
		}
	}

	// Bridge: register all internal tools (builtin + MCP) into the public
	// api/tools registry so the dashboard sees them regardless of whether MCP
	// servers are configured.
	for _, name := range internalReg.List() {
		tool, ok := internalReg.Get(name)
		if !ok || tool == nil {
			continue
		}
		t := tool
		if err := registry.Register(api_tools.ToolFunc{
			ToolName: t.Name(),
			ToolDesc: t.Description(),
			Fn: func(ctx context.Context, params map[string]any) (any, error) {
				res, err := t.Execute(ctx, params)
				if err != nil {
					return nil, err
				}
				return res.Data, nil
			},
		}); err != nil {
			fmt.Printf("MCP bridge: failed to register tool %s: %v\n", t.Name(), err)
		}
	}

	return internalReg, nil
}
