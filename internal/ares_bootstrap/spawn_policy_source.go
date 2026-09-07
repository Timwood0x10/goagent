// Package ares_bootstrap — evolution spawn policy adapter.
package ares_bootstrap

import (
	"context"
	"errors"
	"fmt"

	"github.com/Timwood0x10/ares/internal/aresrecovery"
	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
)

// Spawn policy param keys read from the active evolution strategy's Params
// map. The evolution system evolves these values; the Kernel enforces them
// through aresrecovery.EvolutionAwareSpawner.
const (
	// spawnEnabledParam ("spawn.enabled") gates spawning (default true).
	spawnEnabledParam = "spawn.enabled"
	// spawnMaxConcurrentParam ("spawn.max_concurrent") caps live agents
	// (<= 0 means unlimited, default).
	spawnMaxConcurrentParam = "spawn.max_concurrent"
	// spawnPreferredCapabilitiesParam ("spawn.preferred_capabilities") lists
	// capability types evolution prefers for new agents.
	spawnPreferredCapabilitiesParam = "spawn.preferred_capabilities"
)

// evolutionSpawnPolicySource adapts an evolution.StrategyStore to the
// aresrecovery.SpawnPolicySource contract, keeping aresrecovery decoupled
// from the evolution engine internals (same pattern as strategy_adapter.go).
type evolutionSpawnPolicySource struct {
	store evolution.StrategyStore
}

// NewSpawnPolicySource wraps an evolution StrategyStore as an
// aresrecovery.SpawnPolicySource. Returns nil when the store is nil so
// callers can skip injection safely (serve runs without GA guidance).
func NewSpawnPolicySource(store evolution.StrategyStore) aresrecovery.SpawnPolicySource {
	if store == nil {
		return nil
	}
	return &evolutionSpawnPolicySource{store: store}
}

var _ aresrecovery.SpawnPolicySource = (*evolutionSpawnPolicySource)(nil)

// ActiveSpawnPolicy derives the current spawn policy from the active
// evolution strategy's params. With no active strategy (or no spawn params)
// the policy defaults to enabled + unlimited, preserving plain spawn.
//
//nolint:gocyclo // param extraction is linear, not deeply nested.
func (s *evolutionSpawnPolicySource) ActiveSpawnPolicy(ctx context.Context) (aresrecovery.SpawnPolicy, error) {
	policy := aresrecovery.SpawnPolicy{Enabled: true}

	st, err := s.store.GetActive(ctx)
	if err != nil {
		if errors.Is(err, evolution.ErrNoActiveStrategy) {
			return policy, nil // no deployed strategy: plain spawn
		}
		return aresrecovery.SpawnPolicy{}, fmt.Errorf("bootstrap spawn policy: active strategy: %w", err)
	}
	if st == nil {
		return policy, nil
	}

	if v, ok := st.Params[spawnEnabledParam]; ok {
		enabled, err := asBool(v)
		if err != nil {
			return aresrecovery.SpawnPolicy{}, fmt.Errorf("bootstrap spawn policy: %s: %w", spawnEnabledParam, err)
		}
		policy.Enabled = enabled
	}
	if v, ok := st.Params[spawnMaxConcurrentParam]; ok {
		max, err := asInt(v)
		if err != nil {
			return aresrecovery.SpawnPolicy{}, fmt.Errorf("bootstrap spawn policy: %s: %w", spawnMaxConcurrentParam, err)
		}
		policy.MaxConcurrent = max
	}
	if v, ok := st.Params[spawnPreferredCapabilitiesParam]; ok {
		caps, err := asStringSlice(v)
		if err != nil {
			return aresrecovery.SpawnPolicy{}, fmt.Errorf("bootstrap spawn policy: %s: %w", spawnPreferredCapabilitiesParam, err)
		}
		policy.PreferredCapabilities = caps
	}
	return policy, nil
}

// asBool converts a strategy param to bool, accepting bool or string.
func asBool(v any) (bool, error) {
	switch t := v.(type) {
	case bool:
		return t, nil
	case string:
		switch t {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
	}
	return false, fmt.Errorf("expected bool, got %T (%v)", v, v)
}

// asInt converts a strategy param to int, accepting float64 (JSON numbers),
// int, or string.
func asInt(v any) (int, error) {
	switch t := v.(type) {
	case float64:
		return int(t), nil
	case int:
		return t, nil
	case int64:
		return int(t), nil
	case string:
		var n int
		if _, err := fmt.Sscanf(t, "%d", &n); err == nil {
			return n, nil
		}
	}
	return 0, fmt.Errorf("expected number, got %T (%v)", v, v)
}

// asStringSlice converts a strategy param to []string, accepting []any
// (JSON arrays), []string, or a single string.
func asStringSlice(v any) ([]string, error) {
	switch t := v.(type) {
	case []string:
		return t, nil
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("expected string element, got %T", e)
			}
			out = append(out, s)
		}
		return out, nil
	case string:
		if t == "" {
			return nil, nil
		}
		return []string{t}, nil
	}
	return nil, fmt.Errorf("expected string list, got %T (%v)", v, v)
}
