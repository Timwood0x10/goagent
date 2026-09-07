package diff

import (
	"context"
	"fmt"

	"github.com/Timwood0x10/ares/internal/fabric/task/workflow/engine"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

// WorkflowDiffer computes DAG topology differences between two genome snapshots.
// Snapshots must be *engine.DAG values produced by WorkflowGenome.Snapshot().
type WorkflowDiffer struct{}

// NewWorkflowDiffer creates a new WorkflowDiffer.
func NewWorkflowDiffer() *WorkflowDiffer {
	return &WorkflowDiffer{}
}

// Name returns the differ identifier, matching the WorkflowGenome name.
func (d *WorkflowDiffer) Name() string { return genome.WorkflowGenomeName }

// Diff compares old and new DAG snapshots, producing DAG topology patches.
func (d *WorkflowDiffer) Diff(_ context.Context, old, new any) ([]patch.RuntimePatch, error) {
	oldDAG, ok := old.(*engine.DAG)
	if !ok {
		return nil, fmt.Errorf("workflow differ: old snapshot is %T, want *engine.DAG", old)
	}
	newDAG, ok := new.(*engine.DAG)
	if !ok {
		return nil, fmt.Errorf("workflow differ: new snapshot is %T, want *engine.DAG", new)
	}

	var patches []patch.RuntimePatch

	// Find inserted nodes.
	for nodeID, node := range newDAG.Nodes {
		if _, exists := oldDAG.Nodes[nodeID]; !exists {
			patches = append(patches, patch.RuntimePatch{
				Type:   patch.PatchInsertNode,
				Target: nodeID,
				Value:  node.StepID,
				Source: srcWorkflow,
			})
		}
	}

	// Find removed nodes.
	for nodeID := range oldDAG.Nodes {
		if _, exists := newDAG.Nodes[nodeID]; !exists {
			patches = append(patches, patch.RuntimePatch{
				Type:   patch.PatchRemoveNode,
				Target: nodeID,
				Source: srcWorkflow,
			})
		}
	}

	// Find added edges.
	for fromID, toIDs := range newDAG.Edges {
		oldToIDs := oldDAG.Edges[fromID]
		oldSet := toSet(oldToIDs)
		for _, toID := range toIDs {
			if !oldSet[toID] {
				patches = append(patches, patch.RuntimePatch{
					Type:   patch.PatchAddEdge,
					Target: fromID,
					Value:  toID,
					Source: srcWorkflow,
				})
			}
		}
	}

	// Find removed edges.
	for fromID, toIDs := range oldDAG.Edges {
		newToIDs := newDAG.Edges[fromID]
		newSet := toSet(newToIDs)
		for _, toID := range toIDs {
			if !newSet[toID] {
				patches = append(patches, patch.RuntimePatch{
					Type:   patch.PatchRemoveEdge,
					Target: fromID,
					Value:  toID,
					Source: srcWorkflow,
				})
			}
		}
	}

	// Find metadata-only changes on nodes that persist across the diff.
	// C4: a metadata change on a common node emits a PatchSetNodeMetadata (not a
	// ReplaceNode), so evolution can patch a ToolStep node's enabled/budget/prior
	// attributes WITHOUT restructuring the DAG. The differ compares the per-node
	// Metadata snapshot carried on DAGNode; the inverse (rollback) patch restores
	// the OLD step's metadata.
	for nodeID, newNode := range newDAG.Nodes {
		oldNode, exists := oldDAG.Nodes[nodeID]
		if !exists {
			continue // inserted; handled above
		}
		if !metadataEqual(oldNode.Metadata, newNode.Metadata) {
			patches = append(patches, patch.RuntimePatch{
				Type:   patch.PatchSetNodeMetadata,
				Target: nodeID,
				Value:  newNode.Metadata,
				Source: srcWorkflow,
			})
		}
	}

	return patches, nil
}

// metadataEqual reports whether two metadata maps are semantically equal.
func metadataEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// toSet converts a string slice to a set for O(1) lookup.
func toSet(ss []string) map[string]bool {
	set := make(map[string]bool, len(ss))
	for _, s := range ss {
		set[s] = true
	}
	return set
}
