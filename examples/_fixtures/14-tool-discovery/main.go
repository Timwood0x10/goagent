// Example 14 — Tool Discovery: MCP-style proactive discovery + the
// discover_tools meta-tool.
//
// Purpose:
//
//	Build a MultiSource (runtime registry + one custom static tool),
//	exercise discover_tools directly (deterministic, no LLM needed),
//	then run one agent turn with WithToolDiscovery so the LLM can search
//	the tool pool at runtime.
//
// Learning objectives:
//   - Register custom tools on the runtime tool registry.
//   - Build a toolsource.MultiSource from registry and static sources.
//   - Call the discover_tools meta-tool directly without an LLM.
//   - Create an Agent with WithToolDiscovery and WithToolSource.
//
// Core APIs used:
//   - github.com/Timwood0x10/ares/sdk.LoadConfigFile
//   - github.com/Timwood0x10/ares/sdk.NewRuntime
//   - github.com/Timwood0x10/ares/sdk.Runtime.NewAgent
//   - github.com/Timwood0x10/ares/sdk.WithToolDiscovery
//   - github.com/Timwood0x10/ares/sdk.WithToolSource
//   - github.com/Timwood0x10/ares/sdk.WithMaxIterations
//   - github.com/Timwood0x10/ares/internal/tools/toolsource.NewMultiSource
//   - github.com/Timwood0x10/ares/internal/tools/toolsource.NewRegistrySource
//   - github.com/Timwood0x10/ares/internal/tools/toolsource.NewStaticSource
//   - github.com/Timwood0x10/ares/internal/tools/toolsource.NewDiscoverToolsTool
//
// Run:
//
//	go run examples/14-tool-discovery/main.go
//
// Expected output:
//
//	=== discover_tools (direct, no LLM) ===
//	  query="translate" -> [{translate ...}]
//	  query="reverse" -> [{reverse_text ...}]
//
//	=== agent.Run (LLM) ===
//	🤖 <translated text or agent response>
//
// Try modifying:
//   - The registryTools slice to add or rename tools for discovery.
//   - The static source (reverseTool) to add more non-registry tools.
//   - The query strings in the direct discover_tools demo.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Timwood0x10/ares/api/tools"
	rescore "github.com/Timwood0x10/ares/internal/tools/resources/core"
	"github.com/Timwood0x10/ares/internal/tools/toolsource"
	"github.com/Timwood0x10/ares/sdk"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
}

// run builds the tool discovery setup, demonstrates direct discovery,
// then runs one agent turn with discovery enabled.
func run() error {
	// 30-second timeout keeps the demo bounded.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// ── Step 1: Load ares.yaml (or fall back to SDK defaults) ──
	// If the YAML is missing the discovery demo still runs — we just use
	// the SDK's built-in defaults instead of config-driven options.
	var rtOpts []sdk.Option
	if cfg, err := sdk.LoadConfigFile("ares.yaml"); err == nil {
		if rtOpts, err = cfg.ToOptions(); err != nil {
			return fmt.Errorf("config to options: %w", err)
		}
	} else {
		fmt.Println("(ares.yaml not found; using SDK defaults)")
	}
	rt := sdk.NewRuntime(rtOpts...)
	defer rt.Close()

	// ── Step 2: Register custom tools on the runtime registry ──
	// These tools become discoverable via the RegistrySource below.
	for _, t := range registryTools {
		if err := rt.ToolRegistry().Register(t); err != nil {
			return fmt.Errorf("register %s: %w", t.Name(), err)
		}
	}

	// ── Step 3: Build a MultiSource from registry + a static tool ──
	// CoreRegistry returns the low-level registry backing the runtime.
	// NewMultiSource merges a RegistrySource (runtime tools) with a
	// StaticSource (reverseTool, not in the runtime registry).
	coreReg, err := rt.ToolRegistry().CoreRegistry()
	if err != nil {
		return fmt.Errorf("core registry: %w", err)
	}
	src := toolsource.NewMultiSource(
		toolsource.NewRegistrySource(coreReg),
		toolsource.NewStaticSource([]rescore.Tool{reverseTool{}}),
	)

	// ── Step 4: Demonstrate discover_tools directly (no LLM required) ──
	// NewDiscoverToolsTool creates a meta-tool that searches the
	// MultiSource by keyword. Execute it with a "query" parameter to
	// get deterministic results without an LLM round-trip.
	fmt.Println("=== discover_tools (direct, no LLM) ===")
	meta := toolsource.NewDiscoverToolsTool(src)
	for _, q := range []string{"translate", "reverse"} {
		res, _ := meta.Execute(ctx, map[string]any{"query": q}) // best-effort demo
		fmt.Printf("  query=%q -> %s\n", q, res.Data)
	}

	// ── Step 5: Create an agent with tool discovery and run one turn ──
	// WithToolDiscovery enables the discover_tools meta-tool in the
	// agent's tool set. WithToolSource provides the MultiSource the
	// agent searches at runtime. WithMaxIterations caps the loop at 3.
	agent := rt.NewAgent("assistant",
		sdk.WithToolDiscovery(),
		sdk.WithToolSource(src),
		sdk.WithMaxIterations(3),
		sdk.WithInstruction("You are a helpful assistant. Use discover_tools to find tools."),
	)
	fmt.Println("\n=== agent.Run (LLM) ===")
	result, err := agent.Run(ctx, "Translate 'hello' to French.")
	if err != nil {
		// LLM errors are non-fatal — the direct discovery demo above
		// already proved the mechanism works.
		fmt.Fprintf(os.Stderr, "⚠️ agent run: %v (discovery demo above succeeded)\n", err)
		return nil
	}
	fmt.Printf("🤖 %s\n", result.Output)
	return nil
}

// registryTools are registered in the runtime registry so discover_tools
// can search them by name/description.
var registryTools = []tools.Tool{
	tools.ToolFunc{ToolName: "translate", ToolDesc: "Translate text between languages",
		Fn: func(_ context.Context, p map[string]any) (any, error) {
			return fmt.Sprintf("[%s] %s", p["lang"], p["text"]), nil
		}},
	tools.ToolFunc{ToolName: "calculator", ToolDesc: "Evaluate a mathematical expression",
		Fn: func(_ context.Context, p map[string]any) (any, error) {
			return fmt.Sprintf("= %s", p["expression"]), nil
		}},
}

// reverseTool is a custom static tool (NOT in the runtime registry) showing
// MultiSource merging a source outside the registry. Execute reverses the
// input text rune-wise (real deterministic transform).
type reverseTool struct{}

func (reverseTool) Name() string                       { return "reverse_text" }
func (reverseTool) Description() string                { return "Reverse the characters of a string" }
func (reverseTool) Category() rescore.ToolCategory     { return rescore.CategoryCore }
func (reverseTool) Capabilities() []rescore.Capability { return nil }
func (reverseTool) Parameters() *rescore.ParameterSchema {
	return &rescore.ParameterSchema{
		Type: "object",
		Properties: map[string]*rescore.Parameter{
			"text": {Type: "string", Description: "text to reverse"},
		},
		Required: []string{"text"},
	}
}

func (reverseTool) Execute(_ context.Context, p map[string]interface{}) (rescore.Result, error) {
	text, _ := p["text"].(string)
	runes := []rune(text)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return rescore.NewResult(true, string(runes)), nil
}
