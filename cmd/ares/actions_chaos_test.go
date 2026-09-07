package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/agents/sub"
	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/ares_security"
	"github.com/Timwood0x10/ares/internal/aresrecovery"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/fabric/agent"
	"github.com/Timwood0x10/ares/internal/fabric/task"
)

// chaosTestCognition completes every task in one quantum.
type chaosTestCognition struct{}

func (c *chaosTestCognition) ExecuteStep(_ context.Context, task *models.Task) (*agentfabric.StepOutcome, error) {
	res := models.NewTaskResult(task.TaskID, task.AgentType)
	res.SetSuccess(nil, "done")
	return &agentfabric.StepOutcome{Done: true, Result: res}, nil
}

// newChaosTestKernel assembles a minimal peer kernel (fabrics + scheduler +
// recovery loop with a real replacement factory) plus an actionHandler wired
// for API-key auth — the full production chaos surface without an LLM.
func newChaosTestKernel(t *testing.T, ctx context.Context, withRecoveryLoop bool) (*actionHandler, *kernelHandle, *taskfabric.Fabric, *e2eAgentSink) {
	t.Helper()

	store := ares_events.NewMemoryEventStore()
	fabric := taskfabric.NewFabric().WithEventStore(store)
	agentSink := &e2eAgentSink{}
	agents := agentfabric.NewFabric().WithEventSink(agentSink)

	tracker := newLoadTracker()
	sched := NewKernelScheduler(fabric, map[string]CapabilityExecutor{}, tracker)
	sched.PollInterval = 10 * time.Millisecond
	sched.WithEventStore(store)
	go sched.Run(ctx)

	rec := aresrecovery.New(fabric, agents, aresrecovery.DefaultRestartPolicy())
	// The background recovery loop is opt-in: when a test asserts on the
	// MANUAL /api/chaos/recover sweep, a concurrent periodic sweep would race
	// it for the same expired lease and empty the response nondeterministically.
	if withRecoveryLoop {
		go runKernelRecoveryLoop(ctx, store, rec, kernelLoopConfig{
			RecoverySweepInterval: 20 * time.Millisecond,
			RecoverySweepTimeout:  2 * time.Second,
		},
			func(taskID, agentID string, executor CapabilityExecutor) {
				sched.RegisterExecutorForTask(taskID, agentID, executor)
			},
			func(agentID, capability string) CapabilityExecutor {
				return &chaosStubExecutor{id: agentID, typ: models.AgentType(capability)}
			},
			sched.HasCapableExecutor,
		)
	}

	kh := &kernelHandle{
		fabric:    fabric,
		agents:    agents,
		recovery:  rec,
		scheduler: sched,
		tracker:   tracker,
		flipped:   true,
	}
	handler := &actionHandler{
		kernel: kh,
		apiKey: "test-key",
		// W5 RBAC: chaos operations now require RoleAdmin. Wire the JWT
		// middleware (PermWrite) so admin/operator roles are distinguished —
		// chaos tests mint an admin token via postChaos.
		auth: ares_security.NewAuthMiddleware([]byte(testActionJWTSecret), ares_security.PermWrite),
	}
	return handler, kh, fabric, agentSink
}

// postChaos issues an authenticated POST against the chaos endpoint and
// decodes the JSON body.
func postChaos(t *testing.T, h *actionHandler, chaosType string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/chaos/"+chaosType, bytes.NewReader([]byte("{}")))
	// W5: chaos requires RoleAdmin; mint an admin JWT on the shared test secret.
	req.Header.Set("Authorization", "Bearer "+testActionJWT(t, ares_security.RoleAdmin))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body
}

// TestChaosRandomKillHitsFabricAndEmitsKilled locks the P1 retarget: with the
// peer kernel present, /api/chaos/random-kill kills a LIVE AGENT-FABRIC agent
// (observable via the agent event stream + fabric removal), NOT a legacy
// manager-pool entry. The death then flows through the kernel's own recovery
// chain instead of the retired resurrection path.
func TestChaosRandomKillHitsFabricAndEmitsKilled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handler, kh, _, sink := newChaosTestKernel(t, ctx, false)
	for _, id := range []string{"worker-a", "worker-b"} {
		if _, err := kh.agents.Spawn(ctx, agentfabric.SpawnSpec{
			Identity:         id,
			Capabilities:     []string{"code"},
			CognitionFactory: func([]string) agentfabric.Cognition { return &chaosTestCognition{} },
		}); err != nil {
			t.Fatalf("spawn %s: %v", id, err)
		}
	}

	code, body := postChaos(t, handler, "random-kill")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %v", code, body)
	}
	target, _ := body["target"].(string)
	if target != "worker-a" && target != "worker-b" {
		t.Fatalf("killed target %q is not one of the fabric agents", target)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !sink.contains(agentfabric.EventAgentKilled) {
		time.Sleep(5 * time.Millisecond)
	}
	if !sink.contains(agentfabric.EventAgentKilled) {
		t.Fatal("agent event stream must carry agent.killed after chaos kill")
	}
	if _, err := kh.agents.Get(target); err == nil {
		t.Fatal("killed agent must be gone from the fabric")
	}
}

// TestChaosRecoverSweepRequeuesExpiredLease locks the recover semantics on
// the kernel path: recovery requeues TASKS whose lease expired (durable
// intent), returning their ids so operators see exactly what will resume.
func TestChaosRecoverSweepRequeuesExpiredLease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handler, kh, fabric, _ := newChaosTestKernel(t, ctx, false)

	var clockMu sync.Mutex
	now := time.Now()
	kh.fabric.WithClock(func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	})

	if err := fabric.Create(&taskfabric.Task{
		ID:          "t-chaos-recover",
		Capability:  "code",
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 2},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Hold a lease that expires immediately once the test advances the clock.
	if _, err := fabric.Acquire("t-chaos-recover", "holder-a", time.Millisecond); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	clockMu.Lock()
	now = now.Add(7 * time.Minute) // past the scheduler's 5-minute lease TTL
	clockMu.Unlock()

	code, body := postChaos(t, handler, "recover")
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %v", code, body)
	}
	raw, ok := body["recovered_tasks"].([]any)
	if !ok || len(raw) != 1 {
		t.Fatalf("recovered_tasks = %v, want exactly the expired task", body["recovered_tasks"])
	}
	if got := raw[0].(string); got != "t-chaos-recover" {
		t.Fatalf("recovered task = %q, want t-chaos-recover", got)
	}
	if st := waitFabricState(t, fabric, "t-chaos-recover", taskfabric.StateReady, time.Second); st != taskfabric.StateReady {
		t.Fatalf("expired task must be READY after sweep, got %s", st)
	}
}

// TestChaosKillAllClearsFabric covers the fan-out variant: every live agent
// is killed and reported; a second call reports an empty list rather than
// failing (idempotent over an empty fabric).
func TestChaosKillAllClearsFabric(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handler, kh, _, sink := newChaosTestKernel(t, ctx, false)
	for _, id := range []string{"w1", "w2"} {
		if _, err := kh.agents.Spawn(ctx, agentfabric.SpawnSpec{
			Identity:         id,
			Capabilities:     []string{"code"},
			CognitionFactory: func([]string) agentfabric.Cognition { return &chaosTestCognition{} },
		}); err != nil {
			t.Fatalf("spawn %s: %v", id, err)
		}
	}

	code, body := postChaos(t, handler, "kill-all")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	// Exact set equality: the test owns the spawned ids, so a prefix filter
	// would silently pass if unrelated agents ever joined the fabric.
	gotKilled := map[string]bool{}
	for _, v := range body["killed"].([]any) {
		gotKilled[v.(string)] = true
	}
	if len(gotKilled) != 2 || !gotKilled["w1"] || !gotKilled["w2"] {
		t.Fatalf("killed = %v, want exactly {w1, w2}", body["killed"])
	}
	if failed, _ := body["failed"].([]any); len(failed) != 0 {
		t.Fatalf("failed = %v, want empty on a healthy fabric", body["failed"])
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(liveFabricAgents(kh.agents)) > 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if remain := liveFabricAgents(kh.agents); len(remain) != 0 {
		t.Fatalf("fabric must be empty after kill-all, remaining: %v", remain)
	}
	if !sink.contains(agentfabric.EventAgentKilled) {
		t.Fatal("agent.killed must be observable")
	}
}

// TestChaosKernelWithoutRecoveryReportsError locks the failure-semantics
// contract for batch endpoints: an unwired subsystem is a 4xx ERROR, never a
// silent success with an empty list. random-kill already errored on a nil
// fabric; recover must behave the same when the recovery subsystem is absent
// — otherwise operators read "success, nothing recovered" as "healthy" while
// the sweeper does not even exist.
func TestChaosKernelWithoutRecoveryReportsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handler, kh, _, _ := newChaosTestKernel(t, ctx, false)
	kh.recovery = nil // simulate the unwired subsystem

	code, body := postChaos(t, handler, "recover")
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %v; want 400", code, body)
	}
	if msg, _ := body["error"].(string); !containsStr(msg, "recovery not wired") {
		t.Fatalf("error = %q, want it to name the missing recovery wiring", msg)
	}
}

// containsStr is the package-local substring check for assertion messages.
func containsStr(s, sub string) bool {
	return strings.Contains(s, sub)
}

// chaosStubExecutor satisfies the replacement-factory contract in tests.
type chaosStubExecutor struct {
	id  string
	typ models.AgentType
}

func (e *chaosStubExecutor) ID() string             { return e.id }
func (e *chaosStubExecutor) Type() models.AgentType { return e.typ }
func (e *chaosStubExecutor) ExecuteStep(_ context.Context, task *models.Task) (*sub.StepOutcome, error) {
	res := models.NewTaskResult(task.TaskID, task.AgentType)
	res.SetSuccess(nil, "replacement done")
	return &sub.StepOutcome{Done: true, Result: res}, nil
}

// TestChaosStopEndpointAuth locks the REVIEW #12 Phase 2 emergency-stop
// contract: the endpoint is disabled without a configured stop_token (503),
// rejects a wrong X-Chaos-Token (403), and trips the live-chaos kill switch
// on a valid token.
func TestChaosStopEndpointAuth(t *testing.T) {
	newHandler := func(token string) *actionHandler {
		return &actionHandler{apiKey: "test-key", chaosStopToken: token}
	}

	t.Run("disabled_when_token_empty", func(t *testing.T) {
		// Reset the process-level singleton so repeated runs (-count>1)
		// don't inherit the switch tripped by trips_switch_with_valid_token.
		liveChaosCtl = &chaosStopControl{}
		h := newHandler("")
		code, body := postChaosWithToken(t, h, "stop", "whatever")
		if code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 when stop_token empty, got %d", code)
		}
		if liveChaosCtl.Stopped() {
			t.Fatal("kill switch must not trip while endpoint is disabled")
		}
		_ = body
	})

	t.Run("forbidden_with_wrong_token", func(t *testing.T) {
		liveChaosCtl = &chaosStopControl{}
		h := newHandler("secret")
		code, _ := postChaosWithToken(t, h, "stop", "wrong")
		if code != http.StatusForbidden {
			t.Fatalf("expected 403 for wrong token, got %d", code)
		}
		if liveChaosCtl.Stopped() {
			t.Fatal("kill switch must not trip on wrong token")
		}
	})

	t.Run("trips_switch_with_valid_token", func(t *testing.T) {
		liveChaosCtl = &chaosStopControl{}
		h := newHandler("secret")
		code, _ := postChaosWithToken(t, h, "stop", "secret")
		if code != http.StatusOK {
			t.Fatalf("expected 200, got %d", code)
		}
		if !liveChaosCtl.Stopped() {
			t.Fatal("kill switch must be tripped by valid token")
		}
	})
}

// postChaosWithToken issues an authenticated POST with an X-Chaos-Token header.
func postChaosWithToken(t *testing.T, h *actionHandler, chaosType, token string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/chaos/"+chaosType, bytes.NewReader([]byte("{}")))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("X-Chaos-Token", token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body
}

// TestAgentEligibleForChaosWhitelist locks the whitelist contract: only
// agents declaring at least one whitelisted capability are injectable; the
// empty whitelist disables injection entirely.
func TestAgentEligibleForChaosWhitelist(t *testing.T) {
	ctx := context.Background()
	fabric := agentfabric.NewFabric()
	mustSpawn := func(id string, caps []string) {
		t.Helper()
		if _, err := fabric.Spawn(ctx, agentfabric.SpawnSpec{
			Identity:     id,
			Capabilities: caps,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mustSpawn("coder", []string{"code"})
	mustSpawn("generic", []string{"misc"})

	if agentEligibleForChaos(fabric, "coder", nil) {
		t.Error("empty whitelist must make every agent ineligible")
	}
	if agentEligibleForChaos(fabric, "coder", []string{"browser"}) {
		t.Error("agent without whitelisted capability must be ineligible")
	}
	if !agentEligibleForChaos(fabric, "coder", []string{"browser", "code"}) {
		t.Error("agent declaring a whitelisted capability must be eligible")
	}
	if agentEligibleForChaos(fabric, "ghost", []string{"code"}) {
		t.Error("unknown agent must be ineligible")
	}
}
