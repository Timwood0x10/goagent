// nolint: errcheck // Test code may ignore return values
package observability

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

func TestNewOTelTracer_Defaults(t *testing.T) {
	tracer, err := NewOTelTracer("test-service")
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if tracer == nil {
		t.Fatal("expected non-nil tracer")
	}
	defer tracer.Shutdown(context.Background())

	if tracer.Provider() == nil {
		t.Error("expected non-nil provider")
	}
	if tracer.MeterProvider() == nil {
		t.Error("expected non-nil meter provider")
	}
	if tracer.Metrics() == nil {
		t.Error("expected non-nil metrics")
	}
}

func TestNewOTelTracer_WithCustomExporter(t *testing.T) {
	exp, err := stdouttrace.New()
	if err != nil {
		t.Fatalf("failed to create stdout exporter: %v", err)
	}

	tracer, err := NewOTelTracer(
		"test-service",
		WithExporter(exp),
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	defer tracer.Shutdown(context.Background())

	if tracer == nil {
		t.Fatal("expected non-nil tracer")
	}
}

func TestNewOTelTracer_WithMetricReader(t *testing.T) {
	reader := sdkmetric.NewManualReader()

	tracer, err := NewOTelTracer(
		"test-service",
		WithMetricReader(reader),
	)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	defer tracer.Shutdown(context.Background())

	if tracer == nil {
		t.Fatal("expected non-nil tracer")
	}
}

func TestOTelTracer_RecordLLMCall(t *testing.T) {
	tracer, _ := NewOTelTracer("test-service")
	defer tracer.Shutdown(context.Background())
	ctx := context.Background()

	call := &LLMCall{
		TraceID:    "test-123",
		Model:      "gpt-4o",
		Prompt:     "Hello world",
		Response:   "Hi there",
		TokensUsed: 100,
		Duration:   time.Second,
	}

	tracer.RecordLLMCall(ctx, call)
}

func TestOTelTracer_RecordLLMCall_WithError(t *testing.T) {
	tracer, _ := NewOTelTracer("test-service")
	defer tracer.Shutdown(context.Background())
	ctx := context.Background()

	testErr := errors.New("rate limit exceeded")
	call := &LLMCall{
		TraceID:    "test-456",
		Model:      "gpt-4o",
		Prompt:     "Hello",
		Response:   "",
		TokensUsed: 50,
		Duration:   time.Millisecond * 500,
		Error:      testErr,
	}

	tracer.RecordLLMCall(ctx, call)
}

func TestOTelTracer_RecordLLMCall_Nil(t *testing.T) {
	tracer, _ := NewOTelTracer("test-service")
	defer tracer.Shutdown(context.Background())
	ctx := context.Background()

	tracer.RecordLLMCall(ctx, nil)
}

func TestOTelTracer_RecordToolCall(t *testing.T) {
	tracer, _ := NewOTelTracer("test-service")
	defer tracer.Shutdown(context.Background())
	ctx := context.Background()

	call := &ToolCall{
		TraceID:  "test-789",
		ToolName: "weather_api",
		Input:    map[string]any{"city": "Beijing"},
		Output:   map[string]any{"temp": 25.5},
		Duration: time.Millisecond * 200,
	}

	tracer.RecordToolCall(ctx, call)
}

func TestOTelTracer_RecordToolCall_WithError(t *testing.T) {
	tracer, _ := NewOTelTracer("test-service")
	defer tracer.Shutdown(context.Background())
	ctx := context.Background()

	testErr := errors.New("API timeout")
	call := &ToolCall{
		TraceID:  "test-abc",
		ToolName: "search_api",
		Input:    map[string]any{"query": "golang"},
		Output:   nil,
		Duration: time.Second * 5,
		Error:    testErr,
	}

	tracer.RecordToolCall(ctx, call)
}

func TestOTelTracer_RecordToolCall_Nil(t *testing.T) {
	tracer, _ := NewOTelTracer("test-service")
	defer tracer.Shutdown(context.Background())
	ctx := context.Background()

	tracer.RecordToolCall(ctx, nil)
}

func TestOTelTracer_RecordAgentStep(t *testing.T) {
	tracer, _ := NewOTelTracer("test-service")
	defer tracer.Shutdown(context.Background())
	ctx := context.Background()

	step := &AgentStep{
		TraceID:  "step-001",
		AgentID:  "agent-1",
		StepName: "parse_input",
		Metadata: map[string]any{"lang": "en"},
		Duration: time.Millisecond * 50,
	}

	tracer.RecordAgentStep(ctx, step)
}

func TestOTelTracer_RecordAgentStep_WithError(t *testing.T) {
	tracer, _ := NewOTelTracer("test-service")
	defer tracer.Shutdown(context.Background())
	ctx := context.Background()

	testErr := errors.New("invalid JSON format")
	step := &AgentStep{
		TraceID:  "step-002",
		AgentID:  "agent-1",
		StepName: "validate_response",
		Metadata: map[string]any{},
		Duration: time.Millisecond * 10,
		Error:    testErr,
	}

	tracer.RecordAgentStep(ctx, step)
}

func TestOTelTracer_RecordAgentStep_Nil(t *testing.T) {
	tracer, _ := NewOTelTracer("test-service")
	defer tracer.Shutdown(context.Background())
	ctx := context.Background()

	tracer.RecordAgentStep(ctx, nil)
}

func TestOTelTracer_RecordError(t *testing.T) {
	tracer, _ := NewOTelTracer("test-service")
	defer tracer.Shutdown(context.Background())
	ctx := context.Background()

	err := &AgentError{
		TraceID:   "err-001",
		AgentID:   "agent-1",
		ErrorType: "validation_error",
		Message:   "invalid input format",
		Metadata:  map[string]any{"field": "prompt"},
	}

	tracer.RecordError(ctx, err)
}

func TestOTelTracer_RecordError_Nil(t *testing.T) {
	tracer, _ := NewOTelTracer("test-service")
	defer tracer.Shutdown(context.Background())
	ctx := context.Background()

	tracer.RecordError(ctx, nil)
}

func TestOTelTracer_GetTraceID_EmptyContext(t *testing.T) {
	tracer, _ := NewOTelTracer("test-service")
	defer tracer.Shutdown(context.Background())
	ctx := context.Background()

	traceID := tracer.GetTraceID(ctx)
	if traceID != "" {
		t.Errorf("expected empty trace ID for background context, got: %s", traceID)
	}
}

func TestOTelTracer_GetTraceID_WithTrace(t *testing.T) {
	tracer, _ := NewOTelTracer("test-service")
	defer tracer.Shutdown(context.Background())

	ctx := tracer.WithTrace(context.Background())
	traceID := tracer.GetTraceID(ctx)

	if traceID == "" {
		t.Error("expected non-empty trace ID after WithTrace")
	}
}

func TestOTelTracer_WithTrace(t *testing.T) {
	tracer, _ := NewOTelTracer("test-service")
	defer tracer.Shutdown(context.Background())

	ctx1 := tracer.WithTrace(context.Background())
	ctx2 := tracer.WithTrace(context.Background())

	id1 := tracer.GetTraceID(ctx1)
	id2 := tracer.GetTraceID(ctx2)

	if id1 == "" || id2 == "" {
		t.Error("expected both trace IDs to be non-empty")
	}
}

func TestOTelTracer_Shutdown(t *testing.T) {
	tracer, err := NewOTelTracer("test-service")
	if err != nil {
		t.Fatalf("failed to create tracer: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = tracer.Shutdown(ctx)
	if err != nil {
		t.Errorf("expected no error on shutdown, got: %v", err)
	}
}

func TestOTelTracer_Shutdown_Double(t *testing.T) {
	tracer, _ := NewOTelTracer("test-service")

	ctx := context.Background()
	tracer.Shutdown(ctx)
	err := tracer.Shutdown(ctx)

	if err != nil {
		t.Logf("second shutdown returned error (may be expected): %v", err)
	}
}

func TestOTelTracer_InterfaceImplementation(t *testing.T) {
	tracer, _ := NewOTelTracer("test-service")
	defer tracer.Shutdown(context.Background())

	// Verify OTelTracer implements the Tracer interface.
	var iface Tracer = tracer
	iface.WithTrace(context.Background())
	iface.RecordLLMCall(context.Background(), &LLMCall{Model: "test"})
	iface.RecordToolCall(context.Background(), &ToolCall{ToolName: "test"})
	iface.RecordAgentStep(context.Background(), &AgentStep{AgentID: "a1", StepName: "s1"})
	iface.RecordError(context.Background(), &AgentError{AgentID: "a1", ErrorType: "e1"})

	_ = iface.GetTraceID(context.Background())
}
