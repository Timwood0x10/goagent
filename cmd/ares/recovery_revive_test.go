package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/aresrecovery"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/fabric/agent"
	"github.com/Timwood0x10/ares/internal/fabric/task"
)

// reviveHarness is the A2 acceptance rig: a real kernel loop (scheduler +
// event-driven recovery with background sweeps) over a controllable clock.
type reviveHarness struct {
	fabric   *taskfabric.Fabric
	agents   *agentfabric.Fabric
	recovery *aresrecovery.Recovery
	sink     *e2eAgentSink
	advance  func(time.Duration)
}

func newReviveHarness(t *testing.T, ctx context.Context, maxRestarts int) *reviveHarness {
	t.Helper()
	store := ares_events.NewMemoryEventStore()
	fabric := taskfabric.NewFabric().WithEventStore(store)
	sink := &e2eAgentSink{}
	agents := agentfabric.NewFabric().WithEventSink(sink)

	var clockMu sync.Mutex
	now := time.Now()
	h := &reviveHarness{
		fabric: fabric,
		agents: agents,
		sink:   sink,
	}
	h.advance = func(d time.Duration) {
		clockMu.Lock()
		now = now.Add(d)
		clockMu.Unlock()
	}
	fabric.WithClock(func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	})

	policy := aresrecovery.DefaultRestartPolicy()
	policy.MaxRestarts = maxRestarts
	rec := aresrecovery.New(fabric, agents, policy)
	h.recovery = rec

	tracker := newLoadTracker()
	sched := NewKernelScheduler(fabric, map[string]CapabilityExecutor{}, tracker)
	sched.PollInterval = 10 * time.Millisecond
	sched.WithEventStore(store)
	go sched.Run(ctx)

	go runKernelRecoveryLoop(ctx, store, rec, kernelLoopConfig{
		RecoverySweepInterval: 20 * time.Millisecond,
		RecoverySweepTimeout:  2 * time.Second,
	},
		func(taskID, agentID string, executor CapabilityExecutor) {
			sched.RegisterExecutorForTask(taskID, agentID, executor)
		},
		func(agentID, capability string) CapabilityExecutor {
			return &chaosStubExecutor{id: agentID, typ: models.AgentType(capability)}
		},
		sched.HasCapableExecutor,
	)
	return h
}

// killWithCognition spawns an agent, writes cognition into it, and kills it —
// leaving behind exactly the death snapshot the arbitration rule looks for.
func (h *reviveHarness) killWithCognition(t *testing.T, ctx context.Context, id string) {
	t.Helper()
	if _, err := h.agents.Spawn(ctx, agentfabric.SpawnSpec{
		Identity:     id,
		Capabilities: []string{"code"},
	}); err != nil {
		t.Fatalf("spawn %s: %v", id, err)
	}
	h.writeAndKill(t, ctx, id)
}

// writeAndKill stamps cognition onto an EXISTING agent and kills it (used for
// re-killing a revived identity without re-spawning it).
func (h *reviveHarness) writeAndKill(t *testing.T, ctx context.Context, id string) {
	t.Helper()
	if err := h.agents.SetCognitiveState(id, CognitiveStateFixture("remember-the-lock")); err != nil {
		t.Fatalf("SetCognitiveState %s: %v", id, err)
	}
	if err := h.agents.Kill(ctx, id); err != nil {
		t.Fatalf("kill %s: %v", id, err)
	}
}

// CognitiveStateFixture builds a recognizable cognitive state for assertions.
func CognitiveStateFixture(observation string) agentfabric.CognitiveState {
	return agentfabric.CognitiveState{
		SchemaVersion: agentfabric.CognitiveStateSchemaVersion,
		Context:       "code review",
		Observation:   observation,
		Checkpoint:    "pre-death",
	}
}

// expireLeasedTask creates a task and acquires it under a lease that expires
// as soon as the harness advances the clock past the scheduler TTL.
func (h *reviveHarness) expireLeasedTask(t *testing.T, ctx context.Context, id string) {
	t.Helper()
	if err := h.fabric.Create(&taskfabric.Task{
		ID:          id,
		Capability:  "code",
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 3},
	}); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
	if _, err := h.fabric.Acquire(id, "victim-holder", time.Millisecond); err != nil {
		t.Fatalf("acquire %s: %v", id, err)
	}
	h.advance(7 * time.Minute) // past the 5-minute lease TTL
}

// TestRevivalRestoresCognitionUnderSameIdentity is the A2 core acceptance:
// a dead agent whose death snapshot exists is revived IN PLACE — same id,
// restored cognition — and its bound executor drives the requeued task to
// completion. The consumed snapshot must be cleared afterwards.
func TestRevivalRestoresCognitionUnderSameIdentity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newReviveHarness(t, ctx, 5)
	h.killWithCognition(t, ctx, "worker-victim")
	if _, ok := h.agents.LastSnapshot("worker-victim"); !ok {
		t.Fatal("precondition: death snapshot exists")
	}

	h.expireLeasedTask(t, ctx, "t-revive")
	waitFabricState(t, h.fabric, "t-revive", taskfabric.StateCompleted, 5*time.Second)

	// Same identity, alive again in the fabric.
	a, err := h.agents.Get("worker-victim")
	if err != nil {
		t.Fatalf("revived agent must exist under the SAME id: %v", err)
	}
	// Cognition restored verbatim (the "stateful cognitive revival" promise).
	cs, err := h.agents.CognitiveState("worker-victim")
	if err != nil {
		t.Fatalf("CognitiveState: %v", err)
	}
	if cs.Observation != "remember-the-lock" || cs.Checkpoint != "pre-death" {
		t.Fatalf("restored cognition incomplete: %+v", cs)
	}
	if a.Parent != "" || len(a.Capabilities) != 1 || a.Capabilities[0] != "code" {
		t.Fatalf("revived body lost provenance/capabilities: %+v", a)
	}
	// Snapshot consumed by revival.
	if _, still := h.agents.LastSnapshot("worker-victim"); still {
		t.Fatal("revival must consume the death snapshot")
	}
}

// TestRevivalFallsBackToReplacementWhenExhausted locks arbitration priority 2:
// once the restart budget is exhausted, the same death no longer revives the
// original identity — the generic replacement path takes over (new generated
// id), so a broken agent cannot loop forever.
func TestRevivalFallsBackToReplacementWhenExhausted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newReviveHarness(t, ctx, 1) // budget: exactly one revival
	h.killWithCognition(t, ctx, "fragile-worker")

	// Cycle 1: revived in place...
	h.expireLeasedTask(t, ctx, "t-cycle-1")
	waitFabricState(t, h.fabric, "t-cycle-1", taskfabric.StateCompleted, 5*time.Second)
	if _, err := h.agents.Get("fragile-worker"); err != nil {
		t.Fatal("cycle 1 must revive in place under the same id")
	}

	// ...then it dies AGAIN (the revived body already exists — just re-kill).
	// Budget exhausted → generic replacement wins.
	h.writeAndKill(t, ctx, "fragile-worker")
	h.expireLeasedTask(t, ctx, "t-cycle-2")
	waitFabricState(t, h.fabric, "t-cycle-2", taskfabric.StateCompleted, 5*time.Second)

	if _, err := h.agents.Get("fragile-worker"); err == nil {
		t.Fatal("exhausted identity must NOT be revived again")
	}
	// The generic replacement registered itself under a recovery-* id; the
	// task completing proves the fallback path works end to end.
}

// TestRevivalWithoutSnapshotUsesReplacement covers arbitration priority 2's
// no-snapshot branch: deaths without prior cognition go straight to the
// generic replacement path (no shell revival), matching pre-A2 behavior.
func TestRevivalWithoutSnapshotUsesReplacement(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h := newReviveHarness(t, ctx, 5)
	// Spawn+kill WITHOUT writing any cognition → snapshot has empty state but
	// still captured; simulate the true no-snapshot case by clearing it.
	h.killWithCognition(t, ctx, "plain-worker")
	h.agents.ClearSnapshot("plain-worker")

	h.expireLeasedTask(t, ctx, "t-no-snap")
	got := waitFabricState(t, h.fabric, "t-no-snap", taskfabric.StateCompleted, 5*time.Second)
	if got != taskfabric.StateCompleted {
		t.Fatalf("task state = %s", got)
	}
	if _, err := h.agents.Get("plain-worker"); err == nil {
		t.Fatal("no-snapshot death must not revive the old identity")
	}
}
