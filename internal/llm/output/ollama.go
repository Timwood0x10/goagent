package output

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"io"
	"net/http"
	"time"

	"github.com/Timwood0x10/ares/internal/core/models"
	gerr "github.com/Timwood0x10/ares/internal/errors"
)

const (
	ollamaDefaultURL     = "http://localhost:11434"
	keyOllamaModel       = "model"
	keyOllamaStream      = "stream"
	keyOllamaTemperature = "temperature"
)

// Ollama errors.
var (
	ErrInvalidResponse = stderrors.New("invalid response")
)

// OllamaAdapter implements LLMAdapter for Ollama.
type OllamaAdapter struct {
	config       *Config
	client       *http.Client
	streamClient *http.Client
}

// NewOllamaAdapter creates a new OllamaAdapter.
func NewOllamaAdapter(config *Config) *OllamaAdapter {
	if config == nil {
		config = &Config{}
	}
	if config.BaseURL == "" {
		config.BaseURL = ollamaDefaultURL
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 60
	}

	return &OllamaAdapter{
		config: config,
		client: &http.Client{
			Timeout: time.Duration(timeout) * time.Second,
		},
		streamClient: &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// Generate generates text from prompt.
func (a *OllamaAdapter) Generate(ctx context.Context, prompt string) (string, error) {
	reqBody := map[string]interface{}{
		keyOllamaModel:  a.config.Model,
		"prompt":        prompt,
		keyOllamaStream: false,
		// Ollama expects sampling parameters inside an "options" object;
		// top-level "temperature" is silently ignored.
		"options": map[string]interface{}{
			keyOllamaTemperature: a.config.Temperature,
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", gerr.Wrap(err, "marshal request")
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		a.config.BaseURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return "", gerr.Wrap(err, "create request")
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return "", gerr.Wrap(err, "send request")
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Error("Failed to close response body", "error", err)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		if err != nil {
			return "", gerr.Wrap(err, "read response body")
		}
		return "", gerr.Newf("API request failed with status %d: %s", resp.StatusCode, respBody)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", gerr.Wrap(err, "decode response")
	}

	response, ok := result["response"].(string)
	if !ok {
		return "", ErrInvalidResponse
	}

	return response, nil
}

// GenerateWithParams generates text; per-call parameter overrides are
// not supported by this adapter, so it delegates to Generate.
func (a *OllamaAdapter) GenerateWithParams(ctx context.Context, prompt string, _ map[string]any) (string, error) {
	return a.Generate(ctx, prompt)
}

// GenerateStructured generates structured output.
func (a *OllamaAdapter) GenerateStructured(ctx context.Context, prompt string, schema string) (*models.RecommendResult, error) {
	fullPrompt := prompt + "\n\nRespond with valid JSON matching this schema:\n" + schema

	response, err := a.Generate(ctx, fullPrompt)
	if err != nil {
		return nil, err
	}

	parser := NewParser()
	return parser.ParseRecommendResult(response)
}

// GetModel returns the model name.
func (a *OllamaAdapter) GetModel() string {
	return a.config.Model
}

// GenerateStream generates text as a stream of chunks using Ollama API.
func (a *OllamaAdapter) GenerateStream(ctx context.Context, prompt string) (<-chan StreamChunk, error) {
	if prompt == "" {
		return nil, gerr.New("empty prompt")
	}

	reqBody := map[string]interface{}{
		"model":       a.config.Model,
		"prompt":      prompt,
		"stream":      true,
		"temperature": a.config.Temperature,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, gerr.Wrap(err, "marshal stream request")
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		a.config.BaseURL+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return nil, gerr.Wrap(err, "create stream request")
	}

	req.Header.Set("Content-Type", "application/json")

	// Timeout is controlled via the request context, not the client.
	resp, err := a.streamClient.Do(req) //nolint:bodyclose // body is closed in the goroutine below and in the error-status branch
	if err != nil {
		return nil, gerr.Wrap(err, "send stream request")
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		if err := resp.Body.Close(); err != nil {
			log.Warn("ollama: close stream error response body", "error", err)
		}
		return nil, gerr.Newf("ollama stream error (status %d): %s", resp.StatusCode, string(respBody))
	}

	ch := make(chan StreamChunk, 64)

	go func() {
		defer close(ch)
		defer func() {
			if err := resp.Body.Close(); err != nil {
				log.Error("Failed to close stream response body", "error", err)
			}
		}()

		decoder := json.NewDecoder(resp.Body)
		for {
			var chunk OllamaResponse
			if err := decoder.Decode(&chunk); err != nil {
				if err != io.EOF {
					select {
					case ch <- StreamChunk{Done: true, Err: gerr.Wrap(err, "decode stream chunk")}:
					case <-ctx.Done():
					}
				}
				return
			}

			if chunk.Done {
				return
			}

			select {
			case ch <- StreamChunk{Content: chunk.Response}:
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch, nil
}

// OllamaResponse represents Ollama API response.
type OllamaResponse struct {
	Model     string `json:"model"`
	CreatedAt string `json:"created_at"`
	Response  string `json:"response"`
	Done      bool   `json:"done"`
}
