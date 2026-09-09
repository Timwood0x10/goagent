package llmcore

import (
	"context"
	"errors"
	"testing"
)

// TestLLMProvider tests LLMProvider constants.
func TestLLMProvider(t *testing.T) {
	tests := []struct {
		name     string
		provider LLMProvider
		want     string
	}{
		{
			name:     "OpenRouter provider",
			provider: LLMProviderOpenRouter,
			want:     "openrouter",
		},
		{
			name:     "Ollama provider",
			provider: LLMProviderOllama,
			want:     "ollama",
		},
		{
			name:     "OpenAI provider",
			provider: LLMProviderOpenAI,
			want:     "openai",
		},
		{
			name:     "Anthropic provider",
			provider: LLMProviderAnthropic,
			want:     "anthropic",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.provider) != tt.want {
				t.Errorf("LLMProvider = %q, want %q", tt.provider, tt.want)
			}
		})
	}
}

// TestLLMProviderUniqueness tests that all LLMProvider values are unique.
func TestLLMProviderUniqueness(t *testing.T) {
	providers := map[string]bool{
		string(LLMProviderOpenRouter): true,
		string(LLMProviderOllama):     true,
		string(LLMProviderOpenAI):     true,
		string(LLMProviderAnthropic):  true,
	}

	if len(providers) != 4 {
		t.Errorf("expected 4 unique LLM providers, got %d", len(providers))
	}
}

// TestLLMConfig tests LLMConfig struct.
func TestLLMConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  LLMConfig
	}{
		{
			name: "full config",
			cfg: LLMConfig{
				Provider:         LLMProviderOpenAI,
				APIKey:           "sk-1234567890",
				BaseURL:          "https://api.openai.com/v1",
				Model:            "gpt-4",
				Timeout:          30,
				Temperature:      0.7,
				MaxTokens:        2000,
				TopP:             1.0,
				FrequencyPenalty: 0.0,
				PresencePenalty:  0.0,
			},
		},
		{
			name: "minimal config",
			cfg: LLMConfig{
				Provider: LLMProviderOllama,
				Model:    "llama2",
			},
		},
		{
			name: "config with zero values",
			cfg: LLMConfig{
				Provider:         LLMProviderOpenRouter,
				APIKey:           "",
				BaseURL:          "",
				Model:            "",
				Timeout:          0,
				Temperature:      0.0,
				MaxTokens:        0,
				TopP:             0.0,
				FrequencyPenalty: 0.0,
				PresencePenalty:  0.0,
			},
		},
		{
			name: "config with extreme values",
			cfg: LLMConfig{
				Provider:         LLMProviderAnthropic,
				APIKey:           "sk-xxxxxxxxxxxx",
				BaseURL:          "https://api.anthropic.com/v1",
				Model:            "claude-3-opus",
				Timeout:          120,
				Temperature:      2.0,
				MaxTokens:        100000,
				TopP:             1.0,
				FrequencyPenalty: 2.0,
				PresencePenalty:  2.0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = tt.cfg.Provider
			_ = tt.cfg.APIKey
			_ = tt.cfg.BaseURL
			_ = tt.cfg.Model
			_ = tt.cfg.Timeout
			_ = tt.cfg.Temperature
			_ = tt.cfg.MaxTokens
			_ = tt.cfg.TopP
			_ = tt.cfg.FrequencyPenalty
			_ = tt.cfg.PresencePenalty
		})
	}
}

// TestLLMMessage tests LLMMessage struct.
func TestLLMMessage(t *testing.T) {
	tests := []struct {
		name    string
		message LLMMessage
	}{
		{
			name: "user message",
			message: LLMMessage{
				Role:    "user",
				Content: "Hello, how are you?",
			},
		},
		{
			name: "assistant message",
			message: LLMMessage{
				Role:    "assistant",
				Content: "I'm doing well, thank you!",
			},
		},
		{
			name: "system message",
			message: LLMMessage{
				Role:    "system",
				Content: "You are a helpful assistant.",
			},
		},
		{
			name: "tool message",
			message: LLMMessage{
				Role:       "tool",
				Content:    "Tool result",
				ToolCallID: "call-123",
			},
		},
		{
			name: "assistant message with tool calls",
			message: LLMMessage{
				Role:    "assistant",
				Content: "I'll use a tool to help you.",
				ToolCalls: []ToolCall{
					{
						ID:   "call-456",
						Type: "function",
						Function: FunctionCall{
							Name:      "get_weather",
							Arguments: `{"location":"Tokyo"}`,
						},
					},
				},
			},
		},
		{
			name: "message with nil tool calls",
			message: LLMMessage{
				Role:      "assistant",
				Content:   "No tools needed",
				ToolCalls: nil,
			},
		},
		{
			name: "message with empty tool calls",
			message: LLMMessage{
				Role:      "assistant",
				Content:   "No tools needed",
				ToolCalls: []ToolCall{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = tt.message.Role
			_ = tt.message.Content
			_ = tt.message.ToolCalls
			_ = tt.message.ToolCallID
		})
	}
}

// TestToolCall tests ToolCall struct.
func TestToolCall(t *testing.T) {
	tests := []struct {
		name string
		call ToolCall
	}{
		{
			name: "function call",
			call: ToolCall{
				ID:   "call-123",
				Type: "function",
				Function: FunctionCall{
					Name:      "search",
					Arguments: `{"query":"test"}`,
				},
			},
		},
		{
			name: "minimal tool call",
			call: ToolCall{
				ID:   "call-456",
				Type: "function",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = tt.call.ID
			_ = tt.call.Type
			_ = tt.call.Function
		})
	}
}

// TestFunctionCall tests FunctionCall struct.
func TestFunctionCall(t *testing.T) {
	tests := []struct {
		name string
		fn   FunctionCall
	}{
		{
			name: "function with arguments",
			fn: FunctionCall{
				Name:      "calculate",
				Arguments: `{"a":1,"b":2}`,
			},
		},
		{
			name: "function without arguments",
			fn: FunctionCall{
				Name:      "ping",
				Arguments: `{}`,
			},
		},
		{
			name: "function with empty arguments",
			fn: FunctionCall{
				Name:      "test",
				Arguments: "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = tt.fn.Name
			_ = tt.fn.Arguments
		})
	}
}

// TestGenerateRequest tests GenerateRequest struct.
func TestGenerateRequest(t *testing.T) {
	temperature := 0.7
	maxTokens := 2000

	tests := []struct {
		name    string
		request GenerateRequest
	}{
		{
			name: "full request",
			request: GenerateRequest{
				Messages: []*LLMMessage{
					{
						Role:    "user",
						Content: "Hello",
					},
				},
				Model:       "gpt-4",
				Temperature: &temperature,
				MaxTokens:   &maxTokens,
				Stream:      false,
				Tools: []Tool{
					{
						Type: "function",
						Function: FunctionDefinition{
							Name:        "test",
							Description: "Test function",
							Parameters:  map[string]interface{}{},
						},
					},
				},
			},
		},
		{
			name: "minimal request",
			request: GenerateRequest{
				Messages: []*LLMMessage{
					{
						Role:    "user",
						Content: "Test",
					},
				},
			},
		},
		{
			name: "streaming request",
			request: GenerateRequest{
				Messages: []*LLMMessage{},
				Stream:   true,
			},
		},
		{
			name: "request with nil pointers",
			request: GenerateRequest{
				Messages:    []*LLMMessage{},
				Temperature: nil,
				MaxTokens:   nil,
			},
		},
		{
			name: "request with empty messages",
			request: GenerateRequest{
				Messages: []*LLMMessage{},
			},
		},
		{
			name: "request with nil messages",
			request: GenerateRequest{
				Messages: nil,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = tt.request.Messages
			_ = tt.request.Model
			_ = tt.request.Temperature
			_ = tt.request.MaxTokens
			_ = tt.request.Stream
			_ = tt.request.Tools
		})
	}
}

// TestTool tests Tool struct.
func TestTool(t *testing.T) {
	tests := []struct {
		name string
		tool Tool
	}{
		{
			name: "function tool",
			tool: Tool{
				Type: "function",
				Function: FunctionDefinition{
					Name:        "get_weather",
					Description: "Get weather information",
					Parameters: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"location": map[string]interface{}{
								"type": "string",
							},
						},
					},
				},
			},
		},
		{
			name: "minimal tool",
			tool: Tool{
				Type: "function",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = tt.tool.Type
			_ = tt.tool.Function
		})
	}
}

// TestFunctionDefinition tests FunctionDefinition struct.
func TestFunctionDefinition(t *testing.T) {
	tests := []struct {
		name string
		def  FunctionDefinition
	}{
		{
			name: "full function definition",
			def: FunctionDefinition{
				Name:        "calculate",
				Description: "Performs calculation",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"x": map[string]interface{}{"type": "number"},
						"y": map[string]interface{}{"type": "number"},
					},
				},
			},
		},
		{
			name: "minimal function definition",
			def: FunctionDefinition{
				Name: "ping",
			},
		},
		{
			name: "function with empty parameters",
			def: FunctionDefinition{
				Name:       "test",
				Parameters: map[string]interface{}{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = tt.def.Name
			_ = tt.def.Description
			_ = tt.def.Parameters
		})
	}
}

// TestGenerateResponse tests GenerateResponse struct.
func TestGenerateResponse(t *testing.T) {
	tests := []struct {
		name     string
		response GenerateResponse
	}{
		{
			name: "full response",
			response: GenerateResponse{
				Content:      "Hello! How can I help you?",
				FinishReason: "stop",
				Usage: TokenUsage{
					PromptTokens:     10,
					CompletionTokens: 20,
					TotalTokens:      30,
				},
				ToolCalls: []ToolCall{
					{
						ID:   "call-123",
						Type: "function",
						Function: FunctionCall{
							Name:      "test",
							Arguments: `{}`,
						},
					},
				},
				Model: "gpt-4",
			},
		},
		{
			name: "simple response",
			response: GenerateResponse{
				Content:      "Simple answer",
				FinishReason: "stop",
				Usage: TokenUsage{
					PromptTokens:     5,
					CompletionTokens: 10,
					TotalTokens:      15,
				},
				Model: "gpt-3.5-turbo",
			},
		},
		{
			name: "response with nil tool calls",
			response: GenerateResponse{
				Content:      "No tools",
				FinishReason: "stop",
				Usage:        TokenUsage{},
				ToolCalls:    nil,
				Model:        "gpt-4",
			},
		},
		{
			name: "response with empty tool calls",
			response: GenerateResponse{
				Content:      "No tools",
				FinishReason: "stop",
				Usage:        TokenUsage{},
				ToolCalls:    []ToolCall{},
				Model:        "gpt-4",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = tt.response.Content
			_ = tt.response.FinishReason
			_ = tt.response.Usage
			_ = tt.response.ToolCalls
			_ = tt.response.Model
		})
	}
}

// TestTokenUsage tests TokenUsage struct.
func TestTokenUsage(t *testing.T) {
	tests := []struct {
		name  string
		usage TokenUsage
	}{
		{
			name: "full usage",
			usage: TokenUsage{
				PromptTokens:     100,
				CompletionTokens: 200,
				TotalTokens:      300,
			},
		},
		{
			name: "zero usage",
			usage: TokenUsage{
				PromptTokens:     0,
				CompletionTokens: 0,
				TotalTokens:      0,
			},
		},
		{
			name: "prompt only",
			usage: TokenUsage{
				PromptTokens: 100,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = tt.usage.PromptTokens
			_ = tt.usage.CompletionTokens
			_ = tt.usage.TotalTokens
		})
	}
}

// TestEmbeddingRequest tests EmbeddingRequest struct.
func TestEmbeddingRequest(t *testing.T) {
	tests := []struct {
		name    string
		request EmbeddingRequest
	}{
		{
			name: "full request",
			request: EmbeddingRequest{
				Input: "Hello, world!",
				Model: "text-embedding-ada-002",
			},
		},
		{
			name: "minimal request",
			request: EmbeddingRequest{
				Input: "test",
			},
		},
		{
			name: "empty input",
			request: EmbeddingRequest{
				Input: "",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = tt.request.Input
			_ = tt.request.Model
		})
	}
}

// TestEmbeddingResponse tests EmbeddingResponse struct.
func TestEmbeddingResponse(t *testing.T) {
	tests := []struct {
		name     string
		response EmbeddingResponse
	}{
		{
			name: "full response",
			response: EmbeddingResponse{
				Embedding: []float32{0.1, 0.2, 0.3, 0.4, 0.5},
				Model:     "text-embedding-ada-002",
				Usage: TokenUsage{
					PromptTokens:     10,
					CompletionTokens: 0,
					TotalTokens:      10,
				},
			},
		},
		{
			name: "minimal response",
			response: EmbeddingResponse{
				Embedding: []float32{},
				Model:     "model-123",
			},
		},
		{
			name: "response with nil embedding",
			response: EmbeddingResponse{
				Embedding: nil,
				Model:     "model-456",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = tt.response.Embedding
			_ = tt.response.Model
			_ = tt.response.Usage
		})
	}
}

// TestLLMRepository tests that LLMRepository interface is properly defined.
func TestLLMRepository(t *testing.T) {
	var _ LLMRepository = (*mockLLMRepository)(nil)
}

// mockLLMRepository is a mock implementation of LLMRepository for testing.
type mockLLMRepository struct{}

func (m *mockLLMRepository) LogGeneration(ctx context.Context, request *GenerateRequest, response *GenerateResponse) error {
	return nil
}

func (m *mockLLMRepository) GetGenerationLog(ctx context.Context, logID string) (*GenerateRequest, *GenerateResponse, error) {
	return nil, nil, nil
}

// TestLLMService tests that LLMService interface is properly defined.
func TestLLMService(t *testing.T) {
	var _ LLMService = (*mockLLMService)(nil)
}

// mockLLMService is a mock implementation of LLMService for testing.
type mockLLMService struct{}

func (m *mockLLMService) Generate(ctx context.Context, request *GenerateRequest) (*GenerateResponse, error) {
	return nil, errors.New("not implemented")
}

func (m *mockLLMService) GenerateSimple(ctx context.Context, prompt string) (string, error) {
	return "", nil
}

func (m *mockLLMService) GenerateEmbedding(ctx context.Context, request *EmbeddingRequest) (*EmbeddingResponse, error) {
	return nil, errors.New("not implemented")
}

func (m *mockLLMService) GetConfig() *LLMConfig {
	return nil
}

func (m *mockLLMService) IsEnabled() bool {
	return false
}

func (m *mockLLMService) GetProvider() LLMProvider {
	return ""
}

func (m *mockLLMService) GetModel() string {
	return ""
}
