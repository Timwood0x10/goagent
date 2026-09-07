package agentfabric

import (
	"context"
	"fmt"
)

// SpawnSpec is the syscall-style spawn request (design §13: spawn is a
// syscall, not an orchestration API). The Kernel validates quota / capability
// / resource / policy, then creates the Agent + (optionally) a Task + the
// parent-child provenance link.
type SpawnSpec struct {
	// Identity is the requested agent id; "" means the Fabric assigns one.
	Identity string
	// Capabilities are the declared capabilities of the new agent.
	Capabilities []string
	// ParentID is the spawning agent's id ("" for a root agent).
	ParentID string
	// TaskContext is the shared task state passed from the parent (a
	// snapshot or selected projection — never the parent's private state).
	TaskContext map[string]any
	// Resources are resource hints (quota/capability/policy validation
	// surface; P3 stores them opaquely, full enforcement is P5).
	Resources map[string]any
	// Governance is the P3 cognitive-execution budget (token/tool/deadline).
	// Zero values mean unlimited. Set here so the Kernel admits the agent with
	// its budgets from birth.
	Governance Governance
	// Priority is the scheduling priority of the new agent (>= 0; 0 =
	// normal). It mirrors OS-thread priority: the taskfabric scheduler boosts
	// higher-priority agents when choosing among capable candidates.
	Priority float64
	// CognitionFactory produces the agent's execution body (Cognition) from
	// its declared capabilities. When nil, the spawned agent has no execution
	// capability and cannot run a quantum (it can still be managed by
	// lifecycle operations). The factory is called once at spawn time, under
	// the fabric lock, and the result is stored in Agent.cognition.
	// (A1: Unified Injection of Execution Capabilities Agent.)
	CognitionFactory CognitionFactory
	// ExperiencePrior is the distilled prior experience (aresos-agentos-plan
	// G1: Memory Distill Hook into the agent lifecycle) loaded as the agent's initial
	// cognitive context at spawn time. It is written into
	// CognitiveState.Context so the agent starts with relevant distilled
	// experience instead of a blank slate. Nil = no prior (zero-value usable).
	ExperiencePrior any
}

// Spawn is the Kernel syscall that creates a new Agent (design §13: spawn
// establishes provenance, NOT hierarchy). The new agent is a same-level
// cognitive process (A ≡ B ≡ C): it can compete with its parent for tasks,
// communicate as a peer, and survive its parent's death. The parent-child
// link is recorded in the Process Tree for provenance/Lifecycle only — it
// does NOT form a permission hierarchy.
//
// The Kernel validates the spec (non-empty capabilities when a parent is
// present; non-duplicate id) and the resource quota (P5: a spawn whose
// requested resources exceed the remaining budget is rejected with
// ErrResourceQuotaExceeded — the claim is recorded and released on
// kill/retire) before creating the agent. Spawn does NOT schedule the new
// agent — that is the Scheduler's job.
//
// Args:
//   - ctx: for the event sink.
//   - spec: the spawn request.
//
// Returns:
//   - *Agent: the newly created agent in StateIdle.
//   - error: ErrAgentExists / ErrInvalidSpawnSpec / ErrResourceQuotaExceeded.
func (f *Fabric) Spawn(ctx context.Context, spec SpawnSpec) (*Agent, error) {
	if err := validateSpawnSpec(spec); err != nil {
		return nil, err
	}
	claim := parseResourceClaim(spec.Resources)
	f.mu.Lock()
	id := spec.Identity
	if id == "" {
		id = f.nextIDLocked()
	}
	if _, exists := f.agents[id]; exists {
		f.mu.Unlock()
		return nil, ErrAgentExists
	}
	// P5 resource admission: reject before mutating any state so a failed
	// spawn leaves the fabric untouched (code_rules: validate first,
	// then mutate).
	if !f.canAllocateLocked(claim) {
		f.mu.Unlock()
		return nil, ErrResourceQuotaExceeded
	}
	a := &Agent{
		Identity:       id,
		Capabilities:   append([]string(nil), spec.Capabilities...),
		State:          StateIdle,
		Parent:         spec.ParentID,
		Priority:       spec.Priority,
		SpawnedAt:      f.now(),
		resources:      claim,
		taskContext:    cloneMap(spec.TaskContext),
		privateContext: make(map[string]any),
	}
	// A1: inject the execution body from the declared capabilities. The
	// factory is called under the fabric lock; a nil factory leaves the agent
	// without execution capability (managed but not schedulable). A NON-nil
	// factory that produces nil is a programming error: it would silently
	// spawn a permanently non-executable agent, so it is rejected before any
	// fabric state is mutated (N10: nil cognition was swallowed).
	if spec.CognitionFactory != nil {
		a.cognition = spec.CognitionFactory(spec.Capabilities)
		if a.cognition == nil {
			f.mu.Unlock()
			return nil, fmt.Errorf("%w: CognitionFactory returned nil for agent %q", ErrInvalidSpawnSpec, id)
		}
	}
	// G1: load the distilled prior experience as the agent's initial cognitive
	// context so a spawned agent starts with reusable experience instead of a
	// blank slate. Nil (zero value) leaves the agent with an empty cognitive
	// state.
	if spec.ExperiencePrior != nil {
		a.cognitive = CognitiveState{
			SchemaVersion: CognitiveStateSchemaVersion,
			Context:       spec.ExperiencePrior,
		}
	}
	// P3 governance: every agent carries a budget state from birth. A
	// zero-value Governance means "unlimited" for all dimensions (the default
	// for legacy agents), so ConsumeResource never fails with
	// ErrAgentNotGoverned — it only fails when a non-zero budget is exceeded.
	gov := &governanceState{cfg: spec.Governance}
	if spec.Governance.Deadline > 0 {
		gov.deadline = f.now().Add(spec.Governance.Deadline)
	}
	a.governance = gov
	f.agents[id] = a
	f.allocateLocked(claim)
	if spec.ParentID != "" {
		f.children[spec.ParentID] = append(f.children[spec.ParentID], id)
	}
	f.mu.Unlock()
	f.record(ctx, a, EventAgentSpawned, map[string]any{
		"parent_id":    spec.ParentID,
		"capabilities": spec.Capabilities,
		"resources":    spec.Resources,
	})
	return a, nil
}

// validateSpawnSpec checks the spec is well-formed.
func validateSpawnSpec(spec SpawnSpec) error {
	// ParentID "" is fine (root agent). Identity "" is fine (auto-assigned).
	// A spec with an explicit id must not conflict — checked under mu in Spawn.
	if spec.Identity != "" {
		// reserved/id format check: no spaces.
		for _, r := range spec.Identity {
			if r == ' ' {
				return ErrInvalidSpawnSpec
			}
		}
	}
	return nil
}

// Suspend pauses an IDLE or RUNNING agent (Lifecycle, not Task). The agent's
// in-memory state is preserved; Resume relaunches the SAME instance. A
// retired agent cannot be suspended.
//
// Args:
//   - ctx: for the event sink.
//   - agentID: the agent to suspend.
//
// Returns:
//   - error: ErrAgentNotFound / ErrAgentRetired / ErrAgentNotSuspended.
func (f *Fabric) Suspend(ctx context.Context, agentID string) error {
	f.mu.Lock()
	a, ok := f.agents[agentID]
	if !ok {
		f.mu.Unlock()
		return ErrAgentNotFound
	}
	if a.State == StateRetired {
		f.mu.Unlock()
		return ErrAgentRetired
	}
	if a.State == StateSuspended {
		f.mu.Unlock()
		return nil // idempotent
	}
	a.mu.Lock()
	a.State = StateSuspended
	a.mu.Unlock()
	f.mu.Unlock()
	f.record(ctx, a, EventAgentSuspended, nil)
	return nil
}

// Resume relaunches a previously suspended agent. It is a no-op for agents
// that are not suspended. A retired agent cannot be resumed.
//
// Args:
//   - ctx: for the event sink.
//   - agentID: the agent to resume.
//
// Returns:
//   - error: ErrAgentNotFound / ErrAgentRetired / ErrAgentNotSuspended.
func (f *Fabric) Resume(ctx context.Context, agentID string) error {
	f.mu.Lock()
	a, ok := f.agents[agentID]
	if !ok {
		f.mu.Unlock()
		return ErrAgentNotFound
	}
	if a.State == StateRetired {
		f.mu.Unlock()
		return ErrAgentRetired
	}
	if a.State != StateSuspended {
		f.mu.Unlock()
		return ErrAgentNotSuspended
	}
	a.mu.Lock()
	a.State = StateIdle
	a.mu.Unlock()
	f.mu.Unlock()
	f.record(ctx, a, EventAgentResumed, nil)
	return nil
}

// Retire permanently decommissions an agent (graceful). The agent must NOT be
// RUNNING — suspend it first. A retired agent cannot be resumed; its in-flight
// tasks (if any) are reclaimed by the Runtime (P5 Recovery). Retiring a parent
// does NOT kill its children (§13 invariant #1: parent death ≠ child death).
// The agent's resource claim is released back to the quota.
//
// Args:
//   - ctx: for the event sink.
//   - agentID: the agent to retire.
//
// Returns:
//   - error: ErrAgentNotFound / ErrAgentRunning / ErrAgentRetired.
func (f *Fabric) Retire(ctx context.Context, agentID string) error {
	f.mu.Lock()
	a, ok := f.agents[agentID]
	if !ok {
		f.mu.Unlock()
		return ErrAgentNotFound
	}
	if a.State == StateRetired {
		f.mu.Unlock()
		return nil // idempotent
	}
	if a.State == StateRunning {
		f.mu.Unlock()
		return ErrAgentRunning
	}
	f.releaseLocked(a.resources)
	a.resources = nil
	a.mu.Lock()
	a.State = StateRetired
	a.mu.Unlock()
	// A1: Retire is terminal — any death snapshot from an earlier kill/revive
	// cycle of this identity must not resurrect later.
	f.snapshots.clear(agentID)
	f.mu.Unlock()
	f.record(ctx, a, EventAgentRetired, nil)
	return nil
}

// Kill forcefully terminates an agent (non-graceful; e.g. crash). Unlike
// Retire, Kill works on any state and is the crash path. The agent entry is
// removed from the registry, but its children survive (§13: Parent 死 ≠
// Child 死). Children's Parent field is NOT cleared — it stays as
// provenance. Task reclaim is P5 Recovery. The agent's resource claim is
// released back to the quota.
//
// Args:
//   - ctx: for the event sink.
//   - agentID: the agent to kill.
//
// Returns:
//   - error: ErrAgentNotFound.
func (f *Fabric) Kill(ctx context.Context, agentID string) error {
	f.mu.Lock()
	a, ok := f.agents[agentID]
	if !ok {
		f.mu.Unlock()
		return ErrAgentNotFound
	}
	// A1: capture the revival record BEFORE the registry entry disappears —
	// after this delete the agent is unreadable, so the recovery subsystem's
	// in-place-revival decision depends on this snapshot existing.
	snap := captureFromAgent(a, f.now())
	delete(f.agents, agentID)
	f.releaseLocked(a.resources)
	a.resources = nil
	// NOTE: children of a killed agent survive. We do NOT clear their Parent
	// field — it remains as provenance. The Process Tree edge is preserved
	// in f.children so the parent's causal descendants are still discoverable
	// even after the parent is gone (§13 invariant #1 + #7).
	f.mu.Unlock()
	f.snapshots.save(agentID, snap)
	f.record(ctx, a, EventAgentKilled, nil)
	return nil
}

// Recover restores an agent's state from a checkpoint (design §13: cognitive
// state can independently survive). The agent must exist and be IDLE or
// SUSPENDED. Recover replaces the agent's cognitive state with the provided
// checkpoint — this is how a new Agent resumes a dead one's cognition (§13
// invariant #2: Agent disposable, Task durable; a new agent picks up the
// cognitive checkpoint).
//
// Args:
//   - ctx: for the event sink.
//   - agentID: the agent to recover into.
//   - cognitive: the recovered cognitive state.
//
// Returns:
//   - error: ErrAgentNotFound / ErrAgentRetired.
func (f *Fabric) Recover(ctx context.Context, agentID string, cognitive CognitiveState) error {
	f.mu.Lock()
	a, ok := f.agents[agentID]
	if !ok {
		f.mu.Unlock()
		return ErrAgentNotFound
	}
	if a.State == StateRetired {
		f.mu.Unlock()
		return ErrAgentRetired
	}
	a.mu.Lock()
	if cognitive.SchemaVersion == 0 {
		cognitive.SchemaVersion = CognitiveStateSchemaVersion
	}
	a.cognitive = cognitive
	if a.State == StateSuspended {
		a.State = StateIdle
	}
	a.mu.Unlock()
	f.mu.Unlock()
	f.record(ctx, a, EventAgentRecovered, map[string]any{
		"has_checkpoint": cognitive.Checkpoint != nil,
	})
	return nil
}

// SetRunning marks an agent as RUNNING (called by the Scheduler when it
// binds a Task to the agent). Internal: not a public lifecycle primitive.
func (f *Fabric) SetRunning(agentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agents[agentID]
	if !ok {
		return ErrAgentNotFound
	}
	if a.State == StateRetired {
		return ErrAgentRetired
	}
	a.mu.Lock()
	a.State = StateRunning
	a.mu.Unlock()
	return nil
}

// SetIdle marks an agent as IDLE (called by the Scheduler when a Task yields
// or completes). Internal: not a public lifecycle primitive.
func (f *Fabric) SetIdle(agentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agents[agentID]
	if !ok {
		return ErrAgentNotFound
	}
	if a.State == StateRetired {
		return ErrAgentRetired
	}
	a.mu.Lock()
	a.State = StateIdle
	a.mu.Unlock()
	return nil
}
