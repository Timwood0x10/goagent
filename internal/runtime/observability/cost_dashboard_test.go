// nolint: errcheck // Test code may ignore return values
package observability

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewCostDashboard(t *testing.T) {
	dash := NewCostDashboard()
	if dash == nil {
		t.Fatal("expected non-nil dashboard")
	}

	sessions := dash.GetAllSessions()
	if len(sessions) != 0 {
		t.Errorf("expected empty sessions for new dashboard, got %d", len(sessions))
	}
}

func TestNewCostDashboardWithPricing(t *testing.T) {
	pricing := PricingConfig{
		Models: map[string]ModelPricing{
			"custom-model": {InputCostPer1K: 0.001, OutputCostPer1K: 0.002},
		},
	}
	dash := NewCostDashboardWithPricing(pricing)
	if dash == nil {
		t.Fatal("expected non-nil dashboard")
	}

	tracker := dash.RegisterSession("s1")
	tracker.RecordCall("custom-model", 1000, 500)
	if tracker.TotalCost() == 0 {
		t.Error("expected non-zero cost with custom pricing")
	}
}

func TestCostDashboard_RegisterSession(t *testing.T) {
	dash := NewCostDashboard()

	tracker1 := dash.RegisterSession("session-a")
	tracker2 := dash.RegisterSession("session-b")

	if tracker1 == nil || tracker2 == nil {
		t.Fatal("expected non-nil trackers")
	}

	// Registering the same session again should return the same tracker.
	tracker1Again := dash.RegisterSession("session-a")
	if tracker1 != tracker1Again {
		t.Error("expected same tracker instance for duplicate session registration")
	}
}

func TestCostDashboard_GetSessionCost_Found(t *testing.T) {
	dash := NewCostDashboard()
	tracker := dash.RegisterSession("sess-1")

	tracker.RecordCall("gpt-4o", 1000, 500)

	report, found := dash.GetSessionCost("sess-1")
	if !found {
		t.Fatal("expected session to be found")
	}

	if report.SessionID != "sess-1" {
		t.Errorf("expected session_id sess-1, got: %s", report.SessionID)
	}
	if report.TotalCost != 0.0075 {
		t.Errorf("expected total cost 0.0075, got: %.4f", report.TotalCost)
	}
	if report.CallCount != 1 {
		t.Errorf("expected call count 1, got: %d", report.CallCount)
	}
	if report.TotalInput != 1000 {
		t.Errorf("expected input tokens 1000, got: %d", report.TotalInput)
	}
	if report.TotalOutput != 500 {
		t.Errorf("expected output tokens 500, got: %d", report.TotalOutput)
	}
	if len(report.Entries) != 1 {
		t.Errorf("expected 1 entry, got: %d", len(report.Entries))
	}
	if report.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
	if report.LastActivity.IsZero() {
		t.Error("expected non-zero LastActivity")
	}
}

func TestCostDashboard_GetSessionCost_NotFound(t *testing.T) {
	dash := NewCostDashboard()

	report, found := dash.GetSessionCost("nonexistent")
	if found {
		t.Error("expected session not to be found")
	}
	if report.SessionID != "" {
		t.Errorf("expected empty SessionID for missing session, got: %s", report.SessionID)
	}
}

func TestCostDashboard_GetAllSessions_Ordering(t *testing.T) {
	dash := NewCostDashboard()

	dash.RegisterSession("first")
	dash.RegisterSession("second")
	dash.RegisterSession("third")

	sessions := dash.GetAllSessions()
	if len(sessions) != 3 {
		t.Fatalf("expected 3 sessions, got: %d", len(sessions))
	}

	// Verify insertion order is preserved.
	if sessions[0].SessionID != "first" {
		t.Errorf("expected first session, got: %s", sessions[0].SessionID)
	}
	if sessions[1].SessionID != "second" {
		t.Errorf("expected second session, got: %s", sessions[1].SessionID)
	}
	if sessions[2].SessionID != "third" {
		t.Errorf("expected third session, got: %s", sessions[2].SessionID)
	}
}

func TestCostDashboard_GetAllSessions_AfterRecording(t *testing.T) {
	dash := NewCostDashboard()

	tracker := dash.RegisterSession("s1")
	tracker.RecordCall("gpt-4o", 1000, 500)

	t2 := dash.RegisterSession("s2")
	t2.RecordCall("gpt-3.5-turbo", 2000, 1000)

	sessions := dash.GetAllSessions()
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions, got: %d", len(sessions))
	}

	if sessions[0].TotalCost != 0.0075 {
		t.Errorf("expected s1 cost 0.0075, got: %.4f", sessions[0].TotalCost)
	}
	if sessions[1].TotalCost != 0.0025 {
		t.Errorf("expected s2 cost 0.0025, got: %.4f", sessions[1].TotalCost)
	}
}

func TestCostDashboard_GenerateDashboardHTML_Empty(t *testing.T) {
	dash := NewCostDashboard()

	html := dash.GenerateDashboardHTML()

	if !strings.Contains(html, "<!DOCTYPE html>") {
		t.Error("expected HTML doctype")
	}
	if !strings.Contains(html, "ARES Cost Dashboard") {
		t.Error("expected title in HTML")
	}
	if !strings.Contains(html, "GRAND TOTAL") {
		t.Error("expected grand total row in HTML")
	}
	if !strings.Contains(html, "$0.0000") {
		t.Error("expected zero grand total for empty dashboard")
	}
}

func TestCostDashboard_GenerateDashboardHTML_WithData(t *testing.T) {
	dash := NewCostDashboard()

	tracker := dash.RegisterSession("my-session")
	tracker.RecordCall("gpt-4o", 1000, 500)

	html := dash.GenerateDashboardHTML()

	if !strings.Contains(html, "my-session") {
		t.Error("expected session ID in HTML table")
	}
	if !strings.Contains(html, "$0.0075") {
		t.Error("expected cost value in HTML table")
	}
	if !strings.Contains(html, "1000") {
		t.Error("expected input token count in HTML")
	}
	if !strings.Contains(html, "500") {
		t.Error("expected output token count in HTML")
	}
}

func TestCostDashboard_NilSafe(t *testing.T) {
	var dash *CostDashboard

	tracker := dash.RegisterSession("test")
	if tracker != nil {
		t.Error("expected nil tracker from nil dashboard")
	}

	_, found := dash.GetSessionCost("test")
	if found {
		t.Error("expected false from nil dashboard GetSessionCost")
	}

	sessions := dash.GetAllSessions()
	if sessions != nil {
		t.Error("expected nil from nil dashboard GetAllSessions")
	}

	html := dash.GenerateDashboardHTML()
	if !strings.Contains(html, "No dashboard available") {
		t.Error("expected fallback message from nil dashboard")
	}
}

func TestCostDashboard_ConcurrentAccess(t *testing.T) {
	dash := NewCostDashboard()

	var wg sync.WaitGroup
	numGoroutines := 10
	sessionsPerGoroutine := 5

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < sessionsPerGoroutine; j++ {
				sessionID := strings.Join([]string{"session", string(rune('A' + idx)), string(rune('0' + j))}, "-")
				tracker := dash.RegisterSession(sessionID)
				tracker.RecordCall("gpt-4o", 100, 50)
				dash.GetSessionCost(sessionID)
			}
			dash.GetAllSessions()
		}(i)
	}

	wg.Wait()

	sessions := dash.GetAllSessions()
	expectedCount := numGoroutines * sessionsPerGoroutine
	if len(sessions) != expectedCount {
		t.Errorf("expected %d sessions, got: %d", expectedCount, len(sessions))
	}
}

func TestCostDashboard_HTTP_AllCostsEndpoint(t *testing.T) {
	dash := NewCostDashboard()
	dash.RegisterSession("s1").RecordCall("gpt-4o", 1000, 500)
	dash.RegisterSession("s2").RecordCall("gpt-3.5-turbo", 2000, 1000)

	mux := http.NewServeMux()
	dash.RegisterCostRoutes(mux)

	// GET /api/v1/observability/cost
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/observability/cost", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp AllSessionsResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.TotalSessions != 2 {
		t.Errorf("expected 2 sessions, got: %d", resp.TotalSessions)
	}
	if len(resp.Sessions) != 2 {
		t.Errorf("expected 2 session entries, got: %d", len(resp.Sessions))
	}
	if resp.GrandTotalCost != 0.0100 {
		t.Errorf("expected grand total 0.0100, got: %.4f", resp.GrandTotalCost)
	}
}

func TestCostDashboard_HTTP_SessionDetailEndpoint(t *testing.T) {
	dash := NewCostDashboard()
	dash.RegisterSession("detail-session").RecordCall("gpt-4o", 1500, 750)

	mux := http.NewServeMux()
	dash.RegisterCostRoutes(mux)

	// GET /api/v1/observability/cost/detail-session
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/observability/cost/detail-session", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var report CostReport
	if err := json.NewDecoder(rec.Body).Decode(&report); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if report.SessionID != "detail-session" {
		t.Errorf("expected session_id detail-session, got: %s", report.SessionID)
	}
	if report.TotalCost != 0.01125 {
		t.Errorf("expected cost 0.01125, got: %.5f", report.TotalCost)
	}
	if len(report.Entries) != 1 {
		t.Errorf("expected 1 entry, got: %d", len(report.Entries))
	}
}

func TestCostDashboard_HTTP_SessionNotFound(t *testing.T) {
	dash := NewCostDashboard()

	mux := http.NewServeMux()
	dash.RegisterCostRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/observability/cost/nonexistent", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", rec.Code)
	}

	var errResp map[string]string
	json.NewDecoder(rec.Body).Decode(&errResp)
	if errResp["error"] != "session not found" {
		t.Errorf("expected 'session not found' error, got: %v", errResp)
	}
}

func TestCostDashboard_HTTP_MethodNotAllowed(t *testing.T) {
	dash := NewCostDashboard()

	mux := http.NewServeMux()
	dash.RegisterCostRoutes(mux)

	// POST /api/v1/observability/cost
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v1/observability/cost", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status 405 for POST, got %d", rec.Code)
	}
}

func TestCostDashboard_HTML_DashboardEndpoint(t *testing.T) {
	dash := NewCostDashboard()
	dash.RegisterSession("html-sess").RecordCall("gpt-4o", 500, 250)

	mux := http.NewServeMux()
	dash.RegisterCostRoutes(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v1/observability/dashboard", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("expected text/html content-type, got: %s", ct)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "html-sess") {
		t.Error("expected session ID in HTML dashboard")
	}
	if !strings.Contains(body, "</html>") {
		t.Error("expected valid HTML closing tag")
	}
}

func TestCostDashboard_SessionSummary_JSONTags(t *testing.T) {
	dash := NewCostDashboard()
	dash.RegisterSession("json-test").RecordCall("gpt-4o", 800, 400)

	sessions := dash.GetAllSessions()
	if len(sessions) == 0 {
		t.Fatal("expected at least one session")
	}

	data, err := json.Marshal(sessions[0])
	if err != nil {
		t.Fatalf("failed to marshal SessionSummary: %v", err)
	}

	// Verify JSON tags are correct.
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if m["session_id"] != "json-test" {
		t.Errorf("expected session_id=json-test, got: %v", m["session_id"])
	}
	if _, ok := m["total_cost_usd"]; !ok {
		t.Error("expected total_cost_usd key in JSON")
	}
	if _, ok := m["call_count"]; !ok {
		t.Error("expected call_count key in JSON")
	}
}

func TestCostDashboard_CostReport_JSONTags(t *testing.T) {
	dash := NewCostDashboard()
	dash.RegisterSession("report-test").RecordCall("gpt-4o", 300, 150)

	report, found := dash.GetSessionCost("report-test")
	if !found {
		t.Fatal("expected session found")
	}

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("failed to marshal CostReport: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if m["session_id"] != "report-test" {
		t.Errorf("expected session_id=report-test, got: %v", m["session_id"])
	}
	if _, ok := m["entries"]; !ok {
		t.Error("expected entries key in JSON")
	}
}

func TestCostDashboard_LastActivityTimestamp(t *testing.T) {
	dash := NewCostDashboard()

	tracker := dash.RegisterSession("ts-session")
	tracker.RecordCall("gpt-4o", 100, 50)

	sessions := dash.GetAllSessions()
	if len(sessions) != 1 {
		t.Fatal("expected 1 session")
	}

	lastActivityStr := sessions[0].LastActivity
	lastActivity, err := time.Parse(time.RFC3339Nano, lastActivityStr)
	if err != nil {
		t.Fatalf("failed to parse LastActivity timestamp: %v", err)
	}

	// Allow a 1-second tolerance for clock skew.
	if time.Since(lastActivity) > time.Second {
		t.Errorf("LastActivity %v is more than 1 second ago", lastActivity)
	}
}

func TestCostDashboard_EmptySessionReport(t *testing.T) {
	dash := NewCostDashboard()

	// Register a session but don't record any calls.
	dash.RegisterSession("empty-session")

	report, found := dash.GetSessionCost("empty-session")
	if !found {
		t.Fatal("expected session found")
	}

	if report.CallCount != 0 {
		t.Errorf("expected 0 calls, got: %d", report.CallCount)
	}
	if report.TotalCost != 0 {
		t.Errorf("expected 0 cost, got: %.4f", report.TotalCost)
	}
	// LastActivity should still be set (to time.Now()) even for empty sessions.
	if report.LastActivity.IsZero() {
		t.Error("expected non-zero LastActivity for empty session")
	}
}
