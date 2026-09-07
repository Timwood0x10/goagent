package observability

import (
	"context"
	"time"
)

// Tracer defines the interface for observability tracking.
type Tracer interface {
	// RecordLLMCall records an LLM call.
	RecordLLMCall(ctx context.Context, call *LLMCall)

	// RecordToolCall records a tool execution.
	RecordToolCall(ctx context.Context, call *ToolCall)

	// RecordAgentStep records an agent step.
	RecordAgentStep(ctx context.Context, step *AgentStep)

	// RecordError records an error.
	RecordError(ctx context.Context, err *AgentError)

	// GetTraceID returns the current trace ID.
	GetTraceID(ctx context.Context) string

	// WithTrace returns a new context with trace ID.
	WithTrace(ctx context.Context) context.Context
}

// LLMCall represents an LLM invocation.
type LLMCall struct {
	TraceID    string
	Model      string
	Prompt     string
	Response   string
	TokensUsed int
	Duration   time.Duration
	Error      error
}

// ToolCall represents a tool execution.
type ToolCall struct {
	TraceID  string
	ToolName string
	Input    any
	Output   any
	Duration time.Duration
	Error    error
}

// AgentStep represents an agent execution step.
type AgentStep struct {
	TraceID  string
	AgentID  string
	StepName string
	Metadata map[string]any
	Duration time.Duration
	Error    error
}

// AgentError represents an error during agent execution.
type AgentError struct {
	TraceID   string
	AgentID   string
	ErrorType string
	Message   string
	Metadata  map[string]any
}
