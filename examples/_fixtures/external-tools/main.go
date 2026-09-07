// external-tools demonstrates how external projects use the ares public API.
//
// Purpose:
//
//	This example shows the tool ecosystem from an integrator's point of view:
//	built-in tools, custom tool registration, MCP server connection with
//	auto-registration of MCP tools, and tool discovery through the
//	discover_tools meta-tool — the same path an LLM agent uses at runtime.
//
// Learning objectives:
//   - How to register and execute tools via api/tools.Registry (built-in +
//     custom ToolFunc).
//   - How to connect to a discovered MCP server (api/mcp.ConnectStdio) and
//     wrap its tools into registry entries.
//   - How the discover_tools meta-tool queries a RegistrySource.
//
// Core APIs (with package paths):
//   - tools.NewRegistry / RegisterBuiltinTools / Register / Execute / List /
//     CoreRegistry (api/tools)
//   - mcp.ConnectStdio / (*Client).ListTools / (*Client).CallTool (api/mcp)
//   - discovery.NewEngine / DiscoverNow / List (api/discovery)
//   - toolsource.NewRegistrySource / NewDiscoverToolsTool
//     (internal/tools/toolsource)
//
// Run:
//
//	go run ./examples/external-tools
//
// Expected output:
//
//	=== Built-in Tools ===
//	  calculator ... (list of built-ins)
//	=== Tool Calls ===
//	  calculator(2+3*4): ...  sentiment(good): ...  word_count: ...
//	=== MCP Auto-Discovery ===  →  === All N Tools ===  →  discover_tools
package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/Timwood0x10/ares/api/discovery"
	"github.com/Timwood0x10/ares/api/mcp"
	"github.com/Timwood0x10/ares/api/tools"
	"github.com/Timwood0x10/ares/internal/tools/toolsource"
)

func main() {
	ctx := context.Background()

	// ── Step 1: Register built-in tools ──
	// NewRegistry starts empty; RegisterBuiltinTools loads the standard set
	// (calculator, web_search, regex, ...).
	registry := tools.NewRegistry()
	if err := tools.RegisterBuiltinTools(registry); err != nil {
		fmt.Printf("register builtin tools: %v\n", err)
		return
	}

	fmt.Println("=== Built-in Tools ===")
	for _, t := range registry.ListTools() {
		fmt.Printf("  %-20s %s\n", t.Name, t.Description)
	}

	// ── Step 2: Register custom tools ──
	// A ToolFunc is the simplest custom tool: name + description + Fn that
	// takes params and returns a result. Two custom tools are added to show
	// extension beyond the built-ins.
	if err := registry.Register(tools.ToolFunc{
		ToolName: "sentiment",
		ToolDesc: "Analyze text sentiment",
		Fn: func(_ context.Context, params map[string]any) (any, error) {
			text, _ := params["text"].(string)
			if strings.Contains(strings.ToLower(text), "good") {
				return map[string]any{"sentiment": "positive"}, nil
			}
			return map[string]any{"sentiment": "neutral"}, nil
		},
	}); err != nil {
		fmt.Printf("register sentiment: %v\n", err)
		return
	}

	if err := registry.Register(tools.ToolFunc{
		ToolName: "word_count",
		ToolDesc: "Count words in text",
		Fn: func(_ context.Context, params map[string]any) (any, error) {
			text, _ := params["text"].(string)
			return map[string]any{"count": len(strings.Fields(text))}, nil
		},
	}); err != nil {
		fmt.Printf("register word_count: %v\n", err)
		return
	}

	// ── Step 3: Execute tools by name ──
	// Registry.Execute resolves the name and calls the tool's Fn with params;
	// the returned Result.Data carries the tool output.
	fmt.Println("\n=== Tool Calls ===")

	r1, _ := registry.Execute(ctx, "calculator", map[string]any{"expression": "2 + 3 * 4"})
	fmt.Printf("  calculator(2+3*4): %v\n", r1.Data)

	r2, _ := registry.Execute(ctx, "sentiment", map[string]any{"text": "This is good!"})
	fmt.Printf("  sentiment(good):   %v\n", r2.Data)

	r3, _ := registry.Execute(ctx, "word_count", map[string]any{"text": "hello world foo bar"})
	fmt.Printf("  word_count:        %v\n", r3.Data)

	// ── Step 4: MCP auto-discovery and tool bridging ──
	// The discovery engine scans for MCP servers; for each discovered
	// service we connect via stdio and wrap every MCP tool into a registry
	// entry prefixed "mcp.<server>.<tool>" so agents can call it like any
	// other tool.
	fmt.Println("\n=== MCP Auto-Discovery ===")
	engine := discovery.NewEngine(discovery.EngineConfig{})
	_ = engine.DiscoverNow(ctx)
	services, _ := engine.List(ctx)
	if len(services) == 0 {
		fmt.Println("  No MCP servers found")
	} else {
		fmt.Printf("  Found %d MCP server(s):\n", len(services))
		for _, svc := range services {
			fmt.Printf("    %s (confidence=%d%%)\n", svc.Identity.Name, bestConf(svc))
			// Connect via endpoint
			if len(svc.Records) > 0 {
				client, err := mcp.ConnectStdio(ctx, svc.Identity.Name, svc.Records[0].Endpoint, svc.Records[0].Args)
				if err != nil {
					fmt.Printf("      (connect failed: %v)\n", err)
					continue
				}
				defer func() { _ = client.Close() }()

				// List and register MCP tools into the registry.
				mcpTools, err := client.ListTools(ctx)
				if err != nil {
					fmt.Printf("      (list tools failed: %v)\n", err)
					continue
				}
				for _, mt := range mcpTools {
					toolName := fmt.Sprintf("mcp.%s.%s", client.Name(), mt.Name)
					mcpClient := client
					toolDef := mt
					_ = registry.Register(tools.ToolFunc{
						ToolName: toolName,
						ToolDesc: toolDef.Description,
						Fn: func(ctx context.Context, params map[string]any) (any, error) {
							result, err := mcpClient.CallTool(ctx, toolDef.Name, params)
							if err != nil {
								return nil, err
							}
							return map[string]any{"content": result.Content, "is_error": result.IsError}, nil
						},
					})
				}
				fmt.Printf("      ✓ Registered %d tools\n", len(mcpTools))
			}
		}
	}

	// ── Step 5: List the full registry ──
	// After built-ins + custom + MCP bridging, List shows every registered
	// tool name.
	fmt.Printf("\n=== All %d Tools ===\n", len(registry.List()))
	for _, name := range registry.List() {
		fmt.Printf("  %s\n", name)
	}

	// ── Step 6: Tool discovery via the discover_tools meta-tool ──
	// Build a RegistrySource over the public Registry's core bridge, then
	// drive the discover_tools meta-tool directly: the same path the LLM uses
	// at runtime when an Agent is built with sdk.WithToolDiscovery().
	fmt.Println("\n=== Tool Discovery (discover_tools) ===")
	coreReg, err := registry.CoreRegistry()
	if err != nil {
		fmt.Printf("core registry: %v\n", err)
		return
	}
	src := toolsource.NewRegistrySource(coreReg)
	meta := toolsource.NewDiscoverToolsTool(src)
	for _, q := range []string{"sentiment", "calculator"} {
		res, err := meta.Execute(ctx, map[string]any{"query": q})
		if err != nil {
			fmt.Printf("  discover %q: %v\n", q, err)
			continue
		}
		fmt.Printf("  query=%q -> %s\n", q, res.Data)
	}
}

// bestConf returns the highest confidence percentage across a service's
// discovery records, for display.
func bestConf(svc *discovery.DiscoveredService) int {
	best := 0
	for _, r := range svc.Records {
		if int(r.Confidence) > best {
			best = int(r.Confidence)
		}
	}
	return best
}
