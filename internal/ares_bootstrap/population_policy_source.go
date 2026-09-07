// Package ares_bootstrap — evolution population policy adapter.
package ares_bootstrap

import (
	"context"
	"errors"
	"fmt"

	"github.com/Timwood0x10/ares/internal/aresrecovery"
	"github.com/Timwood0x10/ares/internal/fabric/agent"
	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
)

// population policy param keys read from the active evolution strategy's Params
// map. The evolution system evolves these values; the Kernel enforces them
// through aresrecovery.PopulationAdapter (P6: Runtime Adaptation).
const (
	// populationSpawnParam ("population.spawn") is a list of spawn specs
	// (each a map with identity/capabilities) that the evolution system
	// wants the Kernel to create.
	populationSpawnParam = "population.spawn"
	// populationRetireParam ("population.retire") is a list of agent ids
	// that the evolution system wants the Kernel to retire.
	populationRetireParam = "population.retire"
)

// evolutionPopulationPolicySource adapts an evolution.StrategyStore to the
// aresrecovery.PopulationPolicySource contract, keeping aresrecovery decoupled
// from the evolution engine internals (same pattern as spawn_policy_source.go).
type evolutionPopulationPolicySource struct {
	store evolution.StrategyStore
}

// NewPopulationPolicySource wraps an evolution StrategyStore as an
// aresrecovery.PopulationPolicySource. Returns nil when the store is nil so
// callers can skip injection safely (serve runs without GA guidance).
func NewPopulationPolicySource(store evolution.StrategyStore) aresrecovery.PopulationPolicySource {
	if store == nil {
		return nil
	}
	return &evolutionPopulationPolicySource{store: store}
}

var _ aresrecovery.PopulationPolicySource = (*evolutionPopulationPolicySource)(nil)

// ActivePopulationPolicy derives the current population delta from the active
// evolution strategy's params. With no active strategy (or no population params)
// the policy is empty (no spawn, no retire), preserving prior behavior.
func (s *evolutionPopulationPolicySource) ActivePopulationPolicy(ctx context.Context) (aresrecovery.PopulationPolicy, error) {
	st, err := s.store.GetActive(ctx)
	if err != nil {
		if errors.Is(err, evolution.ErrNoActiveStrategy) {
			return aresrecovery.PopulationPolicy{}, nil // no deployed strategy: no population change
		}
		return aresrecovery.PopulationPolicy{}, fmt.Errorf("bootstrap population policy: active strategy: %w", err)
	}
	if st == nil {
		return aresrecovery.PopulationPolicy{}, nil
	}
	policy := aresrecovery.PopulationPolicy{}
	if v, ok := st.Params[populationSpawnParam]; ok {
		spawn, err := asSpawnSpecs(v)
		if err != nil {
			return aresrecovery.PopulationPolicy{}, fmt.Errorf("bootstrap population policy: %s: %w", populationSpawnParam, err)
		}
		policy.Spawn = spawn
	}
	if v, ok := st.Params[populationRetireParam]; ok {
		retire, err := asStringSlice(v)
		if err != nil {
			return aresrecovery.PopulationPolicy{}, fmt.Errorf("bootstrap population policy: %s: %w", populationRetireParam, err)
		}
		policy.Retire = retire
	}
	return policy, nil
}

// asSpawnSpecs converts a population.spawn param (JSON array of objects) into
// a list of agentfabric.SpawnSpec. Each object may carry "identity" and
// "capabilities" (string list). Missing fields default to empty (the Fabric
// auto-assigns identity when empty).
func asSpawnSpecs(v any) ([]agentfabric.SpawnSpec, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("expected array, got %T (%v)", v, v)
	}
	out := make([]agentfabric.SpawnSpec, 0, len(arr))
	for i, elem := range arr {
		m, ok := elem.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("element %d: expected object, got %T", i, elem)
		}
		spec := agentfabric.SpawnSpec{}
		if id, ok := m["identity"].(string); ok {
			spec.Identity = id
		}
		if caps, err := asStringSlice(m["capabilities"]); err == nil {
			spec.Capabilities = caps
		}
		out = append(out, spec)
	}
	return out, nil
}
