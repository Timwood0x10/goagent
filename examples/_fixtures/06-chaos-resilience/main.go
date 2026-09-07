// Chaos resilience — demonstrates real failure handling and self-healing patterns.
//
// Purpose:
//
//	Show how the ARES agent behaves when tools fail in realistic ways: file
//	system errors, tool timeouts, network failures, graceful degradation, MCP
//	disconnection, LLM service failures, and memory corruption. Each chaos
//	scenario is exercised through a dedicated tool, and the agent is asked to
//	handle the resulting error gracefully.
//
// Learning objectives (what this example teaches you):
//   - How to build custom tools with tools.ToolFunc and register them on the
//     Runtime's tool registry.
//   - How different failure modes (timeout, not-found, connection reset,
//     corrupted data) surface through tool errors.
//   - How a resilient system prompt helps the agent explain what happened
//     instead of silently failing.
//   - How to read Result fields (Output, ToolCalls, TokenUsage, Duration) to
//     assess agent behaviour under stress.
//
// Core APIs used (package path → symbol):
//   - github.com/Timwood0x10/ares/sdk.NewRuntime              // create Runtime
//   - github.com/Timwood0x10/ares/sdk.WithOllama              // pick Ollama provider + model
//   - github.com/Timwood0x10/ares/sdk.WithTrace               // enable per-step trace logging
//   - github.com/Timwood0x10/ares/sdk.(*Runtime).ToolRegistry // access tool registry
//   - github.com/Timwood0x10/ares/api/tools.Tool              // tool interface
//   - github.com/Timwood0x10/ares/api/tools.ToolFunc          // struct-based tool implementation
//   - github.com/Timwood0x10/ares/api/tools.(*Registry).Register
//   - github.com/Timwood0x10/ares/sdk.(*Runtime).NewAgent
//   - github.com/Timwood0x10/ares/sdk.WithInstruction         // set system prompt
//   - github.com/Timwood0x10/ares/sdk.(*Agent).Run            // run a single task
//   - github.com/Timwood0x10/ares/sdk.Result                  // Output, ToolCalls, TokenUsage…
//
// Run:
//
//	go run examples/06-chaos-resilience/main.go
//
// Expected output:
//
//	"═══ File system failures ═══"     → agent reads / misses JSON files
//	"═══ Tool timeout ═══"             → agent handles slow_tool
//	"═══ Graceful degradation ═══"     → agent falls back from unreliable to echo
//	"═══ Network failure simulation ═══" → agent handles flaky_network_api timeout
//	"═══ MCP disconnect ═══"           → agent explains disconnected MCP server
//	"═══ LLM failure simulation ═══"   → agent handles LLM 503 error
//	"═══ Memory corruption ═══"        → agent handles corrupted memory key
//	"✅ Chaos resilience demo completed"
//
// Things you can try to modify:
//   - Change the failure rates or timeout durations in the chaos tool definitions.
//   - Add a retry wrapper around agent.Run to see how repeated attempts affect
//     recovery.
//   - Swap sdk.WithOllama("llama3.2") for a different model to compare
//     resilience across providers.
//   - Add new chaos tools (e.g. disk-full, permission-denied) and register them
//     to explore additional failure modes.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Timwood0x10/ares/api/tools"
	"github.com/Timwood0x10/ares/sdk"
)

func main() {
	ctx := context.Background()

	// ── Step 1: Create a Runtime with Ollama and trace enabled ──
	// NewRuntime initialises the top-level container (LLM client, tool registry,
	// memory engine, etc.). WithOllama selects the Ollama provider with model
	// "llama3.2" — no API key required. WithTrace(true) turns on per-step trace
	// logging so you can follow the agent's reasoning steps in the console.
	rt := sdk.NewRuntime(sdk.WithOllama("llama3.2"), sdk.WithTrace(true))
	defer rt.Close()

	// ── Step 2: Inject all chaos tools into the tool registry ──
	// Each chaos tool simulates a different failure mode. We register them all
	// on the Runtime's tool registry so the agent can discover and call them.
	// readFileTool points at the example's data directory for file-system tests.
	dataDir := filepath.Join("examples", "06-chaos-resilience", "data")
	chaosTools := []tools.Tool{
		readFileTool(dataDir),
		slowTool,
		unreliableTool,
		echoTool,
		flakyNetworkTool,
		mcpDisconnectTool,
		llmFailureTool,
		memoryCorruptTool,
	}
	for _, t := range chaosTools {
		_ = rt.ToolRegistry().Register(t) // register tool; ignore duplicate-name errors
	}

	// ── Step 3: Define chaos scenarios as a table of tasks ──
	// Each scenario groups one or more natural-language tasks that exercise a
	// specific failure mode. The agent receives the task string and is expected
	// to call the relevant chaos tool, observe the error, and respond gracefully.
	scenarios := []struct {
		name  string
		tasks []string
	}{
		{
			name: "File system failures",
			tasks: []string{
				"Read data/languages.json and tell me which language has the most repos",
				"Read data/missing.json and explain what happened",
			},
		},
		{
			name: "Tool timeout",
			tasks: []string{
				"Call slow_tool with input 'test' and handle the result",
			},
		},
		{
			name: "Graceful degradation",
			tasks: []string{
				"Try unreliable_tool first, then fall back to echo_tool if it fails",
			},
		},
		{
			name: "Network failure simulation",
			tasks: []string{
				"Call flaky_network_api and handle any errors gracefully",
			},
		},
		{
			name: "MCP disconnect",
			tasks: []string{
				"Call mcp_disconnect_tool and explain what a disconnected MCP server means",
			},
		},
		{
			name: "LLM failure simulation",
			tasks: []string{
				"Call llm_failure_tool and handle the LLM service error gracefully",
			},
		},
		{
			name: "Memory corruption",
			tasks: []string{
				"Call memory_corrupt_tool with key 'user_data' and handle the corrupted data",
			},
		},
	}

	// ── Step 4: Run each scenario and print results ──
	// For every scenario we create a fresh agent with a resilient system prompt,
	// run each task, and print the output plus a summary line with tool-call
	// count, token usage, and latency. An emoji heuristic flags outputs that
	// mention error/fail/not-found/sorry.
	for _, sc := range scenarios {
		fmt.Printf("\n═══ %s ═══\n", sc.name)
		for _, task := range sc.tasks {
			fmt.Printf("  📋 %s\n", task)
			agent := rt.NewAgent("resilient-agent",
				sdk.WithInstruction("You are resilient. Handle tool failures gracefully. Always explain what happened."),
			)
			result, err := agent.Run(ctx, task)
			if err != nil {
				fmt.Printf("  ❌ %v\n", err)
				continue
			}
			fmt.Printf("  🤖 %s\n", result.Output)
			emoji := "✅"
			if strings.Contains(result.Output, "error") || strings.Contains(result.Output, "fail") ||
				strings.Contains(result.Output, "not found") || strings.Contains(result.Output, "sorry") {
				emoji = "⚠️"
			}
			fmt.Printf("  %s tools: %d | tokens: %d | took: %v\n",
				emoji, result.ToolCalls, result.TokenUsage.Total, result.Duration.Round(time.Millisecond))
		}
	}

	fmt.Println("\n✅ Chaos resilience demo completed")
}

// ── Chaos tools ────────────────────────────────────────────────

// readFileTool returns a tool that reads and pretty-prints a JSON file from the
// given data directory. It exercises file-not-found and invalid-JSON error paths.
func readFileTool(dataDir string) tools.Tool {
	return tools.ToolFunc{
		ToolName: "read_file",
		ToolDesc: "Read a JSON data file from the data directory",
		Fn: func(_ context.Context, params map[string]any) (any, error) {
			filename, _ := params["filename"].(string)
			if filename == "" {
				return nil, fmt.Errorf("filename is required")
			}
			filename = filepath.Base(filename) // strip directory components for safety
			fullPath := filepath.Join(dataDir, filename)

			data, err := os.ReadFile(fullPath)
			if err != nil {
				if os.IsNotExist(err) {
					return nil, fmt.Errorf("file %q not found", filename)
				}
				return nil, fmt.Errorf("read error: %w", err)
			}
			if !json.Valid(data) {
				return nil, fmt.Errorf("file %q contains invalid JSON", filename)
			}
			var parsed any
			_ = json.Unmarshal(data, &parsed) // safe: already validated above
			pretty, _ := json.MarshalIndent(parsed, "", "  ")
			return string(pretty), nil
		},
	}
}

// slowTool is a deliberately slow tool that sleeps for 5 seconds before
// returning, exercising the tool-timeout code path.
var slowTool = tools.ToolFunc{
	ToolName: "slow_tool",
	ToolDesc: "A deliberately slow tool that takes 5 seconds",
	Fn: func(ctx context.Context, params map[string]any) (any, error) {
		input, _ := params["input"].(string)
		select {
		case <-time.After(5 * time.Second):
			return fmt.Sprintf("slow result for: %s", input), nil
		case <-ctx.Done():
			return nil, fmt.Errorf("tool timed out")
		}
	},
}

// unreliableTool simulates a service that fails 80% of the time. Used together
// with echoTool to demonstrate graceful degradation and fallback.
var unreliableTool = tools.ToolFunc{
	ToolName: "unreliable_tool",
	ToolDesc: "A tool that fails 80% of the time",
	Fn: func(_ context.Context, params map[string]any) (any, error) {
		input, _ := params["input"].(string)
		_ = input
		// Simulate 80% failure rate.
		return nil, fmt.Errorf("unreliable_tool: service temporarily unavailable (simulated)")
	},
}

// echoTool is a simple fallback tool that echoes its input string.
var echoTool = tools.ToolFunc{
	ToolName: "echo_tool",
	ToolDesc: "Fallback tool that echoes input",
	Fn: func(_ context.Context, params map[string]any) (any, error) {
		input, _ := params["input"].(string)
		return fmt.Sprintf("echo: %s", input), nil
	},
}

// flakyNetworkTool simulates a flaky network API that times out after 3 seconds,
// exercising the network-failure and cancellation code paths.
var flakyNetworkTool = tools.ToolFunc{
	ToolName: "flaky_network_api",
	ToolDesc: "Simulates a flaky network API that sometimes times out",
	Fn: func(ctx context.Context, params map[string]any) (any, error) {
		endpoint, _ := params["endpoint"].(string)
		_ = endpoint
		select {
		case <-time.After(3 * time.Second):
			return nil, fmt.Errorf("flaky_network_api: connection timeout after 3s")
		case <-ctx.Done():
			return nil, fmt.Errorf("flaky_network_api: request cancelled")
		}
	},
}

// ── Additional chaos modes ─────────────────────────────────────

// mcpDisconnectTool simulates an MCP server disconnection, returning a transport-
// closed error that the agent should explain to the user.
var mcpDisconnectTool = tools.ToolFunc{
	ToolName: "mcp_disconnect_tool",
	ToolDesc: "Simulates an MCP server disconnection",
	Fn: func(_ context.Context, params map[string]any) (any, error) {
		server, _ := params["server"].(string)
		_ = server
		return nil, fmt.Errorf("MCP server 'codegraph' disconnected: transport closed (simulated)")
	},
}

// llmFailureTool simulates an LLM service failure (HTTP 503, rate-limit
// exceeded), exercising the LLM-provider-error recovery path.
var llmFailureTool = tools.ToolFunc{
	ToolName: "llm_failure_tool",
	ToolDesc: "Simulates an LLM service failure",
	Fn: func(_ context.Context, params map[string]any) (any, error) {
		service, _ := params["service"].(string)
		_ = service
		return nil, fmt.Errorf("LLM provider returned 503 Service Unavailable: rate limit exceeded (simulated)")
	},
}

// memoryCorruptTool simulates corrupted memory/data retrieval, returning a
// checksum-mismatch error for the given key.
var memoryCorruptTool = tools.ToolFunc{
	ToolName: "memory_corrupt_tool",
	ToolDesc: "Simulates corrupted memory/data retrieval",
	Fn: func(_ context.Context, params map[string]any) (any, error) {
		key, _ := params["key"].(string)
		return nil, fmt.Errorf("memory corruption detected for key %q: checksum mismatch, data cannot be recovered (simulated)", key)
	},
}
