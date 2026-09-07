// Package ares_bootstrap — Runtime Closure Feedback Loop Tests.
//
// These tests verify the real data flow across the feedback chain
// Event → Evidence → GA → Strategy → Agent. Unlike earlier stage tests that
// check wiring identity, these assert that emitting an event produces
// observable evidence in the shared EvidenceStore, and that a strategy
// written to the shared StrategyStore is readable through the Agent's
// StrategySource — i.e. the loop moves real data, not just references.
//
//go:build closure

package ares_bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/agents"
	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/evidence"
	ares_evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newFeedbackLoopComponents builds a Bootstrap instance with evolution enabled
// so the flight recorder, evidence store, and GA strategy store are wired.
func newFeedbackLoopComponents(t *testing.T) (*Components, context.CancelFunc) {
	t.Helper()
	cfg := &ares_config.Config{
		LLM: ares_config.LLMConfig{
			Provider: "mock",
			Model:    "mock-model",
			APIKey:   "test-key",
			BaseURL:  "http://localhost:9999",
		},
		Memory:    ares_config.MemoryConfig{Enabled: boolPtr(true)},
		Evolution: ares_config.EvolutionConfig{Enabled: true},
	}
	ctx, cancel := context.WithCancel(context.Background())
	comp, err := Bootstrap(ctx, cfg, nil)
	require.NoError(t, err, "Bootstrap must succeed")
	require.NotNil(t, comp)
	require.NotNil(t, comp.EventStore, "EventStore must be wired")
	require.NotNil(t, comp.EvidenceStore, "EvidenceStore must be wired")
	require.NotNil(t, comp.FlightRecorder, "FlightRecorder must be wired when evolution enabled")
	return comp, cancel
}

// emitTaskEvent publishes a task lifecycle event into the shared EventStore,
// which is what the FlightRecorder collector subscribes to.
func emitTaskEvent(t *testing.T, store ares_events.EventStore, eventType ares_events.EventType) {
	t.Helper()
	evt := &ares_events.Event{
		StreamID: "agent-leader",
		Type:     eventType,
		Payload:  map[string]any{"task_id": "task-1"},
		Version:  1,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, store.Append(ctx, evt.StreamID, []*ares_events.Event{evt}, 0),
		"Append must succeed")
}

// queryFitness waits (bounded) for the collector to process the event and
// returns fitness evidence for the given source, or fails the test on timeout.
func queryFitness(t *testing.T, store evidence.Store, source string) []evidence.Evidence {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		evs, err := store.Query(ctx, evidence.Filter{
			Source: source,
			Kind:   evidence.KindFitness,
			Limit:  10,
		})
		cancel()
		require.NoError(t, err, "EvidenceStore.Query must not fail")
		if len(evs) > 0 {
			return evs
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no %q fitness evidence observed within 5s — feedback loop broken", source)
	return nil
}

// TestClosure_EventToEvidence_WorkflowFitness verifies the first hop of the
// loop: emitting a task.completed event produces workflow fitness evidence in
// the shared EvidenceStore via the FlightRecorder collector (real data flow,
// not just a wired reference).
func TestClosure_EventToEvidence_WorkflowFitness(t *testing.T) {
	comp, cancel := newFeedbackLoopComponents(t)
	defer comp.WaitBackground()
	defer cancel()

	emitTaskEvent(t, comp.EventStore, ares_events.EventTaskCompleted)

	evs := queryFitness(t, comp.EvidenceStore, "workflow")
	assert.NotEmpty(t, evs, "workflow fitness evidence must exist after task.completed")
	assert.Equal(t, 1.0, fitnessValue(t, evs[0]), "completed task must score 1.0")
}

// TestClosure_EventToEvidence_SchedulerFitness verifies the scheduler hop:
// a failed task must produce scheduler fitness evidence scored 0.0.
func TestClosure_EventToEvidence_SchedulerFitness(t *testing.T) {
	comp, cancel := newFeedbackLoopComponents(t)
	defer comp.WaitBackground()
	defer cancel()

	emitTaskEvent(t, comp.EventStore, ares_events.EventTaskFailed)

	evs := queryFitness(t, comp.EvidenceStore, "scheduler")
	assert.NotEmpty(t, evs, "scheduler fitness evidence must exist after task.failed")
	assert.Equal(t, 0.0, fitnessValue(t, evs[0]), "failed task must score 0.0")
}

// TestClosure_StrategyWriteAgentRead verifies the GA → Strategy → Agent hop:
// a strategy written to the shared StrategyStore is readable through the same
// StrategySource the Agent uses at runtime (NewStrategySource wraps the same
// store the GA deploys to).
func TestClosure_StrategyWriteAgentRead(t *testing.T) {
	comp, cancel := newFeedbackLoopComponents(t)
	defer comp.WaitBackground()
	defer cancel()

	require.NotNil(t, comp.NewEvolution, "NewEvolution must be wired when evolution enabled")
	require.NotNil(t, comp.NewEvolution.StrategyStore,
		"StrategyStore must be created by wireGAEvolution")

	// Write the deployed strategy exactly as the GA would.
	strategy := &ares_evolution.Strategy{
		ID:             "gen-9",
		PromptTemplate: "use memory",
		Params:         map[string]any{"k": 1},
		Version:        1,
	}
	ctx, c2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer c2()
	require.NoError(t, comp.NewEvolution.StrategyStore.SetActive(ctx, strategy))

	// The Agent reads through NewStrategySource — the same instance the GA
	// wrote to, proving the write→read hop shares one store.
	src := NewStrategySource(comp.NewEvolution.StrategyStore)
	require.NotNil(t, src, "StrategySource must wrap the shared store")
	active, err := src.GetActiveStrategy(ctx)
	require.NoError(t, err, "GetActiveStrategy must succeed after SetActive")
	require.NotNil(t, active, "active strategy must be readable")
	assert.Equal(t, "gen-9", active.ID, "Agent must read the strategy the GA deployed")
}

// fitnessValue extracts the normalized fitness value ("value" key) from the
// evidence payload, which is a json.RawMessage.
func fitnessValue(t *testing.T, e evidence.Evidence) float64 {
	t.Helper()
	var payload map[string]any
	require.NoError(t, json.Unmarshal(e.Payload, &payload), "evidence payload must be JSON")
	val, ok := payload["value"].(float64)
	require.True(t, ok, "fitness evidence must carry a numeric \"value\" key")
	return val
}

// ── acceptance assertions (1–4) ──
//
// The four tests below exercise the promote/rollback control plane end to end
// over the REAL bootstrap wiring: lifecycle → verify gates → ASM →
// StrategyStore, and EventStore → RuntimeObserver → EvidenceStore →
// aggregator → RollbackPolicy → Rollback.

// newControlPlaneComponents builds Bootstrap with evolution enabled and the
// control plane accelerated for tests: 100ms rollback watch ticks, 5-sample
// shadow judgments. The lifecycle/ASM/shadow surfaces must all be wired.
func newControlPlaneComponents(t *testing.T) (*Components, context.CancelFunc) {
	t.Helper()
	cfg := &ares_config.Config{
		LLM: ares_config.LLMConfig{
			Provider: "mock",
			Model:    "mock-model",
			APIKey:   "test-key",
			BaseURL:  "http://localhost:9999",
		},
		Memory: ares_config.MemoryConfig{Enabled: boolPtr(true)},
		Evolution: ares_config.EvolutionConfig{
			Enabled: true,
			Lifecycle: ares_config.EvolutionLifecycleConfig{
				WatchInterval: "100ms",
			},
			Shadow: ares_config.EvolutionShadowConfig{
				MinSamples: 5,
			},
			// The watch loop records one score per NEW evidence batch
			// (decorrelation): baseline 1.0 then degraded 0.5 → 2 scores.
			// The rollback judge therefore needs min_samples=2.
			Rollback: ares_config.EvolutionRollbackConfig{
				MinSamples: 2,
			},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	comp, err := Bootstrap(ctx, cfg, nil)
	require.NoError(t, err, "Bootstrap must succeed")
	require.NotNil(t, comp.NewEvolution, "NewEvolution must be wired")
	require.NotNil(t, comp.NewEvolution.Lifecycle, "StrategyLifecycle must be wired")
	require.NotNil(t, comp.NewEvolution.ActiveStrategyManager, "ASM must be wired")
	require.NotNil(t, comp.NewEvolution.ShadowEvaluator, "ShadowEvaluator must be wired")
	return comp, cancel
}

// submitStrategy is a typed shim over lifecycle.Submit for readability.
func submitStrategy(t *testing.T, comp *Components, s *mutation.Strategy, generation int) {
	t.Helper()
	comp.NewEvolution.Lifecycle.Submit(context.Background(), s, generation)
}

// getActiveID reads the strategy ID the live agent would consume. The
// MemoryStrategyStore returns nil (no error) when nothing is deployed yet.
func getActiveID(t *testing.T, comp *Components) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	st, err := comp.NewEvolution.StrategyStore.GetActive(ctx)
	if err != nil {
		t.Fatalf("GetActive: %v", err)
	}
	if st == nil {
		return ""
	}
	return st.ID
}

// waitForActiveID polls until the active strategy ID equals want (bounded).
func waitForActiveID(t *testing.T, comp *Components, want string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if got := getActiveID(t, comp); got == want {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("active strategy never became %q within %s (current: %q)",
		want, d, getActiveID(t, comp))
}

// emitTaskEvents appends n task lifecycle events with unique IDs.
func emitTaskEvents(t *testing.T, store ares_events.EventStore, eventType ares_events.EventType, n int) {
	t.Helper()
	ctx := context.Background()
	stamp := time.Now().Format("150405.000000000")
	for i := 0; i < n; i++ {
		evt := &ares_events.Event{
			ID:       fmt.Sprintf("%s-%s-%d", stamp, eventType, i),
			StreamID: "agent-leader",
			Type:     eventType,
			Payload:  map[string]any{"task_id": fmt.Sprintf("task-%s-%d", stamp, i)},
			Version:  1,
		}
		require.NoError(t, store.Append(ctx, evt.StreamID, []*ares_events.Event{evt}, -1),
			"Append must succeed")
	}
}

// TestClosure_VerifyGate_RejectsWorseCandidate (the verify
// gate takes effect).
// A candidate that LOSES its shadow comparisons must be rejected by the G2
// gate: the active strategy (and therefore what the agent reads) is unchanged.
func TestClosure_VerifyGate_RejectsWorseCandidate(t *testing.T) {
	comp, cancel := newControlPlaneComponents(t)
	defer comp.WaitBackground()
	defer cancel()

	base := &mutation.Strategy{ID: "base-v1", Version: 1, Score: 0.5}
	submitStrategy(t, comp, base, 1)
	require.Equal(t, "base-v1", getActiveID(t, comp), "baseline must promote (no shadow data yet)")

	// Feed LOSING shadow comparisons for the worse candidate.
	se := comp.NewEvolution.ShadowEvaluator
	worse := &mutation.Strategy{ID: "worse-v2", Version: 2, Score: 0.1}
	se.StartShadow(worse)
	for i := 0; i < 5; i++ {
		se.RecordResult(1.0, 0.0)
	}

	submitStrategy(t, comp, worse, 2)

	assert.Equal(t, "base-v1", getActiveID(t, comp),
		"verify gate must reject the worse candidate: active ID unchanged")
	snap := comp.NewEvolution.Lifecycle.Snapshot()
	assert.NotEqual(t, "worse-v2", snap.ShadowID,
		"rejected candidate must not stay attached as pending")
}

// TestClosure_Promote_KeepsPrevious (correct retention).
// A candidate that WINS its shadow comparisons is promoted, and the previous
// strategy is preserved on the ASM for rollback.
func TestClosure_Promote_KeepsPrevious(t *testing.T) {
	comp, cancel := newControlPlaneComponents(t)
	defer comp.WaitBackground()
	defer cancel()

	base := &mutation.Strategy{ID: "base-v1", Version: 1, Score: 0.5}
	submitStrategy(t, comp, base, 1)
	require.Equal(t, "base-v1", getActiveID(t, comp))

	se := comp.NewEvolution.ShadowEvaluator
	better := &mutation.Strategy{ID: "better-v2", Version: 2, Score: 0.9}
	se.StartShadow(better)
	for i := 0; i < 5; i++ {
		se.RecordResult(0.0, 1.0)
	}

	submitStrategy(t, comp, better, 2)

	waitForActiveID(t, comp, "better-v2", 2*time.Second)
	prev := comp.NewEvolution.ActiveStrategyManager.Previous()
	require.NotNil(t, prev, "previous strategy must be preserved after promote")
	assert.Equal(t, "base-v1", prev.ID, "asm.Previous() must point at the old strategy")
}

// TestClosure_Degradation_TriggersRollback (rollback on
// degradation).
// After a promote, runtime evidence drives the loop: completed tasks build a
// healthy baseline window, then consecutive task.failed events drag the
// window mean down until RollbackPolicy fires and the active strategy
// reverts to previous — all through the real
// Event → Observer → Evidence → Aggregator → RollbackPolicy chain.
func TestClosure_Degradation_TriggersRollback(t *testing.T) {
	comp, cancel := newControlPlaneComponents(t)
	defer comp.WaitBackground()
	defer cancel()

	stratA := &mutation.Strategy{ID: "strat-a", Version: 1, Score: 0.5}
	submitStrategy(t, comp, stratA, 1)

	// stratB needs shadow evidence to pass the (now fail-closed) G2 gate:
	// feed 5 winning comparisons (MinSamples=5 via the test config).
	se := comp.NewEvolution.ShadowEvaluator
	stratB := &mutation.Strategy{ID: "strat-b", Version: 2, Score: 0.9}
	se.StartShadow(stratB)
	for i := 0; i < 5; i++ {
		se.RecordResult(0.0, 1.0)
	}
	submitStrategy(t, comp, stratB, 2)

	require.Equal(t, "strat-b", getActiveID(t, comp))
	require.Equal(t, "strat-a", comp.NewEvolution.ActiveStrategyManager.Previous().ID)

	// Healthy baseline: 12 completed tasks → 1.0 samples for strat-b.
	emitTaskEvents(t, comp.EventStore, ares_events.EventTaskCompleted, 12)

	// Wait until the aggregator window has enough samples, then let ≥3
	// watch ticks (100ms) feed the baseline into RollbackPolicy (min
	// samples 3). No GA runs interfere: no agent-stopped events are emitted.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if comp.NewEvolution.Lifecycle.Snapshot().WindowCount >= 10 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	time.Sleep(400 * time.Millisecond)

	// Degradation: 12 failed tasks → 0.0 samples → window mean drops to 0.5
	// (12×1.0 + 12×0.0 inside the 50-record window), a 0.5 drop vs the
	// 1.0 reference — far beyond the 0.15 threshold.
	emitTaskEvents(t, comp.EventStore, ares_events.EventTaskFailed, 12)

	waitForActiveID(t, comp, "strat-a", 5*time.Second)

	snap := comp.NewEvolution.Lifecycle.Snapshot()
	assert.Contains(t, snap.LastDecision, "rollback",
		"last decision must record the rollback, got %q", snap.LastDecision)
}

// TestClosure_AgentPassivity_ReadOnlyStrategySource (agent
// passivity).
// The agent's strategy surface is read-only by construction: it satisfies
// exactly one method (GetActiveStrategy), and no mutation/evolution entry
// point leaks through the adapter the agent holds.
func TestClosure_AgentPassivity_ReadOnlyStrategySource(t *testing.T) {
	comp, cancel := newFeedbackLoopComponents(t)
	defer comp.WaitBackground()
	defer cancel()

	src := NewStrategySource(comp.NewEvolution.StrategyStore)
	require.NotNil(t, src, "StrategySource must wrap the shared store")

	// Compile-time interface isolation: the adapter satisfies ONLY the
	// read-only contract (same assertion the production file pins).
	var _ agents.StrategySource = src

	// The agent-facing interface exposes exactly one method.
	iface := reflect.TypeOf((*agents.StrategySource)(nil)).Elem()
	require.Equal(t, 1, iface.NumMethod(), "agents.StrategySource must stay read-only")
	assert.Equal(t, "GetActiveStrategy", iface.Method(0).Name)

	// No mutating/evolution method may appear on the concrete adapter.
	mutating := map[string]bool{
		"Submit": true, "Deploy": true, "Rollback": true, "Approve": true,
		"SetActive": true, "RecordScore": true, "Evolve": true,
		"Run": true, "Start": true, "Stop": true,
	}
	typ := reflect.TypeOf(src)
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		assert.False(t, mutating[name],
			"agent strategy surface must not expose mutating method %q", name)
	}
}
