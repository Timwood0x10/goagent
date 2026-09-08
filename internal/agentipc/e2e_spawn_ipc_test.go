package agentipc

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/fabric/agent"
)

// This file is the spawn + IPC end-to-end proof using the IPC Bus:
//
//  1. Agent A spawns B/C/D as same-level peers via agentfabric.
//  2. B/C/D register handlers on the IPC Bus.
//  3. A sends IPC messages to B/C/D (peer communication, no leader).
//  4. B/C/D reply with their findings.
//  5. A synthesises the results.
//  6. A dies → B/C/D survive → B/C/D can still communicate.
//  7. Child can communicate with non-parent (B ↔ C).
//
// This test does NOT use the leader path — it proves "A ≡ B ≡ C ≡ D"
// (peer equivalence).

// TestEndToEndSpawnIPC is the combined end-to-end spawn scenario.
func TestEndToEndSpawnIPC(t *testing.T) {
	ctx := context.Background()
	agents := agentfabric.NewFabric()
	bus := NewBus()

	// Step 1: spawn Agent A (root, auditor).
	_, err := agents.Spawn(ctx, agentfabric.SpawnSpec{
		Identity:     "A",
		Capabilities: []string{"audit", "synthesis"},
	})
	if err != nil {
		t.Fatalf("spawn A: %v", err)
	}

	// Step 2: A spawns B/C/D as same-level peers.
	childSpecs := []agentfabric.SpawnSpec{
		{Identity: "B", Capabilities: []string{"code-structure"}, ParentID: "A"},
		{Identity: "C", Capabilities: []string{"ffi-safety"}, ParentID: "A"},
		{Identity: "D", Capabilities: []string{"dependency-analysis"}, ParentID: "A"},
	}
	for _, spec := range childSpecs {
		if _, err := agents.Spawn(ctx, spec); err != nil {
			t.Fatalf("spawn %s: %v", spec.Identity, err)
		}
	}

	// Step 3: register IPC handlers for B/C/D.
	// Each child receives a Request from A and replies with its finding.
	findings := map[string]string{
		"B": "found 17 unsafe blocks",
		"C": "two ABI mismatch found",
		"D": "3 outdated crates with unsafe",
	}
	for _, childID := range []string{"B", "C", "D"} {
		childID := childID // capture
		if err := bus.Register(childID, func(_ context.Context, msg *Message) (*Message, error) {
			return &Message{
				From:    childID,
				To:      msg.From,
				Topic:   "finding-reply",
				Payload: findings[childID],
			}, nil
		}); err != nil {
			t.Fatalf("register %s: %v", childID, err)
		}
	}

	// Step 4: A sends IPC requests to B/C/D and collects replies.
	// This proves peer communication (A ≡ B ≡ C ≡ D).
	var mu sync.Mutex
	results := make(map[string]string, 3)
	var wg sync.WaitGroup
	for _, childID := range []string{"B", "C", "D"} {
		wg.Add(1)
		go func(target string) {
			defer wg.Done()
			reply, err := bus.Request(ctx, "A", target, "request-finding", nil, 5*time.Second)
			if err != nil {
				return
			}
			mu.Lock()
			results[target] = reply.Payload.(string)
			mu.Unlock()
		}(childID)
	}
	wg.Wait()

	if len(results) != 3 {
		t.Fatalf("want 3 IPC replies, got %d", len(results))
	}
	for _, childID := range []string{"B", "C", "D"} {
		if results[childID] != findings[childID] {
			t.Fatalf("child %s reply mismatch: got %q want %q", childID, results[childID], findings[childID])
		}
	}

	// Step 5: A synthesises the results. Write synthesis to A's cognitive state.
	synthesis := "report: " + results["B"] + "; " + results["C"] + "; " + results["D"]
	if err := agents.SetCognitiveState("A", agentfabric.CognitiveState{
		Context:       "synthesis of FFI audit",
		WorkingMemory: results,
		Decision:      synthesis,
	}); err != nil {
		t.Fatalf("SetCognitiveState A: %v", err)
	}
	gotA, _ := agents.CognitiveState("A")
	if gotA.Decision != synthesis {
		t.Fatalf("A synthesis mismatch: got %v", gotA.Decision)
	}

	// Step 6: A dies → B/C/D must survive and still communicate.
	if err := agents.Kill(ctx, "A"); err != nil {
		t.Fatalf("kill A: %v", err)
	}
	if _, err := agents.Get("A"); err == nil {
		t.Fatal("A must be gone after kill")
	}
	for _, childID := range []string{"B", "C", "D"} {
		if _, err := agents.Get(childID); err != nil {
			t.Fatalf("child %s must survive A's death: %v", childID, err)
		}
	}

	// Step 7: B and C can communicate directly (child ↔ non-parent).
	// B sends a message to C (peer communication without A).
	reply, err := bus.Request(ctx, "B", "C", "cross-check", "is ABI boundary X safe?", 5*time.Second)
	if err != nil {
		t.Fatalf("B → C IPC after A death: %v", err)
	}
	if reply.Payload != findings["C"] {
		t.Fatalf("C's reply to B mismatch: got %q", reply.Payload)
	}

	// Step 8: new agent E can resume A's role via synthesis from children.
	_, err = agents.Spawn(ctx, agentfabric.SpawnSpec{
		Identity:     "E",
		Capabilities: []string{"audit", "synthesis"},
	})
	if err != nil {
		t.Fatalf("spawn E: %v", err)
	}
	// E synthesises the same results from B/C/D.
	resultsE := make(map[string]string, 3)
	for _, childID := range []string{"B", "C", "D"} {
		reply, err := bus.Request(ctx, "E", childID, "request-finding", nil, 5*time.Second)
		if err != nil {
			t.Fatalf("E → %s IPC: %v", childID, err)
		}
		resultsE[childID] = reply.Payload.(string)
	}
	synthesisE := "report: " + resultsE["B"] + "; " + resultsE["C"] + "; " + resultsE["D"]
	if synthesisE != synthesis {
		t.Fatalf("E synthesis must match A's: got %q want %q", synthesisE, synthesis)
	}
}

// TestChildCanCommunicateWithNonParent verifies peer acceptance:
// "Child can communicate with non-parent" — two children of different
// parents can message each other directly.
func TestChildCanCommunicateWithNonParent(t *testing.T) {
	ctx := context.Background()
	agents := agentfabric.NewFabric()
	bus := NewBus()

	// Spawn two separate parent agents.
	if _, err := agents.Spawn(ctx, agentfabric.SpawnSpec{
		Identity: "parent1", Capabilities: []string{"audit"},
	}); err != nil {
		t.Fatalf("spawn parent1: %v", err)
	}
	if _, err := agents.Spawn(ctx, agentfabric.SpawnSpec{
		Identity: "parent2", Capabilities: []string{"review"},
	}); err != nil {
		t.Fatalf("spawn parent2: %v", err)
	}

	// Each parent spawns a child.
	if _, err := agents.Spawn(ctx, agentfabric.SpawnSpec{
		Identity: "child1", Capabilities: []string{"code"}, ParentID: "parent1",
	}); err != nil {
		t.Fatalf("spawn child1: %v", err)
	}
	if _, err := agents.Spawn(ctx, agentfabric.SpawnSpec{
		Identity: "child2", Capabilities: []string{"review"}, ParentID: "parent2",
	}); err != nil {
		t.Fatalf("spawn child2: %v", err)
	}

	// Register handlers.
	if err := bus.Register("child1", func(_ context.Context, msg *Message) (*Message, error) {
		return &Message{From: "child1", To: msg.From, Payload: "child1-reply"}, nil
	}); err != nil {
		t.Fatalf("register child1: %v", err)
	}
	if err := bus.Register("child2", func(_ context.Context, msg *Message) (*Message, error) {
		return &Message{From: "child2", To: msg.From, Payload: "child2-reply"}, nil
	}); err != nil {
		t.Fatalf("register child2: %v", err)
	}

	// child1 sends to child2 (non-parent communication).
	reply, err := bus.Request(ctx, "child1", "child2", "cross-check", "hello", 5*time.Second)
	if err != nil {
		t.Fatalf("child1 → child2 IPC: %v", err)
	}
	if reply.Payload != "child2-reply" {
		t.Fatalf("child2 reply mismatch: got %q", reply.Payload)
	}

	// child2 sends to child1 (bidirectional).
	reply2, err := bus.Request(ctx, "child2", "child1", "cross-check", "hello back", 5*time.Second)
	if err != nil {
		t.Fatalf("child2 → child1 IPC: %v", err)
	}
	if reply2.Payload != "child1-reply" {
		t.Fatalf("child1 reply mismatch: got %q", reply2.Payload)
	}
}

// TestNoLeaderPermissionBypass verifies the peer model invariant:
// "不存在 Leader 权限绕过" — there is no special "leader" agent that
// can bypass the IPC layer. All agents use the same Send/Request/Reply
// primitives. A leader, if it exists, is just another peer on the bus.
func TestNoLeaderPermissionBypass(t *testing.T) {
	ctx := context.Background()
	bus := NewBus()

	// Register "leader" and "worker" as plain agents.
	leaderHandler := func(_ context.Context, msg *Message) (*Message, error) {
		return &Message{From: "leader", To: msg.From, Payload: "leader-reply"}, nil
	}
	workerHandler := func(_ context.Context, msg *Message) (*Message, error) {
		return &Message{From: "worker", To: msg.From, Payload: "worker-reply"}, nil
	}
	if err := bus.Register("leader", leaderHandler); err != nil {
		t.Fatalf("register leader: %v", err)
	}
	if err := bus.Register("worker", workerHandler); err != nil {
		t.Fatalf("register worker: %v", err)
	}

	// Leader sends to worker (same path as worker → leader).
	reply, err := bus.Request(ctx, "leader", "worker", "task", "do work", 5*time.Second)
	if err != nil {
		t.Fatalf("leader → worker IPC: %v", err)
	}
	if reply.Payload != "worker-reply" {
		t.Fatalf("worker reply mismatch: got %q", reply.Payload)
	}

	// Worker sends to leader (same path, no special permission).
	reply2, err := bus.Request(ctx, "worker", "leader", "report", "done", 5*time.Second)
	if err != nil {
		t.Fatalf("worker → leader IPC: %v", err)
	}
	if reply2.Payload != "leader-reply" {
		t.Fatalf("leader reply mismatch: got %q", reply2.Payload)
	}
}
