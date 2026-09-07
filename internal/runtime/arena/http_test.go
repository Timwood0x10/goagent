package arena

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ares_runtime "github.com/Timwood0x10/ares/internal/ares_runtime"
)

func setupHandler(rt RuntimeProvider, dag DAGProvider) (*Handler, *Service) {
	inj := NewInjector(rt, dag)
	svc := NewService(inj, nil, nil)
	h := NewHandler(svc)
	return h, svc
}

func TestHandleKillLeader_Success(t *testing.T) {
	rt := &mockRuntime{
		listAgentsFn: func() []ares_runtime.AgentInfo {
			return []ares_runtime.AgentInfo{{ID: "leader-1", Type: "leader"}}
		},
	}
	h, _ := setupHandler(rt, nil)

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/arena/leader/kill", nil)
	rec := httptest.NewRecorder()
	h.handleKillLeader(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var result Result
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.True(t, result.Success)
}

func TestHandleKillLeader_NoLeader(t *testing.T) {
	rt := &mockRuntime{
		listAgentsFn: func() []ares_runtime.AgentInfo {
			return []ares_runtime.AgentInfo{{ID: "worker-1", Type: "sub"}}
		},
	}
	h, _ := setupHandler(rt, nil)

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/arena/leader/kill", nil)
	rec := httptest.NewRecorder()
	h.handleKillLeader(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	var result Result
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "leader agent not found")
}

func TestHandleKillAgent_Success(t *testing.T) {
	rt := &mockRuntime{}
	h, _ := setupHandler(rt, nil)

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/arena/agent/agent-1/kill", nil)
	req.SetPathValue("id", "agent-1")
	rec := httptest.NewRecorder()
	h.handleKillAgent(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var result Result
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.True(t, result.Success)
	assert.Equal(t, "agent-1", result.Action.TargetID)
}

func TestHandleKillAgent_MissingID(t *testing.T) {
	h, _ := setupHandler(nil, nil)

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/arena/agent//kill", nil)
	// No path value set.
	rec := httptest.NewRecorder()
	h.handleKillAgent(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleRemoveNode_Success(t *testing.T) {
	dag := &mockDAG{}
	h, _ := setupHandler(nil, dag)

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/arena/node/node-1/remove", nil)
	req.SetPathValue("id", "node-1")
	rec := httptest.NewRecorder()
	h.handleRemoveNode(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var result Result
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.True(t, result.Success)
}

func TestHandleRemoveNode_MissingID(t *testing.T) {
	h, _ := setupHandler(nil, nil)

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/arena/node//remove", nil)
	rec := httptest.NewRecorder()
	h.handleRemoveNode(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleRemoveEdge_Success(t *testing.T) {
	dag := &mockDAG{}
	h, _ := setupHandler(nil, dag)

	body := `{"from":"a","to":"b"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/arena/edge/remove", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.handleRemoveEdge(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var result Result
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.True(t, result.Success)
}

func TestHandleRemoveEdge_InvalidJSON(t *testing.T) {
	h, _ := setupHandler(nil, nil)

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/arena/edge/remove", bytes.NewBufferString("not json"))
	rec := httptest.NewRecorder()
	h.handleRemoveEdge(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleRemoveEdge_MissingFields(t *testing.T) {
	h, _ := setupHandler(nil, nil)

	body := `{"from":"a"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/arena/edge/remove", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.handleRemoveEdge(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleStats(t *testing.T) {
	rt := &mockRuntime{}
	h, _ := setupHandler(rt, nil)

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/arena/stats", nil)
	rec := httptest.NewRecorder()
	h.handleStats(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var stats Stats
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &stats))
	assert.Equal(t, 0, stats.TotalActions)
}

func TestHandleHistory_Empty(t *testing.T) {
	h, _ := setupHandler(nil, nil)

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/arena/history", nil)
	rec := httptest.NewRecorder()
	h.handleHistory(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var history []Result
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &history))
	assert.Empty(t, history)
}

func TestHandleHistory_WithData(t *testing.T) {
	rt := &mockRuntime{}
	h, svc := setupHandler(rt, nil)

	// Execute an action first.
	svc.Execute(context.Background(), Action{
		ID: "hist-1", Type: ActionKillAgent, TargetID: "a-1",
	})

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/arena/history", nil)
	rec := httptest.NewRecorder()
	h.handleHistory(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var history []Result
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &history))
	assert.Len(t, history, 1)
}

func TestValidateAction_KillLeader(t *testing.T) {
	err := ValidateAction(Action{Type: ActionKillLeader})
	assert.NoError(t, err)
}

func TestValidateAction_KillAgent_NoTarget(t *testing.T) {
	err := ValidateAction(Action{Type: ActionKillAgent})
	assert.Error(t, err)
	assert.ErrorContains(t, err, "target_id")
}

func TestValidateAction_KillAgent_WithTarget(t *testing.T) {
	err := ValidateAction(Action{Type: ActionKillAgent, TargetID: "a-1"})
	assert.NoError(t, err)
}

func TestValidateAction_RemoveNode_NoTarget(t *testing.T) {
	err := ValidateAction(Action{Type: ActionRemoveNode})
	assert.Error(t, err)
}

func TestValidateAction_RemoveEdge_MissingFields(t *testing.T) {
	err := ValidateAction(Action{Type: ActionRemoveEdge, SourceID: "a"})
	assert.Error(t, err)
}

func TestValidateAction_RemoveEdge_Complete(t *testing.T) {
	err := ValidateAction(Action{Type: ActionRemoveEdge, SourceID: "a", TargetID: "b"})
	assert.NoError(t, err)
}

func TestValidateAction_EmptyType(t *testing.T) {
	err := ValidateAction(Action{})
	assert.Error(t, err)
	assert.ErrorContains(t, err, "type is required")
}

func TestValidateAction_UnknownType(t *testing.T) {
	err := ValidateAction(Action{Type: "chaos_monkey"})
	assert.Error(t, err)
	assert.ErrorContains(t, err, "unknown action type")
}

func TestParseActionType(t *testing.T) {
	tests := []struct {
		input    string
		expected ActionType
		wantErr  bool
	}{
		{"kill_leader", ActionKillLeader, false},
		{"kill_agent", ActionKillAgent, false},
		{"remove_node", ActionRemoveNode, false},
		{"remove_edge", ActionRemoveEdge, false},
		{"KILL_LEADER", ActionKillLeader, false},
		{"tool_timeout", ActionToolTimeout, false},
		{"memory_corrupt", ActionMemoryCorrupt, false},
		{"mcp_disconnect", ActionMCPDisconnect, false},
		{"llm_failure", ActionLLMFailure, false},
		{"unknown", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result, err := ParseActionType(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestRoutePath(t *testing.T) {
	assert.Equal(t, "POST /arena/leader/kill", RoutePath(ActionKillLeader))
	assert.Equal(t, "POST /arena/agent/{id}/kill", RoutePath(ActionKillAgent))
	assert.Equal(t, "POST /arena/node/{id}/remove", RoutePath(ActionRemoveNode))
	assert.Equal(t, "POST /arena/edge/remove", RoutePath(ActionRemoveEdge))
	assert.Equal(t, "POST /arena/agent/{id}/tool-timeout", RoutePath(ActionToolTimeout))
	assert.Equal(t, "POST /arena/agent/{id}/memory-corrupt", RoutePath(ActionMemoryCorrupt))
	assert.Equal(t, "POST /arena/agent/{id}/mcp-disconnect", RoutePath(ActionMCPDisconnect))
	assert.Equal(t, "POST /arena/agent/{id}/llm-failure", RoutePath(ActionLLMFailure))
	assert.Empty(t, RoutePath("unknown"))
}

func TestRecoverMiddleware_NoPanic(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := RecoverMiddleware(inner)

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRecoverMiddleware_WithPanic(t *testing.T) {
	inner := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic(errors.New("test panic"))
	})
	handler := RecoverMiddleware(inner)

	req := httptest.NewRequestWithContext(context.Background(), "GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestRegisterRoutes(t *testing.T) {
	h, _ := setupHandler(nil, nil)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// Verify that all routes are registered by making requests.
	routes := []struct {
		method string
		path   string
	}{
		{"POST", "/arena/leader/kill"},
		{"GET", "/arena/stats"},
		{"GET", "/arena/history"},
	}

	for _, r := range routes {
		req := httptest.NewRequestWithContext(context.Background(), r.method, r.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		// Should not be 404 (route exists).
		assert.NotEqual(t, http.StatusNotFound, rec.Code, "route not found: %s %s", r.method, r.path)
	}
}

func TestHandleToolTimeout_Success(t *testing.T) {
	rt := &mockRuntime{}
	h, _ := setupHandler(rt, nil)

	body := `{"duration":"5s"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/arena/agent/agent-1/tool-timeout", bytes.NewBufferString(body))
	req.SetPathValue("id", "agent-1")
	rec := httptest.NewRecorder()
	h.handleToolTimeout(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var result Result
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.True(t, result.Success)
	assert.Equal(t, "agent-1", result.Action.TargetID)
}

func TestHandleToolTimeout_MissingID(t *testing.T) {
	h, _ := setupHandler(nil, nil)

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/arena/agent//tool-timeout", nil)
	rec := httptest.NewRecorder()
	h.handleToolTimeout(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleToolTimeout_InvalidJSON(t *testing.T) {
	h, _ := setupHandler(nil, nil)

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/arena/agent/agent-1/tool-timeout", bytes.NewBufferString("not json"))
	req.SetPathValue("id", "agent-1")
	rec := httptest.NewRecorder()
	h.handleToolTimeout(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleMemoryCorrupt_Success(t *testing.T) {
	rt := &mockRuntime{}
	h, _ := setupHandler(rt, nil)

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/arena/agent/agent-1/memory-corrupt", nil)
	req.SetPathValue("id", "agent-1")
	rec := httptest.NewRecorder()
	h.handleMemoryCorrupt(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var result Result
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.True(t, result.Success)
	assert.Equal(t, "agent-1", result.Action.TargetID)
}

func TestHandleMemoryCorrupt_MissingID(t *testing.T) {
	h, _ := setupHandler(nil, nil)

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/arena/agent//memory-corrupt", nil)
	rec := httptest.NewRecorder()
	h.handleMemoryCorrupt(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleMCPDisconnect_Success(t *testing.T) {
	rt := &mockRuntime{}
	h, _ := setupHandler(rt, nil)

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/arena/agent/agent-1/mcp-disconnect", nil)
	req.SetPathValue("id", "agent-1")
	rec := httptest.NewRecorder()
	h.handleMCPDisconnect(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var result Result
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.True(t, result.Success)
	assert.Equal(t, "agent-1", result.Action.TargetID)
}

func TestHandleMCPDisconnect_MissingID(t *testing.T) {
	h, _ := setupHandler(nil, nil)

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/arena/agent//mcp-disconnect", nil)
	rec := httptest.NewRecorder()
	h.handleMCPDisconnect(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleLLMFailure_Success(t *testing.T) {
	rt := &mockRuntime{}
	h, _ := setupHandler(rt, nil)

	body := `{"error_type":"rate_limit"}`
	req := httptest.NewRequestWithContext(context.Background(), "POST", "/arena/agent/agent-1/llm-failure", bytes.NewBufferString(body))
	req.SetPathValue("id", "agent-1")
	rec := httptest.NewRecorder()
	h.handleLLMFailure(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var result Result
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
	assert.True(t, result.Success)
	assert.Equal(t, "agent-1", result.Action.TargetID)
}

func TestHandleLLMFailure_MissingID(t *testing.T) {
	h, _ := setupHandler(nil, nil)

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/arena/agent//llm-failure", nil)
	rec := httptest.NewRecorder()
	h.handleLLMFailure(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleLLMFailure_InvalidJSON(t *testing.T) {
	h, _ := setupHandler(nil, nil)

	req := httptest.NewRequestWithContext(context.Background(), "POST", "/arena/agent/agent-1/llm-failure", bytes.NewBufferString("not json"))
	req.SetPathValue("id", "agent-1")
	rec := httptest.NewRecorder()
	h.handleLLMFailure(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestAPIKeyAuthMiddleware_DenyByDefault pins the arena auth posture.
//
// The middleware previously treated an unset API key as "auth disabled" and
// passed every request through. Because arena exposes destructive endpoints
// (leader/agent kill, node removal, memory corruption), a missing key must be
// read as a misconfiguration and denied. Anonymous access is only permitted
// when a caller opts in explicitly via AllowAnonymous.
func TestAPIKeyAuthMiddleware_DenyByDefault(t *testing.T) {
	newGuarded := func(configure func(h *Handler)) http.Handler {
		h, _ := setupHandler(nil, nil)
		configure(h)
		reached := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		return h.APIKeyAuthMiddleware(reached)
	}

	tests := []struct {
		name       string
		configure  func(h *Handler)
		headerKey  string
		wantStatus int
	}{
		{
			name:       "no key configured is denied",
			configure:  func(*Handler) {},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "no key configured still denied when caller sends one",
			configure:  func(*Handler) {},
			headerKey:  "guessed",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "explicit anonymous opt-in allows access",
			configure:  func(h *Handler) { h.AllowAnonymous(true) },
			wantStatus: http.StatusOK,
		},
		{
			name:       "configured key rejects missing header",
			configure:  func(h *Handler) { h.SetAPIKey("secret") },
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "configured key rejects wrong header",
			configure:  func(h *Handler) { h.SetAPIKey("secret") },
			headerKey:  "wrong",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "configured key accepts matching header",
			configure:  func(h *Handler) { h.SetAPIKey("secret") },
			headerKey:  "secret",
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/arena/leader/kill", nil)
			if tt.headerKey != "" {
				req.Header.Set(apiKeyHeader, tt.headerKey)
			}
			rec := httptest.NewRecorder()
			newGuarded(tt.configure).ServeHTTP(rec, req)
			assert.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}
