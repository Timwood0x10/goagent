// Package ares_bootstrap — LLM provider.
package ares_bootstrap

import (
	"fmt"

	"github.com/Timwood0x10/ares/compat"
	compatllm "github.com/Timwood0x10/ares/compat/llm"
	"github.com/Timwood0x10/ares/compat/llm/ollama"
	"github.com/Timwood0x10/ares/compat/llm/openai"
	"github.com/Timwood0x10/ares/internal/ares_callbacks"
	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/ares_security"
	"github.com/Timwood0x10/ares/internal/llm"
	"github.com/Timwood0x10/ares/internal/runtime/observability"
)

func ProvideLLM(cfg ares_config.LLMConfig) (*LLMComponents, error) {
	reg := ares_callbacks.NewRegistry()
	llmCfg := &llm.Config{
		Provider:        cfg.Provider,
		APIKey:          cfg.APIKey,
		BaseURL:         cfg.BaseURL,
		Model:           cfg.Model,
		Timeout:         cfg.Timeout,
		MaxTokens:       cfg.MaxTokens,
		MaxPromptLength: cfg.MaxPromptLength,
		Extra:           cfg.Extra,
	}
	client, err := llm.NewClient(llmCfg, llm.WithCallbacks(reg), llm.WithSanitizer(ares_security.NewSanitizer()))
	if err != nil {
		return nil, fmt.Errorf("bootstrap: LLM client: %w", err)
	}

	// Observability wiring: register the Prometheus ARES_* metrics so the
	// /metrics scrape endpoint (serveIntrospect) returns real counters, and
	// attach a REAL recording tracer (MetricsTracer) so every LLM call
	// increments counters and attributes cost — a NoopTracer here left all
	// ARES_* counters at zero. Registration is idempotent
	// (AlreadyRegisteredError returns the cached instance).
	metrics, merr := observability.NewPrometheusMetrics()
	if merr != nil {
		log.Warn("bootstrap: prometheus metrics registration skipped", "error", merr)
	}
	dashboard := observability.NewCostDashboard()
	client.SetTracer(observability.NewMetricsTracer(metrics, dashboard))

	// Register the LLM provider in the compat layer for ecosystem access.
	// Dispatch to the correct adapter based on provider name instead of
	// always using openai.New. For unknown providers, fall back to openai
	// (which covers openai-compatible endpoints like openrouter/azure).
	provider := cfg.Provider
	if provider == "" {
		provider = "openai"
	}
	if err := compat.RegisterLLM(provider, func(config map[string]any) (compatllm.LLMProvider, error) {
		switch provider {
		case "ollama":
			return ollama.New(config)
		default:
			// openai, openrouter, anthropic (via proxy), azure — all
			// speak the OpenAI-compatible API surface.
			return openai.New(config)
		}
	}); err != nil {
		log.Warn("bootstrap: compat LLM registration skipped", "provider", provider, "error", err)
	}

	return &LLMComponents{
		Client:        client,
		CallbackReg:   reg,
		CostDashboard: dashboard,
	}, nil
}

// NewCallbackRegistry creates a callback registry. Kept as a convenience
// alias over ares_callbacks.NewRegistry for callers that prefer the
// bootstrap-level entry point.
func NewCallbackRegistry() *ares_callbacks.Registry {
	return ares_callbacks.NewRegistry()
}

// NewLLMClientWithCallbacks creates an LLM client with the given callback
// registry attached. Convenience wrapper over llm.NewClient.
func NewLLMClientWithCallbacks(cfg *llm.Config, reg *ares_callbacks.Registry) (*llm.Client, error) {
	return llm.NewClient(cfg, llm.WithCallbacks(reg))
}
