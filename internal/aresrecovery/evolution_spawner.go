package aresrecovery

import (
	"context"
	"errors"
	"fmt"

	"github.com/Timwood0x10/ares/internal/fabric/agent"
)

// Evolution-driven spawn decisions: the Evolution system shapes
// when agents are spawned, how many, and of what capability type. The Kernel
// enforces the decisions through the existing spawn primitive — "Evolution
// decides; Kernel enforces", mirroring "Agent decides; Kernel enforces".
//
// The spawner depends on a SpawnPolicySource (defined here, at the consumer)
// so aresrecovery never imports the evolution package; the evolution system
// (or any test) implements the source.

// SpawnPolicy is the evolution-produced spawn decision for one point in time.
type SpawnPolicy struct {
	// Enabled gates spawn timing: when false, spawning new agents is
	// suspended (e.g. during a population-down phase).
	Enabled bool
	// MaxConcurrent caps the number of live agents (spawn quantity). <= 0
	// means unlimited (no population cap from evolution).
	MaxConcurrent int
	// PreferredCapabilities are the capability types evolution prefers for
	// new agents; they are merged into the spawn spec's capabilities (not
	// replacing the caller's explicit ones).
	PreferredCapabilities []string
}

// SpawnPolicySource supplies the current evolution spawn policy.
type SpawnPolicySource interface {
	// ActiveSpawnPolicy returns the currently deployed spawn policy.
	ActiveSpawnPolicy(ctx context.Context) (SpawnPolicy, error)
}

// ErrSpawnDisabled is returned when evolution suspends spawning.
var ErrSpawnDisabled = errors.New("aresrecovery: evolution disabled spawning")

// ErrSpawnLimitReached is returned when evolution's population cap is reached.
var ErrSpawnLimitReached = errors.New("aresrecovery: evolution spawn limit reached")

// EvolutionAwareSpawner is the Kernel-side spawner that consults evolution
// before creating an agent. It wraps the Agent Fabric spawn
// primitive with policy checks:
//
//  1. Timing:   ErrSpawnDisabled when the policy says spawning is off.
//  2. Quantity: ErrSpawnLimitReached when MaxConcurrent live agents exist.
//  3. Type:     PreferredCapabilities are appended to the spec.
type EvolutionAwareSpawner struct {
	agents *agentfabric.Fabric
	source SpawnPolicySource
}

// NewEvolutionAwareSpawner wires the spawner to the Agent Fabric and the
// evolution policy source.
//
// Args:
//   - agents: the Agent Fabric whose Spawn primitive is wrapped.
//   - source: the evolution policy source (may be nil → policy defaults to
//     enabled + unlimited, i.e. plain spawn).
//
// Returns:
//   - *EvolutionAwareSpawner: ready to Spawn.
func NewEvolutionAwareSpawner(agents *agentfabric.Fabric, source SpawnPolicySource) *EvolutionAwareSpawner {
	return &EvolutionAwareSpawner{agents: agents, source: source}
}

// Spawn creates an agent under the current evolution policy.
//
// Args:
//   - ctx: for the policy lookup and the fabric spawn.
//   - spec: the requested spawn spec; PreferredCapabilities from the policy
//     are appended (deduplicated).
//
// Returns:
//   - *agentfabric.Agent: the spawned agent.
//   - error: ErrSpawnDisabled / ErrSpawnLimitReached, or the fabric error.
func (s *EvolutionAwareSpawner) Spawn(ctx context.Context, spec agentfabric.SpawnSpec) (*agentfabric.Agent, error) {
	return s.spawn(ctx, spec, false)
}

// SpawnForRecovery creates an agent under the evolution policy for the
// RECOVERY path: it applies the timing (Enabled) gate and the
// capability merge, but SKIPS the MaxConcurrent quota check. Recovery spawns
// replace a dead/expired agent — they do not grow the live population, so
// letting a self-healing spawn be rejected by the population cap would strand
// the task permanently (recovery must not be blocked by quota).
//
// Args:
//   - ctx: for the policy lookup and the fabric spawn.
//   - spec: the requested spawn spec; PreferredCapabilities from the policy
//     are appended (deduplicated).
//
// Returns:
//   - *agentfabric.Agent: the spawned agent.
//   - error: ErrSpawnDisabled, or the fabric error.
func (s *EvolutionAwareSpawner) SpawnForRecovery(ctx context.Context, spec agentfabric.SpawnSpec) (*agentfabric.Agent, error) {
	return s.spawn(ctx, spec, true)
}

// spawn implements both spawn paths. skipQuota selects the recovery path
// (MaxConcurrent is not enforced) vs the ordinary spawn (both gates apply).
func (s *EvolutionAwareSpawner) spawn(ctx context.Context, spec agentfabric.SpawnSpec, skipQuota bool) (*agentfabric.Agent, error) {
	if s.agents == nil {
		return nil, errors.New("aresrecovery: evolution spawner has no agent fabric")
	}
	policy := SpawnPolicy{Enabled: true}
	if s.source != nil {
		p, err := s.source.ActiveSpawnPolicy(ctx)
		if err != nil {
			return nil, fmt.Errorf("evolution spawn policy: %w", err)
		}
		policy = p
	}
	if !policy.Enabled {
		return nil, ErrSpawnDisabled
	}
	if !skipQuota && policy.MaxConcurrent > 0 && len(s.agents.Agents()) >= policy.MaxConcurrent {
		return nil, ErrSpawnLimitReached
	}
	// Merge preferred capabilities (dedup, keep caller's explicit ones first).
	spec.Capabilities = mergeCapabilities(spec.Capabilities, policy.PreferredCapabilities)
	return s.agents.Spawn(ctx, spec)
}

// mergeCapabilities appends extra to base, dropping duplicates.
func mergeCapabilities(base, extra []string) []string {
	out := append([]string(nil), base...)
	seen := make(map[string]bool, len(base)+len(extra))
	for _, c := range base {
		seen[c] = true
	}
	for _, c := range extra {
		if c != "" && !seen[c] {
			out = append(out, c)
			seen[c] = true
		}
	}
	return out
}
