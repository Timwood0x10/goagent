package agentfabric

import (
	"context"
	"sync"
	"testing"
	"time"
)

// This file is the P3.4 end-to-end proof (aresos-plan.md §P3.4 + §P3 验收):
// Agent A receives a complex task, judges it too large, spawns B/C/D as
// same-level peers, each child has independent Cognitive State, children
// report results via IPC (simulated through the Fabric's context layer),
// parent A synthesises. A dies → B/C/D survive; tasks do not die with A.
//
// The test also exercises P3.2 (Context three-layer separation): each
// agent's Private State is walled off from Task Shared State, and a
// child's Private State never appears in another agent's Task Shared
// layer.

// TestP3_4_EndToEndSpawnSynthesis is the P3.4 acceptance scenario
// (aresos-plan.md §P3 验收, simplified):
//
//  1. Agent A receives "audit unsafe FFI" (too complex).
//  2. A spawns B (code-structure), C (ffi-safety), D (dependency-analysis).
//  3. B/C/D are SAME-LEVEL agents (A ≡ B ≡ C ≡ D).
//  4. B/C/D have independent Cognitive State.
//  5. B/C/D can independently checkpoint.
//  6. A dies — B/C/D do NOT die (§13 invariant #2).
//  7. Tasks do not disappear when A dies.
//  8. A (or a replacement) synthesises the children's results.
func TestP3_4_EndToEndSpawnSynthesis(t *testing.T) {
	ctx := context.Background()
	fabric := NewFabric()

	// Step 1: spawn Agent A (root agent).
	if _, err := fabric.Spawn(ctx, SpawnSpec{
		Identity:     "A",
		Capabilities: []string{"audit", "synthesis"},
	}); err != nil {
		t.Fatalf("spawn A: %v", err)
	}

	// Step 2: A judges the task too complex → spawns B/C/D.
	// All children are SAME-LEVEL (A ≡ B ≡ C ≡ D). The parent-child
	// link is provenance only, not a permission hierarchy.
	specs := []SpawnSpec{
		{Identity: "B", Capabilities: []string{"code-structure"}, ParentID: "A"},
		{Identity: "C", Capabilities: []string{"ffi-safety"}, ParentID: "A"},
		{Identity: "D", Capabilities: []string{"dependency-analysis"}, ParentID: "A"},
	}
	children := make([]*Agent, 0, len(specs))
	for _, spec := range specs {
		child, spawnErr := fabric.Spawn(ctx, spec)
		if spawnErr != nil {
			t.Fatalf("spawn %s: %v", spec.Identity, spawnErr)
		}
		children = append(children, child)
	}

	// Step 3: B/C/D are same-level: all IDLE, all schedulable.
	for _, child := range children {
		if child.State != StateIdle {
			t.Fatalf("child %s must be IDLE (same level), got %s", child.Identity, child.State)
		}
	}

	// Step 4: each child has independent Cognitive State.
	// Write different cognitive state into each child.
	cognitiveB := CognitiveState{
		Context:       "analyze code structure",
		Observation:   "found 17 unsafe blocks",
		WorkingMemory: "FFI boundary X suspicious",
		Decision:      "investigate X",
		ToolState:     "llvm-analysis completed",
		Checkpoint:    "checkpoint-B",
	}
	cognitiveC := CognitiveState{
		Context:     "analyze FFI safety",
		Observation: "two ABI mismatch found",
		Decision:    "flag ABI boundary",
		Checkpoint:  "checkpoint-C",
	}
	cognitiveD := CognitiveState{
		Context:     "analyze dependencies",
		Observation: "3 outdated crates with unsafe",
		Decision:    "recommend upgrade",
		Checkpoint:  "checkpoint-D",
	}
	if err := fabric.SetCognitiveState("B", cognitiveB); err != nil {
		t.Fatalf("SetCognitiveState B: %v", err)
	}
	if err := fabric.SetCognitiveState("C", cognitiveC); err != nil {
		t.Fatalf("SetCognitiveState C: %v", err)
	}
	if err := fabric.SetCognitiveState("D", cognitiveD); err != nil {
		t.Fatalf("SetCognitiveState D: %v", err)
	}

	// Verify independence: each child's cognitive state is distinct.
	for _, child := range children {
		got, err := fabric.CognitiveState(child.Identity)
		if err != nil {
			t.Fatalf("CognitiveState %s: %v", child.Identity, err)
		}
		if got.Checkpoint == nil {
			t.Fatalf("child %s cognitive state not set", child.Identity)
		}
	}
	// B's observation ≠ C's observation — independent cognition.
	gotB, _ := fabric.CognitiveState("B")
	gotC, _ := fabric.CognitiveState("C")
	if gotB.Observation == gotC.Observation {
		t.Fatal("B and C must have different observations (independent cognition)")
	}

	// Step 5: B/C/D can independently checkpoint.
	checkpointB, err := fabric.CheckpointCognitive("B")
	if err != nil {
		t.Fatalf("CheckpointCognitive B: %v", err)
	}
	if checkpointB.Checkpoint != "checkpoint-B" {
		t.Fatalf("B checkpoint mismatch: got %v", checkpointB.Checkpoint)
	}
	checkpointC, err := fabric.CheckpointCognitive("C")
	if err != nil {
		t.Fatalf("CheckpointCognitive C: %v", err)
	}
	if checkpointC.Checkpoint != "checkpoint-C" {
		t.Fatalf("C checkpoint mismatch: got %v", checkpointC.Checkpoint)
	}
	checkpointD, err := fabric.CheckpointCognitive("D")
	if err != nil {
		t.Fatalf("CheckpointCognitive D: %v", err)
	}
	if checkpointD.Checkpoint != "checkpoint-D" {
		t.Fatalf("D checkpoint mismatch: got %v", checkpointD.Checkpoint)
	}

	// Step 6: A dies (kill). B/C/D must survive (§13 invariant #2).
	if err := fabric.Kill(ctx, "A"); err != nil {
		t.Fatalf("kill A: %v", err)
	}
	// Verify A is gone.
	if _, err := fabric.Get("A"); err == nil {
		t.Fatal("A must be gone after kill")
	}
	// Verify B/C/D survive.
	for _, child := range children {
		if _, err := fabric.Get(child.Identity); err != nil {
			t.Fatalf("child %s must survive A's death: %v", child.Identity, err)
		}
	}
	// Process Tree edge is preserved (provenance).
	if kids := fabric.Children("A"); len(kids) != 3 {
		t.Fatalf("Process Tree must preserve A's children, got %d", len(kids))
	}

	// Step 7: Tasks do not disappear when A dies.
	// (Agent death ≠ Task death; tasks are owned by taskfabric, not by the
	// agent — this is the core ARES philosophy. Here we verify the agent
	// fabric side: B/C/D are still alive and can continue executing.)
	for _, child := range children {
		if err := fabric.SetRunning(child.Identity); err != nil {
			t.Fatalf("child %s cannot continue after A dies: %v", child.Identity, err)
		}
	}

	// Step 8: Synthesis. A replacement agent E can pick up A's role and
	// synthesise the children's results.
	if _, err := fabric.Spawn(ctx, SpawnSpec{
		Identity:     "E",
		Capabilities: []string{"audit", "synthesis"},
	}); err != nil {
		t.Fatalf("spawn E: %v", err)
	}
	// E collects children's checkpoints (simulating IPC synthesis).
	results := make(map[string]any, 3)
	for _, child := range children {
		cs, err := fabric.CheckpointCognitive(child.Identity)
		if err != nil {
			t.Fatalf("E reading %s checkpoint: %v", child.Identity, err)
		}
		results[child.Identity] = cs.Checkpoint
	}
	if len(results) != 3 {
		t.Fatalf("E must collect 3 child results, got %d", len(results))
	}
	// E's synthesis writes its own cognitive state.
	if err := fabric.SetCognitiveState("E", CognitiveState{
		Context:       "synthesis of FFI audit",
		WorkingMemory: results,
		Decision:      "report 2 ABI mismatches + 17 unsafe blocks + 3 outdated crates",
	}); err != nil {
		t.Fatalf("SetCognitiveState E: %v", err)
	}
	gotE, err := fabric.CognitiveState("E")
	if err != nil {
		t.Fatalf("CognitiveState E: %v", err)
	}
	if gotE.Decision == nil {
		t.Fatal("E must have a synthesis decision")
	}
}

// TestP3_2_ContextThreeLayerSeparation verifies P3.2 (aresos-plan.md §P3.2):
// Task Shared State, Agent Private State, and IPC are strictly separated.
// A child's Private State NEVER appears in another agent's Task Shared State,
// and setting a Private key does not bleed into Task Shared.
func TestP3_2_ContextThreeLayerSeparation(t *testing.T) {
	ctx := context.Background()
	fabric := NewFabric()

	// Spawn two agents A and B (peers).
	if _, err := fabric.Spawn(ctx, SpawnSpec{
		Identity:     "A",
		Capabilities: []string{"audit"},
	}); err != nil {
		t.Fatalf("spawn A: %v", err)
	}
	if _, err := fabric.Spawn(ctx, SpawnSpec{
		Identity:     "B",
		Capabilities: []string{"review"},
	}); err != nil {
		t.Fatalf("spawn B: %v", err)
	}

	// Set Task Shared State for A.
	taskShared := map[string]any{
		"goal":        "audit unsafe FFI",
		"constraints": []string{"no false positives"},
		"artifacts":   []string{"report.md"},
	}
	if err := fabric.SetTaskContext("A", taskShared); err != nil {
		t.Fatalf("SetTaskContext A: %v", err)
	}

	// Set Private State for A (hypotheses, scratchpad).
	if err := fabric.SetPrivate("A", "hypothesis", "FFI boundary X is unsafe"); err != nil {
		t.Fatalf("SetPrivate A hypothesis: %v", err)
	}
	if err := fabric.SetPrivate("A", "scratchpad", "draft notes about X"); err != nil {
		t.Fatalf("SetPrivate A scratchpad: %v", err)
	}

	// Verify: Task Shared does NOT contain Private keys.
	view, err := fabric.ContextView("A")
	if err != nil {
		t.Fatalf("ContextView A: %v", err)
	}
	for k := range view.Private {
		if _, leaked := view.TaskShared[k]; leaked {
			t.Fatalf("private key %q leaked into TaskShared", k)
		}
	}
	// Task Shared has the goal, not the hypothesis.
	if _, ok := view.TaskShared["goal"]; !ok {
		t.Fatal("TaskShared must contain goal")
	}
	if _, ok := view.TaskShared["hypothesis"]; ok {
		t.Fatal("hypothesis must NOT appear in TaskShared")
	}
	// Private has the hypothesis.
	if view.Private["hypothesis"] != "FFI boundary X is unsafe" {
		t.Fatalf("Private hypothesis mismatch: got %v", view.Private["hypothesis"])
	}

	// Verify: B's Task Shared State does NOT see A's Private State.
	taskCtxB, err := fabric.TaskContext("B")
	if err != nil {
		t.Fatalf("TaskContext B: %v", err)
	}
	if _, leaked := taskCtxB["hypothesis"]; leaked {
		t.Fatal("A's private hypothesis leaked into B's TaskContext")
	}
	if _, leaked := taskCtxB["scratchpad"]; leaked {
		t.Fatal("A's private scratchpad leaked into B's TaskContext")
	}

	// Verify: B's Private State is independent from A's Private State.
	if err := fabric.SetPrivate("B", "hypothesis", "FFI boundary Y is safe"); err != nil {
		t.Fatalf("SetPrivate B: %v", err)
	}
	privateA, err := fabric.Private("A", "hypothesis")
	if err != nil {
		t.Fatalf("Private A: %v", err)
	}
	privateB, err := fabric.Private("B", "hypothesis")
	if err != nil {
		t.Fatalf("Private B: %v", err)
	}
	if privateA == privateB {
		t.Fatal("A and B must have independent private hypotheses")
	}
	if privateA != "FFI boundary X is unsafe" {
		t.Fatalf("A's private hypothesis changed after B set its own: got %v", privateA)
	}
}

// TestP3_4_ParentDeathChildrenContinueTasks verifies §P3 acceptance:
// "A 死亡，B/C/D 不死亡" and "Task 不因 A 死亡而消失" and
// "B/C/D 可以继续执行".
func TestP3_4_ParentDeathChildrenContinueTasks(t *testing.T) {
	ctx := context.Background()
	fabric := NewFabric()

	// Spawn parent A and children B/C.
	if _, err := fabric.Spawn(ctx, SpawnSpec{
		Identity:     "A-parent",
		Capabilities: []string{"audit"},
	}); err != nil {
		t.Fatalf("spawn A: %v", err)
	}
	if _, err := fabric.Spawn(ctx, SpawnSpec{
		Identity:     "B-child",
		Capabilities: []string{"code"},
		ParentID:     "A-parent",
	}); err != nil {
		t.Fatalf("spawn B: %v", err)
	}
	if _, err := fabric.Spawn(ctx, SpawnSpec{
		Identity:     "C-child",
		Capabilities: []string{"review"},
		ParentID:     "A-parent",
	}); err != nil {
		t.Fatalf("spawn C: %v", err)
	}

	// B and C set cognitive checkpoints (simulating in-progress work).
	if err := fabric.SetCognitiveState("B-child", CognitiveState{
		Checkpoint: "B-progress",
	}); err != nil {
		t.Fatalf("SetCognitiveState B: %v", err)
	}
	if err := fabric.SetCognitiveState("C-child", CognitiveState{
		Checkpoint: "C-progress",
	}); err != nil {
		t.Fatalf("SetCognitiveState C: %v", err)
	}

	// Kill A.
	if err := fabric.Kill(ctx, "A-parent"); err != nil {
		t.Fatalf("kill A: %v", err)
	}

	// B and C survive and can continue.
	if err := fabric.SetRunning("B-child"); err != nil {
		t.Fatalf("B cannot continue after A dies: %v", err)
	}
	if err := fabric.SetRunning("C-child"); err != nil {
		t.Fatalf("C cannot continue after A dies: %v", err)
	}

	// B and C's checkpoints are preserved.
	csB, err := fabric.CheckpointCognitive("B-child")
	if err != nil {
		t.Fatalf("CheckpointCognitive B: %v", err)
	}
	if csB.Checkpoint != "B-progress" {
		t.Fatalf("B checkpoint lost after A death: got %v", csB.Checkpoint)
	}
	csC, err := fabric.CheckpointCognitive("C-child")
	if err != nil {
		t.Fatalf("CheckpointCognitive C: %v", err)
	}
	if csC.Checkpoint != "C-progress" {
		t.Fatalf("C checkpoint lost after A death: got %v", csC.Checkpoint)
	}

	// New agent E can resume from a child's checkpoint (recovery scenario).
	if _, err := fabric.Spawn(ctx, SpawnSpec{
		Identity:     "E-replacement",
		Capabilities: []string{"code"},
	}); err != nil {
		t.Fatalf("spawn E: %v", err)
	}
	if err := fabric.Recover(ctx, "E-replacement", csB); err != nil {
		t.Fatalf("E recover from B checkpoint: %v", err)
	}
	gotE, err := fabric.CognitiveState("E-replacement")
	if err != nil {
		t.Fatalf("CognitiveState E: %v", err)
	}
	if gotE.Checkpoint != "B-progress" {
		t.Fatalf("E must resume from B's checkpoint: got %v", gotE.Checkpoint)
	}
}

// TestP3_4_ConcurrentSpawnSynthesis exercises the concurrent spawn + IPC
// scenario with goroutines (code_rules: managed goroutines).
// Multiple children work in parallel, report via a channel, and the parent
// synthesises.
func TestP3_4_ConcurrentSpawnSynthesis(t *testing.T) {
	ctx := context.Background()
	fabric := NewFabric()

	// Spawn parent A.
	if _, err := fabric.Spawn(ctx, SpawnSpec{
		Identity:     "A",
		Capabilities: []string{"audit"},
	}); err != nil {
		t.Fatalf("spawn A: %v", err)
	}

	// Spawn children concurrently using errgroup (managed goroutines).
	childSpecs := []SpawnSpec{
		{Identity: "B", Capabilities: []string{"code"}, ParentID: "A"},
		{Identity: "C", Capabilities: []string{"review"}, ParentID: "A"},
		{Identity: "D", Capabilities: []string{"dependency"}, ParentID: "A"},
	}
	var wg sync.WaitGroup
	resultCh := make(chan string, len(childSpecs))
	for _, spec := range childSpecs {
		wg.Add(1)
		go func(s SpawnSpec) {
			defer wg.Done()
			child, err := fabric.Spawn(ctx, s)
			if err != nil {
				return
			}
			// Simulate work: set cognitive state with a result.
			_ = fabric.SetCognitiveState(child.Identity, CognitiveState{
				Context:       "working",
				WorkingMemory: s.Capabilities[0] + " result",
				Checkpoint:    s.Identity + "-done",
			})
			resultCh <- child.Identity
		}(spec)
	}
	wg.Wait()
	close(resultCh)

	// Collect results.
	results := make(map[string]string, len(childSpecs))
	for agentID := range resultCh {
		cs, err := fabric.CheckpointCognitive(agentID)
		if err != nil {
			t.Fatalf("CheckpointCognitive %s: %v", agentID, err)
		}
		results[agentID] = cs.Checkpoint.(string)
	}

	// All children must have reported.
	if len(results) != 3 {
		t.Fatalf("want 3 results, got %d", len(results))
	}
	for _, id := range []string{"B", "C", "D"} {
		if results[id] != id+"-done" {
			t.Fatalf("child %s result mismatch: got %s", id, results[id])
		}
	}

	// Parent A synthesises.
	if err := fabric.SetCognitiveState("A", CognitiveState{
		Context:       "synthesis",
		WorkingMemory: results,
		Decision:      "synthesised all child results",
	}); err != nil {
		t.Fatalf("SetCognitiveState A: %v", err)
	}
	gotA, err := fabric.CognitiveState("A")
	if err != nil {
		t.Fatalf("CognitiveState A: %v", err)
	}
	if gotA.Decision != "synthesised all child results" {
		t.Fatalf("A synthesis mismatch: got %v", gotA.Decision)
	}
}

// Ensure time is referenced (used in other tests in this package).
var _ = time.Second
