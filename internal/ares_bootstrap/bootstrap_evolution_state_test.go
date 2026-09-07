package ares_bootstrap

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/evidence"
	ares_evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
)

// appendFitnessEvidence writes a fitness evidence record with the given value
// into the store under the given genome source, mirroring the collector's
// payload contract ({"value": v}).
func appendFitnessEvidence(t *testing.T, store *evidence.MemoryStore, source string, value float64) {
	t.Helper()
	require.NoError(t, store.Append(context.Background(), evidence.NewEvidence(
		source, evidence.KindFitness, map[string]any{"value": value},
	)))
}

// TestRecentFitnessSummary_NoRecords verifies that a store with no matching
// evidence yields ok=false so callers can skip the source in the prompt.
func TestRecentFitnessSummary_NoRecords(t *testing.T) {
	store := evidence.NewMemoryStore()
	mean, count, ok := recentFitnessSummary(context.Background(), store, "workflow", 50)
	assert.False(t, ok)
	assert.Zero(t, mean)
	assert.Zero(t, count)
}

// TestRecentFitnessSummary_NilStore verifies the nil-store guard returns ok=false.
func TestRecentFitnessSummary_NilStore(t *testing.T) {
	mean, count, ok := recentFitnessSummary(context.Background(), nil, "workflow", 50)
	assert.False(t, ok)
	assert.Zero(t, mean)
	assert.Zero(t, count)
}

// TestRecentFitnessSummary_MeanOfValidRecords verifies the mean over valid
// records while skipping non-numeric, out-of-range, and other-source records.
func TestRecentFitnessSummary_MeanOfValidRecords(t *testing.T) {
	store := evidence.NewMemoryStore()
	appendFitnessEvidence(t, store, "workflow", 1.0)
	appendFitnessEvidence(t, store, "workflow", 0.5)
	// Different source must not pollute the mean.
	appendFitnessEvidence(t, store, "recovery", 0.0)
	// Out-of-range value is skipped (payload contract is [0,1]).
	require.NoError(t, store.Append(context.Background(), evidence.NewEvidence(
		"workflow", evidence.KindFitness, map[string]any{"value": 1.5},
	)))
	// Non-numeric payload is skipped.
	require.NoError(t, store.Append(context.Background(), evidence.NewEvidence(
		"workflow", evidence.KindFitness, map[string]any{"value": "not-a-number"},
	)))

	mean, count, ok := recentFitnessSummary(context.Background(), store, "workflow", 50)
	require.True(t, ok)
	assert.Equal(t, 2, count)
	assert.InDelta(t, 0.75, mean, 0.0001)
}

// TestRecentFitnessSummary_RespectsLimit verifies the limit bounds the window
// so a long-running process does not read the whole store each cycle.
func TestRecentFitnessSummary_RespectsLimit(t *testing.T) {
	store := evidence.NewMemoryStore()
	for i := 0; i < 5; i++ {
		appendFitnessEvidence(t, store, "scheduler", 0.2)
	}
	mean, count, ok := recentFitnessSummary(context.Background(), store, "scheduler", 2)
	require.True(t, ok)
	assert.Equal(t, 2, count)
	assert.InDelta(t, 0.2, mean, 0.0001)
}

// TestBuildEvolutionSuggestionPrompt_NilState verifies the fallback: with no
// evidence store and no strategy store the prompt still contains the base
// instruction so the LLM knows which mutations are allowed.
func TestBuildEvolutionSuggestionPrompt_NilState(t *testing.T) {
	prompt := buildEvolutionSuggestionPrompt(context.Background(), nil, nil)
	assert.Contains(t, prompt, "suggest one evolution improvement")
	assert.Contains(t, prompt, "change scheduler")
	assert.NotContains(t, prompt, "Current evolution state")
	assert.NotContains(t, prompt, "Currently deployed strategy")
}

// TestBuildEvolutionSuggestionPrompt_WithEvidence verifies the prompt embeds
// per-genome mean fitness from the shared evidence store.
func TestBuildEvolutionSuggestionPrompt_WithEvidence(t *testing.T) {
	store := evidence.NewMemoryStore()
	appendFitnessEvidence(t, store, "workflow", 1.0)
	appendFitnessEvidence(t, store, "workflow", 0.5)

	prompt := buildEvolutionSuggestionPrompt(context.Background(), store, nil)
	assert.Contains(t, prompt, "Current evolution state")
	assert.Contains(t, prompt, "workflow")
	assert.Contains(t, prompt, "mean fitness 0.75")
	// Sources without evidence are omitted.
	assert.NotContains(t, prompt, "recovery: mean")
}

// TestBuildEvolutionSuggestionPrompt_WithStrategy verifies the deployed
// strategy (id, version, score, mutation description) is included when set.
func TestBuildEvolutionSuggestionPrompt_WithStrategy(t *testing.T) {
	strategyStore := ares_evolution.NewMemoryStrategyStore(0)
	require.NoError(t, strategyStore.SetActive(context.Background(), &ares_evolution.Strategy{
		ID:           "gen-7",
		Version:      3,
		Score:        88.5,
		MutationDesc: "increased temperature",
	}))

	prompt := buildEvolutionSuggestionPrompt(context.Background(), nil, strategyStore)
	assert.Contains(t, prompt, "Currently deployed strategy")
	assert.Contains(t, prompt, "id=gen-7 version=3")
	assert.Contains(t, prompt, "score=88.50")
	assert.Contains(t, prompt, `mutation="increased temperature"`)
}

// TestWaitBackground_NilReceiver verifies the nil guard does not panic.
func TestWaitBackground_NilReceiver(t *testing.T) {
	var comp *Components
	comp.WaitBackground() // must not panic
}

// TestWaitBackground_WaitsForGoroutines verifies WaitBackground blocks until
// background goroutines registered via bgGroup have exited after ctx
// cancellation.
func TestWaitBackground_WaitsForGoroutines(t *testing.T) {
	comp := &Components{}
	ctx, cancel := context.WithCancel(context.Background())
	comp.bgGroup.Go(func() error {
		<-ctx.Done()
		return nil
	})
	cancel()

	done := make(chan struct{})
	go func() {
		comp.WaitBackground()
		close(done)
	}()
	select {
	case <-done:
		// WaitBackground returned only after the goroutine exited.
	case <-time.After(2 * time.Second):
		t.Fatal("WaitBackground did not return after context cancellation")
	}
}

// TestBuildEvolutionSuggestionPrompt_StableOrdering verifies the summary lines
// follow the fixed genome ordering regardless of evidence insertion order.
func TestBuildEvolutionSuggestionPrompt_StableOrdering(t *testing.T) {
	store := evidence.NewMemoryStore()
	appendFitnessEvidence(t, store, "memory", 0.9)
	appendFitnessEvidence(t, store, "workflow", 0.6)

	prompt := buildEvolutionSuggestionPrompt(context.Background(), store, nil)
	workflowIdx := strings.Index(prompt, "workflow")
	memoryIdx := strings.Index(prompt, "memory")
	assert.Greater(t, workflowIdx, 0, "workflow line must be present")
	assert.Greater(t, memoryIdx, workflowIdx, "workflow must be listed before memory")
}
