package aresrecovery

import (
	"context"
	"errors"
	"testing"

	"github.com/Timwood0x10/ares/internal/fabric/agent"
)

// stubQuotaPolicySource returns a fixed quota policy for tests.
type stubQuotaPolicySource struct {
	policy QuotaPolicy
	err    error
}

func (s *stubQuotaPolicySource) ActiveQuotaPolicy(context.Context) (QuotaPolicy, error) {
	return s.policy, s.err
}

// TestEvolutionQuotaManagerAppliesBudget verifies Apply() pushes the evolution
// budget into the fabric and future spawns are admitted against the new cap.
func TestEvolutionQuotaManagerAppliesBudget(t *testing.T) {
	agents := agentfabric.NewFabric()
	src := &stubQuotaPolicySource{policy: QuotaPolicy{Budget: map[string]float64{"cpu": 4}}}
	mgr := NewEvolutionAwareQuotaManager(agents, src)
	ctx := context.Background()

	if err := mgr.Apply(ctx); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// Two spawns of cpu=2 fit; a third must be rejected.
	if _, err := agents.Spawn(ctx, agentfabric.SpawnSpec{
		Identity:  "a1",
		Resources: map[string]any{"cpu": 2},
	}); err != nil {
		t.Fatalf("spawn a1: %v", err)
	}
	if _, err := agents.Spawn(ctx, agentfabric.SpawnSpec{
		Identity:  "a2",
		Resources: map[string]any{"cpu": 2},
	}); err != nil {
		t.Fatalf("spawn a2: %v", err)
	}
	if _, err := agents.Spawn(ctx, agentfabric.SpawnSpec{
		Identity:  "a3",
		Resources: map[string]any{"cpu": 2},
	}); !errors.Is(err, agentfabric.ErrResourceQuotaExceeded) {
		t.Fatalf("third spawn must exceed the applied budget, got %v", err)
	}
}

// TestEvolutionQuotaManagerDynamicAdjustment verifies the budget can be
// tightened or loosened at runtime (CPU/memory weight dynamic
// adjustment) without recreating the fabric.
func TestEvolutionQuotaManagerDynamicAdjustment(t *testing.T) {
	agents := agentfabric.NewFabric()
	mgr := NewEvolutionAwareQuotaManager(agents, &stubQuotaPolicySource{
		policy: QuotaPolicy{Budget: map[string]float64{"cpu": 8}},
	})
	ctx := context.Background()
	if err := mgr.Apply(ctx); err != nil {
		t.Fatalf("apply wide budget: %v", err)
	}

	// Tighten the budget to cpu=2: future spawns must be rejected.
	mgr.source = &stubQuotaPolicySource{policy: QuotaPolicy{Budget: map[string]float64{"cpu": 2}}}
	if err := mgr.Apply(ctx); err != nil {
		t.Fatalf("apply tight budget: %v", err)
	}
	if _, err := agents.Spawn(ctx, agentfabric.SpawnSpec{
		Identity:  "a1",
		Resources: map[string]any{"cpu": 3},
	}); !errors.Is(err, agentfabric.ErrResourceQuotaExceeded) {
		t.Fatalf("spawn must be rejected under the tightened budget, got %v", err)
	}

	// Loosen again: the same claim now fits.
	mgr.source = &stubQuotaPolicySource{policy: QuotaPolicy{Budget: map[string]float64{"cpu": 8}}}
	if err := mgr.Apply(ctx); err != nil {
		t.Fatalf("apply loose budget: %v", err)
	}
	if _, err := agents.Spawn(ctx, agentfabric.SpawnSpec{
		Identity:  "a1",
		Resources: map[string]any{"cpu": 3},
	}); err != nil {
		t.Fatalf("spawn must be admitted after loosening, got %v", err)
	}
}

// TestEvolutionQuotaManagerPolicyErrorPropagates verifies a policy-source
// failure surfaces and leaves the existing budget untouched.
func TestEvolutionQuotaManagerPolicyErrorPropagates(t *testing.T) {
	agents := agentfabric.NewFabric().WithResourceBudget(map[string]float64{"cpu": 4})
	mgr := NewEvolutionAwareQuotaManager(agents, &stubQuotaPolicySource{err: errors.New("policy store down")})
	if err := mgr.Apply(context.Background()); err == nil {
		t.Fatal("policy error must propagate")
	}
	// Budget unchanged: a cpu=4 spawn still fits.
	if _, err := agents.Spawn(context.Background(), agentfabric.SpawnSpec{
		Identity:  "a1",
		Resources: map[string]any{"cpu": 4},
	}); err != nil {
		t.Fatalf("budget must be untouched on policy error, got %v", err)
	}
}

// TestEvolutionQuotaManagerNilSourceNoOp verifies a nil policy source leaves
// the budget untouched.
func TestEvolutionQuotaManagerNilSourceNoOp(t *testing.T) {
	agents := agentfabric.NewFabric().WithResourceBudget(map[string]float64{"cpu": 4})
	mgr := NewEvolutionAwareQuotaManager(agents, nil)
	if err := mgr.Apply(context.Background()); err != nil {
		t.Fatalf("nil source must be a no-op, got %v", err)
	}
	if _, err := agents.Spawn(context.Background(), agentfabric.SpawnSpec{
		Identity:  "a1",
		Resources: map[string]any{"cpu": 4},
	}); err != nil {
		t.Fatalf("budget must be untouched with nil source, got %v", err)
	}
}
