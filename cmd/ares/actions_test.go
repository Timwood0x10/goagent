package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	api_tools "github.com/Timwood0x10/ares/api/tools"
	"github.com/Timwood0x10/ares/internal/agents/base"
	"github.com/Timwood0x10/ares/internal/ares_security"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/runtime"
)

const testActionJWTSecret = "test-action-jwt-secret"

// actionTestEnv builds an actionHandler backed by a real Manager with one
// registered agent, plus a JWT middleware and audit sink on the same secret.
type actionTestEnv struct {
	h        *actionHandler
	mgr      *runtime.Manager
	auditBuf *bytes.Buffer
}

func newActionTestEnv(t *testing.T) *actionTestEnv {
	t.Helper()
	mgr := runtime.New(nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); _ = mgr.Stop() })
	require.NoError(t, mgr.Start(ctx))

	// Register + start one agent so kill/resume have a target.
	agent := newActionTestAgent("act-1")
	mgr.RegisterAgent(agent, func() base.Agent { return newActionTestAgent("act-1") })
	require.NoError(t, mgr.StartAgent(ctx, agent))

	var auditBuf bytes.Buffer
	audit := ares_security.NewAuditLogger(slog.New(slog.NewTextHandler(&auditBuf, nil)))
	auth := ares_security.NewAuthMiddleware([]byte(testActionJWTSecret), ares_security.PermWrite,
		ares_security.WithAudit(audit))

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return &actionTestEnv{
		h:        &actionHandler{inner: inner, mgr: mgr, apiKey: "test-key", auth: auth, audit: audit},
		mgr:      mgr,
		auditBuf: &auditBuf,
	}
}

// testActionJWT mints a JWT for the given role on the test secret.
func testActionJWT(t *testing.T, role ares_security.Role) string {
	t.Helper()
	tok, err := ares_security.SignJWT([]byte(testActionJWTSecret), "ops-user", string(role), time.Hour, time.Now())
	require.NoError(t, err)
	return tok
}

// TestActionHandler_JWTAcceptedOnKill verifies the actionHandler (the
// production entry for POST /api/agents/:id/kill) accepts a valid JWT — the
// v0.3.0 review gap where interception bypassed JWT entirely.
func TestActionHandler_JWTAcceptedOnKill(t *testing.T) {
	env := newActionTestEnv(t)

	req := httptest.NewRequest(http.MethodPost, "/api/agents/act-1/kill", nil)
	req.Header.Set("Authorization", "Bearer "+testActionJWT(t, ares_security.RoleOperator))
	rec := httptest.NewRecorder()
	env.h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	// Audit must record the kill with the JWT subject.
	require.Contains(t, env.auditBuf.String(), "action=kill")
	require.Contains(t, env.auditBuf.String(), "subject=ops-user")
}

// TestActionHandler_JWTRejectedOnInsufficientRole verifies an agent role
// (read-only) cannot kill an agent via the actionHandler. The token is valid
// but lacks write permission, so the rejection is 403 Forbidden (not 401),
// matching the gin middleware's unauthenticated-vs-forbidden distinction.
func TestActionHandler_JWTRejectedOnInsufficientRole(t *testing.T) {
	env := newActionTestEnv(t)

	req := httptest.NewRequest(http.MethodPost, "/api/agents/act-1/kill", nil)
	req.Header.Set("Authorization", "Bearer "+testActionJWT(t, ares_security.RoleAgent))
	rec := httptest.NewRecorder()
	env.h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
}

// TestActionHandler_APIKeyStillAccepted verifies backward compatibility: the
// legacy API key keeps working on the same routes.
func TestActionHandler_APIKeyStillAccepted(t *testing.T) {
	env := newActionTestEnv(t)

	req := httptest.NewRequest(http.MethodPost, "/api/agents/act-1/kill", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()
	env.h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	// API-key subject is recorded in the audit.
	require.Contains(t, env.auditBuf.String(), "subject=api-key")
}

// TestActionHandler_DeniesWhenNoCredentials verifies deny-by-default when
// neither API key nor JWT is configured.
func TestActionHandler_DeniesWhenNoCredentials(t *testing.T) {
	env := newActionTestEnv(t)
	env.h.auth = nil
	env.h.apiKey = ""

	req := httptest.NewRequest(http.MethodPost, "/api/agents/act-1/kill", nil)
	rec := httptest.NewRecorder()
	env.h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
}

// TestActionHandler_ToolCallAudited verifies the tool-call path records the
// action on the audit sink (the call path, not the nil-registry shortcut).
func TestActionHandler_ToolCallAudited(t *testing.T) {
	env := newActionTestEnv(t)
	env.h.tools = api_tools.NewRegistry() // empty registry: Execute returns not-found

	req := httptest.NewRequest(http.MethodPost, "/api/tools/call", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Authorization", "Bearer "+testActionJWT(t, ares_security.RoleOperator))
	rec := httptest.NewRecorder()
	env.h.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String()) // tool not found
	require.Contains(t, env.auditBuf.String(), "action=call_tool")
}

// newActionTestAgent is a minimal base.Agent for the actionHandler tests.
func newActionTestAgent(id string) base.Agent {
	return &actionTestAgent{id: id}
}

type actionTestAgent struct{ id string }

func (a *actionTestAgent) ID() string                  { return a.id }
func (a *actionTestAgent) Type() models.AgentType      { return models.AgentTypeBottom }
func (a *actionTestAgent) Status() models.AgentStatus  { return models.AgentStatusReady }
func (a *actionTestAgent) Start(context.Context) error { return nil }
func (a *actionTestAgent) Stop(context.Context) error  { return nil }
func (a *actionTestAgent) Process(context.Context, any) (any, error) {
	return nil, nil
}
func (a *actionTestAgent) ProcessStream(context.Context, any) (<-chan base.AgentEvent, error) {
	return nil, nil
}
