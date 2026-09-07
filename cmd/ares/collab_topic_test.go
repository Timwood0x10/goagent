package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/agents/base"
	"github.com/Timwood0x10/ares/internal/agents/sub"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/runtime/protocol/ahp"
	"github.com/Timwood0x10/ares/internal/taskfabric"
)

// collabStubAgent satisfies sub.Agent minimally: the kernel-routed
// collaboration path never calls Execute/ExecuteStep on it (work runs as a
// fabric task via the capability executor), but the interface is required by
// wireEvolutionIPC's signature.
type collabStubAgent struct {
	id  string
	typ models.AgentType
}

func (a *collabStubAgent) ID() string                    { return a.id }
func (a *collabStubAgent) Type() models.AgentType        { return a.typ }
func (a *collabStubAgent) Start(_ context.Context) error { return nil }
func (a *collabStubAgent) Stop(_ context.Context) error  { return nil }
func (a *collabStubAgent) Status() models.AgentStatus    { return models.AgentStatusReady }
func (a *collabStubAgent) Process(_ context.Context, _ any) (any, error) {
	return nil, nil // kernel-routed path never calls this on the stub
}
func (a *collabStubAgent) ProcessStream(_ context.Context, _ any) (<-chan base.AgentEvent, error) {
	return nil, nil // ditto
}
func (a *collabStubAgent) Execute(_ context.Context, _ *models.Task) (*models.TaskResult, error) {
	return nil, nil // kernel-routed path never calls this on the stub
}
func (a *collabStubAgent) ExecuteStep(_ context.Context, _ *models.Task) (*sub.StepOutcome, error) {
	return nil, nil // kernel-routed path never calls this on the stub
}
func (a *collabStubAgent) SendMessage(_ context.Context, _ *ahp.AHPMessage) error {
	return nil // kernel-routed path never sends FROM the stub
}

// TestCollabTopicRoutesThroughKernelFabric locks fusion-plan C2: with the peer
// kernel wired, an IPC collaboration topic message executes as a KERNEL FABRIC
// task (observable in the fabric under the collab-ipc-* id) and the reply
// preserves the legacy TaskResult shape — protocol unchanged, engine unified.
func TestCollabTopicRoutesThroughKernelFabric(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handler, kh := newGraphTestKernel(t, ctx)
	_ = handler

	// Execution-time probe: the ephemeral lifecycle deletes collab tasks when
	// runCollabGraph RETURNS, so "ran through the fabric" must be observed
	// DURING execution, not after.
	probe := &kernelFabricProbe{fabric: kh.fabric, t: t,
		inner: &chaosStubExecutor{id: "probe-inner", typ: models.AgentType("research")}}
	kh.scheduler.RegisterExecutor("peer-research", probe)

	bridge, err := wireEvolutionIPC(
		[]sub.Agent{&collabStubAgent{id: "peer-research", typ: "research"}},
		nil, nil, kh,
	)
	if err != nil {
		t.Fatalf("wireEvolutionIPC: %v", err)
	}

	reply, err := bridge.ipc.Bus().Request(ctx, "coordinator", "peer-research",
		"delegate-task",
		map[string]any{taskIDKey: "tk-77", "payload": map[string]any{"input": "do it"}},
		5*time.Second)
	if err != nil {
		t.Fatalf("delegate-task via bus: %v", err)
	}

	// Protocol preserved: reply payload is still a TaskResult.
	res, ok := reply.Payload.(*models.TaskResult)
	if !ok {
		t.Fatalf("reply payload type = %T, want *models.TaskResult", reply.Payload)
	}
	if res.Reason == "" {
		t.Fatalf("reply result empty: %+v", res)
	}
	// Reply topic convention unchanged.
	if reply.Topic != "delegate-task-reply" {
		t.Fatalf("reply topic = %q", reply.Topic)
	}

	if !probe.sawFabricTask() {
		t.Fatal("collaboration work did not execute as a kernel fabric task")
	}
}

// kernelFabricProbe observes, AT EXECUTION TIME, that its quantum runs as a
// real fabric task (the ephemeral lifecycle deletes it on return).
type kernelFabricProbe struct {
	fabric *taskfabric.Fabric
	t      *testing.T
	inner  CapabilityExecutor
	mu     sync.Mutex
	saw    bool
}

func (p *kernelFabricProbe) sawFabricTask() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.saw
}

func (p *kernelFabricProbe) ID() string             { return "peer-research" }
func (p *kernelFabricProbe) Type() models.AgentType { return "research" }
func (p *kernelFabricProbe) ExecuteStep(ctx context.Context, task *models.Task) (*sub.StepOutcome, error) {
	if tk, err := p.fabric.Task(task.TaskID); err == nil &&
		tk.State == taskfabric.StateRunning {
		p.mu.Lock()
		p.saw = true
		p.mu.Unlock()
	} else {
		p.t.Logf("probe: fabric task not RUNNING at quantum start (err=%v)", err)
	}
	return p.inner.ExecuteStep(ctx, task)
}

// recordingExecutor captures the fabric task id it is handed on every quantum,
// then delegates to an inner executor. It lets a test observe the concrete
// fabric task id a collaboration run generated (the ephemeral lifecycle
// deletes the task on return, so the id must be captured DURING execution).
type recordingExecutor struct {
	inner CapabilityExecutor
	mu    sync.Mutex
	ids   []string
}

func (e *recordingExecutor) ID() string             { return "peer-research" }
func (e *recordingExecutor) Type() models.AgentType { return "research" }
func (e *recordingExecutor) ExecuteStep(ctx context.Context, task *models.Task) (*sub.StepOutcome, error) {
	e.mu.Lock()
	e.ids = append(e.ids, task.TaskID)
	e.mu.Unlock()
	return e.inner.ExecuteStep(ctx, task)
}

func (e *recordingExecutor) seenIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.ids))
	copy(out, e.ids)
	return out
}

// TestCollabTopicRunIDsUniquePerInvocation locks the IPC-path runID fix
// (v0.4.0 review): executeCollabViaKernel must NOT derive the fabric run id
// solely from the caller-supplied task id. Two collaboration requests sharing
// a task id (a retry, or two leaders delegating the same logical task) must
// still produce DISTINCT fabric task ids — otherwise the second request's
// fabric.Create would hit ErrTaskExists, the exact collision class the HTTP
// handler was fixed to avoid. Prior to the fix both invocations generated the
// identical id "collab-ipc-<taskID>-exec"; the process-wide atomic sequence
// makes them differ. Sequential invocations with the SAME task id are enough
// to catch the regression: a deterministic id would repeat, a unique id does
// not. If someone reverts the id to "ipc-"+taskID, this test fails loudly.
func TestCollabTopicRunIDsUniquePerInvocation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, kh := newGraphTestKernel(t, ctx)
	rec := &recordingExecutor{inner: &chaosStubExecutor{id: "probe-inner", typ: models.AgentType("research")}}
	kh.scheduler.RegisterExecutor("peer-research", rec)

	bridge, err := wireEvolutionIPC(
		[]sub.Agent{&collabStubAgent{id: "peer-research", typ: "research"}},
		nil, nil, kh,
	)
	if err != nil {
		t.Fatalf("wireEvolutionIPC: %v", err)
	}

	// Two sequential requests carrying the SAME task id.
	const sharedTaskID = "tk-dup"
	for i := 0; i < 2; i++ {
		reply, rerr := bridge.ipc.Bus().Request(ctx, "coordinator", "peer-research",
			topicDelegateTask,
			map[string]any{taskIDKey: sharedTaskID, "payload": map[string]any{"input": "do it"}},
			5*time.Second)
		if rerr != nil {
			t.Fatalf("request %d with shared task id must succeed (a collision would surface here): %v", i, rerr)
		}
		if reply == nil {
			t.Fatalf("request %d: nil reply", i)
		}
	}

	ids := rec.seenIDs()
	if len(ids) != 2 {
		t.Fatalf("expected 2 fabric executions, got %d: %v", len(ids), ids)
	}
	if ids[0] == ids[1] {
		t.Fatalf("fabric run ids must differ across invocations (collision class regressed): both %q", ids[0])
	}
}

// TestCollabTopicNodeFailurePropagatesError locks the IPC error path that the
// happy-path routing test does not cover: when the delegated node's work fails
// after exhausting retries, executeCollabViaKernel must return an ERROR that
// the bus surfaces to the caller — not a silent nil reply that would let a
// leader treat a failed delegation as success.
func TestCollabTopicNodeFailurePropagatesError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, kh := newGraphTestKernel(t, ctx)
	kh.scheduler.RegisterExecutor("peer-flaky", &failingExecutor{id: "peer-flaky", typ: "flaky"})

	bridge, err := wireEvolutionIPC(
		[]sub.Agent{&collabStubAgent{id: "peer-flaky", typ: "flaky"}},
		nil, nil, kh,
	)
	if err != nil {
		t.Fatalf("wireEvolutionIPC: %v", err)
	}

	_, rerr := bridge.ipc.Bus().Request(ctx, "coordinator", "peer-flaky",
		topicDelegateTask,
		map[string]any{taskIDKey: "tk-fail", "payload": map[string]any{"input": "x"}},
		5*time.Second)
	if rerr == nil {
		t.Fatal("a failed delegation must propagate as an error, not a silent reply")
	}
}

// Interface-conformance guards for the stub used by wireEvolutionIPC.
var (
	_ base.Agent = (*collabStubAgent)(nil)
	_ sub.Agent  = (*collabStubAgent)(nil)
)
