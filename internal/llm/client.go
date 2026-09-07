// Package llm provides LLM client functionality for various providers.
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	goerrors "errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_callbacks"
	"github.com/Timwood0x10/ares/internal/ares_ratelimit"
	"github.com/Timwood0x10/ares/internal/ares_security"
	"github.com/Timwood0x10/ares/internal/errors"
	"github.com/Timwood0x10/ares/internal/runtime/observability"
)

// Default configuration constants for LLM client.
const (
	defaultTimeoutSeconds = 60
	maxPromptLength       = 8192
	defaultStreamBuffer   = 64
	defaultMaxTokens      = 4096
)

// HTTPError represents an HTTP request error.
type HTTPError struct {
	StatusCode int
	Message    string
}

// Error returns the error message.
func (e *HTTPError) Error() string {
	return e.Message
}

// isRateLimitError returns true if the error indicates rate limiting (HTTP 429).
// Uses errors.As to unwrap error chains; falls back to message inspection for
// providers that return plain fmt.Errorf.
func isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	// Use errors.As so wrapped *HTTPError is still detected.
	var httpErr *HTTPError
	if goerrors.As(err, &httpErr) {
		return httpErr.StatusCode == http.StatusTooManyRequests
	}
	// Fallback: check error message for edge cases.
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "429") || strings.Contains(msg, "rate limit") || strings.Contains(msg, "rate_limit")
}

// ProviderType represents the LLM provider type.
type ProviderType string

const (
	ProviderOpenAI     ProviderType = "openai"
	ProviderOpenRouter ProviderType = "openrouter"
	ProviderOllama     ProviderType = "ollama"
	ProviderAnthropic  ProviderType = "anthropic"

	// DefaultOllamaBaseURL is the default base URL for Ollama provider.
	DefaultOllamaBaseURL = "http://localhost:11434"

	// DefaultOpenRouterBaseURL is the default base URL for OpenRouter provider.
	DefaultOpenRouterBaseURL = "https://openrouter.ai/api/v1"

	// DefaultOllamaModel is the default model for Ollama provider.
	DefaultOllamaModel = "llama3.2"

	// DefaultOpenRouterModel is the default model for OpenRouter provider.
	DefaultOpenRouterModel = "openai/gpt-3.5-turbo"
)

// Config holds LLM client configuration.
type Config struct {
	Provider        string            `yaml:"provider"`
	APIKey          string            `yaml:"api_key"`
	BaseURL         string            `yaml:"base_url"`
	Model           string            `yaml:"model"`
	Timeout         int               `yaml:"timeout"`
	MaxTokens       int               `yaml:"max_tokens"`        // Maximum tokens in response (0 = use defaultMaxTokens)
	MaxPromptLength int               `yaml:"max_prompt_length"` // Maximum prompt characters (0 = use maxPromptLength default)
	Extra           map[string]string `yaml:"extra"`
}

// Client represents an LLM client that supports multiple providers.
type Client struct {
	config         *Config
	httpClient     *http.Client
	streamClient   *http.Client // No Timeout — streaming uses context for cancellation.
	tracer         observability.Tracer
	ares_callbacks ares_callbacks.Emitter   // Optional: emits lifecycle events for LLM calls.
	limiter        ares_ratelimit.Limiter   // Optional: rate limiter for API calls.
	sanitizer      *ares_security.Sanitizer // Optional: masks secrets in recorded prompts/responses.
	retryPolicy    RetryPolicy              // Retry policy for transient failures (default: 3 attempts).
	circuit        *CircuitBreaker          // Guards against sustained provider failures (default: enabled).
	closeOnce      sync.Once                // Ensures Close() is idempotent and safe for concurrent calls.
}

// Option configures a Client instance during construction.
type Option func(*Client)

// WithCallbacks sets the callback emitter on the LLM client.
// When set, Generate and GenerateStream will emit lifecycle events.
func WithCallbacks(emitter ares_callbacks.Emitter) Option {
	return func(c *Client) {
		c.ares_callbacks = emitter
	}
}

// WithRateLimiter sets an optional rate limiter on the LLM client.
// When set, Generate and GenerateStream will call limiter.Wait(ctx)
// before making each API request, preventing the caller from exceeding
// the configured rate limit (e.g., token bucket or sliding window).
//
// Args:
//
//	limiter - the rate limiter to use (use ares_ratelimit.NewTokenBucketLimiter, etc.).
//
// Returns:
//
//	Option - the configuration function.
func WithRateLimiter(limiter ares_ratelimit.Limiter) Option {
	return func(c *Client) {
		c.limiter = limiter
	}
}

// WithRetryPolicy overrides the default retry policy (3 attempts, exponential
// backoff). Pass DefaultRetryPolicy() with MaxAttempts=1 to disable retries.
func WithRetryPolicy(p RetryPolicy) Option {
	return func(c *Client) {
		c.retryPolicy = p
	}
}

// WithCircuitBreaker replaces the default circuit breaker. Pass nil to disable
// circuit breaking entirely.
func WithCircuitBreaker(cb *CircuitBreaker) Option {
	return func(c *Client) {
		c.circuit = cb
	}
}

// WithSanitizer sets an optional sanitizer that masks sensitive data
// (API keys, tokens, passwords, etc.) in prompts and responses before they
// are recorded by the tracer/event store. This prevents secret leakage into
// logs and traces without altering the live request sent to the provider.
func WithSanitizer(s *ares_security.Sanitizer) Option {
	return func(c *Client) {
		c.sanitizer = s
	}
}

// Close releases idle HTTP connections held by the client.
// It is safe to call Close multiple times; subsequent calls are no-ops.
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		c.httpClient.CloseIdleConnections()
		c.streamClient.CloseIdleConnections()
	})
}

// SetTracer sets an optional observability tracer on the client.
// When set, Generate and GenerateStream will record LLM call spans.
func (c *Client) SetTracer(t observability.Tracer) {
	c.tracer = t
}

// NewClient creates a new LLM client with the given configuration.
// Args:
//   - config: LLM client configuration, must not be nil. Model, Provider, and
//     BaseURL are required fields (except Ollama which provides defaults).
//   - opts: optional client configuration functions.
//
// Returns:
//   - *Client: the configured LLM client.
//   - error: errors.ErrInvalidArgument if config is nil or required fields
//     are missing, or an error describing which field is invalid.
func NewClient(config *Config, opts ...Option) (*Client, error) {
	if config == nil {
		return nil, fmt.Errorf("%w: config must not be nil", errors.ErrInvalidArgument)
	}
	if config.Model == "" {
		return nil, fmt.Errorf("%w: model is required", errors.ErrInvalidArgument)
	}
	if config.Provider == "" {
		return nil, fmt.Errorf("%w: provider is required", errors.ErrInvalidArgument)
	}
	// BaseURL is required for non-Ollama providers; Ollama has a default.
	if config.BaseURL == "" && ProviderType(config.Provider) != ProviderOllama {
		return nil, fmt.Errorf("%w: base_url is required for provider %s", errors.ErrInvalidArgument, config.Provider)
	}

	if config.Timeout <= 0 {
		config.Timeout = defaultTimeoutSeconds
	}

	c := &Client{
		config: config,
		httpClient: &http.Client{
			Timeout: time.Duration(config.Timeout) * time.Second,
		},
		// streamClient has no Timeout: for streaming, timeout is controlled
		// entirely via context so that the goroutine reading the body is
		// properly cancelled when the context expires.
		streamClient: &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		retryPolicy: DefaultRetryPolicy(),
		circuit: NewCircuitBreaker(
			defaultCircuitFailureThreshold,
			defaultCircuitOpenTimeout,
		),
	}

	for _, opt := range opts {
		opt(c)
	}

	return c, nil
}

// recordLLMCall records an LLM call via the tracer if set.
func (c *Client) recordLLMCall(ctx context.Context, prompt, response string, tokens int, start time.Time, err error) {
	// Scrub secrets from the recorded copy only — the live request to the
	// provider is never modified, so functionality is preserved while logs
	// and traces no longer leak credentials.
	if c.sanitizer != nil {
		prompt = c.sanitizer.Sanitize(prompt)
		response = c.sanitizer.Sanitize(response)
	}
	if c.tracer == nil {
		return
	}
	model := ""
	if c.config != nil {
		model = c.config.Model
	}
	c.tracer.RecordLLMCall(ctx, &observability.LLMCall{
		TraceID:    c.tracer.GetTraceID(ctx),
		Model:      model,
		Prompt:     prompt,
		Response:   response,
		TokensUsed: tokens,
		Duration:   time.Since(start),
		Error:      err,
	})
}

// emitCallback emits a lifecycle event via the callback emitter if set.
func (c *Client) emitCallback(ctx *ares_callbacks.Context) {
	if c.ares_callbacks == nil {
		return
	}
	c.ares_callbacks.Emit(ctx)
}

// IsEnabled checks if the LLM client is properly configured.
func (c *Client) IsEnabled() bool {
	if c == nil || c.config == nil {
		return false
	}

	switch ProviderType(c.config.Provider) {
	case ProviderOpenAI, ProviderOpenRouter, ProviderAnthropic:
		return c.config.APIKey != ""
	case ProviderOllama:
		return true // Ollama doesn't require API key
	default:
		return false
	}
}

// GetProvider returns the current provider type.
func (c *Client) GetProvider() string {
	if c.config != nil {
		return c.config.Provider
	}
	return ""
}

// GetModel returns the current model name.
func (c *Client) GetModel() string {
	if c.config != nil {
		return c.config.Model
	}
	return ""
}

// D13: NewClientFromEnv removed (0 production calls — use NewClient with Config directly).
// StreamChunk represents a single chunk in a streaming response.
type StreamChunk struct {
	Content string
	Done    bool
	Err     error
}

// GenerateStream sends a streaming text generation request.
// Returns a channel of StreamChunk that is closed when streaming completes.
func (c *Client) GenerateStream(ctx context.Context, prompt string) (<-chan StreamChunk, error) {
	start := time.Now()
	model := ""
	if c.config != nil {
		model = c.config.Model
	}

	if err := c.validatePrompt(ctx, prompt, start); err != nil {
		c.emitCallback(&ares_callbacks.Context{
			Event: ares_callbacks.EventLLMError,
			Model: model,
			Input: prompt,
			Error: err,
		})
		return nil, err
	}

	var rawCh <-chan StreamChunk
	var err error

	// Derive a cancellable stream context so the wrapper goroutine can stop
	// the underlying provider stream (and free its HTTP connection) when the
	// caller abandons the returned channel without cancelling ctx (M6).
	streamCtx, cancelStream := context.WithCancel(ctx)

	// Apply rate limiter before making the API call.
	if c.limiter != nil {
		if waitErr := c.limiter.Wait(ctx); waitErr != nil {
			cancelStream()
			c.recordLLMCall(ctx, prompt, "", 0, start, waitErr)
			c.emitCallback(&ares_callbacks.Context{
				Event: ares_callbacks.EventLLMError,
				Model: model,
				Input: prompt,
				Error: waitErr,
			})
			return nil, waitErr
		}
	}

	// Run the connection phase under the retry policy and circuit breaker,
	// exactly like Generate/Chat: 429/5xx/transport errors are retried with
	// exponential backoff, and the breaker fails fast while a provider is
	// degraded. Only the connection phase is retried — once the stream is
	// open, mid-stream failures surface as chunk.Err on the channel and
	// cannot be replayed.
	rawCh, err = withRetry(c, streamCtx, func() (<-chan StreamChunk, error) {
		switch ProviderType(c.config.Provider) {
		case ProviderOpenAI, ProviderOpenRouter:
			return c.streamOpenRouter(streamCtx, prompt)
		case ProviderOllama:
			return c.streamOllama(streamCtx, prompt)
		case ProviderAnthropic:
			return c.streamAnthropic(streamCtx, prompt)
		default:
			return nil, fmt.Errorf("unsupported provider: %s", c.config.Provider)
		}
	})

	if err != nil {
		cancelStream()
		c.recordLLMCall(ctx, prompt, "", 0, start, err)
		c.emitCallback(&ares_callbacks.Context{
			Event: ares_callbacks.EventLLMError,
			Model: model,
			Input: prompt,
			Error: err,
		})
		return nil, err
	}

	// Emit LLM start event here: all validation passed, streaming will actually begin.
	c.emitCallback(&ares_callbacks.Context{
		Event: ares_callbacks.EventLLMStart,
		Model: model,
		Input: prompt,
	})

	// Wrap the channel to record the LLM call when streaming completes.
	ch := make(chan StreamChunk, defaultStreamBuffer)
	go func() {
		defer close(ch)
		defer cancelStream()
		var builder strings.Builder
		var streamErr error
		sawDone := false
		for chunk := range rawCh {
			if chunk.Content != "" {
				builder.WriteString(chunk.Content)
			}
			if chunk.Err != nil {
				streamErr = chunk.Err
			}
			if chunk.Done {
				sawDone = true
			}
			// Forward every content/done/err chunk with a BLOCKING send that
			// escapes on ctx cancellation. The previous non-blocking send with
			// a `default: return` silently discarded chunks once the consumer
			// fell behind, closing ch without Done or Err — truncated answers
			// were indistinguishable from complete ones. A caller that stops
			// reading MUST cancel ctx (standard streaming contract); ctx is
			// derived from the caller's request, so cancellation unwinds this
			// goroutine and releases the underlying HTTP connection.
			if chunk.Content != "" || chunk.Done || chunk.Err != nil {
				select {
				case ch <- chunk:
				case <-ctx.Done():
					return
				}
			}
			if chunk.Done {
				break
			}
		}
		// Guarantee the terminal marker: the provider goroutines close their
		// channel on normal completion WITHOUT emitting {Done: true}, so a
		// consumer watching for Done (instead of channel close) would hang
		// forever waiting for an end that never came.
		if !sawDone {
			select {
			case ch <- StreamChunk{Done: true, Err: streamErr}:
			case <-ctx.Done():
				return
			}
		}
		fullResponse := builder.String()
		duration := time.Since(start)
		c.recordLLMCall(ctx, prompt, fullResponse, 0, start, streamErr)

		// Emit LLM end or error event for streaming.
		if streamErr != nil {
			c.emitCallback(&ares_callbacks.Context{
				Event:    ares_callbacks.EventLLMError,
				Model:    model,
				Input:    prompt,
				Error:    streamErr,
				Duration: duration,
			})
		} else {
			c.emitCallback(&ares_callbacks.Context{
				Event:    ares_callbacks.EventLLMEnd,
				Model:    model,
				Input:    prompt,
				Output:   fullResponse,
				Duration: duration,
			})
		}
	}()
	return ch, nil
}

// streamOllama streams text generation using Ollama API.
func (c *Client) streamOllama(ctx context.Context, prompt string) (<-chan StreamChunk, error) {
	requestBody := map[string]interface{}{
		"model":  c.config.Model,
		"prompt": prompt,
		"stream": true,
		"options": map[string]interface{}{
			"temperature": 0.7,
			"num_predict": defaultMaxTokens,
		},
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return nil, errors.Wrap(err, "marshal stream request")
	}

	baseURL := c.config.BaseURL
	if baseURL == "" {
		baseURL = DefaultOllamaBaseURL
	}

	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/api/generate", bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, errors.Wrap(err, "create stream request")
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.streamClient.Do(req) //nolint:bodyclose // body is closed in the goroutine below and in the error-status branch
	if err != nil {
		return nil, errors.Wrap(err, "send stream request")
	}

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		if readErr != nil {
			log.Warn("llm: failed to read error response body", "error", readErr)
		}
		if err := resp.Body.Close(); err != nil {
			log.Warn("http: close response body failed", "error", err)
		}
		return nil, &HTTPError{StatusCode: resp.StatusCode, Message: fmt.Sprintf("ollama stream error (status %d): %s", resp.StatusCode, string(body))}
	}

	ch := make(chan StreamChunk, defaultStreamBuffer)

	go func() {
		defer close(ch)
		defer func() {
			if err := resp.Body.Close(); err != nil {
				log.Error("Failed to close stream response body", "error", err)
			}
		}()

		decoder := json.NewDecoder(resp.Body)
		for {
			var result struct {
				Response string `json:"response"`
				Done     bool   `json:"done"`
			}
			if err := decoder.Decode(&result); err != nil {
				if err != io.EOF {
					select {
					case ch <- StreamChunk{Done: true, Err: errors.Wrap(err, "decode stream chunk")}:
					case <-ctx.Done():
					}
				}
				return
			}

			if result.Done {
				return
			}

			select {
			case ch <- StreamChunk{Content: result.Response}:
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch, nil
}

// streamAnthropic streams text generation using Anthropic API.
// Anthropic streaming uses Server-Sent Events (SSE) with event: content_block_delta.
func (c *Client) streamAnthropic(ctx context.Context, prompt string) (<-chan StreamChunk, error) {
	if c.config.APIKey == "" {
		return nil, errors.New("API key is required for Anthropic streaming")
	}

	// Use configured MaxTokens, fallback to reasonable default for Anthropic.
	streamAnthropicMaxTokens := c.config.MaxTokens
	if streamAnthropicMaxTokens <= 0 {
		streamAnthropicMaxTokens = 1024
	}

	requestBody := map[string]interface{}{
		"model": c.config.Model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"max_tokens": streamAnthropicMaxTokens,
		"stream":     true, // Enable streaming mode for Anthropic
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return nil, errors.Wrap(err, "marshal anthropic stream request")
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.config.BaseURL+"/messages", bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, errors.Wrap(err, "create anthropic stream request")
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.config.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.streamClient.Do(req) //nolint:bodyclose // body is closed in the goroutine below and in the error-status branch
	if err != nil {
		return nil, errors.Wrap(err, "send anthropic stream request")
	}

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		if readErr != nil {
			log.Warn("llm: failed to read anthropic stream error response body", "error", readErr)
		}
		if err := resp.Body.Close(); err != nil {
			log.Warn("llm: close anthropic stream error response body", "error", err)
		}
		return nil, &HTTPError{StatusCode: resp.StatusCode, Message: fmt.Sprintf("anthropic stream error (status %d): %s", resp.StatusCode, string(body))}
	}

	ch := make(chan StreamChunk, defaultStreamBuffer)

	go func() {
		defer close(ch)
		defer func() {
			if err := resp.Body.Close(); err != nil {
				log.Error("Failed to close anthropic stream response body", "error", err)
			}
		}()

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1MB max line
		for scanner.Scan() {
			line := scanner.Text()

			// Some SSE implementations (including Anthropic) wrap JSON in
			// "data: " prefix. Strip it so the JSON check below works.
			line = strings.TrimPrefix(line, "data: ")

			if line == "" || line[0] != '{' {
				continue // skip SSE control lines (event:, id:, etc.)
			}

			// Anthropic SSE format: {"type": "content_block_delta", "delta": {"type": "text_delta", "text": "..."}}
			var event struct {
				Type  string `json:"type"`
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta,omitempty"`
			}

			if err := json.Unmarshal([]byte(line), &event); err != nil {
				log.Debug("skipping non-JSON SSE line in anthropic stream", "line", line)
				continue
			}

			// Only extract text from content_block_delta events
			if event.Type == "content_block_delta" && event.Delta.Type == "text_delta" && event.Delta.Text != "" {
				select {
				case ch <- StreamChunk{Content: event.Delta.Text}:
				case <-ctx.Done():
					return
				}
			}

			// Check for message_stop event (end of stream)
			if event.Type == "message_stop" {
				return
			}
		}

		if err := scanner.Err(); err != nil {
			select {
			case ch <- StreamChunk{Done: true, Err: errors.Wrap(err, "read anthropic stream")}:
			case <-ctx.Done():
			}
		}
	}()

	return ch, nil
}

// streamOpenRouter streams text generation using OpenRouter API.
func (c *Client) streamOpenRouter(ctx context.Context, prompt string) (<-chan StreamChunk, error) {
	if c.config.APIKey == "" {
		return nil, errors.New("API key is required for OpenRouter streaming")
	}

	// Use configured MaxTokens, fallback to defaultMaxTokens if not set or invalid.
	streamMaxTokens := c.config.MaxTokens
	if streamMaxTokens <= 0 {
		streamMaxTokens = defaultMaxTokens
	}

	requestBody := map[string]interface{}{
		"model": c.config.Model,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
		"temperature": 0.7,
		"max_tokens":  streamMaxTokens,
		"stream":      true,
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return nil, errors.Wrap(err, "marshal stream request")
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.config.BaseURL+"/chat/completions", bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, errors.Wrap(err, "create stream request")
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	req.Header.Set("X-Title", "ARES")

	resp, err := c.streamClient.Do(req) //nolint:bodyclose // body is closed in the goroutine below and in the error-status branch
	if err != nil {
		return nil, errors.Wrap(err, "send stream request")
	}

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		if readErr != nil {
			log.Warn("llm: failed to read error response body", "error", readErr)
		}
		if err := resp.Body.Close(); err != nil {
			log.Warn("http: close response body failed", "error", err)
		}
		return nil, &HTTPError{StatusCode: resp.StatusCode, Message: fmt.Sprintf("openrouter stream error (status %d): %s", resp.StatusCode, string(body))}
	}

	ch := make(chan StreamChunk, defaultStreamBuffer)

	go func() {
		defer close(ch)
		defer func() {
			if err := resp.Body.Close(); err != nil {
				log.Error("Failed to close stream response body", "error", err)
			}
		}()

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1MB max line for large SSE chunks
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			if line == "data: [DONE]" {
				return
			}
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")

			var result struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if err := json.Unmarshal([]byte(data), &result); err != nil {
				log.Warn("Failed to unmarshal stream chunk", "error", err)
				continue
			}

			if len(result.Choices) == 0 {
				continue
			}

			select {
			case ch <- StreamChunk{Content: result.Choices[0].Delta.Content}:
			case <-ctx.Done():
				return
			}
		}

		if err := scanner.Err(); err != nil {
			select {
			case ch <- StreamChunk{Done: true, Err: errors.Wrap(err, "read stream")}:
			case <-ctx.Done():
			}
		}
	}()

	return ch, nil
}
