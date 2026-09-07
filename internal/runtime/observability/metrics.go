package observability

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metrics holds OpenTelemetry counters and histograms for MCP/Agent operations.
type Metrics struct {
	// Counters
	llmCallsTotal    metric.Int64Counter
	toolCallsTotal   metric.Int64Counter
	agentErrorsTotal metric.Int64Counter

	// Histograms
	llmCallDuration   metric.Float64Histogram
	agentStepDuration metric.Float64Histogram
	toolCallDuration  metric.Float64Histogram
}

// NewMetrics creates all metrics with proper names, descriptions, and buckets.
//
// Args:
//   - meter: the OpenTelemetry meter used to create instruments.
//
// Returns:
//   - *Metrics: initialized metrics instance.
//   - error: non-nil if any instrument creation fails.
func NewMetrics(meter metric.Meter) (*Metrics, error) {
	llmCallsTotal, err := meter.Int64Counter(
		"ares_llm_calls_total",
		metric.WithDescription("Total number of LLM calls"),
	)
	if err != nil {
		return nil, fmt.Errorf("create llm_calls_total counter: %w", err)
	}

	toolCallsTotal, err := meter.Int64Counter(
		"ares_tool_calls_total",
		metric.WithDescription("Total number of tool calls"),
	)
	if err != nil {
		return nil, fmt.Errorf("create tool_calls_total counter: %w", err)
	}

	agentErrorsTotal, err := meter.Int64Counter(
		"ares_agent_errors_total",
		metric.WithDescription("Total number of agent errors"),
	)
	if err != nil {
		return nil, fmt.Errorf("create agent_errors_total counter: %w", err)
	}

	llmCallDuration, err := meter.Float64Histogram(
		"ares_llm_call_duration_seconds",
		metric.WithDescription("LLM call duration in seconds"),
		metric.WithExplicitBucketBoundaries(0.1, 0.5, 1, 2, 5, 10, 30, 60),
	)
	if err != nil {
		return nil, fmt.Errorf("create llm_call_duration histogram: %w", err)
	}

	agentStepDuration, err := meter.Float64Histogram(
		"ares_agent_step_duration_seconds",
		metric.WithDescription("Agent step duration in seconds"),
		metric.WithExplicitBucketBoundaries(0.1, 0.5, 1, 2, 5, 10, 30, 60),
	)
	if err != nil {
		return nil, fmt.Errorf("create agent_step_duration histogram: %w", err)
	}

	toolCallDuration, err := meter.Float64Histogram(
		"ares_tool_call_duration_seconds",
		metric.WithDescription("Tool call duration in seconds"),
		metric.WithExplicitBucketBoundaries(0.1, 0.5, 1, 2, 5, 10, 30, 60),
	)
	if err != nil {
		return nil, fmt.Errorf("create tool_call_duration histogram: %w", err)
	}

	return &Metrics{
		llmCallsTotal:     llmCallsTotal,
		toolCallsTotal:    toolCallsTotal,
		agentErrorsTotal:  agentErrorsTotal,
		llmCallDuration:   llmCallDuration,
		agentStepDuration: agentStepDuration,
		toolCallDuration:  toolCallDuration,
	}, nil
}

// RecordLLMCall records an LLM call metric with model, duration, and error status.
//
// Args:
//   - ctx: context for metric recording.
//   - model: the LLM model name.
//   - duration: how long the call took.
//   - hasError: whether the call resulted in an error.
func (m *Metrics) RecordLLMCall(ctx context.Context, model string, duration time.Duration, hasError bool) {
	if m == nil {
		return
	}

	attrs := []attribute.KeyValue{
		attribute.String("model", model),
		attribute.Bool("has_error", hasError),
	}

	m.llmCallsTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
	m.llmCallDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attrs...))
}

// RecordToolCall records a tool call metric with tool name, duration, and error status.
//
// Args:
//   - ctx: context for metric recording.
//   - toolName: the name of the tool called.
//   - duration: how long the call took.
//   - hasError: whether the call resulted in an error.
func (m *Metrics) RecordToolCall(ctx context.Context, toolName string, duration time.Duration, hasError bool) {
	if m == nil {
		return
	}

	attrs := []attribute.KeyValue{
		attribute.String("tool_name", toolName),
		attribute.Bool("has_error", hasError),
	}

	m.toolCallsTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
	m.toolCallDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attrs...))
}

// RecordAgentStepDuration records an agent step duration metric.
//
// Args:
//   - ctx: context for metric recording.
//   - agentID: the agent identifier.
//   - duration: how long the step took.
//   - hasError: whether the step resulted in an error.
func (m *Metrics) RecordAgentStepDuration(ctx context.Context, agentID string, duration time.Duration, hasError bool) {
	if m == nil {
		return
	}

	attrs := []attribute.KeyValue{
		attribute.String("agent_id", agentID),
		attribute.Bool("has_error", hasError),
	}

	m.agentStepDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attrs...))
}

// RecordAgentError records an agent error counter increment.
//
// Args:
//   - ctx: context for metric recording.
//   - agentID: the agent identifier.
//   - errorType: the classification of the error.
func (m *Metrics) RecordAgentError(ctx context.Context, agentID, errorType string) {
	if m == nil {
		return
	}

	attrs := []attribute.KeyValue{
		attribute.String("agent_id", agentID),
		attribute.String("error_type", errorType),
	}

	m.agentErrorsTotal.Add(ctx, 1, metric.WithAttributes(attrs...))
}
