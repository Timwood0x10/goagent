// Package patch provides the universal mutation language for ARES Runtime.
//
// Every subsystem (GA, Chaos, LLM, Human, K8s Operator) outputs RuntimePatch.
// Runtime applies them via the Apply function.
// If Apply fails, the automatic rollback undoes the change.
//
// Everything evolves by emitting Runtime Patches.
package patch

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// PatchType classifies a runtime mutation.
type PatchType int

const (
	// ── DAG mutations ──────────────────────────────────
	PatchInsertNode  PatchType = iota // Insert a new node into the DAG
	PatchRemoveNode                   // Remove a node from the DAG
	PatchReplaceNode                  // Replace a node with another
	PatchAddEdge                      // Add a directed edge between nodes
	PatchRemoveEdge                   // Remove a directed edge

	// ── Scheduler mutations ────────────────────────────
	PatchChangeScheduler // Replace the current scheduler

	// ── Knowledge/Planner mutations ────────────────────
	PatchChangePlanner // Change planner strategy
	PatchChangeReducer // Change reducer strategy
	PatchChangeBudget  // Change knowledge budget (e.g. TopK)

	// ── Recovery mutations ────────────────────────────
	PatchChangeRecoveryStrategy // Change recovery strategy (retry/replace/fail)
	PatchChangeMaxRetries       // Change max retry count
	PatchChangeBackoff          // Change backoff duration

	// ── Agent role mutations ──────────────────────────
	PatchChangeInstruction // Change an AgentProfile.Instructions (candidate evolution)

	// ── Node attribute mutations ─────────────────────
	// PatchSetNodeMetadata updates a single live-DAG node's Metadata map
	// (Y1 方案C C4). This is the "作动面" for a ToolStep node: enabled/budget/
	// prior are node attributes the evolution can patch without restructuring
	// the DAG. The differ emits this only for metadata-only changes; a DAG used
	// to produce ZERO patches for those (WorkflowDiffer only compared node/edge
	// presence), so a metadata-only gene mutation was invisible to evolution.
	PatchSetNodeMetadata
)

// String returns a human-readable name for the patch type.
func (pt PatchType) String() string {
	switch pt {
	case PatchInsertNode:
		return "insert_node"
	case PatchRemoveNode:
		return "remove_node"
	case PatchReplaceNode:
		return "replace_node"
	case PatchAddEdge:
		return "add_edge"
	case PatchRemoveEdge:
		return "remove_edge"
	case PatchChangeScheduler:
		return "change_scheduler"
	case PatchChangePlanner:
		return "change_planner"
	case PatchChangeReducer:
		return "change_reducer"
	case PatchChangeBudget:
		return "change_budget"
	case PatchChangeRecoveryStrategy:
		return "change_recovery_strategy"
	case PatchChangeMaxRetries:
		return "change_max_retries"
	case PatchChangeBackoff:
		return "change_backoff"
	case PatchChangeInstruction:
		return "change_instruction"
	case PatchSetNodeMetadata:
		return "set_node_metadata"
	default:
		return fmt.Sprintf("unknown(%d)", int(pt))
	}
}

// RuntimePatch is the universal mutation unit.
// Source identifies who proposed it (genome / chaos / llm / human / k8s).
// If Rollback is non-nil, Runtime can undo the patch on failure.
// ID must be unique for idempotency tracking — Registry skips already-applied IDs.
type RuntimePatch struct {
	ID     string    `json:"id,omitempty"`    // unique idempotency key (optional; empty = no dedup)
	Type   PatchType `json:"type"`            // what to change
	Target string    `json:"target"`          // what to change (node ID / component name)
	Value  any       `json:"value,omitempty"` // what to become (new Node / Scheduler / Config)
	Reason string    `json:"reason,omitempty"`
	// Source is the PROPOSER CLASS (e.g. "diff.memory", "candidate", "ga"),
	// NOT a strategy identifier. Do not use it to key per-strategy evidence
	// queries — those namespaces are disjoint and a lookup by Source always
	// misses, silently degrading any A/B comparison to a cold-start default.
	Source string `json:"source,omitempty"`
	// StrategyID attributes this patch to a mutation.Strategy so evidence
	// queries can score the patch's own strategy against the active one.
	// Empty means "no strategy attribution" — deployment pipelines MUST NOT
	// invent one, because an unattributable patch cannot be A/B compared.
	StrategyID string        `json:"strategy_id,omitempty"`
	Rollback   *RuntimePatch `json:"rollback,omitempty"` // inverse patch for rollback
}

// PatchSet is an atomic batch of patches.
// All patches are applied in order; if any fails, all are rolled back.
type PatchSet struct {
	Patches []RuntimePatch `json:"patches"`
	Reason  string         `json:"reason"`           // why this batch was proposed
	Source  string         `json:"source,omitempty"` // batch source
}

// Executor applies a RuntimePatch to a specific subsystem.
// Each subsystem (DAG, Scheduler, Planner, Recovery) implements this interface.
type Executor interface {
	// Apply applies the patch and returns a rollback patch.
	// If the patch cannot be applied, Apply returns an error.
	// The rollback patch can be used to undo the change.
	Apply(ctx context.Context, patch RuntimePatch) (*RuntimePatch, error)

	// CanApply returns nil if the patch can be applied, or an error explaining why not.
	CanApply(ctx context.Context, patch RuntimePatch) error
}

// RuntimeComponent is the unified interface for all evolvable runtime subsystems.
// It extends Executor with Name and Snapshot, enabling the Coordinator to discover
// and snapshot any component without knowing its concrete type.
//
// Every subsystem (DAG, Scheduler, Planner, Knowledge, Recovery) implements this
// interface to participate in runtime evolution.
type RuntimeComponent interface {
	// Name returns the component identifier, used for registry lookup.
	Name() string

	// Snapshot returns a serializable representation of the component's current
	// state. Used by Diff Engine to compute changes between generations.
	Snapshot(ctx context.Context) (any, error)

	// Apply applies the patch and returns a rollback patch.
	// If the patch cannot be applied, Apply returns an error.
	// The rollback patch can be used to undo the change.
	Apply(ctx context.Context, patch RuntimePatch) (*RuntimePatch, error)

	// CanApply returns nil if the patch can be applied, or an error explaining why not.
	CanApply(ctx context.Context, patch RuntimePatch) error
}

// ExecutorComponent wraps an Executor as a RuntimeComponent.
// This adapter allows existing Executor implementations to participate in the
// RuntimeComponent ecosystem without immediate migration.
// Snapshot returns (nil, nil) by default — concrete implementations should
// override by implementing RuntimeComponent directly.
type ExecutorComponent struct {
	name     string
	executor Executor
}

// NewExecutorComponent creates a RuntimeComponent adapter from an Executor.
func NewExecutorComponent(name string, ex Executor) *ExecutorComponent {
	return &ExecutorComponent{name: name, executor: ex}
}

// Name returns the component name passed at construction time.
func (c *ExecutorComponent) Name() string { return c.name }

// Snapshot returns a nil snapshot. Components that support diffing should
// implement RuntimeComponent directly instead of using this adapter.
func (c *ExecutorComponent) Snapshot(_ context.Context) (any, error) { return nil, ErrNoSnapshot }

// Apply delegates to the wrapped Executor.
func (c *ExecutorComponent) Apply(ctx context.Context, patch RuntimePatch) (*RuntimePatch, error) {
	return c.executor.Apply(ctx, patch)
}

// CanApply delegates to the wrapped Executor.
func (c *ExecutorComponent) CanApply(ctx context.Context, patch RuntimePatch) error {
	return c.executor.CanApply(ctx, patch)
}

// sentinel errors for the patch package.
var (
	// ErrNoSnapshot is returned by Snapshot when no snapshot is available.
	ErrNoSnapshot = errors.New("patch: no snapshot available")
	// ErrNoExecutor is returned by Snapshot/Restore when no executor is
	// registered for the given target.
	ErrNoExecutor = errors.New("patch: no executor registered for target")
	// ErrNoRestore is returned by Restore when a component snapshot was
	// captured but the component cannot consume it back.
	ErrNoRestore = errors.New("patch: component does not support restore")
)

// Restorable is implemented by RuntimeComponents whose Snapshot output can be
// loaded back, making a true state rollback possible. Components that only
// implement RuntimeComponent can be snapshotted but NOT restored — the
// Registry therefore refuses to retain their snapshots (see Snapshot) so a
// captured-but-unusable snapshot can never masquerade as a rollback.
type Restorable interface {
	// Restore loads a value previously returned by Snapshot back into the
	// component, reverting it to the captured state.
	Restore(ctx context.Context, snap any) error
}

// ExecutorSnapshot captures the pre-apply state of a target executor so it
// can be restored during rollback. When the executor implements
// RuntimeComponent and its Snapshot returns a non-nil value, that value is
// stored here. When the executor only implements Executor (via
// ExecutorComponent whose Snapshot returns ErrNoSnapshot), the fallback is
// to save the old Executor reference itself — Restore then Replace-s it back.
type ExecutorSnapshot struct {
	// Target is the executor name this snapshot was taken for.
	Target string
	// ComponentSnap is the snapshot returned by RuntimeComponent.Snapshot.
	// It is populated ONLY when the component also implements Restorable,
	// i.e. only when Restore can actually feed it back. For components that
	// cannot consume their own snapshot, this stays nil and OldExecutor
	// carries the rollback — capturing state that nothing can restore would
	// be a silent data loss dressed up as a rollback.
	ComponentSnap any
	// OldExecutor is the pre-apply Executor reference, used when
	// ComponentSnap is nil. Restore calls Registry.Replace with this.
	OldExecutor Executor
}

// Snapshot captures the pre-apply state of the executor registered for the
// given target. It is the rollback primitive: Apply → Snapshot before,
// Restore after to revert.
//
// A component snapshot is retained ONLY when the target also implements
// Restorable, because only then can Restore feed it back. Otherwise the
// snapshot falls back to the old Executor reference, which Restore Replace-s
// back into the registry.
func (r *Registry) Snapshot(ctx context.Context, target string) (*ExecutorSnapshot, error) {
	r.mu.RLock()
	ex, ok := r.executors[target]
	fallback := r.fallback
	r.mu.RUnlock()

	if !ok {
		if fallback == nil {
			return nil, fmt.Errorf("%w: %q", ErrNoExecutor, target)
		}
		// Fallback is a RuntimeComponent — snapshot it only if it can
		// restore that snapshot itself.
		if _, restorable := fallback.(Restorable); restorable {
			snap, err := fallback.Snapshot(ctx)
			if err != nil && !errors.Is(err, ErrNoSnapshot) {
				return nil, fmt.Errorf("snapshot fallback for %q: %w", target, err)
			}
			if err == nil && snap != nil {
				return &ExecutorSnapshot{Target: target, ComponentSnap: snap, OldExecutor: fallback}, nil
			}
		}
		return &ExecutorSnapshot{Target: target, OldExecutor: fallback}, nil
	}

	// Retain a component snapshot only when the component can consume it.
	if rc, isComp := ex.(RuntimeComponent); isComp {
		if _, restorable := ex.(Restorable); restorable {
			snap, err := rc.Snapshot(ctx)
			if err != nil && !errors.Is(err, ErrNoSnapshot) {
				return nil, fmt.Errorf("snapshot target %q: %w", target, err)
			}
			if err == nil && snap != nil {
				return &ExecutorSnapshot{Target: target, ComponentSnap: snap, OldExecutor: ex}, nil
			}
		}
	}

	// Executor-reference rollback: Restore will Replace this back.
	return &ExecutorSnapshot{Target: target, OldExecutor: ex}, nil
}

// Restore reverts the executor for the given target to the state captured in
// the snapshot.
//
// When the snapshot holds a ComponentSnap, the target MUST implement
// Restorable (Snapshot guarantees this pairing) and the state is loaded back
// into the live component. Otherwise Restore swaps the pre-apply Executor
// reference back into the registry. A nil snapshot, an unknown snapshot type,
// or a snapshot with neither restorable state nor an old executor yields
// ErrNoSnapshot — rollback must fail loudly rather than silently no-op.
func (r *Registry) Restore(ctx context.Context, target string, snap any) error {
	if snap == nil {
		return fmt.Errorf("%w: nil snapshot for %q", ErrNoSnapshot, target)
	}
	es, ok := snap.(*ExecutorSnapshot)
	if !ok {
		return fmt.Errorf("%w: unknown snapshot type %T for %q", ErrNoSnapshot, snap, target)
	}

	// Prefer true state restoration when the snapshot carries component state.
	if es.ComponentSnap != nil {
		restorable, canRestore := es.OldExecutor.(Restorable)
		if !canRestore {
			return fmt.Errorf("%w: %q captured component state", ErrNoRestore, target)
		}
		if err := restorable.Restore(ctx, es.ComponentSnap); err != nil {
			return fmt.Errorf("restore target %q: %w", target, err)
		}
		return nil
	}

	if es.OldExecutor == nil {
		return fmt.Errorf("%w: nil old executor in snapshot for %q", ErrNoSnapshot, target)
	}
	r.mu.Lock()
	r.executors[target] = es.OldExecutor
	r.mu.Unlock()
	return nil
}

// Ensure ExecutorComponent implements RuntimeComponent.
var _ RuntimeComponent = (*ExecutorComponent)(nil)

// Registry manages patch executors and runtime components by target name.
//
// Concurrency: Registry is safe for concurrent use. Rollback paths (Restore)
// mutate the executor map while other goroutines may be dispatching patches,
// so every map access is guarded by mu.
type Registry struct {
	mu        sync.RWMutex
	executors map[string]Executor
	// fallback is a component that handles patches for targets that have no
	// dedicated executor registered. This enables catch-all executors like
	// liveDAGPatchExecutor to handle all workflow structure patches (insert/
	// remove nodes/edges) whose targets are dynamic node IDs.
	fallback RuntimeComponent
	// applied tracks already-applied patch IDs for idempotent re-delivery.
	applied map[string]bool
}

// NewRegistry creates a new patch registry.
func NewRegistry() *Registry {
	return &Registry{
		executors: make(map[string]Executor),
		applied:   make(map[string]bool),
	}
}

// SetFallback sets a fallback component that handles patches for targets
// with no dedicated executor registered. When Apply cannot find an executor
// by target, it delegates to the fallback if one is set.
func (r *Registry) SetFallback(comp RuntimeComponent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fallback = comp
}

// CanApply reports whether a patch targeting the given name would be
// dispatched to an executor (dedicated or fallback) WITHOUT applying anything.
// It is the read-only preflight for shadow/staging runtimes: validating a
// patch must not mutate live state.
func (r *Registry) CanApply(target string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, ok := r.executors[target]; ok {
		return true
	}
	return r.fallback != nil
}

// Register registers an executor for a target component.
func (r *Registry) Register(target string, ex Executor) error {
	if target == "" {
		return errors.New("patch: target must not be empty")
	}
	if ex == nil {
		return errors.New("patch: executor must not be nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.executors[target]; exists {
		return fmt.Errorf("patch: executor for %q already registered", target)
	}
	r.executors[target] = ex
	return nil
}

// RegisterComponent registers a RuntimeComponent by its Name.
// This is the preferred registration method for new code.
func (r *Registry) RegisterComponent(comp RuntimeComponent) error {
	if comp == nil {
		return errors.New("patch: component must not be nil")
	}
	return r.Register(comp.Name(), comp)
}

// Replace registers ex for target, overwriting any existing registration.
// Unlike Register, Replace does not error when the target is already taken.
// Use it for live-swap paths (e.g. injecting the agent's live runtime after
// bootstrap) where a component must be updated in place.
func (r *Registry) Replace(target string, ex Executor) error {
	if target == "" {
		return errors.New("patch: target must not be empty")
	}
	if ex == nil {
		return errors.New("patch: executor must not be nil")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.executors[target] = ex
	return nil
}

// ReplaceComponent replaces the component registered under comp.Name(),
// overwriting any existing registration.
func (r *Registry) ReplaceComponent(comp RuntimeComponent) error {
	if comp == nil {
		return errors.New("patch: component must not be nil")
	}
	return r.Replace(comp.Name(), comp)
}

// lookup resolves the executor for a target plus the current fallback under
// the read lock. Executor calls MUST happen after the lock is released — an
// executor may re-enter the registry (e.g. a rollback path calling Restore),
// and holding the lock across that would deadlock.
func (r *Registry) lookup(target string) (ex Executor, fallback RuntimeComponent, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ex, ok = r.executors[target]
	return ex, r.fallback, ok
}

// executorFor resolves a single target under the read lock.
func (r *Registry) executorFor(target string) (Executor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ex, ok := r.executors[target]
	return ex, ok
}

// isApplied reports whether a non-empty patch ID was already applied.
func (r *Registry) isApplied(id string) bool {
	if id == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.applied[id]
}

// markApplied records a non-empty patch ID as applied.
func (r *Registry) markApplied(id string) {
	if id == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.applied[id] = true
}

// Apply dispatches a patch to the appropriate executor.
// First tries to find an executor by target name. If none is found and a
// fallback is set, delegates to the fallback. If no fallback exists, returns
// an error. If the patch has a Rollback, it is automatically applied on failure.
// If the patch has a non-empty ID that was already applied, Apply silently skips
// it — this provides idempotent re-delivery protection.
func (r *Registry) Apply(ctx context.Context, patch RuntimePatch) error {
	// Idempotency guard: skip already-applied patches.
	if r.isApplied(patch.ID) {
		return nil
	}

	ex, fallback, ok := r.lookup(patch.Target)
	if !ok {
		// No executor for this target — try the fallback if one is set.
		if fallback != nil {
			rollback, err := fallback.Apply(ctx, patch)
			if err != nil {
				// Attempt rollback via the fallback executor itself. A
				// fallback-originated rollback targets a fallback-only key
				// (no exact executor exists in r.executors), so it must be
				// applied by the fallback, not looked up by target name.
				if rollback != nil {
					if _, rbErr := fallback.Apply(ctx, *rollback); rbErr != nil {
						return fmt.Errorf("patch %s on %s (fallback) failed (%w); rollback also failed: %v",
							patch.Type, patch.Target, err, rbErr)
					}
				}
				return fmt.Errorf("patch %s on %s (fallback): %w", patch.Type, patch.Target, err)
			}
			r.markApplied(patch.ID)
			return nil
		}
		return fmt.Errorf("patch: no executor registered for target %q", patch.Target)
	}
	rollback, err := ex.Apply(ctx, patch)
	if err != nil {
		// Attempt rollback if available.
		if rollback != nil {
			if rbEx, found := r.executorFor(rollback.Target); found {
				if _, rbErr := rbEx.Apply(ctx, *rollback); rbErr != nil {
					return fmt.Errorf("patch %s failed (%w); rollback also failed: %v",
						patch.Type, err, rbErr)
				}
			}
		}
		return fmt.Errorf("patch %s on %s: %w", patch.Type, patch.Target, err)
	}
	r.markApplied(patch.ID)
	return nil
}

// ApplySet applies a PatchSet atomically. If any patch in the set fails,
// all previously applied patches are rolled back in reverse order.
func (r *Registry) ApplySet(ctx context.Context, ps PatchSet) error {
	if len(ps.Patches) == 0 {
		return nil
	}

	// Track applied patches for rollback.
	type applied struct {
		patch    RuntimePatch
		rollback *RuntimePatch
	}
	var appliedPatches []applied

	// undoAll reverts every already-applied patch in reverse order. Executor
	// calls happen outside the registry lock (see lookup).
	undoAll := func() {
		for i := len(appliedPatches) - 1; i >= 0; i-- {
			ap := appliedPatches[i]
			if ap.rollback == nil {
				continue
			}
			if rbEx, ok := r.executorFor(ap.rollback.Target); ok {
				_, _ = rbEx.Apply(ctx, *ap.rollback)
			}
		}
	}

	for _, p := range ps.Patches {
		// Idempotency guard: skip already-applied patches.
		if r.isApplied(p.ID) {
			continue
		}

		ex, fallback, ok := r.lookup(p.Target)
		if !ok {
			// Try fallback if no dedicated executor.
			if fallback != nil {
				rollback, fbErr := fallback.Apply(ctx, p)
				if fbErr != nil {
					undoAll()
					return fmt.Errorf("patch set: no executor for target %q (fallback also failed: %w)", p.Target, fbErr)
				}
				r.markApplied(p.ID)
				appliedPatches = append(appliedPatches, applied{patch: p, rollback: rollback})
				continue
			}
			undoAll()
			return fmt.Errorf("patch set: no executor for target %q", p.Target)
		}

		if canErr := ex.CanApply(ctx, p); canErr != nil {
			undoAll()
			return fmt.Errorf("patch set: cannot apply %s on %s: %w", p.Type, p.Target, canErr)
		}

		rollback, err := ex.Apply(ctx, p)
		if err != nil {
			undoAll()
			return fmt.Errorf("patch set: apply %s on %s failed: %w", p.Type, p.Target, err)
		}

		r.markApplied(p.ID)
		appliedPatches = append(appliedPatches, applied{patch: p, rollback: rollback})
	}

	return nil
}
