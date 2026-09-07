package observability

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DefaultPricingConfig returns a PricingConfig with common model pricing.
// Prices are per 1K tokens (USD).
func DefaultPricingConfig() PricingConfig {
	return PricingConfig{
		Models: map[string]ModelPricing{
			"gpt-4o": {
				InputCostPer1K:  0.0025,
				OutputCostPer1K: 0.01,
			},
			"gpt-4o-mini": {
				InputCostPer1K:  0.00015,
				OutputCostPer1K: 0.0006,
			},
			"gpt-3.5-turbo": {
				InputCostPer1K:  0.0005,
				OutputCostPer1K: 0.0015,
			},
			"claude-3.5-sonnet": {
				InputCostPer1K:  0.003,
				OutputCostPer1K: 0.015,
			},
		},
	}
}

// CostTracker accumulates LLM usage costs across an agent session.
type CostTracker struct {
	mu          sync.RWMutex
	pricing     PricingConfig
	entries     []CostEntry
	totalInput  int
	totalOutput int
	totalCost   float64
	createdAt   time.Time
}

// Ensure *CostTracker satisfies expected interface at compile time.

// CostEntry represents a single LLM call's cost information.
type CostEntry struct {
	Timestamp    time.Time
	Model        string
	InputTokens  int
	OutputTokens int
	Cost         float64
}

// PricingConfig maps model names to their per-token pricing.
type PricingConfig struct {
	Models map[string]ModelPricing
}

// ModelPricing holds per-1K-token costs for a model.
type ModelPricing struct {
	InputCostPer1K  float64
	OutputCostPer1K float64
}

// NewCostTracker creates a new CostTracker with the given pricing configuration.
//
// Args:
//   - pricing: the pricing configuration for cost calculation.
//
// Returns:
//   - *CostTracker: initialized cost tracker.
func NewCostTracker(pricing PricingConfig) *CostTracker {
	return &CostTracker{
		pricing:   pricing,
		entries:   make([]CostEntry, 0),
		createdAt: time.Now(),
	}
}

// CreatedAt returns the timestamp when this tracker was created.
func (t *CostTracker) CreatedAt() time.Time {
	return t.createdAt
}

// RecordCall records an LLM call and calculates its cost based on the
// configured pricing for the given model.
//
// Args:
//   - model: the LLM model name used for the call.
//   - inputTokens: number of input tokens consumed.
//   - outputTokens: number of output tokens generated.
func (t *CostTracker) RecordCall(model string, inputTokens, outputTokens int) {
	if t == nil {
		return
	}

	pricing, ok := t.pricing.Models[model]
	if !ok {
		log.Warn("cost: unknown model, skipping cost tracking",
			"model", model,
		)
		return
	}

	inputCost := float64(inputTokens) / 1000.0 * pricing.InputCostPer1K
	outputCost := float64(outputTokens) / 1000.0 * pricing.OutputCostPer1K
	totalCallCost := inputCost + outputCost

	entry := CostEntry{
		Timestamp:    time.Now(),
		Model:        model,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
		Cost:         totalCallCost,
	}

	t.mu.Lock()
	t.entries = append(t.entries, entry)
	// Ring cap on the itemized list (#unbounded): totals above remain exact
	// for the session lifetime; only per-call detail is forgotten.
	if len(t.entries) > maxEntriesPerTracker {
		t.entries = t.entries[len(t.entries)-maxEntriesPerTracker:]
	}
	t.totalInput += inputTokens
	t.totalOutput += outputTokens
	t.totalCost += totalCallCost
	t.mu.Unlock()
}

// maxEntriesPerTracker caps CostTracker's itemized call list.
const maxEntriesPerTracker = 1000

// TotalCost returns the accumulated total cost in USD.
//
// Returns:
//   - float64: the total cost across all recorded calls.
func (t *CostTracker) TotalCost() float64 {
	if t == nil {
		return 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.totalCost
}

// TotalTokens returns the accumulated token counts.
//
// Returns:
//   - int: total input tokens consumed.
//   - int: total output tokens generated.
func (t *CostTracker) TotalTokens() (input, output int) {
	if t == nil {
		return 0, 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.totalInput, t.totalOutput
}

// Entries returns a copy of all recorded cost entries.
//
// Returns:
//   - []CostEntry: copy of all cost entries.
func (t *CostTracker) Entries() []CostEntry {
	if t == nil {
		return nil
	}
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]CostEntry, len(t.entries))
	copy(result, t.entries)
	return result
}

// Report generates a Markdown summary table of all costs.
//
// Returns:
//   - string: Markdown formatted cost report.
func (t *CostTracker) Report() string {
	if t == nil {
		return "No cost data available.\n"
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	var b strings.Builder

	b.WriteString("## Cost Summary\n\n")
	b.WriteString("| Metric | Value |\n")
	b.WriteString("|--------|-------|\n")
	fmt.Fprintf(&b, "| Total Cost | $%.4f |\n", t.totalCost)
	fmt.Fprintf(&b, "| Total Input Tokens | %d |\n", t.totalInput)
	fmt.Fprintf(&b, "| Total Output Tokens | %d |\n", t.totalOutput)
	fmt.Fprintf(&b, "| Total Calls | %d |\n", len(t.entries))
	b.WriteString("\n### Call Details\n\n")

	if len(t.entries) == 0 {
		b.WriteString("No calls recorded.\n")
		return b.String()
	}

	b.WriteString("| Time | Model | Input | Output | Cost |\n")
	b.WriteString("|------|-------|-------|--------|------|\n")

	for _, e := range t.entries {
		fmt.Fprintf(&b,
			"| %s | %s | %d | %d | $%.4f |\n",
			e.Timestamp.Format(time.RFC3339),
			e.Model,
			e.InputTokens,
			e.OutputTokens,
			e.Cost,
		)
	}

	return b.String()
}

// Reset clears all recorded data and resets counters to zero.
func (t *CostTracker) Reset() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.entries = t.entries[:0]
	t.totalInput = 0
	t.totalOutput = 0
	t.totalCost = 0
}

// ── Cost Dashboard: session-level aggregation ──────────────────────

// CostReport provides a detailed cost breakdown for a single session.
type CostReport struct {
	SessionID    string      `json:"session_id"`
	TotalCost    float64     `json:"total_cost_usd"`
	TotalInput   int         `json:"total_input_tokens"`
	TotalOutput  int         `json:"total_output_tokens"`
	CallCount    int         `json:"call_count"`
	Entries      []CostEntry `json:"entries"`
	CreatedAt    time.Time   `json:"created_at"`
	LastActivity time.Time   `json:"last_activity"`
}

// SessionSummary provides a high-level summary for a single session.
type SessionSummary struct {
	SessionID    string  `json:"session_id"`
	TotalCost    float64 `json:"total_cost_usd"`
	CallCount    int     `json:"call_count"`
	TotalInput   int     `json:"total_input_tokens"`
	TotalOutput  int     `json:"total_output_tokens"`
	LastActivity string  `json:"last_activity"`
}

// AllSessionsResponse is the JSON response for listing all sessions.
type AllSessionsResponse struct {
	Sessions       []SessionSummary `json:"sessions"`
	TotalSessions  int              `json:"total_sessions"`
	GrandTotalCost float64          `json:"grand_total_cost_usd"`
}

// CostDashboard aggregates multiple CostTracker instances by session ID
// and exposes cost data via HTTP handlers.
type CostDashboard struct {
	mu       sync.RWMutex
	pricing  PricingConfig
	sessions map[string]*CostTracker
	order    []string // preserves insertion order for listing
}

// NewCostDashboard creates a new CostDashboard with default pricing.
//
// Returns:
//   - *CostDashboard: initialized dashboard.
func NewCostDashboard() *CostDashboard {
	return &CostDashboard{
		pricing:  DefaultPricingConfig(),
		sessions: make(map[string]*CostTracker),
		order:    make([]string, 0),
	}
}

// NewCostDashboardWithPricing creates a new CostDashboard with custom pricing.
//
// Args:
//   - pricing: the pricing configuration.
//
// Returns:
//   - *CostDashboard: initialized dashboard.
func NewCostDashboardWithPricing(pricing PricingConfig) *CostDashboard {
	return &CostDashboard{
		pricing:  pricing,
		sessions: make(map[string]*CostTracker),
		order:    make([]string, 0),
	}
}

// RegisterSession creates or returns an existing CostTracker for the given
// session ID. If the session already exists, the existing tracker is returned.
//
// Args:
//   - sessionID: unique identifier for the session.
//
// Returns:
//   - *CostTracker: the cost tracker for this session.
func (d *CostDashboard) RegisterSession(sessionID string) *CostTracker {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	if tracker, ok := d.sessions[sessionID]; ok {
		return tracker
	}

	tracker := NewCostTracker(d.pricing)
	d.sessions[sessionID] = tracker
	d.order = append(d.order, sessionID)
	d.evictOldestLocked()
	return tracker
}

// maxDashboardSessions caps CostDashboard's session map: long-running
// processes registered one tracker per session forever (#unbounded). The
// oldest-registered session is evicted once the cap is hit.
const maxDashboardSessions = 1000

// evictOldestLocked drops the oldest sessions beyond maxDashboardSessions
// (caller holds d.mu for writing).
func (d *CostDashboard) evictOldestLocked() {
	for len(d.order) > maxDashboardSessions {
		oldest := d.order[0]
		d.order = d.order[1:]
		delete(d.sessions, oldest)
	}
}

// GetSessionCost returns a detailed cost report for the given session ID.
//
// Args:
//   - sessionID: the session identifier to look up.
//
// Returns:
//   - CostReport: detailed report, or zero-value if session not found.
//   - bool: true if the session was found.
func (d *CostDashboard) GetSessionCost(sessionID string) (CostReport, bool) {
	if d == nil {
		return CostReport{}, false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()

	tracker, ok := d.sessions[sessionID]
	if !ok {
		return CostReport{}, false
	}

	entries := tracker.Entries()
	input, output := tracker.TotalTokens()

	var lastActivity time.Time
	if len(entries) > 0 {
		lastActivity = entries[len(entries)-1].Timestamp
	} else {
		lastActivity = time.Now()
	}

	return CostReport{
		SessionID:    sessionID,
		TotalCost:    tracker.TotalCost(),
		TotalInput:   input,
		TotalOutput:  output,
		CallCount:    len(entries),
		Entries:      entries,
		CreatedAt:    tracker.CreatedAt(),
		LastActivity: lastActivity,
	}, true
}

// GetAllSessions returns summaries for all registered sessions in insertion order.
//
// Returns:
//   - []SessionSummary: slice of session summaries.
func (d *CostDashboard) GetAllSessions() []SessionSummary {
	if d == nil {
		return nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()

	summaries := make([]SessionSummary, 0, len(d.order))
	for _, sid := range d.order {
		tracker := d.sessions[sid]
		input, output := tracker.TotalTokens()
		entries := tracker.Entries()

		var lastActivity time.Time
		if len(entries) > 0 {
			lastActivity = entries[len(entries)-1].Timestamp
		} else {
			lastActivity = time.Now()
		}

		summaries = append(summaries, SessionSummary{
			SessionID:    sid,
			TotalCost:    tracker.TotalCost(),
			CallCount:    len(entries),
			TotalInput:   input,
			TotalOutput:  output,
			LastActivity: lastActivity.Format(time.RFC3339),
		})
	}
	return summaries
}

// GenerateDashboardHTML returns an HTML table showing per-session costs.
// This is a simple self-contained HTML page suitable for embedding or
// direct browser viewing.
//
// Returns:
//   - string: HTML content with cost table.
func (d *CostDashboard) GenerateDashboardHTML() string {
	if d == nil {
		return "<html><body><p>No dashboard available.</p></body></html>"
	}

	sessions := d.GetAllSessions()

	var grandTotal float64
	for _, s := range sessions {
		grandTotal += s.TotalCost
	}

	var b strings.Builder
	b.WriteString(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>ARES Cost Dashboard</title>
<style>
body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; margin: 2rem; background: #f8f9fa; }
h1 { color: #1a1a2e; }
table { border-collapse: collapse; width: 100%; max-width: 900px; background: white; box-shadow: 0 1px 3px rgba(0,0,0,0.1); }
th, td { padding: 0.75rem 1rem; text-align: left; border-bottom: 1px solid #e0e0e0; }
th { background: #1a1a2e; color: white; }
tr:hover { background: #f5f5f5; }
.grand-total { font-weight: bold; background: #e8f4f8; }
.footer { margin-top: 1rem; color: #666; font-size: 0.875rem; }
</style>
</head>
<body>
<h1>ARES Cost Dashboard</h1>
<table>
<thead>
<tr><th>Session ID</th><th>Total Cost (USD)</th><th>Calls</th><th>Input Tokens</th><th>Output Tokens</th><th>Last Activity</th></tr>
</thead>
<tbody>
`)

	for _, s := range sessions {
		fmt.Fprintf(&b,
			`<tr><td>%s</td><td>$%.4f</td><td>%d</td><td>%d</td><td>%d</td><td>%s</td></tr>`+"\n",
			s.SessionID, s.TotalCost, s.CallCount, s.TotalInput, s.TotalOutput, s.LastActivity,
		)
	}

	fmt.Fprintf(&b,
		`<tr class="grand-total"><td>GRAND TOTAL</td><td>$%.4f</td><td>%d</td><td colspan="3"></td></tr>
</tbody>
</table>
<p class="footer">Generated at %s | %d sessions</p>
</body>
</html>`,
		grandTotal, len(sessions), time.Now().Format(time.RFC3339), len(sessions),
	)

	return b.String()
}

// handleAllCosts handles GET /api/v1/observability/cost and returns JSON
// of all session costs.
func (d *CostDashboard) handleAllCosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	sessions := d.GetAllSessions()
	grandTotal := 0.0
	for _, s := range sessions {
		grandTotal += s.TotalCost
	}

	resp := AllSessionsResponse{
		Sessions:       sessions,
		TotalSessions:  len(sessions),
		GrandTotalCost: grandTotal,
	}
	writeJSONCost(w, http.StatusOK, resp)
}

// handleSessionCost handles GET /api/v1/observability/cost/{sessionid}
// and returns detailed cost for a specific session.
func (d *CostDashboard) handleSessionCost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Extract session ID from path: /api/v1/observability/cost/{sessionid}
	sessionID := strings.TrimPrefix(r.URL.Path, "/api/v1/observability/cost/")
	if sessionID == "" || sessionID == r.URL.Path {
		writeJSONError(w, http.StatusBadRequest, "session id required")
		return
	}

	report, found := d.GetSessionCost(sessionID)
	if !found {
		writeJSONError(w, http.StatusNotFound, "session not found")
		return
	}

	writeJSONCost(w, http.StatusOK, report)
}

// handleCostDashboardHTML serves the cost dashboard as an HTML page.
func (d *CostDashboard) handleCostDashboardHTML(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, d.GenerateDashboardHTML())
}

// RegisterCostRoutes registers all cost-related HTTP endpoints on the given mux.
//
// Args:
//   - mux: the ServeMux to register endpoints on.
func (d *CostDashboard) RegisterCostRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/observability/cost", d.handleAllCosts)
	mux.HandleFunc("GET /api/v1/observability/cost/", d.handleSessionCost)
	mux.HandleFunc("GET /api/v1/observability/dashboard", d.handleCostDashboardHTML)
}

// ── HTTP helpers ────────────────────────────────────────────────────

// writeJSONCost writes a JSON response for cost API endpoints.
func writeJSONCost(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Warn("cost: encode response", "error", err)
	}
}

// writeJSONError writes a JSON error response.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSONCost(w, status, map[string]string{"error": msg})
}
