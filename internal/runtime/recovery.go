package runtime

import (
	"context"
	"sync"

	"github.com/Timwood0x10/ares/internal/agents/base"
)

// BasicRecoveryPlugin implements RecoveryPlugin with a simple allowlist-based
// recovery policy. Steps whose IDs are in the allowlist are eligible for
// recovery; all others are not.
type BasicRecoveryPlugin struct {
	mu        sync.Mutex
	name      string
	allowlist map[string]bool // step IDs eligible for recovery
}

// NewBasicRecoveryPlugin creates a BasicRecoveryPlugin.
func NewBasicRecoveryPlugin(name string) *BasicRecoveryPlugin {
	if name == "" {
		name = "recovery"
	}
	return &BasicRecoveryPlugin{
		name:      name,
		allowlist: make(map[string]bool),
	}
}

// AllowStep marks a step ID as eligible for recovery.
func (p *BasicRecoveryPlugin) AllowStep(stepID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allowlist[stepID] = true
}

// RevokeStep removes a step ID from the recovery allowlist.
func (p *BasicRecoveryPlugin) RevokeStep(stepID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.allowlist, stepID)
}

// Name returns the plugin name.
func (p *BasicRecoveryPlugin) Name() string { return p.name }

// Capabilities returns the capabilities.
func (p *BasicRecoveryPlugin) Capabilities() []Capability {
	return []Capability{CapRecovery}
}

// Start initializes the recovery plugin.
func (p *BasicRecoveryPlugin) Start(_ context.Context, _ EventBus) error { return nil }

// Stop shuts down the recovery plugin.
func (p *BasicRecoveryPlugin) Stop(_ context.Context) error { return nil }

// ShouldRecover returns true if the failed step's ID is in the allowlist.
func (p *BasicRecoveryPlugin) ShouldRecover(_ context.Context, failure StepFailure, _ ExecutionState) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	ok := p.allowlist[failure.StepID]
	if ok {
		log.Debug("recovery plugin: allowing recovery",
			"step_id", failure.StepID,
			"execution_id", failure.ExecutionID,
		)
	}
	return ok
}

var _ RecoveryPlugin = (*BasicRecoveryPlugin)(nil)

// RecoverSnapshotOrEvents attempts snapshot-first recovery for agent state.
func RecoverSnapshotOrEvents(ctx context.Context, store base.SnapshotStore, agentID string, eventFn func() map[string]any) map[string]any {
	if store != nil {
		snap, err := store.Load(ctx, agentID)
		if err != nil {
			return eventFn()
		}
		if snap != nil {
			return snap
		}
	}
	return eventFn()
}
