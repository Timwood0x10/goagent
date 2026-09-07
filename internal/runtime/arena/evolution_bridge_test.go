package arena

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/coordinator"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

func TestNewEvolutionBridge(t *testing.T) {
	coord := newTestCoordinator(t)
	bridge := NewEvolutionBridge(coord)
	require.NotNil(t, bridge)
	assert.Same(t, coord, bridge.coordinator)
}

func TestNewEvolutionBridge_Nil(t *testing.T) {
	bridge := NewEvolutionBridge(nil)
	require.NotNil(t, bridge)
	assert.Nil(t, bridge.coordinator)
}

func TestEvolutionBridge_OnActionExecuted_Success(t *testing.T) {
	coord := newTestCoordinator(t)
	bridge := NewEvolutionBridge(coord)

	// Successful action should NOT produce a proposal.
	bridge.OnActionExecuted(
		Action{ID: "a1", Type: ActionRemoveNode, TargetID: "validator"},
		Result{Success: true},
	)
	assert.Equal(t, 0, coord.PendingCount(),
		"successful actions should not submit proposals")
}

func TestEvolutionBridge_OnActionExecuted_Failure(t *testing.T) {
	coord := newTestCoordinator(t)
	bridge := NewEvolutionBridge(coord)

	bridge.OnActionExecuted(
		Action{ID: "a2", Type: ActionRemoveNode, TargetID: "validator", CreatedAt: time.Now()},
		Result{Success: false, Error: "node not found"},
	)

	// Failed action SHOULD produce a proposal.
	assert.Equal(t, 1, coord.PendingCount(),
		"failed actions should submit proposals")

	// Evaluate and verify the patch.
	coord.Evaluate(context.Background())
	history := coord.PatchHistory()
	require.GreaterOrEqual(t, len(history), 1)
	assert.Equal(t, coordinator.SourceChaos, history[0].Proposal.Source)
}

func TestEvolutionBridge_OnActionExecuted_NilCoordinator(t *testing.T) {
	bridge := NewEvolutionBridge(nil)

	// Should not panic.
	bridge.OnActionExecuted(
		Action{Type: ActionRemoveNode},
		Result{Success: false},
	)
}

func TestEvolutionBridge_BuildProposal_KillAgent(t *testing.T) {
	coord := newTestCoordinator(t)
	bridge := NewEvolutionBridge(coord)

	bridge.OnActionExecuted(
		Action{ID: "a3", Type: ActionKillAgent, TargetID: "processor"},
		Result{Success: false, Error: "agent killed"},
	)

	coord.Evaluate(context.Background())
	history := coord.PatchHistory()
	require.GreaterOrEqual(t, len(history), 1)
	assert.Equal(t, patch.PatchReplaceNode, history[0].Proposal.Patch.Type)
	assert.Equal(t, "processor", history[0].Proposal.Patch.Target)
	assert.Equal(t, 8, history[0].Proposal.Priority)
}

func TestEvolutionBridge_BuildProposal_RemoveEdge(t *testing.T) {
	coord := newTestCoordinator(t)
	bridge := NewEvolutionBridge(coord)

	bridge.OnActionExecuted(
		Action{ID: "a4", Type: ActionRemoveEdge, TargetID: "C", SourceID: "B"},
		Result{Success: false},
	)

	coord.Evaluate(context.Background())
	history := coord.PatchHistory()
	require.GreaterOrEqual(t, len(history), 1)
	assert.Equal(t, patch.PatchAddEdge, history[0].Proposal.Patch.Type)
	assert.Equal(t, "B", history[0].Proposal.Patch.Target)
	assert.Equal(t, "C", history[0].Proposal.Patch.Value)
}

func TestEvolutionBridge_BuildProposal_LLMFailure(t *testing.T) {
	coord := newTestCoordinator(t)
	bridge := NewEvolutionBridge(coord)

	bridge.OnActionExecuted(
		Action{ID: "a5", Type: ActionLLMFailure, TargetID: "llm-agent"},
		Result{Success: false},
	)

	coord.Evaluate(context.Background())
	// LLM failure has priority 6 < AutoApplyThreshold(8), so the patch is
	// delayed rather than applied. Verify the decision was made (delay is
	// recorded in DecisionHistory, not PatchHistory).
	require.GreaterOrEqual(t, len(coord.DecisionHistory()), 1)
}

func TestEvolutionBridge_BuildProposal_UnmappedAction(t *testing.T) {
	coord := newTestCoordinator(t)
	bridge := NewEvolutionBridge(coord)

	// ActionResumeAgent is not mapped to any patch → no proposal.
	bridge.OnActionExecuted(
		Action{ID: "a6", Type: ActionResumeAgent, TargetID: "agent-x"},
		Result{Success: false},
	)

	assert.Equal(t, 0, coord.PendingCount(),
		"unmapped action types should not submit proposals")
}

func TestEvolutionBridge_ChaosPriority(t *testing.T) {
	assert.Equal(t, 9, chaosPriority(ActionKillLeader), "kill_leader most urgent")
	assert.Equal(t, 8, chaosPriority(ActionRemoveNode), "remove_node high")
	assert.Equal(t, 5, chaosPriority(ActionSlowAgent), "slow_agent medium")
	assert.Equal(t, 3, chaosPriority("unknown"), "unknown low")
}

func TestService_SetEvolutionBridge(t *testing.T) {
	s := NewService(NewInjector(nil, nil), nil, nil)
	coord := newTestCoordinator(t)
	bridge := NewEvolutionBridge(coord)

	s.SetEvolutionBridge(bridge)
	assert.Same(t, bridge, s.evolutionBridge)
}

func TestService_Execute_WithEvolutionBridge(t *testing.T) {
	// End-to-end: Service.Execute should trigger the evolution bridge
	// when an action fails.
	s := NewService(NewInjector(nil, nil), nil, nil)
	coord := newTestCoordinator(t)
	bridge := NewEvolutionBridge(coord)
	s.SetEvolutionBridge(bridge)

	// Execute an action that will fail (nil runtime).
	result := s.Execute(context.Background(), Action{
		ID:       "e2e-1",
		Type:     ActionKillAgent,
		TargetID: "nonexistent",
	})
	assert.False(t, result.Success)

	// The bridge should have submitted a proposal.
	assert.Equal(t, 1, coord.PendingCount(),
		"Service.Execute should trigger evolution bridge on failure")
}

// ── Helpers ────────────────────────────────

func newTestCoordinator(t *testing.T) *coordinator.EvolutionCoordinator {
	t.Helper()
	return coordinator.NewEvolutionCoordinator(
		coordinator.DefaultPolicy(),
		patch.NewRegistry(),
	)
}
