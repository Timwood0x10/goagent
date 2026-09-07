package taskfabric

// TaskState is the lifecycle state of a Task (design of docs/zh/architecture/ares-runtime.md).
type TaskState string

const (
	// StateReady: the task is unowned and available for acquire.
	StateReady TaskState = "READY"
	// StateLeased: an agent holds a TaskLease but has not started execution.
	StateLeased TaskState = "LEASED"
	// StateRunning: an agent is executing the task.
	StateRunning TaskState = "RUNNING"
	// StateSuspended: execution paused at a quantum boundary; a checkpoint is
	// preserved and the task may be re-acquired (cooperative preemption).
	StateSuspended TaskState = "SUSPENDED"
	// StateCompleted: the task finished successfully.
	StateCompleted TaskState = "COMPLETED"
	// StateFailed: the task failed (retry policy may re-queue it).
	StateFailed TaskState = "FAILED"
)

// canTransition reports whether the state machine allows from → to.
// Legal transitions (docs/zh/architecture/ares-runtime.md §4):
//
//	READY → LEASED (acquire)
//	LEASED → RUNNING (start), READY (release)
//	RUNNING → COMPLETED, FAILED, SUSPENDED (yield), READY (preempt/release)
//	SUSPENDED → LEASED (re-acquire with preserved checkpoint), READY (release)
func canTransition(from, to TaskState) bool {
	switch from {
	case StateReady:
		return to == StateLeased
	case StateLeased:
		return to == StateRunning || to == StateReady
	case StateRunning:
		return to == StateCompleted || to == StateFailed ||
			to == StateSuspended || to == StateReady
	case StateSuspended:
		return to == StateLeased || to == StateReady
	default:
		return false
	}
}
