package ares_bootstrap

import (
	"context"
	"errors"
	"testing"

	"github.com/Timwood0x10/ares/internal/aresrecovery"
	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
)

// TestNewQuotaPolicySourceNilStore verifies a nil store yields nil (skip).
func TestNewQuotaPolicySourceNilStore(t *testing.T) {
	if got := NewQuotaPolicySource(nil, nil); got != nil {
		t.Fatalf("nil store must yield nil source, got %v", got)
	}
}

// TestActiveQuotaPolicyDefaultsToConfigBudget verifies no active strategy (or
// no quota param) falls back to the configured default budget instead of
// wiping it.
func TestActiveQuotaPolicyDefaultsToConfigBudget(t *testing.T) {
	defaultBudget := map[string]float64{"cpu": 8, "memory": 8192}

	cases := []*stubStrategyStore{
		{},                                      // empty store, nil active
		{active: &evolution.Strategy{ID: "s1"}}, // strategy without quota param
	}
	for _, store := range cases {
		src := NewQuotaPolicySource(store, defaultBudget)
		policy, err := src.ActiveQuotaPolicy(context.Background())
		if err != nil {
			t.Fatalf("ActiveQuotaPolicy: %v", err)
		}
		if policy.Budget["cpu"] != 8 || policy.Budget["memory"] != 8192 {
			t.Fatalf("must fall back to default budget, got %v", policy.Budget)
		}
	}
}

// TestActiveQuotaPolicyNoActiveStrategy verifies ErrNoActiveStrategy maps to
// the default budget instead of surfacing as an error.
func TestActiveQuotaPolicyNoActiveStrategy(t *testing.T) {
	src := NewQuotaPolicySource(&stubStrategyStore{err: evolution.ErrNoActiveStrategy}, map[string]float64{"cpu": 2})
	policy, err := src.ActiveQuotaPolicy(context.Background())
	if err != nil {
		t.Fatalf("ErrNoActiveStrategy must map to default, got %v", err)
	}
	if policy.Budget["cpu"] != 2 {
		t.Fatalf("want default budget, got %v", policy.Budget)
	}
}

// TestActiveQuotaPolicyFromParams verifies the quota.budget param overrides
// the configured default.
func TestActiveQuotaPolicyFromParams(t *testing.T) {
	src := NewQuotaPolicySource(&stubStrategyStore{
		active: &evolution.Strategy{
			ID: "s1",
			Params: map[string]any{
				"quota.budget": map[string]any{"cpu": float64(16), "memory": float64(16384)},
			},
		},
	}, map[string]float64{"cpu": 8})
	policy, err := src.ActiveQuotaPolicy(context.Background())
	if err != nil {
		t.Fatalf("ActiveQuotaPolicy: %v", err)
	}
	if policy.Budget["cpu"] != 16 || policy.Budget["memory"] != 16384 {
		t.Fatalf("evolution budget must override config, got %v", policy.Budget)
	}
}

// TestActiveQuotaPolicyTypeErrors verifies malformed quota params surface as
// errors instead of being silently ignored.
func TestActiveQuotaPolicyTypeErrors(t *testing.T) {
	cases := []map[string]any{
		{"quota.budget": "lots"},
		{"quota.budget": map[string]any{"cpu": "lots"}},
		{"quota.budget": []any{1, 2}},
	}
	for _, params := range cases {
		src := NewQuotaPolicySource(&stubStrategyStore{
			active: &evolution.Strategy{ID: "s1", Params: params},
		}, nil)
		if _, err := src.ActiveQuotaPolicy(context.Background()); err == nil {
			t.Fatalf("params %v must error, got nil", params)
		}
	}
}

// TestActiveQuotaPolicyStoreErrorPropagates verifies a store failure surfaces.
func TestActiveQuotaPolicyStoreErrorPropagates(t *testing.T) {
	src := NewQuotaPolicySource(&stubStrategyStore{err: errors.New("store down")}, nil)
	if _, err := src.ActiveQuotaPolicy(context.Background()); err == nil {
		t.Fatal("store error must propagate")
	}
}

// TestQuotaAdapterSatisfiesContract verifies the adapter implements the
// aresrecovery.QuotaPolicySource contract.
func TestQuotaAdapterSatisfiesContract(t *testing.T) {
	var _ aresrecovery.QuotaPolicySource = (*evolutionQuotaPolicySource)(nil)
}
