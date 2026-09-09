// Package llmsvcapi provides the LLM service API used by the sdk and
// agentloop layers (M5 internalization of the former api/service/llm).
package llmsvcapi

import (
	"context"

	"github.com/Timwood0x10/ares/internal/llm"
	llmcore "github.com/Timwood0x10/ares/internal/llmcore"
	llmservice "github.com/Timwood0x10/ares/internal/llmservice"
)

// Config holds configuration for the LLM service.
// This is a public type that wraps the internal Config to avoid
// leaking internal package types into the public API.
type Config struct {
	// BaseConfig is the base configuration (timeout, retries, etc.).
	BaseConfig *llmcore.BaseConfig
	// LLMConfig is the LLM provider configuration.
	LLMConfig *llmcore.LLMConfig
	// Fallbacks is a list of fallback LLM configs for failover.
	// When non-empty, a FailoverClient is created instead of a single Client.
	Fallbacks []*llmcore.LLMConfig
	// Repo is the LLM repository (optional, for logging/audit).
	Repo llmcore.LLMRepository
	// EmbeddingClient is the embedding service client (optional).
	EmbeddingClient any
}

// toInternal converts the public Config to the internal Config type.
func (c *Config) toInternal() *llmservice.Config {
	if c == nil {
		return nil
	}
	// Convert fallback configs, skipping nil entries to avoid a nil-pointer
	// dereference when a caller passes a sparse slice.
	var fallbacks []*llm.Config
	for _, f := range c.Fallbacks {
		if f == nil {
			continue
		}
		fallbacks = append(fallbacks, &llm.Config{
			Provider:        string(f.Provider),
			Model:           f.Model,
			BaseURL:         f.BaseURL,
			APIKey:          f.APIKey,
			Timeout:         f.Timeout,
			MaxTokens:       f.MaxTokens,
			MaxPromptLength: f.MaxPromptLength,
		})
	}
	return &llmservice.Config{
		BaseConfig:      c.BaseConfig,
		LLMConfig:       c.LLMConfig,
		Fallbacks:       fallbacks,
		Repo:            c.Repo,
		EmbeddingClient: c.EmbeddingClient,
	}
}

// Service wraps internal/llmservice.Service for public consumption.
type Service struct {
	inner *llmservice.Service
}

// NewService creates a new LLM service with the given config.
func NewService(cfg *Config) (*Service, error) {
	s, err := llmservice.NewService(cfg.toInternal())
	if err != nil {
		return nil, err
	}
	return &Service{inner: s}, nil
}

// Generate delegates to the inner service.
func (s *Service) Generate(ctx context.Context, request *llmcore.GenerateRequest) (*llmcore.GenerateResponse, error) {
	return s.inner.Generate(ctx, request)
}

// GenerateSimple delegates to the inner service.
func (s *Service) GenerateSimple(ctx context.Context, prompt string) (string, error) {
	return s.inner.GenerateSimple(ctx, prompt)
}

// GenerateEmbedding delegates to the inner service.
func (s *Service) GenerateEmbedding(ctx context.Context, request *llmcore.EmbeddingRequest) (*llmcore.EmbeddingResponse, error) {
	return s.inner.GenerateEmbedding(ctx, request)
}

// GetConfig returns the current LLM configuration.
func (s *Service) GetConfig() *llmcore.LLMConfig {
	return s.inner.GetConfig()
}

// IsEnabled checks if the LLM service is properly configured and available.
func (s *Service) IsEnabled() bool {
	return s.inner.IsEnabled()
}

// GetProvider returns the current LLM provider.
func (s *Service) GetProvider() llmcore.LLMProvider {
	return s.inner.GetProvider()
}

// GetModel returns the current model name.
func (s *Service) GetModel() string {
	return s.inner.GetModel()
}

// Close releases resources held by the LLM service.
func (s *Service) Close() {
	s.inner.Close()
}
