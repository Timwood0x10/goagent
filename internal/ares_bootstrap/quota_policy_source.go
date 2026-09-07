// Package ares_bootstrap — evolution quota policy adapter.
package ares_bootstrap

import (
	"context"
	"errors"
	"fmt"

	"github.com/Timwood0x10/ares/internal/aresrecovery"
	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
)

// quotaBudgetParam ("quota.budget") is the strategy param carrying the
// evolution-produced resource budget as a name → amount map, e.g.
// {"cpu": 8, "memory": 8192} (v0.3.0 M2-2).
const quotaBudgetParam = "quota.budget"

// evolutionQuotaPolicySource adapts an evolution.StrategyStore to the
// aresrecovery.QuotaPolicySource contract. When the active strategy carries
// no quota params, the source falls back to the configured default budget
// (cfg.Kernel.Resources) so Apply is a no-op that preserves the prior
// behavior instead of wiping the config budget.
type evolutionQuotaPolicySource struct {
	store         evolution.StrategyStore
	defaultBudget map[string]float64
}

// NewQuotaPolicySource wraps an evolution StrategyStore as an
// aresrecovery.QuotaPolicySource. defaultBudget is applied when no quota
// params are deployed. Returns nil when the store is nil so callers can skip
// injection safely (serve runs without GA guidance).
func NewQuotaPolicySource(store evolution.StrategyStore, defaultBudget map[string]float64) aresrecovery.QuotaPolicySource {
	if store == nil {
		return nil
	}
	return &evolutionQuotaPolicySource{store: store, defaultBudget: defaultBudget}
}

var _ aresrecovery.QuotaPolicySource = (*evolutionQuotaPolicySource)(nil)

// ActiveQuotaPolicy derives the current resource budget from the active
// evolution strategy's quota.budget param. With no active strategy (or no
// quota param) the configured default budget is returned.
func (s *evolutionQuotaPolicySource) ActiveQuotaPolicy(ctx context.Context) (aresrecovery.QuotaPolicy, error) {
	st, err := s.store.GetActive(ctx)
	if err != nil {
		if errors.Is(err, evolution.ErrNoActiveStrategy) {
			return aresrecovery.QuotaPolicy{Budget: s.defaultBudget}, nil
		}
		return aresrecovery.QuotaPolicy{}, fmt.Errorf("bootstrap quota policy: active strategy: %w", err)
	}
	if st == nil {
		return aresrecovery.QuotaPolicy{Budget: s.defaultBudget}, nil
	}

	v, ok := st.Params[quotaBudgetParam]
	if !ok {
		// No evolution budget deployed yet: keep the configured budget.
		return aresrecovery.QuotaPolicy{Budget: s.defaultBudget}, nil
	}
	budget, err := asResourceBudget(v)
	if err != nil {
		return aresrecovery.QuotaPolicy{}, fmt.Errorf("bootstrap quota policy: %s: %w", quotaBudgetParam, err)
	}
	return aresrecovery.QuotaPolicy{Budget: budget}, nil
}

// asResourceBudget converts a quota.budget param (JSON object of numbers)
// into a name → amount budget map.
func asResourceBudget(v any) (map[string]float64, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expected object, got %T (%v)", v, v)
	}
	out := make(map[string]float64, len(m))
	for name, raw := range m {
		f, err := asFloat(raw)
		if err != nil {
			return nil, fmt.Errorf("budget %q: %w", name, err)
		}
		out[name] = f
	}
	return out, nil
}

// asFloat converts a JSON number (or int) to float64.
func asFloat(v any) (float64, error) {
	switch t := v.(type) {
	case float64:
		return t, nil
	case float32:
		return float64(t), nil
	case int:
		return float64(t), nil
	case int64:
		return float64(t), nil
	case string:
		var f float64
		if _, err := fmt.Sscanf(t, "%f", &f); err == nil {
			return f, nil
		}
	}
	return 0, fmt.Errorf("expected number, got %T (%v)", v, v)
}
