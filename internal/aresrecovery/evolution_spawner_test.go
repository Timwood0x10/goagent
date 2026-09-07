package aresrecovery

import (
	"context"
	"errors"
	"testing"

	"github.com/Timwood0x10/ares/internal/fabric/agent"
)

// stubSpawnPolicySource returns a fixed policy for tests.
type stubSpawnPolicySource struct {
	policy SpawnPolicy
	err    error
}

func (s *stubSpawnPolicySource) ActiveSpawnPolicy(context.Context) (SpawnPolicy, error) {
	return s.policy, s.err
}

// TestEvolutionSpawnerRespectsTiming verifies spawn timing: a disabled policy
// rejects spawn with ErrSpawnDisabled.
func TestEvolutionSpawnerRespectsTiming(t *testing.T) {
	agents := agentfabric.NewFabric()
	src := &stubSpawnPolicySource{policy: SpawnPolicy{Enabled: false}}
	spawner := NewEvolutionAwareSpawner(agents, src)

	_, err := spawner.Spawn(context.Background(), agentfabric.SpawnSpec{Identity: "a1"})
	if !errors.Is(err, ErrSpawnDisabled) {
		t.Fatalf("want ErrSpawnDisabled, got %v", err)
	}
	if len(agents.Agents()) != 0 {
		t.Fatal("no agent must be spawned when evolution disables spawning")
	}
}

// TestEvolutionSpawnerRespectsQuantity verifies spawn quantity: reaching
// MaxConcurrent rejects further spawns with ErrSpawnLimitReached.
func TestEvolutionSpawnerRespectsQuantity(t *testing.T) {
	agents := agentfabric.NewFabric()
	src := &stubSpawnPolicySource{policy: SpawnPolicy{Enabled: true, MaxConcurrent: 2}}
	spawner := NewEvolutionAwareSpawner(agents, src)
	ctx := context.Background()

	if _, err := spawner.Spawn(ctx, agentfabric.SpawnSpec{Identity: "a1"}); err != nil {
		t.Fatalf("first spawn: %v", err)
	}
	if _, err := spawner.Spawn(ctx, agentfabric.SpawnSpec{Identity: "a2"}); err != nil {
		t.Fatalf("second spawn: %v", err)
	}
	if _, err := spawner.Spawn(ctx, agentfabric.SpawnSpec{Identity: "a3"}); !errors.Is(err, ErrSpawnLimitReached) {
		t.Fatalf("third spawn must hit the cap, got %v", err)
	}
}

// TestEvolutionSpawnerAppliesPreferredCapabilities verifies spawn type:
// evolution's PreferredCapabilities are merged into the spec without replacing
// the caller's explicit ones (dedup).
func TestEvolutionSpawnerAppliesPreferredCapabilities(t *testing.T) {
	agents := agentfabric.NewFabric()
	src := &stubSpawnPolicySource{policy: SpawnPolicy{
		Enabled:               true,
		PreferredCapabilities: []string{"code", "review", "code"}, // dup on purpose
	}}
	spawner := NewEvolutionAwareSpawner(agents, src)

	a, err := spawner.Spawn(context.Background(), agentfabric.SpawnSpec{
		Identity:     "a1",
		Capabilities: []string{"code"},
	})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	got := map[string]bool{}
	for _, c := range a.Capabilities {
		got[c] = true
	}
	if !got["code"] || !got["review"] {
		t.Fatalf("capabilities must merge caller+preferred, got %v", a.Capabilities)
	}
	if len(a.Capabilities) != 2 {
		t.Fatalf("duplicates must be deduplicated, got %v", a.Capabilities)
	}
}

// TestEvolutionSpawnerPolicyErrorPropagates verifies a policy-source failure
// surfaces instead of silently spawning.
func TestEvolutionSpawnerPolicyErrorPropagates(t *testing.T) {
	agents := agentfabric.NewFabric()
	src := &stubSpawnPolicySource{err: errors.New("policy store down")}
	spawner := NewEvolutionAwareSpawner(agents, src)

	if _, err := spawner.Spawn(context.Background(), agentfabric.SpawnSpec{Identity: "a1"}); err == nil {
		t.Fatal("policy error must propagate")
	}
	if len(agents.Agents()) != 0 {
		t.Fatal("no agent must be spawned when the policy lookup fails")
	}
}

// TestEvolutionSpawnerNilSourceDefaultsOpen verifies a nil policy source
// behaves as a plain spawner (enabled + unlimited).
func TestEvolutionSpawnerNilSourceDefaultsOpen(t *testing.T) {
	agents := agentfabric.NewFabric()
	spawner := NewEvolutionAwareSpawner(agents, nil)

	if _, err := spawner.Spawn(context.Background(), agentfabric.SpawnSpec{Identity: "a1"}); err != nil {
		t.Fatalf("nil source must spawn normally, got %v", err)
	}
	if len(agents.Agents()) != 1 {
		t.Fatal("agent must be created with nil source")
	}
}
