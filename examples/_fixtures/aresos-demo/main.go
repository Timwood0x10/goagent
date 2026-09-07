// ARES "Agent-OS" Grand-Loop demo — one continuous story proving the thesis:
//
//	User → Agent A receives a large task
//	  → A judges it too complex → spawns B / C / D (same-level peers)
//	  → B / C / D work in parallel with independent Cognitive State
//	  → A dies; the task outlives the agent; B / C / D continue
//	  → Peer IPC collaboration: B questions A's split, C verifies
//	  → a replacement agent resumes from checkpoint and synthesises
//	  → final result returned
//
// Purpose:
//
//	This is aresos-plan.md 附件 E (「唯一大闭环」) as a runnable, deterministic
//	program. Unlike the LLM-backed examples, it runs with ZERO external
//	dependencies (no LLM, no config): the agent fabric and IPC bus are pure
//	in-memory, so `go run` prints the whole Agent-OS story on any machine.
//
//	The demo deliberately uses only public library APIs — agentfabric (Spawn /
//	SetCognitiveState / CheckpointCognitive / Kill / Children) and agentipc
//	(Bus / Register / Request). No library code is extended; the "agent
//	cognition" here is demo-level logic standing in for a real LLM loop.
//
// Learning objectives:
//   - Agent OS 核心：Agent 无等级（peer network），Spawn 建立 provenance 而非
//     hierarchy（aresos-plan.md 核心模型修正 §1/§3）。
//   - Kernel 管 lifecycle/恢复，Agent 管「要不要拆、找谁协作」（§2/§4/§7）。
//   - Agent death ≠ Task death：A 死后 B/C/D 继续，替代者从 checkpoint 接续（P5）。
//   - 协作关系运行时动态形成：B 反驳 A、C 验证 B，A ≡ B ≡ C ≡ D（P4）。
//   - P3 resource governance：预算（token/tool/deadline）超限是协作式 yield，
//     不是硬抢占（「Agent Runtime resource governance, not cgroups」）。
//
// Core APIs used (with package paths):
//   - agentfabric.NewFabric              — github.com/Timwood0x10/ares/internal/fabric/agent
//   - (*Fabric).Spawn / SetCognitiveState / CheckpointCognitive / Kill / Children
//   - (*Fabric).CheckResource / ConsumeResource / DeadlineExceeded / ResetResource
//   - agentipc.NewBus / (*Bus).Register / Request — github.com/Timwood0x10/ares/internal/agentipc
//
// Run (from the repo root):
//
//	go run examples/aresos-demo/main.go
//
// Expected output: a 7-step log ending with A2's synthesis decision:
//
//	report: 17 unsafe blocks, 2 ABI mismatches (one overlapping), 3 outdated crates
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/Timwood0x10/ares/internal/agentipc"
	"github.com/Timwood0x10/ares/internal/fabric/agent"
)

func main() {
	ctx := context.Background()
	fabric := agentfabric.NewFabric()
	bus := agentipc.NewBus()

	fmt.Println("═══ ARES Agent-OS Grand Loop (aresos-plan.md 附件 E) ═══")

	// ── 1. A receives a large task and decides to split it ────────────────
	fmt.Println("\n[1] A receives: analyze Rust project FFI security")
	spawnAgent(ctx, fabric, "A", []string{"audit", "synthesis"})
	_ = fabric.SetCognitiveState("A", agentfabric.CognitiveState{
		Context:     "analyze Rust project FFI security",
		Observation: "scope exceeds single-agent budget",
		Decision:    "split into 3 parallel investigations",
		Checkpoint:  "A-decides-split",
	})
	fmt.Println("    A cognition: task too large → split into B/C/D (peers, no hierarchy)")

	// ── 2. A spawns B / C / D as same-level peers ─────────────────────────
	fmt.Println("\n[2] A spawns three peer investigators:")
	spawnAgent(ctx, fabric, "B", []string{"code-structure"}, "A")
	spawnAgent(ctx, fabric, "C", []string{"ffi-safety"}, "A")
	spawnAgent(ctx, fabric, "D", []string{"dependency-analysis"}, "A")
	fmt.Println("    parent link is provenance only — B/C/D are schedulable equals")

	// ── 3. B / C / D work in parallel with independent Cognitive State ────
	fmt.Println("\n[3] Peers investigate in parallel:")
	work := []struct {
		id     string
		cognit agentfabric.CognitiveState
	}{
		{"B", agentfabric.CognitiveState{Context: "code structure", Observation: "17 unsafe blocks", Checkpoint: "B-done"}},
		{"C", agentfabric.CognitiveState{Context: "ffi safety", Observation: "2 ABI mismatches", Checkpoint: "C-done"}},
		{"D", agentfabric.CognitiveState{Context: "dependencies", Observation: "3 outdated crates", Checkpoint: "D-done"}},
	}
	for _, w := range work {
		_ = fabric.SetCognitiveState(w.id, w.cognit)
		fmt.Printf("    %s: %s\n", w.id, w.cognit.Observation)
	}

	// ── 4. A dies; the task outlives the agent ────────────────────────────
	fmt.Println("\n[4] A crashes mid-flight. Kernel keeps the task durable.")
	_ = fabric.Kill(ctx, "A")
	fmt.Println("    A is gone; B/C/D continue their work (agent death ≠ task death)")
	fmt.Printf("    provenance kept for audit: A's children = %v\n", fabric.Children("A"))

	// ── 5. Peer IPC collaboration: B questions A, C verifies ──────────────
	fmt.Println("\n[5] Peer collaboration over the bus (no leader):")
	registerPeer(bus, "B", func(msg agentipc.Message) agentipc.Message {
		return agentipc.Message{Payload: "B: A's split overlaps — one ABI case is in both FFI and structure scope"}
	})
	registerPeer(bus, "C", func(msg agentipc.Message) agentipc.Message {
		return agentipc.Message{Payload: "C: verified — the overlapping ABI case is real"}
	})
	reply, err := bus.Request(ctx, "B", "C", "peer-verify",
		"please verify the overlapping ABI case", 2*time.Second)
	if err != nil {
		fmt.Printf("    IPC error: %v\n", err)
	} else {
		fmt.Printf("    B → C: %v\n    C → B: %v\n",
			"please verify the overlapping ABI case", reply.Payload)
	}

	// ── 5b. P3 resource governance: budgets are cooperative yield, not cgroups
	fmt.Println("\n[5b] P3 governance: A2 is spawned with token/tool/deadline budgets")
	spawnGovernedAgent(ctx, fabric, "A2", []string{"audit", "synthesis"})
	fmt.Println("    pre-quantum check: within budget?",
		mustCheck(fabric, "A2", 10_000, 50))
	fmt.Println("    consume 4k tokens + 5 tools (still within budget):",
		mustConsume(fabric, "A2", 4_000, 5) == nil)
	fmt.Println("    budget usage after consume:",
		mustUsage(fabric, "A2"))

	// ── 6. Replacement agent resumes and synthesises ──────────────────────
	fmt.Println("\n[6] Kernel resurrects the task: replacement agent A2 resumes from checkpoint")
	decision := synthesise(ctx, fabric, "A2", []string{"B", "C", "D"})

	// ── 7. Final result ───────────────────────────────────────────────────
	fmt.Printf("\n[7] Final synthesis by A2:\n    %s\n", decision)
	fmt.Println("\n═══ Agent-OS loop complete: A ≡ B ≡ C ≡ D ≡ A2, Kernel kept it safe ═══")
}

func spawnAgent(ctx context.Context, f *agentfabric.Fabric, id string, caps []string, parent ...string) {
	spec := agentfabric.SpawnSpec{Identity: id, Capabilities: caps}
	if len(parent) > 0 {
		spec.ParentID = parent[0]
	}
	if _, err := f.Spawn(ctx, spec); err != nil {
		panic(fmt.Sprintf("spawn %s: %v", id, err))
	}
}

// spawnGovernedAgent spawns an agent with P3 budgets: 50k tokens, 100 tool
// calls, 10 minute deadline — the plan's exact example.
func spawnGovernedAgent(ctx context.Context, f *agentfabric.Fabric, id string, caps []string) {
	if _, err := f.Spawn(ctx, agentfabric.SpawnSpec{
		Identity:     id,
		Capabilities: caps,
		Governance: agentfabric.Governance{
			TokenBudget: 50_000,
			ToolBudget:  100,
			Deadline:    10 * time.Minute,
		},
	}); err != nil {
		panic(fmt.Sprintf("spawn %s: %v", id, err))
	}
}

func mustCheck(f *agentfabric.Fabric, id string, token, tool int) bool {
	ok, err := f.CheckResource(id, token, tool)
	if err != nil {
		panic(fmt.Sprintf("CheckResource %s: %v", id, err))
	}
	return ok
}

func mustConsume(f *agentfabric.Fabric, id string, token, tool int) error {
	return f.ConsumeResource(id, token, tool)
}

func mustUsage(f *agentfabric.Fabric, id string) string {
	tok, tool, err := f.BudgetUsage(id)
	if err != nil {
		panic(fmt.Sprintf("BudgetUsage %s: %v", id, err))
	}
	return fmt.Sprintf("tokens=%d tools=%d", tok, tool)
}

func registerPeer(bus *agentipc.Bus, id string, fn func(agentipc.Message) agentipc.Message) {
	_ = bus.Register(id, func(_ context.Context, msg *agentipc.Message) (*agentipc.Message, error) {
		reply := fn(*msg)
		return &reply, nil
	})
}

func synthesise(ctx context.Context, f *agentfabric.Fabric, id string, peers []string) string {
	findings := map[string]string{}
	for _, p := range peers {
		cs, err := f.CheckpointCognitive(p)
		if err != nil {
			fmt.Printf("    (warn: no checkpoint for %s: %v)\n", p, err)
			continue
		}
		if cs.Observation != nil {
			findings[p] = fmt.Sprint(cs.Observation)
		}
	}
	decision := "report: 17 unsafe blocks, 2 ABI mismatches (one overlapping), 3 outdated crates"
	_ = f.SetCognitiveState(id, agentfabric.CognitiveState{
		Context:       "synthesis of FFI audit",
		WorkingMemory: findings,
		Decision:      decision,
		Checkpoint:    id + "-synthesised",
	})
	return decision
}
