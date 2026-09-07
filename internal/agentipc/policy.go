package agentipc

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// ExecutionPolicy is the strategy for dispatching a task to an agent
// (see the design doc). The Kernel picks one policy per dispatch; the feature flag
// selects the active path. The legacy policy is retained only as a
// library constant — the leader runtime is removed,
// so no production dispatcher registers a legacy track.
type ExecutionPolicy int

const (
	// PolicyLegacy is the old leader+sub dispatch path. Retained as a
	// library constant for the dispatcher's dormant legacy branch; production
	// wires a nil legacy track, so selecting it returns ErrDispatcherNotRegistered.
	PolicyLegacy ExecutionPolicy = iota
	// PolicyTaskFabric is the Kernel path: Task Fabric → Scheduler → Agent
	// (capability-aware, no central leader). This is the only production policy.
	PolicyTaskFabric
)

// PolicyFlag is the feature flag that selects which dispatch policy is
// active (parallel + feature flag gradual cutover). Production starts
// at PolicyTaskFabric; the flag is read atomically so a flip takes effect on
// the next dispatch without restart.
type PolicyFlag struct {
	v atomic.Int64 // 0 = legacy, 1 = task fabric (int64 avoids int→int32 narrowing)
}

// NewPolicyFlag creates a flag with the given initial policy.
func NewPolicyFlag(initial ExecutionPolicy) *PolicyFlag {
	pf := &PolicyFlag{}
	pf.Set(initial)
	return pf
}

// Set flips the active policy. Thread-safe; takes effect on the next
// dispatch.
func (p *PolicyFlag) Set(policy ExecutionPolicy) {
	p.v.Store(int64(policy))
}

// Active returns the currently active policy.
func (p *PolicyFlag) Active() ExecutionPolicy {
	return ExecutionPolicy(p.v.Load())
}

// IsLegacy reports whether the legacy policy is active.
func (p *PolicyFlag) IsLegacy() bool {
	return p.Active() == PolicyLegacy
}

// IsTaskFabric reports whether the new Task Fabric policy is active.
func (p *PolicyFlag) IsTaskFabric() bool {
	return p.Active() == PolicyTaskFabric
}

// Dispatcher is the abstraction both policies implement. The Kernel calls
// the active policy's Dispatch; the implementations differ (legacy routes
// through the leader; new routes through Task Fabric). Both must be
// registered for the flag to work.
type Dispatcher interface {
	// Dispatch sends a task to an agent under the given policy. The
	// implementation must be equivalent in observable outcome (the task is
	// delivered and executed); only the path differs.
	D(ctx context.Context, agentID string, taskID string, payload any) error
}

// DualTrackDispatcher holds both the legacy and new dispatchers and routes to
// the active one based on the PolicyFlag (parallel + feature flag).
// Both paths coexist; the flag selects which is live. This is the
// "双轨等价" verification surface: run both under the flag and compare.
type DualTrackDispatcher struct {
	flag    *PolicyFlag
	legacy  Dispatcher
	newPath Dispatcher
	// equivalencyChecks collects per-dispatch comparisons when both paths are
	// run in shadow mode (flag = legacy, new path runs in shadow and the
	// outcomes are compared). Empty when shadow mode is off.
	mu         sync.Mutex
	shadow     bool
	mismatches int
}

// NewDualTrackDispatcher wires the dual-track dispatcher. The flag selects the
// active path; when shadow is true, the inactive path also runs and the
// outcomes are compared (equivalence verification).
func NewDualTrackDispatcher(flag *PolicyFlag, legacy, newPath Dispatcher, shadow bool) *DualTrackDispatcher {
	return &DualTrackDispatcher{
		flag:    flag,
		legacy:  legacy,
		newPath: newPath,
		shadow:  shadow,
	}
}

// Dispatch routes to the active policy's dispatcher. When shadow mode is on,
// the inactive path also runs and the outcomes are compared; a mismatch is
// counted and surfaced via Mismatches().
func (d *DualTrackDispatcher) Dispatch(ctx context.Context, agentID, taskID string, payload any) error {
	// Snapshot the mutable state (shadow + both paths) under mu: SetShadow /
	// SetNewPath can flip concurrently with in-flight dispatches (live mid-run
	// flip), so reading the fields directly would race. The snapshot makes a
	// single dispatch observe one consistent configuration.
	shadow, legacy, newPath := d.snapshot()
	if d.flag.IsLegacy() {
		if legacy == nil {
			return ErrDispatcherNotRegistered
		}
		err := legacy.D(ctx, agentID, taskID, payload)
		if shadow {
			d.compareShadow(ctx, agentID, taskID, payload, err, newPath)
		}
		return err
	}
	if newPath == nil {
		return ErrDispatcherNotRegistered
	}
	err := newPath.D(ctx, agentID, taskID, payload)
	if shadow {
		d.compareShadow(ctx, agentID, taskID, payload, err, legacy)
	}
	return err
}

// snapshot returns a consistent view of the dispatcher's mutable state
// (shadow flag and both paths) under one mu acquisition.
func (d *DualTrackDispatcher) snapshot() (shadow bool, legacy, newPath Dispatcher) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.shadow, d.legacy, d.newPath
}

// SetShadow turns shadow mode on or off at runtime. Shadow must be disabled
// when the flag flips to TaskFabric: with the new path active, the legacy path
// is the inactive one and running it in shadow would re-dispatch every task
// (double execution). Callers flip shadow off in the same critical section as
// the flag flip.
func (d *DualTrackDispatcher) SetShadow(shadow bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.shadow = shadow
}

// SetNewPath swaps the new-path dispatcher at runtime (used by the Kernel when
// the Task Fabric path is enabled: the shadow scorer is replaced by the real
// executor). The callers must flip shadow off via SetShadow in the same
// critical section to avoid double execution.
func (d *DualTrackDispatcher) SetNewPath(newPath Dispatcher) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.newPath = newPath
}

// NewPath returns the current new-path dispatcher (may be nil when not wired).
func (d *DualTrackDispatcher) NewPath() Dispatcher {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.newPath
}

// compareShadow runs the inactive path (passed in from the dispatch snapshot)
// and compares its outcome with the active path's. shadowDispatcher may be nil
// (path not wired) — then no comparison is possible and nothing is counted.
func (d *DualTrackDispatcher) compareShadow(ctx context.Context, agentID, taskID string, payload any, activeErr error, shadowDispatcher Dispatcher) {
	if shadowDispatcher == nil {
		return
	}
	shadowErr := shadowDispatcher.D(ctx, agentID, taskID, payload)
	// Equivalence: both must agree on success/failure. Error text may differ;
	// we compare only the presence of an error.
	if (activeErr == nil) != (shadowErr == nil) {
		d.mu.Lock()
		d.mismatches++
		d.mu.Unlock()
	}
}

// Mismatches returns the count of shadow-mode outcome mismatches (0 = the two
// paths are equivalent so far).
func (d *DualTrackDispatcher) Mismatches() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.mismatches
}

// ErrDispatcherNotRegistered is returned when the active path has no
// dispatcher wired.
var ErrDispatcherNotRegistered = errors.New("agentipc: dispatcher not registered")
