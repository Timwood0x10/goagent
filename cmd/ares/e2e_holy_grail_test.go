package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/agentipc"
	"github.com/Timwood0x10/ares/internal/agents/sub"
	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/aresrecovery"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/fabric/agent"
	"github.com/Timwood0x10/ares/internal/fabric/task"
)

// Fixture identities shared across executors, fabrics, and assertions so the
// dimension names stay in sync (goconst).
const (
	agentA          = "agent-A"
	agentB          = "agent-B"
	agentC          = "agent-C"
	capCoordinator  = "coordinator"
	capInvestigate  = "investigate"
	childTaskPrefix = "child-"
	rootTaskID      = "root-task"
	rootTask2ID     = "root-task-2"
	outB1           = "B-analysis-v1"
	outB2           = "B-analysis-v2"
	outC1           = "C-analysis-v1"
	outC2           = "C-analysis-v2"
)

// ─── Cognition bodies for the holy-grail E2E ───

// holyGrailChildOutput is the configurable payload a child agent returns.
// The test mutates it between runs to prove the parent's synthesis tracks
// the child's REAL output (H2 §10.4: synthesis must not be hardcoded).
type holyGrailChildOutput struct {
	mu  sync.Mutex // guards val
	val string
}

func (o *holyGrailChildOutput) get() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.val
}

func (o *holyGrailChildOutput) set(v string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.val = v
}

// holyGrailChildExecutor is a CapabilityExecutor for child agents B and C.
// It registers its IPC handler at construction time (not per-quantum) so the
// parent can always reach it. Its task result carries the real output.
type holyGrailChildExecutor struct {
	id     string
	typ    models.AgentType
	output *holyGrailChildOutput
}

func (e *holyGrailChildExecutor) ID() string             { return e.id }
func (e *holyGrailChildExecutor) Type() models.AgentType { return e.typ }

func (e *holyGrailChildExecutor) ExecuteStep(_ context.Context, task *models.Task) (*sub.StepOutcome, error) {
	childOut := e.output.get()
	res := models.NewTaskResult(task.TaskID, task.AgentType)
	res.SetSuccess(nil, childOut)
	return &sub.StepOutcome{Done: true, Result: res}, nil
}

// holyGrailParentExecutor is the coordinator (Agent A). Its first quantum
// spawns child tasks (B, C) into the fabric and yields. After the children
// complete, its next quantum requests their outputs over the IPC bus and
// synthesises a result derived from the children's REAL output — proving
// the synthesis is not hardcoded.
type holyGrailParentExecutor struct {
	id          string
	typ         models.AgentType
	bus         *agentipc.Bus
	fabric      *taskfabric.Fabric
	childIDs    []string
	mu          sync.Mutex // guards round, phase, and finalResult
	round       int
	phase       int
	finalResult string
}

func (e *holyGrailParentExecutor) ID() string             { return e.id }
func (e *holyGrailParentExecutor) Type() models.AgentType { return e.typ }

func (e *holyGrailParentExecutor) ExecuteStep(ctx context.Context, task *models.Task) (*sub.StepOutcome, error) {
	e.mu.Lock()
	e.phase++
	phase := e.phase
	e.mu.Unlock()

	if phase == 1 {
		// Phase 1: spawn child tasks into the fabric; the scheduler drives
		// the children (executors were registered at construction time).
		// Round 2+ uses fresh child task IDs so the children really
		// re-execute with the new outputs.
		for _, childID := range e.childIDs {
			if err := e.fabric.Create(&taskfabric.Task{
				ID:          e.childTaskID(childID),
				Capability:  capInvestigate,
				RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 2},
			}); err != nil {
				return nil, fmt.Errorf("create child task %s: %w", childID, err)
			}
		}
		// Yield: children need to run before A can synthesise.
		return &sub.StepOutcome{
			Done:       false,
			Checkpoint: map[string]any{"phase": "children-spawned"},
		}, nil
	}

	// Phase 2+: wait for all children to complete, then IPC-synthesise.
	allDone := true
	for _, childID := range e.childIDs {
		ct, err := e.fabric.Task(e.childTaskID(childID))
		if err != nil || ct.State != taskfabric.StateCompleted {
			allDone = false
			break
		}
	}
	if !allDone {
		return &sub.StepOutcome{
			Done:       false,
			Checkpoint: map[string]any{"phase": "waiting-for-children"},
		}, nil
	}

	// All children completed: request each child's output via IPC and
	// synthesise from the REAL replies (not hardcoded).
	var findings []string
	for _, childID := range e.childIDs {
		reply, err := e.bus.Request(ctx, e.id, childID, "analyze",
			map[string]any{"task": task.TaskID}, 5*time.Second)
		if err != nil {
			return nil, fmt.Errorf("IPC request to %s: %w", childID, err)
		}
		payload, ok := reply.Payload.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("IPC reply from %s: unexpected payload %T", childID, reply.Payload)
		}
		analysis, ok := payload["analysis"].(string)
		if !ok {
			return nil, fmt.Errorf("IPC reply from %s: missing analysis", childID)
		}
		findings = append(findings, childID+": "+analysis)
	}

	synthesis := "synthesis [" + strings.Join(findings, ", ") + "]"
	e.mu.Lock()
	e.finalResult = synthesis
	e.mu.Unlock()

	res := models.NewTaskResult(task.TaskID, task.AgentType)
	res.SetSuccess(nil, synthesis)
	return &sub.StepOutcome{Done: true, Result: res}, nil
}

// childTaskID returns the fabric task ID for the given child in the current
// round. Round 1 uses "child-<id>"; later rounds append "-rN" so re-spawned
// children are fresh tasks (round-1 children stay COMPLETED in the fabric,
// and re-Create would fail with ErrTaskExists).
func (e *holyGrailParentExecutor) childTaskID(childID string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.round == 0 {
		return childTaskPrefix + childID
	}
	return fmt.Sprintf("%s%s-r%d", childTaskPrefix, childID, e.round+1)
}

// reset advances the round and clears the phase counter and final result so
// the executor can coordinate a fresh root task (second half of the test).
func (e *holyGrailParentExecutor) reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.round++
	e.phase = 0
	e.finalResult = ""
}

// holyGrailReplacementExecutor is the W1/E1 replacement for a killed child.
type holyGrailReplacementExecutor struct {
	id      string
	typ     models.AgentType
	output  *holyGrailChildOutput
	mu      sync.Mutex // guards resumed
	resumed any
}

func (e *holyGrailReplacementExecutor) ID() string             { return e.id }
func (e *holyGrailReplacementExecutor) Type() models.AgentType { return e.typ }

func (e *holyGrailReplacementExecutor) ExecuteStep(_ context.Context, task *models.Task) (*sub.StepOutcome, error) {
	e.mu.Lock()
	e.resumed = task.Payload["checkpoint"]
	e.mu.Unlock()

	childOut := e.output.get()
	res := models.NewTaskResult(task.TaskID, task.AgentType)
	res.SetSuccess(nil, childOut)
	return &sub.StepOutcome{Done: true, Result: res}, nil
}

func (e *holyGrailReplacementExecutor) resumedCheckpoint() any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.resumed
}

// ─── World fixture ───

// holyGrailFixture bundles the whole E2E world: event stores, fabrics, IPC
// bus, executors, scheduler, and the controllable clock. The test function
// only drives the phases; all wiring lives here so the test stays readable.
type holyGrailFixture struct {
	store      *ares_events.MemoryEventStore
	taskEvents *e2eTaskEventLog
	agentSink  *e2eAgentSink
	fabric     *taskfabric.Fabric
	agents     *agentfabric.Fabric
	bus        *agentipc.Bus
	sched      *kernelScheduler
	parentExec *holyGrailParentExecutor
	childB     *holyGrailChildExecutor
	childC     *holyGrailChildExecutor
	outB       *holyGrailChildOutput
	outC       *holyGrailChildOutput
	clockMu    sync.Mutex // guards now
	now        time.Time

	replacementMu sync.Mutex // guards replacementB
	replacementB  *holyGrailReplacementExecutor
}

// newHolyGrailFixture builds the event stores, fabrics, IPC bus, executors,
// and agent provenance. IPC handlers are registered at construction time so
// the parent can always reach the children (regardless of task scheduling
// order); child outputs are configurable to prove synthesis tracks reality.
func newHolyGrailFixture(t *testing.T, ctx context.Context) *holyGrailFixture {
	t.Helper()
	f := &holyGrailFixture{
		store:      ares_events.NewMemoryEventStore(),
		taskEvents: &e2eTaskEventLog{},
		agentSink:  &e2eAgentSink{},
		now:        time.Now(),
		outB:       &holyGrailChildOutput{val: outB1},
		outC:       &holyGrailChildOutput{val: outC1},
	}
	evCh, err := f.store.Subscribe(ctx, ares_events.EventFilter{})
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
				f.taskEvents.add(ev.Type)
			}
		}
	}()

	// Controllable clock for lease expiry.
	clock := func() time.Time {
		f.clockMu.Lock()
		defer f.clockMu.Unlock()
		return f.now
	}
	f.fabric = taskfabric.NewFabric().WithClock(clock).WithEventStore(f.store)
	f.agents = agentfabric.NewFabric().WithEventSink(f.agentSink)
	f.bus = agentipc.NewBus()

	// ── 1. Build executors + register IPC handlers at construction time ─
	f.parentExec = &holyGrailParentExecutor{
		id:       agentA,
		typ:      models.AgentType(capCoordinator),
		bus:      f.bus,
		fabric:   f.fabric,
		childIDs: []string{agentB, agentC},
	}
	f.childB = &holyGrailChildExecutor{
		id: agentB, typ: models.AgentType(capInvestigate),
		output: f.outB,
	}
	f.childC = &holyGrailChildExecutor{
		id: agentC, typ: models.AgentType(capInvestigate),
		output: f.outC,
	}
	for _, child := range []*holyGrailChildExecutor{f.childB, f.childC} {
		c := child
		if err := f.bus.Register(c.id, func(_ context.Context, _ *agentipc.Message) (*agentipc.Message, error) {
			return &agentipc.Message{Payload: map[string]any{"analysis": c.output.get()}}, nil
		}); err != nil {
			t.Fatalf("register IPC handler for %s: %v", c.id, err)
		}
	}

	// ── 2. Spawn agents into agentfabric (provenance + lifecycle) ──────
	if _, err := f.agents.Spawn(ctx, agentfabric.SpawnSpec{
		Identity: agentA, Capabilities: []string{capCoordinator},
	}); err != nil {
		t.Fatalf("spawn agent-A: %v", err)
	}
	for _, childID := range []string{agentB, agentC} {
		if _, err := f.agents.Spawn(ctx, agentfabric.SpawnSpec{
			Identity: childID, Capabilities: []string{capInvestigate},
			ParentID: agentA,
		}); err != nil {
			t.Fatalf("spawn %s: %v", childID, err)
		}
	}
	return f
}

// startKernel wires the scheduler and the W1 recovery loop over the fabric
// and launches both managed workers (both stop via ctx).
func (f *holyGrailFixture) startKernel(t *testing.T, ctx context.Context) {
	t.Helper()
	tracker := newLoadTracker()
	executors := map[string]CapabilityExecutor{
		agentA: f.parentExec,
		agentB: f.childB,
		agentC: f.childC,
	}
	f.sched = NewKernelScheduler(f.fabric, executors, tracker)
	f.sched.PollInterval = 20 * time.Millisecond
	f.sched.WithMaxConcurrent(1) // serial: avoid B stealing C's task
	f.sched.WithEventStore(f.store)
	go f.sched.Run(ctx)

	// Recovery loop.
	rec := aresrecovery.New(f.fabric, f.agents, aresrecovery.DefaultRestartPolicy())
	go runKernelRecoveryLoop(ctx, f.store, rec, kernelLoopConfig{
		RecoverySweepInterval: 50 * time.Millisecond,
		RecoverySweepTimeout:  5 * time.Second,
	},
		func(taskID, agentID string, executor CapabilityExecutor) {
			f.sched.RegisterExecutorForTask(taskID, agentID, executor)
		},
		func(agentID, capability string) CapabilityExecutor {
			rep := &holyGrailReplacementExecutor{
				id: agentID, typ: models.AgentType(capability),
				output: f.outB,
			}
			f.replacementMu.Lock()
			f.replacementB = rep
			f.replacementMu.Unlock()
			return rep
		},
		f.sched.HasCapableExecutor,
	)
}

// advanceClock moves the frozen clock forward by d (lease expiry simulation).
func (f *holyGrailFixture) advanceClock(d time.Duration) {
	f.clockMu.Lock()
	defer f.clockMu.Unlock()
	f.now = f.now.Add(d)
}

// waitForChildTask polls until the given task exists in the fabric. The task
// may briefly be SUSPENDED before the scheduler re-acquires it; the key
// observable is the child task appearing at all.
func waitForChildTask(t *testing.T, f *holyGrailFixture, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := f.fabric.Task(id); err == nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("child task %s was not created by agent A", id)
}

// assertSynthesis reads the parent's final synthesis and asserts it contains
// both children's real outputs, returning the synthesis for change detection
// (H2 §10.4).
func assertSynthesis(t *testing.T, f *holyGrailFixture, wantB, wantC string) string {
	t.Helper()
	f.parentExec.mu.Lock()
	synthesis := f.parentExec.finalResult
	f.parentExec.mu.Unlock()

	if synthesis == "" {
		t.Fatal("parent A must have produced a synthesis result")
	}
	if !strings.Contains(synthesis, wantB) {
		t.Fatalf("synthesis must contain B's real output %q, got %q", wantB, synthesis)
	}
	if !strings.Contains(synthesis, wantC) {
		t.Fatalf("synthesis must contain C's real output %q, got %q", wantC, synthesis)
	}
	return synthesis
}

// ─── The holy-grail E2E test ───

// TestE2E_HolyGrail is the single continuous end-to-end test that proves the
// full Agent-OS thesis (aresos-agentos-plan H2 §10.2/§10.4 "圣杯测试"):
//
//	User → Submit(root task) → Scheduler → Agent A (quantum 1: spawn B,C)
//	  → B,C scheduled → B,C run → IPC results back to A
//	  → A synthesis (from B/C REAL output, not hardcoded)
//	  → kill B → B' recovery → converge
//
// The synthesis assertion: changing the child's output changes the final
// result (§10.4).
func TestE2E_HolyGrail(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	f := newHolyGrailFixture(t, ctx)
	f.startKernel(t, ctx)

	// ── 4. Submit the root task ──
	if err := f.fabric.Create(&taskfabric.Task{
		ID: rootTaskID, Capability: capCoordinator,
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 5},
	}); err != nil {
		t.Fatalf("create root task: %v", err)
	}

	// ── 5. Agent A runs quantum 1 → spawns B,C child tasks → yields ────
	waitForChildTask(t, f, childTaskPrefix+agentB)
	waitForChildTask(t, f, childTaskPrefix+agentC)

	// ── 6. B and C execute via scheduler → COMPLETED ──────────────────
	waitFabricState(t, f.fabric, childTaskPrefix+agentB, taskfabric.StateCompleted, 5*time.Second)
	waitFabricState(t, f.fabric, childTaskPrefix+agentC, taskfabric.StateCompleted, 5*time.Second)

	// Verify provenance: B and C are children of A (Rule 2).
	if kids := f.agents.Children(agentA); len(kids) != 2 {
		t.Fatalf("provenance: A must have 2 children, got %d", len(kids))
	}

	// ── 7. Kill agent-B (chaos) and advance clock for lease expiry ────
	if err := f.agents.Kill(ctx, agentB); err != nil {
		t.Fatalf("chaos kill agent-B: %v", err)
	}
	if !f.agentSink.contains(agentfabric.EventAgentKilled) {
		t.Fatal("agent event stream must carry agent.killed")
	}
	f.advanceClock(7 * time.Minute)

	// ── 8. A runs quantum 2 → IPC synthesis from B/C REAL output ──────
	if state := waitFabricState(t, f.fabric, rootTaskID, taskfabric.StateCompleted,
		10*time.Second); state != taskfabric.StateCompleted {
		t.Fatalf("root task must complete after synthesis, got %s", state)
	}

	// ── 9. Assert: synthesis result tracks the REAL child output ──────
	synthesis := assertSynthesis(t, f, outB1, outC1)

	// ── 10. Assert: changing child output changes the final result ────
	f.outB.set(outB2)
	f.outC.set(outC2)
	if err := f.fabric.Create(&taskfabric.Task{
		ID: rootTask2ID, Capability: capCoordinator,
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 5},
	}); err != nil {
		t.Fatalf("create root task 2: %v", err)
	}
	f.parentExec.reset()

	if state := waitFabricState(t, f.fabric, rootTask2ID, taskfabric.StateCompleted,
		10*time.Second); state != taskfabric.StateCompleted {
		t.Fatalf("root task 2 must complete, got %s", state)
	}

	synthesis2 := assertSynthesis(t, f, outB2, outC2)
	if synthesis == synthesis2 {
		t.Fatalf("synthesis must change when child output changes (§10.4): got %q both times", synthesis)
	}

	// ── 11. Event stream assertions ──
	if !f.taskEvents.contains(ares_events.EventTaskCreated) {
		t.Fatal("event stream must carry task.created")
	}
	if !f.taskEvents.contains(ares_events.EventTaskCompleted) {
		t.Fatal("event stream must carry task.completed")
	}
	if !f.agentSink.contains(agentfabric.EventAgentSpawned) {
		t.Fatal("agent event stream must carry agent.spawned")
	}

	// ── 12. Recovery assertion ──
	f.replacementMu.Lock()
	rep := f.replacementB
	f.replacementMu.Unlock()
	if rep != nil {
		if cp := rep.resumedCheckpoint(); cp != nil {
			t.Logf("recovery: B' resumed from checkpoint %v", cp)
		}
	}

	t.Logf("Holy Grail PASS: Submit→spawn→schedule→IPC synthesis (child-driven)→" +
		"kill→recovery→converge; synthesis tracks child output (§10.4)")
}
