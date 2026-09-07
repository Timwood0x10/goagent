package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/agents/sub"
	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/aresrecovery"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/fabric/agent"
	"github.com/Timwood0x10/ares/internal/fabric/task"
)

// e2ePhaseCognition is the execution body injected into
// the fabric agent. Every quantum does real work but yields (Done=false) with
// a checkpoint — the task stays SUSPENDED with the checkpoint preserved
// (execution-quantum semantics). Only the replacement executor (created after
// the chaos kill) completes the task, so the SUSPENDED window is stable and
// the test cannot race past it.
type e2ePhaseCognition struct {
	mu      sync.Mutex
	quanta  int
	resumed any // payload["checkpoint"] surfaced by toModelTask on resume
}

var _ agentfabric.Cognition = (*e2ePhaseCognition)(nil)

func (c *e2ePhaseCognition) ExecuteStep(_ context.Context, task *models.Task) (*agentfabric.StepOutcome, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.quanta++
	// toModelTask surfaces the previous quantum's StepCheckpoint as
	// payload["checkpoint"] — a resumed quantum observes where it left off.
	c.resumed = task.Payload["checkpoint"]
	return &agentfabric.StepOutcome{
		Done:       false,
		Checkpoint: map[string]any{"phase": "investigation-done", "n": 1},
	}, nil
}

// e2eRecoveryExecutor is the replacement executor the recovery loop
// factories when the dead agent leaves no capable executor: it resumes the
// recovered task from the preserved checkpoint (the new
// execution body continues where the old one stopped, it does not restart).
type e2eRecoveryExecutor struct {
	id          string
	typ         models.AgentType
	mu          sync.Mutex
	resumedFrom any
}

func (e *e2eRecoveryExecutor) ID() string             { return e.id }
func (e *e2eRecoveryExecutor) Type() models.AgentType { return e.typ }
func (e *e2eRecoveryExecutor) ExecuteStep(_ context.Context, task *models.Task) (*sub.StepOutcome, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.resumedFrom = task.Payload["checkpoint"]
	res := models.NewTaskResult(task.TaskID, task.AgentType)
	res.SetSuccess(nil, "e2e: resumed by "+e.id)
	return &sub.StepOutcome{Done: true, Result: res}, nil
}

func (e *e2eRecoveryExecutor) resumed() any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.resumedFrom
}

// e2eAgentSink collects agentfabric lifecycle events (agent.spawned/killed/...)
// so the event-stream assertion can verify the chaos kill is observable.
type e2eAgentSink struct {
	mu    sync.Mutex
	types []agentfabric.AgentEventType
}

var _ agentfabric.EventSink = (*e2eAgentSink)(nil)

func (s *e2eAgentSink) Emit(_ context.Context, ev agentfabric.AgentEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.types = append(s.types, ev.Type)
	return nil
}

func (s *e2eAgentSink) contains(t agentfabric.AgentEventType) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range s.types {
		if v == t {
			return true
		}
	}
	return false
}

// snapshot returns a copy of the collected types under the lock, for the same
// reason as e2eTaskEventLog.snapshot: reading s.types directly from the test
// goroutine races with Emit.
func (s *e2eAgentSink) snapshot() []agentfabric.AgentEventType {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]agentfabric.AgentEventType(nil), s.types...)
}

// e2eTaskEventLog collects ares_events task lifecycle events (task.created /
// acquired / yielded / completed / ...) published by the Task Fabric.
type e2eTaskEventLog struct {
	mu    sync.Mutex
	types []ares_events.EventType
}

func (l *e2eTaskEventLog) add(t ares_events.EventType) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.types = append(l.types, t)
}

func (l *e2eTaskEventLog) contains(t ares_events.EventType) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, v := range l.types {
		if v == t {
			return true
		}
	}
	return false
}

// snapshot returns a copy of the collected types under the lock. Reading
// l.types directly (e.g. inside a t.Fatalf format arg) races with the
// subscriber goroutine's append — measured as a `-race` failure once per ~40
// runs of the grand-loop test.
func (l *e2eTaskEventLog) snapshot() []ares_events.EventType {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]ares_events.EventType(nil), l.types...)
}

// waitForEvents polls until every wanted event type has been observed, or the
// timeout elapses. It returns the missing types (empty when all arrived).
//
// A bounded wait is required rather than an instantaneous check: the fabric
// publishes lifecycle events through the EventStore, and the subscriber
// goroutine appends them asynchronously. A task can therefore be COMPLETED in
// the fabric a few microseconds before task.completed lands in this log — which
// is precisely what made the assertion flaky under `-race -coverprofile`.
func (l *e2eTaskEventLog) waitForEvents(timeout time.Duration, want ...ares_events.EventType) []ares_events.EventType {
	deadline := time.Now().Add(timeout)
	for {
		missing := make([]ares_events.EventType, 0, len(want))
		for _, w := range want {
			if !l.contains(w) {
				missing = append(missing, w)
			}
		}
		if len(missing) == 0 || time.Now().After(deadline) {
			return missing
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// waitFabricState polls until the task reaches the given state or the timeout
// elapses, returning the final state.
func waitFabricState(t *testing.T, f *taskfabric.Fabric, taskID string, want taskfabric.TaskState, timeout time.Duration) taskfabric.TaskState {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		tk, err := f.Task(taskID)
		if err == nil && tk.State == want {
			return tk.State
		}
		time.Sleep(5 * time.Millisecond)
	}
	tk, err := f.Task(taskID)
	if err != nil {
		t.Fatalf("Task(%q): %v", taskID, err)
	}
	return tk.State
}

// TestE2E_GrandLoop_RealSchedulerChaosRecovery is the total acceptance test
// for chaos recovery, run through the REAL
// scheduling chain — no leader, no planner, no simulation:
//
//	Submit(Create) → Schedule → Acquire → RunQuantum(agent-A quantum 1, yield)
//	→ SUSPENDED + checkpoint preserved → Chaos kill agent-A → lease expiry →
//	recovery requeues → the factory spawns a replacement execution body →
//	bound executor resumes from the checkpoint → RunQuantum(quantum 2, done)
//	→ COMPLETED.
//
// Assertions: (1) the task completes with the replacement having RESUMED from
// the checkpoint (not restarted); (2) the event stream carries
// task.created/acquired/yielded/completed and agent.killed; (3) the whole run
// is Leader OFF (no leader/dispatcher participates).
func TestE2E_GrandLoop_RealSchedulerChaosRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := ares_events.NewMemoryEventStore()
	taskEvents := &e2eTaskEventLog{}
	// Subscribe SYNCHRONOUSLY before any fabric.Create: the broadcast store
	// delivers only to subscribers present at publish time, so an async
	// subscription could miss task.created (flaky under load — make check
	// with -short -cover ./...).
	evCh, err := store.Subscribe(ctx, ares_events.EventFilter{})
	if err != nil {
		t.Fatalf("subscribe task events: %v", err)
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-evCh:
				if !ok {
					return
				}
				taskEvents.add(ev.Type)
			}
		}
	}()

	// Controllable clock: chaos kill must age the lease past its TTL without
	// real sleeping. Guarded because the fabric's clock func reads it from
	// scheduler/recovery goroutines while the test advances it.
	var clockMu sync.Mutex
	now := time.Now()
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		now = now.Add(d)
		clockMu.Unlock()
	}

	fabric := taskfabric.NewFabric().WithClock(clock).WithEventStore(store)
	agentSink := &e2eAgentSink{}
	agents := agentfabric.NewFabric().WithEventSink(agentSink)

	// ── 1. Spawn agent-A WITH a real execution body ────────────────
	cogA := &e2ePhaseCognition{}
	if _, err := agents.Spawn(ctx, agentfabric.SpawnSpec{
		Identity:     "agent-A",
		Capabilities: []string{"code"},
		CognitionFactory: func([]string) agentfabric.Cognition {
			return cogA
		},
	}); err != nil {
		t.Fatalf("spawn agent-A: %v", err)
	}

	// ── 2. Scheduler: the fabric is the single candidate source ────
	sched := NewKernelScheduler(fabric, map[string]CapabilityExecutor{}, newLoadTracker())
	sched.PollInterval = 20 * time.Millisecond
	sched.WithAgentFabric(agents).WithEventStore(store)
	go sched.Run(ctx)

	// ── 3. Recovery loop (a REAL replacement execution body) ─────
	var replacementMu sync.Mutex
	var replacement *e2eRecoveryExecutor
	rec := aresrecovery.New(fabric, agents, aresrecovery.DefaultRestartPolicy())
	// Wire the scheduler's stale-winner nomination exactly as peer mode
	// does. Without it this test does not exercise the production chain: a
	// drain that acquires t1 AFTER the clock advance mints a lease expiring at
	// (now + TTL), which this controlled clock never reaches — the task then
	// sits LEASED forever and recovery is never triggered. That was the 1-in-20
	// flake under `-race -coverprofile` (coverage instrumentation widens the
	// kill/lookup window). In production the same defect costs a full lease TTL
	// of dead time per agent death.
	recoveryKick, recoveryHint := newRecoveryKick()
	sched.WithRecoveryHint(recoveryHint)
	go runKernelRecoveryLoop(ctx, store, rec, kernelLoopConfig{RecoveryKick: recoveryKick},
		func(taskID, agentID string, executor CapabilityExecutor) {
			sched.RegisterExecutorForTask(taskID, agentID, executor)
		},
		func(agentID, capability string) CapabilityExecutor {
			rep := &e2eRecoveryExecutor{id: agentID, typ: models.AgentType(capability)}
			replacementMu.Lock()
			replacement = rep
			replacementMu.Unlock()
			return rep
		},
		sched.HasCapableExecutor,
	)

	// ── 4. Submit the task directly to the fabric (no leader) ───────────
	if err := fabric.Create(&taskfabric.Task{
		ID:          "t1",
		Capability:  "code",
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 2},
	}); err != nil {
		t.Fatalf("create task: %v", err)
	}

	// ── 5. agent-A runs quantum 1 → yields → SUSPENDED + checkpoint ─────
	if state := waitFabricState(t, fabric, "t1", taskfabric.StateSuspended, 8*time.Second); state != taskfabric.StateSuspended {
		t.Fatalf("task must yield to SUSPENDED after quantum 1, got %s", state)
	}
	tk, err := fabric.Task("t1")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	// Root submission (fabric.Create directly, no agent caller): Origin must
	// be empty — agent-created tasks are stamped by the create_task syscall.
	if tk.Origin != "" {
		t.Fatalf("root task origin = %q, want \"\" (no agent creator)", tk.Origin)
	}
	dc, err := taskfabric.DecodeCheckpoint(tk.Checkpoint)
	if err != nil {
		t.Fatalf("decode checkpoint: %v", err)
	}
	step, ok := dc.StepCheckpoint.(map[string]any)
	if !ok || step["phase"] != "investigation-done" {
		t.Fatalf("checkpoint must be preserved after yield, got %v", dc.StepCheckpoint)
	}

	// ── 6. Chaos: kill the real execution body ──────────────────────────
	if err := agents.Kill(ctx, "agent-A"); err != nil {
		t.Fatalf("chaos kill agent-A: %v", err)
	}

	// ── 7. Lease expiry → recovery → replacement resumes → COMPLETED ────
	advance(7 * time.Minute) // past the scheduler's 5-minute lease TTL
	if state := waitFabricState(t, fabric, "t1", taskfabric.StateCompleted, 10*time.Second); state != taskfabric.StateCompleted {
		t.Fatalf("task must complete after recovery, got %s", state)
	}

	// ── 8. Assertions ───────────────────────────────────────────────────
	replacementMu.Lock()
	rep := replacement
	replacementMu.Unlock()
	if rep == nil {
		t.Fatal("W1 recovery must create a replacement execution body")
	}
	if resumed := rep.resumed(); resumed == nil {
		t.Fatal("replacement must RESUME from the preserved checkpoint (not restart)")
	} else if phase, ok := resumed.(map[string]any); !ok || phase["phase"] != "investigation-done" {
		t.Fatalf("replacement resumed from the wrong checkpoint: %v", resumed)
	}
	if missing := taskEvents.waitForEvents(2*time.Second,
		ares_events.EventTaskCreated,
		ares_events.EventTaskAcquired,
		ares_events.EventTaskYielded,
		ares_events.EventTaskCompleted,
	); len(missing) > 0 {
		t.Fatalf("event stream must carry task.created/acquired/yielded/completed, missing %v, got %v",
			missing, taskEvents.snapshot())
	}
	if !agentSink.contains(agentfabric.EventAgentKilled) {
		t.Fatalf("agent event stream must carry agent.killed, got %v", agentSink.snapshot())
	}
	// Leader OFF: the whole run used only taskfabric + agentfabric + the
	// scheduler — no leader dispatcher, no planner participated.
	t.Logf("Grand loop PASS (Leader OFF): created→acquired→yielded→killed→recovered→completed")
}
