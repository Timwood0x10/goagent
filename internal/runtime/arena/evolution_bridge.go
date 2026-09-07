package arena

import (
	"context"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/coordinator"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

// EvolutionBridge connects arena fault injections to the evolution Coordinator.
// When an arena action fails — or a fault is detected — the bridge produces
// a PatchProposal and submits it to the Coordinator for evaluation.
//
// The Coordinator treats Chaos as one of 7 equal PatchSources. No special
// privileges; the same DecisionPolicy applies to all sources.
type EvolutionBridge struct {
	coordinator *coordinator.EvolutionCoordinator
}

// NewEvolutionBridge creates a bridge between arena and the evolution Coordinator.
func NewEvolutionBridge(coord *coordinator.EvolutionCoordinator) *EvolutionBridge {
	if coord == nil {
		log.Warn("NewEvolutionBridge: nil coordinator, evolution bridge disabled")
	}
	return &EvolutionBridge{coordinator: coord}
}

// OnActionExecuted is called after every arena action completes.
// Failed actions (faults) are converted to PatchProposals and submitted.
// High-priority faults (>= 9) bypass the normal decision process and apply
// immediately via ApplyEmergency for self-healing.
func (b *EvolutionBridge) OnActionExecuted(action Action, result Result) {
	if b.coordinator == nil {
		return
	}

	// Only act on failures — successful fault injection means the system
	// withstood the fault, no patch needed.
	if result.Success {
		return
	}

	proposal := b.buildProposal(action, result)
	if proposal == nil {
		return
	}

	// High-priority faults (priority >= 9) bypass the decision process
	// and apply immediately via ApplyEmergency. This enables self-healing
	// for critical scenarios like leader/agent death.
	if proposal.Priority >= 9 {
		if err := b.coordinator.ApplyEmergency(context.Background(), proposal.Patch); err != nil {
			log.Error("evolution bridge: emergency apply failed",
				"action_type", action.Type,
				"target", action.TargetID,
				"error", err,
			)
			return
		}
		log.Info("evolution bridge: emergency apply succeeded",
			"action_type", action.Type,
			"target", action.TargetID,
			"patch_type", proposal.Patch.Type,
		)
		return
	}

	// Normal priority — submit to the Coordinator for evaluation.
	b.coordinator.Submit(*proposal)
	log.Info("evolution bridge: submitted patch proposal",
		"action_type", action.Type,
		"target", action.TargetID,
		"patch_type", proposal.Patch.Type,
		"priority", proposal.Priority,
	)
}

// buildProposal converts a failed arena action into a PatchProposal.
// Returns nil if the action type cannot be mapped to a patch type.
func (b *EvolutionBridge) buildProposal(action Action, _ Result) *coordinator.PatchProposal {
	patchType, target, value := arenaActionToPatch(action)

	if patchType < 0 {
		return nil
	}

	proposal := &coordinator.PatchProposal{
		Patch: patch.RuntimePatch{
			Type:   patchType,
			Target: target,
			Reason: "chaos: fault detected by arena",
			Source: "chaos",
		},
		Source:    coordinator.SourceChaos,
		Reason:    "Chaos Engineering detected fault: " + string(action.Type),
		Priority:  chaosPriority(action.Type),
		Timestamp: time.Now(),
	}

	if value != "" {
		proposal.Patch.Value = value
	}

	return proposal
}

// arenaActionToPatch maps arena action types to evolution patch types.
// The mapping is not 1:1 — some arena actions (e.g. ActionResumeAgent) are
// not actionable by the evolution system and return -1.
func arenaActionToPatch(action Action) (patch.PatchType, string, string) {
	switch action.Type {
	// ── DAG mutations ─────────────────────────────────
	case ActionRemoveNode:
		// A node was removed by chaos → evolution should replace it.
		return patch.PatchInsertNode, action.TargetID,
			"chaos-restored-" + action.TargetID

	case ActionRemoveEdge:
		// An edge was removed → evolution should restore it.
		return patch.PatchAddEdge, action.SourceID, action.TargetID

	// ── Agent-level faults → replace the failing node ──
	case ActionKillAgent, ActionKillLeader:
		return patch.PatchReplaceNode, action.TargetID, "fallback-" + action.TargetID

	// ── Performance faults → change scheduler ─────────
	case ActionSlowAgent, ActionToolTimeout:
		return patch.PatchChangeScheduler, "graph.scheduler", ""

	// ── Infrastructure faults → change recovery strategy ──
	case ActionLLMFailure, ActionMemoryCorrupt, ActionMCPDisconnect:
		return patch.PatchChangeRecoveryStrategy, "recovery.strategy", "replace_node"

	// ── Not actionable ─────────────────────────────────
	default:
		return -1, "", ""
	}
}

// chaosPriority assigns priority based on fault severity.
// Higher priority = more urgent = more likely to be auto-applied.
func chaosPriority(at ActionType) int {
	switch at {
	case ActionKillLeader, ActionKillOrchestrator:
		return 9 // Critical — system without leader
	case ActionKillAgent, ActionRemoveNode, ActionRemoveEdge:
		return 8 // High — DAG integrity compromised
	case ActionNetworkPartition, ActionMCPDisconnect:
		return 7 // High — connectivity lost
	case ActionLLMFailure:
		return 6 // Medium — degraded but functional
	case ActionSlowAgent, ActionToolTimeout:
		return 5 // Medium — performance degradation
	case ActionMemoryCorrupt:
		return 6 // Medium — data integrity risk
	case ActionPauseAgent:
		return 4 // Low — paused, can resume
	default:
		return 3 // Low — informational
	}
}
