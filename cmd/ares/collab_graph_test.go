package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/agents/sub"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/fabric/task"
)

// newGraphTestKernel is a chaos-style harness with STATIC capability
// executors registered, so submitted graphs resolve their nodes immediately.
func newGraphTestKernel(t *testing.T, ctx context.Context) (*actionHandler, *kernelHandle) {
	t.Helper()
	_, kh, fabric, _ := newChaosTestKernel(t, ctx, true)
	for _, cap := range []string{"research", "review", "write"} {
		kh.scheduler.RegisterExecutor("peer-"+cap, &chaosStubExecutor{
			id: "peer-" + cap, typ: models.AgentType(cap),
		})
	}
	_ = fabric
	handler := &actionHandler{kernel: kh, apiKey: "test-key"}
	return handler, kh
}

// postGraph submits a graph JSON payload to the endpoint.
func postGraph(t *testing.T, h *actionHandler, body string) (int, map[string]any) {
	t.Helper()
	return postGraphCtx(t, h, context.Background(), body)
}

// postGraphCtx is postGraph with an explicit request context, so tests that
// exercise deadline-driven paths (e.g. the 504 timeout) can bound the request.
func postGraphCtx(t *testing.T, h *actionHandler, ctx context.Context, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/graphs", bytes.NewReader([]byte(body)))
	req = req.WithContext(ctx)
	req.Header.Set("Authorization", "Bearer test-key")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// TestGraphsEndpointPipeline locks the pipeline mode (delegate/pipeline/
// orchestrate share one engine — the kernel fabric DAG): a serial chain runs
// in dependency order and every node's output is returned.
func TestGraphsEndpointPipeline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler, _ := newGraphTestKernel(t, ctx)

	body := `{
		"schema_version": 1,
		"nodes": [
			{"id": "s1", "capability": "research", "input": "topic"},
			{"id": "s2", "capability": "write",    "input": "draft"}
		],
		"edges": [{"from": "s1", "to": "s2"}]
	}`
	code, resp := postGraph(t, handler, body)
	if code != http.StatusOK || resp["success"] != true {
		t.Fatalf("status=%d resp=%v", code, resp)
	}
	outputs := resp["outputs"].(map[string]any)
	if outputs["s1"] == "" || outputs["s2"] == "" {
		t.Fatalf("pipeline outputs missing: %v", outputs)
	}
}

// TestGraphsEndpointAutoGeneratesUniqueRunIDs locks the C4-review fix #1:
// the server always generates run ids; two identical submissions BOTH succeed
// with distinct graph ids (a caller-supplied id colliding with live fabric
// tasks used to surface as a 500 for a caller mistake).
func TestGraphsEndpointAutoGeneratesUniqueRunIDs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler, _ := newGraphTestKernel(t, ctx)

	body := `{"schema_version":1,"nodes":[{"id":"only","capability":"research"}],"edges":[]}`
	code1, r1 := postGraph(t, handler, body)
	code2, r2 := postGraph(t, handler, body)
	if code1 != http.StatusOK || code2 != http.StatusOK {
		t.Fatalf("status %d/%d — duplicate submission must not 500", code1, code2)
	}
	if r1["graph_id"] == r2["graph_id"] {
		t.Fatalf("graph ids must differ: %v vs %v", r1["graph_id"], r2["graph_id"])
	}
}

// TestGraphsEndpointConcurrentRunIDsAreUnique locks the C4-review fix #1
// hardening: run ids must stay collision-free even under concurrent
// submissions landing in the same nanosecond tick (UnixNano alone is not
// unique — a collision would make the loser's first fabric.Create hit
// ErrTaskExists → a spurious 500). Many parallel identical submissions must
// all succeed with pairwise-distinct graph ids.
func TestGraphsEndpointConcurrentRunIDsAreUnique(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler, _ := newGraphTestKernel(t, ctx)

	const n = 32
	body := `{"schema_version":1,"nodes":[{"id":"only","capability":"research"}],"edges":[]}`
	var wg sync.WaitGroup
	var mu sync.Mutex
	ids := make(map[string]struct{}, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			code, r := postGraph(t, handler, body)
			if code != http.StatusOK {
				t.Errorf("concurrent submission got status %d (want 200)", code)
				return
			}
			gid, _ := r["graph_id"].(string)
			mu.Lock()
			ids[gid] = struct{}{}
			mu.Unlock()
		}()
	}
	wg.Wait()
	if len(ids) != n {
		t.Fatalf("expected %d unique graph ids, got %d (collision → 500 risk)", n, len(ids))
	}
}

// TestGraphsEndpointFanOutJoin locks the orchestration mode: two workers run
// after the root; the join node waits for BOTH (dependency fan-in). The probe
// reads its own task's Dependencies at execution time and fails the test if
// any dependency is not COMPLETED — server-generated ids need no pre-knowledge.
func TestGraphsEndpointFanOutJoin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler, kh := newGraphTestKernel(t, ctx)

	jp := &joinProbe{fabric: kh.fabric, t: t}
	kh.scheduler.RegisterExecutor("peer-review-probe", jp)

	body := `{
		"schema_version": 1,
		"nodes": [
			{"id": "root",   "capability": "research"},
			{"id": "w1",     "capability": "research"},
			{"id": "w2",     "capability": "write"},
			{"id": "join",   "capability": "review"}
		],
		"edges": [
			{"from": "root", "to": "w1"},
			{"from": "root", "to": "w2"},
			{"from": "w1",  "to": "join"},
			{"from": "w2",  "to": "join"}
		]
	}`
	code, resp := postGraph(t, handler, body)
	if code != http.StatusOK || resp["success"] != true {
		t.Fatalf("status=%d resp=%v", code, resp)
	}
}

// TestGraphsEndpointRejectsUnknownCapability locks defensive validation:
// an un-servable capability is rejected BEFORE any task is created.
func TestGraphsEndpointRejectsUnknownCapability(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler, kh := newGraphTestKernel(t, ctx)

	body := `{"schema_version":1,"nodes":[{"id":"x","capability":"nonexistent"}],"edges":[]}`
	code, resp := postGraph(t, handler, body)
	if code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400", code)
	}
	if !strings.Contains(resp["error"].(string), "no peer executor") {
		t.Fatalf("error = %v", resp["error"])
	}
	// Nothing leaked into the fabric: capability validation happens BEFORE
	// runCollabGraph, so no collab- task must exist. (Checking a fixed id
	// like "collab-x" would be a no-op — server-generated run ids never match
	// it, so the assertion would pass regardless of a real leak.)
	for _, id := range kh.fabric.IDs() {
		if strings.HasPrefix(id, "collab-") {
			t.Fatalf("rejected graph leaked task %q", id)
		}
	}
}

// TestGraphsEndpointSchemaVersionGuard covers the wire-evolution guard.
func TestGraphsEndpointSchemaVersionGuard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler, _ := newGraphTestKernel(t, ctx)
	code, resp := postGraph(t, handler, `{"schema_version":2,"nodes":[{"id":"x","capability":"research"}]}`)
	if code != http.StatusBadRequest || !strings.Contains(resp["error"].(string), "schema_version") {
		t.Fatalf("status=%d resp=%v", code, resp)
	}
}

// TestGraphsEndpointNodeFailureReturns422 locks the error taxonomy (C4-review
// #2): when the DAG is well-formed and runs but a node's work fails after
// exhausting retries, the endpoint returns 422 (Unprocessable) — NOT 500. The
// graph was accepted and executed; only the payload failed, so callers must
// not treat it as a server hiccup to blindly retry.
func TestGraphsEndpointNodeFailureReturns422(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler, kh := newGraphTestKernel(t, ctx)
	kh.scheduler.RegisterExecutor("peer-flaky", &failingExecutor{id: "peer-flaky", typ: "flaky"})

	body := `{"schema_version":1,"nodes":[{"id":"bad","capability":"flaky"}],"edges":[]}`
	code, resp := postGraph(t, handler, body)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d want 422, resp=%v", code, resp)
	}
	if resp["success"] != false {
		t.Fatalf("success must be false on node failure: %v", resp)
	}
	if !strings.Contains(resp["error"].(string), "node") {
		t.Fatalf("error should name the failure: %v", resp["error"])
	}
}

// failingExecutor always returns a step error; the fabric requeues it per the
// retry budget and finalizes FAILED once exhausted.
type failingExecutor struct {
	id  string
	typ models.AgentType
}

func (e *failingExecutor) ID() string             { return e.id }
func (e *failingExecutor) Type() models.AgentType { return e.typ }
func (e *failingExecutor) ExecuteStep(_ context.Context, _ *models.Task) (*sub.StepOutcome, error) {
	return nil, errFlakyNode
}

var errFlakyNode = errors.New("flaky node always fails")

// joinProbe asserts, AT EXECUTION TIME, that every dependency of its task is
// already COMPLETED (join-after-both-workers proof). It reads the task's own
// Dependencies list, so it needs no knowledge of server-generated run ids.
type joinProbe struct {
	fabric *taskfabric.Fabric
	t      *testing.T
}

func (j *joinProbe) ID() string             { return "review-probe" }
func (j *joinProbe) Type() models.AgentType { return "review" }
func (j *joinProbe) ExecuteStep(_ context.Context, task *models.Task) (*sub.StepOutcome, error) {
	ftk, err := j.fabric.Task(task.TaskID)
	if err != nil {
		j.t.Errorf("join fabric task unreadable: %v", err)
	}
	for _, dep := range ftk.Dependencies {
		dtk, derr := j.fabric.Task(dep)
		if derr != nil {
			j.t.Errorf("join dep %s unreadable: %v", dep, derr)
			continue
		}
		if dtk.State != taskfabric.StateCompleted {
			j.t.Errorf("join dep %s state=%s, want COMPLETED before join runs", dep, dtk.State)
		}
	}
	res := models.NewTaskResult(task.TaskID, task.AgentType)
	res.SetSuccess(nil, "probed")
	return &sub.StepOutcome{Done: true, Result: res}, nil
}

// TestGraphsEndpointNodeFailureReportsAllFailedNodes locks the deterministic
// error contract (review #1): when MULTIPLE nodes fail in the same scan tick,
// the 422 error must name every failed node — not an arbitrary one whose
// identity depends on scan order. Two INDEPENDENT root nodes (no dependencies
// between them, so no fail-fast cascade kills the second before it runs) both
// fail; the error must name BOTH.
func TestGraphsEndpointNodeFailureReportsAllFailedNodes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler, kh := newGraphTestKernel(t, ctx)
	kh.scheduler.RegisterExecutor("peer-flaky", &failingExecutor{id: "peer-flaky", typ: "flaky"})

	// Two roots, both flaky, no dependency between them — both run and fail in
	// the same or adjacent scan ticks.
	body := `{
		"schema_version": 1,
		"nodes": [
			{"id": "w1", "capability": "flaky"},
			{"id": "w2", "capability": "flaky"}
		],
		"edges": []
	}`
	code, resp := postGraph(t, handler, body)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d want 422, resp=%v", code, resp)
	}
	msg, _ := resp["error"].(string)
	if !strings.Contains(msg, "w1") || !strings.Contains(msg, "w2") {
		t.Fatalf("error must name all failed nodes, got: %v", msg)
	}
}

// TestGraphsEndpointNodeFailureDeterministicSingleNode locks the single-node
// failure case so the "nodes ... failed" plural form is still precise when
// exactly one node is lost: it names that node alone.
func TestGraphsEndpointNodeFailureDeterministicSingleNode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler, kh := newGraphTestKernel(t, ctx)
	kh.scheduler.RegisterExecutor("peer-flaky", &failingExecutor{id: "peer-flaky", typ: "flaky"})

	body := `{"schema_version":1,"nodes":[{"id":"solo","capability":"flaky"}],"edges":[]}`
	code, resp := postGraph(t, handler, body)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d want 422, resp=%v", code, resp)
	}
	msg, _ := resp["error"].(string)
	if !strings.Contains(msg, "[solo]") {
		t.Fatalf("error must name the failed node, got: %v", msg)
	}
}

// TestGraphsEndpointTimeoutReturns504 locks the ErrGraphTimeout → 504 mapping
// (review #2): a node that never settles (its executor blocks until ctx is
// cancelled) drives the wait loop past the deadline, and the endpoint reports
// 504 Gateway Timeout — not a 500 — with a "timed out" error.
func TestGraphsEndpointTimeoutReturns504(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	handler, kh := newGraphTestKernel(t, ctx)
	kh.scheduler.RegisterExecutor("peer-hang", &blockingExecutor{id: "peer-hang", typ: "hang"})

	body := `{"schema_version":1,"nodes":[{"id":"stuck","capability":"hang"}],"edges":[]}`
	code, resp := postGraphCtx(t, handler, ctx, body)
	if code != http.StatusGatewayTimeout {
		t.Fatalf("status=%d want 504, resp=%v", code, resp)
	}
	if resp["success"] != false {
		t.Fatalf("success must be false on timeout: %v", resp)
	}
	if !strings.Contains(resp["error"].(string), "timed out") {
		t.Fatalf("error should mention timeout: %v", resp["error"])
	}
}

// TestGraphsEndpointIgnoresCallerRunID locks the wire contract (review #2):
// graphSubmissionRequest.RunID is accepted for back-compat but the server
// ALWAYS generates the run id (C4-review fix #1). A caller-supplied run_id
// must NOT leak into the returned graph_id — the graph id is server-generated
// (prefix "g" + timestamp). If someone later "helpfully" reconnects the field,
// this test fails loudly instead of silently changing the contract.
func TestGraphsEndpointIgnoresCallerRunID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler, _ := newGraphTestKernel(t, ctx)

	body := `{"schema_version":1,"run_id":"caller-xyz","nodes":[{"id":"only","capability":"research"}],"edges":[]}`
	code, resp := postGraph(t, handler, body)
	if code != http.StatusOK {
		t.Fatalf("status=%d want 200, resp=%v", code, resp)
	}
	gid, _ := resp["graph_id"].(string)
	if gid == "" || strings.HasPrefix(gid, "caller-") {
		t.Fatalf("graph_id must be server-generated, not the caller-supplied run_id: got %q", gid)
	}
	if !strings.HasPrefix(gid, "g") {
		t.Fatalf("graph_id should follow the server-generated pattern (g<ts>-<seq>): got %q", gid)
	}
}

// blockingExecutor never returns until its context is cancelled — a node that
// stays in-flight past any deadline, used to exercise the 504 timeout path.
type blockingExecutor struct {
	id  string
	typ models.AgentType
}

func (e *blockingExecutor) ID() string             { return e.id }
func (e *blockingExecutor) Type() models.AgentType { return e.typ }
func (e *blockingExecutor) ExecuteStep(ctx context.Context, task *models.Task) (*sub.StepOutcome, error) {
	<-ctx.Done()
	res := models.NewTaskResult(task.TaskID, task.AgentType)
	res.SetError("blocked until cancel")
	return &sub.StepOutcome{Done: true, Result: res}, nil
}
