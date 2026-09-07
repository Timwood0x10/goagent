package agentfabric

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// recordingSink captures lifecycle events for assertions.
type recordingSink struct {
	mu     sync.Mutex
	events []AgentEvent
}

func (s *recordingSink) Emit(_ context.Context, ev AgentEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
	return nil
}

func (s *recordingSink) count(typ AgentEventType) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, ev := range s.events {
		if ev.Type == typ {
			n++
		}
	}
	return n
}

// TestSpawnEstablishesProvenanceNotHierarchy verifies §13 invariant #1:
// spawn creates a parent-child link (Process Tree) but the child is a
// SAME-LEVEL cognitive process — both parent and child can compete for the
// same task (both are IDLE and schedulable). The link is provenance only,
// not a permission hierarchy.
func TestSpawnEstablishesProvenanceNotHierarchy(t *testing.T) {
	f := NewFabric()
	parent, err := f.Spawn(context.Background(), SpawnSpec{
		Identity: "parent", Capabilities: []string{"rust"},
	})
	if err != nil {
		t.Fatalf("Spawn parent: %v", err)
	}
	child, err := f.Spawn(context.Background(), SpawnSpec{
		Identity: "child", Capabilities: []string{"rust"},
		ParentID: "parent",
	})
	if err != nil {
		t.Fatalf("Spawn child: %v", err)
	}
	// Process Tree edge exists.
	if kids := f.Children("parent"); len(kids) != 1 || kids[0] != "child" {
		t.Fatalf("Process Tree must record child, got %v", kids)
	}
	// Both are same-level: IDLE, both schedulable, no permission difference.
	if parent.State != StateIdle || child.State != StateIdle {
		t.Fatalf("both must be IDLE (same level), got parent=%s child=%s",
			parent.State, child.State)
	}
	if child.Parent != "parent" {
		t.Fatalf("child.Parent must be provenance link, got %q", child.Parent)
	}
}

// TestParentDeathChildSurvives verifies §13 invariant #2 + #7: killing a
// parent does NOT kill the child or its tasks. The child stays alive (IDLE)
// and the Process Tree edge is preserved for provenance.
func TestParentDeathChildSurvives(t *testing.T) {
	f := NewFabric()
	if _, err := f.Spawn(context.Background(), SpawnSpec{
		Identity: "parent", Capabilities: []string{"rust"},
	}); err != nil {
		t.Fatalf("Spawn parent: %v", err)
	}
	if _, err := f.Spawn(context.Background(), SpawnSpec{
		Identity: "child", Capabilities: []string{"rust"},
		ParentID: "parent",
	}); err != nil {
		t.Fatalf("Spawn child: %v", err)
	}
	// Kill the parent.
	if err := f.Kill(context.Background(), "parent"); err != nil {
		t.Fatalf("Kill parent: %v", err)
	}
	// Child must still be alive and IDLE.
	child, err := f.Get("child")
	if err != nil {
		t.Fatalf("child must survive parent death: %v", err)
	}
	if child.State != StateIdle {
		t.Fatalf("child must be IDLE after parent death, got %s", child.State)
	}
	if child.Parent != "parent" {
		t.Fatalf("child.Parent must keep provenance, got %q", child.Parent)
	}
	// Process Tree edge preserved (provenance does not disappear with parent).
	if kids := f.Children("parent"); len(kids) != 1 || kids[0] != "child" {
		t.Fatalf("Process Tree edge must survive parent death, got %v", kids)
	}
}

// TestCognitiveStateCheckpointResume verifies §13 invariant #5: an agent's
// cognitive state is independently checkpointable. A new agent can resume
// the cognitive state of a dead agent (Agent disposable, cognition durable).
func TestCognitiveStateCheckpointResume(t *testing.T) {
	f := NewFabric()
	a1, err := f.Spawn(context.Background(), SpawnSpec{
		Identity: "a1", Capabilities: []string{"rust"},
	})
	if err != nil {
		t.Fatalf("Spawn a1: %v", err)
	}
	// a1 builds up cognitive state.
	orig := CognitiveState{
		Context:       "analyze rust unsafe code",
		WorkingMemory: []string{"found ptr::null", "found unwrap"},
		Decision:      "need bounds check",
		Checkpoint:    map[string]any{"step": 3},
	}
	if err := f.SetCognitiveState("a1", orig); err != nil {
		t.Fatalf("SetCognitiveState: %v", err)
	}
	// Checkpoint the cognitive state.
	cp, err := f.CheckpointCognitive("a1")
	if err != nil {
		t.Fatalf("CheckpointCognitive: %v", err)
	}
	// a1 dies.
	if err := f.Kill(context.Background(), "a1"); err != nil {
		t.Fatalf("Kill a1: %v", err)
	}
	// A new agent a2 resumes the cognitive state.
	if _, err := f.Spawn(context.Background(), SpawnSpec{
		Identity: "a2", Capabilities: []string{"rust"},
	}); err != nil {
		t.Fatalf("Spawn a2: %v", err)
	}
	if err := f.Recover(context.Background(), "a2", cp); err != nil {
		t.Fatalf("Recover into a2: %v", err)
	}
	got, err := f.CognitiveState("a2")
	if err != nil {
		t.Fatalf("CognitiveState a2: %v", err)
	}
	if got.Context != orig.Context || got.Decision != orig.Decision {
		t.Fatalf("cognitive state must survive death+recovery, got %+v", got)
	}
	if got.WorkingMemory.([]string)[0] != "found ptr::null" {
		t.Fatalf("working memory must survive, got %+v", got.WorkingMemory)
	}
	_ = a1 // silence unused
}

// TestContextThreeLayersIsolation verifies §13 invariant #6: the three
// context layers (Task Shared / Agent Private / IPC) do NOT bleed into each
// other. Private state written by one agent never appears in another agent's
// Task Shared State or Private State.
func TestContextThreeLayersIsolation(t *testing.T) {
	f := NewFabric()
	if _, err := f.Spawn(context.Background(), SpawnSpec{
		Identity: "a", Capabilities: []string{"rust"},
		TaskContext: map[string]any{"goal": "shared-goal"},
	}); err != nil {
		t.Fatalf("Spawn a: %v", err)
	}
	if _, err := f.Spawn(context.Background(), SpawnSpec{
		Identity: "b", Capabilities: []string{"rust"},
		TaskContext: map[string]any{"goal": "shared-goal"},
	}); err != nil {
		t.Fatalf("Spawn b: %v", err)
	}
	// Agent a writes to its Private layer.
	if err := f.SetPrivate("a", "hypothesis", "maybe-unsafe"); err != nil {
		t.Fatalf("SetPrivate a: %v", err)
	}
	// Agent a's Private must NOT appear in a's Task Shared.
	viewA, err := f.ContextView("a")
	if err != nil {
		t.Fatalf("ContextView a: %v", err)
	}
	if _, leak := viewA.TaskShared["hypothesis"]; leak {
		t.Fatal("Private must not leak into Task Shared (same agent)")
	}
	if viewA.Private["hypothesis"] != "maybe-unsafe" {
		t.Fatalf("Private must be readable, got %+v", viewA.Private)
	}
	// Agent a's Private must NOT appear in b's Task Shared or Private.
	viewB, err := f.ContextView("b")
	if err != nil {
		t.Fatalf("ContextView b: %v", err)
	}
	if _, leak := viewB.TaskShared["hypothesis"]; leak {
		t.Fatal("Agent a's Private must not leak into b's Task Shared")
	}
	if _, leak := viewB.Private["hypothesis"]; leak {
		t.Fatal("Agent a's Private must not leak into b's Private")
	}
	// Task Shared is the same objective state.
	if viewA.TaskShared["goal"] != viewB.TaskShared["goal"] {
		t.Fatal("Task Shared must be the same objective state for both agents")
	}
}

// TestLifecycleSuspendResumeRetireKill verifies the lifecycle primitives:
// Suspend→Resume is idempotent and state-preserving; Retire requires
// non-RUNNING; Kill works on any state and removes the agent.
func TestLifecycleSuspendResumeRetireKill(t *testing.T) {
	f := NewFabric()
	sink := &recordingSink{}
	f.WithEventSink(sink)
	if _, err := f.Spawn(context.Background(), SpawnSpec{
		Identity: "a", Capabilities: []string{"rust"},
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	// Suspend + Resume.
	if err := f.Suspend(context.Background(), "a"); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	if st, _ := f.Get("a"); st.State != StateSuspended {
		t.Fatalf("want SUSPENDED, got %s", st.State)
	}
	if err := f.Resume(context.Background(), "a"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if st, _ := f.Get("a"); st.State != StateIdle {
		t.Fatalf("want IDLE after resume, got %s", st.State)
	}
	// Suspend idempotent.
	if err := f.Suspend(context.Background(), "a"); err != nil {
		t.Fatalf("Suspend again: %v", err)
	}
	if err := f.Suspend(context.Background(), "a"); err != nil {
		t.Fatalf("Suspend idempotent: %v", err)
	}
	// Retire requires non-RUNNING.
	if err := f.SetRunning("a"); err != nil {
		t.Fatalf("SetRunning: %v", err)
	}
	if err := f.Retire(context.Background(), "a"); !errors.Is(err, ErrAgentRunning) {
		t.Fatalf("retire RUNNING must be rejected, got %v", err)
	}
	// Suspend then retire.
	if err := f.Suspend(context.Background(), "a"); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	if err := f.Retire(context.Background(), "a"); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if st, _ := f.Get("a"); st.State != StateRetired {
		t.Fatalf("want RETIRED, got %s", st.State)
	}
	// Retire idempotent.
	if err := f.Retire(context.Background(), "a"); err != nil {
		t.Fatalf("Retire idempotent: %v", err)
	}
	// Kill removes the agent.
	if err := f.Kill(context.Background(), "a"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if _, err := f.Get("a"); !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("Get after Kill must be ErrAgentNotFound, got %v", err)
	}
	// Events recorded.
	if sink.count(EventAgentSpawned) != 1 || sink.count(EventAgentRetired) != 1 {
		t.Fatalf("events: spawned=%d retired=%d", sink.count(EventAgentSpawned), sink.count(EventAgentRetired))
	}
}

// TestSpawnAutoID verifies an empty Identity gets auto-assigned.
func TestSpawnAutoID(t *testing.T) {
	f := NewFabric()
	a, err := f.Spawn(context.Background(), SpawnSpec{
		Capabilities: []string{"rust"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if a.Identity == "" {
		t.Fatal("auto-assigned id must not be empty")
	}
	b, err := f.Spawn(context.Background(), SpawnSpec{
		Capabilities: []string{"rust"},
	})
	if err != nil {
		t.Fatalf("Spawn #2: %v", err)
	}
	if a.Identity == b.Identity {
		t.Fatal("auto-assigned ids must be unique")
	}
}

// TestSpawnDuplicateRejected verifies an existing id is rejected.
func TestSpawnDuplicateRejected(t *testing.T) {
	f := NewFabric()
	if _, err := f.Spawn(context.Background(), SpawnSpec{
		Identity: "dup", Capabilities: []string{"rust"},
	}); err != nil {
		t.Fatalf("Spawn #1: %v", err)
	}
	_, err := f.Spawn(context.Background(), SpawnSpec{
		Identity: "dup", Capabilities: []string{"rust"},
	})
	if !errors.Is(err, ErrAgentExists) {
		t.Fatalf("want ErrAgentExists, got %v", err)
	}
}

// TestRecoverRevivesSuspendedAgent verifies Recover on a suspended agent
// clears the suspended state (back to IDLE) and installs the cognitive state.
func TestRecoverRevivesSuspendedAgent(t *testing.T) {
	f := NewFabric()
	if _, err := f.Spawn(context.Background(), SpawnSpec{
		Identity: "a", Capabilities: []string{"rust"},
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := f.Suspend(context.Background(), "a"); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	cs := CognitiveState{Context: "recovered-context", Checkpoint: map[string]any{"step": 1}}
	if err := f.Recover(context.Background(), "a", cs); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	st, _ := f.Get("a")
	if st.State != StateIdle {
		t.Fatalf("want IDLE after recover, got %s", st.State)
	}
	got, _ := f.CognitiveState("a")
	if got.Context != "recovered-context" {
		t.Fatalf("cognitive state must be installed, got %+v", got)
	}
}

// TestRetiredAgentRejectsOperations verifies a retired agent rejects
// suspend/resume/recover.
func TestRetiredAgentRejectsOperations(t *testing.T) {
	f := NewFabric()
	if _, err := f.Spawn(context.Background(), SpawnSpec{
		Identity: "a", Capabilities: []string{"rust"},
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if err := f.Retire(context.Background(), "a"); err != nil {
		t.Fatalf("Retire: %v", err)
	}
	if err := f.Suspend(context.Background(), "a"); !errors.Is(err, ErrAgentRetired) {
		t.Fatalf("Suspend retired must be rejected, got %v", err)
	}
	if err := f.Resume(context.Background(), "a"); !errors.Is(err, ErrAgentRetired) {
		t.Fatalf("Resume retired must be rejected, got %v", err)
	}
	if err := f.Recover(context.Background(), "a", CognitiveState{}); !errors.Is(err, ErrAgentRetired) {
		t.Fatalf("Recover retired must be rejected, got %v", err)
	}
}

// TestSetTaskContextDoesNotMutateCaller verifies the agent receives a copy of
// the task context, so the caller's map is never mutated by the agent.
func TestSetTaskContextDoesNotMutateCaller(t *testing.T) {
	f := NewFabric()
	if _, err := f.Spawn(context.Background(), SpawnSpec{
		Identity: "a", Capabilities: []string{"rust"},
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	orig := map[string]any{"goal": "x"}
	if err := f.SetTaskContext("a", orig); err != nil {
		t.Fatalf("SetTaskContext: %v", err)
	}
	// Mutate the caller's map — agent must not see it.
	orig["goal"] = "y"
	tc, _ := f.TaskContext("a")
	if tc["goal"] != "x" {
		t.Fatalf("agent must have a copy, got %v", tc["goal"])
	}
}

// TestConcurrentSpawnIsSafe verifies concurrent spawn is race-free
// (code_rules §4.6: go test -race).
func TestConcurrentSpawnIsSafe(t *testing.T) {
	f := NewFabric()
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = f.Spawn(context.Background(), SpawnSpec{
				Capabilities: []string{"rust"},
			})
		}()
	}
	wg.Wait()
	if len(f.Agents()) != 20 {
		t.Fatalf("want 20 agents, got %d", len(f.Agents()))
	}
}

// TestSpawnCarriesPriority verifies the scheduling priority requested at
// spawn is carried onto the Agent (B2: OS-thread-style thread priority) and
// that the default 0 stays 0.
func TestSpawnCarriesPriority(t *testing.T) {
	ctx := context.Background()
	f := NewFabric()

	a, err := f.Spawn(ctx, SpawnSpec{Identity: "high", Capabilities: []string{"rust"}, Priority: 2.0})
	if err != nil {
		t.Fatalf("Spawn high: %v", err)
	}
	if a.Priority != 2.0 {
		t.Fatalf("want Priority=2.0, got %v", a.Priority)
	}

	b, err := f.Spawn(ctx, SpawnSpec{Identity: "normal", Capabilities: []string{"rust"}})
	if err != nil {
		t.Fatalf("Spawn normal: %v", err)
	}
	if b.Priority != 0.0 {
		t.Fatalf("default priority must be 0, got %v", b.Priority)
	}

	// Get returns the same value (persisted on the agent).
	got, err := f.Get("high")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Priority != 2.0 {
		t.Fatalf("Get Priority = %v, want 2.0", got.Priority)
	}
}

// keep references to avoid unused import lint.
var _ = time.Now
