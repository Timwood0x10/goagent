package agentsyscall

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/fabric/task"
)

// drivePlanTask completes a loop-compiled task under a synthetic worker
// lease — the production Acquire→Start→Complete path the scheduler drives.
func drivePlanTask(t *testing.T, f *taskfabric.Fabric, id string) {
	t.Helper()
	epoch, err := f.Acquire(id, "worker", time.Minute)
	if err != nil {
		t.Fatalf("Acquire %s: %v", id, err)
	}
	if err := f.Start(id, "worker", epoch); err != nil {
		t.Fatalf("Start %s: %v", id, err)
	}
	if err := f.Complete(id, "worker", epoch); err != nil {
		t.Fatalf("Complete %s: %v", id, err)
	}
}

// waitForPlanTask polls until the named task exists in the fabric.
func waitForPlanTask(t *testing.T, f *taskfabric.Fabric, id string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := f.Task(id); err == nil {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for task %s", id)
}

func loopPlanArgs() CreatePlanArgs {
	// No Until condition: the loop runs exactly MaxRounds rounds, so the
	// test can observe round 2 being compiled after a clean round 1.
	return CreatePlanArgs{
		Steps: []PlanStepArgs{{ID: "gen", Capability: "ares/plan"}},
		Loop:  &PlanLoopArgs{MaxRounds: 2},
	}
}

// TestCreatePlanLoopRunsRounds verifies the create_plan loop option:
// the kernel starts a bounded plan loop whose rounds are compiled into the
// same fabric and executed by the normal scheduler path.
func TestCreatePlanLoopRunsRounds(t *testing.T) {
	fabric := taskfabric.NewFabric()
	kernel := NewKernel(nil, fabric, nil, nil, WithLoopLifetime(context.Background()))

	result, err := kernel.CreatePlan(context.Background(), loopPlanArgs())
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	if result.PlanID == "" || result.LoopMaxRounds != 2 {
		t.Fatalf("loop result not reported: %+v", result)
	}
	if result.Count != 1 || len(result.TaskIDs) != 1 {
		t.Fatalf("unexpected batch: %+v", result)
	}
	// Round-1 IDs carry the loop namespace.
	if got := result.TaskIDs[0]; got != taskfabric.PlanTaskID(result.PlanID, 1, "gen") {
		t.Fatalf("round-1 task id = %q, want loop-namespaced id", got)
	}
	waitForPlanTask(t, fabric, result.TaskIDs[0])
	drivePlanTask(t, fabric, result.TaskIDs[0])

	// Round 2 compiles once round 1 goes terminal, same DAG, new namespace.
	round2 := taskfabric.PlanTaskID(result.PlanID, 2, "gen")
	waitForPlanTask(t, fabric, round2)
	drivePlanTask(t, fabric, round2)

	// MaxRounds=2 is the hard cap: no round 3 may ever appear.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := fabric.Task(taskfabric.PlanTaskID(result.PlanID, 3, "gen")); err != nil {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if _, err := fabric.Task(taskfabric.PlanTaskID(result.PlanID, 3, "gen")); err == nil {
		t.Fatal("round 3 must not compile beyond MaxRounds")
	}
}

// TestCreatePlanLoopRejectsInvalidSpecs locks the fail-loudly contract: a
// loop spec the kernel cannot honor must error instead of silently running
// an unbounded or condition-less loop.
func TestCreatePlanLoopRejectsInvalidSpecs(t *testing.T) {
	fabric := taskfabric.NewFabric()

	// No loop lifetime wired → refuse rather than leak an unmanaged worker.
	noLifetime := NewKernel(nil, fabric, nil, nil)
	if _, err := noLifetime.CreatePlan(context.Background(), loopPlanArgs()); err == nil {
		t.Fatal("loop without WithLoopLifetime must be rejected")
	}

	kernel := NewKernel(nil, fabric, nil, nil, WithLoopLifetime(context.Background()))
	cases := []struct {
		name string
		loop PlanLoopArgs
	}{
		{"zero_rounds", PlanLoopArgs{MaxRounds: 0}},
		{"negative_rounds", PlanLoopArgs{MaxRounds: -1}},
		{"unknown_until", PlanLoopArgs{MaxRounds: 2, Until: "score > 9000"}},
	}
	for _, tc := range cases {
		args := CreatePlanArgs{Steps: []PlanStepArgs{{ID: "gen", Capability: "ares/plan"}}, Loop: &tc.loop}
		if _, err := kernel.CreatePlan(context.Background(), args); err == nil {
			t.Fatalf("%s: expected rejection", tc.name)
		}
	}
}

// TestCreatePlanLoopCancelledCallerRejected locks the tool-call contract: a
// create_plan whose caller context is already cancelled must not start a
// background loop that outlives the abandoned call.
func TestCreatePlanLoopCancelledCallerRejected(t *testing.T) {
	kernel := NewKernel(nil, taskfabric.NewFabric(), nil, nil, WithLoopLifetime(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := kernel.CreatePlan(ctx, loopPlanArgs()); err == nil {
		t.Fatal("cancelled caller context must be rejected")
	}
	if live := kernel.LivePlanLoops(); len(live) != 0 {
		t.Fatalf("no loop may be registered: %v", live)
	}
}

// TestCreatePlanLoopCapIsEnforced locks the quota contract: create_plan is
// LLM-callable, and every looped plan owns a driver goroutine for the serve
// lifetime, so the live-loop count must be capped like the spawn quota.
func TestCreatePlanLoopCapIsEnforced(t *testing.T) {
	fabric := taskfabric.NewFabric()
	kernel := NewKernel(nil, fabric, nil, nil,
		WithLoopLifetime(context.Background()), WithMaxPlanLoops(2))

	// MaxRounds is high enough that the loops stay live for the whole test.
	args := CreatePlanArgs{
		Steps: []PlanStepArgs{{ID: "gen", Capability: "ares/plan"}},
		Loop:  &PlanLoopArgs{MaxRounds: 1000},
	}
	for i := 0; i < 2; i++ {
		if _, err := kernel.CreatePlan(context.Background(), args); err != nil {
			t.Fatalf("loop %d must be admitted: %v", i, err)
		}
	}
	if _, err := kernel.CreatePlan(context.Background(), args); err == nil {
		t.Fatal("third loop must be rejected by the cap")
	}
	live := kernel.LivePlanLoops()
	if len(live) != 2 {
		t.Fatalf("LivePlanLoops = %v, want 2 entries", live)
	}

	// Stopping one frees its slot, so a new plan is admitted again.
	if err := kernel.StopPlanLoop(live[0]); err != nil {
		t.Fatalf("StopPlanLoop: %v", err)
	}
	result, err := kernel.CreatePlan(context.Background(), args)
	if err != nil {
		t.Fatalf("loop must be admitted after a slot is freed: %v", err)
	}
	for _, id := range append(kernel.LivePlanLoops(), result.PlanID) {
		_ = kernel.StopPlanLoop(id) // best-effort teardown
	}
}

// TestStopPlanLoopUnknownPlan locks the sentinel: an unknown or already
// finished plan is reported via ErrPlanLoopNotFound, never silently accepted.
func TestStopPlanLoopUnknownPlan(t *testing.T) {
	kernel := NewKernel(nil, taskfabric.NewFabric(), nil, nil, WithLoopLifetime(context.Background()))
	if err := kernel.StopPlanLoop("plan-does-not-exist"); !errors.Is(err, ErrPlanLoopNotFound) {
		t.Fatalf("StopPlanLoop = %v, want ErrPlanLoopNotFound", err)
	}
}

// TestCreatePlanLoopDeregistersWhenFinished locks the registry lifecycle: the
// kernel's watcher must drop a finished loop, otherwise the cap would leak a
// slot per completed plan.
func TestCreatePlanLoopDeregistersWhenFinished(t *testing.T) {
	fabric := taskfabric.NewFabric()
	kernel := NewKernel(nil, fabric, nil, nil, WithLoopLifetime(context.Background()))

	result, err := kernel.CreatePlan(context.Background(), CreatePlanArgs{
		Steps: []PlanStepArgs{{ID: "gen", Capability: "ares/plan"}},
		Loop:  &PlanLoopArgs{MaxRounds: 1},
	})
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	waitForPlanTask(t, fabric, result.TaskIDs[0])
	drivePlanTask(t, fabric, result.TaskIDs[0])

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(kernel.LivePlanLoops()) == 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("finished loop still registered: %v", kernel.LivePlanLoops())
}
