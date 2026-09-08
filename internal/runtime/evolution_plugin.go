package runtime

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// StrategyProvider provides runtime recommendations based on evolutionary
// computation. Implementations query the active strategy and transform it
// into a recommendation for the plugin bus.
type StrategyProvider interface {
	// GetRecommendation returns the current runtime recommendation.
	// Returning nil, nil means no recommendation is available.
	GetRecommendation(ctx context.Context) (*RuntimeRecommendation, error)
}

// OutcomeRecorder persists execution outcomes for offline evolution learning.
type OutcomeRecorder interface {
	// RecordOutcome saves an execution outcome for later analysis.
	RecordOutcome(ctx context.Context, outcome ExecutionOutcome) error
}

// EvolutionPluginOptions holds optional configuration for EvolutionPlugin.
type EvolutionPluginOptions struct {
	// CacheTTL is how long a recommendation is considered fresh.
	// Default: 30 seconds. Set to 0 to disable caching.
	CacheTTL time.Duration
}

// EvolutionPluginOption configures EvolutionPluginOptions.
type EvolutionPluginOption func(*EvolutionPluginOptions)

// WithCacheTTL sets the recommendation cache TTL.
func WithCacheTTL(ttl time.Duration) EvolutionPluginOption {
	return func(o *EvolutionPluginOptions) {
		o.CacheTTL = ttl
	}
}

func defaultEvolutionPluginOptions() EvolutionPluginOptions {
	return EvolutionPluginOptions{
		CacheTTL: 30 * time.Second,
	}
}

// defaultEvolutionPlugin provides runtime recommendations from evolutionary
// computation. It implements RuntimePlugin and EvolutionPlugin.
type defaultEvolutionPlugin struct {
	name     string
	provider StrategyProvider
	recorder OutcomeRecorder
	config   EvolutionPluginOptions

	mu        sync.Mutex
	lastRec   *RuntimeRecommendation
	lastRecAt time.Time
}

// NewEvolutionPlugin creates an EvolutionPlugin with the given dependencies.
// provider and recorder may be nil (recommendation returns nil, outcomes
// are silently dropped).
func NewEvolutionPlugin(
	name string,
	provider StrategyProvider,
	recorder OutcomeRecorder,
	opts ...EvolutionPluginOption,
) EvolutionPlugin {
	if name == "" {
		name = "evolution"
	}
	cfg := defaultEvolutionPluginOptions()
	for _, opt := range opts {
		opt(&cfg)
	}
	return &defaultEvolutionPlugin{
		name:     name,
		provider: provider,
		recorder: recorder,
		config:   cfg,
	}
}

// Name returns the plugin name.
func (p *defaultEvolutionPlugin) Name() string { return p.name }

// Capabilities returns the capabilities this plugin provides.
func (p *defaultEvolutionPlugin) Capabilities() []Capability {
	return []Capability{CapEvolution}
}

// Start initializes the evolution plugin.
func (p *defaultEvolutionPlugin) Start(_ context.Context, _ EventBus) error {
	return nil
}

// Stop shuts down the evolution plugin.
func (p *defaultEvolutionPlugin) Stop(_ context.Context) error {
	p.mu.Lock()
	p.lastRec = nil
	p.lastRecAt = time.Time{}
	p.mu.Unlock()
	return nil
}

// Recommend returns a runtime recommendation based on the current strategy.
// Results are cached according to CacheTTL. Returns nil, nil if no provider
// is configured or no recommendation is available (per the StrategyProvider
// contract: a nil recommendation with a nil error means "no recommendation").
func (p *defaultEvolutionPlugin) Recommend(ctx context.Context, _ ExecutionState) (*RuntimeRecommendation, error) {
	// No provider configured is a valid "evolution disabled" state, not an
	// error: return an empty recommendation per the documented contract.
	// The router's Recommend caller nil-checks the result and falls through
	// to expression rules, so (nil, nil) is a supported "no routing input"
	// outcome, never a misread failure.
	if p.provider == nil {
		return nil, nil //nolint:nilnil // documented "no provider" contract.
	}

	p.mu.Lock()
	if p.lastRec != nil && !p.lastRecAt.IsZero() {
		if time.Since(p.lastRecAt) < p.config.CacheTTL {
			rec := *p.lastRec
			p.mu.Unlock()
			return &rec, nil
		}
	}
	p.mu.Unlock()

	rec, err := p.provider.GetRecommendation(ctx)
	if err != nil {
		return nil, fmt.Errorf("evolution: get recommendation: %w", err)
	}

	p.mu.Lock()
	p.lastRec = rec
	p.lastRecAt = time.Now()
	p.mu.Unlock()

	if rec != nil {
		log.Debug("evolution: recommendation updated",
			"plugin", p.name,
			"agent", rec.PreferredAgent,
			"confidence", rec.Confidence,
		)
	}

	// A nil recommendation with a nil error is the documented "no
	// recommendation available" success result — not a failure. This is the
	// StrategyProvider contract verbatim, and the router consumer treats
	// rec == nil as "fall through to expression rules".
	if rec == nil {
		return nil, nil //nolint:nilnil // documented "no recommendation" contract.
	}
	cp := *rec
	return &cp, nil
}

// RecordOutcome ingests a completed execution outcome for offline learning.
// Silently drops the outcome if no recorder is configured.
func (p *defaultEvolutionPlugin) RecordOutcome(ctx context.Context, outcome ExecutionOutcome) error {
	if p.recorder == nil {
		return nil
	}
	if err := p.recorder.RecordOutcome(ctx, outcome); err != nil {
		return fmt.Errorf("evolution: record outcome: %w", err)
	}
	return nil
}
