package runtime

import (
	"context"
)

// AgentStepResolver maps a PreferredAgent name to a target step ID.
// If the resolver returns "" or false, the router falls through to expression rules.
type AgentStepResolver func(preferredAgent string) (string, bool)

// EvolutionRouter implements RouterPlugin by consulting EvolutionPlugin.Recommend
// to bias routing decisions. The recommendation's RouterWeight and PreferredAgent
// influence which step to route to. Falls back to expression rules when evolution
// advice is unavailable.
type EvolutionRouter struct {
	ExpressionRouter
	bus         EventBus
	agentMapper AgentStepResolver // optional; maps agent name to step ID
}

// NewEvolutionRouter creates an EvolutionRouter with the given name and expression rules.
func NewEvolutionRouter(name string, rules []RouteRule) *EvolutionRouter {
	if name == "" {
		name = "evolution-router"
	}
	return &EvolutionRouter{
		ExpressionRouter: ExpressionRouter{
			name:  name,
			rules: rules,
		},
	}
}

// WithAgentResolver sets an optional mapping function that translates a
// PreferredAgent (from RuntimeRecommendation) to a target step ID. If the
// resolver returns "" or false, the router falls through to expression rules.
func (r *EvolutionRouter) WithAgentResolver(resolver AgentStepResolver) *EvolutionRouter {
	r.agentMapper = resolver
	return r
}

// Route evaluates routes using evolution recommendations first, then falls back
// to expression rules.
func (r *EvolutionRouter) Route(ctx context.Context, state RouteState) (*RouteDecision, error) {
	if d := r.evolutionAdvice(ctx, state); d != nil {
		return d, nil
	}
	return r.ExpressionRouter.Route(ctx, state)
}

func (r *EvolutionRouter) evolutionAdvice(ctx context.Context, state RouteState) *RouteDecision {
	if r.bus == nil {
		return nil
	}
	pb, ok := r.bus.(*PluginBus)
	if !ok {
		return nil
	}
	evoPlugins := pb.PluginsByCap(CapEvolution)
	if len(evoPlugins) == 0 {
		return nil
	}
	ep, ok := evoPlugins[0].(EvolutionPlugin)
	if !ok || ep == nil {
		return nil
	}
	execState := ExecutionState{
		ExecutionID:   state.ExecutionID,
		WorkflowID:    state.WorkflowID,
		CurrentStepID: state.CurrentStepID,
	}
	rec, err := ep.Recommend(ctx, execState)
	if err != nil {
		log.Warn("evolution router: Recommend failed, falling back to expression rules",
			"error", err,
		)
		return nil
	}
	if rec == nil || rec.Confidence < 0.3 {
		return nil
	}
	// Use PreferredAgent to find the target step. If an AgentStepResolver is
	// configured, use it to map the preferred agent to a step ID. Fall through
	// to expression rules when no mapping is available.
	if rec.PreferredAgent != "" && r.agentMapper != nil {
		if stepID, ok := r.agentMapper(rec.PreferredAgent); ok && stepID != "" {
			return &RouteDecision{
				NextStepID: stepID,
				Reason:     "evolution: preferred agent " + rec.PreferredAgent,
				Source:     "evolution",
			}
		}
	}
	// RouterWeight alone doesn't indicate a target step; it is a bias signal
	// for expression rule evaluation. Fall through to expression rules which
	// may use the weight from RouteRouteState if available.
	return nil
}

// Start saves the EventBus reference for finding EvolutionPlugin instances.
func (r *EvolutionRouter) Start(ctx context.Context, bus EventBus) error {
	r.bus = bus
	return nil
}

// Capabilities returns the capabilities (router + evolution consumer).
func (r *EvolutionRouter) Capabilities() []Capability {
	return []Capability{CapRouter}
}

var _ RouterPlugin = (*EvolutionRouter)(nil)
