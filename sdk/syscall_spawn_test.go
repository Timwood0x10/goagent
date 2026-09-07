package sdk

import (
	"context"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/api/core"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/fabric/task"
)

// TestSDKAgentExecutorTypeCapabilityOverride verifies the scheduler-facing
// identity of an SDK executor.
//
// Bug scenario: before the fix Type() always returned the agent name, so a
// peer created by spawn_agent("researcher") reported its GENERATED id
// ("spawned-researcher-1") as its type and could never match a sub-task
// declared with capability "researcher".
//
// Fix contract: the typ field carries the DECLARED capability when set;
// without it the executor falls back to the agent name (the pre-spawn
// behavior for RegisterAgent-created executors).
func TestSDKAgentExecutorTypeCapabilityOverride(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()

	cases := []struct {
		name      string
		agentName string
		typ       models.AgentType
		wantType  models.AgentType
	}{
		{
			name:      "declared capability wins",
			agentName: "spawned-researcher-1",
			typ:       models.AgentType("researcher"),
			wantType:  models.AgentType("researcher"),
		},
		{
			name:      "empty typ falls back to agent name",
			agentName: "coder",
			typ:       "",
			wantType:  models.AgentType("coder"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &sdkAgentExecutor{agent: rt.NewAgent(tc.agentName), typ: tc.typ}
			if got := exec.Type(); got != tc.wantType {
				t.Fatalf("Type() = %q, want %q", got, tc.wantType)
			}
			if got := exec.ID(); got != tc.agentName {
				t.Fatalf("ID() = %q, want %q", got, tc.agentName)
			}
		})
	}
}

// TestSDKSpawnedPeerExecutesSubTask is the end-to-end regression for the
// spawn capability fix: a coordinator whose LLM decides to
// decompose must produce a spawned peer that the shared scheduler can match
// to the sub-task's declared capability.
//
// Flow (all through the real production path):
//
//	Submit(coordinator) → LLM tool call spawn_agent(researcher)
//	  → Kernel spawns peer + registers it as an executor
//	  → LLM tool call create_task(researcher, payload{input})
//	  → Kernel creates a READY sub-task stamped Origin=coordinator
//	  → scheduler matches the sub-task to the spawned peer BY CAPABILITY
//	  → peer executes it → COMPLETED
//
// Before the fix step "match by capability" never succeeded: the spawned
// executor's Type() was its generated id, Score() returned 0 for every
// candidate, and the sub-task stalled in READY forever.
func TestSDKSpawnedPeerExecutesSubTask(t *testing.T) {
	ctx := context.Background()
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()
	rt.llmSvc = &mockLLMSvc{responses: []*core.GenerateResponse{
		// Coordinator iteration 0: decide to spawn a specialist peer.
		// only L2-routable capabilities spawn executable peers.
		{Content: "", ToolCalls: []core.ToolCall{
			mockToolCall("tc1", "spawn_agent", `{"capability":"tool/researcher"}`),
		}},
		// Coordinator iteration 1: hand work to the peer via a sub-task.
		{Content: "", ToolCalls: []core.ToolCall{
			mockToolCall("tc2", "create_task",
				`{"capability":"tool/researcher","payload":{"input":"analyse subsystem X"}}`),
		}},
		// Coordinator iteration 2: final synthesis answer.
		{Content: "synthesis done"},
		// Spawned peer's own run: its sub-task result.
		{Content: "sub-task analysis done"},
	}}

	rt.RegisterAgent("coordinator")

	res, err := rt.Submit(ctx, Task{Capability: "coordinator", Input: "decompose this"})
	if err != nil {
		t.Fatalf("Submit error: %v", err)
	}
	if res.Output == "" {
		t.Fatal("Submit must return the coordinator's output")
	}

	// The first syscall consumed sequence 1: the spawned peer's deterministic
	// id. It must be registered on the shared scheduler WITH ITS DECLARED
	// CAPABILITY as its scheduler-facing type (the fix under test).
	exec, ok := rt.sched.LookupExecutor("spawned-tool/researcher-1")
	if !ok {
		t.Fatal("spawn_agent must register the spawned peer as a scheduler executor")
	}
	if got := exec.Type(); got != models.AgentType("tool/researcher") {
		t.Fatalf("spawned executor Type() = %q, want %q (declared capability, not generated id)", got, "tool/researcher")
	}

	// The second syscall consumed sequence 2: the sub-task id. It must be
	// picked up by the spawned peer (capability match) and driven to
	// COMPLETED — the end-to-end proof that autonomous decomposition closes.
	const subTaskID = "task-tool/researcher-2"
	deadline := time.Now().Add(5 * time.Second)
	var tk *taskfabric.Task
	for time.Now().Before(deadline) {
		task, err := rt.sdkFabric.Task(subTaskID)
		if err == nil && task.State == taskfabric.StateCompleted {
			tk = task
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if tk == nil {
		final, ferr := rt.sdkFabric.Task(subTaskID)
		if ferr != nil {
			t.Fatalf("sub-task %s never completed and cannot be read: %v", subTaskID, ferr)
		}
		t.Fatalf("sub-task %s state = %s, want COMPLETED (spawned peer must match by capability)", subTaskID, final.State)
	}

	// Provenance: the create_task caller is stamped from the tool context by
	// the agent loop engine, so the sub-task records the coordinator as its
	// origin — the LLM cannot forge or omit it.
	if tk.Origin != "coordinator" {
		t.Fatalf("sub-task Origin = %q, want %q (caller stamped from tool context)", tk.Origin, "coordinator")
	}
}
