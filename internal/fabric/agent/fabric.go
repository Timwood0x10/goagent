package agentfabric

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Fabric owns the Agent registry, Process Tree, and lifecycle primitives.
// It is the Kernel's Lifecycle pillar: spawn / suspend /
// resume / retire / kill / recover. It does NOT schedule (that is
// taskfabric's job) and does NOT do IPC (that is P4).
//
// All state transitions are serialized under mu. Lifecycle events are emitted
// via the optional EventSink so the Runtime can rebuild agent state from the
// event log (Evidence-Driven).
type Fabric struct {
	mu       sync.Mutex // guards agents, children, idSeq, resourceBudget, allocated
	agents   map[string]*Agent
	children map[string][]string // parentID -> childIDs (Process Tree; provenance only)
	idSeq    int
	now      func() time.Time // injectable clock for deterministic tests
	sink     EventSink        // optional lifecycle event sink (nil = in-memory only)

	// resourceBudget is the P5 resource quota (name → max total across live
	// agents); nil/empty disables admission control. allocated tracks the
	// currently claimed amounts. Both are guarded by mu.
	resourceBudget map[string]float64
	allocated      map[string]float64

	// snapshots keeps the last cognitive snapshot of agents that died via
	// Kill, captured BEFORE the registry entry is removed (after which the
	// agent is unreadable). Guarded by its own lock inside snapshotStore.
	// Fusion plan Phase A1: agent-level cognitive resurrection.
	snapshots *snapshotStore
}

// EventSink receives lifecycle events. Implementations may persist them
// (e.g. ares_events.EventStore) for cross-restart rebuild.
type EventSink interface {
	// Emit appends one lifecycle event.
	Emit(ctx context.Context, ev AgentEvent) error
}

// AgentEvent is one immutable lifecycle record (design §7: full state
// rebuild from the event stream).
type AgentEvent struct {
	Type     AgentEventType
	AgentID  string
	ParentID string
	State    AgentState
	At       time.Time
	Payload  map[string]any
}

// AgentEventType enumerates the agent lifecycle events.
type AgentEventType string

const (
	EventAgentSpawned   AgentEventType = "agent.spawned"
	EventAgentSuspended AgentEventType = "agent.suspended"
	EventAgentResumed   AgentEventType = "agent.resumed"
	EventAgentRetired   AgentEventType = "agent.retired"
	EventAgentKilled    AgentEventType = "agent.killed"
	EventAgentRecovered AgentEventType = "agent.recovered"
)

// NewFabric creates an empty Agent Fabric.
func NewFabric() *Fabric {
	return &Fabric{
		agents:         make(map[string]*Agent),
		children:       make(map[string][]string),
		now:            time.Now,
		resourceBudget: make(map[string]float64),
		allocated:      make(map[string]float64),
		snapshots:      newSnapshotStore(),
	}
}

// WithEventSink wires a lifecycle event sink. Nil detaches. Returns the
// Fabric for chaining.
func (f *Fabric) WithEventSink(sink EventSink) *Fabric {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sink = sink
	return f
}

// WithClock injects a controllable clock for deterministic tests.
func (f *Fabric) WithClock(now func() time.Time) *Fabric {
	f.mu.Lock()
	defer f.mu.Unlock()
	if now != nil {
		f.now = now
	}
	return f
}

// Get returns a snapshot of an agent (ErrAgentNotFound when unknown).
func (f *Fabric) Get(agentID string) (*Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agents[agentID]
	if !ok {
		return nil, ErrAgentNotFound
	}
	return a, nil
}

// Agents returns the sorted list of registered agent IDs.
func (f *Fabric) Agents() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.agents))
	for id := range f.agents {
		out = append(out, id)
	}
	// stable order for tests
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// IsIdle reports whether agentID is currently IDLE (schedulable). It is the
// thread-safe scheduling view of Agent.State: the scheduler reads it from
// drain goroutines without holding the agent's internal lock. Unknown or
// non-IDLE agents report false (B1: 候选 = StateIdle 且 capability 匹配).
func (f *Fabric) IsIdle(agentID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agents[agentID]
	if !ok {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.State == StateIdle
}

// Children returns the child agent IDs of a parent (Process Tree: spawn
// causality). This is PROVENANCE ONLY — it does NOT imply a permission
// hierarchy (§13 invariant #1: A ≡ B ≡ C). Returns nil for a leaf or unknown
// agent.
func (f *Fabric) Children(parentID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	kids := f.children[parentID]
	out := make([]string, len(kids))
	copy(out, kids)
	return out
}

// nextIDLocked generates a unique agent id. Caller must hold f.mu.
func (f *Fabric) nextIDLocked() string {
	f.idSeq++
	return fmt.Sprintf("agent-%d", f.idSeq)
}

// record emits a lifecycle event to the sink (best-effort; a failed emit
// never breaks the state machine — the in-memory registry is authoritative).
func (f *Fabric) record(ctx context.Context, a *Agent, typ AgentEventType, payload map[string]any) {
	if f.sink == nil {
		return
	}
	ev := AgentEvent{
		Type:     typ,
		AgentID:  a.Identity,
		ParentID: a.Parent,
		State:    a.State,
		At:       f.now(),
		Payload:  payload,
	}
	_ = f.sink.Emit(ctx, ev) // best-effort
}

// AgentView is a read-only copy of an agent's scheduling-relevant state —
// the runtime introspection panel's Domain C row (monitoring.md §2.2).
// Named AgentView to avoid clashing with the revival-record AgentSnapshot.
type AgentView struct {
	// Identity is the stable agent identifier.
	Identity string `json:"identity"`
	// State is the lifecycle state (IDLE/RUNNING/SUSPENDED/RETIRED).
	State AgentState `json:"state"`
	// Capabilities are the declared capabilities (copied).
	Capabilities []string `json:"capabilities"`
	// Load / Confidence / Priority mirror the scheduler hints.
	Load       float64 `json:"load"`
	Confidence float64 `json:"confidence"`
	Priority   float64 `json:"priority"`
	// Parent is the spawning agent ("" = root); provenance only.
	Parent string `json:"parent"`
	// SpawnedAt is the creation time.
	SpawnedAt time.Time `json:"spawnedAt"`
}

// AgentsView returns a point-in-time copy of every registered agent, sorted
// by Identity. Everything is copied under f.mu so callers can render or
// serialize without racing lifecycle transitions — unlike Fabric.Get, which
// hands out the live pointer and must not be read lock-free.
func (f *Fabric) AgentsView() []AgentView {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]AgentView, 0, len(f.agents))
	for _, a := range f.agents {
		v := AgentView{
			Identity:   a.Identity,
			State:      a.State,
			Load:       a.Load,
			Confidence: a.Confidence,
			Priority:   a.Priority,
			Parent:     a.Parent,
			SpawnedAt:  a.SpawnedAt,
		}
		if len(a.Capabilities) > 0 {
			v.Capabilities = append([]string(nil), a.Capabilities...)
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Identity < out[j].Identity })
	return out
}
