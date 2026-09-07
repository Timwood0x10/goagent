package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/agents/base"
	"github.com/Timwood0x10/ares/internal/agents/sub"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/fabric/agent"
	"github.com/Timwood0x10/ares/internal/fabric/task"
)

// stubAgent is a minimal sub.Agent that records executed tasks and reports a
// configurable result.
type stubAgent struct {
	id        string
	typ       models.AgentType
	executed  []string
	resultErr string
	mu        sync.Mutex
}

var _ sub.Agent = (*stubAgent)(nil)

func (a *stubAgent) ID() string                  { return a.id }
func (a *stubAgent) Type() models.AgentType      { return a.typ }
func (a *stubAgent) Status() models.AgentStatus  { return models.AgentStatusReady }
func (a *stubAgent) Start(context.Context) error { return nil }
func (a *stubAgent) Stop(context.Context) error  { return nil }
func (a *stubAgent) Process(context.Context, any) (any, error) {
	return nil, nil
}
func (a *stubAgent) ProcessStream(context.Context, any) (<-chan base.AgentEvent, error) {
	return nil, nil
}
func (a *stubAgent) Execute(_ context.Context, task *models.Task) (*models.TaskResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.executed = append(a.executed, task.TaskID)
	res := models.NewTaskResult(task.TaskID, task.AgentType)
	if a.resultErr != "" {
		res.SetError(a.resultErr)
	}
	return res, nil
}
func (a *stubAgent) ExecuteStep(_ context.Context, task *models.Task) (*sub.StepOutcome, error) {
	// The scheduler drives tasks quantum-by-quantum; a stub has no internal
	// loop, so the whole run completes in one quantum (same behavior as the
	// pre-quantum Execute path).
	res, _ := a.Execute(context.Background(), task)
	return &sub.StepOutcome{Done: true, Result: res}, nil
}
func (a *stubAgent) executedCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.executed)
}

// TestKernelSchedulerRunsFullFabricPath verifies the no-leader loop end to end:
// a task created in the fabric is drained by the scheduler, scheduled to a
// capable agent, executed via sub.Agent.Execute, and finalized COMPLETED.
func TestKernelSchedulerRunsFullFabricPath(t *testing.T) {
	f := taskfabric.NewFabric()
	executor := &stubAgent{id: "code_01", typ: models.AgentType("code")}
	sched := NewKernelScheduler(f, map[string]CapabilityExecutor{"code_01": executor}, nil)
	sched.PollInterval = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(ctx)

	// A task requiring the "code" capability (declared by the executor).
	if err := f.Create(&taskfabric.Task{
		ID:          "t1",
		Capability:  "code",
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 1},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Wait for the task to reach COMPLETED.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tk, err := f.Task("t1")
		if err == nil && tk.State == taskfabric.StateCompleted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	tk, err := f.Task("t1")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if tk.State != taskfabric.StateCompleted {
		t.Fatalf("want COMPLETED, got %s", tk.State)
	}
	if executor.executedCount() != 1 {
		t.Fatalf("executor must run the task once, got %d", executor.executedCount())
	}
}

// TestKernelSchedulerNoCapableCandidate verifies a task whose required
// capability no executor declares is not executed (the scheduler cannot
// schedule it, so it skips without a spurious failure).
func TestKernelSchedulerNoCapableCandidate(t *testing.T) {
	f := taskfabric.NewFabric()
	executor := &stubAgent{id: "code_01", typ: models.AgentType("code")}
	sched := NewKernelScheduler(f, map[string]CapabilityExecutor{"code_01": executor}, nil)
	sched.PollInterval = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(ctx)

	if err := f.Create(&taskfabric.Task{
		ID:          "t-rust",
		Capability:  "rust",
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 1},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Let the scheduler poll a few times; it must never execute the task.
	time.Sleep(150 * time.Millisecond)
	if executor.executedCount() != 0 {
		t.Fatalf("executor must not run an unschedulable task, got %d", executor.executedCount())
	}
	tk, err := f.Task("t-rust")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if tk.State != taskfabric.StateReady {
		t.Fatalf("unschedulable task stays READY, got %s", tk.State)
	}
}

// TestKernelSchedulerExecutionFailureRequeues verifies the fabric RetryPolicy
// drives requeueing: a failing executor requeues the task to READY, and a
// second drain retries it before exhausting the budget.
func TestKernelSchedulerExecutionFailureRequeues(t *testing.T) {
	f := taskfabric.NewFabric()
	executor := &stubAgent{id: "code_01", typ: models.AgentType("code"), resultErr: "boom"}
	sched := NewKernelScheduler(f, map[string]CapabilityExecutor{"code_01": executor}, nil)
	sched.PollInterval = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(ctx)

	if err := f.Create(&taskfabric.Task{
		ID:          "t-fail",
		Capability:  "code",
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 3},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The executor always errors; the fabric must requeue (READY again) until
	// the retry budget is exhausted, then finalize FAILED.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tk, err := f.Task("t-fail")
		if err == nil && (tk.State == taskfabric.StateFailed || tk.State == taskfabric.StateCompleted) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	tk, err := f.Task("t-fail")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if tk.State != taskfabric.StateFailed {
		t.Fatalf("want FAILED after retries exhausted, got %s", tk.State)
	}
	if executor.executedCount() == 0 {
		t.Fatal("executor must have been retried at least once")
	}
}

// blockingAgent is a stub sub.Agent whose Execute blocks for execDelay so the
// test can observe true concurrency: N tasks with a per-task delay complete in
// ~delay when drained in parallel, but ~N*delay when drained serially.
//
// Each agent is single-slot (one task at a time), so per-agent concurrency is
// always 1; true concurrency is observed through the SHARED meter: the peak
// number of agents executing simultaneously across the whole fleet. When the
// scheduler drains N ready tasks concurrently (work stealing), N agents are
// active at once (meter peak ≈ N); when it drains serially, the peak is 1.
type blockingAgent struct {
	id        string
	typ       models.AgentType
	execDelay time.Duration
	meter     *concurrencyMeter
	mu        sync.Mutex
	executed  []string
}

var _ sub.Agent = (*blockingAgent)(nil)

// concurrencyMeter tracks the peak number of concurrent executions across all
// agents sharing it (the fleet-wide work-stealing concurrency).
type concurrencyMeter struct {
	mu     sync.Mutex
	active int
	peak   int
}

func (m *concurrencyMeter) begin() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active++
	if m.active > m.peak {
		m.peak = m.active
	}
}

func (m *concurrencyMeter) end() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active--
}

func (m *concurrencyMeter) Peak() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.peak
}

func (a *blockingAgent) ID() string                  { return a.id }
func (a *blockingAgent) Type() models.AgentType      { return a.typ }
func (a *blockingAgent) Status() models.AgentStatus  { return models.AgentStatusReady }
func (a *blockingAgent) Start(context.Context) error { return nil }
func (a *blockingAgent) Stop(context.Context) error  { return nil }
func (a *blockingAgent) Process(context.Context, any) (any, error) {
	return nil, nil
}
func (a *blockingAgent) ProcessStream(context.Context, any) (<-chan base.AgentEvent, error) {
	return nil, nil
}
func (a *blockingAgent) Execute(ctx context.Context, task *models.Task) (*models.TaskResult, error) {
	a.mu.Lock()
	a.executed = append(a.executed, task.TaskID)
	a.mu.Unlock()
	a.meter.begin()
	defer a.meter.end()

	select {
	case <-time.After(a.execDelay):
	case <-ctx.Done():
	}
	return models.NewTaskResult(task.TaskID, task.AgentType), nil
}
func (a *blockingAgent) ExecuteStep(ctx context.Context, task *models.Task) (*sub.StepOutcome, error) {
	// Same single-quantum contract as Execute (see stubAgent.ExecuteStep).
	res, _ := a.Execute(ctx, task)
	return &sub.StepOutcome{Done: true, Result: res}, nil
}

func (a *blockingAgent) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.executed)
}

// TestKernelSchedulerConcurrentDrainWorkStealing verifies the scheduler drains
// multiple ready tasks CONCURRENTLY across agents (work stealing): with 3
// single-slot agents and 3 ready tasks, the fleet-wide concurrency peak must
// reach 3 (all agents busy at once) and every agent must execute its own task.
// Serial draining would yield a fleet peak of 1 and take ~3× the delay.
func TestKernelSchedulerConcurrentDrainWorkStealing(t *testing.T) {
	const numAgents = 3
	const perTaskDelay = 200 * time.Millisecond

	f := taskfabric.NewFabric()
	executors := make(map[string]CapabilityExecutor, numAgents)
	agents := make([]*blockingAgent, 0, numAgents)
	meter := &concurrencyMeter{}
	for i := 0; i < numAgents; i++ {
		cap := fmt.Sprintf("cap-%d", i)
		ag := &blockingAgent{id: fmt.Sprintf("agent_%d", i), typ: models.AgentType(cap), execDelay: perTaskDelay, meter: meter}
		agents = append(agents, ag)
		executors[ag.id] = ag
	}
	sched := NewKernelScheduler(f, executors, nil)
	sched.PollInterval = 10 * time.Millisecond
	sched.WithMaxConcurrent(numAgents)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	go sched.Run(ctx)

	// One task per agent capability — all ready at once.
	for i := 0; i < numAgents; i++ {
		if err := f.Create(&taskfabric.Task{
			ID:          fmt.Sprintf("t-%d", i),
			Capability:  fmt.Sprintf("cap-%d", i),
			RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 1},
		}); err != nil {
			t.Fatalf("Create t-%d: %v", i, err)
		}
	}

	// All tasks must complete.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		allDone := true
		for i := 0; i < numAgents; i++ {
			tk, err := f.Task(fmt.Sprintf("t-%d", i))
			if err != nil || tk.State != taskfabric.StateCompleted {
				allDone = false
				break
			}
		}
		if allDone {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	elapsed := time.Since(start)
	for i := 0; i < numAgents; i++ {
		tk, err := f.Task(fmt.Sprintf("t-%d", i))
		if err != nil || tk.State != taskfabric.StateCompleted {
			t.Fatalf("task t-%d must complete, got %+v err=%v", i, tk, err)
		}
	}

	// Concurrent execution: N tasks with a per-task delay must finish well
	// under N*delay (serial would take ~600ms; parallel ~200ms + drain jitter).
	if elapsed >= numAgents*perTaskDelay {
		t.Fatalf("tasks must drain in parallel: elapsed=%v, serial would be ~%v",
			elapsed.Round(time.Millisecond), (numAgents * perTaskDelay).Round(time.Millisecond))
	}
	// Work stealing: the fleet concurrency peak must reach N (all agents busy
	// at once). Serial draining would leave the peak at 1.
	if peak := meter.Peak(); peak < numAgents {
		t.Fatalf("work stealing must run all %d agents concurrently, fleet peak = %d", numAgents, peak)
	}
	// Every agent picked up its own task (work distributed, not serialized).
	for _, ag := range agents {
		if got := ag.count(); got != 1 {
			t.Fatalf("agent %s must execute its task once, got %d", ag.id, got)
		}
	}
}

// yieldAgent is a sub.Agent stub that yields on the first quantum (returning
// a resumable checkpoint) and completes on the second, recording the
// checkpoint it received on resume — the proof that the scheduler round-trips
// the PCB across a SUSPENDED boundary.
type yieldAgent struct {
	id       string
	typ      models.AgentType
	mu       sync.Mutex
	steps    int
	lastCkpt any
}

var _ sub.Agent = (*yieldAgent)(nil)

func (a *yieldAgent) ID() string                  { return a.id }
func (a *yieldAgent) Type() models.AgentType      { return a.typ }
func (a *yieldAgent) Status() models.AgentStatus  { return models.AgentStatusReady }
func (a *yieldAgent) Start(context.Context) error { return nil }
func (a *yieldAgent) Stop(context.Context) error  { return nil }
func (a *yieldAgent) Process(context.Context, any) (any, error) {
	return nil, nil
}
func (a *yieldAgent) ProcessStream(context.Context, any) (<-chan base.AgentEvent, error) {
	return nil, nil
}
func (a *yieldAgent) Execute(_ context.Context, task *models.Task) (*models.TaskResult, error) {
	res := models.NewTaskResult(task.TaskID, task.AgentType)
	res.SetSuccess(nil, "done")
	return res, nil
}
func (a *yieldAgent) ExecuteStep(_ context.Context, task *models.Task) (*sub.StepOutcome, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.steps++
	if a.steps == 1 {
		// Quantum 1: progress only — yield with a resumable PCB.
		return &sub.StepOutcome{Checkpoint: map[string]any{"step": 1}}, nil
	}
	// Quantum 2: resumed from the previous checkpoint, now complete.
	a.lastCkpt = task.Payload["checkpoint"]
	res := models.NewTaskResult(task.TaskID, task.AgentType)
	res.SetSuccess([]*models.RecommendItem{{ItemID: "i1", Category: "general", Name: "R"}}, "done after 2 quanta")
	return &sub.StepOutcome{Done: true, Result: res}, nil
}
func (a *yieldAgent) stepCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.steps
}
func (a *yieldAgent) resumeCheckpoint() any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastCkpt
}

// TestKernelSchedulerQuantumYieldResume is the core yield/resume contract at the
// scheduler level: a task whose first quantum yields must SUSPEND (recorded as
// a TaskYielded event), then resume with the checkpoint round-tripped and
// complete — exactly the "Task A → Quantum#1 → checkpoint → yield → Quantum#2
// → complete" acceptance scenario, driven through the real scheduler.
func TestKernelSchedulerQuantumYieldResume(t *testing.T) {
	f := taskfabric.NewFabric()
	y := &yieldAgent{id: "code_01", typ: models.AgentType("code")}
	sched := NewKernelScheduler(f, map[string]CapabilityExecutor{"code_01": y}, nil)
	sched.PollInterval = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(ctx)

	if err := f.Create(&taskfabric.Task{
		ID:          "t-yield",
		Capability:  "code",
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 1},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tk, err := f.Task("t-yield")
		if err == nil && tk.State == taskfabric.StateCompleted {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	tk, err := f.Task("t-yield")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if tk.State != taskfabric.StateCompleted {
		t.Fatalf("task must complete across 2 quanta, got %s", tk.State)
	}
	// The fabric must have recorded a yield boundary (Quantum#1 → SUSPENDED).
	gotYielded := false
	for _, ev := range f.Events() {
		if ev.Type == taskfabric.EventTaskYielded && ev.TaskID == "t-yield" {
			gotYielded = true
		}
	}
	if !gotYielded {
		t.Fatal("task must have yielded once (EventTaskYielded) before completing")
	}
	if got := y.stepCount(); got != 2 {
		t.Fatalf("task must run exactly 2 quanta (yield + resume), got %d", got)
	}
	// Quantum 2 must have observed the checkpoint that quantum 1 produced.
	if ck := y.resumeCheckpoint(); ck == nil {
		t.Fatal("resumed quantum must receive the quantum-1 checkpoint")
	} else if m, ok := ck.(map[string]any); !ok || m["step"] != 1 {
		t.Fatalf("resumed quantum must see the previous PCB, got %#v", ck)
	}
}

// TestKernelSchedulerCapabilityPicksCorrectAgent (capability matching): when two
// agents declare different capabilities, a task must be scheduled to the
// capable one — never to the other, however idle.
func TestKernelSchedulerCapabilityPicksCorrectAgent(t *testing.T) {
	f := taskfabric.NewFabric()
	codeAgent := &stubAgent{id: "code_01", typ: models.AgentType("code")}
	docsAgent := &stubAgent{id: "docs_01", typ: models.AgentType("docs")}
	sched := NewKernelScheduler(f, map[string]CapabilityExecutor{"code_01": codeAgent, "docs_01": docsAgent}, nil)
	sched.PollInterval = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(ctx)

	if err := f.Create(&taskfabric.Task{
		ID:          "t-code",
		Capability:  "code",
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 1},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tk, err := f.Task("t-code")
		if err == nil && tk.State == taskfabric.StateCompleted {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	tk, err := f.Task("t-code")
	if err != nil || tk.State != taskfabric.StateCompleted {
		t.Fatalf("t-code must complete, got %+v err=%v", tk, err)
	}
	if got := codeAgent.executedCount(); got != 1 {
		t.Fatalf("code agent must execute the code task, ran %d", got)
	}
	if got := docsAgent.executedCount(); got != 0 {
		t.Fatalf("docs agent must never run a code task, ran %d", got)
	}
}

// TestKernelSchedulerWorkStealingPicksIdleCapableAgent (work stealing): work
// stealing in the shared-queue substrate is load-aware — a candidate at full
// load (Load=1) scores 0, so an idle capable agent picks up the work it
// cannot accept. The busy agent is marked busy via the shared load tracker
// (drains are serialized by wg.Wait, so a mid-flight agent is modeled by its
// Load, exactly what Schedule reads). Both code-capability tasks must be
// executed by the idle agent while busy_01 stays at Load=1.
func TestKernelSchedulerWorkStealingPicksIdleCapableAgent(t *testing.T) {
	f := taskfabric.NewFabric()
	tracker := newLoadTracker()
	// busy_01 holds a (simulated) running task: Load=1, so Score(busy)=0.
	tracker.Begin("busy_01")
	defer tracker.End("busy_01", true)

	busy := &stubAgent{id: "busy_01", typ: models.AgentType("code")}
	idle := &stubAgent{id: "idle_01", typ: models.AgentType("code")}
	sched := NewKernelScheduler(f, map[string]CapabilityExecutor{"busy_01": busy, "idle_01": idle}, tracker)
	sched.PollInterval = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(ctx)

	for _, id := range []string{"t-a", "t-b"} {
		if err := f.Create(&taskfabric.Task{
			ID:          id,
			Capability:  "code",
			RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 1},
		}); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		allDone := true
		for _, id := range []string{"t-a", "t-b"} {
			tk, err := f.Task(id)
			if err != nil || tk.State != taskfabric.StateCompleted {
				allDone = false
				break
			}
		}
		if allDone {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	for _, id := range []string{"t-a", "t-b"} {
		if tk, err := f.Task(id); err != nil || tk.State != taskfabric.StateCompleted {
			t.Fatalf("task %s must complete via the idle agent, got %+v err=%v", id, tk, err)
		}
	}
	if got := idle.executedCount(); got != 2 {
		t.Fatalf("idle capable agent must steal both tasks from the busy agent, idle ran %d", got)
	}
	if got := busy.executedCount(); got != 0 {
		t.Fatalf("busy agent at Load=1 must never be scheduled, ran %d", got)
	}
}

// TestKernelSchedulerIncapableAgentCannotSteal (capability guard): an idle agent
// that does not declare the required capability must never pick up a task,
// even when the capable agent is busy — the task waits for a capable executor.
func TestKernelSchedulerIncapableAgentCannotSteal(t *testing.T) {
	const busyDelay = 300 * time.Millisecond
	f := taskfabric.NewFabric()
	busy := &blockingAgent{id: "code_01", typ: models.AgentType("code"), execDelay: busyDelay, meter: &concurrencyMeter{}}
	docs := &stubAgent{id: "docs_01", typ: models.AgentType("docs")}
	sched := NewKernelScheduler(f, map[string]CapabilityExecutor{"code_01": busy, "docs_01": docs}, nil)
	sched.PollInterval = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(ctx)

	if err := f.Create(&taskfabric.Task{
		ID:          "t-slow",
		Capability:  "code",
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 1},
	}); err != nil {
		t.Fatalf("Create t-slow: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if busy.count() == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if busy.count() != 1 {
		t.Fatal("code_01 must pick up t-slow")
	}

	// A second code task arrives while code_01 is busy. docs_01 is idle but
	// incapable (Score=0) — it must not steal; the task completes only after
	// code_01 frees up and runs it serially.
	if err := f.Create(&taskfabric.Task{
		ID:          "t-code-2",
		Capability:  "code",
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 1},
	}); err != nil {
		t.Fatalf("Create t-code-2: %v", err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tk, err := f.Task("t-code-2")
		if err == nil && tk.State == taskfabric.StateCompleted {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	tk, err := f.Task("t-code-2")
	if err != nil || tk.State != taskfabric.StateCompleted {
		t.Fatalf("t-code-2 must complete on the capable agent, got %+v err=%v", tk, err)
	}
	if got := docs.executedCount(); got != 0 {
		t.Fatalf("incapable docs agent must never steal a code task, ran %d", got)
	}
	if got := busy.count(); got != 2 {
		t.Fatalf("capable code agent must run both code tasks serially, ran %d", got)
	}
}

// TestKernelSchedulerP3GovernanceYieldsOnBudgetExhausted verifies the governance
// wiring at the scheduler boundary: when the winning agent's tool budget is
// exhausted, the pre-quantum gate yields the task back (Release → READY)
// instead of running it, and the agent never executes again.
func TestKernelSchedulerP3GovernanceYieldsOnBudgetExhausted(t *testing.T) {
	ctx := context.Background()
	f := taskfabric.NewFabric()

	// An agent fabric with a governed agent: tool budget 1, so the FIRST
	// quantum passes the gate and consumes, the SECOND is gated out.
	agents := agentfabric.NewFabric()
	if _, err := agents.Spawn(ctx, agentfabric.SpawnSpec{
		Identity:   "code_01",
		Governance: agentfabric.Governance{ToolBudget: 1},
	}); err != nil {
		t.Fatalf("spawn governed agent: %v", err)
	}

	executor := &stubAgent{id: "code_01", typ: models.AgentType("code")}
	sched := NewKernelScheduler(f, map[string]CapabilityExecutor{"code_01": executor}, nil)
	sched.PollInterval = 5 * time.Millisecond
	sched.WithGovernance(agents)

	sctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(sctx)

	if err := f.Create(&taskfabric.Task{
		ID:          "t1",
		Capability:  "code",
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 3},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The task must be COMPLETED (1 quantum ran and finished it).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tk, err := f.Task("t1")
		if err == nil && tk.State == taskfabric.StateCompleted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	tk, err := f.Task("t1")
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if tk.State != taskfabric.StateCompleted {
		t.Fatalf("want COMPLETED after first quantum, got %s", tk.State)
	}
	if got := executor.executedCount(); got != 1 {
		t.Fatalf("governed agent must run exactly once, ran %d", got)
	}

	// Second task: budget is now exhausted → gate yields, task stays READY,
	// executor never runs again.
	if err := f.Create(&taskfabric.Task{
		ID:          "t2",
		Capability:  "code",
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 3},
	}); err != nil {
		t.Fatalf("Create t2: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if got := executor.executedCount(); got != 1 {
		t.Fatalf("budget-exhausted agent must not run t2, ran %d", got)
	}
}

// TestKernelSchedulerP3GovernanceDeadlineYields verifies the deadline arm of
// the governance gate: a deadline-expired agent has its task released back to READY.
func TestKernelSchedulerP3GovernanceDeadlineYields(t *testing.T) {
	ctx := context.Background()
	f := taskfabric.NewFabric()

	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	agents := agentfabric.NewFabric().WithClock(func() time.Time { return now })
	if _, err := agents.Spawn(ctx, agentfabric.SpawnSpec{
		Identity:   "code_01",
		Governance: agentfabric.Governance{Deadline: time.Minute},
	}); err != nil {
		t.Fatalf("spawn governed agent: %v", err)
	}

	executor := &stubAgent{id: "code_01", typ: models.AgentType("code")}
	sched := NewKernelScheduler(f, map[string]CapabilityExecutor{"code_01": executor}, nil)
	sched.PollInterval = 5 * time.Millisecond
	sched.WithGovernance(agents)

	sctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go sched.Run(sctx)

	// First task completes within the deadline.
	if err := f.Create(&taskfabric.Task{
		ID: "t1", Capability: "code", RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 3},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		tk, err := f.Task("t1")
		if err == nil && tk.State == taskfabric.StateCompleted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Advance the clock past the deadline; the next task must be gated out.
	now = now.Add(2 * time.Minute)
	if err := f.Create(&taskfabric.Task{
		ID: "t2", Capability: "code", RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 3},
	}); err != nil {
		t.Fatalf("Create t2: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if got := executor.executedCount(); got != 1 {
		t.Fatalf("deadline-expired agent must not run t2, ran %d", got)
	}
}
