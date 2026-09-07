package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

// DAGSnapshot captures a deep copy of the whole live DAG so a structure patch
// batch can be reverted to the exact pre-apply topology. It is the rollback
// primitive returned by DAGPatchExecutor.Snapshot and consumed by Restore.
type DAGSnapshot struct {
	Steps []*Step
}

func cloneStepForSnapshot(s *Step) *Step {
	if s == nil {
		return nil
	}
	c := *s
	c.DependsOn = make([]string, len(s.DependsOn))
	copy(c.DependsOn, s.DependsOn)
	if s.RecoveryPolicy != nil {
		rp := *s.RecoveryPolicy
		c.RecoveryPolicy = &rp
	}
	if s.RetryPolicy != nil {
		rp := *s.RetryPolicy
		c.RetryPolicy = &rp
	}
	if s.Interrupt != nil {
		ic := *s.Interrupt
		c.Interrupt = &ic
	}
	if s.Metadata != nil {
		md := make(map[string]string, len(s.Metadata))
		for k, v := range s.Metadata {
			md[k] = v
		}
		c.Metadata = md
	}
	return &c
}

// DAGPatchExecutor is the LIVE-topology structure executor. It applies workflow
// structure patches (insert/remove/replace node, add/remove edge) directly to a
// *MutableDAG — the same object runtime.GetAgentDAG(AgentDAGLiveKey) returns.
//
// It is wired as the patch registry's fallback so a workflow patch no longer
// dies on "no executor registered for target <nodeID>"; instead it mutates the
// real live DAG. This is the closure the cross-graph fix (Step 7.1) unlocks:
// patches produced over the live topology are now applied to the live topology.
//
// Snapshot/Restore implement patch.Restorable so deployment rollback can revert
// the DAG to the captured topology in place (the *MutableDAG pointer stays
// stable, so the runtime manager, WorkflowGenome and the other executors all
// observe the restored graph).
type DAGPatchExecutor struct {
	dag *MutableDAG
}

// NewDAGPatchExecutor creates a structure executor bound to the given live DAG.
func NewDAGPatchExecutor(dag *MutableDAG) *DAGPatchExecutor {
	return &DAGPatchExecutor{dag: dag}
}

// Name returns the component identifier. Used only when registered by name;
// as a fallback it is not a routing key, but the RuntimeComponent contract
// requires it.
func (e *DAGPatchExecutor) Name() string { return "workflow.dag" }

// SetDAG rebinds the executor to a (possibly new) live DAG.
func (e *DAGPatchExecutor) SetDAG(dag *MutableDAG) {
	e.dag = dag
}

// DAG returns the currently bound DAG.
func (e *DAGPatchExecutor) DAG() *MutableDAG { return e.dag }

// Snapshot deep-copies the live DAG's steps. Implemented as patch.Restorable.
func (e *DAGPatchExecutor) Snapshot(_ context.Context) (any, error) {
	if e.dag == nil {
		return nil, errors.New("workflow.dag: dag is nil")
	}
	steps := e.dag.Steps()
	snap := &DAGSnapshot{Steps: make([]*Step, 0, len(steps))}
	for _, s := range steps {
		snap.Steps = append(snap.Steps, cloneStepForSnapshot(s))
	}
	return snap, nil
}

// Restore reverts the live DAG to a previously captured snapshot.
func (e *DAGPatchExecutor) Restore(_ context.Context, snap any) error {
	if e.dag == nil {
		return errors.New("workflow.dag: dag is nil")
	}
	s, ok := snap.(*DAGSnapshot)
	if !ok {
		return fmt.Errorf("workflow.dag restore: unsupported snapshot type %T", snap)
	}
	return e.dag.ResetFromSteps(s.Steps)
}

// CanApply reports which structure patch types this executor accepts.
func (e *DAGPatchExecutor) CanApply(_ context.Context, p patch.RuntimePatch) error {
	switch p.Type {
	case patch.PatchInsertNode, patch.PatchRemoveNode, patch.PatchReplaceNode,
		patch.PatchAddEdge, patch.PatchRemoveEdge, patch.PatchSetNodeMetadata:
		return nil
	default:
		return fmt.Errorf("workflow.dag: unsupported patch type %v on target %q", p.Type, p.Target)
	}
}

// Apply mutates the live DAG and returns an inverse patch for rollback.
func (e *DAGPatchExecutor) Apply(ctx context.Context, p patch.RuntimePatch) (*patch.RuntimePatch, error) {
	if e.dag == nil {
		return nil, errors.New("workflow.dag: dag is nil")
	}

	switch p.Type {
	case patch.PatchInsertNode:
		step, err := stepFromPatchValue(p.Value, p.Target)
		if err != nil {
			return nil, fmt.Errorf("workflow.dag insert %q: %w", p.Target, err)
		}
		step.ID = p.Target
		if err := e.dag.AddNode(ctx, step); err != nil {
			return nil, fmt.Errorf("workflow.dag insert %q: %w", p.Target, err)
		}
		return &patch.RuntimePatch{Type: patch.PatchRemoveNode, Target: p.Target}, nil

	case patch.PatchRemoveNode:
		if err := e.dag.RemoveNode(ctx, p.Target); err != nil {
			return nil, fmt.Errorf("workflow.dag remove %q: %w", p.Target, err)
		}
		return &patch.RuntimePatch{Type: patch.PatchInsertNode, Target: p.Target}, nil

	case patch.PatchReplaceNode:
		step, err := stepFromPatchValue(p.Value, p.Target)
		if err != nil {
			return nil, fmt.Errorf("workflow.dag replace %q: %w", p.Target, err)
		}
		var oldStep *Step
		if cur, ok := e.dag.StepIndex()[p.Target]; ok && cur != nil {
			oldStep = cloneStepForSnapshot(cur)
		}
		if err := e.dag.ReplaceNode(ctx, p.Target, step); err != nil {
			return nil, fmt.Errorf("workflow.dag replace %q: %w", p.Target, err)
		}
		return &patch.RuntimePatch{Type: patch.PatchReplaceNode, Target: p.Target, Value: oldStep}, nil

	case patch.PatchSetNodeMetadata:
		var md map[string]string
		switch v := p.Value.(type) {
		case map[string]string:
			md = v
		case *Step:
			md = v.Metadata
		case Step:
			md = v.Metadata
		default:
			return nil, fmt.Errorf("workflow.dag set-node-metadata %q: value %T is not a metadata map", p.Target, p.Value)
		}
		var old *Step
		if cur, ok := e.dag.StepIndex()[p.Target]; ok && cur != nil {
			old = cloneStepForSnapshot(cur)
		}
		if err := e.dag.SetNodeMetadata(p.Target, md); err != nil {
			return nil, fmt.Errorf("workflow.dag set-node-metadata %q: %w", p.Target, err)
		}
		return &patch.RuntimePatch{Type: patch.PatchSetNodeMetadata, Target: p.Target, Value: old}, nil

	case patch.PatchAddEdge:
		to, ok := p.Value.(string)
		if !ok {
			return nil, fmt.Errorf("workflow.dag add-edge %q: value %v is not a target node ID", p.Target, p.Value)
		}
		if err := e.dag.AddEdge(ctx, p.Target, to); err != nil {
			return nil, fmt.Errorf("workflow.dag add-edge %q->%q: %w", p.Target, to, err)
		}
		return &patch.RuntimePatch{Type: patch.PatchRemoveEdge, Target: p.Target, Value: to}, nil

	case patch.PatchRemoveEdge:
		to, ok := p.Value.(string)
		if !ok {
			return nil, fmt.Errorf("workflow.dag remove-edge %q: value %v is not a target node ID", p.Target, p.Value)
		}
		if err := e.dag.RemoveEdge(ctx, p.Target, to); err != nil {
			return nil, fmt.Errorf("workflow.dag remove-edge %q->%q: %w", p.Target, to, err)
		}
		return &patch.RuntimePatch{Type: patch.PatchAddEdge, Target: p.Target, Value: to}, nil
	}

	return nil, fmt.Errorf("workflow.dag: unsupported patch type %v on target %q", p.Type, p.Target)
}

// stepFromPatchValue extracts a *Step from a patch Value, tolerating both
// *Step and Step (from the workflow differ) and a bare node-ID string.
func stepFromPatchValue(v any, id string) (*Step, error) {
	switch val := v.(type) {
	case *Step:
		if val == nil {
			return &Step{ID: id}, nil
		}
		clone := cloneStepForSnapshot(val)
		clone.ID = id
		return clone, nil
	case Step:
		clone := cloneStepForSnapshot(&val)
		clone.ID = id
		return clone, nil
	case string:
		if val == "" {
			return &Step{ID: id}, nil
		}
		return &Step{ID: id, Name: val}, nil
	case nil:
		return &Step{ID: id}, nil
	default:
		return nil, fmt.Errorf("unsupported step value type %T", v)
	}
}
