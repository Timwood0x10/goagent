package agentfabric

import (
	"errors"
)

// ErrCognitiveStateSchemaVersion is returned by DecodeCognitiveState when the
// state's schema version is from a future version the current code cannot
// interpret. The caller must migrate the state or reject the agent recovery.
var ErrCognitiveStateSchemaVersion = errors.New("agentfabric: cognitive state schema version mismatch")

// DecodeCognitiveState decodes an agent's cognitive state through the single
// versioned path (A2). It accepts:
//
//   - CognitiveState / *CognitiveState: the native struct. A version greater
//     than CognitiveStateSchemaVersion returns ErrCognitiveStateSchemaVersion.
//     A zero version (legacy pre-A2 state) is accepted as compatible.
//   - map[string]any: a JSON-round-tripped state (e.g. after persistence and
//     reload). The version is read from "schema_version"; unknown keys are
//     left as raw values for the caller to reify.
//   - nil: a zero-valued state.
//
// Returns:
//   - CognitiveState: the decoded state.
//   - error: ErrCognitiveStateSchemaVersion for a future schema version.
func DecodeCognitiveState(v any) (CognitiveState, error) {
	switch c := v.(type) {
	case nil:
		return CognitiveState{}, nil
	case CognitiveState:
		return checkCognitiveVersion(c)
	case *CognitiveState:
		if c == nil {
			return CognitiveState{}, nil
		}
		return checkCognitiveVersion(*c)
	case map[string]any:
		return decodeCognitiveMap(c)
	default:
		// Unknown concrete type: treat as legacy opaque state.
		return CognitiveState{Context: v}, nil
	}
}

func checkCognitiveVersion(cs CognitiveState) (CognitiveState, error) {
	if cs.SchemaVersion > CognitiveStateSchemaVersion {
		return CognitiveState{}, ErrCognitiveStateSchemaVersion
	}
	return cs, nil
}

func decodeCognitiveMap(m map[string]any) (CognitiveState, error) {
	if sv, ok := m["schema_version"]; ok {
		version := 0
		switch v := sv.(type) {
		case float64:
			version = int(v)
		case int:
			version = v
		}
		if version > CognitiveStateSchemaVersion {
			return CognitiveState{}, ErrCognitiveStateSchemaVersion
		}
		cs := CognitiveState{SchemaVersion: version}
		if v, ok := m["context"]; ok {
			cs.Context = v
		}
		if v, ok := m["observation"]; ok {
			cs.Observation = v
		}
		if v, ok := m["working_memory"]; ok {
			cs.WorkingMemory = v
		}
		if v, ok := m["decision"]; ok {
			cs.Decision = v
		}
		if v, ok := m["tool_state"]; ok {
			cs.ToolState = v
		}
		if v, ok := m["checkpoint"]; ok {
			cs.Checkpoint = v
		}
		return cs, nil
	}
	// A map without a version key: legacy opaque state carried in Context.
	return CognitiveState{Context: m}, nil
}

// ContextLayer identifies the three context tiers (design §13: Context three
// layers — do not share one brain).
type ContextLayer int

const (
	// ContextTaskShared is the shared task state (goal / constraints /
	// artifacts / decisions / dependencies / checkpoints). Objective; all
	// agents working on the task must see it.
	ContextTaskShared ContextLayer = iota
	// ContextAgentPrivate is the agent's private state (reasoning /
	// observations / hypotheses / scratchpad). Per-agent; NEVER leaks to
	// other agents or the task layer.
	ContextAgentPrivate
	// ContextIPC is the message channel between agents ("I found X" /
	// "help me verify Y" / "your conclusion conflicts with mine"). Handled
	// by the IPC pillar (P4); this layer is the storage surface.
	ContextIPC
)

// ContextView is a read-only snapshot of one agent's three-layer context.
// It is used to verify isolation (Private never bleeds into Task).
type ContextView struct {
	TaskShared map[string]any
	Private    map[string]any
}

// SetTaskContext replaces the agent's Task Shared State. Called by the
// Scheduler/Runtime when binding a Task to the agent. The agent receives a
// copy so it never mutates the caller's map.
func (f *Fabric) SetTaskContext(agentID string, taskCtx map[string]any) error {
	f.mu.Lock()
	a, ok := f.agents[agentID]
	if !ok {
		f.mu.Unlock()
		return ErrAgentNotFound
	}
	a.mu.Lock()
	a.taskContext = cloneMap(taskCtx)
	a.mu.Unlock()
	f.mu.Unlock()
	return nil
}

// TaskContext returns a copy of the agent's Task Shared State.
func (f *Fabric) TaskContext(agentID string) (map[string]any, error) {
	f.mu.Lock()
	a, ok := f.agents[agentID]
	if !ok {
		f.mu.Unlock()
		return nil, ErrAgentNotFound
	}
	a.mu.RLock()
	out := cloneMap(a.taskContext)
	a.mu.RUnlock()
	f.mu.Unlock()
	return out, nil
}

// SetPrivate stores a key in the agent's Private State (scratchpad). This
// layer NEVER leaks to the Task Shared State or to other agents (§13
// invariant #5 + #6).
func (f *Fabric) SetPrivate(agentID, key string, val any) error {
	f.mu.Lock()
	a, ok := f.agents[agentID]
	if !ok {
		f.mu.Unlock()
		return ErrAgentNotFound
	}
	a.mu.Lock()
	a.privateContext[key] = val
	a.mu.Unlock()
	f.mu.Unlock()
	return nil
}

// Private returns a value from the agent's Private State.
func (f *Fabric) Private(agentID, key string) (any, error) {
	f.mu.Lock()
	a, ok := f.agents[agentID]
	if !ok {
		f.mu.Unlock()
		return nil, ErrAgentNotFound
	}
	a.mu.RLock()
	val := a.privateContext[key]
	a.mu.RUnlock()
	f.mu.Unlock()
	return val, nil
}

// ContextView returns a snapshot of the agent's Task Shared + Private
// layers (IPC is P4). Used to verify isolation: Private must not appear in
// TaskShared.
func (f *Fabric) ContextView(agentID string) (ContextView, error) {
	f.mu.Lock()
	a, ok := f.agents[agentID]
	if !ok {
		f.mu.Unlock()
		return ContextView{}, ErrAgentNotFound
	}
	a.mu.RLock()
	v := ContextView{
		TaskShared: cloneMap(a.taskContext),
		Private:    cloneMap(a.privateContext),
	}
	a.mu.RUnlock()
	f.mu.Unlock()
	return v, nil
}

// CognitiveState returns a copy of the agent's cognitive state.
func (f *Fabric) CognitiveState(agentID string) (CognitiveState, error) {
	f.mu.Lock()
	a, ok := f.agents[agentID]
	if !ok {
		f.mu.Unlock()
		return CognitiveState{}, ErrAgentNotFound
	}
	a.mu.RLock()
	cs := a.cognitive
	a.mu.RUnlock()
	f.mu.Unlock()
	return cs, nil
}

// SetCognitiveState replaces the agent's cognitive state (used by Recover
// and by the agent itself to checkpoint progress). A legacy state written
// without a SchemaVersion (pre-A2 zero value) is upgraded to the current
// version at the boundary, so every stored state carries a version.
func (f *Fabric) SetCognitiveState(agentID string, cs CognitiveState) error {
	f.mu.Lock()
	a, ok := f.agents[agentID]
	if !ok {
		f.mu.Unlock()
		return ErrAgentNotFound
	}
	if cs.SchemaVersion == 0 {
		cs.SchemaVersion = CognitiveStateSchemaVersion
	}
	a.mu.Lock()
	a.cognitive = cs
	a.mu.Unlock()
	f.mu.Unlock()
	return nil
}

// CheckpointCognitive returns a snapshot of the agent's cognitive state for
// durable storage (the Runtime does NOT depend on hidden CoT — only on this
// checkpointable state; §13 invariant #5). The snapshot is a copy: mutating
// it does not affect the live agent.
func (f *Fabric) CheckpointCognitive(agentID string) (CognitiveState, error) {
	return f.CognitiveState(agentID)
}

// cloneMap returns a shallow copy of m (nil → empty map). The copy is safe
// for the caller to mutate without affecting the source.
func cloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
