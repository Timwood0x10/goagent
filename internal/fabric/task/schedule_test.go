package taskfabric

import (
	"testing"
	"time"
)

// TestFabricSchedulePicksBestCapable verifies Schedule picks the best capable
// executor (capability × load × confidence — design §8 "who is the best
// executor") and acquires the task for it, granting a usable fencing token.
func TestFabricSchedulePicksBestCapable(t *testing.T) {
	f := NewFabric()
	tk := newTask("t1")
	tk.Capability = "rust/unsafe-analysis"
	if err := f.Create(tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	candidates := []Candidate{
		{AgentID: "A", Capabilities: []string{"rust"}, Load: 0.2, Confidence: 0.81},
		{AgentID: "B", Capabilities: []string{"python"}, Load: 0.0, Confidence: 0.99},
		{AgentID: "C", Capabilities: []string{"rust", "unsafe-analysis"}, Load: 0.4, Confidence: 0.97},
	}
	winner, epoch, err := f.Schedule("t1", candidates, time.Minute)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if winner != "C" || epoch == 0 {
		t.Fatalf("want C to win with a fencing token, got winner=%q epoch=%d", winner, epoch)
	}
	task, _ := f.Task("t1")
	if task.Owner != "C" || task.State != StateLeased {
		t.Fatalf("C must own a LEASED task, got owner=%q state=%s", task.Owner, task.State)
	}
	// The winner's fencing token is usable end-to-end.
	if err := f.Start("t1", "C", epoch); err != nil {
		t.Fatalf("winner must be able to start with its epoch: %v", err)
	}
}

// TestFabricScheduleNoCapableCandidate verifies Schedule rejects when no
// candidate is capable of the task (capability gating).
func TestFabricScheduleNoCapableCandidate(t *testing.T) {
	f := NewFabric()
	tk := newTask("t1")
	tk.Capability = "rust"
	if err := f.Create(tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	candidates := []Candidate{
		{AgentID: "B", Capabilities: []string{"python"}, Confidence: 0.99},
	}
	if _, _, err := f.Schedule("t1", candidates, time.Minute); err != ErrNoCapableCandidate {
		t.Fatalf("want ErrNoCapableCandidate, got %v", err)
	}
}

// TestFabricScheduleRejectsOwnedTask verifies Schedule cannot double-assign a
// task that already has an owner (CAS still holds through the scheduler).
func TestFabricScheduleRejectsOwnedTask(t *testing.T) {
	f := NewFabric()
	tk := newTask("t1")
	tk.Capability = "rust"
	if err := f.Create(tk); err != nil {
		t.Fatalf("Create: %v", err)
	}
	candidates := []Candidate{{AgentID: "A", Capabilities: []string{"rust"}, Confidence: 0.9}}
	if _, _, err := f.Schedule("t1", candidates, time.Minute); err != nil {
		t.Fatalf("first Schedule: %v", err)
	}
	// Task now LEASED: a second Schedule must be rejected.
	if _, _, err := f.Schedule("t1", candidates, time.Minute); err != ErrTaskNotReady {
		t.Fatalf("second Schedule must be rejected, got %v", err)
	}
}
