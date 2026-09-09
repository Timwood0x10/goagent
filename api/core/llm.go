// Package core is the DEPRECATED public alias of internal/llmcore (M5).
// New code MUST import internal/llmcore; this package exists only for
// external consumers and is scheduled for removal.
package core

import "github.com/Timwood0x10/ares/internal/llmcore"

type (
	LLMProvider        = llmcore.LLMProvider
	LLMConfig          = llmcore.LLMConfig
	LLMMessage         = llmcore.LLMMessage
	ToolCall           = llmcore.ToolCall
	FunctionCall       = llmcore.FunctionCall
	GenerateRequest    = llmcore.GenerateRequest
	Tool               = llmcore.Tool
	FunctionDefinition = llmcore.FunctionDefinition
	GenerateResponse   = llmcore.GenerateResponse
	TokenUsage         = llmcore.TokenUsage
	EmbeddingRequest   = llmcore.EmbeddingRequest
	EmbeddingResponse  = llmcore.EmbeddingResponse
	LLMRepository      = llmcore.LLMRepository
	LLMService         = llmcore.LLMService
	BaseConfig         = llmcore.BaseConfig
	Metadata           = llmcore.Metadata
	MessageRole        = llmcore.MessageRole
	Message            = llmcore.Message
	CleaningMode       = llmcore.CleaningMode
	CleanOptions       = llmcore.CleanOptions
	CleanerStats       = llmcore.CleanerStats
	ContextCleaner     = llmcore.ContextCleaner
)

const (
	// LLMProviderOpenRouter represents OpenRouter provider.
	LLMProviderOpenRouter = llmcore.LLMProviderOpenRouter
	// LLMProviderOllama represents Ollama provider.
	LLMProviderOllama = llmcore.LLMProviderOllama
	// LLMProviderOpenAI represents OpenAI provider.
	LLMProviderOpenAI = llmcore.LLMProviderOpenAI
	// LLMProviderAnthropic represents Anthropic provider.
	LLMProviderAnthropic = llmcore.LLMProviderAnthropic
)

const (
	// MessageRoleSystem represents a system message.
	MessageRoleSystem = llmcore.MessageRoleSystem
	// MessageRoleUser represents a user message.
	MessageRoleUser = llmcore.MessageRoleUser
	// MessageRoleAssistant represents an assistant message.
	MessageRoleAssistant = llmcore.MessageRoleAssistant
	// MessageRoleTool represents a tool/function call message.
	MessageRoleTool = llmcore.MessageRoleTool
)

const (
	CleaningModeDefault      = llmcore.CleaningModeDefault
	CleaningModeConservative = llmcore.CleaningModeConservative
	CleaningModeAggressive   = llmcore.CleaningModeAggressive
)

// DefaultCleanOptions returns sensible defaults for content truncation.
func DefaultCleanOptions() CleanOptions { return llmcore.DefaultCleanOptions() }
