package kernel

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/core/models"
	agentfabric "github.com/Timwood0x10/ares/internal/fabric/agent"
	taskfabric "github.com/Timwood0x10/ares/internal/fabric/task"
)

// TestLoadTrackerTryBegin pins the admission gate itself: TryBegin must be an
// atomic check-and-increment, must refuse at capacity without mutating the
// counter, and must interoperate with End/EndNeutral so a released slot becomes
// claimable again.
func TestLoadTrackerTryBegin(t *testing.T) {
	tr := NewLoadTracker()

	if !tr.TryBegin("a", 1) {
		t.Fatal("first TryBegin on an idle agent must succeed")
	}
	if tr.TryBegin("a", 1) {
		t.Fatal("second TryBegin must refuse when the agent is already at capacity")
	}
	if got := tr.Load("a"); got != 1 {
		t.Fatalf("a refused TryBegin must not touch the counter, load = %v", got)
	}

	// Release through the neutral path (preemption / gate rejection) and the
	// slot must be claimable again.
	tr.EndNeutral("a")
	if !tr.TryBegin("a", 1) {
		t.Fatal("TryBegin must succeed after the slot was released")
	}

	// A higher cap admits more than one concurrent holder.
	if !tr.TryBegin("a", 3) {
		t.Fatal("TryBegin with maxLoad 3 must admit a second holder")
	}
	if got := tr.Load("a"); got != 2 {
		t.Fatalf("load = %v, want 2", got)
	}
}

// overlapProbe is a Cognition that records how many of its quanta ran at the
// same time, so a test can assert the one-quantum-per-agent invariant from
// observed behaviour instead of trusting the scheduler's own bookkeeping.
type overlapProbe struct {
	mu      sync.Mutex
	active  int
	peak    int
	started int
}

func (p *overlapProbe) ExecuteStep(ctx context.Context, task *models.Task) (*agentfabric.StepOutcome, error) {
	p.mu.Lock()
	p.active++
	p.started++
	if p.active > p.peak {
		p.peak = p.active
	}
	p.mu.Unlock()

	// Hold the quantum so a second dispatch to the same agent would overlap
	// measurably rather than hide behind a fast step.
	select {
	case <-time.After(300 * time.Millisecond):
	case <-ctx.Done():
	}

	p.mu.Lock()
	p.active--
	p.mu.Unlock()

	res := models.NewTaskResult(task.TaskID, task.AgentType)
	res.SetSuccess(nil, "done")
	return &agentfabric.StepOutcome{Done: true, Result: res}, nil
}

func (p *overlapProbe) stats() (peak, started int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.peak, p.started
}

// TestDrainNeverRunsTwoQuantaOnOneAgent locks the admission gate end to end.
//
// Bug scenario: the candidate snapshot is built before Schedule and the busy
// slot used to be taken only after it, so two concurrent drain goroutines could
// both read load == 0 for the same agent and hand it two different tasks. An
// agent is a process with one cognitive state, so those two quanta would mutate
// it concurrently. The drain-parallelism fix (drainLimit falling back to the
// fabric candidate count) is what made >1 goroutines normal in production, so
// the gate has to exist.
//
// The test forces the overlap deterministically: ONE agent (so both tasks must
// pick it) with drain parallelism pinned to 2.
func TestDrainNeverRunsTwoQuantaOnOneAgent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fabric := taskfabric.NewFabric()
	agents := agentfabric.NewFabric()
	probe := &overlapProbe{}
	if _, err := agents.Spawn(ctx, agentfabric.SpawnSpec{
		Identity:     "solo-worker",
		Capabilities: []string{"code"},
		CognitionFactory: func([]string) agentfabric.Cognition {
			return probe
		},
	}); err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	sched := New(fabric, map[string]CapabilityExecutor{}, NewLoadTracker())
	sched.PollInterval = 5 * time.Millisecond
	sched.WithAgentFabric(agents)
	sched.WithMaxConcurrent(2)

	// Both tasks must be READY before the scheduler starts, otherwise the first
	// drain sees a single task and the overlap window never opens.
	for _, id := range []string{"task-1", "task-2"} {
		if err := fabric.Create(&taskfabric.Task{
			ID:          id,
			Capability:  "code",
			RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 20},
		}); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
	}

	go sched.Run(ctx)

	for _, id := range []string{"task-1", "task-2"} {
		waitForTaskState(t, fabric, id, taskfabric.StateCompleted, 5*time.Second)
	}

	peak, started := probe.stats()
	if started != 2 {
		t.Fatalf("both tasks must reach the agent, quanta started = %d", started)
	}
	if peak > maxConcurrentPerAgent {
		t.Fatalf("agent ran %d quanta concurrently, want at most %d", peak, maxConcurrentPerAgent)
	}
}
