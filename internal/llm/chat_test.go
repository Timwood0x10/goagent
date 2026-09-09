package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	llmcore "github.com/Timwood0x10/ares/internal/llmcore"
)

func TestChat_OpenAI_WithTools(t *testing.T) {
	responseBody := `{
		"choices": [{
			"message": {
				"role": "assistant",
				"content": "",
				"tool_calls": [{
					"id": "call_abc123",
					"type": "function",
					"function": {
						"name": "get_weather",
						"arguments": "{\"city\":\"Seattle\"}"
					}
				}]
			},
			"finish_reason": "tool_calls"
		}]
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("expected path /chat/completions, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("expected Authorization header, got %s", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, responseBody)
	}))
	defer server.Close()

	client, err := NewClient(&Config{
		Provider: "openai",
		APIKey:   "test-key",
		BaseURL:  server.URL,
		Model:    "gpt-4",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	messages := []*llmcore.LLMMessage{
		{Role: "user", Content: "What is the weather in Seattle?"},
	}
	tools := []llmcore.Tool{
		{
			Type: "function",
			Function: llmcore.FunctionDefinition{
				Name:        "get_weather",
				Description: "Get weather for a city",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"city": map[string]interface{}{"type": "string"},
					},
				},
			},
		},
	}

	resp, err := client.Chat(context.Background(), messages, tools, nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].ID != "call_abc123" {
		t.Errorf("expected tool call ID call_abc123, got %s", resp.ToolCalls[0].ID)
	}
	if resp.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("expected function name get_weather, got %s", resp.ToolCalls[0].Function.Name)
	}
	if resp.ToolCalls[0].Function.Arguments != `{"city":"Seattle"}` {
		t.Errorf("expected arguments {\"city\":\"Seattle\"}, got %s", resp.ToolCalls[0].Function.Arguments)
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("expected finish_reason tool_calls, got %s", resp.FinishReason)
	}
}

func TestChat_OpenAI_TextOnly(t *testing.T) {
	responseBody := `{
		"choices": [{
			"message": {
				"role": "assistant",
				"content": "Hello! How can I help you?"
			},
			"finish_reason": "stop"
		}]
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, responseBody)
	}))
	defer server.Close()

	client, err := NewClient(&Config{
		Provider: "openai",
		APIKey:   "test-key",
		BaseURL:  server.URL,
		Model:    "gpt-4",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	resp, err := client.Chat(context.Background(), []*llmcore.LLMMessage{
		{Role: "user", Content: "Hello"},
	}, nil, nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	if resp.Content != "Hello! How can I help you?" {
		t.Errorf("expected content 'Hello! How can I help you?', got %s", resp.Content)
	}
	if len(resp.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(resp.ToolCalls))
	}
}

func TestChat_OpenRouter_WithTools(t *testing.T) {
	responseBody := `{
		"choices": [{
			"message": {
				"role": "assistant",
				"content": "",
				"tool_calls": [{
					"id": "call_xyz789",
					"type": "function",
					"function": {
						"name": "search",
						"arguments": "{\"query\":\"test\"}"
					}
				}]
			},
			"finish_reason": "tool_calls"
		}]
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Title") != "ARES" {
			t.Errorf("expected X-Title header ARES, got %s", r.Header.Get("X-Title"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, responseBody)
	}))
	defer server.Close()

	client, err := NewClient(&Config{
		Provider: "openrouter",
		APIKey:   "test-key",
		BaseURL:  server.URL,
		Model:    "openai/gpt-3.5-turbo",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	resp, err := client.Chat(context.Background(), []*llmcore.LLMMessage{
		{Role: "user", Content: "Search for test"},
	}, []llmcore.Tool{
		{Type: "function", Function: llmcore.FunctionDefinition{Name: "search", Description: "Search the web"}},
	}, nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	if resp.ToolCalls[0].Function.Name != "search" {
		t.Errorf("expected function name search, got %s", resp.ToolCalls[0].Function.Name)
	}
}

func TestChat_OpenAI_NoAPIKey(t *testing.T) {
	client, err := NewClient(&Config{
		Provider: "openai",
		APIKey:   "",
		BaseURL:  "http://localhost:11434",
		Model:    "gpt-4",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.Chat(context.Background(), []*llmcore.LLMMessage{
		{Role: "user", Content: "Hello"},
	}, nil, nil)
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
}

func TestChat_Anthropic_WithTools(t *testing.T) {
	responseBody := `{
		"content": [
			{
				"type": "text",
				"text": "Let me check that for you."
			},
			{
				"type": "tool_use",
				"id": "toolu_01A09q90qw90lq91734",
				"name": "get_weather",
				"input": {"city": "Seattle"}
			}
		],
		"stop_reason": "tool_use"
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			t.Errorf("expected path /messages, got %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("expected x-api-key header, got %s", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("expected anthropic-version header, got %s", r.Header.Get("anthropic-version"))
		}

		// Validate request body has Anthropic format.
		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]any
		if err := json.Unmarshal(body, &reqBody); err != nil {
			t.Fatalf("failed to parse request body: %v", err)
		}
		if _, ok := reqBody["tools"]; !ok {
			t.Error("expected tools field in request body")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, responseBody)
	}))
	defer server.Close()

	client, err := NewClient(&Config{
		Provider: "anthropic",
		APIKey:   "test-key",
		BaseURL:  server.URL,
		Model:    "claude-sonnet-4-20250514",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	resp, err := client.Chat(context.Background(), []*llmcore.LLMMessage{
		{Role: "user", Content: "What is the weather in Seattle?"},
	}, []llmcore.Tool{
		{
			Type: "function",
			Function: llmcore.FunctionDefinition{
				Name:        "get_weather",
				Description: "Get weather for a city",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"city": map[string]interface{}{"type": "string"},
					},
				},
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	if resp.Content != "Let me check that for you." {
		t.Errorf("expected content 'Let me check that for you.', got %s", resp.Content)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].ID != "toolu_01A09q90qw90lq91734" {
		t.Errorf("expected tool call ID toolu_01A09q90qw90lq91734, got %s", resp.ToolCalls[0].ID)
	}
	if resp.ToolCalls[0].Function.Name != "get_weather" {
		t.Errorf("expected function name get_weather, got %s", resp.ToolCalls[0].Function.Name)
	}
	// Anthropic returns input as JSON object; we marshal it to string.
	if resp.ToolCalls[0].Function.Arguments != `{"city":"Seattle"}` {
		t.Errorf("expected arguments {\"city\":\"Seattle\"}, got %s", resp.ToolCalls[0].Function.Arguments)
	}
	if resp.FinishReason != "tool_use" {
		t.Errorf("expected finish_reason tool_use, got %s", resp.FinishReason)
	}
}

func TestChat_Anthropic_SystemMessage(t *testing.T) {
	responseBody := `{
		"content": [{"type": "text", "text": "Understood."}],
		"stop_reason": "end_turn"
	}`

	var receivedSystem string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]any
		_ = json.Unmarshal(body, &reqBody)
		if sys, ok := reqBody["system"]; ok {
			receivedSystem = sys.(string)
		}
		// Verify system messages are NOT in the messages array.
		msgs := reqBody["messages"].([]any)
		for _, m := range msgs {
			msg := m.(map[string]any)
			if msg["role"] == "system" {
				t.Error("system message should not appear in messages array for Anthropic")
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, responseBody)
	}))
	defer server.Close()

	client, err := NewClient(&Config{
		Provider: "anthropic",
		APIKey:   "test-key",
		BaseURL:  server.URL,
		Model:    "claude-sonnet-4-20250514",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	resp, err := client.Chat(context.Background(), []*llmcore.LLMMessage{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "Hello"},
	}, nil, nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	if receivedSystem != "You are a helpful assistant." {
		t.Errorf("expected system prompt extracted to top-level, got %q", receivedSystem)
	}
	if resp.Content != "Understood." {
		t.Errorf("expected content 'Understood.', got %s", resp.Content)
	}
}

func TestChat_Anthropic_ToolResult(t *testing.T) {
	responseBody := `{
		"content": [{"type": "text", "text": "The weather is sunny."}],
		"stop_reason": "end_turn"
	}`

	var receivedMsgs []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]any
		_ = json.Unmarshal(body, &reqBody)
		msgs := reqBody["messages"].([]any)
		receivedMsgs = make([]map[string]any, len(msgs))
		for i, m := range msgs {
			receivedMsgs[i] = m.(map[string]any)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, responseBody)
	}))
	defer server.Close()

	client, err := NewClient(&Config{
		Provider: "anthropic",
		APIKey:   "test-key",
		BaseURL:  server.URL,
		Model:    "claude-sonnet-4-20250514",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	resp, err := client.Chat(context.Background(), []*llmcore.LLMMessage{
		{Role: "user", Content: "What is the weather?"},
		{Role: "assistant", Content: "", ToolCalls: []llmcore.ToolCall{
			{ID: "toolu_123", Type: "function", Function: llmcore.FunctionCall{Name: "get_weather", Arguments: `{"city":"Seattle"}`}},
		}},
		{Role: "tool", ToolCallID: "toolu_123", Content: "Sunny, 72F"},
	}, nil, nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	// Verify tool result is sent as user message with tool_result content block.
	if len(receivedMsgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(receivedMsgs))
	}
	toolResultMsg := receivedMsgs[2]
	if toolResultMsg["role"] != "user" {
		t.Errorf("tool result should be sent as role=user, got %v", toolResultMsg["role"])
	}
	contentArr, ok := toolResultMsg["content"].([]any)
	if !ok {
		t.Fatal("tool result content should be an array")
	}
	contentBlock := contentArr[0].(map[string]any)
	if contentBlock["type"] != "tool_result" {
		t.Errorf("expected type tool_result, got %v", contentBlock["type"])
	}
	if contentBlock["tool_use_id"] != "toolu_123" {
		t.Errorf("expected tool_use_id toolu_123, got %v", contentBlock["tool_use_id"])
	}

	if resp.Content != "The weather is sunny." {
		t.Errorf("expected content 'The weather is sunny.', got %s", resp.Content)
	}
}

func TestChat_Anthropic_NoAPIKey(t *testing.T) {
	client, err := NewClient(&Config{
		Provider: "anthropic",
		APIKey:   "",
		BaseURL:  "http://localhost:11434",
		Model:    "claude-sonnet-4-20250514",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.Chat(context.Background(), []*llmcore.LLMMessage{
		{Role: "user", Content: "Hello"},
	}, nil, nil)
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
}

func TestChat_Ollama_WithTools(t *testing.T) {
	responseBody := `{
		"message": {
			"role": "assistant",
			"content": "",
			"tool_calls": [{
				"id": "call_ollama_1",
				"type": "function",
				"function": {
					"name": "calculate",
					"arguments": "{\"expression\":\"2+2\"}"
				}
			}]
		}
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("expected path /api/chat, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, responseBody)
	}))
	defer server.Close()

	client, err := NewClient(&Config{
		Provider: "ollama",
		BaseURL:  server.URL,
		Model:    "llama3.2",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	resp, err := client.Chat(context.Background(), []*llmcore.LLMMessage{
		{Role: "user", Content: "What is 2+2?"},
	}, []llmcore.Tool{
		{Type: "function", Function: llmcore.FunctionDefinition{Name: "calculate", Description: "Calculate math"}},
	}, nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].ID != "call_ollama_1" {
		t.Errorf("expected tool call ID call_ollama_1, got %s", resp.ToolCalls[0].ID)
	}
}

func TestChat_UnsupportedProvider(t *testing.T) {
	client, err := NewClient(&Config{
		Provider: "unknown",
		BaseURL:  "http://localhost:11434",
		Model:    "test",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.Chat(context.Background(), []*llmcore.LLMMessage{
		{Role: "user", Content: "Hello"},
	}, nil, nil)
	if err == nil {
		t.Fatal("expected error for unsupported provider")
	}
}

func TestChat_EmptyMessages(t *testing.T) {
	client, err := NewClient(&Config{
		Provider: "ollama",
		BaseURL:  "http://localhost:11434",
		Model:    "llama3.2",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.Chat(context.Background(), nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for empty messages")
	}
}

func TestChat_OpenAI_ToolResultMessage(t *testing.T) {
	responseBody := `{
		"choices": [{
			"message": {
				"role": "assistant",
				"content": "The weather in Seattle is sunny and 72F."
			},
			"finish_reason": "stop"
		}]
	}`

	var receivedMsgs []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]any
		_ = json.Unmarshal(body, &reqBody)
		msgs := reqBody["messages"].([]any)
		receivedMsgs = make([]map[string]any, len(msgs))
		for i, m := range msgs {
			receivedMsgs[i] = m.(map[string]any)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, responseBody)
	}))
	defer server.Close()

	client, err := NewClient(&Config{
		Provider: "openai",
		APIKey:   "test-key",
		BaseURL:  server.URL,
		Model:    "gpt-4",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	resp, err := client.Chat(context.Background(), []*llmcore.LLMMessage{
		{Role: "user", Content: "What is the weather?"},
		{Role: "assistant", Content: "", ToolCalls: []llmcore.ToolCall{
			{ID: "call_123", Type: "function", Function: llmcore.FunctionCall{Name: "get_weather", Arguments: `{"city":"Seattle"}`}},
		}},
		{Role: "tool", ToolCallID: "call_123", Content: "Sunny, 72F"},
	}, nil, nil)
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	// Verify tool message is sent with tool_call_id.
	toolMsg := receivedMsgs[2]
	if toolMsg["role"] != "tool" {
		t.Errorf("expected role tool, got %v", toolMsg["role"])
	}
	if toolMsg["tool_call_id"] != "call_123" {
		t.Errorf("expected tool_call_id call_123, got %v", toolMsg["tool_call_id"])
	}

	// Verify assistant message includes tool_calls.
	assistantMsg := receivedMsgs[1]
	if assistantMsg["role"] != "assistant" {
		t.Errorf("expected role assistant, got %v", assistantMsg["role"])
	}

	if resp.Content != "The weather in Seattle is sunny and 72F." {
		t.Errorf("expected final content, got %s", resp.Content)
	}
}

func TestFailoverClient_Chat_AllProviders(t *testing.T) {
	responseBody := `{
		"choices": [{
			"message": {"role": "assistant", "content": "Hello from OpenAI"},
			"finish_reason": "stop"
		}]
	}`

	openAIServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, responseBody)
	}))
	defer openAIServer.Close()

	ollamaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"message": {"role": "assistant", "content": "Hello from Ollama"}}`)
	}))
	defer ollamaServer.Close()

	openAIClient, _ := NewClient(&Config{
		Provider: "openai",
		APIKey:   "test-key",
		BaseURL:  openAIServer.URL,
		Model:    "gpt-4",
	})
	ollamaClient, _ := NewClient(&Config{
		Provider: "ollama",
		BaseURL:  ollamaServer.URL,
		Model:    "llama3.2",
	})

	fc := &FailoverClient{
		clients:          []*Client{openAIClient, ollamaClient},
		timeout:          5 * 1e9,
		cooldownDuration: 30 * 1e9,
		cooldowns:        make(map[string]time.Time),
	}

	resp, err := fc.Chat(context.Background(), []*llmcore.LLMMessage{
		{Role: "user", Content: "Hello"},
	}, nil, nil)
	if err != nil {
		t.Fatalf("FailoverClient.Chat() error = %v", err)
	}

	// Should succeed with OpenAI (first client), not skip to Ollama.
	if resp.Content != "Hello from OpenAI" {
		t.Errorf("expected response from OpenAI, got %s", resp.Content)
	}
}
