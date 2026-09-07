// Peer-spawn demo — prove a REAL LLM autonomously decomposes a task.
//
// This is the "LLM decides to split" showcase (W2, aresos-plan.md §6): the
// coordinator agent receives a task that is complex enough to benefit from
// decomposition, and decides BY ITSELF to call the spawn_agent / create_task
// syscalls (they are injected into every SDK agent's LLM tool list by the
// runtime — see sdk/syscall.go wireSyscalls + resolveTools).
//
// The Kernel enforces what the LLM decides: spawn_agent validates the
// capability/quota, creates the peer in the Agent Fabric, and registers it as
// a schedulable executor; create_task writes a real Task Fabric task that the
// shared scheduler drives to completion. Nothing here is a canned stub — the
// transcript you see is the LLM's actual tool calls.
//
// Purpose:
//
//	Show the full autonomous-decomposition loop with a real LLM: submit a
//	multi-part task → the coordinator decides to spawn child peers → the
//	kernel scheduler executes the child tasks → the coordinator synthesises
//	the final report. The stdout + runtime log together are the evidence.
//
// Learning objectives:
//   - Every SDK agent carries spawn_agent / create_task for free (D1 wiring);
//     no WithTools entry needed for the syscalls.
//   - The LLM decides WHEN to decompose — the task prompt only describes the
//     work, it never forces the tool calls.
//   - Runtime log lines `agentsyscall: spawned agent ...` and
//     `agentsyscall: created task ...` prove the syscalls executed.
//   - Result.ToolCalls counts every tool call the coordinator made,
//     including the syscalls.
//
// Core APIs used (with package paths):
//   - sdk.LoadConfigFile             — github.com/Timwood0x10/ares/sdk
//   - (*cfg.ConfigFile).ToOptions()  — github.com/Timwood0x10/ares/sdk
//   - sdk.NewRuntime                 — github.com/Timwood0x10/ares/sdk
//   - (*Runtime).RegisterAgent       — github.com/Timwood0x10/ares/sdk
//   - (*Runtime).Submit              — github.com/Timwood0x10/ares/sdk
//
// Run (from the repo root — reads ./ares.yaml for your real LLM endpoints):
//
//	go run examples/27-peer-spawn-demo/main.go
//
// Expected output:
//
//	📋 Task: <multi-part analysis task>
//	🕸️  agent "coordinator" sees spawn_agent + create_task in its tool list
//	✅ Result: <the consolidated report>
//	   tool_calls: N (includes spawn_agent / create_task) | took: ...
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Timwood0x10/ares/sdk"
)

func main() {
	ctx := context.Background()

	// ── Step 1: Load ares.yaml (real LLM endpoints + key) ──
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
	rt := sdk.NewRuntime(opts...)
	defer rt.Close()

	// ── Step 2: Register the coordinator peer ──
	// The instruction defines the agent's cognition: it is a peer, it MAY
	// decompose, and it knows the syscalls exist. The decision to actually
	// call spawn_agent is left to the LLM — that decision is the point.
	rt.RegisterAgent("coordinator",
		sdk.WithInstruction(`You are a peer-coordinator agent in a flat agent
runtime. Peers are equal — you are not a leader, you delegate work by spawning
specialist peers.

You have two syscalls available:
- spawn_agent(capability, task_context): create a new peer agent with a
  declared capability. Use it when a part of the task is separable and a
  specialist peer could do it better in parallel.
- create_task(capability, payload): create a sub-task the kernel scheduler
  will assign to a capable peer. Use it to hand work to a spawned peer.
  Payload should carry {"input": "<the sub-task description>"}.

Decide yourself whether the task benefits from decomposition. When it does:
spawn one peer per separable part, create_task for each, then synthesise the
final answer from the parts. When it does not, just answer directly.`),
	)

	// ── Step 3: Submit a task complex enough to deserve decomposition ──
	// The task names three independent subsystems; a capable coordinator will
	// recognise the separable structure and spawn three specialist peers.
	task := "Analyse the three core ARES subsystems and produce a consolidated " +
		"comparison report: (1) internal/fabric/task — the durable task state " +
		"machine and capability-aware scheduler; (2) internal/fabric/agent — " +
		"the agent lifecycle fabric (spawn/suspend/resume/retire/kill/recover); " +
		"(3) internal/agentipc — the peer message bus. For each subsystem " +
		"describe its responsibilities, its key public types, and how it " +
		"interacts with the other two. Then compare the three side by side."
	// Override the task via argv when your model lacks repo context: ANY
	// genuinely multi-part task works, because the decomposition DECISION
	// belongs to the LLM — that is the whole point of this demo.
	if len(os.Args) > 1 {
		task = strings.Join(os.Args[1:], " ")
	}

	fmt.Printf("📋 Task: %s\n\n", task)
	fmt.Println("🕸️  agent \"coordinator\" carries spawn_agent + create_task in its LLM tool list (auto-wired by the runtime)")

	start := time.Now()
	result, err := rt.Submit(ctx, sdk.Task{
		Capability: "coordinator",
		Input:      task,
	})
	elapsed := time.Since(start)
	if err != nil {
		if strings.Contains(err.Error(), "API key") {
			fmt.Fprintf(os.Stderr, "❌ %v\n   → Set your LLM key in ./ares.yaml\n", err)
			return
		}
		fmt.Fprintf(os.Stderr, "❌ submit: %v\n", err)
		return
	}

	// ── Step 4: The evidence ──
	fmt.Println("✅ Result:")
	fmt.Printf("%s\n\n", result.Output)
	fmt.Printf("   tool_calls: %d | took: %v (runtime elapsed: %v)\n",
		result.ToolCalls, result.Duration.Round(time.Millisecond), elapsed.Round(time.Millisecond))

	// ── Step 5: Observation window ──
	// The coordinator's create_task calls are fire-and-forget: the shared
	// scheduler picks the sub-tasks up asynchronously (they were READY the
	// moment the syscall returned). Keep the runtime alive a few more seconds
	// so the spawned peers actually execute their sub-tasks; the runtime log
	// on stderr shows each `spawned-researcher-* → LLM call` step.
	fmt.Println("\n🕵️  observation window: waiting 45s for the scheduler to drive the spawned peers' sub-tasks…")
	fmt.Println("   (watch stderr for `spawned-researcher-* → LLM call` + `agentsyscall:` lines)")
	select {
	case <-time.After(45 * time.Second):
	case <-ctx.Done():
	}

	fmt.Println("\n   Runtime log (stderr) shows the syscalls the LLM decided to call:")
	fmt.Println("     agentsyscall: spawned agent ... registered as executor")
	fmt.Println("     agentsyscall: created task ... → READY")
	fmt.Println("     [ares:trace] spawned-researcher-* → LLM call  (sub-tasks executing)")
}
