package runtime

import (
	"context"
	"errors"
	"fmt"
)

// FallbackRouter tries multiple RouterPlugin instances in order and returns
// the first non-nil decision. If all routers return nil, it returns a fallback
// decision with source "fallback". This provides degradation when primary
// routing paths (memory, evolution, expression) are unavailable.
type FallbackRouter struct {
	name    string
	routers []RouterPlugin
}

// NewFallbackRouter creates a FallbackRouter that tries routers in order.
// At least one router must be provided.
func NewFallbackRouter(name string, routers []RouterPlugin) (*FallbackRouter, error) {
	if name == "" {
		name = "fallback-router"
	}
	if len(routers) == 0 {
		return nil, errors.New("fallback router requires at least one sub-router")
	}
	return &FallbackRouter{
		name:    name,
		routers: routers,
	}, nil
}

func (r *FallbackRouter) Name() string { return r.name }

func (r *FallbackRouter) Capabilities() []Capability {
	return []Capability{CapRouter}
}

func (r *FallbackRouter) Start(ctx context.Context, bus EventBus) error {
	for _, router := range r.routers {
		if err := router.Start(ctx, bus); err != nil {
			return fmt.Errorf("fallback router: sub-router %s start: %w", router.Name(), err)
		}
	}
	return nil
}

func (r *FallbackRouter) Stop(ctx context.Context) error {
	var firstErr error
	for _, router := range r.routers {
		if err := router.Stop(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Route tries each sub-router in order and returns the first non-nil decision.
// If all routers return nil, returns a fallback decision with source "fallback".
func (r *FallbackRouter) Route(ctx context.Context, state RouteState) (*RouteDecision, error) {
	for _, router := range r.routers {
		decision, err := router.Route(ctx, state)
		if err != nil {
			log.Warn("fallback router: sub-router errored, trying next",
				"router", router.Name(),
				"error", err,
			)
			continue
		}
		if decision != nil {
			log.Debug("fallback router: selected",
				"router", router.Name(),
				"next_step", decision.NextStepID,
				"source", decision.Source,
			)
			return decision, nil
		}
	}
	// All routers returned nil; emit a fallback decision so the caller
	// knows we tried but no route was suggested. The executor continues
	// with default DAG traversal when NextStepID is empty.
	return &RouteDecision{
		NextStepID: "",
		Reason:     "no router produced a decision",
		Source:     "fallback",
	}, nil
}

// Routers returns the sub-routers for inspection.
func (r *FallbackRouter) Routers() []RouterPlugin {
	out := make([]RouterPlugin, len(r.routers))
	copy(out, r.routers)
	return out
}

var _ RouterPlugin = (*FallbackRouter)(nil)
