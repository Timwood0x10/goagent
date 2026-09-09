// Package graph provides dynamic agent orchestration with pluggable scheduling.
//
// It also includes Runtime Patch executors for the Evolution system.
package graph

import (
	"context"
	"errors"
	"fmt"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

// ── GraphPatchExecutor ──────────────────────

// GraphPatchExecutor handles DAG-related runtime patches.
// It wraps a *Graph and applies InsertNode/RemoveNode/ReplaceNode/AddEdge/RemoveEdge/ChangeScheduler.
// Implements patch.RuntimeComponent for unified runtime evolution.
type GraphPatchExecutor struct {
	graph *Graph
}

// NewGraphPatchExecutor creates a new GraphPatchExecutor.
func NewGraphPatchExecutor(g *Graph) *GraphPatchExecutor {
	return &GraphPatchExecutor{graph: g}
}

// Name returns "workflow.graph" as the component identifier for patch routing.
func (e *GraphPatchExecutor) Name() string { return "workflow.graph" }

// SetGraph replaces the executor's underlying graph reference with a live one.
// Called after agents are created so workflow/scheduler patches mutate the
// agent's real graph rather than the bootstrap placeholder. This mirrors
// RecoveryPatchExecutor.SetDAG: the executor is already registered on the
// patch registry, so it must be updated in place (Register cannot overwrite an
// already-registered component key).
func (e *GraphPatchExecutor) SetGraph(g *Graph) {
	if g == nil {
		return
	}
	e.graph = g
}

// Snapshot returns the current graph structure as a serializable snapshot.
func (e *GraphPatchExecutor) Snapshot(_ context.Context) (any, error) {
	if e.graph == nil {
		return nil, patch.ErrNoSnapshot
	}
	return e.graph, nil
}

// Ensure GraphPatchExecutor implements patch.RuntimeComponent.
var _ patch.RuntimeComponent = (*GraphPatchExecutor)(nil)

// Apply applies a runtime patch to the graph.
func (e *GraphPatchExecutor) Apply(ctx context.Context, p patch.RuntimePatch) (*patch.RuntimePatch, error) {
	if e.graph == nil {
		return nil, errors.New("graph executor: graph is nil (call SetGraph first)")
	}
	switch p.Type {
	case patch.PatchInsertNode:
		return e.applyInsertNode(ctx, p)
	case patch.PatchRemoveNode:
		return e.applyRemoveNode(ctx, p)
	case patch.PatchReplaceNode:
		return e.applyReplaceNode(ctx, p)
	case patch.PatchAddEdge:
		return e.applyAddEdge(ctx, p)
	case patch.PatchRemoveEdge:
		return e.applyRemoveEdge(ctx, p)
	case patch.PatchChangeScheduler:
		return e.applyChangeScheduler(ctx, p)
	default:
		return nil, fmt.Errorf("graph executor: unsupported patch type %s", p.Type)
	}
}

// CanApply checks whether a patch can be applied.
func (e *GraphPatchExecutor) CanApply(_ context.Context, p patch.RuntimePatch) error {
	if e.graph == nil {
		return errors.New("graph executor: graph is nil")
	}
	switch p.Type {
	case patch.PatchInsertNode:
		if p.Target == "" {
			return errors.New("graph executor: insert node requires non-empty target")
		}
		return nil
	case patch.PatchRemoveNode:
		if p.Target == "" {
			return errors.New("graph executor: remove node requires non-empty target")
		}
		return nil
	case patch.PatchReplaceNode:
		if p.Target == "" {
			return errors.New("graph executor: replace node requires non-empty target")
		}
		return nil
	case patch.PatchAddEdge:
		if p.Target == "" {
			return errors.New("graph executor: add edge requires non-empty from")
		}
		to, ok := p.Value.(string)
		if !ok || to == "" {
			return errors.New("graph executor: add edge value must be non-empty string (to)")
		}
		return nil
	case patch.PatchRemoveEdge:
		if p.Target == "" {
			return errors.New("graph executor: remove edge requires non-empty from")
		}
		to, ok := p.Value.(string)
		if !ok || to == "" {
			return errors.New("graph executor: remove edge value must be non-empty string (to)")
		}
		return nil
	case patch.PatchChangeScheduler:
		return nil
	default:
		return fmt.Errorf("graph executor: unsupported patch type %s", p.Type)
	}
}

// ── Apply implementations ───────────────────

func (e *GraphPatchExecutor) applyInsertNode(_ context.Context, p patch.RuntimePatch) (*patch.RuntimePatch, error) {
	// Determine the node to insert.
	var node Node
	if n, ok := p.Value.(Node); ok {
		node = n
	} else {
		// Create a FuncNode with the target ID.
		fn, err := NewFuncNode(p.Target, placeholderNodeExecute(p.Target))
		if err != nil {
			return nil, fmt.Errorf("graph executor: create func node: %w", err)
		}
		node = fn
	}

	// Capture the old node if it exists (for rollback).
	e.graph.mu.RLock()
	oldNode := e.graph.nodes[p.Target]
	e.graph.mu.RUnlock()

	_, err := e.graph.Node(p.Target, node)
	if err != nil {
		return nil, fmt.Errorf("graph executor: insert node %s: %w", p.Target, err)
	}

	return &patch.RuntimePatch{
		Type:   patch.PatchRemoveNode,
		Target: p.Target,
		Value:  oldNode,
		Reason: "rollback: remove inserted node",
	}, nil
}

func (e *GraphPatchExecutor) applyRemoveNode(_ context.Context, p patch.RuntimePatch) (*patch.RuntimePatch, error) {
	// Capture the node before removing (for rollback).
	e.graph.mu.RLock()
	oldNode, exists := e.graph.nodes[p.Target]
	e.graph.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("graph executor: node %q not found", p.Target)
	}

	_, err := e.graph.RemoveNode(p.Target)
	if err != nil {
		return nil, fmt.Errorf("graph executor: remove node %s: %w", p.Target, err)
	}

	return &patch.RuntimePatch{
		Type:   patch.PatchInsertNode,
		Target: p.Target,
		Value:  oldNode,
		Reason: "rollback: re-insert removed node",
	}, nil
}

func (e *GraphPatchExecutor) applyReplaceNode(_ context.Context, p patch.RuntimePatch) (*patch.RuntimePatch, error) {
	// Remove old node and insert new node in its place.
	e.graph.mu.RLock()
	oldNode, exists := e.graph.nodes[p.Target]
	e.graph.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("graph executor: node %q not found for replace", p.Target)
	}

	var newNode Node
	if n, ok := p.Value.(Node); ok {
		newNode = n
	} else {
		fn, err := NewFuncNode(p.Target, placeholderNodeExecute(p.Target))
		if err != nil {
			return nil, fmt.Errorf("graph executor: create replacement func node: %w", err)
		}
		newNode = fn
	}

	e.graph.mu.Lock()
	e.graph.nodes[p.Target] = newNode
	e.graph.mu.Unlock()

	return &patch.RuntimePatch{
		Type:   patch.PatchReplaceNode,
		Target: p.Target,
		Value:  oldNode,
		Reason: "rollback: restore replaced node",
	}, nil
}

func (e *GraphPatchExecutor) applyAddEdge(_ context.Context, p patch.RuntimePatch) (*patch.RuntimePatch, error) {
	to, ok := p.Value.(string)
	if !ok {
		return nil, errors.New("graph executor: add edge value must be string (to node ID)")
	}

	_, err := e.graph.Edge(p.Target, to)
	if err != nil {
		return nil, fmt.Errorf("graph executor: add edge %s→%s: %w", p.Target, to, err)
	}

	return &patch.RuntimePatch{
		Type:   patch.PatchRemoveEdge,
		Target: p.Target,
		Value:  to,
		Reason: "rollback: remove added edge",
	}, nil
}

func (e *GraphPatchExecutor) applyRemoveEdge(_ context.Context, p patch.RuntimePatch) (*patch.RuntimePatch, error) {
	to, ok := p.Value.(string)
	if !ok {
		return nil, errors.New("graph executor: remove edge value must be string (to node ID)")
	}

	_, err := e.graph.RemoveEdge(p.Target, to)
	if err != nil {
		return nil, fmt.Errorf("graph executor: remove edge %s→%s: %w", p.Target, to, err)
	}

	return &patch.RuntimePatch{
		Type:   patch.PatchAddEdge,
		Target: p.Target,
		Value:  to,
		Reason: "rollback: re-add removed edge",
	}, nil
}

func (e *GraphPatchExecutor) applyChangeScheduler(
	_ context.Context,
	p patch.RuntimePatch,
) (*patch.RuntimePatch, error) {
	newSched, ok := p.Value.(Scheduler)
	if !ok {
		return nil, errors.New("graph executor: change scheduler value must be a Scheduler")
	}

	// Capture old scheduler for rollback. e.graph.scheduler is guarded by
	// e.graph.mu (SetScheduler writes it under the write lock), so the read
	// must hold the read lock too — matching the pattern used by the sibling
	// apply* functions and avoiding a data race with concurrent SetScheduler.
	e.graph.mu.RLock()
	oldSched := e.graph.scheduler
	e.graph.mu.RUnlock()

	_, err := e.graph.SetScheduler(newSched)
	if err != nil {
		return nil, fmt.Errorf("graph executor: change scheduler: %w", err)
	}

	return &patch.RuntimePatch{
		Type:   patch.PatchChangeScheduler,
		Value:  oldSched,
		Reason: "rollback: restore previous scheduler",
	}, nil
}

// PlaceholderResult is the output written to state by structural placeholder
// nodes — evolution-inserted or replaced nodes that have no real tool/agent
// executor backing them. It explicitly signals that no real work was performed
// so downstream nodes and observers can distinguish a genuine no-op placeholder
// from a node that produced real output. This is NOT a fabricated success: the
// Placeholder flag and Reason make the absence of real work observable, which
// is the honest counterpart to the previous silent no-op that returned success
// doing nothing.
type PlaceholderResult struct {
	Placeholder bool   `json:"placeholder"`
	NodeID      string `json:"node_id"`
	Reason      string `json:"reason"`
}

// placeholderNodeExecute returns a FuncNode executor for evolution-inserted
// structural nodes that have no real tool/agent backing. Rather than silently
// returning success (which would pretend work was done), it writes a
// PlaceholderResult into state under "node.<id>" so the absence of real work is
// explicitly observable by callers. It returns nil so the DAG stays
// topologically valid for topology-only evolution; callers that need real
// output must later replace the node with a real executor via PatchReplaceNode.
func placeholderNodeExecute(nodeID string) func(context.Context, *State) error {
	return func(_ context.Context, state *State) error {
		if state == nil {
			return nil
		}
		state.Set("node."+nodeID, PlaceholderResult{
			Placeholder: true,
			NodeID:      nodeID,
			Reason:      "structural placeholder: no executor configured",
		})
		return nil
	}
}
