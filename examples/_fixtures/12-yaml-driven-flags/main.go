// Example 12 — YAML-driven flags: the "one yaml + one go file starts an
// agent" philosophy.
//
// Purpose:
//
//	Demonstrate that every internal component (LLM, memory, distillation,
//	database, embedding, knowledge) is configurable via ares.yaml, and
//	fields left at zero fall back to the component default.
//
// Learning objectives:
//   - Load a YAML config and convert it to SDK options.
//   - Create a Runtime and an Agent from those options.
//   - Run a single agent turn and inspect token usage and latency.
//
// Core APIs used:
//   - github.com/Timwood0x10/ares/sdk.LoadConfigFile
//   - github.com/Timwood0x10/ares/sdk.Config.ToOptions
//   - github.com/Timwood0x10/ares/sdk.NewRuntime
//   - github.com/Timwood0x10/ares/sdk.Runtime.NewAgent
//   - github.com/Timwood0x10/ares/sdk.Runtime.Close
//   - github.com/Timwood0x10/ares/sdk.WithInstruction
//   - github.com/Timwood0x10/ares/sdk.Agent.Run
//
// Run:
//
//	go run examples/12-yaml-driven-flags/main.go
//
// Expected output:
//
//	✅ <short answer about memory distillation>
//	   tokens: <N> | took: <duration>
//
// Try editing ares.yaml to toggle memory.enable_distillation or
// distillation_threshold and observe the behaviour change.
//
// To use a different config file:
//
//	ARES_YAML=./my-config.yaml go run examples/12-yaml-driven-flags/main.go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/Timwood0x10/ares/sdk"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
}

// run loads the YAML config, builds the runtime, creates an agent,
// and runs one query.
func run() error {
	ctx := context.Background()

	// ── Step 1: Load ares.yaml and convert to SDK options ──
	// LoadConfigFile reads the YAML file; ToOptions turns each populated
	// field into a functional SDK option. Unset fields keep defaults.
	cfg, err := sdk.LoadConfigFile("ares.yaml")
	if err != nil {
		return fmt.Errorf("load ares.yaml: %w", err)
	}
	opts, err := cfg.ToOptions()
	if err != nil {
		return fmt.Errorf("config to options: %w", err)
	}

	// ── Step 2: Create the runtime from options ──
	// NewRuntime wires all subsystems (LLM, memory, distillation, AKG)
	// from the options slice. Close releases resources at exit.
	rt := sdk.NewRuntime(opts...)
	defer rt.Close()

	// ── Step 3: Create an agent with a custom instruction ──
	// WithInstruction sets the system prompt that guides the agent's
	// response style and tool selection behaviour.
	agent := rt.NewAgent("assistant",
		sdk.WithInstruction("You are a helpful assistant. Answer briefly."),
	)

	// ── Step 4: Run one agent turn ──
	// Agent.Run executes the full loop: LLM call, optional tool dispatch,
	// memory write-back. The returned RunResult carries output, token
	// usage, tool-call count, and duration.
	result, err := agent.Run(ctx, "In one short sentence, what is memory distillation?")
	if err != nil {
		return fmt.Errorf("agent run: %w", err)
	}

	// ── Step 5: Print the result and usage stats ──
	fmt.Printf("✅ %s\n", result.Output)
	fmt.Printf("   tokens: %d | took: %v\n",
		result.TokenUsage.Total, result.Duration)
	return nil
}
