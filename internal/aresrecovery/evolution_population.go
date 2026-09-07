package aresrecovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Timwood0x10/ares/internal/fabric/agent"
)

// PopulationPolicy is the evolution-produced agent population decision for one
// point in time (P6: Runtime Adaptation — agent population). The Evolution
// system computes the desired population delta; the Kernel enforces it through
// the existing spawn/retire primitives.
//
// "Evolution decides; Kernel enforces" — the adapter applies the decision
// through agentfabric.Spawn/Retire, never bypassing the Kernel.
type PopulationPolicy struct {
	// Spawn is the list of agents the evolution system wants to create.
	Spawn []agentfabric.SpawnSpec
	// Retire is the list of agent ids the evolution system wants to retire.
	Retire []string
}

// PopulationPolicySource supplies the current evolution population policy.
// Implementations live in the evolution system (or tests); aresrecovery never
// imports the evolution package.
type PopulationPolicySource interface {
	// ActivePopulationPolicy returns the currently desired population delta.
	ActivePopulationPolicy(ctx context.Context) (PopulationPolicy, error)
}

// PopulationAdapter is the Kernel-side adapter that applies evolution's agent
// population decisions (P6: Runtime Adaptation). It wraps the EvolutionAdapter
// with a policy source so the kernel loop can periodically call AdaptPopulation
// without understanding evolution semantics.
//
// The adapter is safe for concurrent use (it delegates to the fabric's
// serialized primitives). Apply is idempotent: an empty policy is a no-op.
type PopulationAdapter struct {
	adapter *EvolutionAdapter
	source  PopulationPolicySource
}

// NewPopulationAdapter wires the population adapter to the Agent Fabric and
// the evolution policy source.
//
// Args:
//   - agents: the Agent Fabric whose population is managed.
//   - source: the evolution policy source (may be nil → Apply is a no-op).
//
// Returns:
//   - *PopulationAdapter: ready to Apply.
func NewPopulationAdapter(agents *agentfabric.Fabric, source PopulationPolicySource) *PopulationAdapter {
	return &PopulationAdapter{
		adapter: NewEvolutionAdapter(agents, agents),
		source:  source,
	}
}

// Apply pushes the current evolution population policy into the Agent Fabric.
// It spawns the requested agents and retires the requested ones. A nil source
// or nil fabric is a no-op.
//
// Args:
//   - ctx: for event sinks and policy lookup.
//
// Returns:
//   - []string: the ids of spawned agents.
//   - error: the first spawn/retire error encountered.
func (a *PopulationAdapter) Apply(ctx context.Context) ([]string, error) {
	if a.adapter == nil || a.adapter.agents == nil {
		return nil, errors.New("aresrecovery: population adapter has no agent fabric")
	}
	if a.source == nil {
		return nil, nil // no evolution source wired — leave population untouched
	}
	policy, err := a.source.ActivePopulationPolicy(ctx)
	if err != nil {
		return nil, fmt.Errorf("evolution population policy: %w", err)
	}
	return a.adapter.AdaptPopulation(ctx, policy.Spawn, policy.Retire)
}

// evolutionApplyInterval is how often the kernel evolution loop applies the
// population policy. The GA evolution ticker runs on a 5-minute cadence, so a
// 1-minute apply loop keeps a deployed policy effective within a reasonable
// window. It is the default when kernel.evolution_apply_interval is not
// configured.
const evolutionApplyInterval = time.Minute

// evolutionApplyTimeout bounds one population policy application. A hung
// policy store must not stall the loop, so every Apply runs under this
// timeout.
const evolutionApplyTimeout = 30 * time.Second

// RunKernelEvolutionLoop periodically applies the evolution population policy
// to the Agent Fabric (P6: Runtime Adaptation — agent population). It applies
// once at startup so an already-deployed policy is effective immediately,
// then re-applies on a fixed interval — Apply is idempotent (an empty policy
// is a no-op).
//
// Args:
//   - ctx: stops the loop.
//   - adapter: the population adapter (nil disables the loop).
//   - interval: how often to apply; <= 0 falls back to evolutionApplyInterval.
//   - timeout: per-Apply timeout; <= 0 falls back to evolutionApplyTimeout.
func RunKernelEvolutionLoop(ctx context.Context, adapter *PopulationAdapter, interval, timeout time.Duration) {
	if adapter == nil {
		return
	}
	if interval <= 0 {
		interval = evolutionApplyInterval
	}
	if timeout <= 0 {
		timeout = evolutionApplyTimeout
	}
	apply := func(phase string) {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("kernel: evolution apply panic",
					slog.String("phase", phase), slog.Any("panic", r))
			}
		}()
		applyCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		spawned, err := adapter.Apply(applyCtx)
		if err != nil {
			slog.Error("kernel: evolution apply failed",
				slog.String("phase", phase), slog.Any("error", err))
			return
		}
		if len(spawned) > 0 {
			slog.Info("kernel: evolution apply spawned agents",
				slog.String("phase", phase), slog.Int("count", len(spawned)))
		}
	}
	apply("startup")
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			apply("tick")
		case <-ctx.Done():
			return
		}
	}
}
