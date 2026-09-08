package agentipc

import (
	"context"
	"sync"
	"sync/atomic"
)

// ExecutionPolicy is the dispatch-policy vocabulary for the kernel config and
// flag (kernel.policy). The dispatch entry that routed by this policy was
// removed (zero production callers — HTTP submits tasks to the Task Fabric
// directly and the kernelScheduler drains them), so the constants are
// config/flag state rather than live routing branches.
type ExecutionPolicy int

const (
	// PolicyLegacy is the old leader+sub dispatch path, retained as a library
	// constant for config compatibility. Production wires a nil legacy track;
	// with the dispatch entry removed, nothing dispatches on it.
	PolicyLegacy ExecutionPolicy = iota
	// PolicyTaskFabric is the Kernel path: Task Fabric → Scheduler → Agent
	// (capability-aware, no central leader). This is the only production policy.
	PolicyTaskFabric
)

// PolicyFlag is the feature flag recording which dispatch policy is active
// (parallel + feature flag gradual cutover). Production starts at
// PolicyTaskFabric; the flag is read atomically. With the dispatch entry
// removed, a flip only changes the recorded policy — no dispatch routing
// happens anymore.
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

// Dispatcher is the abstraction both policies implement. Production wires the
// new path (Task Fabric); the legacy leader path is removed. Implementations
// must be equivalent in observable outcome (the task is delivered and
// executed); only the path differs.
type Dispatcher interface {
	// D sends a task to an agent under the given policy. The
	// implementation must be equivalent in observable outcome (the task is
	// delivered and executed); only the path differs.
	D(ctx context.Context, agentID string, taskID string, payload any) error
}

// DualTrackDispatcher holds the legacy and new dispatchers and records the
// active one based on the PolicyFlag (parallel + feature flag). The Kernel
// uses it as a mutable dispatcher facade: enableKernelExecution swaps the
// new path (SetNewPath) and turns shadow off (SetShadow) at startup.
type DualTrackDispatcher struct {
	flag    *PolicyFlag
	legacy  Dispatcher
	newPath Dispatcher
	// shadow records whether the inactive path would run alongside the
	// active one for equivalence comparison. The dispatch entry that acted on
	// it was removed (zero production callers); the field stays as mutable
	// facade state so SetShadow keeps its contract.
	mu     sync.Mutex
	shadow bool
}

// NewDualTrackDispatcher wires the dual-track dispatcher. The flag records
// the active path; shadow and the two tracks are held for the facade's
// mutable state (SetShadow / SetNewPath).
func NewDualTrackDispatcher(flag *PolicyFlag, legacy, newPath Dispatcher, shadow bool) *DualTrackDispatcher {
	return &DualTrackDispatcher{
		flag:    flag,
		legacy:  legacy,
		newPath: newPath,
		shadow:  shadow,
	}
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
