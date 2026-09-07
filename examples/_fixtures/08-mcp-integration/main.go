// MCP integration — demonstrates connecting to an MCP server and using its tools.
//
// Purpose:
//
//	Build the embedded MCP null server, then use WithConfigFromEnv() for the
//	runtime and WithMCP() for the MCP connection. This shows how to compose
//	YAML-driven defaults with programmatic overrides so that external tool
//	servers become first-class citizens of the agent's tool set.
//
// Learning objectives (what this example teaches you):
//   - How to build an external MCP server binary with `go build` and connect to
//     it via sdk.WithMCP(sdk.MCPConn{…}).
//   - How to compose YAML config (sdk.LoadConfigFile + cfg.ToOptions) with
//     programmatic MCP options appended at call time.
//   - How an agent discovers and calls MCP-provided tools (e.g. echo) at run
//     time, just like natively registered tools.
//   - How to read a system-runtime Snapshot for observability — component
//     names, lifecycle states, and a readiness summary.
//
// Core APIs used (package path → symbol):
//   - github.com/Timwood0x10/ares/sdk.LoadConfigFile    // read & validate ares.yaml
//   - github.com/Timwood0x10/ares/sdk.(*ConfigFile).ToOptions
//   - github.com/Timwood0x10/ares/sdk.WithMCP           // connect to an MCP server
//   - github.com/Timwood0x10/ares/sdk.MCPConn           // MCP connection config struct
//   - github.com/Timwood0x10/ares/sdk.NewRuntime         // create Runtime from options
//   - github.com/Timwood0x10/ares/sdk.(*Runtime).NewAgent
//   - github.com/Timwood0x10/ares/sdk.WithInstruction   // set the agent's system prompt
//   - github.com/Timwood0x10/ares/sdk.(*Agent).Run      // run a single task
//   - github.com/Timwood0x10/ares/sdk.(*Runtime).Snapshot // get system-runtime snapshot
//
// Run:
//
//	go run examples/08-mcp-integration/main.go
//
// Expected output:
//
//	"---" + "📋 Use the echo tool to echo 'Hello from MCP!'"
//	"🤖 <agent output using MCP echo tool>"
//	"   tools: N | tokens: N | took: …"
//	"---" + "📋 What tools do you have available?"
//	"🤖 <agent lists tools>"
//	"   tools: N | tokens: N | took: …"
//	"✅ MCP integration demo completed"
//	"System Runtime snapshot: { … }"  → JSON snapshot of component states
//
// Things you can try to modify:
//   - Replace `ares mcp-null serve` with a real MCP server (e.g. a filesystem
//     or database tool server) to see how the agent interacts with live
//     external tools.
//   - Add a second sdk.WithMCP(…) call to connect to multiple MCP servers
//     simultaneously.
//   - Change the agent's system prompt to steer which MCP tool it prefers.
//   - Inspect rt.Snapshot().JSON() output to verify all components reached the
//     "Ready" lifecycle state before task execution.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Timwood0x10/ares/sdk"
)

func main() {
	ctx := context.Background()

	// ── Step 1: Build the MCP null-server binary ──
	// Use exec.Command("go", "build", …) to compile the MCP null server into a
	// temporary binary. It is served by the unified `ares` CLI (`ares mcp-null
	// serve`, cmd/ares/mcp_null.go) — the standalone cmd/mcp-null/ entry was
	// removed in the cmd consolidation. The built binary is cleaned up via
	// defer os.Remove.
	mcpBin := filepath.Join(os.TempDir(), "ares")
	build := exec.Command("go", "build", "-o", mcpBin, "./cmd/ares")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "❌ build MCP server: %v\n", err)
		return
	}
	defer func() { _ = os.Remove(mcpBin) }() // best-effort cleanup of temp binary

	// ── Step 2: Load ares.yaml and compose with an MCP connection ──
	// sdk.LoadConfigFile reads and validates the YAML configuration; cfg.ToOptions
	// converts it into the Option list that NewRuntime accepts. We then append a
	// sdk.WithMCP(…) option to connect the Runtime to the MCP null server built
	// in Step 1. This demonstrates composing YAML-driven defaults with
	// programmatic overrides at call time.
	cfg, err := sdk.LoadConfigFile("ares.yaml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ load config: %v\n", err)
		return
	}
	opts, err := cfg.ToOptions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ config: %v\n", err)
		return
	}
	opts = append(opts, sdk.WithMCP(sdk.MCPConn{
		Name:    "null-server", // human-readable label for this MCP server
		Command: mcpBin,        // path to the MCP server binary
		Args:    []string{"mcp-null", "serve"},
	}))
	rt := sdk.NewRuntime(opts...) // create Runtime with YAML options + MCP connection
	defer rt.Close()

	// ── Step 3: Create an Agent with a system instruction ──
	// rt.NewAgent creates a new Agent bound to this Runtime. WithInstruction sets
	// the system prompt that tells the agent to use the MCP echo tool when asked
	// to echo something. The MCP tools are auto-discovered by the agent through
	// the Runtime's tool registry.
	agent := rt.NewAgent("assistant",
		sdk.WithInstruction(`You are a helpful assistant with access to MCP tools.
Use the echo tool when asked to echo something.`),
	)

	// ── Step 4: Run each task and print results ──
	// For every task we call agent.Run, which streams the task through the LLM,
	// invokes any necessary tools (including MCP-provided ones), and returns a
	// *sdk.Result. API-key / refusal errors are fatal; other errors are printed
	// and the loop continues to the next task.
	for _, task := range []string{
		"Use the echo tool to echo 'Hello from MCP!'",
		"What tools do you have available?",
	} {
		fmt.Printf("\n---\n📋 %s\n", task)
		result, err := agent.Run(ctx, task)
		if err != nil {
			if strings.Contains(err.Error(), "API key") || strings.Contains(err.Error(), "refused") {
				fmt.Fprintf(os.Stderr, "❌ %v\n", err)
				return
			}
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			continue
		}
		fmt.Printf("🤖 %s\n", result.Output)
		fmt.Printf("   tools: %d | tokens: %d | took: %v\n",
			result.ToolCalls, result.TokenUsage.Total, result.Duration)
	}

	fmt.Println("\n✅ MCP integration demo completed")

	// ── Step 5: Show system runtime snapshot (Stage 1 observability) ──
	// The Snapshot() method returns the component status from the Bootstrap core:
	// names, modes, lifecycle states (Constructed / Bound / Started / Ready /
	// Stopped) and a readiness summary. Available on any SDK Runtime backed by
	// the Bootstrap core. Calling .JSON() on the snapshot produces indented JSON
	// suitable for diagnostic output.
	if snapJSON, snapErr := rt.Snapshot().JSON(); snapErr == nil {
		fmt.Printf("System Runtime snapshot: %s\n", string(snapJSON))
	} else {
		fmt.Printf("System Runtime snapshot unavailable: %v\n", snapErr)
	}
}
