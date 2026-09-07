package evolution

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// newGuardedAdapter builds a population adapter whose population is entirely
// unevaluated (every variant carries genome.ScoreUnevaluated, which is what
// mockGenomeMutator produces), with the given guardrails attached.
func newGuardedAdapter(t *testing.T, g *EvolutionGuardrails) *GenomePopulationAdapter {
	t.Helper()
	base := &mutation.Strategy{
		ID:        "base",
		Params:    map[string]any{"temperature": 0.7},
		Score:     genome.ScoreUnevaluated,
		CreatedAt: time.Now(),
	}
	pop, err := genome.NewPopulation(context.Background(), base, &mockGenomeMutator{},
		genome.WithPopulationSize(10),
	)
	require.NoError(t, err)

	crosser, err := genome.NewCrossover(genome.WithSeed(1))
	require.NoError(t, err)

	opts := []GenomeAdapterOption{}
	if g != nil {
		opts = append(opts, WithAdapterGuardrails(g))
	}
	adapter, err := NewGenomePopulationAdapter(pop, &mockGenomeMutator{}, crosser, opts...)
	require.NoError(t, err)
	return adapter
}

// TestAdapterPreGuardrailsBlockUnevaluatedPopulation is the behavioural
// contract for the adapter layer.
//
// Historically bootstrap never assigned gaCfg.Guardrails, so WithAdapterGuardrails
// was never applied and runPreGuardrails returned nil on its first line. The
// "majority of the population is unevaluated" check — the guardrail's only
// Critical pre-condition — could not fire in any production configuration.
//
// This test drives the check through the adapter rather than calling
// PreEvolveCheck directly, so it fails if the adapter ever stops consulting
// the guardrail (which is exactly how the defect went unnoticed).
func TestAdapterPreGuardrailsBlockUnevaluatedPopulation(t *testing.T) {
	ctx := context.Background()
	g, err := NewEvolutionGuardrails()
	require.NoError(t, err)

	adapter := newGuardedAdapter(t, g)
	err = adapter.runPreGuardrails(ctx)
	require.Error(t, err, "a majority-unevaluated population must block the cycle")
	assert.Contains(t, err.Error(), "pre-evolve guardrail check failed")
}

// TestAdapterPreGuardrailsNilPassesThrough pins the nil-guardrails behavior as
// the
// explicit no-guardrails contract rather than an accident: paths that wire no
// guardrails (tests, minimal configs) must still run, so the nil check is a
// deliberate opt-out, not the default.
func TestAdapterPreGuardrailsNilPassesThrough(t *testing.T) {
	adapter := newGuardedAdapter(t, nil)
	assert.NoError(t, adapter.runPreGuardrails(context.Background()),
		"without guardrails the adapter must not gate (documented opt-out)")
}

// TestSchedulerTickBlockedByGuardrails is the behavioural contract for the
// legacy scheduler path.
//
// Two defects had to be fixed for this to be assertable at all:
//  1. ProvideEvolution passed only WithEnabled and WithMinInterval, so
//     s.guardrails stayed nil and checkGuardrails returned true unconditionally.
//  2. checkGuardrails passed a hardcoded unevaluatedCount of 0, and an
//     unevaluated majority is PreEvolveCheck's ONLY ShouldStop condition — so
//     even a configured guardrail could not block. Wiring (1) without (2) would
//     have produced a gate that still never fires.
//
// The assertion is therefore end-to-end: a population the guardrail objects to
// must prevent adapter.Run from being called at all, not merely log a warning.
func TestSchedulerTickBlockedByGuardrails(t *testing.T) {
	ctx := context.Background()
	// A real adapter over a fully-unevaluated population: it implements
	// populationInspector, so the scheduler can see the population shape.
	adapter := newGuardedAdapter(t, nil)

	g, err := NewEvolutionGuardrails()
	require.NoError(t, err)

	s := NewEvolutionScheduler(nil, adapter,
		WithMinInterval(time.Nanosecond),
		WithSchedulerGuardrails(g),
	)
	s.SetEnabled(true)

	// Seed a degradation signal so shouldEvolve says yes — otherwise the test
	// would pass for the wrong reason (throttling, not the guardrail).
	for i := 0; i < 30; i++ {
		s.RecordScore(taskScoreSuccess)
	}
	for i := 0; i < 10; i++ {
		s.RecordScore(taskScoreFailure)
	}
	require.True(t, s.shouldEvolve(ctx, CallbackData{AgentID: "test"}),
		"the window must be evolve-eligible, otherwise this test proves nothing")

	require.Positive(t, adapter.PopulationUnevaluated(),
		"the fixture must have unevaluated individuals for the guardrail to object to")
	assert.False(t, s.checkGuardrails(ctx),
		"an unevaluated-majority population must block the cycle")

	// The verdict must actually gate execution, not merely be logged.
	genBefore := adapter.PopulationGeneration()
	s.Tick(ctx)
	assert.Equal(t, genBefore, adapter.PopulationGeneration(),
		"a blocking guardrail must prevent the generation from advancing")
}

// TestSchedulerCheckGuardrailsNilPassesThrough pins the opt-out contract for
// the scheduler side, mirroring the adapter test above.
func TestSchedulerCheckGuardrailsNilPassesThrough(t *testing.T) {
	s := NewEvolutionScheduler(nil, newMockAdapterForScheduler(), WithEnabled(true))
	assert.True(t, s.checkGuardrails(context.Background()),
		"without guardrails the scheduler must not gate (documented opt-out)")
}

// TestSchedulerGuardrailsSeeRealPopulationShape locks fix (2) above on its own:
// an adapter that reports its population must have that shape forwarded to
// PreEvolveCheck. Without this the guardrail is configured but blind, which is
// indistinguishable from not being wired at all.
func TestSchedulerGuardrailsSeeRealPopulationShape(t *testing.T) {
	ctx := context.Background()
	adapter := newGuardedAdapter(t, nil)

	var gotPop, gotUnevaluated, gotGeneration int
	g, err := NewEvolutionGuardrails(
		WithGuardrailEventHandler(func(evt GuardrailEvent) {
			gotGeneration = evt.Generation
		}),
	)
	require.NoError(t, err)

	s := NewEvolutionScheduler(nil, adapter, WithSchedulerGuardrails(g))
	s.SetEnabled(true)
	require.False(t, s.checkGuardrails(ctx))

	gotPop = adapter.PopulationSize()
	gotUnevaluated = adapter.PopulationUnevaluated()
	assert.Equal(t, 10, gotPop, "population size must reach the guardrail")
	assert.Positive(t, gotUnevaluated, "unevaluated count must reach the guardrail")
	assert.Equal(t, adapter.PopulationGeneration(), gotGeneration,
		"guardrail events must carry the real generation, not a hardcoded 0")
}
