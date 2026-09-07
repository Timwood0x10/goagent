package taskfabric

// IsReady reports whether a task's dependencies are all satisfied — every
// dependency task is COMPLETED — and the task itself is currently READY.
// This is the DAG-as-scheduling-source primitive (design of
// ares-runtime): the Scheduler only asks is_ready(task); the topology
// lives in each task's Dependencies. D3 (2026-08-16): P1/P2 construct
// Dependencies manually; planner / live DAG wiring happens in the P4
// migration.
//
// Args:
//   - id: the task id.
//
// Returns:
//   - bool: true when the task is READY and all dependencies completed.
//   - error: ErrTaskNotFound for an unknown id.
func (f *Fabric) IsReady(id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[id]
	if !ok {
		return false, ErrTaskNotFound
	}
	if t.State != StateReady {
		return false, nil
	}
	return depsCompletedLocked(f.tasks, t.Dependencies), nil
}

// ReadyTasks returns the ids of every task whose dependencies are satisfied
// and that is currently READY — the scheduler's work source. No leader
// decides "B is done, now run C"; the completed states make C ready.
func (f *Fabric) ReadyTasks() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for id, t := range f.tasks {
		if t.State != StateReady {
			continue
		}
		if depsCompletedLocked(f.tasks, t.Dependencies) {
			out = append(out, id)
		}
	}
	return out
}

// ResumableTasks returns the ids of every task that can run a quantum right
// now: READY tasks (dependencies satisfied) plus SUSPENDED tasks whose lease
// is still valid — a yielded task the scheduler resumes by re-acquiring at the
// next quantum boundary (SUSPENDED semantics lock: "Continue is the
// Scheduler's decision via re-acquire"). SUSPENDED tasks with an expired lease
// are intentionally excluded: the crash-recovery path (CheckExpiredLeases)
// requeues them to READY, and including them here too would let two drains
// race the same task.
func (f *Fabric) ResumableTasks() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for id, t := range f.tasks {
		switch t.State {
		case StateReady:
			if depsCompletedLocked(f.tasks, t.Dependencies) {
				out = append(out, id)
			}
		case StateSuspended:
			if t.Lease != nil && !t.Lease.IsExpired(f.now()) {
				out = append(out, id)
			}
		}
	}
	return out
}

// depsCompletedLocked reports whether every dependency task exists and is
// COMPLETED. Caller must hold f.mu.
func depsCompletedLocked(tasks map[string]*Task, deps []string) bool {
	for _, dep := range deps {
		d, ok := tasks[dep]
		if !ok || d.State != StateCompleted {
			return false
		}
	}
	return true
}
