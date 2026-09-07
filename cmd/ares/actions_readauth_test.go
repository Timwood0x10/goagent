package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_security"
	"github.com/Timwood0x10/ares/internal/introspect"
)

// newReadAuthHandler builds an actionHandler with auth configured the way
// serve_routine does for security.auth_enabled=true: a WRITE middleware for
// destructive routes and a READ middleware for the JSON read surfaces.
// A real intro Handler + inner pass-through make the /api/v1/introspect/*
// routes reach the auth gate instead of falling through to a nil inner.
func newReadAuthHandler() *actionHandler {
	secret := []byte("t7-test-secret")
	audit := ares_security.NewAuditLogger(nil)
	return &actionHandler{
		apiKey:   "t7-api-key",
		inner:    http.NotFoundHandler(),
		intro:    introspect.NewHandler(&introspect.Store{}),
		auth:     ares_security.NewAuthMiddleware(secret, ares_security.PermWrite, ares_security.WithAudit(audit)),
		readAuth: ares_security.NewAuthMiddleware(secret, ares_security.PermRead, ares_security.WithAudit(audit)),
	}
}

// TestReadEndpointsRequireCredentials is the read-auth acceptance matrix: with auth
// configured, every JSON read surface rejects unauthenticated requests with
// 401 — the tool inventories, the introspect snapshot/eventstream feed, and
// the cost API. These endpoints expose task payloads and the tool/MCP
// topology, so "read-only" must not mean "unauthenticated" once auth is on.
func TestReadEndpointsRequireCredentials(t *testing.T) {
	h := newReadAuthHandler()
	endpoints := []string{
		"/api/tools",
		"/api/mcp/tools",
		"/api/v1/introspect/snapshot",
		"/api/v1/introspect/events",
	}
	for _, ep := range endpoints {
		t.Run(ep, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ep, nil))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("GET %s without credentials = %d, want 401", ep, rec.Code)
			}
		})
	}
}

// TestReadEndpointsAcceptAPIKey verifies the API-key read path (a write
// credential may read) still serves the gated read surfaces.
func TestReadEndpointsAcceptAPIKey(t *testing.T) {
	h := newReadAuthHandler()
	for _, ep := range []string{"/api/tools", "/api/mcp/tools"} {
		req := httptest.NewRequest(http.MethodGet, ep, nil)
		req.Header.Set("Authorization", "Bearer t7-api-key")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == http.StatusUnauthorized {
			t.Fatalf("GET %s with API key = 401, key must grant read access", ep)
		}
	}
}

// TestReadEndpointsAcceptJWTReadRole verifies a RoleAgent JWT (read-only role)
// passes the read gate but is rejected by the WRITE gate.
func TestReadEndpointsAcceptJWTReadRole(t *testing.T) {
	secret := []byte("t7-test-secret")
	token, err := ares_security.SignJWT(secret, "agent-7", string(ares_security.RoleAgent), time.Hour, time.Now())
	if err != nil {
		t.Fatalf("issue jwt: %v", err)
	}
	h := newReadAuthHandler()

	// Read gate: agent role holds PermRead → allowed.
	req := httptest.NewRequest(http.MethodGet, "/api/tools", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
		t.Fatalf("GET /api/tools with agent-role JWT = %d, want allowed", rec.Code)
	}

	// Write gate: the same token must NOT kill an agent (PermWrite missing).
	req = httptest.NewRequest(http.MethodPost, "/api/agents/coder/kill", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST kill with agent-role JWT = %d, want 403", rec.Code)
	}
}

// TestControlServerReadRoutesRequireCredentials covers the pass-through read
// surfaces served by introspect.ControlServer. One policy applies across
// equally sensitive read endpoints: the flight recorder exposes scheduling
// decisions and diagnostics, /api/agents the live agent topology, so these
// must not stay open while /api/v1/introspect/* is gated.
func TestControlServerReadRoutesRequireCredentials(t *testing.T) {
	h := newReadAuthHandler()
	endpoints := []string{
		"/api/agents",
		"/api/health",
		"/api/runtime/config",
		"/api/flight/timeline",
		"/api/flight/decisions",
		"/api/observability/spans",
		"/api/insights",
	}
	for _, ep := range endpoints {
		t.Run(ep, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, ep, nil))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("GET %s without credentials = %d, want 401", ep, rec.Code)
			}
		})
	}

	// With a credential the request reaches the inner server (404 here since
	// the test's inner is a NotFoundHandler) — the point is: not a 401.
	req := httptest.NewRequest(http.MethodGet, "/api/agents", nil)
	req.Header.Set("Authorization", "Bearer t7-api-key")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Fatal("GET /api/agents with API key = 401, key must grant read access")
	}
}

// TestNonAPIPathsStayUngated verifies the read gate is scoped to /api/*: an
// unknown non-API path must still fall through to the inner handler so a
// mistyped URL does not require a token to learn it is a 404.
func TestNonAPIPathsStayUngated(t *testing.T) {
	h := newReadAuthHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/not-a-route", nil))
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("GET /not-a-route = 401, non-API paths must stay ungated")
	}
}

// TestReadEndpointsOpenWhenAuthUnconfigured verifies the local-dev contract:
// with no auth configured at all, the read surfaces stay open (protected only
// by the loopback default bind). Destructive endpoints remain
// deny-by-default regardless.
func TestReadEndpointsOpenWhenAuthUnconfigured(t *testing.T) {
	// No apiKey, no auth middleware; a real intro + inner so the JSON feed
	// route reaches the handler rather than a nil inner.
	h := &actionHandler{inner: http.NotFoundHandler(), intro: introspect.NewHandler(&introspect.Store{})}
	for _, ep := range []string{"/api/tools", "/api/mcp/tools", "/api/v1/introspect/snapshot"} {
		req := httptest.NewRequest(http.MethodGet, ep, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code == http.StatusUnauthorized {
			t.Fatalf("GET %s with auth unconfigured = 401, local-dev access must stay open", ep)
		}
	}
}
