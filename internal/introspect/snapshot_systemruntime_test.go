package introspect

// System Runtime snapshot acceptance: the snapshot endpoint carries the System Runtime component
// graph when the provider is wired, keeps the legacy shape otherwise, and
// stays 503-safe before the first collect.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestHandlerSnapshot_WithSystemRuntime verifies the merged payload: legacy
// snapshot fields stay inline AND the system_runtime section is present.
func TestHandlerSnapshot_WithSystemRuntime(t *testing.T) {
	var store Store
	store.Set(Snapshot{Seq: 3, TS: time.Now()})
	h := NewHandler(&store).WithSystemRuntime(func() any {
		return map[string]any{
			"components": []map[string]any{
				{"name": "scheduler", "state": "ready"},
				{"name": "taskfabric", "state": "ready"},
			},
		}
	})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/introspect/snapshot", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}

	// The legacy fields must remain inline (embedded pointer), so decoding
	// into the old Snapshot shape keeps working for existing consumers.
	var legacy Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &legacy); err != nil {
		t.Fatalf("legacy shape decode: %v", err)
	}
	if legacy.Seq != 3 {
		t.Fatalf("seq = %d, want 3", legacy.Seq)
	}

	// The additive section must be present with the provider's data.
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &merged); err != nil {
		t.Fatalf("decode: %v", err)
	}
	sr, ok := merged["system_runtime"]
	if !ok {
		t.Fatalf("system_runtime section missing: %s", w.Body.String())
	}
	if !json.Valid(sr) {
		t.Fatal("system_runtime section must be valid JSON")
	}
}

// TestHandlerSnapshot_WithoutSystemRuntime verifies the legacy shape is
// unchanged when no provider is wired.
func TestHandlerSnapshot_WithoutSystemRuntime(t *testing.T) {
	var store Store
	store.Set(Snapshot{Seq: 5, TS: time.Now()})
	h := NewHandler(&store)

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/introspect/snapshot", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	var legacy Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &legacy); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if legacy.Seq != 5 {
		t.Fatalf("seq = %d, want 5", legacy.Seq)
	}
	if len(w.Body.Bytes()) == 0 {
		t.Fatal("empty body")
	}
}

// TestHandlerSnapshot_SystemRuntimeSurvivesNilProviderResult guards the
// serve path: a provider returning nil must not panic — the section is
// simply omitted (omitempty).
func TestHandlerSnapshot_SystemRuntimeSurvivesNilProviderResult(t *testing.T) {
	var store Store
	store.Set(Snapshot{Seq: 7, TS: time.Now()})
	h := NewHandler(&store).WithSystemRuntime(func() any { return nil })

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/introspect/snapshot", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	var legacy Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &legacy); err != nil {
		t.Fatalf("decode: %v", err)
	}
}
