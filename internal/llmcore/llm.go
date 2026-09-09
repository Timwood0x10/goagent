// Package llmcore provides core abstractions for LLM operations.
//
// This package holds the canonical definitions previously exposed as
// api/core (M5 migration); api/core is now a pure forwarding layer.
package llmcore

import (
	"context"
	"encoding/json"
)

// LLMProvider represents the LLM provider type.
type LLMProvider string

const (
	// LLMProviderOpenRouter represents OpenRouter provider.
	LLMProviderOpenRouter LLMProvider = "openrouter"
	// LLMProviderOllama represents Ollama provider.
	LLMProviderOllama LLMProvider = "ollama"
	// LLMProviderOpenAI represents OpenAI provider.
	LLMProviderOpenAI LLMProvider = "openai"
	// LLMProviderAnthropic represents Anthropic provider.
	LLMProviderAnthropic LLMProvider = "anthropic"
)

// LLMConfig represents configuration for LLM operations.
type LLMConfig struct {
	// Provider is the LLM provider.
	Provider LLMProvider
	// APIKey is the API key for authentication.
	APIKey string
	// BaseURL is the base URL for the LLM API.
	BaseURL string
	// Model is the model name.
	Model string
	// Timeout is the request timeout in seconds.
	Timeout int
	// Temperature controls randomness (0.0-2.0).
	Temperature float64
	// MaxTokens is the maximum number of tokens to generate.
	MaxTokens int
	// TopP is the nucleus sampling parameter.
	TopP float64
	// FrequencyPenalty penalizes frequent tokens.
	FrequencyPenalty float64
	// PresencePenalty penalizes new tokens.
	PresencePenalty float64
	// MaxPromptLength limits the prompt character count. 0 = internal default (8192).
	MaxPromptLength int `yaml:"max_prompt_length"`
}

// Message represents a message in a conversation.
type LLMMessage struct {
	// Role is the message role (system, user, assistant, tool).
	Role string
	// Content is the message content.
	Content string
	// ToolCalls contains tool/function call information (for assistant messages).
	ToolCalls []ToolCall
	// ToolCallID contains the tool call ID (for tool messages).
	ToolCallID string
	// Name is an optional participant name (OpenAI v2+).
	Name string
}

// ToolCall represents a tool/function call.
type ToolCall struct {
	// ID is the unique identifier for the tool call.
	ID string
	// Type is the type of tool call (e.g., "function").
	Type string
	// Function contains the function call details.
	Function FunctionCall
}

// FunctionCall represents a function call.
type FunctionCall struct {
	// Name is the function name.
	Name string
	// Arguments is the function arguments as JSON string.
	Arguments string
}

// GenerateRequest represents a text generation request.
type GenerateRequest struct {
	// Messages is the conversation messages.
	Messages []*LLMMessage
	// Model is the model to use (overrides config).
	Model string
	// Temperature controls randomness (overrides config).
	Temperature *float64
	// MaxTokens is the maximum tokens to generate (overrides config).
	MaxTokens *int
	// Stream enables streaming responses.
	Stream bool
	// Tools are available tools for function calling.
	Tools []Tool
	// TopP is nucleus sampling parameter (OpenAI v2+).
	TopP *float64 `json:"top_p,omitempty"`
	// Stop sequences where the model will stop generating.
	Stop []string `json:"stop,omitempty"`
	// Seed for deterministic sampling.
	Seed *int64 `json:"seed,omitempty"`
	// User identifier for the end-user.
	User string `json:"user,omitempty"`
	// Metadata for the request (OpenAI v2+).
	Metadata map[string]string `json:"metadata,omitempty"`
	// ToolChoice controls which tool is called (OpenAI v2+).
	ToolChoice json.RawMessage `json:"tool_choice,omitempty"`
	// ResponseFormat controls the output format (OpenAI v2+).
	ResponseFormat json.RawMessage `json:"response_format,omitempty"`
}

// Tool represents a tool/function available to the LLM.
type Tool struct {
	// Type is the tool type (e.g., "function").
	Type string
	// Function contains the function definition.
	Function FunctionDefinition
}

// FunctionDefinition represents a function definition.
type FunctionDefinition struct {
	// Name is the function name.
	Name string
	// Description is the function description.
	Description string
	// Parameters is the JSON schema for parameters.
	Parameters map[string]interface{}
}

// GenerateResponse represents a text generation response.
type GenerateResponse struct {
	// Content is the generated text content.
	Content string
	// FinishReason is the reason the generation finished.
	FinishReason string
	// Usage contains token usage information.
	Usage TokenUsage
	// ToolCalls contains tool calls made by the model.
	ToolCalls []ToolCall
	// Model is the model used for generation.
	Model string
}

// TokenUsage represents token usage statistics.
type TokenUsage struct {
	// PromptTokens is the number of tokens in the prompt.
	PromptTokens int
	// CompletionTokens is the number of tokens in the completion.
	CompletionTokens int
	// TotalTokens is the total number of tokens.
	TotalTokens int
}

// EmbeddingRequest represents an embedding generation request.
type EmbeddingRequest struct {
	// Input is the text to embed.
	Input string
	// Model is the model to use (overrides config).
	Model string
}

// EmbeddingResponse represents an embedding generation response.
type EmbeddingResponse struct {
	// Embedding is the generated embedding vector.
	Embedding []float32
	// Model is the model used for embedding.
	Model string
	// Usage contains token usage information.
	Usage TokenUsage
}

// LLMRepository defines the interface for LLM data access operations.
// NOTE: LLM operations are primarily client-side, but this interface
// can be used for caching, logging, or audit purposes.
type LLMRepository interface {
	// LogGeneration logs a generation request and response.
	// Args:
	// ctx - operation context.
	// request - the generation request.
	// response - the generation response.
	// Returns error if logging fails.
	LogGeneration(ctx context.Context, request *GenerateRequest, response *GenerateResponse) error

	// GetGenerationLog retrieves a generation log.
	// Args:
	// ctx - operation context.
	// logID - the log identifier.
	// Returns the log or error if not found.
	GetGenerationLog(ctx context.Context, logID string) (*GenerateRequest, *GenerateResponse, error)
}

// LLMService defines the interface for LLM business logic operations.
type LLMService interface {
	// Generate generates text from the given messages.
	// Args:
	// ctx - operation context.
	// request - the generation request.
	// Returns the generation response or error.
	Generate(ctx context.Context, request *GenerateRequest) (*GenerateResponse, error)

	// GenerateSimple generates text from a simple prompt.
	// Args:
	// ctx - operation context.
	// prompt - the prompt text.
	// Returns the generated text or error.
	GenerateSimple(ctx context.Context, prompt string) (string, error)

	// GenerateEmbedding generates an embedding for the given text.
	// Args:
	// ctx - operation context.
	// request - the embedding request.
	// Returns the embedding response or error.
	GenerateEmbedding(ctx context.Context, request *EmbeddingRequest) (*EmbeddingResponse, error)

	// GetConfig returns the current LLM configuration.
	// Returns the LLM configuration.
	GetConfig() *LLMConfig

	// IsEnabled checks if the LLM service is properly configured and available.
	// Returns true if enabled, false otherwise.
	IsEnabled() bool

	// GetProvider returns the current LLM provider.
	// Returns the provider type.
	GetProvider() LLMProvider

	// GetModel returns the current model name.
	// Returns the model name.
	GetModel() string
}
