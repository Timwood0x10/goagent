// Package diff — MemoryDiffer produces memory config patches from genome evolution.
package diff

import (
	"context"
	"fmt"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

// MemoryDiffer computes memory parameter differences between two genome snapshots.
// Snapshots must be genome.MemoryGenomeConfig values.
type MemoryDiffer struct{}

// targetMemory is the patch target identifier for the memory subsystem.
// Kept as a constant so goconst stays quiet and the value is grep-able.
const targetMemory = "memory"

// NewMemoryDiffer creates a new MemoryDiffer.
func NewMemoryDiffer() *MemoryDiffer {
	return &MemoryDiffer{}
}

// Name returns the differ identifier, matching the MemoryGenome name.
func (d *MemoryDiffer) Name() string { return genome.MemoryGenomeName }

// Diff compares old and new MemoryGenomeConfig snapshots.
func (d *MemoryDiffer) Diff(_ context.Context, old, new any) ([]patch.RuntimePatch, error) {
	oldCfg, ok := old.(genome.MemoryGenomeConfig)
	if !ok {
		return nil, fmt.Errorf("memory differ: old snapshot is %T, want MemoryGenomeConfig", old)
	}
	newCfg, ok := new.(genome.MemoryGenomeConfig)
	if !ok {
		return nil, fmt.Errorf("memory differ: new snapshot is %T, want MemoryGenomeConfig", new)
	}

	var patches []patch.RuntimePatch

	// Planner-type patches: max_history, max_tasks, max_sessions
	if oldCfg.MaxHistory != newCfg.MaxHistory || oldCfg.MaxSessions != newCfg.MaxSessions {
		vals := map[string]any{}
		if oldCfg.MaxHistory != newCfg.MaxHistory {
			vals["max_history"] = newCfg.MaxHistory
		}
		if oldCfg.MaxSessions != newCfg.MaxSessions {
			vals["max_sessions"] = newCfg.MaxSessions
		}
		patches = append(patches, patch.RuntimePatch{
			Type:   patch.PatchChangePlanner,
			Target: targetMemory,
			Value:  vals,
			Reason: fmt.Sprintf("memory: MaxHistory %d→%d, MaxSessions %d→%d",
				oldCfg.MaxHistory, newCfg.MaxHistory, oldCfg.MaxSessions, newCfg.MaxSessions),
			Source: srcMemory,
		})
	}

	// Budget-type patches: max_distilled_tasks
	if oldCfg.MaxDistilledTasks != newCfg.MaxDistilledTasks {
		vals := map[string]any{
			"max_distilled_tasks": newCfg.MaxDistilledTasks,
		}
		patches = append(patches, patch.RuntimePatch{
			Type:   patch.PatchChangeBudget,
			Target: targetMemory,
			Value:  vals,
			Reason: fmt.Sprintf("memory: MaxDistilledTasks %d→%d", oldCfg.MaxDistilledTasks, newCfg.MaxDistilledTasks),
			Source: srcMemory,
		})
	}

	// Reducer-type patches: use_structured_cleaning
	if oldCfg.UseStructuredCleaning != newCfg.UseStructuredCleaning {
		vals := map[string]any{
			"use_structured_cleaning": newCfg.UseStructuredCleaning,
		}
		patches = append(patches, patch.RuntimePatch{
			Type:   patch.PatchChangeReducer,
			Target: targetMemory,
			Value:  vals,
			Reason: fmt.Sprintf("memory: UseStructuredCleaning %v→%v",
				oldCfg.UseStructuredCleaning, newCfg.UseStructuredCleaning),
			Source: srcMemory,
		})
	}

	return patches, nil
}
