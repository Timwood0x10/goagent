package main

import (
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/fabric/task"
)

// seedCollabTask creates a collab-prefixed task in the requested state:
// ready (default), leased (Acquire only), running (Acquire+Start), or failed
// (acquire→start→fail so the FAILED entry is deletable).
func seedCollabTask(t *testing.T, f *taskfabric.Fabric, id, state string) {
	t.Helper()
	spec := &taskfabric.Task{ID: id, Capability: "x"}
	if state == "failed" {
		// Bound the budget explicitly so Fail() lands the task in terminal
		// FAILED. (MaxRetries<=0 now means no retries too — the explicit 1
		// pins the single-attempt intent.)
		spec.RetryPolicy = taskfabric.RetryPolicy{MaxRetries: 1}
	}
	if err := f.Create(spec); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
	if state == "ready" {
		return
	}
	const holder = "holder"
	epoch, err := f.Acquire(id, holder, 5*time.Minute)
	if err != nil {
		t.Fatalf("acquire %s: %v", id, err)
	}
	if state == "leased" {
		return
	}
	if err := f.Start(id, holder, epoch); err != nil {
		t.Fatalf("start %s: %v", id, err)
	}
	if state == "failed" {
		if err := f.Fail(id, holder, epoch); err != nil {
			t.Fatalf("fail %s: %v", id, err)
		}
	}
}

// TestSweepStaleCollabTasks locks the janitor semantics (the background
// runCollabGCLoop reclaims residue off the submission hot path):
//
//  1. Terminal / never-started tasks of PREVIOUS runs are garbage → swept.
//  2. In-flight (LEASED) tasks are refused by the Delete guard → survive.
//  3. Non-collab tasks are invisible to the prefix filter.
//  4. A LIVE run's pending tasks are protected from the janitor
//     (activeCollabRuns registry), and become harvestable only after the
//     run unregisters — eventual cleanup without harvesting a live run.
func TestSweepStaleCollabTasks(t *testing.T) {
	f := taskfabric.NewFabric()

	// Previous-run garbage.
	seedCollabTask(t, f, "collab-old-completed", "failed")
	seedCollabTask(t, f, "collab-old-ready", "ready") // caller died pre-start
	seedCollabTask(t, f, "collab-inflight", "leased") // guard refuses delete

	// Unrelated task: invisible to the prefix filter.
	seedCollabTask(t, f, "unrelated-task", "ready")

	if removed := sweepStaleCollabTasks(f); removed != 2 {
		t.Fatalf("sweep removed %d, want 2 (failed + ready)", removed)
	}
	for _, gone := range []string{"collab-old-completed", "collab-old-ready"} {
		if _, err := f.Task(gone); err == nil {
			t.Errorf("%s should have been harvested", gone)
		}
	}
	for _, alive := range []string{"collab-inflight", "unrelated-task"} {
		if _, err := f.Task(alive); err != nil {
			t.Errorf("%s must survive the sweep", alive)
		}
	}

	// Live run protection.
	markActiveRun("g-live")
	seedCollabTask(t, f, "collab-g-live-node", "ready")

	if again := sweepStaleCollabTasks(f); again != 0 {
		t.Fatalf("second sweep removed %d, want 0 (live run protected)", again)
	}
	if _, err := f.Task("collab-g-live-node"); err != nil {
		t.Fatal("live run's pending task must survive concurrent sweeps")
	}

	// After unregistering, its leftovers are harvestable — eventual cleanup
	// without ever colliding with an active submission.
	unmarkActiveRun("g-live")
	if third := sweepStaleCollabTasks(f); third != 1 {
		t.Fatalf("post-unmark sweep removed %d, want 1", third)
	}
	if _, err := f.Task("collab-g-live-node"); err == nil {
		t.Fatal("unregistered run's leftovers must be harvestable")
	}
}
