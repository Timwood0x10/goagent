package observability

import (
	"context"
	"log/slog"

	"github.com/Timwood0x10/ares/internal/logger"
)

// log is the package-level structured logger.
var log = logger.Module("observability")

// LogTracer is a tracer that logs to standard output using slog.
// It can be used for development and debugging.
type LogTracer struct {
	logger *slog.Logger
}

// LogTracerConfig holds configuration for LogTracer.
type LogTracerConfig struct {
	Logger *slog.Logger
}

// NewLogTracer creates a new LogTracer.
func NewLogTracer(cfg *LogTracerConfig) Tracer {
	logger := slog.Default()
	if cfg != nil && cfg.Logger != nil {
		logger = cfg.Logger
	}
	return &LogTracer{
		logger: logger,
	}
}

// RecordLLMCall implements Tracer.
func (t *LogTracer) RecordLLMCall(ctx context.Context, call *LLMCall) {
	if call == nil {
		return
	}
	if call.Error != nil {
		t.logger.ErrorContext(ctx, "LLM call failed",
			"trace_id", call.TraceID,
			"model", call.Model,
			"prompt_len", len(call.Prompt),
			"response_len", len(call.Response),
			"tokens", call.TokensUsed,
			"duration_ms", call.Duration.Milliseconds(),
			"error", call.Error.Error(),
		)
	} else {
		t.logger.InfoContext(ctx, "LLM call",
			"trace_id", call.TraceID,
			"model", call.Model,
			"prompt_len", len(call.Prompt),
			"response_len", len(call.Response),
			"tokens", call.TokensUsed,
			"duration_ms", call.Duration.Milliseconds(),
		)
	}
}

// RecordToolCall implements Tracer.
func (t *LogTracer) RecordToolCall(ctx context.Context, call *ToolCall) {
	if call == nil {
		return
	}
	if call.Error != nil {
		t.logger.ErrorContext(ctx, "Tool call failed",
			"trace_id", call.TraceID,
			"tool_name", call.ToolName,
			"input", call.Input,
			"output", call.Output,
			"duration_ms", call.Duration.Milliseconds(),
			"error", call.Error.Error(),
		)
	} else {
		t.logger.InfoContext(ctx, "Tool call",
			"trace_id", call.TraceID,
			"tool_name", call.ToolName,
			"input", call.Input,
			"output", call.Output,
			"duration_ms", call.Duration.Milliseconds(),
		)
	}
}

// RecordAgentStep implements Tracer.
func (t *LogTracer) RecordAgentStep(ctx context.Context, step *AgentStep) {
	if step == nil {
		return
	}
	if step.Error != nil {
		t.logger.ErrorContext(ctx, "Agent step failed",
			"trace_id", step.TraceID,
			"agent_id", step.AgentID,
			"step_name", step.StepName,
			"metadata", step.Metadata,
			"duration_ms", step.Duration.Milliseconds(),
			"error", step.Error.Error(),
		)
	} else {
		t.logger.InfoContext(ctx, "Agent step",
			"trace_id", step.TraceID,
			"agent_id", step.AgentID,
			"step_name", step.StepName,
			"metadata", step.Metadata,
			"duration_ms", step.Duration.Milliseconds(),
		)
	}
}

// RecordError implements Tracer.
func (t *LogTracer) RecordError(ctx context.Context, err *AgentError) {
	if err == nil {
		return
	}
	t.logger.ErrorContext(ctx, "Agent error",
		"trace_id", err.TraceID,
		"agent_id", err.AgentID,
		"error_type", err.ErrorType,
		"message", err.Message,
		"metadata", err.Metadata,
	)
}

// GetTraceID returns the trace ID from context.
func (t *LogTracer) GetTraceID(ctx context.Context) string {
	if id, ok := ctx.Value(defaultTraceID).(string); ok {
		return id
	}
	return ""
}

// WithTrace returns a new context with a new trace ID.
func (t *LogTracer) WithTrace(ctx context.Context) context.Context {
	if existingID, ok := ctx.Value(defaultTraceID).(string); ok && existingID != "" {
		return ctx
	}
	traceID := generateTraceID()
	return context.WithValue(ctx, defaultTraceID, traceID)
}
