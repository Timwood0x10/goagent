package runtime

import (
	"context"
	"errors"
	"sync"
)

// RouterPlugin determines the next step to execute based on current state.
type RouterPlugin interface {
	RuntimePlugin
	// Route returns the next step decision for the given state.
	// Returning nil means no routing is needed (execution order continues
	// as defined by the DAG).
	Route(ctx context.Context, state RouteState) (*RouteDecision, error)
}

// RouteState contains the inputs available for routing decisions.
type RouteState struct {
	ExecutionID       string
	WorkflowID        string
	CurrentStepID     string
	CurrentStepOutput string
	Variables         map[string]any
	CollectedRoutes   []RouteRecord
	CollectedTools    []ToolRecord
	CollectedMemory   []MemoryHitRecord
	// Collector allows the router to record route decisions directly into
	// the execution collector for checkpoint persistence and evolution
	// consumption. May be nil; routers should check before using.
	Collector *ExecutionCollector
}

// RouteDecision is the outcome of a routing decision.
type RouteDecision struct {
	NextStepID string `json:"next_step_id"`
	Reason     string `json:"reason"`
	Source     string `json:"source"` // "expression" | "memory" | "evolution" | "fallback"
}

// ExpressionRouter evaluates a set of route rules to decide the next step.
// It is the default, simple-rule-based router implementation.
type ExpressionRouter struct {
	mu    sync.RWMutex
	name  string
	rules []RouteRule
}

// RouteRule defines a single routing condition and target.
type RouteRule struct {
	FromStepID string // source step; empty matches any
	ToStepID   string // target step when condition matches
	Condition  func(output string, vars map[string]any) bool
	Reason     string // description of why this rule exists
}

// NewExpressionRouter creates an ExpressionRouter with the given rules.
func NewExpressionRouter(name string, rules []RouteRule) *ExpressionRouter {
	if name == "" {
		name = "expression-router"
	}
	return &ExpressionRouter{
		name:  name,
		rules: rules,
	}
}

// Name returns the plugin name.
func (r *ExpressionRouter) Name() string { return r.name }

// Capabilities returns the capabilities.
func (r *ExpressionRouter) Capabilities() []Capability {
	return []Capability{CapRouter}
}

// Start initializes the router.
func (r *ExpressionRouter) Start(_ context.Context, _ EventBus) error { return nil }

// Stop shuts down the router.
func (r *ExpressionRouter) Stop(_ context.Context) error { return nil }

// Route evaluates rules in order and returns the first match.
// If no rule matches, it returns (nil, nil) — per the RouterPlugin contract,
// a nil decision means "no routing is needed" and execution continues along
// the DAG's default order. Callers must not treat the nil-decision case as
// an error.
func (r *ExpressionRouter) Route(_ context.Context, state RouteState) (*RouteDecision, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, rule := range r.rules {
		if rule.FromStepID != "" && rule.FromStepID != state.CurrentStepID {
			continue
		}
		if rule.Condition != nil && !rule.Condition(state.CurrentStepOutput, state.Variables) {
			continue
		}
		return &RouteDecision{
			NextStepID: rule.ToStepID,
			Reason:     rule.Reason,
			Source:     "expression",
		}, nil
	}
	log.Debug("expression router: no matching rule, continuing default DAG order",
		"step", state.CurrentStepID,
	)
	//nolint:nilnil // documented RouterPlugin contract: nil decision + nil error = "no routing needed"
	return nil, nil
}

// AddRule adds a rule to the router.
func (r *ExpressionRouter) AddRule(rule RouteRule) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rules = append(r.rules, rule)
}

// Rules returns a copy of the current rules.
func (r *ExpressionRouter) Rules() []RouteRule {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rules := make([]RouteRule, len(r.rules))
	copy(rules, r.rules)
	return rules
}

// Validate checks that the router was found and returns a clear error.
func ValidateRouterFound(plugin RuntimePlugin) error {
	if plugin == nil {
		return errors.New("router plugin not found")
	}
	return nil
}
