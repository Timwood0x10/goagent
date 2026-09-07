package main

import (
	"context"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/fabric/agent"
	"github.com/Timwood0x10/ares/internal/fabric/planprojection"
	"github.com/Timwood0x10/ares/internal/fabric/task"
)

// admissionKernel builds a kernelHandle with the L2 session path wired
// (registry + shared compile coordinator on one fabric).
func admissionKernel() (*kernelHandle, *taskfabric.Fabric) {
	fabric := taskfabric.NewFabric()
	return &kernelHandle{
		fabric:       fabric,
		sessionReg:   agentfabric.NewSessionRegistry(),
		compileCoord: planprojection.NewCompileCoordinator(fabric, nil),
	}, fabric
}

// TestSessionKeepSet pins the reaper keep predicate (P0-1): the session
// registry is the single authority — tasks of live sessions are kept, tasks
// of released sessions are harvestable, and IDs that are not session-scoped
// are never kept (the reaper's prefix filter plus grace handle those).
func TestSessionKeepSet(t *testing.T) {
	ctx := context.Background()
	reg := agentfabric.NewSessionRegistry()
	if _, err := reg.InitSession(ctx, "keep-1", "p", nil, nil); err != nil {
		t.Fatalf("InitSession: %v", err)
	}
	keep := sessionKeepSet(reg)

	if !keep("sess/keep-1/d0/alpha#1") {
		t.Error("live session task must be kept")
	}
	if !keep(agentfabric.SessionRootID("keep-1")) {
		t.Error("live session root must be kept")
	}
	if keep("sess/gone/d0/alpha#1") {
		t.Error("task of an unknown session must not be kept")
	}
	if keep("plain/task") {
		t.Error("non-session id must not be kept")
	}

	if err := reg.ReleaseSession("keep-1"); err != nil {
		t.Fatalf("ReleaseSession: %v", err)
	}
	if keep("sess/keep-1/d0/alpha#1") {
		t.Error("released session task must become harvestable")
	}
}

// TestSubmitPeerTask_AdmitsSessionFirst pins M4-B2 admission: a session-scoped
// submission registers the session and compiles its root BEFORE the user
// task is created, so the planner's first quantum finds a live graph.
func TestSubmitPeerTask_AdmitsSessionFirst(t *testing.T) {
	ctx := context.Background()
	kernel, fabric := admissionKernel()

	taskID, err := submitPeerTask(ctx, kernel, "ares/plan", map[string]any{
		"session_id": "adm-1",
		"input":      "find the answer",
	})
	if err != nil {
		t.Fatalf("submitPeerTask with session payload error = %v", err)
	}
	if taskID == "" {
		t.Fatal("submitPeerTask must return the created task id")
	}
	got, err := kernel.sessionReg.GetSession("adm-1")
	if err != nil {
		t.Fatalf("session must exist after submission: %v", err)
	}
	if got.Root() != agentfabric.SessionRootID("adm-1") {
		t.Errorf("session root = %q, want deterministic root id", got.Root())
	}
	if _, err := fabric.Task(got.Root()); err != nil {
		t.Errorf("session root task must be compiled at admission: %v", err)
	}
	if _, err := fabric.Task(taskID); err != nil {
		t.Errorf("submitted task must exist: %v", err)
	}
}

// TestSubmitPeerTask_ResubmitReusesSession pins admission idempotency: a
// second submission into a live session is a continuation — no error, no
// duplicate root, exactly one root task.
func TestSubmitPeerTask_ResubmitReusesSession(t *testing.T) {
	ctx := context.Background()
	kernel, fabric := admissionKernel()

	payload := map[string]any{"session_id": "adm-2", "input": "prompt"}
	if _, err := submitPeerTask(ctx, kernel, "ares/plan", payload); err != nil {
		t.Fatalf("first submission error = %v", err)
	}
	if _, err := submitPeerTask(ctx, kernel, "ares/plan", payload); err != nil {
		t.Fatalf("resubmission into a live session must not fail: %v", err)
	}
	root := agentfabric.SessionRootID("adm-2")
	seen := 0
	for _, id := range fabric.IDs() {
		if id == root {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("root task count = %d, want exactly 1 (no duplicate admission)", seen)
	}
}

// TestSubmitPeerTask_SessionlessAutoAdmits pins the M4-D single path: without
// a session_id the submission is auto-admitted into a fresh session — the
// task is always ares/plan with a live graph behind it. There is no
// session-less legacy submission anymore.
func TestSubmitPeerTask_SessionlessAutoAdmits(t *testing.T) {
	ctx := context.Background()
	kernel, fabric := admissionKernel()

	taskID, err := submitPeerTask(ctx, kernel, "worker", map[string]any{"q": "x"})
	if err != nil {
		t.Fatalf("sessionless submission error = %v", err)
	}
	task, err := fabric.Task(taskID)
	if err != nil {
		t.Fatalf("submitted task must exist: %v", err)
	}
	if task.Capability != "ares/plan" {
		t.Errorf("capability = %q, want ares/plan (normalized)", task.Capability)
	}
	sessions := kernel.sessionReg.SessionIDs()
	if len(sessions) != 1 {
		t.Fatalf("auto-admission created %d sessions, want 1", len(sessions))
	}
	env, ok := task.Checkpoint.(*taskfabric.CheckpointEnvelope)
	if !ok || env == nil || env.SessionID != sessions[0] {
		t.Errorf("task envelope must carry the auto-admitted session")
	}
}

// TestSubmitPeerTask_NoRegistryFailsFast pins fail-fast: without a session
// registry no session can be admitted, so the submission errors AND creates
// nothing — never an unrunnable task.
func TestSubmitPeerTask_NoRegistryFailsFast(t *testing.T) {
	ctx := context.Background()
	kernel := &kernelHandle{fabric: taskfabric.NewFabric()}

	if _, err := submitPeerTask(ctx, kernel, "worker", map[string]any{
		"session_id": "adm-off",
		"input":      "prompt",
	}); err == nil {
		t.Fatal("submission without session registry must fail, not degrade silently")
	}
	if len(kernel.fabric.IDs()) != 0 {
		t.Errorf("failed submission left %d tasks behind, want 0", len(kernel.fabric.IDs()))
	}
}

// TestSubmitPeerTask_AdmissionFailureCreatesNothing pins fail-fast: when the
// compile coordinator is missing, the submission errors AND creates nothing —
// no unrunnable task, no half-admitted session.
func TestSubmitPeerTask_AdmissionFailureCreatesNothing(t *testing.T) {
	ctx := context.Background()
	fabric := taskfabric.NewFabric()
	kernel := &kernelHandle{
		fabric:     fabric,
		sessionReg: agentfabric.NewSessionRegistry(),
		// compileCoord nil: admission cannot project grown nodes.
	}

	if _, err := submitPeerTask(ctx, kernel, "ares/plan", map[string]any{
		"session_id": "adm-fail",
		"input":      "prompt",
	}); err == nil {
		t.Fatal("submission without compile coordinator must fail, not degrade silently")
	}
	if len(fabric.IDs()) != 0 {
		t.Errorf("failed admission left %d tasks behind, want 0", len(fabric.IDs()))
	}
	if _, err := kernel.sessionReg.GetSession("adm-fail"); err == nil {
		t.Error("failed admission must not leave a half-admitted session")
	}
}

// TestSubmitPeerTask_RejectsSlashSessionID pins P0-1b: a client-supplied
// session_id containing "/" would break the reaper keep-set's reverse parse
// (a live session's history becomes harvestable mid-flight), so admission
// fails fast at the boundary and creates nothing.
func TestSubmitPeerTask_RejectsSlashSessionID(t *testing.T) {
	ctx := context.Background()
	kernel, fabric := admissionKernel()

	if _, err := submitPeerTask(ctx, kernel, "ares/plan", map[string]any{
		"session_id": "a/b",
		"input":      "prompt",
	}); err == nil {
		t.Fatal("session_id containing a slash must be rejected")
	}
	if len(fabric.IDs()) != 0 {
		t.Errorf("rejected submission left %d tasks behind, want 0", len(fabric.IDs()))
	}
	if len(kernel.sessionReg.SessionIDs()) != 0 {
		t.Errorf("rejected submission left %d sessions behind, want 0", len(kernel.sessionReg.SessionIDs()))
	}
}

// completeFabricTask drives a READY task to COMPLETED through the normal
// lease transitions (Acquire → Start → Complete).
func completeFabricTask(t *testing.T, fabric *taskfabric.Fabric, id string) {
	t.Helper()
	epoch, err := fabric.Acquire(id, "turn-agent", time.Minute)
	if err != nil {
		t.Fatalf("acquire %s: %v", id, err)
	}
	if err := fabric.Start(id, "turn-agent", epoch); err != nil {
		t.Fatalf("start %s: %v", id, err)
	}
	if err := fabric.Complete(id, "turn-agent", epoch); err != nil {
		t.Fatalf("complete %s: %v", id, err)
	}
}

// TestSubmitPeerTask_ResubmitAfterReleaseStartsClean pins P0-1c: after a
// session is released, a resubmission under the SAME id (the natural client
// "continue the chat") must not adopt the previous turn's terminal root or
// inherit same-named node tasks — the stale tasks are harvested and the
// fresh root carries the NEW prompt, READY for a real first quantum.
func TestSubmitPeerTask_ResubmitAfterReleaseStartsClean(t *testing.T) {
	ctx := context.Background()
	kernel, fabric := admissionKernel()

	if _, err := submitPeerTask(ctx, kernel, "ares/plan", map[string]any{
		"session_id": "adm-3", "input": "turn one",
	}); err != nil {
		t.Fatalf("first submission error = %v", err)
	}
	root := agentfabric.SessionRootID("adm-3")

	// Turn 1 finishes: root and one grown tool node reach COMPLETED, then
	// the answer body releases the session. The tasks linger in the fabric
	// until the reaper's grace window — that's the contamination window.
	completeFabricTask(t, fabric, root)
	node := agentfabric.SessionNodeID("adm-3", 1, "grep", 0)
	if err := fabric.Create(&taskfabric.Task{
		ID: node, Capability: "tool/grep",
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 2},
	}); err != nil {
		t.Fatalf("create turn-1 node task: %v", err)
	}
	completeFabricTask(t, fabric, node)
	if err := kernel.sessionReg.ReleaseSession("adm-3"); err != nil {
		t.Fatalf("release turn-1 session: %v", err)
	}

	// Turn 2 reuses the id with a different prompt.
	if _, err := submitPeerTask(ctx, kernel, "ares/plan", map[string]any{
		"session_id": "adm-3", "input": "turn two",
	}); err != nil {
		t.Fatalf("resubmission after release error = %v", err)
	}

	tk, err := fabric.Task(root)
	if err != nil {
		t.Fatalf("fresh root must exist: %v", err)
	}
	if tk.State != taskfabric.StateReady {
		t.Errorf("root state = %v, want READY (terminal root must not be adopted)", tk.State)
	}
	if _, err := fabric.Task(node); err == nil {
		t.Error("turn-1 node task must be harvested, not inherited as turn-2's result")
	}
	env, ok := tk.Checkpoint.(*taskfabric.CheckpointEnvelope)
	if !ok || env == nil {
		t.Fatalf("root checkpoint = %T, want envelope", tk.Checkpoint)
	}
	if got, _ := env.Payload["input"].(string); got != "turn two" {
		t.Errorf("root envelope prompt = %q, want %q (no cross-turn bleed)", got, "turn two")
	}
}

// TestSubmitPeerTask_LiveRootRetryStillAdopts pins the other side of
// P0-1c: a NON-terminal root left by a failed admission retry is still
// adopted — harvesting only applies to a released session's terminal tasks.
func TestSubmitPeerTask_LiveRootRetryStillAdopts(t *testing.T) {
	ctx := context.Background()
	kernel, fabric := admissionKernel()

	if _, err := submitPeerTask(ctx, kernel, "ares/plan", map[string]any{
		"session_id": "adm-4", "input": "prompt",
	}); err != nil {
		t.Fatalf("first submission error = %v", err)
	}
	// Simulate the retry: release the session but leave the root READY
	// (in-flight, never completed) — the recompile must adopt, not harvest.
	if err := kernel.sessionReg.ReleaseSession("adm-4"); err != nil {
		t.Fatalf("release session: %v", err)
	}
	root := agentfabric.SessionRootID("adm-4")
	if _, err := submitPeerTask(ctx, kernel, "ares/plan", map[string]any{
		"session_id": "adm-4", "input": "prompt",
	}); err != nil {
		t.Fatalf("retry submission error = %v", err)
	}
	tk, err := fabric.Task(root)
	if err != nil {
		t.Fatalf("root must still exist after adopting retry: %v", err)
	}
	if tk.State != taskfabric.StateReady {
		t.Errorf("root state = %v, want READY", tk.State)
	}
}
