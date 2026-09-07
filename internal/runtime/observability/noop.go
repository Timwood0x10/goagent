package observability

import (
	"context"
	"fmt"
	"sync/atomic"
)

// traceIDKey is the context key for trace ID.
type traceIDKey string

const defaultTraceID traceIDKey = "trace_id"

var traceCounter uint64

// NoopTracer is a lightweight tracer that manages trace IDs in context
// without performing any external recording. Use this when observability
// overhead should be minimal but trace ID propagation is still needed.
type NoopTracer struct{}

// NewNoopTracer creates a new NoopTracer.
func NewNoopTracer() Tracer {
	return &NoopTracer{}
}

// RecordLLMCall implements Tracer. No-op: does not record.
func (t *NoopTracer) RecordLLMCall(ctx context.Context, call *LLMCall) {
}

// RecordToolCall implements Tracer. No-op: does not record.
func (t *NoopTracer) RecordToolCall(ctx context.Context, call *ToolCall) {
}

// RecordAgentStep implements Tracer. No-op: does not record.
func (t *NoopTracer) RecordAgentStep(ctx context.Context, step *AgentStep) {
}

// RecordError implements Tracer. No-op: does not record.
func (t *NoopTracer) RecordError(ctx context.Context, err *AgentError) {
}

// GetTraceID returns the trace ID from context if present.
func (t *NoopTracer) GetTraceID(ctx context.Context) string {
	if id, ok := ctx.Value(defaultTraceID).(string); ok {
		return id
	}
	return ""
}

// WithTrace returns a new context with a generated trace ID.
func (t *NoopTracer) WithTrace(ctx context.Context) context.Context {
	if existingID, ok := ctx.Value(defaultTraceID).(string); ok && existingID != "" {
		return ctx
	}
	return context.WithValue(ctx, defaultTraceID, generateTraceID())
}

// generateTraceID generates a unique trace ID.
func generateTraceID() string {
	id := atomic.AddUint64(&traceCounter, 1)
	return fmt.Sprintf("trace-%d", id)
}
