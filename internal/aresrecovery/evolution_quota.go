package aresrecovery

import (
	"context"
	"errors"
	"fmt"

	"github.com/Timwood0x10/ares/internal/fabric/agent"
)

// Evolution-driven resource allocation: the Evolution system
// adjusts CPU / memory quota weights at runtime. The quota manager applies the
// evolution-produced budget to the Agent Fabric's resource admission control
// without recreating the fabric. As with spawn decisions,
// "Evolution decides; Kernel enforces" — the manager only pushes the new
// budget through the existing enforcement primitive.
//
// The manager depends on a QuotaPolicySource (defined here, at the consumer)
// so aresrecovery never imports the evolution package.

// QuotaPolicy is the evolution-produced resource allocation for one point in
// time: a name → max amount budget (e.g. {"cpu": 8, "memory": 8192}).
type QuotaPolicy struct {
	// Budget is the resource budget to apply. Nil/empty disables enforcement.
	Budget map[string]float64
}

// QuotaPolicySource supplies the current evolution resource policy.
type QuotaPolicySource interface {
	// ActiveQuotaPolicy returns the currently deployed resource policy.
	ActiveQuotaPolicy(ctx context.Context) (QuotaPolicy, error)
}

// EvolutionAwareQuotaManager adjusts the Agent Fabric's resource budget from
// the evolution policy. Apply() is idempotent and safe to call
// repeatedly (e.g. after each evolution generation): it replaces the budget
// in place via agentfabric.UpdateResourceBudget.
type EvolutionAwareQuotaManager struct {
	agents *agentfabric.Fabric
	source QuotaPolicySource
}

// NewEvolutionAwareQuotaManager wires the quota manager to the Agent Fabric
// and the evolution policy source.
//
// Args:
//   - agents: the Agent Fabric whose resource budget is adjusted.
//   - source: the evolution policy source (may be nil → Apply is a no-op).
//
// Returns:
//   - *EvolutionAwareQuotaManager: ready to Apply.
func NewEvolutionAwareQuotaManager(agents *agentfabric.Fabric, source QuotaPolicySource) *EvolutionAwareQuotaManager {
	return &EvolutionAwareQuotaManager{agents: agents, source: source}
}

// Apply pushes the current evolution resource policy into the Agent Fabric's
// budget. A nil source or nil fabric is a no-op.
//
// Args:
//   - ctx: for the policy lookup.
//
// Returns:
//   - error: the policy-source error, or an error when no fabric is wired.
func (m *EvolutionAwareQuotaManager) Apply(ctx context.Context) error {
	if m.agents == nil {
		return errors.New("aresrecovery: evolution quota manager has no agent fabric")
	}
	if m.source == nil {
		return nil // no evolution source wired — leave the budget untouched
	}
	policy, err := m.source.ActiveQuotaPolicy(ctx)
	if err != nil {
		return fmt.Errorf("evolution quota policy: %w", err)
	}
	m.agents.UpdateResourceBudget(policy.Budget)
	return nil
}
