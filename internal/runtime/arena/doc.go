// Package arena runs ARES chaos-engineering scenarios (convergence Phase 3).
//
// Scenarios (leader assassination, cascading storms, …) inject failures
// into a live or scratch runtime and assert recovery: lease expiry,
// requeue, replacement spawn, checkpoint resume. It is a mainline
// capability, not a demo — cmd/ares/arena.go drives it directly, which is
// why it lives under runtime/ instead of examples/.
package arena
