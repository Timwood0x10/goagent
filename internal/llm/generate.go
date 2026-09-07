package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Timwood0x10/ares/internal/ares_callbacks"
	"github.com/Timwood0x10/ares/internal/errors"
)

// validatePrompt checks prompt constraints and records errors on failure.
// Returns nil on success, or an error describing the first violated constraint.
func (c *Client) validatePrompt(ctx context.Context, prompt string, start time.Time) error {
	if prompt == "" {
		err := errors.ErrInvalidArgument
		c.recordLLMCall(ctx, prompt, "", 0, start, err)
		return err
	}
	trimmed := bytes.TrimSpace([]byte(prompt))
	if len(trimmed) == 0 {
		err := errors.ErrInvalidArgument
		c.recordLLMCall(ctx, prompt, "", 0, start, err)
		return err
	}
	// Count runes, not bytes: CJK and other multi-byte characters would
	// otherwise be wrongly rejected against a character-based limit.
	if utf8.RuneCountInString(prompt) > c.promptMaxLength() {
		err := fmt.Errorf("prompt exceeds maximum length of %d characters", c.promptMaxLength())
		c.recordLLMCall(ctx, prompt, "", 0, start, err)
		return err
	}
	return nil
}

// promptMaxLength returns the configured max prompt length, or the default.
func (c *Client) promptMaxLength() int {
	if c.config != nil && c.config.MaxPromptLength > 0 {
		return c.config.MaxPromptLength
	}
	return maxPromptLength
}

// Generate sends a text generation request using the configured defaults.
// It delegates to GenerateWithParams with no overrides.
//
// Args:
//
//	ctx - operation context.
//	prompt - the prompt text.
//
// Returns:
//
//	generated text or error.
func (c *Client) Generate(ctx context.Context, prompt string) (string, error) {
	return c.generateWithParams(ctx, prompt, requestOverrides{})
}

// GenerateWithParams sends a text generation request with per-call overrides
// (temperature, max_tokens, top_k) drawn from an evolution strategy's
// Params map. A nil or empty params map uses the configured defaults.
//
// Args:
//
//	ctx - operation context.
//	prompt - the prompt text.
//	params - optional evolution strategy parameter overrides.
//
// Returns:
//
//	generated text or error.
func (c *Client) GenerateWithParams(ctx context.Context, prompt string, params map[string]any) (string, error) {
	return c.generateWithParams(ctx, prompt, extractOverrides(params))
}

// generateWithParams is the shared implementation behind Generate and
// GenerateWithParams. The overrides are applied per-provider below.
func (c *Client) generateWithParams(ctx context.Context, prompt string, o requestOverrides) (string, error) {
	start := time.Now()
	model := ""
	if c.config != nil {
		model = c.config.Model
	}

	c.emitCallback(&ares_callbacks.Context{
		Event: ares_callbacks.EventLLMStart,
		Model: model,
		Input: prompt,
	})

	if err := c.validatePrompt(ctx, prompt, start); err != nil {
		c.emitCallback(&ares_callbacks.Context{
			Event: ares_callbacks.EventLLMError,
			Model: model,
			Input: prompt,
			Error: err,
		})
		return "", err
	}

	var result string
	var err error

	// Apply rate limiter before making the API call.
	if c.limiter != nil {
		if waitErr := c.limiter.Wait(ctx); waitErr != nil {
			c.recordLLMCall(ctx, prompt, "", 0, start, waitErr)
			c.emitCallback(&ares_callbacks.Context{
				Event: ares_callbacks.EventLLMError,
				Model: model,
				Input: prompt,
				Error: waitErr,
			})
			return "", waitErr
		}
	}

	// Run the provider call under the retry policy and circuit breaker:
	// 429/5xx/transport errors are retried with exponential backoff, and the
	// breaker fails fast while a provider is degraded.
	result, err = withRetry(c, ctx, func() (string, error) {
		switch ProviderType(c.config.Provider) {
		case ProviderOpenAI, ProviderOpenRouter:
			return c.generateOpenRouter(ctx, prompt, o)
		case ProviderOllama:
			return c.generateOllama(ctx, prompt, o)
		case ProviderAnthropic:
			return c.generateAnthropic(ctx, prompt, o)
		default:
			return "", fmt.Errorf("unsupported provider: %s", c.config.Provider)
		}
	})

	duration := time.Since(start)
	c.recordLLMCall(ctx, prompt, result, 0, start, err)

	if err != nil {
		c.emitCallback(&ares_callbacks.Context{
			Event:    ares_callbacks.EventLLMError,
			Model:    model,
			Input:    prompt,
			Error:    err,
			Duration: duration,
		})
	} else {
		c.emitCallback(&ares_callbacks.Context{
			Event:    ares_callbacks.EventLLMEnd,
			Model:    model,
			Input:    prompt,
			Output:   result,
			Duration: duration,
		})
	}

	return result, err
}

// generateOpenRouter generates text using OpenRouter API.
func (c *Client) generateOpenRouter(ctx context.Context, prompt string, o requestOverrides) (string, error) {
	if c.config.APIKey == "" {
		return "", errors.New("API key is required for OpenRouter")
	}

	maxTokens := o.applyMaxTokens(c.config.MaxTokens)
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}

	requestBody := map[string]interface{}{
		"model": c.config.Model,
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": prompt,
			},
		},
		"temperature": o.applyTemperature(0.7),
		"max_tokens":  maxTokens,
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return "", errors.Wrap(err, "marshal request")
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.config.BaseURL+"/chat/completions", bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", errors.Wrap(err, "create request")
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	req.Header.Set("X-Title", "ARES")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", errors.Wrap(err, "send request")
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Error("failed to close response body: ", "error", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		if readErr != nil {
			log.Warn("llm: failed to read error response body", "error", readErr)
		}
		return "", &HTTPError{StatusCode: resp.StatusCode, Message: fmt.Sprintf("openrouter error (status %d): %s", resp.StatusCode, string(body))}
	}

	var response struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				Reasoning string `json:"reasoning"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return "", errors.Wrap(err, "decode response")
	}

	if len(response.Choices) == 0 {
		return "", errors.New("no choices in response")
	}

	result := response.Choices[0].Message.Content
	if result == "" {
		result = response.Choices[0].Message.Reasoning
	}

	return result, nil
}

// generateOllama generates text using Ollama API.
func (c *Client) generateOllama(ctx context.Context, prompt string, o requestOverrides) (string, error) {
	requestBody := map[string]interface{}{
		"model":  c.config.Model,
		"prompt": prompt,
		"stream": false,
		"options": map[string]interface{}{
			"temperature": o.applyTemperature(0.7),
			"num_predict": o.applyMaxTokens(defaultMaxTokens),
			"top_k":       o.applyTopK(defaultOllamaTopK),
		},
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return "", errors.Wrap(err, "marshal request")
	}

	baseURL := c.config.BaseURL
	if baseURL == "" {
		baseURL = DefaultOllamaBaseURL
	}

	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/api/generate", bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", errors.Wrap(err, "create request")
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", errors.Wrap(err, "send request")
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Error("failed to close response body: ", "error", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		if readErr != nil {
			log.Warn("llm: failed to read error response body", "error", readErr)
		}
		return "", &HTTPError{
			StatusCode: resp.StatusCode,
			Message:    fmt.Sprintf("unexpected status code: %d, body: %s", resp.StatusCode, string(body)),
		}
	}

	var response struct {
		Response string `json:"response"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return "", errors.Wrap(err, "decode response")
	}

	return response.Response, nil
}

// generateAnthropic generates text using Anthropic API.
// Anthropic uses a different API format: /v1/messages endpoint with required max_tokens.
func (c *Client) generateAnthropic(ctx context.Context, prompt string, o requestOverrides) (string, error) {
	if c.config.APIKey == "" {
		return "", errors.New("API key is required for Anthropic")
	}

	anthropicMaxTokens := o.applyMaxTokens(c.config.MaxTokens)
	if anthropicMaxTokens <= 0 {
		anthropicMaxTokens = 1024
	}

	requestBody := map[string]interface{}{
		"model": c.config.Model,
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": prompt,
			},
		},
		"max_tokens": anthropicMaxTokens,
	}
	// Anthropic rejects top_k=0 with an API 400; only send it when a
	// positive override is present.
	if topK := o.applyTopK(0); topK > 0 {
		requestBody["top_k"] = topK
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return "", errors.Wrap(err, "marshal anthropic request")
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.config.BaseURL+"/messages", bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", errors.Wrap(err, "create anthropic request")
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.config.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", errors.Wrap(err, "send anthropic request")
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Error("failed to close anthropic response body: ", "error", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		if readErr != nil {
			log.Warn("llm: failed to read anthropic error response body", "error", readErr)
		}
		return "", &HTTPError{StatusCode: resp.StatusCode, Message: fmt.Sprintf("anthropic error (status %d): %s", resp.StatusCode, string(body))}
	}

	var response struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return "", errors.Wrap(err, "decode anthropic response")
	}

	var result strings.Builder
	for _, block := range response.Content {
		if block.Type == "text" {
			result.WriteString(block.Text)
		}
	}

	return result.String(), nil
}
