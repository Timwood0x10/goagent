package agentfabric

import (
	"context"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/agentipc"
	"github.com/Timwood0x10/ares/internal/core/models"
)

// ipcAnalystCognition is the child agent's execution body: it serves analysis
// requests on the IPC bus. Its output is configurable so the test can prove
// the synthesised result tracks the child's REAL output (改子 Agent 输出会改变
// 最终结果 — H2 验收).
type ipcAnalystCognition struct {
	output string
}

var _ Cognition = (*ipcAnalystCognition)(nil)

func (c *ipcAnalystCognition) ExecuteStep(_ context.Context, task *models.Task) (*StepOutcome, error) {
	res := models.NewTaskResult(task.TaskID, task.AgentType)
	res.SetSuccess(nil, "child ran")
	return &StepOutcome{Done: true, Result: res}, nil
}

// ipcCoordinatorCognition is the parent agent's execution body: it requests
// the child's analysis over the IPC bus and synthesises the final result from
// the child's REAL reply payload — the synthesis is not hardcoded.
type ipcCoordinatorCognition struct {
	bus     *agentipc.Bus
	timeout time.Duration
}

var _ Cognition = (*ipcCoordinatorCognition)(nil)

func (c *ipcCoordinatorCognition) ExecuteStep(ctx context.Context, task *models.Task) (*StepOutcome, error) {
	reply, err := c.bus.Request(ctx, "coordinator", "analyst", "analyze", map[string]any{"task": task.TaskID}, c.timeout)
	if err != nil {
		return nil, err
	}
	payload, _ := reply.Payload.(map[string]any)
	childOut, _ := payload["analysis"].(string)
	res := models.NewTaskResult(task.TaskID, task.AgentType)
	res.SetSuccess(nil, "synthesis: "+childOut)
	return &StepOutcome{Done: true, Result: res}, nil
}

// TestE2E_PeerIPCSynthesis is the H2 Peer IPC + synthesis segment
// (aresos-agentos-plan H2): the parent agent gathers the child agent's output
// through the IPC bus and synthesises the final result from it — changing the
// child's output changes the final result (no hardcoded synthesis).
func TestE2E_PeerIPCSynthesis(t *testing.T) {
	ctx := context.Background()
	bus := agentipc.NewBus()

	// Child agent: serves analysis requests over the bus.
	child := &ipcAnalystCognition{output: "analysis-v1"}
	if err := bus.Register("analyst", func(_ context.Context, msg *agentipc.Message) (*agentipc.Message, error) {
		return &agentipc.Message{Payload: map[string]any{"analysis": child.output}}, nil
	}); err != nil {
		t.Fatalf("Register analyst: %v", err)
	}

	// Parent agent: synthesises from the child's real reply.
	parent := &ipcCoordinatorCognition{bus: bus, timeout: 2 * time.Second}

	// Both are fabric agents (A1): the IPC is between equal cognitive
	// processes — no leader orchestrates it.
	fab := NewFabric()
	spawn := func(id string, cap string, cog Cognition) {
		if _, err := fab.Spawn(ctx, SpawnSpec{
			Identity:     id,
			Capabilities: []string{cap},
			CognitionFactory: func([]string) Cognition {
				return cog
			},
		}); err != nil {
			t.Fatalf("spawn %s: %v", id, err)
		}
	}
	spawn("analyst", "analyst", child)
	spawn("coordinator", "coordinator", parent)

	a, err := fab.Get("coordinator")
	if err != nil {
		t.Fatalf("Get coordinator: %v", err)
	}
	out, err := a.ExecuteStep(ctx, models.NewTask("t-ipc", models.AgentType("coordinator"), nil))
	if err != nil {
		t.Fatalf("ExecuteStep: %v", err)
	}
	if out.Result == nil || out.Result.Reason != "synthesis: analysis-v1" {
		t.Fatalf("synthesis must contain the child's real output, got %q", out.Result.Reason)
	}

	// 改子 Agent 输出会改变最终结果：the child now returns v2, the parent's
	// synthesis must track it (not a hardcoded v1).
	child.output = "analysis-v2"
	out2, err := a.ExecuteStep(ctx, models.NewTask("t-ipc", models.AgentType("coordinator"), nil))
	if err != nil {
		t.Fatalf("ExecuteStep 2: %v", err)
	}
	if out2.Result == nil || out2.Result.Reason != "synthesis: analysis-v2" {
		t.Fatalf("synthesis must track the child's changed output, got %q", out2.Result.Reason)
	}
}
