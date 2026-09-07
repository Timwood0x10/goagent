// nolint: errcheck // Test code may ignore return values
package observability

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNoopTracer_RecordLLMCall(t *testing.T) {
	tracer := NewNoopTracer()
	ctx := context.Background()

	call := &LLMCall{
		TraceID:    "test-123",
		Model:      "gpt-4",
		Prompt:     "test prompt",
		Response:   "test response",
		TokensUsed: 100,
		Duration:   time.Second,
	}

	tracer.RecordLLMCall(ctx, call)
}

func TestNoopTracer_RecordToolCall(t *testing.T) {
	tracer := NewNoopTracer()
	ctx := context.Background()

	call := &ToolCall{
		TraceID:  "test-123",
		ToolName: "weather",
		Input:    map[string]any{"city": "Beijing"},
		Output:   map[string]any{"temp": 25},
		Duration: time.Millisecond * 500,
	}

	tracer.RecordToolCall(ctx, call)
}

func TestNoopTracer_RecordAgentStep(t *testing.T) {
	tracer := NewNoopTracer()
	ctx := context.Background()

	step := &AgentStep{
		TraceID:  "test-123",
		AgentID:  "leader-1",
		StepName: "parse_profile",
		Metadata: map[string]any{"input": "casual style"},
		Duration: time.Millisecond * 100,
	}

	tracer.RecordAgentStep(ctx, step)
}

func TestNoopTracer_RecordError(t *testing.T) {
	tracer := NewNoopTracer()
	ctx := context.Background()

	err := &AgentError{
		TraceID:   "test-123",
		AgentID:   "leader-1",
		ErrorType: "validation",
		Message:   "invalid input",
		Metadata:  map[string]any{"field": "style"},
	}

	tracer.RecordError(ctx, err)
}

func TestNoopTracer_GetTraceID_Empty(t *testing.T) {
	tracer := NewNoopTracer()
	ctx := context.Background()

	traceID := tracer.GetTraceID(ctx)
	if traceID != "" {
		t.Errorf("expected empty trace ID, got %s", traceID)
	}
}

func TestNoopTracer_GetTraceID_WithValue(t *testing.T) {
	tracer := NewNoopTracer()
	ctx := context.WithValue(context.Background(), defaultTraceID, "custom-trace-id")

	traceID := tracer.GetTraceID(ctx)
	if traceID != "custom-trace-id" {
		t.Errorf("expected custom-trace-id, got %s", traceID)
	}
}

func TestNoopTracer_WithTrace(t *testing.T) {
	tracer := NewNoopTracer()
	ctx := context.Background()

	newCtx := tracer.WithTrace(ctx)
	traceID := tracer.GetTraceID(newCtx)

	if traceID == "" {
		t.Error("expected trace ID to be generated")
	}
}

func TestLogTracer_RecordLLMCall(t *testing.T) {
	tracer := NewLogTracer(&LogTracerConfig{Logger: nil})
	ctx := context.Background()

	call := &LLMCall{
		TraceID:    "test-123",
		Model:      "gpt-4",
		Prompt:     "test prompt",
		Response:   "test response",
		TokensUsed: 100,
		Duration:   time.Second,
	}

	tracer.RecordLLMCall(ctx, call)
}

func TestLogTracer_RecordToolCall(t *testing.T) {
	tracer := NewLogTracer(&LogTracerConfig{Logger: nil})
	ctx := context.Background()

	call := &ToolCall{
		TraceID:  "test-123",
		ToolName: "weather",
		Input:    map[string]any{"city": "Beijing"},
		Output:   map[string]any{"temp": 25},
		Duration: time.Millisecond * 500,
		Error:    nil,
	}

	tracer.RecordToolCall(ctx, call)
}

func TestLogTracer_RecordToolCall_WithError(t *testing.T) {
	tracer := NewLogTracer(&LogTracerConfig{Logger: nil})
	ctx := context.Background()

	testErr := errors.New("tool execution failed")
	call := &ToolCall{
		TraceID:  "test-123",
		ToolName: "weather",
		Input:    map[string]any{"city": "Beijing"},
		Output:   nil,
		Duration: time.Millisecond * 500,
		Error:    testErr,
	}

	tracer.RecordToolCall(ctx, call)
}

func TestLogTracer_RecordAgentStep(t *testing.T) {
	tracer := NewLogTracer(&LogTracerConfig{Logger: nil})
	ctx := context.Background()

	step := &AgentStep{
		TraceID:  "test-123",
		AgentID:  "leader-1",
		StepName: "parse_profile",
		Metadata: map[string]any{"input": "casual style"},
		Duration: time.Millisecond * 100,
	}

	tracer.RecordAgentStep(ctx, step)
}

func TestLogTracer_RecordAgentStep_WithError(t *testing.T) {
	tracer := NewLogTracer(&LogTracerConfig{Logger: nil})
	ctx := context.Background()

	testErr := errors.New("step failed")
	step := &AgentStep{
		TraceID:  "test-123",
		AgentID:  "leader-1",
		StepName: "parse_profile",
		Metadata: map[string]any{"input": "casual style"},
		Duration: time.Millisecond * 100,
		Error:    testErr,
	}

	tracer.RecordAgentStep(ctx, step)
}

func TestLogTracer_RecordError(t *testing.T) {
	tracer := NewLogTracer(&LogTracerConfig{Logger: nil})
	ctx := context.Background()

	err := &AgentError{
		TraceID:   "test-123",
		AgentID:   "leader-1",
		ErrorType: "validation",
		Message:   "invalid input",
		Metadata:  map[string]any{"field": "style"},
	}

	tracer.RecordError(ctx, err)
}

func TestLogTracer_GetTraceID_Empty(t *testing.T) {
	tracer := NewLogTracer(&LogTracerConfig{})
	ctx := context.Background()

	traceID := tracer.GetTraceID(ctx)
	if traceID != "" {
		t.Errorf("expected empty trace ID, got %s", traceID)
	}
}

func TestLogTracer_GetTraceID_WithValue(t *testing.T) {
	tracer := NewLogTracer(&LogTracerConfig{})
	ctx := context.WithValue(context.Background(), defaultTraceID, "custom-trace-id")

	traceID := tracer.GetTraceID(ctx)
	if traceID != "custom-trace-id" {
		t.Errorf("expected custom-trace-id, got %s", traceID)
	}
}

func TestLogTracer_WithTrace(t *testing.T) {
	tracer := NewLogTracer(&LogTracerConfig{})
	ctx := context.Background()

	newCtx := tracer.WithTrace(ctx)
	traceID := tracer.GetTraceID(newCtx)

	if traceID == "" {
		t.Error("expected trace ID to be generated")
	}
}

func TestLogTracer_WithTrace_DifferentIDs(t *testing.T) {
	tracer := NewLogTracer(&LogTracerConfig{})
	ctx := context.Background()

	ctx1 := tracer.WithTrace(ctx)
	ctx2 := tracer.WithTrace(ctx)

	id1 := tracer.GetTraceID(ctx1)
	id2 := tracer.GetTraceID(ctx2)

	if id1 == id2 {
		t.Errorf("expected different trace IDs, got same: %s", id1)
	}
}

func TestLogTracer_JSON_Format(t *testing.T) {
	tracer := NewLogTracer(&LogTracerConfig{Logger: nil})
	ctx := context.Background()

	call := &LLMCall{
		TraceID:    "test-123",
		Model:      "gpt-4",
		Prompt:     "test prompt",
		Response:   "test response",
		TokensUsed: 100,
		Duration:   time.Second,
	}

	tracer.RecordLLMCall(ctx, call)
}

func TestTracerInterface_AllMethods(t *testing.T) {
	tracer := NewNoopTracer()
	ctx := tracer.WithTrace(context.Background())

	tracer.RecordLLMCall(ctx, &LLMCall{
		TraceID: "test",
	})

	tracer.RecordToolCall(ctx, &ToolCall{
		TraceID: "test",
	})

	tracer.RecordAgentStep(ctx, &AgentStep{
		TraceID: "test",
	})

	tracer.RecordError(ctx, &AgentError{
		TraceID: "test",
	})

	if tracer.GetTraceID(ctx) == "" {
		t.Error("expected trace ID")
	}
}

func TestNoopTracer_ConcurrentAccess(t *testing.T) {
	tracer := NewNoopTracer()
	ctx := context.Background()

	done := make(chan bool, 10)

	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				tracer.RecordLLMCall(ctx, &LLMCall{
					TraceID: "concurrent-test",
				})
				tracer.GetTraceID(ctx)
				tracer.WithTrace(ctx)
			}
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestLogTracer_ConcurrentAccess(t *testing.T) {
	tracer := NewLogTracer(&LogTracerConfig{})
	ctx := context.Background()

	done := make(chan bool, 10)

	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				tracer.RecordLLMCall(ctx, &LLMCall{
					TraceID: "concurrent-test",
				})
				tracer.GetTraceID(ctx)
				tracer.WithTrace(ctx)
			}
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

// nolint: errcheck // Test code may ignore return values
