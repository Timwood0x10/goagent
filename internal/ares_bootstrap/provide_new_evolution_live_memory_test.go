package ares_bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
	aresmemory "github.com/Timwood0x10/ares/internal/runtime/memory"
)

// TestProvideNewEvolution_LiveMemoryStore verifies that when a live
// MemoryConfigStore is passed to ProvideNewEvolution, the MemoryPatchExecutor
// mutates the live config (not an isolated copy).
//
// This is the Step 2 closure fix: pre-fix the bootstrap passed an isolated
// Minimal manager, so evolution patches never reached the agent's real config.
func TestProvideNewEvolution_LiveMemoryStore(t *testing.T) {
	ctx := context.Background()

	// Build a real live memory manager (the same type bootstrap creates).
	liveMem, err := aresmemory.NewMemoryManager(aresmemory.DefaultMemoryConfig())
	require.NoError(t, err)

	// Type-assert to MemoryConfigStore — this is the exact assertion
	// bootstrap.go performs at Step 2.
	liveStore, ok := liveMem.(aresmemory.MemoryConfigStore)
	require.True(t, ok, "*memoryManager must implement MemoryConfigStore")

	// Record the original MaxHistory.
	liveStore.Lock()
	originalMaxHistory := liveStore.GetConfig().MaxHistory
	liveStore.Unlock()

	// Wire ProvideNewEvolution with the LIVE memory store.
	components, err := ProvideNewEvolution(nil, nil, liveStore, nil)
	require.NoError(t, err)
	require.NotNil(t, components.PatchReg)

	// Build a PatchChangePlanner that sets max_history to a new value.
	newMaxHistory := originalMaxHistory + 100
	applyPatch := patch.RuntimePatch{
		Type:   patch.PatchChangePlanner,
		Target: "memory",
		Value:  map[string]any{"max_history": newMaxHistory},
	}

	// Dispatch the patch via the registry (the same path Coordinator uses).
	err = components.PatchReg.Apply(ctx, applyPatch)
	require.NoError(t, err)

	// Verify the LIVE memory manager's config was mutated.
	liveStore.Lock()
	actualMaxHistory := liveStore.GetConfig().MaxHistory
	liveStore.Unlock()

	assert.Equal(t, newMaxHistory, actualMaxHistory,
		"evolution patch must mutate the live memory config, not an isolated copy")
}

// TestProvideNewEvolution_NilMemoryStoreSkipsExecutor verifies that when
// memoryStore is nil, the MemoryPatchExecutor is not registered.
//
// Pre-fix: ProvideNewEvolution always registered the executor because
// memoryMgr was never nil (bootstrap passed a Minimal manager).
func TestProvideNewEvolution_NilMemoryStoreSkipsExecutor(t *testing.T) {
	components, err := ProvideNewEvolution(nil, nil, nil, nil)
	require.NoError(t, err)

	// Apply a patch targeted at "memory" — should fail with "no executor".
	err = components.PatchReg.Apply(context.Background(), patch.RuntimePatch{
		Type:   patch.PatchChangePlanner,
		Target: "memory",
		Value:  map[string]any{"max_history": 50},
	})
	require.Error(t, err, "no memory executor should be registered when store is nil")
}
