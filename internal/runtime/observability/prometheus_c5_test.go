package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// newTestMetricsWithCompile creates a PrometheusMetrics instance with the
// compile gauges registered on a private registry for test isolation.
func newTestMetricsWithCompile(t *testing.T) (*PrometheusMetrics, http.Handler) {
	t.Helper()
	reg := prometheus.NewRegistry()
	m := &PrometheusMetrics{
		EvolutionGeneration: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "ARES_evolution_generation",
				Help: "Current evolution generation number",
			},
		),
		EvolutionDAGVersion: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "ARES_evolution_dag_version",
				Help: "Current live MutableDAG version (mutation counter)",
			},
		),
		EvolutionCompileCount: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "ARES_evolution_compile_count",
				Help: "Total number of CompilePlan calls since startup",
			},
		),
	}
	collectors := []prometheus.Collector{
		m.EvolutionGeneration,
		m.EvolutionDAGVersion,
		m.EvolutionCompileCount,
	}
	for _, c := range collectors {
		if err := reg.Register(c); err != nil {
			t.Fatalf("failed to register collector: %v", err)
		}
	}
	handler := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	return m, handler
}

func TestPrometheusMetrics_EvolutionGeneration(t *testing.T) {
	m, handler := newTestMetricsWithCompile(t)

	m.SetEvolutionGeneration(5)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil)
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "ARES_evolution_generation 5") {
		t.Errorf("expected generation gauge to be 5, got:\n%s", body)
	}
}

func TestPrometheusMetrics_EvolutionDAGVersion(t *testing.T) {
	m, handler := newTestMetricsWithCompile(t)

	m.SetEvolutionDAGVersion(42)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil)
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "ARES_evolution_dag_version 42") {
		t.Errorf("expected DAG version gauge to be 42, got:\n%s", body)
	}
}

func TestPrometheusMetrics_EvolutionCompileCount(t *testing.T) {
	m, handler := newTestMetricsWithCompile(t)

	m.SetEvolutionCompileCount(7)

	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", nil)
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "ARES_evolution_compile_count 7") {
		t.Errorf("expected compile count gauge to be 7, got:\n%s", body)
	}
}

// TestPrometheusMetrics_NilSafeC5 verifies the compile metric setters are
// nil-safe (matching the existing convention for all metrics methods).
func TestPrometheusMetrics_NilSafeC5(t *testing.T) {
	var m *PrometheusMetrics // nil

	// None of these should panic.
	m.SetEvolutionGeneration(1)
	m.SetEvolutionDAGVersion(1)
	m.SetEvolutionCompileCount(1)
}
