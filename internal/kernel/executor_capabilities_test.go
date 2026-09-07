package kernel

import (
	"context"
	"testing"

	"github.com/Timwood0x10/ares/internal/agentfabric"
	"github.com/Timwood0x10/ares/internal/taskfabric"
)

// TestCapabilitiesIncludesFabricAgents pins the M4-D fix: with an empty
// static pool (production fabric-only mode), Capabilities must still report
// the live fabric population's advertised sets — otherwise the
// graph-submission endpoint would 400 every request for lack of a routable
// capability.
func TestCapabilitiesIncludesFabricAgents(t *testing.T) {
	ctx := context.Background()
	sched := New(taskfabric.NewFabric(), map[string]CapabilityExecutor{}, NewLoadTracker())

	if got := sched.Capabilities(); len(got) != 0 {
		t.Fatalf("empty scheduler reports %v, want none", got)
	}

	agents := agentfabric.NewFabric()
	if _, err := agents.Spawn(ctx, agentfabric.SpawnSpec{
		Identity:     "p1",
		Capabilities: []string{"ares/plan", "tool/grep"},
	}); err != nil {
		t.Fatalf("spawn p1: %v", err)
	}
	if _, err := agents.Spawn(ctx, agentfabric.SpawnSpec{
		Identity:     "p2",
		Capabilities: []string{"ares/plan", "tool/read"},
	}); err != nil {
		t.Fatalf("spawn p2: %v", err)
	}
	sched.WithAgentFabric(agents)

	got := sched.Capabilities()
	want := map[string]bool{"ares/plan": true, "tool/grep": true, "tool/read": true}
	if len(got) != len(want) {
		t.Fatalf("capabilities = %v, want exactly the fabric union", got)
	}
	for _, c := range got {
		if !want[c] {
			t.Errorf("capabilities contain %q, want only fabric-advertised sets", c)
		}
	}
}
