package main

import (
	"context"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/aresrecovery"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/fabric/agent"
	"github.com/Timwood0x10/ares/internal/fabric/task"
)

// evolutionSpawnBody is the execution body a GA/evolution policy would
// inject into its spawned agents (GA spawn 真实执行体). It completes every
// task in one quantum.
type evolutionSpawnBody struct{}

func (evolutionSpawnBody) ExecuteStep(_ context.Context, task *models.Task) (*agentfabric.StepOutcome, error) {
	res := models.NewTaskResult(task.TaskID, task.AgentType)
	res.SetSuccess(nil, "evolved by "+task.TaskID)
	return &agentfabric.StepOutcome{Done: true, Result: res}, nil
}

// TestEvolutionSpawnedAgentIsExecutableAndSchedulable verifies the F1
// acceptance (GA spawn 的 agent 能被真实调度执行，
// 非 phantom): an evolution policy that spawns agents WITH their execution body
// (CognitionFactory) produces REAL cognitive processes that
// the kernel scheduler selects and executes, not empty shells. The chain is
// exactly the production one: AdaptPopulation → agents.Spawn → scheduler
// WithAgentFabric candidate → Schedule → Acquire → RunQuantum → COMPLETED.
func TestEvolutionSpawnedAgentIsExecutableAndSchedulable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agents := agentfabric.NewFabric()
	fabric := taskfabric.NewFabric()
	sched := NewKernelScheduler(fabric, map[string]CapabilityExecutor{}, newLoadTracker())
	sched.PollInterval = 20 * time.Millisecond
	sched.WithAgentFabric(agents)
	go sched.Run(ctx)

	// GA/evolution decides to spawn a "reviewer" capability agent, carrying
	// its execution body (CognitionFactory).
	adapter := aresrecovery.NewEvolutionAdapter(agents, agents)
	spawned, err := adapter.AdaptPopulation(ctx, []agentfabric.SpawnSpec{
		{
			Identity:     "evolved-reviewer",
			Capabilities: []string{"reviewer"},
			CognitionFactory: func([]string) agentfabric.Cognition {
				return evolutionSpawnBody{}
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("AdaptPopulation: %v", err)
	}
	if len(spawned) != 1 {
		t.Fatalf("want 1 spawned, got %d", len(spawned))
	}

	// The spawned agent is a REAL execution body — not a phantom shell.
	a, err := agents.Get("evolved-reviewer")
	if err != nil {
		t.Fatalf("Get evolved agent: %v", err)
	}
	if !a.Executable() {
		t.Fatal("F1: GA-spawned agent must be executable (Cognition injected), not a phantom")
	}

	// The GA-spawned agent is schedulable: a task requiring its capability is
	// executed by it through the real scheduler chain.
	if err := fabric.Create(&taskfabric.Task{
		ID:          "f1-task",
		Capability:  "reviewer",
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 1},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if state := waitTaskState(t, fabric, "f1-task", 3*time.Second); state != taskfabric.StateCompleted {
		t.Fatalf("GA-spawned agent must be scheduled and complete the task, got %s", state)
	}
}

// TestGAInterventionChangesCandidateOrdering is the scheduling-weight
// acceptance (the spawn test only verified an agent CAN be selected, not
// that GA intervention actually REORDERS candidates). Two equally-capable
// agents, same task capability: before the GA intervention agent-A (higher
// confidence) wins; after the intervention flips the confidences, agent-B
// wins. The ordering change must be observable through the real scheduler
// chain (candidate build → ConfidenceFor → Score → Schedule), not just a
// unit-level Pick call.
func TestGAInterventionChangesCandidateOrdering(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	f := taskfabric.NewFabric()
	tracker := newLoadTracker()
	agentA := &stubAgent{id: "agent-A", typ: models.AgentType("code")}
	agentB := &stubAgent{id: "agent-B", typ: models.AgentType("code")}

	// Pre-intervention: A is the high-confidence candidate (0.9 vs 0.1).
	tracker.SetAgentConfidence("agent-A", 0.9)
	tracker.SetAgentConfidence("agent-B", 0.1)

	sched := NewKernelScheduler(f, map[string]CapabilityExecutor{
		"agent-A": agentA,
		"agent-B": agentB,
	}, tracker)
	sched.PollInterval = 10 * time.Millisecond
	go sched.Run(ctx)

	runTask := func(id string) string {
		task := &taskfabric.Task{
			ID:          id,
			Capability:  "code",
			RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 1},
		}
		if err := f.Create(task); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			tk, err := f.Task(id)
			if err == nil && tk.State == taskfabric.StateCompleted {
				return tk.Owner
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("task %s did not complete", id)
		return ""
	}

	before := runTask("f1-before")
	if before != "agent-A" {
		t.Fatalf("pre-intervention the high-confidence agent must win, got %q", before)
	}
	if agentA.executedCount() != 1 || agentB.executedCount() != 0 {
		t.Fatalf("pre-intervention only agent-A must execute (A=%d B=%d)", agentA.executedCount(), agentB.executedCount())
	}

	// GA intervention: evolution flips the confidences (agent-A's history
	// degraded, agent-B improved) — the SAME candidate set must now order
	// differently.
	tracker.SetAgentConfidence("agent-A", 0.1)
	tracker.SetAgentConfidence("agent-B", 0.9)

	after := runTask("f1-after")
	if after != "agent-B" {
		t.Fatalf("post-intervention the re-weighted candidate must win, got %q", after)
	}
	if agentA.executedCount() != 1 || agentB.executedCount() != 1 {
		t.Fatalf("post-intervention only agent-B must execute (A=%d B=%d)", agentA.executedCount(), agentB.executedCount())
	}
}
