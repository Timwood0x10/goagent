package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/agentfabric"
	"github.com/Timwood0x10/ares/internal/aresrecovery"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/planprojection"
	"github.com/Timwood0x10/ares/internal/taskfabric"
)

// recordingPeerCognition completes every task in one quantum and records the
// executed task ids — the peer runtime's execution body for this test.
type recordingPeerCognition struct {
	mu       sync.Mutex
	executed []string
}

func (c *recordingPeerCognition) ExecuteStep(_ context.Context, task *models.Task) (*agentfabric.StepOutcome, error) {
	c.mu.Lock()
	c.executed = append(c.executed, task.TaskID)
	c.mu.Unlock()
	res := models.NewTaskResult(task.TaskID, task.AgentType)
	res.SetSuccess(nil, "done")
	return &agentfabric.StepOutcome{Done: true, Result: res}, nil
}

func (c *recordingPeerCognition) executedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.executed)
}

// buildTestPeerKernel assembles a minimal peer kernel (fabric + agents +
// scheduler) exactly as createPeerAgents does, minus the LLM — enough for the
// POST /api/tasks handler to submit a task that the scheduler executes.
func buildTestPeerKernel(t *testing.T, ctx context.Context) (*kernelHandle, *recordingPeerCognition) {
	t.Helper()
	kernel := &kernelHandle{}
	kernel.fabric = taskfabric.NewFabric()
	agents := agentfabric.NewFabric()
	kernel.agents = agents
	// M4-D: the submission path always admits sessions, so the minimal
	// kernel needs the same registry + coordinator pair createPeerAgents
	// wires in production.
	kernel.sessionReg = agentfabric.NewSessionRegistry()
	kernel.compileCoord = planprojection.NewCompileCoordinator(kernel.fabric, nil)
	cog := &recordingPeerCognition{}

	// Spawn the peer agent WITH its execution body (A1), advertising the
	// production L2 capability set (M4-D: no legacy capabilities).
	if _, err := agents.Spawn(ctx, agentfabric.SpawnSpec{
		Identity:     "coder",
		Capabilities: peerCapabilities(nil),
		CognitionFactory: func([]string) agentfabric.Cognition {
			return cog
		},
	}); err != nil {
		t.Fatalf("spawn peer agent: %v", err)
	}

	kernel.executors = map[string]CapabilityExecutor{}
	tracker := newLoadTracker()
	kernel.tracker = tracker
	sched := NewKernelScheduler(kernel.fabric, kernel.executors, tracker)
	sched.PollInterval = 10 * time.Millisecond
	sched.WithAgentFabric(agents)
	kernel.scheduler = sched
	kernel.recovery = aresrecovery.New(kernel.fabric, agents, aresrecovery.DefaultRestartPolicy())
	go sched.Run(ctx)
	return kernel, cog
}

// authorizedTasksRequest builds a POST /api/tasks request carrying the
// legacy API key so checkAuth permits it (deny-by-default without a credential).
func authorizedTasksRequest(t *testing.T, body []byte) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/tasks", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-key")
	return req
}

// TestHTTPSubmitTask is the peer HTTP entry acceptance (POST /api/tasks →
// submitPeerTask → fabric → scheduler): a task submitted over HTTP in Leader
// OFF mode is created in the Task Fabric, executed by the peer agent through
// the scheduler, and the endpoint returns the assigned task id.
func TestHTTPSubmitTask(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	kernel, cog := buildTestPeerKernel(t, ctx)

	h := &actionHandler{kernel: kernel, apiKey: "test-key"}
	body, _ := json.Marshal(map[string]any{
		"capability": "code",
		"payload":    map[string]any{"task_desc": "fix the bug"},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authorizedTasksRequest(t, body))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body: %s)", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if taskID, _ := resp["task_id"].(string); taskID == "" {
		t.Fatal("response must carry the assigned task id")
	}

	// The scheduler must execute the submitted task (poll the fabric).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cog.executedCount() >= 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("submitted task was never executed by the peer scheduler")
}

// TestHTTPSubmitTaskRejectsMissingCapability verifies the request contract:
// a task without a capability is a 400, not a submission attempt.
func TestHTTPSubmitTaskRejectsMissingCapability(t *testing.T) {
	kernel := &kernelHandle{}
	h := &actionHandler{kernel: kernel, apiKey: "test-key"}
	body, _ := json.Marshal(map[string]any{"payload": map[string]any{}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authorizedTasksRequest(t, body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestHTTPSubmitTaskNoPeerKernel verifies the deny-by-default contract: the
// legacy leader path (kernel == nil) reports 503, not a silent no-op.
func TestHTTPSubmitTaskNoPeerKernel(t *testing.T) {
	h := &actionHandler{apiKey: "test-key"}
	body, _ := json.Marshal(map[string]any{"capability": "code"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authorizedTasksRequest(t, body))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
