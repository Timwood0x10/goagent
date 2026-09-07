package taskfabric

import (
	"fmt"
	"testing"
	"time"
)

// newTask builds a minimal READY-capable task.
func newTask(id string) *Task {
	return &Task{ID: id, Capability: "rust", Priority: 1}
}

// withClock injects a controllable clock into the fabric.
func withClock(f *Fabric, at *time.Time) {
	f.now = func() time.Time { return *at }
}

// TestFabricCASCompetition verifies acceptance #1: two agents competing for
// the same task see exactly one winner — the second acquire is rejected.
func TestFabricCASCompetition(t *testing.T) {
	f := NewFabric()
	if err := f.Create(newTask("t1")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	epoch, err := f.Acquire("t1", "agent-a", time.Minute)
	if err != nil {
		t.Fatalf("first acquire must win: %v", err)
	}
	if epoch == 0 {
		t.Fatal("acquire must return a non-zero fencing token")
	}
	if _, err := f.Acquire("t1", "agent-b", time.Minute); err != ErrTaskNotReady {
		t.Fatalf("second acquire must be rejected, got %v", err)
	}
	task, _ := f.Task("t1")
	if task.Owner != "agent-a" || task.State != StateLeased {
		t.Fatalf("owner must be agent-a, got owner=%q state=%s", task.Owner, task.State)
	}
}

// TestFabricLeaseExpiryRequeues verifies acceptance #2: an expired lease
// returns the task to READY, and another agent can acquire it (Agent 死亡 ≠
// Task 死亡).
func TestFabricLeaseExpiryRequeues(t *testing.T) {
	f := NewFabric()
	now := time.Now()
	withClock(f, &now)
	if err := f.Create(newTask("t1")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.Acquire("t1", "agent-a", time.Minute); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	// Lease not expired yet: nothing requeued.
	if n := f.CheckExpiredLeases(); len(n) != 0 {
		t.Fatalf("want 0 requeued before expiry, got %d", len(n))
	}
	// Advance past the TTL.
	now = now.Add(2 * time.Minute)
	if n := f.CheckExpiredLeases(); len(n) != 1 || n[0] != "t1" {
		t.Fatalf("want [t1] requeued after expiry, got %v", n)
	}
	if _, err := f.Acquire("t1", "agent-b", time.Minute); err != nil {
		t.Fatalf("agent-b must acquire after expiry: %v", err)
	}
}

// TestFabricIllegalTransitions verifies acceptance #3: illegal state
// transitions and non-owner operations are rejected.
func TestFabricIllegalTransitions(t *testing.T) {
	f := NewFabric()
	if err := f.Create(newTask("t1")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// READY → COMPLETED directly is illegal: the task is unowned, so the
	// owner check rejects it (ErrNotOwner), never reaching the transition.
	if err := f.Complete("t1", "agent-a", 1); err != ErrNotOwner {
		t.Fatalf("complete from READY must be rejected, got %v", err)
	}
	// Acquire then start by a non-owner is rejected.
	epoch, err := f.Acquire("t1", "agent-a", time.Minute)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := f.Start("t1", "agent-b", epoch); err != ErrNotOwner {
		t.Fatalf("start by non-owner must be rejected, got %v", err)
	}
}

// TestFabricFencingTokenRejectsStaleOperations verifies the fencing token
// (lease epoch) guard: a stale epoch cannot act on a task whose lease has
// moved on — neither when the same agent re-acquired (ErrEpochMismatch) nor
// when another agent took over after lease expiry (ErrNotOwner, and the new
// owner's operations stay intact).
func TestFabricFencingTokenRejectsStaleOperations(t *testing.T) {
	f := NewFabric()
	now := time.Now()
	withClock(f, &now)
	if err := f.Create(newTask("t1")); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Same-owner stale epoch: A releases, re-acquires (epoch bumps), then a
	// late Complete with the OLD epoch must be rejected.
	e1, err := f.Acquire("t1", "agent-a", time.Minute)
	if err != nil {
		t.Fatalf("Acquire#1: %v", err)
	}
	if err := f.Release("t1", "agent-a", e1); err != nil {
		t.Fatalf("Release: %v", err)
	}
	e2, err := f.Acquire("t1", "agent-a", time.Minute)
	if err != nil {
		t.Fatalf("Acquire#2: %v", err)
	}
	if e2 == e1 {
		t.Fatal("epoch must bump across acquisitions")
	}
	if err := f.Start("t1", "agent-a", e2); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := f.Complete("t1", "agent-a", e1); err != ErrEpochMismatch {
		t.Fatalf("stale epoch must be rejected with ErrEpochMismatch, got %v", err)
	}

	// Cross-owner takeover: A's lease expires, B acquires, A's late Release
	// must NOT free B's task.
	now = now.Add(2 * time.Minute)
	if n := f.CheckExpiredLeases(); len(n) != 1 {
		t.Fatalf("want 1 requeued, got %v", n)
	}
	eB, err := f.Acquire("t1", "agent-b", time.Minute)
	if err != nil {
		t.Fatalf("B acquire: %v", err)
	}
	if err := f.Release("t1", "agent-a", e2); err != ErrNotOwner {
		t.Fatalf("stale owner must be rejected, got %v", err)
	}
	// B's ownership is untouched and fully usable.
	if err := f.Start("t1", "agent-b", eB); err != nil {
		t.Fatalf("B start must still work: %v", err)
	}
	task, _ := f.Task("t1")
	if task.Owner != "agent-b" || task.State != StateRunning {
		t.Fatalf("B must own a RUNNING task, got owner=%q state=%s", task.Owner, task.State)
	}
}

// TestFabricEventLogRebuildsState verifies acceptance #4: the event log fully
// rebuilds the task's final state (Evidence-Driven).
func TestFabricEventLogRebuildsState(t *testing.T) {
	f := NewFabric()
	if err := f.Create(newTask("t1")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	epoch, err := f.Acquire("t1", "agent-a", time.Minute)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := f.Start("t1", "agent-a", epoch); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := f.Complete("t1", "agent-a", epoch); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	// Rebuild state from the event log alone.
	state := StateReady
	owner := ""
	for _, ev := range f.Events() {
		switch ev.Type {
		case EventTaskCreated:
			state = StateReady
		case EventTaskAcquired:
			state = StateLeased
			owner = ev.AgentID
		case EventTaskStarted:
			state = StateRunning
		case EventTaskCompleted:
			state = StateCompleted
		}
	}
	if state != StateCompleted || owner != "agent-a" {
		t.Fatalf("event rebuild must end COMPLETED by agent-a, got state=%s owner=%q", state, owner)
	}
}

// TestFabricYieldPreservesCheckpoint verifies the cooperative preemption
// primitive: yield stores a checkpoint, the task returns to SUSPENDED, and a
// re-acquire preserves it.
func TestFabricYieldPreservesCheckpoint(t *testing.T) {
	f := NewFabric()
	if err := f.Create(newTask("t1")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	epoch, err := f.Acquire("t1", "agent-a", time.Minute)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := f.Start("t1", "agent-a", epoch); err != nil {
		t.Fatalf("Start: %v", err)
	}
	cp := map[string]any{"step": 3}
	if err := f.Yield("t1", "agent-a", epoch, cp); err != nil {
		t.Fatalf("Yield: %v", err)
	}
	// Agent-a releases (cooperative preemption), agent-b re-acquires.
	if err := f.Release("t1", "agent-a", epoch); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := f.Acquire("t1", "agent-b", time.Minute); err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	task, _ := f.Task("t1")
	if task.State != StateLeased {
		t.Fatalf("want LEASED, got %s", task.State)
	}
	kept, ok := task.Checkpoint.(map[string]any)
	if !ok || kept["step"] != 3 {
		t.Fatalf("checkpoint must survive preemption, got %+v", task.Checkpoint)
	}
}

// TestFabricFailRetries verifies the retry policy: a failing task requeues to
// READY while attempts remain, then settles in FAILED once exhausted.
func TestFabricFailRetries(t *testing.T) {
	f := NewFabric()
	tk := newTask("t1")
	tk.RetryPolicy = RetryPolicy{MaxRetries: 2} // 2 attempts: 1st failure requeues, 2nd fails out
	if err := f.Create(tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	epoch, err := f.Acquire("t1", "agent-a", time.Minute)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := f.Start("t1", "agent-a", epoch); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := f.Fail("t1", "agent-a", epoch); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	task, _ := f.Task("t1")
	if task.State != StateReady {
		t.Fatalf("want requeue to READY, got %s", task.State)
	}
	// Second attempt fails: budget exhausted → FAILED.
	epoch2, err := f.Acquire("t1", "agent-a", time.Minute)
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	if err := f.Start("t1", "agent-a", epoch2); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := f.Fail("t1", "agent-a", epoch2); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if task, _ := f.Task("t1"); task.State != StateFailed {
		t.Fatalf("want FAILED after retries exhausted, got %s", task.State)
	}
}

// TestFabricTerminalEventsCarryAgentID locks the N8 contract: terminal and
// requeue events must record the agent that caused the transition. The old
// code cleared the owner BEFORE recording, so task.failed / task.expired /
// task.released events lost the actor.
func TestFabricTerminalEventsCarryAgentID(t *testing.T) {
	t.Run("fail_retry_event_keeps_failing_agent", func(t *testing.T) {
		f := NewFabric()
		tk := newTask("t1")
		tk.RetryPolicy = RetryPolicy{MaxRetries: 2}
		if err := f.Create(tk); err != nil {
			t.Fatalf("Create: %v", err)
		}
		epoch, _ := f.Acquire("t1", "agent-a", time.Minute)
		if err := f.Start("t1", "agent-a", epoch); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if err := f.Fail("t1", "agent-a", epoch); err != nil {
			t.Fatalf("Fail: %v", err)
		}
		var failedAgent string
		for _, ev := range f.Events() {
			if ev.Type == EventTaskFailed {
				failedAgent = ev.AgentID
			}
		}
		if failedAgent != "agent-a" {
			t.Fatalf("task.failed event must carry the failing agent, got %q", failedAgent)
		}
	})

	t.Run("expire_event_keeps_dead_agent", func(t *testing.T) {
		f := NewFabric()
		now := time.Now()
		withClock(f, &now)
		if err := f.Create(newTask("t1")); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := f.Acquire("t1", "agent-a", time.Minute); err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		now = now.Add(2 * time.Minute)
		f.CheckExpiredLeases()
		var expiredAgent string
		for _, ev := range f.Events() {
			if ev.Type == EventTaskExpired {
				expiredAgent = ev.AgentID
			}
		}
		if expiredAgent != "agent-a" {
			t.Fatalf("task.expired event must carry the dead agent, got %q", expiredAgent)
		}
	})

	t.Run("release_event_keeps_releasing_agent", func(t *testing.T) {
		f := NewFabric()
		if err := f.Create(newTask("t1")); err != nil {
			t.Fatalf("Create: %v", err)
		}
		epoch, _ := f.Acquire("t1", "agent-a", time.Minute)
		if err := f.Release("t1", "agent-a", epoch); err != nil {
			t.Fatalf("Release: %v", err)
		}
		var releasedAgent string
		for _, ev := range f.Events() {
			if ev.Type == EventTaskReleased {
				releasedAgent = ev.AgentID
			}
		}
		if releasedAgent != "agent-a" {
			t.Fatalf("task.released event must carry the releasing agent, got %q", releasedAgent)
		}
	})
}

// TestFabricEventLogBounded locks the N8 contract: the in-memory lifecycle log
// is capped (maxInMemoryEvents × 2 resident bound), so a pathological number
// of transitions cannot grow memory without bound.
func TestFabricEventLogBounded(t *testing.T) {
	f := NewFabric()
	// Exceed 2× the cap: 3×maxInMemoryEvents creations.
	for i := 0; i < 3*maxInMemoryEvents; i++ {
		if err := f.Create(&Task{ID: fmt.Sprintf("bulk-%d", i), Capability: "rust"}); err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
	}
	events := f.Events()
	if len(events) > 2*maxInMemoryEvents {
		t.Fatalf("in-memory log must stay within 2×max (%d), got %d events", 2*maxInMemoryEvents, len(events))
	}
	if len(events) < maxInMemoryEvents {
		t.Fatalf("log must keep the newest maxInMemoryEvents, got only %d", len(events))
	}
	// The retained tail must be the newest creations (drop-oldest policy).
	if events[len(events)-1].TaskID != fmt.Sprintf("bulk-%d", 3*maxInMemoryEvents-1) {
		t.Fatalf("newest event must be retained, got %q", events[len(events)-1].TaskID)
	}
}
