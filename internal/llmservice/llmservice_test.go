package llmservice

import (
	"context"
	"errors"
	"testing"

	"github.com/Timwood0x10/ares/internal/llm"
	llmcore "github.com/Timwood0x10/ares/internal/llmcore"
)

// mockLLMClient implements LLMClient for testing.
type mockLLMClient struct {
	generateFunc       func(ctx context.Context, prompt string) (string, error)
	generateStreamFunc func(ctx context.Context, prompt string) (<-chan llm.StreamChunk, error)
	chatFunc           func(ctx context.Context, messages []*llmcore.LLMMessage, tools []llmcore.Tool) (*llmcore.GenerateResponse, error)
	isEnabledFunc      func() bool
	getProviderFunc    func() string
	getModelFunc       func() string
	closeFunc          func()
}

func (m *mockLLMClient) Generate(ctx context.Context, prompt string) (string, error) {
	if m.generateFunc != nil {
		return m.generateFunc(ctx, prompt)
	}
	return "", nil
}

func (m *mockLLMClient) GenerateStream(ctx context.Context, prompt string) (<-chan llm.StreamChunk, error) {
	if m.generateStreamFunc != nil {
		return m.generateStreamFunc(ctx, prompt)
	}
	ch := make(chan llm.StreamChunk)
	close(ch)
	return ch, nil
}

func (m *mockLLMClient) Chat(ctx context.Context, messages []*llmcore.LLMMessage, tools []llmcore.Tool, params map[string]any) (*llmcore.GenerateResponse, error) {
	if m.chatFunc != nil {
		return m.chatFunc(ctx, messages, tools)
	}
	return &llmcore.GenerateResponse{Content: "chat response"}, nil
}

func (m *mockLLMClient) IsEnabled() bool {
	if m.isEnabledFunc != nil {
		return m.isEnabledFunc()
	}
	return true
}

func (m *mockLLMClient) GetProvider() string {
	if m.getProviderFunc != nil {
		return m.getProviderFunc()
	}
	return "test-provider"
}

func (m *mockLLMClient) GetModel() string {
	if m.getModelFunc != nil {
		return m.getModelFunc()
	}
	return "test-model"
}

func (m *mockLLMClient) Close() {
	if m.closeFunc != nil {
		m.closeFunc()
	}
}

// mockRepository implements llmcore.LLMRepository for testing.
type mockRepository struct {
	logGenerationFunc func(ctx context.Context, request *llmcore.GenerateRequest, response *llmcore.GenerateResponse) error
}

func (m *mockRepository) LogGeneration(ctx context.Context, request *llmcore.GenerateRequest, response *llmcore.GenerateResponse) error {
	if m.logGenerationFunc != nil {
		return m.logGenerationFunc(ctx, request, response)
	}
	return nil
}

func (m *mockRepository) GetGenerationLog(ctx context.Context, logID string) (*llmcore.GenerateRequest, *llmcore.GenerateResponse, error) {
	return nil, nil, nil
}

// mockEmbedder implements the embedded interface used in GenerateEmbedding.
type mockEmbedder struct {
	embedFunc func(ctx context.Context, text string) ([]float64, error)
	modelFunc func() string
}

func (m *mockEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	if m.embedFunc != nil {
		return m.embedFunc(ctx, text)
	}
	return []float64{0.1, 0.2, 0.3}, nil
}

func (m *mockEmbedder) GetModel() string {
	if m.modelFunc != nil {
		return m.modelFunc()
	}
	return "embed-model"
}

func newTestService(client LLMClient) *Service {
	return &Service{
		client: client,
		config: &llmcore.BaseConfig{
			RequestTimeout: 0,
			MaxRetries:     0,
			RetryDelay:     0,
		},
		llmConfig: &llmcore.LLMConfig{
			Provider: llmcore.LLMProviderOpenAI,
			Model:    "gpt-4",
		},
	}
}

func newTestServiceWithRepo(client LLMClient, repo llmcore.LLMRepository) *Service {
	s := newTestService(client)
	s.repo = repo
	return s
}

// ----- NewService tests -----

func TestNewService_NilConfig(t *testing.T) {
	s, err := NewService(nil)
	if err == nil {
		t.Fatal("expected error for nil config")
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("expected ErrInvalidConfig, got %v", err)
	}
	if s != nil {
		t.Errorf("expected nil service, got %v", s)
	}
}

func TestNewService_NilLLMConfig(t *testing.T) {
	s, err := NewService(&Config{})
	if err == nil {
		t.Fatal("expected error for nil LLMConfig")
	}
	if !errors.Is(err, ErrInvalidLLMConfig) {
		t.Errorf("expected ErrInvalidLLMConfig, got %v", err)
	}
	if s != nil {
		t.Errorf("expected nil service, got %v", s)
	}
}

func TestNewService_NilBaseConfig(t *testing.T) {
	s, err := NewService(&Config{
		LLMConfig: &llmcore.LLMConfig{
			Provider: llmcore.LLMProviderOpenAI,
			Model:    "gpt-4",
			BaseURL:  "https://api.openai.com",
		},
		Fallbacks: nil,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s == nil {
		t.Fatal("expected non-nil service")
	}
	if s.config == nil {
		t.Fatal("expected default base config")
	}
	if s.config.RequestTimeout == 0 {
		t.Error("expected default RequestTimeout")
	}
	if s.config.MaxRetries != 3 {
		t.Errorf("expected MaxRetries 3, got %d", s.config.MaxRetries)
	}
}

// ----- Generate tests -----

func TestService_Generate_NilRequest(t *testing.T) {
	s := newTestService(&mockLLMClient{})
	_, err := s.Generate(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("expected ErrInvalidConfig, got %v", err)
	}
}

func TestService_Generate_EmptyMessages(t *testing.T) {
	s := newTestService(&mockLLMClient{})
	_, err := s.Generate(context.Background(), &llmcore.GenerateRequest{})
	if err == nil {
		t.Fatal("expected error for empty messages")
	}
	if !errors.Is(err, ErrInvalidMessages) {
		t.Errorf("expected ErrInvalidMessages, got %v", err)
	}
}

func TestService_Generate_Simple(t *testing.T) {
	mockClient := &mockLLMClient{
		generateFunc: func(ctx context.Context, prompt string) (string, error) {
			return "hello world", nil
		},
	}
	s := newTestService(mockClient)

	resp, err := s.Generate(context.Background(), &llmcore.GenerateRequest{
		Messages: []*llmcore.LLMMessage{
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "hello world" {
		t.Errorf("expected 'hello world', got %q", resp.Content)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("expected finish reason 'stop', got %q", resp.FinishReason)
	}
	if resp.Usage.PromptTokens == 0 {
		t.Error("expected non-zero prompt tokens")
	}
	if resp.Model != "gpt-4" {
		t.Errorf("expected model 'gpt-4', got %q", resp.Model)
	}
}

func TestService_Generate_WithTools(t *testing.T) {
	chatCalled := false
	mockClient := &mockLLMClient{
		chatFunc: func(ctx context.Context, messages []*llmcore.LLMMessage, tools []llmcore.Tool) (*llmcore.GenerateResponse, error) {
			chatCalled = true
			if len(tools) == 0 {
				t.Error("expected tools to be passed")
			}
			return &llmcore.GenerateResponse{
				Content:      "tool result",
				FinishReason: "stop",
			}, nil
		},
	}
	s := newTestService(mockClient)

	resp, err := s.Generate(context.Background(), &llmcore.GenerateRequest{
		Messages: []*llmcore.LLMMessage{
			{Role: "user", Content: "use a tool"},
		},
		Tools: []llmcore.Tool{
			{
				Type: "function",
				Function: llmcore.FunctionDefinition{
					Name: "test_tool",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !chatCalled {
		t.Error("expected Chat to be called when tools are present")
	}
	if resp.Content != "tool result" {
		t.Errorf("expected 'tool result', got %q", resp.Content)
	}
}

func TestService_Generate_WithToolMessages(t *testing.T) {
	chatCalled := false
	mockClient := &mockLLMClient{
		chatFunc: func(ctx context.Context, messages []*llmcore.LLMMessage, tools []llmcore.Tool) (*llmcore.GenerateResponse, error) {
			chatCalled = true
			return &llmcore.GenerateResponse{Content: "tool call result"}, nil
		},
	}
	s := newTestService(mockClient)

	resp, err := s.Generate(context.Background(), &llmcore.GenerateRequest{
		Messages: []*llmcore.LLMMessage{
			{Role: "assistant", Content: "", ToolCalls: []llmcore.ToolCall{{ID: "call_1"}}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !chatCalled {
		t.Error("expected Chat to be called when messages have tool calls")
	}
	_ = resp

	chatCalled = false
	_, _ = s.Generate(context.Background(), &llmcore.GenerateRequest{
		Messages: []*llmcore.LLMMessage{
			{Role: "tool", Content: "result", ToolCallID: "call_1"},
		},
	})
	if !chatCalled {
		t.Error("expected Chat to be called when messages have ToolCallID")
	}
}

func TestService_Generate_ClientError(t *testing.T) {
	expectErr := errors.New("api failure")
	mockClient := &mockLLMClient{
		generateFunc: func(ctx context.Context, prompt string) (string, error) {
			return "", expectErr
		},
	}
	s := newTestService(mockClient)

	_, err := s.Generate(context.Background(), &llmcore.GenerateRequest{
		Messages: []*llmcore.LLMMessage{
			{Role: "user", Content: "hi"},
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestService_Generate_RepoLogError(t *testing.T) {
	repoCalled := false
	mockClient := &mockLLMClient{
		generateFunc: func(ctx context.Context, prompt string) (string, error) {
			return "hello", nil
		},
	}
	repo := &mockRepository{
		logGenerationFunc: func(ctx context.Context, request *llmcore.GenerateRequest, response *llmcore.GenerateResponse) error {
			repoCalled = true
			return errors.New("log failure")
		},
	}
	s := newTestServiceWithRepo(mockClient, repo)

	resp, err := s.Generate(context.Background(), &llmcore.GenerateRequest{
		Messages: []*llmcore.LLMMessage{
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !repoCalled {
		t.Error("expected repo.LogGeneration to be called")
	}
	if resp.Content != "hello" {
		t.Errorf("expected 'hello', got %q", resp.Content)
	}
}

func TestService_Generate_ChatFinishReason(t *testing.T) {
	// When Chat returns a response with empty FinishReason and no ToolCalls
	mockClient := &mockLLMClient{
		chatFunc: func(ctx context.Context, messages []*llmcore.LLMMessage, tools []llmcore.Tool) (*llmcore.GenerateResponse, error) {
			return &llmcore.GenerateResponse{
				Content:      "no tool calls",
				FinishReason: "",
				ToolCalls:    nil,
			}, nil
		},
	}
	s := newTestService(mockClient)

	resp, err := s.Generate(context.Background(), &llmcore.GenerateRequest{
		Messages: []*llmcore.LLMMessage{
			{Role: "user", Content: "hi"},
		},
		Tools: []llmcore.Tool{
			{Type: "function", Function: llmcore.FunctionDefinition{Name: "test"}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("expected finish reason 'stop' when no tool calls, got %q", resp.FinishReason)
	}
}

func TestService_Generate_ChatFinishReason_ToolCalls(t *testing.T) {
	mockClient := &mockLLMClient{
		chatFunc: func(ctx context.Context, messages []*llmcore.LLMMessage, tools []llmcore.Tool) (*llmcore.GenerateResponse, error) {
			return &llmcore.GenerateResponse{
				Content:      "",
				FinishReason: "",
				ToolCalls:    []llmcore.ToolCall{{ID: "call_1"}},
			}, nil
		},
	}
	s := newTestService(mockClient)

	resp, err := s.Generate(context.Background(), &llmcore.GenerateRequest{
		Messages: []*llmcore.LLMMessage{
			{Role: "user", Content: "call a tool"},
		},
		Tools: []llmcore.Tool{
			{Type: "function", Function: llmcore.FunctionDefinition{Name: "test"}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("expected finish reason 'tool_calls', got %q", resp.FinishReason)
	}
}

// ----- GenerateSimple tests -----

func TestService_GenerateSimple_EmptyPrompt(t *testing.T) {
	s := newTestService(&mockLLMClient{})
	_, err := s.GenerateSimple(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty prompt")
	}
	if !errors.Is(err, ErrInvalidPrompt) {
		t.Errorf("expected ErrInvalidPrompt, got %v", err)
	}
}

func TestService_GenerateSimple_Success(t *testing.T) {
	mockClient := &mockLLMClient{
		generateFunc: func(ctx context.Context, prompt string) (string, error) {
			if prompt != "hello" {
				t.Errorf("unexpected prompt %q", prompt)
			}
			return "world", nil
		},
	}
	s := newTestService(mockClient)

	result, err := s.GenerateSimple(context.Background(), "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "world" {
		t.Errorf("expected 'world', got %q", result)
	}
}

func TestService_GenerateSimple_ClientError(t *testing.T) {
	mockClient := &mockLLMClient{
		generateFunc: func(ctx context.Context, prompt string) (string, error) {
			return "", errors.New("api error")
		},
	}
	s := newTestService(mockClient)

	_, err := s.GenerateSimple(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error")
	}
}

// ----- GenerateEmbedding tests -----

func TestService_GenerateEmbedding_NilRequest(t *testing.T) {
	s := newTestService(&mockLLMClient{})
	_, err := s.GenerateEmbedding(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
	if !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("expected ErrInvalidConfig, got %v", err)
	}
}

func TestService_GenerateEmbedding_EmptyInput(t *testing.T) {
	s := newTestService(&mockLLMClient{})
	_, err := s.GenerateEmbedding(context.Background(), &llmcore.EmbeddingRequest{Input: ""})
	if err == nil {
		t.Fatal("expected error for empty input")
	}
	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("expected ErrInvalidInput, got %v", err)
	}
}

func TestService_GenerateEmbedding_NoClient(t *testing.T) {
	s := newTestService(&mockLLMClient{})
	_, err := s.GenerateEmbedding(context.Background(), &llmcore.EmbeddingRequest{Input: "test"})
	if err == nil {
		t.Fatal("expected error when no embedding client configured")
	}
	if !errors.Is(err, ErrLLMNotAvailable) {
		t.Errorf("expected ErrLLMNotAvailable, got %v", err)
	}
}

func TestService_GenerateEmbedding_UnsupportedClient(t *testing.T) {
	s := newTestService(&mockLLMClient{})
	s.embeddingClient = struct{}{}
	_, err := s.GenerateEmbedding(context.Background(), &llmcore.EmbeddingRequest{Input: "test"})
	if err == nil {
		t.Fatal("expected error for unsupported embedding client")
	}
	if !errors.Is(err, ErrEmbeddingFailed) {
		t.Errorf("expected ErrEmbeddingFailed, got %v", err)
	}
}

func TestService_GenerateEmbedding_Success(t *testing.T) {
	s := newTestService(&mockLLMClient{})
	s.embeddingClient = &mockEmbedder{
		embedFunc: func(ctx context.Context, text string) ([]float64, error) {
			return []float64{1.0, 2.0, 3.0}, nil
		},
	}

	resp, err := s.GenerateEmbedding(context.Background(), &llmcore.EmbeddingRequest{Input: "test text"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp == nil {
		t.Fatal("expected non-nil response")
	}
	if len(resp.Embedding) != 3 {
		t.Errorf("expected 3 embedding values, got %d", len(resp.Embedding))
	}
	if resp.Embedding[0] != 1.0 || resp.Embedding[1] != 2.0 || resp.Embedding[2] != 3.0 {
		t.Errorf("unexpected embedding values: %v", resp.Embedding)
	}
	if resp.Model != "embed-model" {
		t.Errorf("expected model 'embed-model', got %q", resp.Model)
	}
	if resp.Usage.PromptTokens == 0 {
		t.Error("expected non-zero prompt tokens")
	}
}

func TestService_GenerateEmbedding_WithoutModelGetter(t *testing.T) {
	e := &partialEmbedder{}

	s := newTestService(&mockLLMClient{})
	s.embeddingClient = e

	resp, err := s.GenerateEmbedding(context.Background(), &llmcore.EmbeddingRequest{Input: "text"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Model != "" {
		t.Errorf("expected empty model when GetModel not implemented, got %q", resp.Model)
	}
}

type partialEmbedder struct{}

func (p *partialEmbedder) Embed(ctx context.Context, text string) ([]float64, error) {
	return []float64{0.5, 0.6}, nil
}

func TestService_GenerateEmbedding_ClientError(t *testing.T) {
	s := newTestService(&mockLLMClient{})
	s.embeddingClient = &mockEmbedder{
		embedFunc: func(ctx context.Context, text string) ([]float64, error) {
			return nil, errors.New("embedding api error")
		},
	}

	_, err := s.GenerateEmbedding(context.Background(), &llmcore.EmbeddingRequest{Input: "test"})
	if err == nil {
		t.Fatal("expected error from embedding client")
	}
	if !errors.Is(err, ErrEmbeddingFailed) {
		t.Errorf("expected ErrEmbeddingFailed, got %v", err)
	}
}

// ----- Accessor tests -----

func TestService_GetConfig(t *testing.T) {
	llmCfg := &llmcore.LLMConfig{
		Provider: llmcore.LLMProviderAnthropic,
		Model:    "claude-3",
	}
	s := &Service{llmConfig: llmCfg}
	got := s.GetConfig()
	if got != llmCfg {
		t.Error("expected same config pointer")
	}
}

func TestService_IsEnabled(t *testing.T) {
	mockClient := &mockLLMClient{
		isEnabledFunc: func() bool { return true },
	}
	s := newTestService(mockClient)
	if !s.IsEnabled() {
		t.Error("expected enabled")
	}

	mockClient2 := &mockLLMClient{
		isEnabledFunc: func() bool { return false },
	}
	s2 := newTestService(mockClient2)
	if s2.IsEnabled() {
		t.Error("expected disabled")
	}
}

func TestService_GetProvider(t *testing.T) {
	s := &Service{llmConfig: &llmcore.LLMConfig{Provider: llmcore.LLMProviderOllama}}
	if s.GetProvider() != llmcore.LLMProviderOllama {
		t.Errorf("expected ollama, got %q", s.GetProvider())
	}

	s2 := &Service{}
	if s2.GetProvider() != "" {
		t.Errorf("expected empty provider, got %q", s2.GetProvider())
	}
}

func TestService_GetModel(t *testing.T) {
	s := &Service{llmConfig: &llmcore.LLMConfig{Model: "llama3"}}
	if s.GetModel() != "llama3" {
		t.Errorf("expected llama3, got %q", s.GetModel())
	}

	s2 := &Service{}
	if s2.GetModel() != "" {
		t.Errorf("expected empty model, got %q", s2.GetModel())
	}
}

func TestService_Close(t *testing.T) {
	closed := false
	mockClient := &mockLLMClient{
		closeFunc: func() { closed = true },
	}
	s := newTestService(mockClient)
	s.Close()
	if !closed {
		t.Error("expected client.Close() to be called")
	}

	// Should not panic when client is nil
	s2 := &Service{}
	s2.Close()
}

// ----- Internal function tests -----

func TestService_buildPrompt(t *testing.T) {
	s := &Service{}
	messages := []*llmcore.LLMMessage{
		{Role: "system", Content: "You are a helper"},
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there"},
	}
	result := s.buildPrompt(messages)
	expected := "[system]: You are a helper\n[user]: Hello\n[assistant]: Hi there\n"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestService_hasToolMessages(t *testing.T) {
	s := &Service{}

	// No tool data
	noTool := []*llmcore.LLMMessage{
		{Role: "user", Content: "hello"},
	}
	if s.hasToolMessages(noTool) {
		t.Error("expected false for messages without tool data")
	}

	// ToolCalls present
	withToolCalls := []*llmcore.LLMMessage{
		{Role: "assistant", Content: "", ToolCalls: []llmcore.ToolCall{{ID: "call_1"}}},
	}
	if !s.hasToolMessages(withToolCalls) {
		t.Error("expected true for messages with ToolCalls")
	}

	// ToolCallID present
	withToolCallID := []*llmcore.LLMMessage{
		{Role: "tool", Content: "result", ToolCallID: "call_1"},
	}
	if !s.hasToolMessages(withToolCallID) {
		t.Error("expected true for messages with ToolCallID")
	}

	// Empty messages slice
	if s.hasToolMessages(nil) {
		t.Error("expected false for nil slice")
	}
}

func TestService_getModel(t *testing.T) {
	s := &Service{llmConfig: &llmcore.LLMConfig{Model: "gpt-4o"}}
	if model := s.getModel(); model != "gpt-4o" {
		t.Errorf("expected gpt-4o, got %q", model)
	}

	s2 := &Service{llmConfig: &llmcore.LLMConfig{Model: ""}}
	if model := s2.getModel(); model != "default" {
		t.Errorf("expected 'default', got %q", model)
	}

	s3 := &Service{}
	if model := s3.getModel(); model != "default" {
		t.Errorf("expected 'default', got %q", model)
	}
}

func TestService_calculateTokens(t *testing.T) {
	s := &Service{}

	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"a", 1},
		{"abc", 1},
		{"abcd", 1},
		{"abcde", 1},
		{"abcdefgh", 2},
		{"hello world, this is a test", 6}, // 27 runes / 4 = 6
		{"你好世界", 1},                        // 4 runes / 4 = 1
		{"你好世界!", 1},                       // 5 runes / 4 = 1
	}
	for _, tc := range tests {
		got := s.calculateTokens(tc.input)
		if got != tc.want {
			t.Errorf("calculateTokens(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

// ----- Error sentinel tests -----

func TestErrorSentinels(t *testing.T) {
	if ErrInvalidConfig.Error() != "invalid configuration" {
		t.Errorf("unexpected message: %q", ErrInvalidConfig.Error())
	}
	if ErrInvalidLLMConfig.Error() != "invalid LLM configuration" {
		t.Errorf("unexpected message: %q", ErrInvalidLLMConfig.Error())
	}
	if ErrInvalidMessages.Error() != "invalid messages" {
		t.Errorf("unexpected message: %q", ErrInvalidMessages.Error())
	}
	if ErrInvalidPrompt.Error() != "invalid prompt" {
		t.Errorf("unexpected message: %q", ErrInvalidPrompt.Error())
	}
	if ErrInvalidInput.Error() != "invalid input" {
		t.Errorf("unexpected message: %q", ErrInvalidInput.Error())
	}
	if ErrGenerationFailed.Error() != "generation failed" {
		t.Errorf("unexpected message: %q", ErrGenerationFailed.Error())
	}
	if ErrEmbeddingFailed.Error() != "embedding generation failed" {
		t.Errorf("unexpected message: %q", ErrEmbeddingFailed.Error())
	}
	if ErrLLMNotAvailable.Error() != "LLM service not available" {
		t.Errorf("unexpected message: %q", ErrLLMNotAvailable.Error())
	}
}
