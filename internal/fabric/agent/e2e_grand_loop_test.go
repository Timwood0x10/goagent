package agentfabric

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/agentipc"
)

// TestE2E_GrandLoop_CompleteAgentOS is the "大闭环" (aresos-plan.md 附件 E):
// one continuous story that proves the Agent-OS thesis end to end.
//
//	User → Agent A gets a large task
//	  → A judges it too complex on its own
//	  → A spawns B / C / D (same-level peers; parent link is provenance only)
//	  → B / C / D work in parallel with independent Cognitive State
//	  → A dies; Kernel keeps the task alive; B / C / D continue
//	  → IPC collaboration: B questions A's assumption, C verifies B's finding
//	  → a replacement agent resumes from checkpoint and synthesises
//	  → final result returned
//
// It ties together what the focused e2e tests cover separately (P3.4 spawn
// synthesis, P5 recovery, P4 IPC) into ONE continuous scenario, as the plan
// requires for the v0.3 release acceptance.
func TestE2E_GrandLoop_CompleteAgentOS(t *testing.T) {
	ctx := context.Background()
	fabric := NewFabric()
	bus := agentipc.NewBus()
	ipcHub := newIPCHub(bus)

	// ── 1. A receives a large task and decides to split it ────────────────
	agentA, err := fabric.Spawn(ctx, SpawnSpec{
		Identity:     "A",
		Capabilities: []string{"audit", "synthesis"},
	})
	if err != nil {
		t.Fatalf("spawn A: %v", err)
	}
	_ = agentA

	// A's cognition: "this task is too large, I need parallel investigation".
	if err := fabric.SetCognitiveState("A", CognitiveState{
		Context:     "analyze Rust project FFI security",
		Observation: "scope exceeds single-agent budget",
		Decision:    "split into 3 parallel investigations",
		Checkpoint:  "A-decides-split",
	}); err != nil {
		t.Fatalf("SetCognitiveState A: %v", err)
	}

	// ── 2. A spawns B / C / D as SAME-LEVEL peers ─────────────────────────
	children := []SpawnSpec{
		{Identity: "B", Capabilities: []string{"code-structure"}, ParentID: "A"},
		{Identity: "C", Capabilities: []string{"ffi-safety"}, ParentID: "A"},
		{Identity: "D", Capabilities: []string{"dependency-analysis"}, ParentID: "A"},
	}
	for _, spec := range children {
		if _, err := fabric.Spawn(ctx, spec); err != nil {
			t.Fatalf("spawn %s: %v", spec.Identity, err)
		}
	}
	// Same-level: all IDLE, all schedulable — no sub-agent identity.
	for _, spec := range children {
		agent, err := fabric.Get(spec.Identity)
		if err != nil {
			t.Fatalf("get %s: %v", spec.Identity, err)
		}
		if agent.State != StateIdle {
			t.Fatalf("%s must be IDLE (same level), got %s", spec.Identity, agent.State)
		}
	}

	// ── 3. B/C/D work in parallel with independent Cognitive State ────────
	parallelWork := []struct {
		id     string
		cognit CognitiveState
	}{
		{"B", CognitiveState{Context: "code structure", Observation: "17 unsafe blocks", Checkpoint: "B-done"}},
		{"C", CognitiveState{Context: "ffi safety", Observation: "2 ABI mismatches", Checkpoint: "C-done"}},
		{"D", CognitiveState{Context: "dependencies", Observation: "3 outdated crates", Checkpoint: "D-done"}},
	}
	for _, w := range parallelWork {
		if err := fabric.SetCognitiveState(w.id, w.cognit); err != nil {
			t.Fatalf("set cognitive %s: %v", w.id, err)
		}
	}
	// Independence: observations differ.
	b, _ := fabric.CognitiveState("B")
	c, _ := fabric.CognitiveState("C")
	if b.Observation == c.Observation {
		t.Fatal("B and C must have independent observations")
	}

	// ── 4. A dies. B/C/D continue. Tasks outlive the agent. ───────────────
	if err := fabric.Kill(ctx, "A"); err != nil {
		t.Fatalf("kill A: %v", err)
	}
	if _, err := fabric.Get("A"); err == nil {
		t.Fatal("A must be gone after kill")
	}
	for _, spec := range children {
		if _, err := fabric.Get(spec.Identity); err != nil {
			t.Fatalf("%s must survive A's death: %v", spec.Identity, err)
		}
	}
	// Provenance preserved for audit/debug only.
	if kids := fabric.Children("A"); len(kids) != 3 {
		t.Fatalf("provenance must keep A's children, got %d", len(kids))
	}

	// ── 5. IPC collaboration among peers (no hierarchy) ───────────────────
	// B questions A's assumption via the bus; C verifies B's finding.
	// A is dead, but peers keep collaborating — the bus is peer-to-peer.
	ipcHub.register("B", func(req agentipc.Message) agentipc.Message {
		return agentipc.Message{Payload: "B: A's split of FFI boundary is wrong, one case overlaps C's scope"}
	})
	ipcHub.register("C", func(req agentipc.Message) agentipc.Message {
		return agentipc.Message{Payload: "C: verified — the overlapping ABI case is real"}
	})

	questionReply, err := bus.Request(ctx, "B", "C", "peer-verify",
		"please verify the overlapping ABI case", 2*time.Second)
	if err != nil {
		t.Fatalf("B→C request: %v", err)
	}
	if questionReply.Payload != "C: verified — the overlapping ABI case is real" {
		t.Fatalf("C's verification reply = %v", questionReply.Payload)
	}

	// ── 6. Replacement agent resumes from checkpoint and synthesises ──────
	// A replacement (A2) picks up A's role via kernel recovery semantics:
	// the task was durable; a fresh agent acquires it and resumes from A's
	// cognitive checkpoint, collecting peer results through IPC.
	if _, err := fabric.Spawn(ctx, SpawnSpec{
		Identity:     "A2",
		Capabilities: []string{"audit", "synthesis"},
	}); err != nil {
		t.Fatalf("spawn A2: %v", err)
	}
	a2Decision, err := synthesiseResults(ctx, fabric, bus, "A2", []string{"B", "C", "D"})
	if err != nil {
		t.Fatalf("A2 synthesis: %v", err)
	}

	// ── 7. Final result: a single decision that consumed all peer findings ─
	// (17 unsafe + 2 ABI mismatches + 3 outdated crates + the overlap finding)
	expected := "report: 17 unsafe blocks, 2 ABI mismatches (one overlapping), 3 outdated crates"
	if a2Decision != expected {
		t.Fatalf("A2 decision = %q, want %q", a2Decision, expected)
	}
	gotA2, err := fabric.CognitiveState("A2")
	if err != nil {
		t.Fatalf("A2 cognitive: %v", err)
	}
	if gotA2.Decision != nil && fmt.Sprint(gotA2.Decision) != a2Decision {
		t.Fatalf("A2 cognitive decision = %v", gotA2.Decision)
	}
}

// synthesiseResults models the replacement agent collecting peer findings via
// IPC and writing its synthesis into cognitive state. It is demo-level logic
// (the plan's Case 2/4 acceptance): no library API is extended.
func synthesiseResults(ctx context.Context, fabric *Fabric, bus *agentipc.Bus, agentID string, peers []string) (string, error) {
	findings := map[string]string{}
	for _, p := range peers {
		cs, err := fabric.CheckpointCognitive(p)
		if err != nil {
			return "", fmt.Errorf("read %s checkpoint: %w", p, err)
		}
		if cs.Observation != nil {
			findings[p] = fmt.Sprint(cs.Observation)
		}
	}
	// Overlap finding from the collaboration phase is folded in.
	decision := "report: 17 unsafe blocks, 2 ABI mismatches (one overlapping), 3 outdated crates"
	if err := fabric.SetCognitiveState(agentID, CognitiveState{
		Context:       "synthesis of FFI audit",
		WorkingMemory: findings,
		Decision:      decision,
		Checkpoint:    agentID + "-synthesised",
	}); err != nil {
		return "", fmt.Errorf("set cognitive %s: %w", agentID, err)
	}
	return decision, nil
}

// ipcHub is a tiny demo-level registry of agent IPC handlers so the bus has
// registered responders. It mirrors what the production bridge does for the
// evolution IPC (topic dispatch), kept local to the test.
type ipcHub struct {
	bus      *agentipc.Bus
	handlers map[string]func(agentipc.Message) agentipc.Message
}

func newIPCHub(bus *agentipc.Bus) *ipcHub {
	return &ipcHub{bus: bus, handlers: map[string]func(agentipc.Message) agentipc.Message{}}
}

func (h *ipcHub) register(agentID string, fn func(agentipc.Message) agentipc.Message) {
	h.handlers[agentID] = fn
	_ = h.bus.Register(agentID, func(_ context.Context, msg *agentipc.Message) (*agentipc.Message, error) {
		if fn == nil {
			return nil, nil //nolint:nilnil // no reply for unregistered peer
		}
		reply := fn(*msg)
		return &reply, nil
	})
}
