// Quickstart — the simplest end-to-end example with ARES.
//
// Purpose:
//
//	Show the minimal flow: load YAML config, create a Runtime, register one
//	custom tool, create an Agent, and run a single conversational turn.
//
// Learning objectives (what this example teaches you):
//   - How to load ares.yaml and convert it into Runtime options.
//   - How to create a Runtime (which auto-wires LLM, memory, distillation,
//     AKG, and evolution) and close it when done.
//   - How to register a custom tool via the Runtime's ToolRegistry.
//   - How to create an Agent with a system instruction and call Run().
//
// Core APIs used (with package paths):
//   - sdk.LoadConfigFile             — github.com/Timwood0x10/ares/sdk
//   - (*cfg.ConfigFile).ToOptions()  — github.com/Timwood0x10/ares/sdk
//   - sdk.NewRuntime                 — github.com/Timwood0x10/ares/sdk
//   - rt.ToolRegistry().Register     — github.com/Timwood0x10/ares/sdk
//   - rt.NewAgent                    — github.com/Timwood0x10/ares/sdk
//   - sdk.WithInstruction            — github.com/Timwood0x10/ares/sdk
//   - agent.Run                      — github.com/Timwood0x10/ares/sdk
//   - tools.ToolFunc                 — github.com/Timwood0x10/ares/api/tools
//
// Run:
//
//	go run examples/01-quickstart/main.go
//
// Expected output (when an LLM backend is configured):
//
//	✅ <the assistant's text answer, e.g. "result of 15*23 + 100 = 445">
//	   tools: 1 calls | tokens: <n> | took: <duration>
//
// If no API key / Ollama is available the run will fail with an LLM error.
//
// Try editing ares.yaml to toggle memory.enable_distillation,
// knowledge.enabled or evolution.enabled and see the behaviour change.
// You can also change the instruction string or the tool's description.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Timwood0x10/ares/api/tools"
	"github.com/Timwood0x10/ares/sdk"
)

// main is the entry point; it delegates to run() so that error handling
// stays linear and clean.
func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
}

// run contains the whole example so that error returns stay simple.
func run() error {
	// A root context controls the Run lifecycle and supports cancellation.
	ctx := context.Background()

	// ── Step 1: Load ares.yaml and assemble Runtime options ──
	// LoadConfigFile reads and parses the YAML config file, returning *ConfigFile.
	// Passing "ares.yaml" makes the Runtime look for it in the current directory
	// (the $ARES_YAML env var can also specify a path).
	cfg, err := sdk.LoadConfigFile("ares.yaml")
	if err != nil {
		return fmt.Errorf("load ares.yaml: %w", err)
	}
	// ToOptions converts the ConfigFile into a slice of Runtime Option values,
	// covering every subsystem: LLM backend, memory, distillation, AKG, evolution.
	opts, err := cfg.ToOptions()
	if err != nil {
		return fmt.Errorf("config to options: %w", err)
	}
	// NewRuntime builds the runtime from the options; LLM, memory, AKG and
	// evolution are all auto-wired — no manual assembly required.
	rt := sdk.NewRuntime(opts...)
	// defer Close releases the connections and background resources held by Runtime.
	defer rt.Close()

	// ── Step 2: Register a custom tool (optional customisation point) ──
	// Most projects only need to register custom tools in Go; everything else
	// is driven by YAML.
	// ToolRegistry() returns the global tool registry; Register adds a tools.Tool.
	if err := rt.ToolRegistry().Register(calculatorTool); err != nil {
		return fmt.Errorf("register tool: %w", err)
	}

	// ── Step 3: Create an Agent ──
	// NewAgent creates a named Agent on the current Runtime.
	// WithInstruction sets the system prompt (prepended to the conversation).
	agent := rt.NewAgent("assistant",
		sdk.WithInstruction("You are a helpful assistant. Use tools when needed."),
	)

	// ── Step 4: Run one conversational turn ──
	// Run executes a ReAct loop: build message → call LLM → call tool if needed → return.
	// The argument is the user's natural-language input; the return is *Result
	// containing output text, tool-call count, token usage, and duration.
	result, err := agent.Run(ctx, "Calculate 15*23 + 100, what's the result?")
	if err != nil {
		return fmt.Errorf("agent run: %w", err)
	}

	// Print the final answer and statistics: tool calls, total tokens, duration.
	fmt.Printf("✅ %s\n", result.Output)
	fmt.Printf("   tools: %d calls | tokens: %d | took: %v\n",
		result.ToolCalls, result.TokenUsage.Total, result.Duration)
	return nil
}

// ── Custom Tool ──────────────────────────────────────────────
// calculatorTool is a demo "calculator" tool. It implements tools.Tool via
// the tools.ToolFunc convenience struct:
//   - ToolName: the tool name the LLM sees to decide when to call it.
//   - ToolDesc: a description helping the LLM understand the tool's purpose.
//   - Fn:       the actual function, receiving context and params (map[string]any).
//
// For simplicity Fn returns a hard-coded result string and does no real math.
var calculatorTool = tools.ToolFunc{
	ToolName: "calculator",
	ToolDesc: "Evaluate a mathematical expression",
	Fn: func(ctx context.Context, params map[string]any) (any, error) {
		// Extract the "expression" field from the params map (the LLM generates these).
		expr, _ := params["expression"].(string)
		// Return a string as the tool result; the LLM incorporates it into subsequent reasoning.
		return fmt.Sprintf("result of %s = 445", expr), nil
	},
}
