package diff

import (
	"context"
	"fmt"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
	"github.com/Timwood0x10/ares/internal/workflow/engine"
)

// RecoveryDiffer computes recovery policy differences between two genome snapshots.
// Snapshots must be *engine.RecoveryPolicy values.
type RecoveryDiffer struct{}

// NewRecoveryDiffer creates a new RecoveryDiffer.
func NewRecoveryDiffer() *RecoveryDiffer {
	return &RecoveryDiffer{}
}

// Name returns the differ identifier, matching the RecoveryGenome name.
func (d *RecoveryDiffer) Name() string { return genome.RecoveryGenomeName }

// Diff compares old and new RecoveryPolicy snapshots.
func (d *RecoveryDiffer) Diff(_ context.Context, old, new any) ([]patch.RuntimePatch, error) {
	oldPolicy, ok := old.(*engine.RecoveryPolicy)
	if !ok {
		return nil, fmt.Errorf("recovery differ: old snapshot is %T, want *engine.RecoveryPolicy", old)
	}
	newPolicy, ok := new.(*engine.RecoveryPolicy)
	if !ok {
		return nil, fmt.Errorf("recovery differ: new snapshot is %T, want *engine.RecoveryPolicy", new)
	}

	var patches []patch.RuntimePatch

	if oldPolicy.Strategy != newPolicy.Strategy {
		patches = append(patches, patch.RuntimePatch{
			Type:   patch.PatchChangeRecoveryStrategy,
			Target: "recovery.strategy",
			Value:  string(newPolicy.Strategy),
			Reason: fmt.Sprintf("recovery: %s → %s", oldPolicy.Strategy, newPolicy.Strategy),
			Source: srcRecovery,
		})
	}

	if oldPolicy.MaxAttempts != newPolicy.MaxAttempts {
		patches = append(patches, patch.RuntimePatch{
			Type:   patch.PatchChangeMaxRetries,
			Target: "recovery.max_attempts",
			Value:  newPolicy.MaxAttempts,
			Reason: fmt.Sprintf("recovery: MaxAttempts %d → %d", oldPolicy.MaxAttempts, newPolicy.MaxAttempts),
			Source: srcRecovery,
		})
	}

	if oldPolicy.ReplacementAgent != newPolicy.ReplacementAgent {
		patches = append(patches, patch.RuntimePatch{
			Type:   patch.PatchChangeRecoveryStrategy,
			Target: "recovery.replacement_agent",
			Value:  newPolicy.ReplacementAgent,
			Reason: fmt.Sprintf("recovery: ReplacementAgent %s → %s", oldPolicy.ReplacementAgent, newPolicy.ReplacementAgent),
			Source: srcRecovery,
		})
	}

	return patches, nil
}
