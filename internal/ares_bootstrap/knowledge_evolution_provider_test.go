package ares_bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/knowledge"
	evoprovider "github.com/Timwood0x10/ares/internal/knowledge/provider/evolution"
	ares_evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
)

// TestKnowledgeRuntime_EvolutionProviderWired locks the server-side closure of
// the evolution-context loop: once the GA StrategyStore exists (created by
// wireGAEvolution, after the knowledge runtime), the runtime must expose an
// "evolution" graph provider so strategy decisions are retrievable from the
// server knowledge graph — mirroring the SDK path in sdk/knowledge.go.
func TestKnowledgeRuntime_EvolutionProviderWired(t *testing.T) {
	rt := BuildKnowledgeRuntime(nil, nil, nil)
	require.NotNil(t, rt)
	require.NotContains(t, rt.ProviderNames(), "evolution", "provider must be absent before late registration")

	store := ares_evolution.NewMemoryStrategyStore(0)
	err := rt.RegisterProvider(evoprovider.New("evolution", store))
	require.NoError(t, err)
	require.Contains(t, rt.ProviderNames(), "evolution")
}

// TestKnowledgeRuntime_RegisterProviderDuplicate verifies the duplicate-name
// contract: bootstrap must not silently overwrite an existing provider.
func TestKnowledgeRuntime_RegisterProviderDuplicate(t *testing.T) {
	rt := BuildKnowledgeRuntime(nil, nil, nil)
	store := ares_evolution.NewMemoryStrategyStore(0)
	require.NoError(t, rt.RegisterProvider(evoprovider.New("evolution", store)))
	require.Error(t, rt.RegisterProvider(evoprovider.New("evolution", store)))
}

// TestAttachEvolutionKnowledgeProvider drives the real bootstrap glue: the
// helper must register the "evolution" provider on a live runtime, tolerate
// nil runtime/store (dev/offline paths), and keep the original provider when
// a duplicate registration is attempted.
func TestAttachEvolutionKnowledgeProvider(t *testing.T) {
	ctx := context.Background()

	// Nil runtime / nil store must be no-ops, not panics (dev/offline paths).
	require.NotPanics(t, func() { attachEvolutionKnowledgeProvider(ctx, nil, nil, nil) })

	rt := BuildKnowledgeRuntime(nil, nil, nil)
	store := ares_evolution.NewMemoryStrategyStore(0)
	attachEvolutionKnowledgeProvider(ctx, rt, store, nil)
	require.Contains(t, rt.ProviderNames(), "evolution")

	// Duplicate attach must not panic or register a second "evolution"
	// provider (the runtime also carries its default memory/code providers).
	attachEvolutionKnowledgeProvider(ctx, rt, store, nil)
	count := 0
	for _, name := range rt.ProviderNames() {
		if name == "evolution" {
			count++
		}
	}
	require.Equal(t, 1, count, "duplicate attach must not create a second evolution provider")
}

// TestKnowledgeRuntime_EvolutionProviderStreamsStrategies verifies the closure
// end-to-end at the data level: an active strategy written into the
// StrategyStore streams out of the registered provider as a decision-type
// knowledge object — i.e. server-side evolution queries can now retrieve it.
func TestKnowledgeRuntime_EvolutionProviderStreamsStrategies(t *testing.T) {
	store := ares_evolution.NewMemoryStrategyStore(0)
	strategy := &ares_evolution.Strategy{
		ID:      "strategy-1",
		Version: 1,
		Params:  map[string]any{paramTemperature: 0.8},
	}
	require.NoError(t, store.SetActive(context.Background(), strategy))

	prov := evoprovider.New("evolution", store)
	objCh, errCh := prov.Stream(context.Background(), knowledge.Intent{})
	var got *knowledge.KnowledgeObject
	for obj := range objCh {
		if got == nil {
			got = obj
		}
	}
	select {
	case err := <-errCh:
		require.NoError(t, err)
	default:
	}
	require.NotNil(t, got, "active strategy must stream as knowledge object")
}
