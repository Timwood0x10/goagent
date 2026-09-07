package diff

import (
	"context"
	"fmt"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

// KnowledgeDiffer computes knowledge parameter differences between two genome snapshots.
// Snapshots must be genome.KnowledgeGenomeConfig values.
type KnowledgeDiffer struct{}

// NewKnowledgeDiffer creates a new KnowledgeDiffer.
func NewKnowledgeDiffer() *KnowledgeDiffer {
	return &KnowledgeDiffer{}
}

// Name returns the differ identifier, matching the KnowledgeGenome name.
func (d *KnowledgeDiffer) Name() string { return genome.KnowledgeGenomeName }

// Diff compares old and new KnowledgeGenomeConfig snapshots.
func (d *KnowledgeDiffer) Diff(_ context.Context, old, new any) ([]patch.RuntimePatch, error) {
	oldCfg, ok := old.(genome.KnowledgeGenomeConfig)
	if !ok {
		return nil, fmt.Errorf("knowledge differ: old snapshot is %T, want KnowledgeGenomeConfig", old)
	}
	newCfg, ok := new.(genome.KnowledgeGenomeConfig)
	if !ok {
		return nil, fmt.Errorf("knowledge differ: new snapshot is %T, want KnowledgeGenomeConfig", new)
	}

	var patches []patch.RuntimePatch

	if oldCfg.MaxResults != newCfg.MaxResults {
		patches = append(patches, patch.RuntimePatch{
			Type:   patch.PatchChangeBudget,
			Target: "knowledge.planner.max_results",
			Value:  newCfg.MaxResults,
			Reason: fmt.Sprintf("knowledge: MaxResults %d → %d", oldCfg.MaxResults, newCfg.MaxResults),
			Source: srcKnowledge,
		})
	}

	if oldCfg.ReducerStrategy != newCfg.ReducerStrategy {
		patches = append(patches, patch.RuntimePatch{
			Type:   patch.PatchChangeReducer,
			Target: "knowledge.planner.reducer",
			Value:  newCfg.ReducerStrategy,
			Reason: fmt.Sprintf("knowledge: Reducer %s → %s", oldCfg.ReducerStrategy, newCfg.ReducerStrategy),
			Source: srcKnowledge,
		})
	}

	if oldCfg.PlannerStrategy != newCfg.PlannerStrategy {
		patches = append(patches, patch.RuntimePatch{
			Type:   patch.PatchChangePlanner,
			Target: "knowledge.planner.strategy",
			Value:  newCfg.PlannerStrategy,
			Reason: fmt.Sprintf("knowledge: Planner %s → %s", oldCfg.PlannerStrategy, newCfg.PlannerStrategy),
			Source: srcKnowledge,
		})
	}

	if oldCfg.SummarizerType != newCfg.SummarizerType {
		patches = append(patches, patch.RuntimePatch{
			Type:   patch.PatchChangePlanner,
			Target: "knowledge.planner.summarizer",
			Value:  newCfg.SummarizerType,
			Reason: fmt.Sprintf("knowledge: Summarizer %s → %s", oldCfg.SummarizerType, newCfg.SummarizerType),
			Source: srcKnowledge,
		})
	}

	return patches, nil
}
