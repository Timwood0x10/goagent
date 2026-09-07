package observability

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// OTelTracer implements the Tracer interface using OpenTelemetry SDK.
type OTelTracer struct {
	tracer        trace.Tracer
	meter         metric.Meter
	provider      *sdktrace.TracerProvider
	meterProvider *sdkmetric.MeterProvider
	metrics       *Metrics
}

// Ensure OTelTracer implements Tracer at compile time.
var _ Tracer = (*OTelTracer)(nil)

// otelConfig holds configuration options for OTelTracer.
type otelConfig struct {
	exporter     sdktrace.SpanExporter
	sampler      sdktrace.Sampler
	metricReader sdkmetric.Reader
}

// OTelOption is a functional option for configuring OTelTracer.
type OTelOption func(*otelConfig)

// WithExporter sets a custom span exporter for the tracer.
func WithExporter(exp sdktrace.SpanExporter) OTelOption {
	return func(c *otelConfig) {
		c.exporter = exp
	}
}

// WithSampler sets the sampling strategy for the tracer.
func WithSampler(sampler sdktrace.Sampler) OTelOption {
	return func(c *otelConfig) {
		c.sampler = sampler
	}
}

// WithMetricReader sets a custom metric reader for the meter provider.
func WithMetricReader(reader sdkmetric.Reader) OTelOption {
	return func(c *otelConfig) {
		c.metricReader = reader
	}
}

// NewOTelTracer creates a new OTelTracer with full OpenTelemetry setup.
//
// Args:
//   - serviceName: the service name used for resource attributes (service.name).
//   - opts: functional options to customize the tracer configuration.
//
// Returns:
//   - *OTelTracer: the configured tracer instance.
//   - error: non-nil if initialization fails.
func NewOTelTracer(serviceName string, opts ...OTelOption) (*OTelTracer, error) {
	cfg := &otelConfig{
		sampler: sdktrace.AlwaysSample(),
	}

	for _, opt := range opts {
		opt(cfg)
	}

	// Default to stdout exporter if none provided
	if cfg.exporter == nil {
		exp, err := stdouttrace.New()
		if err != nil {
			return nil, fmt.Errorf("create stdout exporter: %w", err)
		}
		cfg.exporter = exp
	}

	// Default to manual metric reader if none provided
	if cfg.metricReader == nil {
		cfg.metricReader = sdkmetric.NewManualReader()
	}

	// resource.Default() carries the SDK's schema URL; merging a second
	// resource with a different schema URL (e.g. semconv/v1.26.0) fails on
	// newer OTel SDKs ("conflicting Schema URL"). Align both sides by using
	// the default resource's own schema URL for the injected attributes.
	res, err := resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			resource.Default().SchemaURL(),
			semconv.ServiceName(serviceName),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("merge resources: %w", err)
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(cfg.exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(cfg.sampler),
	)

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(cfg.metricReader),
	)

	tracer := provider.Tracer("ares")
	meter := meterProvider.Meter("ares")

	metrics, err := NewMetrics(meter)
	if err != nil {
		return nil, fmt.Errorf("create metrics: %w", err)
	}

	return &OTelTracer{
		tracer:        tracer,
		meter:         meter,
		provider:      provider,
		meterProvider: meterProvider,
		metrics:       metrics,
	}, nil
}

// RecordLLMCall records an LLM call as an OpenTelemetry span with attributes
// for model, tokens used, duration, and error status.
//
// Args:
//   - ctx: the context carrying trace information.
//   - call: the LLM call data to record.
func (t *OTelTracer) RecordLLMCall(ctx context.Context, call *LLMCall) {
	if call == nil {
		return
	}

	ctx, span := t.tracer.Start(ctx, "llm.call")
	defer span.End()

	span.SetAttributes(
		attribute.String("llm.model", call.Model),
		attribute.Int("llm.tokens_used", call.TokensUsed),
		attribute.Float64("llm.duration_ms", float64(call.Duration.Milliseconds())),
		attribute.Bool("llm.has_error", call.Error != nil),
	)

	t.metrics.RecordLLMCall(ctx, call.Model, call.Duration, call.Error != nil)

	if call.Error != nil {
		span.RecordError(call.Error)
		span.SetStatus(codes.Error, call.Error.Error())
	}
}

// RecordToolCall records a tool execution as an OpenTelemetry span with
// attributes for tool name, duration, and error status.
//
// Args:
//   - ctx: the context carrying trace information.
//   - call: the tool call data to record.
func (t *OTelTracer) RecordToolCall(ctx context.Context, call *ToolCall) {
	if call == nil {
		return
	}

	ctx, span := t.tracer.Start(ctx, "tool.call")
	defer span.End()

	span.SetAttributes(
		attribute.String("tool.name", call.ToolName),
		attribute.Float64("tool.duration_ms", float64(call.Duration.Milliseconds())),
		attribute.Bool("tool.has_error", call.Error != nil),
	)

	t.metrics.RecordToolCall(ctx, call.ToolName, call.Duration, call.Error != nil)

	if call.Error != nil {
		span.RecordError(call.Error)
		span.SetStatus(codes.Error, call.Error.Error())
	}
}

// RecordAgentStep records an agent step as an OpenTelemetry span with
// attributes for agent ID, step name, duration, and error status.
//
// Args:
//   - ctx: the context carrying trace information.
//   - step: the agent step data to record.
func (t *OTelTracer) RecordAgentStep(ctx context.Context, step *AgentStep) {
	if step == nil {
		return
	}

	ctx, span := t.tracer.Start(ctx, "agent.step")
	defer span.End()

	span.SetAttributes(
		attribute.String("agent.id", step.AgentID),
		attribute.String("agent.step_name", step.StepName),
		attribute.Float64("agent.duration_ms", float64(step.Duration.Milliseconds())),
		attribute.Bool("agent.has_error", step.Error != nil),
	)

	t.metrics.RecordAgentStepDuration(ctx, step.AgentID, step.Duration, step.Error != nil)

	if step.Error != nil {
		span.RecordError(step.Error)
		span.SetStatus(codes.Error, step.Error.Error())
	}
}

// RecordError records an agent error as an OpenTelemetry span with
// attributes for agent ID, error type, and message.
//
// Args:
//   - ctx: the context carrying trace information.
//   - err: the agent error data to record.
func (t *OTelTracer) RecordError(ctx context.Context, err *AgentError) {
	if err == nil {
		return
	}

	_, span := t.tracer.Start(ctx, "agent.error")
	defer span.End()

	span.SetAttributes(
		attribute.String("agent.id", err.AgentID),
		attribute.String("error.type", err.ErrorType),
		attribute.String("error.message", err.Message),
	)

	t.metrics.RecordAgentError(ctx, err.AgentID, err.ErrorType)

	span.RecordError(fmt.Errorf("%s: %s", err.ErrorType, err.Message))
	span.SetStatus(codes.Error, err.Message)
}

// GetTraceID extracts the trace ID from the current span context.
//
// Args:
//   - ctx: the context containing trace information.
//
// Returns:
//   - string: the hex-encoded trace ID, or empty string if no span is active.
func (t *OTelTracer) GetTraceID(ctx context.Context) string {
	spanCtx := trace.SpanContextFromContext(ctx)
	if !spanCtx.IsValid() {
		return ""
	}
	return spanCtx.TraceID().String()
}

// WithTrace returns a new context with an active root span. The span is ended
// immediately so it is flushed/exported without leaking, but the span context
// (trace ID) remains embedded in the returned context for GetTraceID.
//
// Args:
//   - ctx: the parent context.
//
// Returns:
//   - context.Context: a new context with a trace ID.
func (t *OTelTracer) WithTrace(ctx context.Context) context.Context {
	ctx, span := t.tracer.Start(ctx, "root")
	span.End()
	return ctx
}

// Shutdown gracefully shuts down the tracer and meter providers.
// It flushes any pending spans and metrics before returning.
//
// Args:
//   - ctx: the context for shutdown timeout control.
//
// Returns:
//   - error: non-nil if shutdown fails.
func (t *OTelTracer) Shutdown(ctx context.Context) error {
	var errs []error

	if err := t.provider.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("shutdown tracer provider: %w", err))
	}

	if err := t.meterProvider.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("shutdown meter provider: %w", err))
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// Provider returns the underlying TracerProvider for instrumentation purposes.
//
// Returns:
//   - *sdktrace.TracerProvider: the OpenTelemetry tracer provider.
func (t *OTelTracer) Provider() *sdktrace.TracerProvider {
	return t.provider
}

// MeterProvider returns the underlying MeterProvider for instrumentation.
//
// Returns:
//   - *sdkmetric.MeterProvider: the OpenTelemetry meter provider.
func (t *OTelTracer) MeterProvider() *sdkmetric.MeterProvider {
	return t.meterProvider
}

// Metrics returns the metrics collector associated with this tracer.
//
// Returns:
//   - *Metrics: the metrics instance.
func (t *OTelTracer) Metrics() *Metrics {
	return t.metrics
}
