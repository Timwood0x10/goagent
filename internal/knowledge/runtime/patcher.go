package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/planner"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

// PlanConfig holds the runtime-tunable planner configuration.
// These values are applied by KnowledgePatchExecutor when a patch arrives.
type PlanConfig struct {
	// MaxResults caps the number of results per knowledge query.
	MaxResults int

	// ReducerStrategy selects how query results are reduced: default / strict / relaxed.
	ReducerStrategy string
}

// SetPlanConfig updates the planner's MaxResults and reducer strategy.
// This is the integration point for KnowledgePatchExecutor.
func (r *KnowledgeRuntime) SetPlanConfig(cfg PlanConfig) {
	r.planMu.Lock()
	defer r.planMu.Unlock()
	r.planner = &configurablePlanner{
		maxResults:      cfg.MaxResults,
		reducerStrategy: cfg.ReducerStrategy,
	}
	log.Info("knowledge runtime: plan config updated",
		"max_results", cfg.MaxResults,
		"reducer", cfg.ReducerStrategy)
}

// PlanConfig returns the currently applied planner configuration, or the zero
// value when the planner is not a configurablePlanner (e.g. the bootstrap
// default planner). It is the read-side counterpart of SetPlanConfig and lets
// the patch executor capture the previous values needed for a precise rollback
// instead of falling back to hardcoded defaults.
func (r *KnowledgeRuntime) PlanConfig() PlanConfig {
	r.planMu.RLock()
	defer r.planMu.RUnlock()
	if cp, ok := r.planner.(*configurablePlanner); ok && cp != nil {
		return PlanConfig{
			MaxResults:      cp.maxResults,
			ReducerStrategy: cp.reducerStrategy,
		}
	}
	return PlanConfig{}
}

// configurablePlanner wraps a KnowledgePlanner with configurable MaxResults and ReducerStrategy.
type configurablePlanner struct {
	maxResults      int
	reducerStrategy string
}

func (p *configurablePlanner) Plan(ctx context.Context, goal string, budget knowledge.TokenBudget) (*planner.KnowledgePlan, error) {
	// Delegate to the default planner, then override MaxResults and reducer.
	base := planner.NewKnowledgePlanner()
	plan, err := base.Plan(ctx, goal, budget)
	if err != nil {
		return nil, err
	}
	if p.maxResults > 0 {
		for i := range plan.Requirements {
			plan.Requirements[i].MaxResults = p.maxResults
		}
	}
	if p.reducerStrategy != "" {
		for i := range plan.Requirements {
			plan.Requirements[i].ReducerStrategy = p.reducerStrategy
		}
	}
	return plan, nil
}

// ── KnowledgePatchExecutor ──────────────────

// KnowledgePatchExecutor handles knowledge-related runtime patches.
// It wraps a *KnowledgeRuntime and applies ChangePlanner/ChangeBudget/ChangeReducer.
// Implements patch.RuntimeComponent for unified runtime evolution.
type KnowledgePatchExecutor struct {
	mu      sync.Mutex
	runtime *KnowledgeRuntime
}

// NewKnowledgePatchExecutor creates a new KnowledgePatchExecutor.
func NewKnowledgePatchExecutor(r *KnowledgeRuntime) *KnowledgePatchExecutor {
	return &KnowledgePatchExecutor{runtime: r}
}

// SetRuntime replaces the wrapped KnowledgeRuntime so the executor evolves the
// live runtime instead of a bootstrap placeholder. This is the correct
// live-swap mechanism: patch.Registry.Register cannot overwrite an already
// registered component key, so re-registering would silently fail and the swap
// would be a no-op. The executor holds the runtime by reference, so swapping it
// in place makes knowledge genome patches affect the actual agent runtime.
func (e *KnowledgePatchExecutor) SetRuntime(r *KnowledgeRuntime) {
	e.mu.Lock()
	e.runtime = r
	e.mu.Unlock()
}

// Name returns "knowledge" as the component identifier for patch routing.
func (e *KnowledgePatchExecutor) Name() string { return "knowledge" }

// Snapshot returns the current plan configuration as a snapshot for diffing.
// It reads the live value from the wrapped KnowledgeRuntime so generations
// and rollbacks diff against the real applied config instead of an empty
// PlanConfig.
func (e *KnowledgePatchExecutor) Snapshot(_ context.Context) (any, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.runtime == nil {
		return nil, errors.New("knowledge executor: runtime is nil")
	}
	return e.runtime.PlanConfig(), nil
}

// Ensure KnowledgePatchExecutor implements patch.RuntimeComponent.
var _ patch.RuntimeComponent = (*KnowledgePatchExecutor)(nil)

// Apply applies a runtime patch to the knowledge runtime.
func (e *KnowledgePatchExecutor) Apply(_ context.Context, p patch.RuntimePatch) (*patch.RuntimePatch, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch p.Type {
	case patch.PatchChangeBudget:
		return e.applyChangeBudget(p)
	case patch.PatchChangePlanner:
		return e.applyChangePlanner(p)
	case patch.PatchChangeReducer:
		return e.applyChangeReducer(p)
	default:
		return nil, fmt.Errorf("knowledge executor: unsupported patch type %s", p.Type)
	}
}

// CanApply checks whether a patch can be applied.
func (e *KnowledgePatchExecutor) CanApply(_ context.Context, p patch.RuntimePatch) error {
	if e.runtime == nil {
		return errors.New("knowledge executor: runtime is nil")
	}
	switch p.Type {
	case patch.PatchChangeBudget:
		_, ok := p.Value.(int)
		if !ok {
			return errors.New("knowledge executor: ChangeBudget value must be int")
		}
		return nil
	case patch.PatchChangePlanner:
		_, ok := p.Value.(string)
		if !ok {
			return errors.New("knowledge executor: ChangePlanner value must be string")
		}
		return nil
	case patch.PatchChangeReducer:
		_, ok := p.Value.(string)
		if !ok {
			return errors.New("knowledge executor: ChangeReducer value must be string")
		}
		return nil
	default:
		return fmt.Errorf("knowledge executor: unsupported patch type %s", p.Type)
	}
}

// applyChangeBudget updates the MaxResults parameter. It captures the previous
// MaxResults so the returned rollback patch restores the exact old value rather
// than a hardcoded default, and preserves the current ReducerStrategy (a budget
// change must not wipe an unrelated setting).
func (e *KnowledgePatchExecutor) applyChangeBudget(p patch.RuntimePatch) (*patch.RuntimePatch, error) {
	newBudget, ok := p.Value.(int)
	if !ok {
		return nil, errors.New("knowledge executor: ChangeBudget value must be int")
	}

	old := e.runtime.PlanConfig()
	e.runtime.SetPlanConfig(PlanConfig{
		MaxResults:      newBudget,
		ReducerStrategy: old.ReducerStrategy,
	})

	return &patch.RuntimePatch{
		Type:   patch.PatchChangeBudget,
		Value:  old.MaxResults,
		Reason: "rollback: restore previous budget",
	}, nil
}

// applyChangePlanner updates the planner strategy. It applies the incoming
// strategy (instead of a hardcoded "default") and captures the previous
// strategy so the rollback restores it precisely. MaxResults is preserved.
func (e *KnowledgePatchExecutor) applyChangePlanner(p patch.RuntimePatch) (*patch.RuntimePatch, error) {
	newStrategy, ok := p.Value.(string)
	if !ok {
		return nil, errors.New("knowledge executor: ChangePlanner value must be string")
	}

	old := e.runtime.PlanConfig()
	e.runtime.SetPlanConfig(PlanConfig{
		MaxResults:      old.MaxResults,
		ReducerStrategy: newStrategy,
	})

	return &patch.RuntimePatch{
		Type:   patch.PatchChangePlanner,
		Value:  old.ReducerStrategy,
		Reason: "rollback: restore previous planner",
	}, nil
}

// applyChangeReducer updates the reducer strategy. It captures the previous
// strategy so the rollback restores it precisely. MaxResults is preserved.
func (e *KnowledgePatchExecutor) applyChangeReducer(p patch.RuntimePatch) (*patch.RuntimePatch, error) {
	strategy, ok := p.Value.(string)
	if !ok {
		return nil, errors.New("knowledge executor: ChangeReducer value must be string")
	}

	old := e.runtime.PlanConfig()
	e.runtime.SetPlanConfig(PlanConfig{
		MaxResults:      old.MaxResults,
		ReducerStrategy: strategy,
	})

	return &patch.RuntimePatch{
		Type:   patch.PatchChangeReducer,
		Value:  old.ReducerStrategy,
		Reason: "rollback: restore previous reducer",
	}, nil
}
