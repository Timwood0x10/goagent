package kernel

import (
	"context"
	"fmt"
	"testing"

	"github.com/Timwood0x10/ares/internal/fabric/agent"
	"github.com/Timwood0x10/ares/internal/fabric/task"
)

// spawnDrainCandidate spawns one fabric agent under id. With executable=true
// it carries a cognition and satisfies the schedulable-candidate predicate
// (live + IDLE + executable); without one it is managed by the fabric but
// never schedulable, so it must not widen the drain limit.
func spawnDrainCandidate(t *testing.T, ctx context.Context, agents *agentfabric.Fabric, id string, executable bool) {
	t.Helper()
	spec := agentfabric.SpawnSpec{
		Identity:     id,
		Capabilities: []string{"code"},
	}
	if executable {
		spec.CognitionFactory = func([]string) agentfabric.Cognition {
			return &countingCognition{}
		}
	}
	if _, err := agents.Spawn(ctx, spec); err != nil {
		t.Fatalf("spawn %s: %v", id, err)
	}
}

// TestDrainLimit pins the drain parallelism fallback chain (the production
// concurrency=1 bug): an explicit WithMaxConcurrent wins, then the static
// executor registry, then — in peer mode, where the static registry is empty
// BY DESIGN — the count of live IDLE executable fabric agents. The result is
// floored at 1 (the drain must survive an empty world) and capped at 32 (a
// drain never spawns unbounded goroutines).
func TestDrainLimit(t *testing.T) {
	ctx := context.Background()

	// newPeerScheduler builds the production shape: task fabric + agent
	// fabric wired, static executor registry empty.
	newPeerScheduler := func() (*Scheduler, *agentfabric.Fabric) {
		agents := agentfabric.NewFabric()
		sched := New(taskfabric.NewFabric(), map[string]CapabilityExecutor{}, NewLoadTracker())
		sched.WithAgentFabric(agents)
		return sched, agents
	}

	cases := []struct {
		name string
		run  func(t *testing.T) *Scheduler
		want int
	}{
		{
			name: "peer_mode_auto_uses_fabric_candidate_count",
			run: func(t *testing.T) *Scheduler {
				sched, agents := newPeerScheduler()
				for i := 0; i < 4; i++ {
					spawnDrainCandidate(t, ctx, agents, fmt.Sprintf("worker-%d", i), true)
				}
				return sched
			},
			want: 4,
		},
		{
			name: "explicit_max_concurrent_overrides_fabric_count",
			run: func(t *testing.T) *Scheduler {
				sched, agents := newPeerScheduler()
				for i := 0; i < 7; i++ {
					spawnDrainCandidate(t, ctx, agents, fmt.Sprintf("worker-%d", i), true)
				}
				return sched.WithMaxConcurrent(3)
			},
			want: 3,
		},
		{
			name: "negative_max_concurrent_behaves_as_auto",
			run: func(t *testing.T) *Scheduler {
				sched, agents := newPeerScheduler()
				for i := 0; i < 3; i++ {
					spawnDrainCandidate(t, ctx, agents, fmt.Sprintf("worker-%d", i), true)
				}
				return sched.WithMaxConcurrent(-1)
			},
			want: 3,
		},
		{
			name: "auto_mode_takes_max_of_static_and_fabric",
			run: func(t *testing.T) *Scheduler {
				sched, agents := newPeerScheduler()
				sched.RegisterExecutor("static-a", &reconcileProbe{id: "static-a", typ: "code"})
				sched.RegisterExecutor("static-b", &reconcileProbe{id: "static-b", typ: "code"})
				for i := 0; i < 5; i++ {
					spawnDrainCandidate(t, ctx, agents, fmt.Sprintf("worker-%d", i), true)
				}
				// The static entries model a temporary recovery binding: the
				// fabric's 5 idle candidates must not be shadowed by it.
				return sched
			},
			want: 5,
		},
		{
			name: "non_idle_and_non_executable_agents_do_not_count",
			run: func(t *testing.T) *Scheduler {
				sched, agents := newPeerScheduler()
				for i := 0; i < 3; i++ {
					spawnDrainCandidate(t, ctx, agents, fmt.Sprintf("worker-%d", i), true)
				}
				// No CognitionFactory: managed but never schedulable.
				spawnDrainCandidate(t, ctx, agents, "inert-a", false)
				spawnDrainCandidate(t, ctx, agents, "inert-b", false)
				// Executable but suspended: not IDLE, not a candidate.
				if err := agents.Suspend(ctx, "worker-1"); err != nil {
					t.Fatalf("suspend worker-1: %v", err)
				}
				return sched
			},
			want: 2,
		},
		{
			name: "floor_one_when_no_candidates_anywhere",
			run: func(t *testing.T) *Scheduler {
				sched, _ := newPeerScheduler()
				return sched
			},
			want: 1,
		},
		{
			name: "nil_agent_fabric_floors_at_one",
			run: func(t *testing.T) *Scheduler {
				// Legacy/SDK wiring: no agent fabric, static registry empty.
				return New(taskfabric.NewFabric(), map[string]CapabilityExecutor{}, NewLoadTracker())
			},
			want: 1,
		},
		{
			name: "cap_32_on_large_fabric_population",
			run: func(t *testing.T) *Scheduler {
				sched, agents := newPeerScheduler()
				for i := 0; i < 40; i++ {
					spawnDrainCandidate(t, ctx, agents, fmt.Sprintf("worker-%d", i), true)
				}
				return sched
			},
			want: 32,
		},
		{
			name: "cap_32_on_explicit_large_max_concurrent",
			run: func(t *testing.T) *Scheduler {
				sched, _ := newPeerScheduler()
				return sched.WithMaxConcurrent(100)
			},
			want: 32,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.run(t).drainLimit(); got != tc.want {
				t.Fatalf("drainLimit() = %d, want %d", got, tc.want)
			}
		})
	}
}
