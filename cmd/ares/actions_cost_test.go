package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Timwood0x10/ares/internal/introspect"
	"github.com/Timwood0x10/ares/internal/runtime/observability"
)

// TestActionHandlerCostRoutesWired locks the construction contract: a
// handler with the cost dashboard wired must serve the read-only cost routes.
// The mux is built through the same buildCostMux call the construction
// literal in serve_routine.go uses — an earlier revision set cost without
// costMux and every dashboard request panicked on the nil mux.
func TestActionHandlerCostRoutesWired(t *testing.T) {
	dash := observability.NewCostDashboard()
	h := &actionHandler{
		intro:   introspect.NewHandler(&introspect.Store{}),
		cost:    dash,
		costMux: buildCostMux(dash), // same construction path as serve_routine.go
	}
	tests := []struct {
		path string
		want int
	}{
		{"/api/v1/observability/cost", http.StatusOK},
		{"/api/v1/observability/dashboard", http.StatusOK},
		{"/api/v1/observability/cost/no-such-session", http.StatusNotFound},
	}
	for _, tc := range tests {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.want {
			t.Fatalf("GET %s: status = %d, want %d (body: %s)", tc.path, rec.Code, tc.want, rec.Body.String())
		}
	}
}

// TestBuildCostMuxNilDash pins the cost/costMux pairing rule: no dashboard
// means no mux, matching the nil-cost fallthrough in serveIntrospect.
func TestBuildCostMuxNilDash(t *testing.T) {
	if buildCostMux(nil) != nil {
		t.Fatal("buildCostMux(nil) must return nil so costMux stays nil when cost is nil")
	}
}
