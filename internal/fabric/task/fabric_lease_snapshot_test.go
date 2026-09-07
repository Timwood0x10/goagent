package taskfabric

import (
	"testing"
	"time"
)

// TestFabricLeaseSnapshot locks the monitoring.md Phase 0 Domain B contract:
// the snapshot is a pure read of every non-terminal task (lease owner/expiry,
// epoch, checkpoint presence, dependencies), sorted by TaskID, with terminal
// tasks excluded so the view stays bounded by live work.
func TestFabricLeaseSnapshot(t *testing.T) {
	f := NewFabric()

	mustCreate := func(id string) {
		t.Helper()
		if err := f.Create(&Task{ID: id, Capability: "cap", Dependencies: []string{"dep-" + id}}); err != nil {
			t.Fatal(err)
		}
	}
	mustCreate("t-1")
	mustCreate("t-2")
	if err := f.Create(&Task{ID: "t-done", Capability: "cap"}); err != nil {
		t.Fatal(err)
	}

	// t-2 acquires a lease; t-done runs to COMPLETED (terminal → must be
	// excluded from the snapshot). Acquire returns the epoch required by
	// subsequent Start/Complete calls.
	_, err := f.Acquire("t-2", "agent-a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	epochDone, err := f.Acquire("t-done", "agent-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Start("t-done", "agent-b", epochDone); err != nil {
		t.Fatal(err)
	}
	if err := f.Complete("t-done", "agent-b", epochDone); err != nil {
		t.Fatal(err)
	}

	snap := f.LeaseSnapshot()

	if len(snap) != 2 {
		t.Fatalf("expected 2 non-terminal entries, got %d: %+v", len(snap), snap)
	}
	if snap[0].TaskID != "t-1" || snap[1].TaskID != "t-2" {
		t.Fatalf("entries not sorted / wrong ids: %+v", snap)
	}

	unowned := snap[0]
	if unowned.Owner != "" || !unowned.ExpiresAt.IsZero() {
		t.Errorf("unowned entry must have empty owner/zero expiry: %+v", unowned)
	}
	if len(unowned.Dependencies) != 1 || unowned.Dependencies[0] != "dep-t-1" {
		t.Errorf("dependencies must be copied: %+v", unowned.Dependencies)
	}
	if unowned.HasCheckpoint {
		t.Error("fresh task must have no checkpoint")
	}

	leased := snap[1]
	if leased.Owner != "agent-a" {
		t.Errorf("owner = %q, want agent-a", leased.Owner)
	}
	if leased.Epoch == 0 {
		t.Error("acquired lease must carry a non-zero epoch")
	}
	if leased.ExpiresAt.Before(time.Now()) {
		t.Errorf("expiry %v must be in the future", leased.ExpiresAt)
	}
}

// TestFabricLeaseSnapshotPureRead verifies purity: taking a snapshot does not
// expire leases, mutate states, or advance any counter — unlike
// CheckExpiredLeases, which is a write path and must never be used for
// observation (monitoring.md §2.2 note).
func TestFabricLeaseSnapshotPureRead(t *testing.T) {
	f := NewFabric()
	if err := f.Create(&Task{ID: "t-x", Capability: "cap"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Acquire("t-x", "a", 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}

	time.Sleep(80 * time.Millisecond) // lease is now expired

	for i := 0; i < 5; i++ {
		snap := f.LeaseSnapshot()
		if len(snap) != 1 || snap[0].State != StateLeased {
			t.Fatalf("snapshot must not mutate state on expired lease: %+v", snap)
		}
	}

	// The write-path sweep still works afterwards — purity did not break it.
	requeued := f.CheckExpiredLeases()
	if len(requeued) != 1 || requeued[0] != "t-x" {
		t.Fatalf("expected t-x requeued after sweep, got %v", requeued)
	}
}
