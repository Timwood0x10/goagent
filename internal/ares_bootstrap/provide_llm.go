// Package ares_bootstrap — LLM provider.
package ares_bootstrap

import (
	"fmt"

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

	// TODO(tech-debt): the compat.RegisterLLM block was removed (M5
	// sunset step, 2026-09-09): the registration was write-only — the
	// compat registry had zero readers anywhere (no GetLLM consumer in
	// this repo, examples included), so bootstrap was populating a
	// registry nobody queried. The compat/ tree now has zero internal
	// references; deleting the directory itself is a 0.4.x release-note
	// decision per compat/doc.go's stated policy (removing exported
	// packages is a breaking change on the patch line).

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
