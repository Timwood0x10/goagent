package ares_bootstrap

import (
	"context"
	"errors"
	"testing"

	"github.com/Timwood0x10/ares/internal/aresrecovery"
	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
)

// stubStrategyStore is a minimal StrategyStore for tests.
type stubStrategyStore struct {
	active *evolution.Strategy
	err    error
}

func (s *stubStrategyStore) GetActive(context.Context) (*evolution.Strategy, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.active, nil
}

func (s *stubStrategyStore) SetActive(context.Context, *evolution.Strategy) error { return nil }
func (s *stubStrategyStore) GetHistory(context.Context, string, int) ([]*evolution.Strategy, error) {
	return nil, nil
}

// TestNewSpawnPolicySourceNilStore verifies a nil store yields nil (skip).
func TestNewSpawnPolicySourceNilStore(t *testing.T) {
	if got := NewSpawnPolicySource(nil); got != nil {
		t.Fatalf("nil store must yield nil source, got %v", got)
	}
}

// TestActiveSpawnPolicyDefaults verifies no active strategy yields the
// enabled + unlimited default (plain spawn).
func TestActiveSpawnPolicyDefaults(t *testing.T) {
	src := NewSpawnPolicySource(&stubStrategyStore{})
	if src == nil {
		t.Fatal("non-nil store must yield a source")
	}
	policy, err := src.ActiveSpawnPolicy(context.Background())
	if err != nil {
		t.Fatalf("ActiveSpawnPolicy: %v", err)
	}
	if !policy.Enabled || policy.MaxConcurrent != 0 || len(policy.PreferredCapabilities) != 0 {
		t.Fatalf("default policy must be enabled+unlimited, got %+v", policy)
	}
}

// TestActiveSpawnPolicyNoActiveStrategy verifies ErrNoActiveStrategy maps to
// the open default instead of surfacing as an error.
func TestActiveSpawnPolicyNoActiveStrategy(t *testing.T) {
	src := NewSpawnPolicySource(&stubStrategyStore{err: evolution.ErrNoActiveStrategy})
	policy, err := src.ActiveSpawnPolicy(context.Background())
	if err != nil {
		t.Fatalf("ErrNoActiveStrategy must map to default, got %v", err)
	}
	if !policy.Enabled {
		t.Fatal("default policy must be enabled")
	}
}

// TestActiveSpawnPolicyFromParams verifies all three spawn params are read
// from the active strategy's Params map.
func TestActiveSpawnPolicyFromParams(t *testing.T) {
	src := NewSpawnPolicySource(&stubStrategyStore{
		active: &evolution.Strategy{
			ID: "s1",
			Params: map[string]any{
				"spawn.enabled":                "false",
				"spawn.max_concurrent":         float64(4),
				"spawn.preferred_capabilities": []any{"code", "review"},
			},
		},
	})
	policy, err := src.ActiveSpawnPolicy(context.Background())
	if err != nil {
		t.Fatalf("ActiveSpawnPolicy: %v", err)
	}
	if policy.Enabled {
		t.Fatal("spawn.enabled=false must disable spawning")
	}
	if policy.MaxConcurrent != 4 {
		t.Fatalf("want MaxConcurrent=4, got %d", policy.MaxConcurrent)
	}
	if len(policy.PreferredCapabilities) != 2 || policy.PreferredCapabilities[0] != "code" || policy.PreferredCapabilities[1] != "review" {
		t.Fatalf("unexpected preferred capabilities %v", policy.PreferredCapabilities)
	}
}

// TestActiveSpawnPolicyParamTypeErrors verifies malformed params surface as
// errors instead of being silently ignored.
func TestActiveSpawnPolicyParamTypeErrors(t *testing.T) {
	cases := []map[string]any{
		{"spawn.enabled": "maybe"},
		{"spawn.max_concurrent": "lots"},
		{"spawn.preferred_capabilities": []any{42}},
	}
	for _, params := range cases {
		src := NewSpawnPolicySource(&stubStrategyStore{
			active: &evolution.Strategy{ID: "s1", Params: params},
		})
		if _, err := src.ActiveSpawnPolicy(context.Background()); err == nil {
			t.Fatalf("params %v must error, got nil", params)
		}
	}
}

// TestActiveSpawnPolicyStoreErrorPropagates verifies a store failure surfaces
// instead of silently falling back to the open default.
func TestActiveSpawnPolicyStoreErrorPropagates(t *testing.T) {
	src := NewSpawnPolicySource(&stubStrategyStore{err: errors.New("store down")})
	if _, err := src.ActiveSpawnPolicy(context.Background()); err == nil {
		t.Fatal("store error must propagate")
	}
}

// TestAdapterSatisfiesContract verifies the adapter implements the
// aresrecovery.SpawnPolicySource contract.
func TestAdapterSatisfiesContract(t *testing.T) {
	var _ aresrecovery.SpawnPolicySource = (*evolutionSpawnPolicySource)(nil)
}
