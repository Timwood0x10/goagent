package ares_security

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testSecret = "test-secret"

func testToken(t *testing.T, role, subject string) string {
	t.Helper()
	tok, err := SignJWT([]byte(testSecret), subject, role, time.Hour, time.Now())
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}
	return tok
}

func TestVerifyAllowsValidToken(t *testing.T) {
	mw := NewAuthMiddleware([]byte(testSecret), PermWrite)
	tok := testToken(t, "operator", "alice")

	req := httptest.NewRequest(http.MethodPost, "/api/agents/1/kill", nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	p, status := mw.Verify(req)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if p == nil || p.Subject != "alice" || p.Role != RoleOperator {
		t.Fatalf("principal = %+v, want alice/operator", p)
	}
}

func TestVerifyDeniesMissingToken(t *testing.T) {
	mw := NewAuthMiddleware([]byte(testSecret), PermWrite)
	req := httptest.NewRequest(http.MethodPost, "/api/agents/1/kill", nil)
	p, status := mw.Verify(req)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
	if p != nil {
		t.Fatal("principal must be nil on deny")
	}
}

func TestVerifyDeniesWrongScheme(t *testing.T) {
	mw := NewAuthMiddleware([]byte(testSecret), PermWrite)
	tok := testToken(t, "operator", "alice")
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("Authorization", "Basic "+tok) // wrong scheme
	_, status := mw.Verify(req)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
}

func TestVerifyDeniesInsufficientRole(t *testing.T) {
	mw := NewAuthMiddleware([]byte(testSecret), PermWrite)
	tok := testToken(t, "agent", "bob") // read-only role
	req := httptest.NewRequest(http.MethodPost, "/api/agents/1/kill", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	_, status := mw.Verify(req)
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", status)
	}
}

func TestVerifyDeniesWhenSecretNil(t *testing.T) {
	// Nil secret = deny all (misconfig safety).
	mw := NewAuthMiddleware(nil, PermWrite)
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	_, status := mw.Verify(req)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
}

// TestVerifyDeniesTokenForgedWithEmptyKey is the case the deny-by-default claim
// actually has to survive: an attacker who knows the deployment forgot its key
// can mint a perfectly well-formed HS256 token under the EMPTY key. Sending no
// header at all only exercises the "missing token" branch, so this test forges
// a real token and asserts it is rejected — and rejected as a server fault
// (503), not as a client mistake.
func TestVerifyDeniesTokenForgedWithEmptyKey(t *testing.T) {
	forged, err := encodeSigned(nil, jwtClaims{
		Subject: "alice",
		Role:    string(RoleAdmin),
		Expires: time.Now().Add(time.Hour).Unix(),
		Issued:  time.Now().Unix(),
	})
	if err != nil {
		t.Fatalf("forge: %v", err)
	}

	for _, mw := range []*AuthMiddleware{
		NewAuthMiddleware(nil, PermWrite),
		NewAuthMiddleware([]byte{}, PermWrite),
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/agents/1/kill", nil)
		req.Header.Set("Authorization", "Bearer "+forged)
		p, status := mw.Verify(req)
		if p != nil {
			t.Fatalf("forged empty-key token must yield no principal, got %+v", p)
		}
		if status != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 (unconfigured key is a server fault)", status)
		}
	}
}

func TestFromContextNilOnUnprotected(t *testing.T) {
	if p := FromContext(context.Background()); p != nil {
		t.Fatalf("FromContext on plain ctx = %+v, want nil", p)
	}
}

// TestVerifyAuditsThroughModule verifies auth decisions reach the modular audit
// sink (WithAudit), both allow and deny paths.
func TestVerifyAuditsThroughModule(t *testing.T) {
	audit, buf := newTestAuditLogger(t)
	mw := NewAuthMiddleware([]byte(testSecret), PermWrite, WithAudit(audit))
	tok := testToken(t, "operator", "alice")

	// Allow path.
	req := httptest.NewRequest(http.MethodPost, "/api/agents/1/kill", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	mw.Verify(req)

	// Deny path (missing token).
	req = httptest.NewRequest(http.MethodPost, "/api/agents/1/kill", nil)
	mw.Verify(req)

	out := buf.String()
	// slog's TextHandler quotes values containing spaces.
	if !strings.Contains(out, "decision=allowed") || !strings.Contains(out, `decision="missing bearer token"`) {
		t.Fatalf("audit sink must record both allow and deny decisions; got:\n%s", out)
	}
	if !strings.Contains(out, "subject=alice") {
		t.Fatalf("audit must carry the subject; got:\n%s", out)
	}
}
