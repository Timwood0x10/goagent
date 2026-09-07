// nolint: errcheck // Test code may ignore return values
package observability

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// newTestMetrics creates PrometheusMetrics with an isolated registry to avoid
// global state pollution between tests.
func newTestMetrics(t *testing.T) (*PrometheusMetrics, http.Handler) {
	t.Helper()

	reg := prometheus.NewRegistry()
	m := &PrometheusMetrics{
		LLMCallsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "ARES_llm_calls_total",
				Help: "Total number of LLM calls",
			},
			[]string{"model", "status"},
		),
		ToolCallsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "ARES_tool_calls_total",
				Help: "Total number of tool calls",
			},
			[]string{"tool", "status"},
		),
		AgentErrorsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "ARES_agent_errors_total",
				Help: "Total number of agent errors",
			},
			[]string{"agent", "phase"},
		),
		LLMCallDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "ARES_llm_call_duration_seconds",
				Help:    "LLM call duration in seconds",
				Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10},
			},
			[]string{"model"},
		),
		AgentStepDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "ARES_agent_step_duration_seconds",
				Help:    "Agent step duration in seconds",
				Buckets: []float64{0.1, 0.5, 1, 2.5, 5, 10, 30, 60},
			},
			[]string{"phase"},
		),
		ActiveAgents: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "ARES_active_agents",
				Help: "Number of currently active agents",
			},
		),
		LLMTokensTotal: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "ARES_llm_tokens_total",
				Help: "Total LLM tokens used",
			},
			[]string{"model", "direction"},
		),
		CostUSDTotal: prometheus.NewSummaryVec(
			prometheus.SummaryOpts{
				Name:       "ARES_cost_usd_total",
				Help:       "Total cost in USD",
				Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
			},
			// model only (#cardinality), mirrors NewPrometheusMetrics.
			[]string{"model"},
		),
		EvolutionGateSkippedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "ARES_evolution_gate_skipped_total",
				Help: "Total number of verify gates deliberately NOT registered, by gate and reason",
			},
			[]string{"gate", "reason"},
		),
		EvolutionActiveDuration: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "ARES_evolution_active_duration_seconds",
				Help: "How long the currently active strategy has been promoted, in seconds",
			},
		),
		EvolutionWindowSamples: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "ARES_evolution_window_samples",
				Help: "Runtime fitness evidence window sample count, by strategy and source",
			},
			[]string{"strategy_id", "source"},
		),
	}

	collectors := []prometheus.Collector{
		m.LLMCallsTotal,
		m.ToolCallsTotal,
		m.AgentErrorsTotal,
		m.LLMCallDuration,
		m.AgentStepDuration,
		m.ActiveAgents,
		m.LLMTokensTotal,
		m.CostUSDTotal,
		m.EvolutionGateSkippedTotal,
		m.EvolutionActiveDuration,
		m.EvolutionWindowSamples,
	}
	for _, c := range collectors {
		if err := reg.Register(c); err != nil {
			t.Fatalf("failed to register collector: %v", err)
		}
	}

	handler := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	return m, handler
}

// collectMetrics returns the Prometheus output string from the given handler.
func collectMetrics(t *testing.T, handler http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	return rec.Body.String()
}

func TestNewPrometheusMetrics(t *testing.T) {
	m, err := NewPrometheusMetrics()
	if err != nil {
		t.Fatalf("expected no error creating Prometheus metrics, got: %v", err)
	}
	if m == nil {
		t.Fatal("expected non-nil metrics")
	}

	if m.LLMCallsTotal == nil {
		t.Error("expected LLMCallsTotal to be initialized")
	}
	if m.ToolCallsTotal == nil {
		t.Error("expected ToolCallsTotal to be initialized")
	}
	if m.AgentErrorsTotal == nil {
		t.Error("expected AgentErrorsTotal to be initialized")
	}
	if m.LLMCallDuration == nil {
		t.Error("expected LLMCallDuration to be initialized")
	}
	if m.AgentStepDuration == nil {
		t.Error("expected AgentStepDuration to be initialized")
	}
	if m.ActiveAgents == nil {
		t.Error("expected ActiveAgents gauge to be initialized")
	}
	if m.LLMTokensTotal == nil {
		t.Error("expected LLMTokensTotal to be initialized")
	}
	if m.CostUSDTotal == nil {
		t.Error("expected CostUSDTotal to be initialized")
	}
	if m.EvolutionGateSkippedTotal == nil {
		t.Error("expected EvolutionGateSkippedTotal to be initialized")
	}
	if m.EvolutionActiveDuration == nil {
		t.Error("expected EvolutionActiveDuration gauge to be initialized")
	}
	if m.EvolutionWindowSamples == nil {
		t.Error("expected EvolutionWindowSamples to be initialized")
	}
}

func TestNewPrometheusMetrics_Idempotent(t *testing.T) {
	_, err1 := NewPrometheusMetrics()
	_, err2 := NewPrometheusMetrics()

	if err1 != nil {
		t.Errorf("first creation should succeed, got: %v", err1)
	}
	if err2 != nil {
		t.Errorf("second creation should handle AlreadyRegisteredError, got: %v", err2)
	}
}

func TestPrometheusMetrics_RecordLLMCall(t *testing.T) {
	m, handler := newTestMetrics(t)

	m.RecordLLMCall("gpt-4o", "success", 0.5)
	m.RecordLLMCall("gpt-4o", "error", 2.0)

	body := collectMetrics(t, handler)

	if !strings.Contains(body, `ARES_llm_calls_total{model="gpt-4o",status="success"} 1`) {
		t.Errorf("expected LLM calls counter for gpt-4o success in output:\n%s", body)
	}
	if !strings.Contains(body, `ARES_llm_calls_total{model="gpt-4o",status="error"} 1`) {
		t.Errorf("expected LLM calls counter for gpt-4o error in output:\n%s", body)
	}
	if !strings.Contains(body, "ARES_llm_call_duration_seconds") {
		t.Error("expected LLM call duration histogram in output")
	}
}

func TestPrometheusMetrics_RecordToolCall(t *testing.T) {
	m, handler := newTestMetrics(t)

	m.RecordToolCall("search_api", "success")
	m.RecordToolCall("weather_api", "error")

	body := collectMetrics(t, handler)

	if !strings.Contains(body, `ARES_tool_calls_total{status="success",tool="search_api"} 1`) {
		t.Errorf("expected tool call counter in output:\n%s", body)
	}
	if !strings.Contains(body, `ARES_tool_calls_total{status="error",tool="weather_api"} 1`) {
		t.Errorf("expected tool call error counter in output:\n%s", body)
	}
}

func TestPrometheusMetrics_RecordAgentError(t *testing.T) {
	m, handler := newTestMetrics(t)

	m.RecordAgentError("leader-1", "planning")
	m.RecordAgentError("sub-agent-1", "execution")

	body := collectMetrics(t, handler)

	if !strings.Contains(body, `ARES_agent_errors_total{agent="leader-1",phase="planning"} 1`) {
		t.Errorf("expected agent error counter in output:\n%s", body)
	}
	if !strings.Contains(body, `ARES_agent_errors_total{agent="sub-agent-1",phase="execution"} 1`) {
		t.Errorf("expected sub-agent error counter in output:\n%s", body)
	}
}

func TestPrometheusMetrics_RecordAgentStepDuration(t *testing.T) {
	m, handler := newTestMetrics(t)

	m.RecordAgentStepDuration("planning", 0.25)
	m.RecordAgentStepDuration("execution", 5.5)

	body := collectMetrics(t, handler)

	if !strings.Contains(body, "ARES_agent_step_duration_seconds") {
		t.Error("expected step duration histogram in output")
	}
	if !strings.Contains(body, `phase="planning"`) {
		t.Error("expected planning phase label in output")
	}
	if !strings.Contains(body, `phase="execution"`) {
		t.Error("expected execution phase label in output")
	}
}

func TestPrometheusMetrics_ActiveAgentsGauge(t *testing.T) {
	m, handler := newTestMetrics(t)

	m.SetActiveAgents(3)
	m.IncActiveAgents()
	m.IncActiveAgents()
	m.DecActiveAgents()

	body := collectMetrics(t, handler)

	if !strings.Contains(body, "ARES_active_agents 4") {
		t.Errorf("expected active_agents=4 after set(3)+inc+inc-dec, got:\n%s", body)
	}
}

func TestPrometheusMetrics_RecordLLMTokens(t *testing.T) {
	m, handler := newTestMetrics(t)

	m.RecordLLMTokens("gpt-4o", "input", 5000)
	m.RecordLLMTokens("gpt-4o", "output", 2000)

	body := collectMetrics(t, handler)

	if !strings.Contains(body, `ARES_llm_tokens_total{direction="input",model="gpt-4o"} 5000`) {
		t.Errorf("expected input token gauge in output:\n%s", body)
	}
	if !strings.Contains(body, `ARES_llm_tokens_total{direction="output",model="gpt-4o"} 2000`) {
		t.Errorf("expected output token gauge in output:\n%s", body)
	}
}

func TestPrometheusMetrics_RecordCost(t *testing.T) {
	m, handler := newTestMetrics(t)

	m.RecordCost("gpt-4o", "session-abc", 0.075)
	m.RecordCost("claude-3.5-sonnet", "session-def", 0.15)

	body := collectMetrics(t, handler)

	if !strings.Contains(body, "ARES_cost_usd_total") {
		t.Error("expected cost summary in output")
	}
	if !strings.Contains(body, `model="gpt-4o"`) {
		t.Error("expected model label in cost summary")
	}
	// Session is intentionally NOT a label (#cardinality): unbounded session
	// IDs would grow the Prometheus registry forever. Per-session cost detail
	// lives in CostTracker (cost.go).
	if strings.Contains(body, `session="`) {
		t.Error("expected no session label in cost summary")
	}
}

func TestPrometheusMetrics_NilSafe(t *testing.T) {
	var m *PrometheusMetrics

	m.RecordLLMCall("test", "success", 1.0)
	m.RecordToolCall("tool", "success")
	m.RecordAgentError("agent", "phase")
	m.RecordAgentStepDuration("phase", 1.0)
	m.SetActiveAgents(5)
	m.IncActiveAgents()
	m.DecActiveAgents()
	m.RecordLLMTokens("model", "input", 100)
	m.RecordCost("model", "session", 0.01)
}

func TestMetricsHTTPHandler(t *testing.T) {
	handler := MetricsHTTPHandler()
	if handler == nil {
		t.Fatal("expected non-nil handler")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("expected text/plain content type, got: %s", ct)
	}

	body := rec.Body.String()
	if len(body) == 0 {
		t.Error("expected non-empty metrics output")
	}
}

func TestRegisterMetricsRouter(t *testing.T) {
	mux := http.NewServeMux()
	RegisterMetricsRouter(mux)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200 for /metrics, got %d", rec.Code)
	}

	recPost := httptest.NewRecorder()
	reqPost := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/metrics", nil)
	mux.ServeHTTP(recPost, reqPost)

	if recPost.Code != http.StatusMethodNotAllowed && recPost.Code != http.StatusNotFound {
		t.Logf("POST /metrics returned status %d (may vary by Go version)", recPost.Code)
	}
}

func TestMetricsHTTPHandler_ContentType(t *testing.T) {
	_, _ = NewPrometheusMetrics()
	handler := MetricsHTTPHandler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil)
	handler.ServeHTTP(rec, req)

	bodyBytes, _ := io.ReadAll(rec.Body)
	body := string(bodyBytes)

	if !strings.Contains(body, "# HELP") {
		t.Error("expected # HELP comments in Prometheus output")
	}
	if !strings.Contains(body, "# TYPE") {
		t.Error("expected # TYPE comments in Prometheus output")
	}
}

func TestPrometheusMetrics_EvolutionGateSkipped(t *testing.T) {
	m, handler := newTestMetrics(t)

	m.RecordEvolutionGateSkipped("shadow", "no_configured_evaluator")
	m.RecordEvolutionGateSkipped("shadow", "no_configured_evaluator")

	body := collectMetrics(t, handler)

	if !strings.Contains(body, `ARES_evolution_gate_skipped_total{gate="shadow",reason="no_configured_evaluator"} 2`) {
		t.Errorf("expected gate-skipped counter to increment to 2, got:\n%s", body)
	}
}

func TestPrometheusMetrics_EvolutionActiveDuration(t *testing.T) {
	m, handler := newTestMetrics(t)

	m.SetEvolutionActiveDuration(120.5)

	body := collectMetrics(t, handler)

	if !strings.Contains(body, "ARES_evolution_active_duration_seconds") {
		t.Error("expected active-duration gauge in output")
	}
	if !strings.Contains(body, "ARES_evolution_active_duration_seconds 120.5") {
		t.Errorf("expected active-duration to be 120.5, got:\n%s", body)
	}
}

func TestPrometheusMetrics_EvolutionWindowSamples(t *testing.T) {
	m, handler := newTestMetrics(t)

	m.SetEvolutionWindowSamples("strategy-a", "strategy", 42)
	m.SetEvolutionWindowSamples("strategy-a", "strategy", 99)

	body := collectMetrics(t, handler)

	if !strings.Contains(body, `ARES_evolution_window_samples{source="strategy",strategy_id="strategy-a"} 99`) {
		t.Errorf("expected window-samples gauge to reflect latest set (99), got:\n%s", body)
	}
}

func TestPrometheusMetrics_MultipleModels(t *testing.T) {
	m, handler := newTestMetrics(t)

	models := []string{"gpt-4o", "gpt-4o-mini", "claude-3.5-sonnet"}
	for _, model := range models {
		m.RecordLLMCall(model, "success", float64(len(model))*0.1)
	}

	body := collectMetrics(t, handler)

	for _, model := range models {
		label := `model="` + model + `"`
		if !strings.Contains(body, label) {
			t.Errorf("expected model %s in output:\n%s", model, body)
		}
	}
}

func TestPrometheusMetrics_CounterIncrementMultiple(t *testing.T) {
	m, handler := newTestMetrics(t)

	m.RecordLLMCall("gpt-4o", "success", 0.1)
	m.RecordLLMCall("gpt-4o", "success", 0.2)
	m.RecordLLMCall("gpt-4o", "success", 0.3)

	body := collectMetrics(t, handler)

	if !strings.Contains(body, `ARES_llm_calls_total{model="gpt-4o",status="success"} 3`) {
		t.Errorf("expected counter value 3 after 3 increments, got:\n%s", body)
	}
}

func TestPrometheusMetrics_HistogramBuckets(t *testing.T) {
	m, handler := newTestMetrics(t)

	m.RecordLLMCall("test-model", "success", 0.3)
	m.RecordLLMCall("test-model", "success", 3.0)

	body := collectMetrics(t, handler)

	// Verify histogram bucket structure exists with expected labels.
	expectedBuckets := []string{
		`le="0.1"`,
		`le="0.25"`,
		`le="0.5"`,
		`le="1"`,
		`le="2.5"`,
		`le="5"`,
		`le="10"`,
		`le="+Inf"`,
	}
	for _, bucket := range expectedBuckets {
		if !strings.Contains(body, bucket) {
			t.Errorf("expected bucket %s in histogram output:\n%s", bucket, body)
		}
	}
}
