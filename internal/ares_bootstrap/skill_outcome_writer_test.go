package ares_bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_events"
	ares_skills "github.com/Timwood0x10/ares/internal/runtime/protocol/skills"
)

// The M4.4 experience write-side loop: taskfabric terminal events (with the
// capability key recordLocked stamps on every persisted event) → the skill
// outcome writer → Experience priors keyed by capability — the exact key the
// READ side (ExperienceConfidenceSource → Fabric.Schedule) queries. This is
// the closure the retired recorder never achieved: it consumed an event
// nobody emitted and keyed records by a pattern the scheduler never looked
// up.

// emitTerminal appends one terminal taskfabric-shaped event to the store.
func emitTerminal(t *testing.T, ctx context.Context, store *ares_events.MemoryEventStore, typ ares_events.EventType, capability string) {
	t.Helper()
	err := store.Append(ctx, "t-"+capability, []*ares_events.Event{{
		Type:       typ,
		StreamID:   "t-" + capability,
		ModuleName: "taskfabric",
		Payload: map[string]any{
			"task_id":    "t-" + capability,
			"capability": capability,
			"state":      "completed",
		},
	}}, 0)
	require.NoError(t, err)
}

// waitForPrior polls the experience store until the capability's prior
// exists or the deadline passes (subscriber delivery is async).
func waitForPrior(t *testing.T, exp *ares_skills.Experience, capability string) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := exp.BestMatch(capability); ok {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func TestSkillOutcomeWriterRecordsTerminalOutcomes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := ares_events.NewMemoryEventStore()
	exp := ares_skills.NewExperience()
	startSkillOutcomeWriter(ctx, store, exp)

	emitTerminal(t, ctx, store, ares_events.EventTaskCompleted, "rust/unsafe-analysis")
	require.True(t, waitForPrior(t, exp, "rust/unsafe-analysis"),
		"completed event must record a prior")

	rec, ok := exp.BestMatch("rust/unsafe-analysis")
	require.True(t, ok)
	assert.Equal(t, 1.0, rec.SuccessRate)
	assert.Equal(t, "rust/unsafe-analysis", rec.Skill, "the prior's skill IS the capability (the read side's join key)")

	emitTerminal(t, ctx, store, ares_events.EventTaskFailed, "rust/unsafe-analysis")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rec, ok = exp.BestMatch("rust/unsafe-analysis")
		if ok && rec.SuccessRate == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	rec, ok = exp.BestMatch("rust/unsafe-analysis")
	require.True(t, ok)
	assert.Equal(t, 0.0, rec.SuccessRate, "a later failure must replace the prior (recency semantics)")
}

func TestSkillOutcomeWriterSkipsCapabilitylessEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := ares_events.NewMemoryEventStore()
	exp := ares_skills.NewExperience()
	startSkillOutcomeWriter(ctx, store, exp)

	// A pre-M4.4-shaped event with no capability key: nothing may be
	// recorded (a "" pattern could never match the read side's query).
	err := store.Append(ctx, "t-bare", []*ares_events.Event{{
		Type:       ares_events.EventTaskCompleted,
		StreamID:   "t-bare",
		ModuleName: "taskfabric",
		Payload:    map[string]any{"task_id": "t-bare"},
	}}, 0)
	require.NoError(t, err)

	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, 0, exp.Count(), "capability-less events must not create records")
}

func TestSkillOutcomeWriterNilDepsAreNoOps(t *testing.T) {
	// nil store / nil experience: offline wiring, no panic, no error path.
	assert.NotPanics(t, func() {
		startSkillOutcomeWriter(context.Background(), nil, ares_skills.NewExperience())
		startSkillOutcomeWriter(context.Background(), ares_events.NewMemoryEventStore(), nil)
		startSkillOutcomeWriter(context.Background(), nil, nil)
	})
}

// TestSkillConfidenceLoopClosesAcrossSides is the full-closure pin: events
// → writer → Experience → ExperienceConfidenceSource returns the recorded
// success rate for the capability the scheduler will query.
func TestSkillConfidenceLoopClosesAcrossSides(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := ares_events.NewMemoryEventStore()
	exp := ares_skills.NewExperience()
	startSkillOutcomeWriter(ctx, store, exp)

	emitTerminal(t, ctx, store, ares_events.EventTaskCompleted, "ffi-expert")
	require.True(t, waitForPrior(t, exp, "ffi-expert"))

	src := ares_skills.NewExperienceConfidenceSource(exp)
	assert.Equal(t, 1.0, src.Confidence("ffi-expert"),
		"the confidence source (the fabric's read side) must see the written prior")

	emitTerminal(t, ctx, store, ares_events.EventTaskFailed, "ffi-expert")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if src.Confidence("ffi-expert") == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	assert.Equal(t, 0.0, src.Confidence("ffi-expert"),
		"a failing capability must read back as zero confidence")
}
