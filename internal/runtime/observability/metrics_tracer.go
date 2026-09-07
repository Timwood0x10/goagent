package observability

import "context"

// MetricsTracer is a Tracer adapter that feeds real LLM/tool calls into the
// Prometheus registry and the cost dashboard. Before it existed, the
// default NoopTracer left every ARES_* counter at
// zero — the /metrics endpoint was wired but永远 empty.
//
// Structure: embeds the Noop tracer for the firehose methods the metrics
// registry does not model (agent steps, errors, trace-id plumbing), and
// overrides the two hot paths (LLM calls, tool calls) with real recording.
type MetricsTracer struct {
	Tracer
	metrics   *PrometheusMetrics
	dashboard *CostDashboard
}

// NewMetricsTracer wraps the metrics registry (and optionally the cost
// dashboard) as a Tracer. Both may be nil — nil metrics discards everything
// (equivalent to the Noop tracer), nil dashboard skips cost attribution.
//
// Args:
//   - metrics: the Prometheus registry to record into.
//   - dashboard: the cost dashboard receiving per-session cost entries.
//
// Returns:
//
//	Tracer - the composite tracer.
func NewMetricsTracer(metrics *PrometheusMetrics, dashboard *CostDashboard) Tracer {
	return &MetricsTracer{
		Tracer:    NewNoopTracer(),
		metrics:   metrics,
		dashboard: dashboard,
	}
}

// RecordLLMCall forwards the call into the Prometheus counters and attributes
// token cost to the per-trace session in the dashboard.
func (t *MetricsTracer) RecordLLMCall(ctx context.Context, call *LLMCall) {
	if t.metrics != nil && call != nil {
		status := "success"
		if call.Error != nil {
			status = "error"
		}
		t.metrics.RecordLLMCall(call.Model, status, call.Duration.Seconds())
		// Cost attribution: session = trace id (the only per-call scope LLMCall
		// carries); tokens × pricing when the dashboard is wired.
		if t.dashboard != nil && call.TraceID != "" {
			tracker := t.dashboard.RegisterSession(call.TraceID)
			if tracker != nil {
				tracker.RecordCall(call.Model, call.TokensUsed/2, call.TokensUsed-call.TokensUsed/2)
			}
		}
	}
}

// RecordToolCall forwards the tool call into the Prometheus counters.
func (t *MetricsTracer) RecordToolCall(ctx context.Context, call *ToolCall) {
	if t.metrics != nil && call != nil {
		status := "success"
		if call.Error != nil {
			status = "error"
		}
		t.metrics.RecordToolCall(call.ToolName, status)
	}
}
