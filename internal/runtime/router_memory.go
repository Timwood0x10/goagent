package runtime

import (
	"context"
	"sync"
)

// MemoryRouter implements RouterPlugin by consulting MemoryPlugin.AdviseRoute
// and falling back to expression rules when memory advice is unavailable or
// below the confidence threshold.
//
// MemoryRouter supports async pre-fetch: it implements WorkflowHook so that
// BeforeStep starts a background memory query for the upcoming step. When
// Route() is called after the step completes, the pre-fetched advice is used
// immediately, avoiding synchronous latency.
type MemoryRouter struct {
	ExpressionRouter
	bus           EventBus
	confidenceMin float64

	prefetch struct {
		mu     sync.Mutex
		advice []RouteAdvice // pre-fetched advice for a specific step
		stepID string        // step for which advice was pre-fetched
	}
}

// NewMemoryRouter creates a MemoryRouter with the given name, expression rules,
// and minimum confidence threshold. If confidenceMin is 0, the default of 0.5 is used.
func NewMemoryRouter(name string, rules []RouteRule, confidenceMin float64) *MemoryRouter {
	if name == "" {
		name = "memory-router"
	}
	if confidenceMin <= 0 {
		confidenceMin = 0.5
	}
	return &MemoryRouter{
		ExpressionRouter: ExpressionRouter{
			name:  name,
			rules: rules,
		},
		confidenceMin: confidenceMin,
	}
}

// Name returns the plugin name.
func (r *MemoryRouter) Name() string { return r.ExpressionRouter.Name() }

// Capabilities returns the capabilities (router + memory consumer + hook).
func (r *MemoryRouter) Capabilities() []Capability {
	return []Capability{CapRouter}
}

// Start saves the EventBus reference for finding MemoryPlugin instances.
func (r *MemoryRouter) Start(ctx context.Context, bus EventBus) error {
	r.bus = bus
	return nil
}

// Stop shuts down the router.
func (r *MemoryRouter) Stop(_ context.Context) error { return nil }

// BeforeStep starts an async pre-fetch of memory advice for the step about
// to execute. The query runs in a background goroutine while the step executes.
// When Route() is called after the step completes, the pre-fetched advice is
// used immediately, avoiding synchronous latency.
func (r *MemoryRouter) BeforeStep(ctx context.Context, _ string, step *Step) error {
	if r.bus == nil {
		return nil
	}
	state := RouteState{
		CurrentStepID: step.ID,
	}
	// Launch the memory query in a background goroutine so it runs in
	// parallel with step execution. If the query completes before Route()
	// is called, the prefetched result is used. Otherwise, Route() falls
	// through to a synchronous query.
	go func() {
		advice := r.queryMemory(ctx, state)
		r.prefetch.mu.Lock()
		r.prefetch.advice = advice
		r.prefetch.stepID = step.ID
		r.prefetch.mu.Unlock()
	}()
	return nil
}

// AfterStep is a no-op; the MemoryRouter only needs BeforeStep for pre-fetch.
func (r *MemoryRouter) AfterStep(_ context.Context, _ string, _ *StepResult) error {
	return nil
}

// Route evaluates routes using memory advice first, then falls back to
// expression rules. If pre-fetched advice is available for the current
// step (from BeforeStep), it is used immediately.
func (r *MemoryRouter) Route(ctx context.Context, state RouteState) (*RouteDecision, error) {
	// Check pre-fetched advice first.
	r.prefetch.mu.Lock()
	prefetched := r.prefetch.advice
	prefetchedStep := r.prefetch.stepID
	r.prefetch.mu.Unlock()

	if prefetched != nil && prefetchedStep == state.CurrentStepID {
		if d := r.bestAdvice(prefetched); d != nil {
			log.Debug("memory router: using pre-fetched advice",
				"step", state.CurrentStepID,
				"next_step", d.NextStepID,
				"confidence", "prefetched",
			)
			return d, nil
		}
	}

	// Fall through to synchronous memory query.
	if d := r.memoryAdvice(ctx, state); d != nil {
		return d, nil
	}
	return r.ExpressionRouter.Route(ctx, state)
}

// queryMemory calls AdviseRoute on the first available MemoryPlugin and
// returns all advice. Returns nil if no memory plugin is available or if
// the query fails.
func (r *MemoryRouter) queryMemory(ctx context.Context, state RouteState) []RouteAdvice {
	if r.bus == nil {
		return nil
	}
	pb, ok := r.bus.(*PluginBus)
	if !ok {
		return nil
	}
	memPlugins := pb.PluginsByCap(CapMemory)
	if len(memPlugins) == 0 {
		return nil
	}
	mp, ok := memPlugins[0].(MemoryPlugin)
	if !ok || mp == nil {
		return nil
	}
	advice, err := mp.AdviseRoute(ctx, state)
	if err != nil {
		log.Warn("memory router: AdviseRoute failed",
			"error", err,
		)
		return nil
	}
	return advice
}

// memoryAdvice queries memory synchronously and returns the best advice
// above the confidence threshold.
func (r *MemoryRouter) memoryAdvice(ctx context.Context, state RouteState) *RouteDecision {
	advice := r.queryMemory(ctx, state)
	return r.bestAdvice(advice)
}

// bestAdvice picks the advice with highest confidence above threshold.
func (r *MemoryRouter) bestAdvice(advice []RouteAdvice) *RouteDecision {
	if len(advice) == 0 {
		return nil
	}
	best := advice[0]
	for _, a := range advice[1:] {
		if a.Confidence > best.Confidence {
			best = a
		}
	}
	if best.Confidence < r.confidenceMin {
		log.Debug("memory router: best advice below confidence threshold, falling back",
			"confidence", best.Confidence,
			"threshold", r.confidenceMin,
		)
		return nil
	}
	return &RouteDecision{
		NextStepID: best.NextStepID,
		Reason:     best.Reason,
		Source:     "memory",
	}
}

var _ RouterPlugin = (*MemoryRouter)(nil)
var _ WorkflowHook = (*MemoryRouter)(nil)
