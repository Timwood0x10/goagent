package main

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/agentipc"
	"github.com/Timwood0x10/ares/internal/agents/base"
	"github.com/Timwood0x10/ares/internal/agents/sub"
	"github.com/Timwood0x10/ares/internal/ares_bootstrap"
	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/aresrecovery"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/fabric/agent"
	"github.com/Timwood0x10/ares/internal/fabric/task"
)

// TestTaskFromPayload verifies payload decoding: agent_type is honored, absent
// metadata falls back to a default type.
func TestTaskFromPayload(t *testing.T) {
	task, err := taskFromPayload("t1", map[string]any{"agent_type": "rust"})
	if err != nil {
		t.Fatalf("taskFromPayload: %v", err)
	}
	if task.TaskID != "t1" || task.AgentType != models.AgentType("rust") {
		t.Fatalf("unexpected task: %+v", task)
	}
	if _, err := taskFromPayload("", nil); err == nil {
		t.Fatal("empty task id must error")
	}
	if _, err := taskFromPayload("t2", nil); err != nil {
		t.Fatalf("nil payload must not error: %v", err)
	}
}

// TestTaskFromPayloadRestoresDependencies verifies the DAG wiring: the kernel
// dispatch payload carries Context.Dependencies through the agentipc hop so
// executeFabricTask can create the fabric task with its DAG edges.
func TestTaskFromPayloadRestoresDependencies(t *testing.T) {
	cases := []struct {
		name string
		// deps is the payload value for "dependencies": []string comes from
		// kernelTaskDispatcher.Dispatch (in-memory hop), []any comes from a
		// JSON round-trip.
		deps any
	}{
		{name: "in-memory []string", deps: []string{"task_a", "task_b"}},
		{name: "json-shaped []any", deps: []any{"task_a", "task_b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{
				"agent_type":   "write",
				"dependencies": tc.deps,
			}
			task, err := taskFromPayload("t1", payload)
			if err != nil {
				t.Fatalf("taskFromPayload: %v", err)
			}
			if !slicesEqual(task.Context.Dependencies, []string{"task_a", "task_b"}) {
				t.Fatalf("dependencies not restored: %v", task.Context.Dependencies)
			}
		})
	}
}

// TestKernelDAGGateDefersDependentTask verifies the DAG-as-scheduling-source
// wiring in the kernel path : the leader dispatch SUBMITS
// tasks to the fabric (submitFabricTask) and the kernelScheduler drains only
// READY tasks — a task whose dependencies are not all COMPLETED stays queued
// until its dependency completes (it never executes out of order).
func TestKernelDAGGateDefersDependentTask(t *testing.T) {
	f := taskfabric.NewFabric()
	research := &stubAgent{id: "research_01", typ: models.AgentType("tool/research")}
	writer := &stubAgent{id: "writer_01", typ: models.AgentType("tool/write")}
	executors := map[string]CapabilityExecutor{"research_01": research, "writer_01": writer}
	tracker := newLoadTracker()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The scheduler drains READY tasks; the DAG gate means B waits for A.
	sched := NewKernelScheduler(f, executors, tracker)
	sched.PollInterval = 10 * time.Millisecond
	go sched.Run(ctx)

	// Submit B (depends on A) first: it must NOT run before A completes.
	b := models.NewTask("task_b", models.AgentType("tool/write"), nil)
	b.Context.Dependencies = []string{"task_a"}
	if err := submitFabricTask(ctx, f, b); err != nil {
		t.Fatalf("submitFabricTask(B): %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	if writer.executedCount() != 0 {
		t.Fatal("B must not execute before its dependency A completes")
	}

	// Submit A: the scheduler runs it, completing A and unlocking B.
	a := models.NewTask("task_a", models.AgentType("tool/research"), nil)
	if err := submitFabricTask(ctx, f, a); err != nil {
		t.Fatalf("submitFabricTask(A): %v", err)
	}

	// Both tasks must complete, in dependency order.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if research.executedCount() == 1 && writer.executedCount() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if research.executedCount() != 1 {
		t.Fatalf("A must execute once, got %d", research.executedCount())
	}
	if writer.executedCount() != 1 {
		t.Fatalf("B must execute exactly once after A completes, got %d", writer.executedCount())
	}
}

// slicesEqual is a tiny helper comparing two string slices (test-only).
func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestEnableKernelExecutionRunsFabricPath verifies that after flipping to the
// Task Fabric policy, the kernel's new path executes the task through the
// fabric (Create→Schedule→Acquire→RunQuantum) instead of scoring only, and
// that shadow mode is turned off (so the legacy path is not re-run).
func TestEnableKernelExecutionRunsFabricPath(t *testing.T) {
	kernel, flag := wireKernelDispatcher([]subAgentCapability{
		{ID: "code_01", Type: "tool/code"},
	})

	f := taskfabric.NewFabric()
	executor := &stubAgent{id: "code_01", typ: models.AgentType("tool/code")}
	executors := map[string]CapabilityExecutor{"code_01": executor}
	tracker := newLoadTracker()

	// Attach the submit-only new path and disable shadow. The dispatch now
	// SUBMITS the task; the kernelScheduler is the single executor (no
	// double-path acquire race).
	enableKernelExecution(kernel, f)
	flag.Set(agentipc.PolicyTaskFabric)

	// Dispatch through the kernel's live path: the facade's current new-path
	// dispatcher (the DualTrack dispatch entry was removed — zero production
	// callers; enableKernelExecution swaps the path via SetNewPath).
	payload := map[string]any{"agent_type": "tool/code"}
	if err := kernel.NewPath().D(context.Background(), "", "t1", payload); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// After dispatch the task is SUBMITTED (READY), not yet executed — the
	// scheduler must complete it.
	tk, err := f.Task("t1")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if tk.State != taskfabric.StateReady {
		t.Fatalf("after dispatch want READY (submitted, scheduler owns execution), got %s", tk.State)
	}
	if executor.executedCount() != 0 {
		t.Fatalf("dispatch must NOT execute the task (scheduler owns execution), got %d", executor.executedCount())
	}

	// Now the kernelScheduler drains it to completion (single executor).
	sched := NewKernelScheduler(f, executors, tracker)
	sched.PollInterval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(ctx)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tk, err = f.Task("t1")
		if err == nil && tk.State == taskfabric.StateCompleted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	tk, err = f.Task("t1")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if tk.State != taskfabric.StateCompleted {
		t.Fatalf("want COMPLETED after scheduler drain, got %s", tk.State)
	}
	if executor.executedCount() != 1 {
		t.Fatalf("executor must run once, got %d", executor.executedCount())
	}
}

// TestRunKernelRecoveryLoopEventDriven verifies the event-driven recovery loop:
// task lifecycle events on the shared EventStore
// drive the recovery chain instead of a command loop. Publishing an
// EventTaskExpired event triggers the kernel's requeue-only recovery
// (a review Bug 1 fix): the expired task returns to READY, UNOWNED — NOT re-leased
// to a phantom replacement agent that no registered executor can drive. The
// kernelScheduler (which owns execution) picks up the READY task and resumes
// from its preserved checkpoint.
func TestRunKernelRecoveryLoopEventDriven(t *testing.T) {
	store := ares_events.NewMemoryEventStore()
	defer func() { _ = store.Close() }()

	// A task fabric with one READY task whose lease is expired.
	tf := taskfabric.NewFabric().WithEventStore(store)
	if err := tf.Create(&taskfabric.Task{ID: "t1", Capability: "code"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Acquire to put it on a lease.
	if _, err := tf.Acquire("t1", "agent-a", 5*time.Minute); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	// Expire the lease by advancing the clock past the TTL.
	tf.WithClock(func() time.Time { return time.Now().Add(10 * time.Minute) })

	agents := agentfabric.NewFabric()
	recovery := aresrecovery.New(tf, agents, aresrecovery.DefaultRestartPolicy())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runKernelRecoveryLoop(ctx, store, recovery, kernelLoopConfig{}, nil, nil, nil)

	// Publish the TaskExpired event (as CheckExpiredLeases would) and wait for
	// the requeue: the expired lease returns to READY, unowned. The OLD
	// behavior handed it to a freshly-spawned replacement agent (LEASED to a
	// phantom — see Bug 1); the kernel now requeues only and lets the
	// scheduler re-acquire with registered executors.
	if err := store.Append(ctx, "t1", []*ares_events.Event{
		{Type: ares_events.EventTaskExpired, StreamID: "t1", Payload: map[string]any{}},
	}, 0); err != nil {
		t.Fatalf("append expired event: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tk, err := tf.Task("t1")
		if err == nil && tk.State == taskfabric.StateReady && tk.Owner == "" {
			return // recovered: requeued to READY, awaiting a registered executor
		}
		time.Sleep(20 * time.Millisecond)
	}
	tk, err := tf.Task("t1")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	t.Fatalf("task must be requeued to READY unowned after recovery (no phantom agent), state=%s owner=%q", tk.State, tk.Owner)
}

// flakyQuotaSource blocks the FIRST ActiveQuotaPolicy call until its context
// is cancelled (a policy store that hangs once), then answers subsequent calls
// normally. Used by TestRunKernelQuotaLoopSurvivesBlockedApply.
type flakyQuotaSource struct {
	mu    sync.Mutex
	calls int
}

func (s *flakyQuotaSource) ActiveQuotaPolicy(ctx context.Context) (aresrecovery.QuotaPolicy, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	if call == 1 {
		<-ctx.Done() // block until the caller's timeout cancels ctx
		return aresrecovery.QuotaPolicy{}, ctx.Err()
	}
	return aresrecovery.QuotaPolicy{}, nil
}

func (s *flakyQuotaSource) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// TestRunKernelQuotaLoopSurvivesBlockedApply: a quota Apply that hangs on
// the policy store must be bounded by the loop's per-apply timeout — the loop
// must keep ticking instead of spinning forever on a single blocked Apply.
func TestRunKernelQuotaLoopSurvivesBlockedApply(t *testing.T) {
	source := &flakyQuotaSource{}
	mgr := aresrecovery.NewEvolutionAwareQuotaManager(agentfabric.NewFabric(), source)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := kernelLoopConfig{
		QuotaApplyInterval: 20 * time.Millisecond,
		QuotaApplyTimeout:  50 * time.Millisecond,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		runKernelQuotaLoop(ctx, mgr, cfg)
	}()

	// The first Apply blocks (50ms), then the ticker must drive a second
	// Apply — proof the loop survived the stalled call.
	deadline := time.Now().Add(2 * time.Second)
	for source.callCount() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := source.callCount(); got < 2 {
		t.Fatalf("quota loop must survive a blocked Apply and tick again, calls=%d", got)
	}
	cancel()
	<-done
}

// TestKernelDispatchReleasesResultSubscription: after Dispatch returns,
// the waitCtx-bounded result subscription must be released. Subscribing with
// the raw parent ctx would leave every completed Dispatch's subscription — and
// its cleanup goroutine — alive until the parent context is cancelled,
// accumulating across dispatches.
// Bug 3 fix: a yielded task's checkpoint is the meta envelope re-wrapped by the
// scheduler's quantum step, so toModelTask can still restore UserProfile/
// Payload/UsedExperienceID on resume (the old code type-asserted a plain map
// after a yield and degraded the executor to executeByType).
func TestToModelTaskPreservesMetaAcrossYieldCheckpoint(t *testing.T) {
	up := models.NewUserProfile("u1", "alice")
	up.Style = []models.StyleTag{models.StyleMinimalist}
	meta := &taskfabric.CheckpointEnvelope{
		UserProfile:      up,
		Payload:          map[string]any{"task_desc": "pick"},
		UsedExperienceID: "exp-1",
		// What RunQuantum stores after a yield: the step's durable progress.
		StepCheckpoint: map[string]any{"step": 2},
	}
	s := NewKernelScheduler(taskfabric.NewFabric(), nil, nil)

	tk := &taskfabric.Task{ID: "t1", Capability: "code", Checkpoint: meta}
	model := s.ToModelTask(tk)
	if model.UserProfile == nil || model.UserProfile.UserID != "u1" {
		t.Fatalf("resume must restore UserProfile, got %+v", model.UserProfile)
	}
	if model.UsedExperienceID != "exp-1" {
		t.Fatalf("resume must restore UsedExperienceID, got %q", model.UsedExperienceID)
	}
	step, ok := model.Payload["checkpoint"].(map[string]any)
	if !ok || step["step"] != float64(2) && step["step"] != 2 {
		t.Fatalf("resume must surface the step checkpoint in payload, got %#v", model.Payload)
	}
	// The initial (pre-quantum) envelope without StepCheckpoint must also still
	// restore the profile, and must not invent a checkpoint key.
	initModel := s.ToModelTask(&taskfabric.Task{ID: "t1", Capability: "code", Checkpoint: &taskfabric.CheckpointEnvelope{
		UserProfile: up, Payload: map[string]any{"task_desc": "pick"}, UsedExperienceID: "exp-1",
	}})
	if initModel.UserProfile == nil || initModel.UsedExperienceID != "exp-1" {
		t.Fatalf("initial envelope must restore meta, got profile=%+v exp=%q", initModel.UserProfile, initModel.UsedExperienceID)
	}
	if _, hasKey := initModel.Payload["checkpoint"]; hasKey {
		t.Fatal("initial envelope must not expose a checkpoint key")
	}
}

// TestRetryPolicyAllowsOneRetry verifies the retry-budget semantics:
// submitFabricTask now grants ONE real retry (MaxRetries counts total
// attempts, so 2 = first attempt + one retry). A transient failure requeues
// the task to READY; only the second failure finalizes FAILED.
func TestRetryPolicyAllowsOneRetry(t *testing.T) {
	f := taskfabric.NewFabric()
	task := models.NewTask("t-retry", models.AgentType("tool/code"), nil)
	if err := submitFabricTask(context.Background(), f, task); err != nil {
		t.Fatalf("submitFabricTask: %v", err)
	}
	tk, err := f.Task("t-retry")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if tk.RetryPolicy.MaxRetries != 2 {
		t.Fatalf("submitFabricTask must grant 1 retry (MaxRetries=2), got %d", tk.RetryPolicy.MaxRetries)
	}
	// First execution fails → requeued to READY (the retry).
	epoch, err := f.Acquire("t-retry", "agent-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	if err := f.Start("t-retry", "agent-a", epoch); err != nil {
		t.Fatalf("start 1: %v", err)
	}
	if err := f.Fail("t-retry", "agent-a", epoch); err != nil {
		t.Fatalf("fail 1: %v", err)
	}
	tk, _ = f.Task("t-retry")
	if tk.State != taskfabric.StateReady {
		t.Fatalf("first failure must requeue (1 retry granted), got state %s", tk.State)
	}
	// Second execution fails → budget exhausted → FAILED.
	epoch, err = f.Acquire("t-retry", "agent-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	if err := f.Start("t-retry", "agent-a", epoch); err != nil {
		t.Fatalf("start 2: %v", err)
	}
	if err := f.Fail("t-retry", "agent-a", epoch); err != nil {
		t.Fatalf("fail 2: %v", err)
	}
	tk, _ = f.Task("t-retry")
	if tk.State != taskfabric.StateFailed {
		t.Fatalf("second failure must finalize FAILED, got state %s", tk.State)
	}
}

// TestSchedulerPriorityPreemption verifies the priority wiring:
// fabric.Preempt is exercised from the scheduler — a RUNNING low-priority task
// is cooperatively handed back to READY (checkpoint preserved) when a READY
// high-priority task arrives, freeing the executor for the next drain.
func TestSchedulerPriorityPreemption(t *testing.T) {
	f := taskfabric.NewFabric()
	// A low-priority task that is RUNNING (executor busy across a drain).
	if err := f.Create(&taskfabric.Task{ID: "low", Capability: "code", Priority: 1}); err != nil {
		t.Fatalf("create low: %v", err)
	}
	epoch, err := f.Acquire("low", "agent-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire low: %v", err)
	}
	if err := f.Start("low", "agent-a", epoch); err != nil {
		t.Fatalf("start low: %v", err)
	}
	if err := f.Yield("low", "agent-a", epoch, map[string]any{"step": 3}); err != nil {
		t.Fatalf("yield low (checkpoint): %v", err)
	}
	// Re-acquire to RUNNING so there is a RUNNING task to preempt.
	epoch, err = f.Acquire("low", "agent-a", time.Minute)
	if err != nil {
		t.Fatalf("re-acquire low: %v", err)
	}
	if err := f.Start("low", "agent-a", epoch); err != nil {
		t.Fatalf("re-start low: %v", err)
	}
	// A higher-priority READY task.
	if err := f.Create(&taskfabric.Task{ID: "high", Capability: "code", Priority: 10}); err != nil {
		t.Fatalf("create high: %v", err)
	}
	rt := f.RunningTasks()
	if len(rt) != 1 || rt[0].ID != "low" {
		t.Fatalf("want exactly one running task (low), got %+v", rt)
	}

	s := NewKernelScheduler(f, map[string]CapabilityExecutor{"agent-a": &stubAgent{id: "agent-a", typ: models.AgentType("code")}}, nil)
	s.PreemptLowerPriority([]string{"high"})

	tk, err := f.Task("low")
	if err != nil {
		t.Fatalf("Task low: %v", err)
	}
	if tk.State != taskfabric.StateReady {
		t.Fatalf("low-priority RUNNING task must be preempted to READY, got %s", tk.State)
	}
	if tk.Checkpoint == nil {
		t.Fatal("preempted task must preserve its checkpoint")
	}
	// No-op on ties / unset priorities: a RUNNING task is never churned.
	if err := f.Create(&taskfabric.Task{ID: "tie", Capability: "code", Priority: 0}); err != nil {
		t.Fatalf("create tie: %v", err)
	}
	epoch, err = f.Acquire("tie", "agent-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire tie: %v", err)
	}
	if err := f.Start("tie", "agent-a", epoch); err != nil {
		t.Fatalf("start tie: %v", err)
	}
	s.PreemptLowerPriority([]string{"tie"})
	tk, _ = f.Task("tie")
	if tk.State != taskfabric.StateRunning {
		t.Fatal("zero-priority preempt must not churn a running task")
	}
}

// TestTaskFromPayloadRestoresJSONUserProfile verifies (kernel side) that
// a user_profile that survived a JSON round-trip arrives as
// a plain map, not a *models.UserProfile. taskFromPayload must still restore it
// so the executor never degrades to executeByType.
func TestTaskFromPayloadRestoresJSONUserProfile(t *testing.T) {
	up := models.NewUserProfile("u1", "alice")
	raw, err := json.Marshal(map[string]any{"user_profile": up})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := payload["user_profile"].(map[string]any); !ok {
		t.Fatalf("test precondition: user_profile must be a plain map after round-trip, got %T", payload["user_profile"])
	}
	task, err := taskFromPayload("t-json", payload)
	if err != nil {
		t.Fatalf("taskFromPayload: %v", err)
	}
	if task.UserProfile == nil || task.UserProfile.UserID != "u1" {
		t.Fatalf("JSON round-tripped profile must be restored, got %+v", task.UserProfile)
	}
}

// checkpointStubAgent is a sub.Agent stub that records the checkpoint it
// observes on resume, so the recovery E2E test can assert that a
// replacement executor sees the dead agent's preserved checkpoint.
type checkpointStubAgent struct {
	id         string
	typ        models.AgentType
	mu         sync.Mutex
	executed   []string
	checkpoint any
	observed   bool
}

var _ sub.Agent = (*checkpointStubAgent)(nil)

func (a *checkpointStubAgent) ID() string                  { return a.id }
func (a *checkpointStubAgent) Type() models.AgentType      { return a.typ }
func (a *checkpointStubAgent) Status() models.AgentStatus  { return models.AgentStatusReady }
func (a *checkpointStubAgent) Start(context.Context) error { return nil }
func (a *checkpointStubAgent) Stop(context.Context) error  { return nil }
func (a *checkpointStubAgent) Process(context.Context, any) (any, error) {
	return nil, nil
}
func (a *checkpointStubAgent) ProcessStream(context.Context, any) (<-chan base.AgentEvent, error) {
	return nil, nil
}
func (a *checkpointStubAgent) Execute(_ context.Context, task *models.Task) (*models.TaskResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.executed = append(a.executed, task.TaskID)
	if task.Payload != nil {
		if cp, ok := task.Payload["checkpoint"]; ok && cp != nil {
			a.checkpoint = cp
			a.observed = true
		}
	}
	res := models.NewTaskResult(task.TaskID, task.AgentType)
	res.SetSuccess(nil, "recovery executor completed")
	return res, nil
}
func (a *checkpointStubAgent) ExecuteStep(ctx context.Context, task *models.Task) (*sub.StepOutcome, error) {
	res, err := a.Execute(ctx, task)
	if err != nil {
		return nil, err
	}
	return &sub.StepOutcome{Done: true, Result: res}, nil
}

func (a *checkpointStubAgent) didObserveCheckpoint() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.observed
}

func (a *checkpointStubAgent) getCheckpoint() any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.checkpoint
}

// TestW1RecoveryClosureE2E verifies the production-grade recovery闭环:
//
//	Task → executor A executes quantum#1 (writes checkpoint) → A crashes
//	(lease expiry) → recovery loop → replacement executor A' registered →
//	scheduler schedules A' → A' observes A's checkpoint → COMPLETE.
//
// The test proves:
//  1. The replacement executor is a real registered executor (not phantom).
//  2. The replacement observes the dead agent's preserved checkpoint.
//  3. The task completes via the new executor.
//  4. RequeueExpiredLeases → bound replacement registration have a caller
//     through the real recovery loop.
func TestW1RecoveryClosureE2E(t *testing.T) {
	// Build a fabric with one task.
	f := taskfabric.NewFabric()
	if err := f.Create(&taskfabric.Task{
		ID:          "w1-task",
		Capability:  "code",
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 2},
		Checkpoint: &taskfabric.CheckpointEnvelope{
			StepCheckpoint: map[string]any{"step": 1, "data": "quantum-1-output"},
		},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Acquire and yield to simulate quantum#1 execution with a checkpoint.
	epoch, err := f.Acquire("w1-task", "agent-A", time.Minute)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := f.Start("w1-task", "agent-A", epoch); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := f.Yield("w1-task", "agent-A", epoch, &taskfabric.CheckpointEnvelope{
		StepCheckpoint: map[string]any{"step": 1, "data": "quantum-1-output"},
	}); err != nil {
		t.Fatalf("yield: %v", err)
	}

	// Verify the task is SUSPENDED with the checkpoint preserved.
	tk, _ := f.Task("w1-task")
	if tk.State != taskfabric.StateSuspended {
		t.Fatalf("task must be SUSPENDED after yield, got %s", tk.State)
	}
	if tk.Checkpoint == nil {
		t.Fatal("task must have a preserved checkpoint after yield")
	}

	// Expire the lease to simulate agent A's death.
	f.WithClock(func() time.Time { return time.Now().Add(10 * time.Minute) })

	// Build the recovery subsystem and scheduler.
	agents := agentfabric.NewFabric()
	recovery := aresrecovery.New(f, agents, aresrecovery.DefaultRestartPolicy())

	// The replacement executor that will be created by the factory.
	replacement := &checkpointStubAgent{id: "replacement-A", typ: models.AgentType("code")}

	// Build the scheduler with NO initial executors (simulating A's death —
	// the only executor is gone). The recovery loop will register the
	// replacement dynamically.
	sched := NewKernelScheduler(f, map[string]CapabilityExecutor{}, nil)
	sched.PollInterval = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Register the replacement via the recovery loop's factory. The
	// registerFn binds the executor to the specific recovered task.
	registerFn := func(taskID, agentID string, executor CapabilityExecutor) {
		sched.RegisterExecutorForTask(taskID, agentID, executor)
	}
	// Override the replacement to use our checkpoint-recording stub.
	factoryFn := func(agentID, capability string) CapabilityExecutor {
		replacement.id = agentID
		return replacement
	}
	// No registered executor can resume the task (the scheduler starts empty),
	// so the recovery loop must spawn a replacement.
	hasCapable := func(taskID string) bool { return sched.HasCapableExecutor(taskID) }

	// Start the recovery loop with the full recovery chain.
	go runKernelRecoveryLoop(ctx, nil, recovery, kernelLoopConfig{
		RecoverySweepInterval: 50 * time.Millisecond,
		RecoverySweepTimeout:  5 * time.Second,
	}, registerFn, factoryFn, hasCapable)

	// Start the scheduler.
	go sched.Run(ctx)

	// Wait for the task to complete via the replacement executor.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		tk, err := f.Task("w1-task")
		if err == nil && tk.State == taskfabric.StateCompleted {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Assert: task completed.
	tk, err = f.Task("w1-task")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if tk.State != taskfabric.StateCompleted {
		t.Fatalf("task must be COMPLETED by replacement executor, got state %s", tk.State)
	}

	// Assert: the replacement executor observed the checkpoint from quantum#1.
	if !replacement.didObserveCheckpoint() {
		t.Fatal("replacement executor must observe the dead agent's checkpoint")
	}
	// The checkpoint rides inside a *taskfabric.CheckpointEnvelope. After the
	// fabric's yield→requeue→toModelTask path it arrives as a map[string]any
	// (the scheduler's toModelTask unwraps the envelope and places the
	// StepCheckpoint into payload["checkpoint"]) — the step data must be
	// preserved.
	cp := replacement.getCheckpoint()
	v, ok := cp.(map[string]any)
	if !ok {
		t.Fatalf("checkpoint must be map[string]any, got %T", cp)
	}
	if v["data"] != "quantum-1-output" {
		t.Fatalf("checkpoint data must be 'quantum-1-output', got %v", v["data"])
	}
}

// TestW1RegisterExecutorDynamic verifies the scheduler's dynamic executor
// registration: a task that was unschedulable (no capable candidate) becomes
// schedulable after RegisterExecutor injects a matching executor.
func TestW1RegisterExecutorDynamic(t *testing.T) {
	f := taskfabric.NewFabric()
	if err := f.Create(&taskfabric.Task{
		ID:          "reg-task",
		Capability:  "code",
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 1},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Scheduler with no executors — task is unschedulable.
	sched := NewKernelScheduler(f, map[string]CapabilityExecutor{}, nil)
	sched.PollInterval = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(ctx)

	// Wait a bit to confirm the task stays READY (no executor).
	time.Sleep(100 * time.Millisecond)
	tk, _ := f.Task("reg-task")
	if tk.State != taskfabric.StateReady {
		t.Fatalf("task must stay READY with no executor, got %s", tk.State)
	}

	// Dynamically register an executor.
	executor := &stubAgent{id: "dynamic-1", typ: models.AgentType("code")}
	sched.RegisterExecutor("dynamic-1", executor)

	// Wait for the task to be scheduled and completed.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tk, err := f.Task("reg-task")
		if err == nil && tk.State == taskfabric.StateCompleted {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	tk, _ = f.Task("reg-task")
	if tk.State != taskfabric.StateCompleted {
		t.Fatalf("task must be COMPLETED after dynamic executor registration, got %s", tk.State)
	}
}

// TestW1UnregisterExecutor verifies that unregistering an executor removes it
// from the scheduling candidate pool.
func TestW1UnregisterExecutor(t *testing.T) {
	f := taskfabric.NewFabric()
	executor := &stubAgent{id: "removable", typ: models.AgentType("code")}
	sched := NewKernelScheduler(f, map[string]CapabilityExecutor{"removable": executor}, nil)

	// Verify it's registered.
	if count := sched.ExecutorCount(); count != 1 {
		t.Fatalf("expected 1 executor, got %d", count)
	}

	// Unregister.
	sched.UnregisterExecutor("removable")
	if count := sched.ExecutorCount(); count != 0 {
		t.Fatalf("expected 0 executors after unregister, got %d", count)
	}

	// Lookup must return false.
	if _, ok := sched.LookupExecutor("removable"); ok {
		t.Fatal("lookup must return false after unregister")
	}
}

// TestSetupPeerRegistryRetainsOnKernel locks the retention contract: the peer
// registry built by setupPeerRegistry must be retained on the kernel handle
// instead of being discarded after construction.
func TestSetupPeerRegistryRetainsOnKernel(t *testing.T) {
	var comp ares_bootstrap.Components
	kernel := &kernelHandle{}

	// No evolution wired: the plain direct peer registry path is used.
	reg, err := setupPeerRegistry(nil, &comp, kernel)
	require.NoError(t, err)
	require.NotNil(t, reg, "setupPeerRegistry must return a usable registry")
	// The construction site must retain the registry on the kernel handle
	// so it stays reachable for direct peer messaging / capability
	// discovery.
	if kernel.peerRegistry != reg {
		t.Fatal("peer registry must be retained on the kernel handle (N4)")
	}
}
