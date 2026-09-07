package taskfabric

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// drive completes the named plan task under a synthetic worker lease, the
// same Acquire→Start→Complete path the kernel scheduler drives in production.
func drive(t *testing.T, f *Fabric, id string, checkpoint any) {
	t.Helper()
	epoch, err := f.Acquire(id, "worker", time.Minute)
	if err != nil {
		t.Fatalf("Acquire %s: %v", id, err)
	}
	if err := f.Start(id, "worker", epoch); err != nil {
		t.Fatalf("Start %s: %v", id, err)
	}
	if checkpoint != nil {
		if err := f.CompleteWithCheckpoint(id, "worker", epoch, checkpoint); err != nil {
			t.Fatalf("CompleteWithCheckpoint %s: %v", id, err)
		}
		return
	}
	if err := f.Complete(id, "worker", epoch); err != nil {
		t.Fatalf("Complete %s: %v", id, err)
	}
}

// failTask drives the named plan task to a terminal FAILED state (no retry
// budget, so the failure sticks).
func failTask(t *testing.T, f *Fabric, id string) {
	t.Helper()
	epoch, err := f.Acquire(id, "worker", time.Minute)
	if err != nil {
		t.Fatalf("Acquire %s: %v", id, err)
	}
	if err := f.Start(id, "worker", epoch); err != nil {
		t.Fatalf("Start %s: %v", id, err)
	}
	if err := f.Fail(id, "worker", epoch); err != nil {
		t.Fatalf("Fail %s: %v", id, err)
	}
}

// waitFor polls cond with a timeout — the code_rules §7.3 sanctioned
// alternative to sleep-based synchronization.
func waitFor(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", desc)
}

func newTestLoop(t *testing.T, f *Fabric, spec PlanLoopSpec) *PlanLoop {
	t.Helper()
	l, err := NewPlanLoop(f, spec, WithPlanInterval(2*time.Millisecond))
	if err != nil {
		t.Fatalf("NewPlanLoop: %v", err)
	}
	return l
}

func oneStepPlan(planID, stepID string) PlanLoopSpec {
	return PlanLoopSpec{
		PlanID:    planID,
		Steps:     []PlanStep{{ID: stepID, Capability: "coder", Origin: "test"}},
		MaxRounds: 3,
	}
}

func TestPlanLoopRunsAllRoundsThenStops(t *testing.T) {
	f := NewFabric()
	spec := oneStepPlan("plan-a", "build")
	l := newTestLoop(t, f, spec)
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Stop()

	for round := 1; round <= spec.MaxRounds; round++ {
		id := PlanTaskID("plan-a", round, "build")
		waitFor(t, fmt.Sprintf("round %d task %s ready", round, id), func() bool {
			task, err := f.Task(id)
			return err == nil && task.State == StateReady
		})
		drive(t, f, id, map[string]any{"round": round})
	}
	waitFor(t, "loop done", func() bool {
		select {
		case <-l.Done():
			return true
		default:
			return false
		}
	})
	if err := l.Err(); err != nil {
		t.Fatalf("loop ended with error: %v", err)
	}
	out, ok := l.LastOutcome()
	if !ok || out.Round != spec.MaxRounds {
		t.Fatalf("LastOutcome = %+v, ok=%v; want round %d", out, ok, spec.MaxRounds)
	}
	if out.Status["build"] != StateCompleted {
		t.Fatalf("final step status = %q, want COMPLETED", out.Status["build"])
	}
	// Every round produced its own namespaced task in the fabric.
	for round := 1; round <= spec.MaxRounds; round++ {
		if _, err := f.Task(PlanTaskID("plan-a", round, "build")); err != nil {
			t.Fatalf("round %d task missing: %v", round, err)
		}
	}
}

func TestPlanLoopUntilConditionStopsEarly(t *testing.T) {
	f := NewFabric()
	spec := oneStepPlan("plan-b", "build")
	spec.MaxRounds = 5
	spec.UntilCondition = func(o RoundOutcome) bool { return o.Round >= 2 }
	l := newTestLoop(t, f, spec)
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Stop()

	for round := 1; round <= 2; round++ {
		id := PlanTaskID("plan-b", round, "build")
		waitFor(t, fmt.Sprintf("round %d ready", round), func() bool {
			_, err := f.Task(id)
			return err == nil
		})
		drive(t, f, id, nil)
	}
	waitFor(t, "loop stopped at round 2", func() bool {
		select {
		case <-l.Done():
			return true
		default:
			return false
		}
	})
	if err := l.Err(); err != nil {
		t.Fatalf("loop error: %v", err)
	}
	if _, err := f.Task(PlanTaskID("plan-b", 3, "build")); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("round 3 task should not exist, got err=%v", err)
	}
}

func TestPlanLoopFailureContinuesAndAllSucceededStops(t *testing.T) {
	f := NewFabric()
	spec := PlanLoopSpec{
		PlanID: "plan-c",
		Steps: []PlanStep{
			// MaxRetries=1: the first failed attempt exhausts the budget, so
			// the task lands in terminal FAILED instead of requeueing READY
			// (CanRetry treats MaxRetries<=0 as unlimited).
			{ID: "probe", Capability: "coder", MaxRetries: 1},
		},
		MaxRounds: 4,
		// Round 1 fails on purpose; the loop must keep iterating and stop
		// on the first clean round (the declarative "all_succeeded" exit).
		UntilCondition: func(o RoundOutcome) bool { return o.Succeeded() },
	}
	l := newTestLoop(t, f, spec)
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Stop()

	id := PlanTaskID("plan-c", 1, "probe")
	waitFor(t, "round 1 ready", func() bool {
		_, err := f.Task(id)
		return err == nil
	})
	failTask(t, f, id)
	// Round 2 must still compile despite the failed round-1 task.
	id2 := PlanTaskID("plan-c", 2, "probe")
	waitFor(t, "round 2 compiled after failure", func() bool {
		task, err := f.Task(id2)
		return err == nil && task.State == StateReady
	})
	drive(t, f, id2, nil)
	waitFor(t, "loop stopped on clean round", func() bool {
		select {
		case <-l.Done():
			return true
		default:
			return false
		}
	})
	if err := l.Err(); err != nil {
		t.Fatalf("loop error: %v", err)
	}
	out, _ := l.LastOutcome()
	if out.Round != 2 || len(out.Failed) != 0 {
		t.Fatalf("final outcome = %+v; want clean round 2", out)
	}
}

func TestPlanLoopReplanDerivesNextRound(t *testing.T) {
	f := NewFabric()
	spec := PlanLoopSpec{
		PlanID:    "plan-d",
		Steps:     []PlanStep{{ID: "gen", Capability: "coder"}},
		MaxRounds: 2,
		Replan: func(prev RoundOutcome) ([]PlanStep, error) {
			return []PlanStep{{
				ID:         "gen",
				Capability: "reviewer",
				Payload:    map[string]any{"from_round": prev.Round},
			}}, nil
		},
	}
	l := newTestLoop(t, f, spec)
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Stop()

	id1 := PlanTaskID("plan-d", 1, "gen")
	waitFor(t, "round 1 ready", func() bool {
		_, err := f.Task(id1)
		return err == nil
	})
	drive(t, f, id1, map[string]any{"artifact": "v1"})

	id2 := PlanTaskID("plan-d", 2, "gen")
	waitFor(t, "replanned round 2", func() bool {
		task, err := f.Task(id2)
		return err == nil && task.Capability == "reviewer"
	})
	task, _ := f.Task(id2)
	env, ok := task.Checkpoint.(*CheckpointEnvelope)
	if !ok || env.Payload["from_round"] != 1 {
		t.Fatalf("replan payload not propagated: %#v", task.Checkpoint)
	}
	drive(t, f, id2, map[string]any{"artifact": "v2"})
	waitFor(t, "loop done", func() bool {
		select {
		case <-l.Done():
			return true
		default:
			return false
		}
	})
	out, _ := l.LastOutcome()
	if out.Output["gen"] == nil {
		t.Fatalf("round 2 output missing from outcome: %#v", out.Output)
	}
}

// TestPlanLoopReplanMayChangeStepSet locks the incremental-replanning
// contract: Replan is allowed to rename, add and drop steps, and the loop
// must then watch the tasks it actually compiled. Deriving the watched set
// from the base spec instead made the driver poll task IDs that were never
// created, so the round never finished and the loop hung silently.
func TestPlanLoopReplanMayChangeStepSet(t *testing.T) {
	f := NewFabric()
	spec := PlanLoopSpec{
		PlanID:    "plan-replan-set",
		Steps:     []PlanStep{{ID: "gen", Capability: "coder"}},
		MaxRounds: 2,
		Replan: func(RoundOutcome) ([]PlanStep, error) {
			// A different step id AND a wider batch than round 1.
			return []PlanStep{
				{ID: "review", Capability: "reviewer"},
				{ID: "fix", Capability: "coder", DependsOn: []string{"review"}},
			}, nil
		},
	}
	l := newTestLoop(t, f, spec)
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Stop()

	drive(t, f, PlanTaskID(spec.PlanID, 1, "gen"), nil)

	review := PlanTaskID(spec.PlanID, 2, "review")
	waitFor(t, "replanned round 2 compiled", func() bool {
		_, err := f.Task(review)
		return err == nil
	})
	drive(t, f, review, map[string]any{"verdict": "needs work"})
	fix := PlanTaskID(spec.PlanID, 2, "fix")
	waitFor(t, "dependent step ready", func() bool {
		task, err := f.Task(fix)
		return err == nil && task.State == StateReady
	})
	drive(t, f, fix, nil)

	waitFor(t, "loop observes the replanned round as finished", func() bool {
		select {
		case <-l.Done():
			return true
		default:
			return false
		}
	})
	if err := l.Err(); err != nil {
		t.Fatalf("loop error: %v", err)
	}
	out, ok := l.LastOutcome()
	if !ok {
		t.Fatal("no outcome recorded for the replanned round")
	}
	if out.Status["review"] != StateCompleted || out.Status["fix"] != StateCompleted {
		t.Fatalf("outcome must be keyed by the replanned step ids: %+v", out.Status)
	}
	if _, stale := out.Status["gen"]; stale {
		t.Fatalf("dropped step must not appear in the outcome: %+v", out.Status)
	}
}

// TestPlanLoopInvalidReplanBatchStopsLoop locks the replan validation: a
// batch that could never compile (empty, unnamed or duplicated step ids) ends
// the loop with an error instead of leaving the driver polling forever.
func TestPlanLoopInvalidReplanBatchStopsLoop(t *testing.T) {
	cases := []struct {
		name  string
		steps []PlanStep
	}{
		{"empty_batch", nil},
		{"missing_step_id", []PlanStep{{Capability: "coder"}}},
		{"duplicate_step_id", []PlanStep{{ID: "x", Capability: "a"}, {ID: "x", Capability: "b"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := NewFabric()
			planID := "plan-bad-replan-" + tc.name
			spec := PlanLoopSpec{
				PlanID:    planID,
				Steps:     []PlanStep{{ID: "gen", Capability: "coder"}},
				MaxRounds: 3,
				Replan:    func(RoundOutcome) ([]PlanStep, error) { return tc.steps, nil },
			}
			l := newTestLoop(t, f, spec)
			if err := l.Start(context.Background()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			defer l.Stop()
			drive(t, f, PlanTaskID(planID, 1, "gen"), nil)
			waitFor(t, "loop failed on invalid replan batch", func() bool { return l.Err() != nil })
		})
	}
}

// TestPlanLoopOutcomeSeparatesInputFromOutput locks the RoundOutcome.Output
// contract: only the executed step's own checkpoint counts as output. A step
// that merely carried a submission payload (CompilePlan stores it as a
// pre-execution envelope) must not look like it produced something, or Replan
// cannot tell "produced nothing" from "was given something".
func TestPlanLoopOutcomeSeparatesInputFromOutput(t *testing.T) {
	f := NewFabric()
	spec := PlanLoopSpec{
		PlanID: "plan-output",
		Steps: []PlanStep{
			{ID: "with_input", Capability: "coder", Payload: map[string]any{"task_desc": "build"}},
			{ID: "with_output", Capability: "coder"},
		},
		MaxRounds: 1,
	}
	l := newTestLoop(t, f, spec)
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Stop()

	// with_input completes without writing a checkpoint: its stored envelope
	// still holds the submission payload.
	drive(t, f, PlanTaskID(spec.PlanID, 1, "with_input"), nil)
	drive(t, f, PlanTaskID(spec.PlanID, 1, "with_output"), map[string]any{"artifact": "v1"})

	waitFor(t, "loop done", func() bool {
		select {
		case <-l.Done():
			return true
		default:
			return false
		}
	})
	out, ok := l.LastOutcome()
	if !ok {
		t.Fatal("no outcome recorded")
	}
	if _, leaked := out.Output["with_input"]; leaked {
		t.Fatalf("submission payload must not surface as output: %#v", out.Output)
	}
	produced, ok := out.Output["with_output"].(map[string]any)
	if !ok || produced["artifact"] != "v1" {
		t.Fatalf("executed step output missing: %#v", out.Output)
	}
}

// TestPlanLoopDeletedTaskCountsAsFailed locks the interference contract: a
// round task deleted behind the loop's back is terminal-FAILED, not an
// eternal wait. Fabric.Delete is a public API (CompilePlan's own rollback
// uses it), so this is a reachable state, and hanging on it would strand the
// plan for the whole serve lifetime.
func TestPlanLoopDeletedTaskCountsAsFailed(t *testing.T) {
	f := NewFabric()
	spec := PlanLoopSpec{
		PlanID:         "plan-deleted",
		Steps:          []PlanStep{{ID: "gen", Capability: "coder"}},
		MaxRounds:      1,
		UntilCondition: func(o RoundOutcome) bool { return o.Succeeded() },
	}
	l := newTestLoop(t, f, spec)
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Stop()

	if err := f.Delete(PlanTaskID(spec.PlanID, 1, "gen")); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	waitFor(t, "loop treats the deleted task as terminal", func() bool {
		select {
		case <-l.Done():
			return true
		default:
			return false
		}
	})
	out, ok := l.LastOutcome()
	if !ok {
		t.Fatal("no outcome recorded for the interfered round")
	}
	if out.Status["gen"] != StateFailed || out.Succeeded() {
		t.Fatalf("deleted task must count as failed: %+v", out)
	}
}

func TestPlanLoopReplanErrorStopsLoop(t *testing.T) {
	f := NewFabric()
	replanErr := errors.New("planner exhausted")
	spec := PlanLoopSpec{
		PlanID:    "plan-e",
		Steps:     []PlanStep{{ID: "gen", Capability: "coder"}},
		MaxRounds: 3,
		Replan:    func(RoundOutcome) ([]PlanStep, error) { return nil, replanErr },
	}
	l := newTestLoop(t, f, spec)
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Stop()

	drive(t, f, PlanTaskID("plan-e", 1, "gen"), nil)
	waitFor(t, "loop failed on replan error", func() bool { return l.Err() != nil })
	if !errors.Is(l.Err(), replanErr) {
		t.Fatalf("Err = %v, want wrapped replanErr", l.Err())
	}
	if _, err := f.Task(PlanTaskID("plan-e", 2, "gen")); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("no round 2 task should exist after replan error, got %v", err)
	}
}

func TestPlanLoopUntilPanicsAreContained(t *testing.T) {
	f := NewFabric()
	spec := oneStepPlan("plan-f", "gen")
	spec.UntilCondition = func(RoundOutcome) bool { panic("bad predicate") }
	l := newTestLoop(t, f, spec)
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer l.Stop()

	drive(t, f, PlanTaskID("plan-f", 1, "gen"), nil)
	waitFor(t, "loop failed on panic", func() bool { return l.Err() != nil })
	if got := l.Err().Error(); !strings.Contains(got, "panicked") {
		t.Fatalf("Err = %v, want panic attribution", l.Err())
	}
}

func TestPlanLoopStopMidRound(t *testing.T) {
	f := NewFabric()
	l := newTestLoop(t, f, oneStepPlan("plan-g", "gen"))
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	l.Stop()
	select {
	case <-l.Done():
	default:
		t.Fatal("Stop must close Done")
	}
}

func TestPlanLoopCtxCancelEndsDriver(t *testing.T) {
	f := NewFabric()
	l := newTestLoop(t, f, oneStepPlan("plan-h", "gen"))
	ctx, cancel := context.WithCancel(context.Background())
	if err := l.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	cancel()
	waitFor(t, "driver exit on ctx cancel", func() bool {
		select {
		case <-l.Done():
			return true
		default:
			return false
		}
	})
}

func TestPlanLoopValidation(t *testing.T) {
	valid := oneStepPlan("plan-ok", "gen")
	cases := []struct {
		name string
		spec PlanLoopSpec
	}{
		{"empty_plan_id", func() PlanLoopSpec { s := valid; s.PlanID = ""; return s }()},
		{"empty_steps", func() PlanLoopSpec { s := valid; s.Steps = nil; return s }()},
		{"zero_rounds", func() PlanLoopSpec { s := valid; s.MaxRounds = 0; return s }()},
		{"empty_step_id", func() PlanLoopSpec {
			s := valid
			s.Steps = []PlanStep{{Capability: "coder"}}
			return s
		}()},
		{"duplicate_step_id", func() PlanLoopSpec {
			s := valid
			s.Steps = []PlanStep{{ID: "x", Capability: "a"}, {ID: "x", Capability: "b"}}
			return s
		}()},
	}
	for _, tc := range cases {
		if _, err := NewPlanLoop(NewFabric(), tc.spec); err == nil {
			t.Fatalf("%s: expected validation error", tc.name)
		}
	}
	if _, err := NewPlanLoop(nil, valid); err == nil {
		t.Fatal("nil fabric must be rejected")
	}
}

func TestPlanLoopDoubleStartRejected(t *testing.T) {
	f := NewFabric()
	l := newTestLoop(t, f, oneStepPlan("plan-i", "gen"))
	if err := l.Start(context.Background()); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	defer l.Stop()
	if err := l.Start(context.Background()); err == nil {
		t.Fatal("second Start must fail")
	}
}

// TestPlanLoopStopWithoutStart locks the lifecycle contract for the
// construct-then-fail path: a loop that was never started must be safely
// stoppable. Creating the Done channel only inside Start left Stop blocking
// on a nil channel forever, which turns the idiomatic `defer loop.Stop()` into
// a deadlock whenever Start is not reached.
func TestPlanLoopStopWithoutStart(t *testing.T) {
	l := newTestLoop(t, NewFabric(), oneStepPlan("plan-j", "gen"))
	stopped := make(chan struct{})
	go func() {
		l.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop on a never-started loop must return immediately")
	}
	if l.Round() != 0 {
		t.Fatalf("Round = %d, want 0 before Start", l.Round())
	}
}

// TestPlanLoopFailedStartClosesDone locks the same contract from the other
// side: when Start rejects the plan, Done must already be closed so a caller
// waiting on it is not stranded. Round 1 is compiled synchronously, so a
// duplicate plan namespace (task IDs are globally unique) is the natural
// trigger.
func TestPlanLoopFailedStartClosesDone(t *testing.T) {
	f := NewFabric()
	spec := oneStepPlan("plan-k", "gen")
	first := newTestLoop(t, f, spec)
	if err := first.Start(context.Background()); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	defer first.Stop()

	// A second loop over the same plan id collides on round-1 task IDs.
	second := newTestLoop(t, f, spec)
	if err := second.Start(context.Background()); err == nil {
		t.Fatal("colliding plan namespace must fail Start")
	}
	select {
	case <-second.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("failed Start must close Done")
	}
	second.Stop() // must not block either
}
