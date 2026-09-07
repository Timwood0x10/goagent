// Multi-agent — demonstrates multiple specialised agents with the peer
// Runtime (H1: RegisterAgent + Submit, no leader/sub).
//
// ARES is a Peer Agent operating system where all agents are first-class
// cognitive processes with no inherent hierarchy (aresos-plan.md §1.1): a
// flat set of capability agents registered on the Runtime, and tasks are
// dispatched to the agent registered for their capability. The legacy
// Leader/Sub team orchestration (NewTeam/team.Run) has been removed; this
// example shows the peer Runtime as the only multi-agent path.
//
// Purpose:
//
//	Show how to create specialised agents (coordinator, researcher, writer),
//	register them as peer capabilities, and submit a task that the Runtime
//	dispatches to the matching agent.
//
// Learning objectives (what this example teaches you):
//   - How to create multiple agents with distinct system instructions.
//   - How to register them as peer capabilities with RegisterAgent.
//   - How to call Submit() and interpret the Result (Output, Duration).
//   - How YAML-driven config keeps Go code minimal.
//
// Core APIs used (with package paths):
//   - sdk.LoadConfigFile             — github.com/Timwood0x10/ares/sdk
//   - (*cfg.ConfigFile).ToOptions()  — github.com/Timwood0x10/ares/sdk
//   - sdk.NewRuntime                 — github.com/Timwood0x10/ares/sdk
//   - rt.NewAgent                    — github.com/Timwood0x10/ares/sdk
//   - sdk.WithInstruction            — github.com/Timwood0x10/ares/sdk
//   - rt.RegisterAgent               — github.com/Timwood0x10/ares/sdk
//   - rt.Submit                      — github.com/Timwood0x10/ares/sdk
//   - sdk.Task (struct)              — github.com/Timwood0x10/ares/sdk
//
// Run:
//
//	go run examples/04-multi-agent/main.go
//
// Expected output (when an LLM backend is configured):
//
//	📋 Task: Research and write a one-paragraph summary about the Go programming language
//
//	📝 Result:
//	<the synthesised final output from the agent>
//
//	   took: <duration>
//
// If the run fails with an "API key" error, set OPENAI_API_KEY or install
// Ollama. Try adding a fourth agent or changing an agent's instruction to
// alter the behaviour.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Timwood0x10/ares/sdk"
)

func main() {
	ctx := context.Background()

	// ── Step 1: Load ares.yaml and wire everything ──
	// LoadConfigFile reads the YAML config; ToOptions converts it to Runtime
	// options that auto-wire LLM, memory, distillation, AKG, and evolution.
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
	// NewRuntime builds the runtime; LLM and all subsystems are auto-wired.
	rt := sdk.NewRuntime(opts...)
	// defer Close releases connections and background resources.
	defer rt.Close()

	// ── Step 2: Register the peer capabilities (H1) ──
	// RegisterAgent creates a capability agent and registers it on the
	// Runtime; WithInstruction configures its system prompt. There is no
	// leader and no hierarchy: Submit dispatches a task to the agent
	// registered for its capability.
	rt.RegisterAgent("coordinator",
		sdk.WithInstruction(`You are a coordinator. You plan tasks and produce a clear synthesis.
Be concise.`),
	)
	rt.RegisterAgent("researcher",
		sdk.WithInstruction(`You are a researcher. You find facts, analyze data, and provide insights.
Be factual and concise.`),
	)
	rt.RegisterAgent("writer",
		sdk.WithInstruction(`You are a writer. You produce clear, well-structured content.
Be concise and engaging.`),
	)

	// ── Step 3: Submit the task ──
	// Submit is the uniform entry point: the Runtime picks the agent
	// registered for the task's capability and returns the Result.
	task := "Research and write a one-paragraph summary about the Go programming language"
	fmt.Printf("📋 Task: %s\n", task)

	result, err := rt.Submit(ctx, sdk.Task{
		Capability: "coordinator",
		Input:      task,
	})
	if err != nil {
		// Provide a hint if the error is about a missing API key.
		if strings.Contains(err.Error(), "API key") {
			fmt.Fprintf(os.Stderr, "❌ %v\n   → Set OPENAI_API_KEY or install Ollama\n", err)
			return
		}
		fmt.Fprintf(os.Stderr, "❌ submit: %v\n", err)
		return
	}

	// Print the output and duration.
	fmt.Printf("📝 Result:\n%s\n", result.Output)
	fmt.Printf("\n   took: %v\n", result.Duration)
}
