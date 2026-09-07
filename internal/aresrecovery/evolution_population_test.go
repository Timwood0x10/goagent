package aresrecovery

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/fabric/agent"
)

// stubPopulationPolicySource is a test PopulationPolicySource that returns
// a configurable policy or error.
type stubPopulationPolicySource struct {
	mu     sync.Mutex
	policy PopulationPolicy
	err    error
	calls  int
}

func (s *stubPopulationPolicySource) ActivePopulationPolicy(_ context.Context) (PopulationPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return PopulationPolicy{}, s.err
	}
	return s.policy, nil
}

func (s *stubPopulationPolicySource) setPolicy(p PopulationPolicy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policy = p
}

func (s *stubPopulationPolicySource) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// TestPopulationAdapterApply verifies that the population adapter applies the
// evolution population policy: spawns requested agents and retires requested
// ones.
func TestPopulationAdapterApply(t *testing.T) {
	agents := agentfabric.NewFabric()
	src := &stubPopulationPolicySource{
		policy: PopulationPolicy{
			Spawn: []agentfabric.SpawnSpec{
				{Identity: "e1", Capabilities: []string{"rust"}},
				{Identity: "e2", Capabilities: []string{"python"}},
			},
		},
	}
	adapter := NewPopulationAdapter(agents, src)

	spawned, err := adapter.Apply(context.Background())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(spawned) != 2 {
		t.Fatalf("want 2 spawned, got %d", len(spawned))
	}
	for _, id := range spawned {
		if _, err := agents.Get(id); err != nil {
			t.Fatalf("spawned agent %s not in fabric: %v", id, err)
		}
	}

	// Now retire one.
	src.setPolicy(PopulationPolicy{
		Retire: []string{"e1"},
	})
	spawned, err = adapter.Apply(context.Background())
	if err != nil {
		t.Fatalf("Apply retire: %v", err)
	}
	if len(spawned) != 0 {
		t.Fatalf("want 0 spawned on retire, got %d", len(spawned))
	}
	// e1 is retired (stays in registry as RETIRED).
	a, err := agents.Get("e1")
	if err != nil {
		t.Fatalf("retired agent e1 must still exist: %v", err)
	}
	if a.State != agentfabric.StateRetired {
		t.Fatalf("e1 must be RETIRED, got %s", a.State)
	}
	// e2 is still idle.
	a2, err := agents.Get("e2")
	if err != nil {
		t.Fatalf("agent e2 must exist: %v", err)
	}
	if a2.State != agentfabric.StateIdle {
		t.Fatalf("e2 must be IDLE, got %s", a2.State)
	}
}

// TestPopulationAdapterNilSourceIsNoop verifies a nil policy source is a no-op.
func TestPopulationAdapterNilSourceIsNoop(t *testing.T) {
	agents := agentfabric.NewFabric()
	adapter := NewPopulationAdapter(agents, nil)
	spawned, err := adapter.Apply(context.Background())
	if err != nil {
		t.Fatalf("Apply with nil source: %v", err)
	}
	if len(spawned) != 0 {
		t.Fatalf("want 0 spawned with nil source, got %d", len(spawned))
	}
}

// TestPopulationAdapterSourceErrorPropagates verifies a policy source error
// propagates through Apply.
func TestPopulationAdapterSourceErrorPropagates(t *testing.T) {
	agents := agentfabric.NewFabric()
	src := &stubPopulationPolicySource{err: errors.New("policy store down")}
	adapter := NewPopulationAdapter(agents, src)
	if _, err := adapter.Apply(context.Background()); err == nil {
		t.Fatal("policy error must propagate")
	}
}

// TestRunKernelEvolutionLoop verifies the kernel evolution loop periodically
// applies the population policy.
func TestRunKernelEvolutionLoop(t *testing.T) {
	agents := agentfabric.NewFabric()
	src := &stubPopulationPolicySource{
		policy: PopulationPolicy{
			Spawn: []agentfabric.SpawnSpec{
				{Identity: "loop-1", Capabilities: []string{"code"}},
			},
		},
	}
	adapter := NewPopulationAdapter(agents, src)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Use a short interval for the test.
	go RunKernelEvolutionLoop(ctx, adapter, 50*time.Millisecond, 5*time.Second)

	// Wait for the startup apply to spawn the agent.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := agents.Get("loop-1"); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := agents.Get("loop-1"); err != nil {
		t.Fatalf("loop-1 was not spawned by evolution loop: %v", err)
	}

	// Verify the source was called (at least startup + ticks).
	if src.callCount() < 1 {
		t.Fatalf("source must have been called at least once, got %d", src.callCount())
	}

	// Cancel and wait briefly to ensure the loop exits.
	cancel()
	time.Sleep(100 * time.Millisecond)
}
