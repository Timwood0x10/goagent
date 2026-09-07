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

// newToolSetAdapter builds an adapter whose best strategy carries the given
// Params["tools"] whitelist, wired to an in-memory active strategy store so a
// deploy is observable.
func newToolSetAdapter(t *testing.T, tools string, opts ...GuardrailOption) (*GenomePopulationAdapter, *MemoryStrategyStore) {
	t.Helper()

	params := map[string]any{"temperature": 0.7}
	if tools != "" {
		params["tools"] = tools
	}
	base := &mutation.Strategy{
		ID: "toolset-base", Version: 1,
		Params: params, Score: 50.0, CreatedAt: time.Now(),
	}
	mut := &mockGenomeMutator{}
	pop, err := genome.NewPopulation(context.Background(), base, mut,
		genome.WithPopulationSize(4),
		genome.WithEliteCount(1),
	)
	require.NoError(t, err)
	crosser, err := genome.NewCrossover(genome.WithSeed(7))
	require.NoError(t, err)

	store := NewMemoryStrategyStore(4)
	mgr, err := NewActiveStrategyManager(store, nil)
	require.NoError(t, err)

	adapterOpts := []GenomeAdapterOption{WithActiveStrategyManager(mgr)}
	if len(opts) > 0 {
		g, gerr := NewEvolutionGuardrails(opts...)
		require.NoError(t, gerr)
		adapterOpts = append(adapterOpts, WithAdapterGuardrails(g))
	}

	adapter, err := NewGenomePopulationAdapter(pop, mut, crosser, adapterOpts...)
	require.NoError(t, err)
	require.NotNil(t, adapter.pop.BestStrategy(), "base strategy must be the evaluated best")
	return adapter, store
}

// TestGenomeWinnerToolSetGuardrail covers the tool-set guardrail on the genome
// path: the genome adapter promotes its own
// winner without going through the dream cycle's findWinner, so the tool-set
// guardrail has to be applied on this path too. Otherwise an over-bound or
// unregistered whitelist reaches the live agent from the genome path even though
// the dream path rejects the same shape.
func TestGenomeWinnerToolSetGuardrail(t *testing.T) {
	defer discardLogs()()
	ctx := context.Background()

	t.Run("over_bound_whitelist_not_deployed", func(t *testing.T) {
		adapter, store := newToolSetAdapter(t, "a,b,c", WithMaxToolsEnabled(2))
		adapter.deployBestStrategy(ctx)

		active, err := store.GetActive(ctx)
		require.NoError(t, err)
		assert.Nil(t, active, "a jailed strategy must never reach the active store")
		assert.Nil(t, adapter.activeStrategyMgr.Current(), "manager must hold no current strategy")
	})

	t.Run("within_bound_whitelist_deployed", func(t *testing.T) {
		adapter, store := newToolSetAdapter(t, "a,b", WithMaxToolsEnabled(2))
		adapter.deployBestStrategy(ctx)

		active, err := store.GetActive(ctx)
		require.NoError(t, err)
		require.NotNil(t, active, "a compliant strategy must deploy")
		assert.Equal(t, "toolset-base", active.ID)
	})

	t.Run("unregistered_tool_name_not_deployed", func(t *testing.T) {
		// The dangerous case: at runtime this whitelist intersects the registry
		// to zero and the executors fall back to the FULL tool set, so the
		// "narrow" strategy silently becomes the broadest one.
		adapter, store := newToolSetAdapter(t, "ghost_tool",
			WithKnownTools([]string{"web_search", "calculator"}))
		adapter.deployBestStrategy(ctx)

		active, err := store.GetActive(ctx)
		require.NoError(t, err)
		assert.Nil(t, active)
	})

	t.Run("no_guardrails_deploys_unchanged", func(t *testing.T) {
		// Guardrails disabled must mean behavior identical to before the gate.
		adapter, store := newToolSetAdapter(t, "a,b,c,d,e")
		require.Nil(t, adapter.guardrails)
		adapter.deployBestStrategy(ctx)

		active, err := store.GetActive(ctx)
		require.NoError(t, err)
		require.NotNil(t, active)
	})

	t.Run("no_whitelist_deploys", func(t *testing.T) {
		// A strategy that sets no whitelist parses to zero names; that is the
		// default "all tools" shape and must not be treated as an empty set
		// unless requireAnyTool is explicitly on.
		adapter, store := newToolSetAdapter(t, "", WithMaxToolsEnabled(2))
		adapter.deployBestStrategy(ctx)

		active, err := store.GetActive(ctx)
		require.NoError(t, err)
		require.NotNil(t, active)
	})
}

// TestToolSetRejectedGuard covers the shared gate used by both promotion paths
// (lifecycle submit and direct deploy) at its edges.
func TestToolSetRejectedGuard(t *testing.T) {
	defer discardLogs()()
	ctx := context.Background()

	t.Run("nil_strategy_not_rejected", func(t *testing.T) {
		adapter, _ := newToolSetAdapter(t, "a", WithMaxToolsEnabled(1))
		assert.False(t, adapter.toolSetRejected(ctx, nil))
	})

	t.Run("nil_guardrails_not_rejected", func(t *testing.T) {
		adapter, _ := newToolSetAdapter(t, "a,b,c")
		assert.False(t, adapter.toolSetRejected(ctx, adapter.pop.BestStrategy()))
	})

	t.Run("parses_same_names_as_executor", func(t *testing.T) {
		// "a,a," parses to ONE name, so a bound of 1 must accept it. If the
		// guardrail counted raw comma fields it would see 3 and wrongly jail a
		// strategy the executor would run with a single tool.
		adapter, _ := newToolSetAdapter(t, "a,a,", WithMaxToolsEnabled(1))
		assert.False(t, adapter.toolSetRejected(ctx, adapter.pop.BestStrategy()))
	})
}
