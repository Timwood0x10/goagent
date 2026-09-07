package evolution

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewEvolutionGuardrails_ValidOptions(t *testing.T) {
	tests := []struct {
		name    string
		opts    []GuardrailOption
		wantErr bool
		check   func(t *testing.T, g *EvolutionGuardrails)
	}{
		{
			name: "default_options",
			opts: []GuardrailOption{},
			check: func(t *testing.T, g *EvolutionGuardrails) {
				assert.Equal(t, 0.0, g.BaselineScore)
				assert.Equal(t, 10, g.MaxStagnantGenerations)
				assert.Equal(t, 0.8, g.MaxLineageShare)
				assert.Equal(t, 1000, g.MaxEvents)
			},
		},
		{
			name: "custom_baseline_score",
			opts: []GuardrailOption{WithBaselineScore(85.5)},
			check: func(t *testing.T, g *EvolutionGuardrails) {
				assert.Equal(t, 85.5, g.BaselineScore)
			},
		},
		{
			name: "custom_stagnant_generations",
			opts: []GuardrailOption{WithMaxStagnantGenerations(20)},
			check: func(t *testing.T, g *EvolutionGuardrails) {
				assert.Equal(t, 20, g.MaxStagnantGenerations)
			},
		},
		{
			name: "custom_lineage_share",
			opts: []GuardrailOption{WithMaxLineageShare(0.6)},
			check: func(t *testing.T, g *EvolutionGuardrails) {
				assert.Equal(t, 0.6, g.MaxLineageShare)
			},
		},
		{
			name: "all_custom_options",
			opts: []GuardrailOption{
				WithBaselineScore(90.0),
				WithMaxStagnantGenerations(15),
				WithMaxLineageShare(0.7),
			},
			check: func(t *testing.T, g *EvolutionGuardrails) {
				assert.Equal(t, 90.0, g.BaselineScore)
				assert.Equal(t, 15, g.MaxStagnantGenerations)
				assert.Equal(t, 0.7, g.MaxLineageShare)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, err := NewEvolutionGuardrails(tt.opts...)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, g)
			tt.check(t, g)
		})
	}
}

func TestPreEvolveCheck_NormalCase(t *testing.T) {
	g, _ := NewEvolutionGuardrails(
		WithBaselineScore(80.0),
		WithMaxStagnantGenerations(5),
	)

	ctx := context.Background()
	result := g.PreEvolveCheck(ctx, 75.0, 1, 100, 10)

	assert.False(t, result.ShouldStop)
	assert.Empty(t, result.Events)
}

func TestPreEvolveCheck_MajorityUnevaluated(t *testing.T) {
	defer discardLogs()()
	g, _ := NewEvolutionGuardrails(
		WithBaselineScore(80.0),
		WithMaxStagnantGenerations(5),
	)

	ctx := context.Background()
	result := g.PreEvolveCheck(ctx, 75.0, 1, 100, 60)

	assert.True(t, result.ShouldStop)
	require.Len(t, result.Events, 1)
	assert.Equal(t, GuardrailCritical, result.Events[0].Level)
	assert.Equal(t, "unevaluated_population", result.Events[0].Rule)
	assert.Contains(t, result.Events[0].Message, "majority population unevaluated")
	assert.Equal(t, 1, result.Events[0].Generation)
}

func TestPreEvolveCheck_StagnationExceeded(t *testing.T) {
	defer discardLogs()()
	g, _ := NewEvolutionGuardrails(
		WithBaselineScore(80.0),
		WithMaxStagnantGenerations(3),
	)

	// Simulate stagnation by calling PostEvolveCheck multiple times without improvement
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		g.PostEvolveCheck(ctx, 75.0, i+1, nil)
	}

	result := g.PreEvolveCheck(ctx, 75.0, 5, 100, 10)

	assert.False(t, result.ShouldStop) // Warning should not stop
	require.Len(t, result.Events, 1)
	assert.Equal(t, GuardrailWarning, result.Events[0].Level)
	assert.Equal(t, "stagnation", result.Events[0].Rule)
	assert.Contains(t, result.Events[0].Message, "no improvement for")
}

func TestPostEvolveCheck_ImprovementResetsStagnation(t *testing.T) {
	defer discardLogs()()
	g, _ := NewEvolutionGuardrails(
		WithBaselineScore(70.0),
		WithMaxStagnantGenerations(5),
	)

	ctx := context.Background()

	// First call - initial score (improvement from 0)
	result := g.PostEvolveCheck(ctx, 80.0, 1, nil)
	assert.False(t, result.ShouldStop)
	assert.Equal(t, 0, g.StagnantCount())

	// Second call with same score (no improvement)
	result = g.PostEvolveCheck(ctx, 80.0, 2, nil)
	assert.False(t, result.ShouldStop)
	assert.Equal(t, 1, g.StagnantCount())

	// Third call with better score (improvement)
	result = g.PostEvolveCheck(ctx, 85.0, 3, nil)
	assert.False(t, result.ShouldStop)
	assert.Equal(t, 0, g.StagnantCount()) // Should reset to 0
}

func TestPostEvolveCheck_NoImprovementIncrementsStagnation(t *testing.T) {
	defer discardLogs()()
	g, _ := NewEvolutionGuardrails(
		WithBaselineScore(70.0),
		WithMaxStagnantGenerations(5),
	)

	ctx := context.Background()

	// Initial improvement
	g.PostEvolveCheck(ctx, 80.0, 1, nil)
	assert.Equal(t, 0, g.StagnantCount())

	// Multiple calls without improvement
	for i := 2; i <= 5; i++ {
		g.PostEvolveCheck(ctx, 79.0, i, nil)
		expectedStagnant := i - 1 // First non-improvement call sets stagnantCount=1
		assert.Equal(t, expectedStagnant, g.StagnantCount(), "generation %d", i)
	}
}

func TestPostEvolveCheck_RegressionBelowBaseline(t *testing.T) {
	defer discardLogs()()
	g, _ := NewEvolutionGuardrails(
		WithBaselineScore(85.0),
		WithMaxStagnantGenerations(5),
	)

	ctx := context.Background()

	// First call establishes best known above baseline
	g.PostEvolveCheck(ctx, 90.0, 1, nil)

	// Second call drops below baseline
	result := g.PostEvolveCheck(ctx, 80.0, 2, nil)

	assert.True(t, result.ShouldStop)
	require.Len(t, result.Events, 1)
	assert.Equal(t, GuardrailCritical, result.Events[0].Level)
	assert.Equal(t, "baseline_regression", result.Events[0].Rule)
	assert.Contains(t, result.Events[0].Message, "best score regressed below baseline")
}

func TestPostEvolveCheck_LineageConcentrationWarning(t *testing.T) {
	defer discardLogs()()
	g, _ := NewEvolutionGuardrails(
		WithBaselineScore(70.0),
		WithMaxStagnantGenerations(5),
		WithMaxLineageShare(0.5), // Set low threshold
	)

	ctx := context.Background()

	// Lineage shares with one dominant lineage (>50%)
	lineageShares := map[string]int{
		"lineage-a": 80,
		"lineage-b": 10,
		"lineage-c": 10,
	}

	result := g.PostEvolveCheck(ctx, 85.0, 1, lineageShares)

	assert.False(t, result.ShouldStop) // Warning should not stop
	require.Len(t, result.Events, 1)
	assert.Equal(t, GuardrailWarning, result.Events[0].Level)
	assert.Equal(t, "lineage_concentration", result.Events[0].Rule)
	assert.Contains(t, result.Events[0].Message, "lineage concentration")
}

func TestPostEvolveCheck_LineageWithinThreshold(t *testing.T) {
	defer discardLogs()()
	g, _ := NewEvolutionGuardrails(
		WithBaselineScore(70.0),
		WithMaxStagnantGenerations(5),
		WithMaxLineageShare(0.9), // High threshold
	)

	ctx := context.Background()

	// Lineage shares within threshold
	lineageShares := map[string]int{
		"lineage-a": 60,
		"lineage-b": 30,
		"lineage-c": 10,
	}

	result := g.PostEvolveCheck(ctx, 85.0, 1, lineageShares)

	assert.False(t, result.ShouldStop)
	assert.Empty(t, result.Events)
}

func TestEventsRecordedAndRetrievable(t *testing.T) {
	defer discardLogs()()
	g, _ := NewEvolutionGuardrails(
		WithBaselineScore(80.0),
		WithMaxStagnantGenerations(3),
	)

	ctx := context.Background()

	// Trigger an event via PreEvolveCheck
	g.PreEvolveCheck(ctx, 75.0, 1, 100, 60)

	// Trigger an event via PostEvolveCheck
	g.PostEvolveCheck(ctx, 70.0, 2, nil)

	events := g.Events()
	require.Len(t, events, 2)

	// Verify first event (unevaluated population)
	assert.Equal(t, GuardrailCritical, events[0].Level)
	assert.Equal(t, "unevaluated_population", events[0].Rule)

	// Verify second event (baseline regression)
	assert.Equal(t, GuardrailCritical, events[1].Level)
	assert.Equal(t, "baseline_regression", events[1].Rule)

	// Manually record a custom event
	customEvent := GuardrailEvent{
		Level:           GuardrailInfo,
		Rule:            "custom_rule",
		Message:         "test event",
		Generation:      3,
		Timestamp:       time.Now(),
		SuggestedAction: "none",
	}
	g.RecordEvent(customEvent)

	events = g.Events()
	require.Len(t, events, 3)
	assert.Equal(t, "custom_rule", events[2].Rule)
}

func TestReset_ClearsState(t *testing.T) {
	defer discardLogs()()
	g, _ := NewEvolutionGuardrails(
		WithBaselineScore(80.0),
		WithMaxStagnantGenerations(3),
	)

	ctx := context.Background()

	// Generate some state
	g.PreEvolveCheck(ctx, 75.0, 1, 100, 60)
	g.PostEvolveCheck(ctx, 90.0, 2, nil)
	g.PostEvolveCheck(ctx, 88.0, 3, nil)

	// Verify state exists before reset
	assert.Greater(t, g.StagnantCount(), 0)
	assert.NotEmpty(t, g.Events())

	// Reset
	g.Reset()

	// Verify cleared state
	assert.Equal(t, 0, g.StagnantCount())
	assert.Empty(t, g.Events())
}

func TestConcurrentAccessSafety(t *testing.T) {
	defer discardLogs()()
	g, _ := NewEvolutionGuardrails(
		WithBaselineScore(80.0),
		WithMaxStagnantGenerations(5),
	)

	ctx := context.Background()
	var wg sync.WaitGroup

	// Launch multiple goroutines that concurrently access guardrails
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Mix of read and write operations
			switch id % 4 {
			case 0:
				g.PreEvolveCheck(ctx, float64(id*10), id, 100, id%30)
			case 1:
				lineageShares := map[string]int{
					"a": 50 + id,
					"b": 50 - id,
				}
				g.PostEvolveCheck(ctx, float64(80+id), id+1, lineageShares)
			case 2:
				_ = g.StagnantCount()
			case 3:
				_ = g.Events()
			}
		}(i)
	}

	// Wait for all goroutines to complete with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success - no deadlock or race condition
		t.Log("Concurrent access test passed")
	case <-time.After(5 * time.Second):
		t.Fatal("Concurrent access test timed out - possible deadlock")
	}
}

func TestPreEvolveCheck_ZeroPopulation(t *testing.T) {
	g, _ := NewEvolutionGuardrails(
		WithBaselineScore(80.0),
		WithMaxStagnantGenerations(5),
	)

	ctx := context.Background()
	result := g.PreEvolveCheck(ctx, 75.0, 1, 0, 0)

	// Should not panic on zero population
	assert.False(t, result.ShouldStop)
	assert.Empty(t, result.Events)
}

func TestPostEvolveCheck_NilLineageShares(t *testing.T) {
	defer discardLogs()()
	g, _ := NewEvolutionGuardrails(
		WithBaselineScore(70.0),
		WithMaxStagnantGenerations(5),
		WithMaxLineageShare(0.5),
	)

	ctx := context.Background()

	// Should handle nil lineageShares gracefully
	result := g.PostEvolveCheck(ctx, 85.0, 1, nil)

	assert.False(t, result.ShouldStop)
	assert.Empty(t, result.Events)
}

func TestPostEvolveCheck_ZeroBaseline(t *testing.T) {
	defer discardLogs()()
	g, _ := NewEvolutionGuardrails(
		WithBaselineScore(0), // Zero baseline means disabled
		WithMaxStagnantGenerations(5),
	)

	ctx := context.Background()

	// Should not trigger regression check when baseline is 0
	result := g.PostEvolveCheck(ctx, -10.0, 1, nil)

	assert.False(t, result.ShouldStop)
	assert.Empty(t, result.Events)
}

func TestEvents_MaxEventsLimit(t *testing.T) {
	defer discardLogs()()
	g, _ := NewEvolutionGuardrails(
		WithBaselineScore(80.0),
		WithMaxStagnantGenerations(3),
	)

	// Set a small max events limit
	g.mu.Lock()
	g.MaxEvents = 5
	g.mu.Unlock()

	ctx := context.Background()

	// Generate more events than MaxEvents allows
	for i := 0; i < 10; i++ {
		g.PreEvolveCheck(ctx, 75.0, i+1, 100, 60+i)
	}

	events := g.Events()
	// Should have at most MaxEvents
	assert.LessOrEqual(t, len(events), 5)
}

// --- GuardrailErrorCode tests ---

func TestGuardrailErrorCode_UnevaluatedPopulation(t *testing.T) {
	defer discardLogs()()
	g, _ := NewEvolutionGuardrails()
	ctx := context.Background()
	result := g.PreEvolveCheck(ctx, 75.0, 1, 100, 60)
	require.Len(t, result.Events, 1)
	assert.Equal(t, ErrCodeUnevaluatedPopulation, result.Events[0].ErrorCode)
}

func TestGuardrailErrorCode_Stagnation(t *testing.T) {
	defer discardLogs()()
	g, _ := NewEvolutionGuardrails(WithMaxStagnantGenerations(3))
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		g.PostEvolveCheck(ctx, 75.0, i+1, nil)
	}
	result := g.PreEvolveCheck(ctx, 75.0, 5, 100, 10)
	require.Len(t, result.Events, 1)
	assert.Equal(t, ErrCodeStagnation, result.Events[0].ErrorCode)
}

func TestGuardrailErrorCode_BaselineRegression(t *testing.T) {
	defer discardLogs()()
	g, _ := NewEvolutionGuardrails(WithBaselineScore(85.0))
	ctx := context.Background()
	g.PostEvolveCheck(ctx, 90.0, 1, nil)
	result := g.PostEvolveCheck(ctx, 80.0, 2, nil)
	require.Len(t, result.Events, 1)
	assert.Equal(t, ErrCodeBaselineRegression, result.Events[0].ErrorCode)
}

func TestGuardrailErrorCode_LineageConcentration(t *testing.T) {
	defer discardLogs()()
	g, _ := NewEvolutionGuardrails(WithMaxLineageShare(0.5))
	ctx := context.Background()
	lineageShares := map[string]int{"lineage-a": 80, "lineage-b": 10, "lineage-c": 10}
	result := g.PostEvolveCheck(ctx, 85.0, 1, lineageShares)
	require.Len(t, result.Events, 1)
	assert.Equal(t, ErrCodeLineageConcentration, result.Events[0].ErrorCode)
}

func TestGuardrailError_ErrorFormat(t *testing.T) {
	err := &GuardrailError{
		Code:       ErrCodeBaselineRegression,
		Message:    "best score regressed below baseline",
		Generation: 5,
		Score:      70.0,
		Threshold:  85.0,
	}
	msg := err.Error()
	assert.Contains(t, msg, string(ErrCodeBaselineRegression))
	assert.Contains(t, msg, "best score regressed below baseline")
	assert.Contains(t, msg, "gen=5")
	assert.Contains(t, msg, "score=70.00")
	assert.Contains(t, msg, "threshold=85.00")
}

func TestToGuardrailError_UnevaluatedPopulation(t *testing.T) {
	defer discardLogs()()
	g, _ := NewEvolutionGuardrails()
	event := GuardrailEvent{
		Rule:      "unevaluated_population",
		ErrorCode: ErrCodeUnevaluatedPopulation,
		Message:   "majority population unevaluated",
		Score:     75.0,
	}
	ge := g.ToGuardrailError(event)
	require.NotNil(t, ge)
	assert.Equal(t, ErrCodeUnevaluatedPopulation, ge.Code)
	assert.Equal(t, 75.0, ge.Score)
	assert.Equal(t, 0.5, ge.Threshold)
	assert.Error(t, ge)
}

func TestToGuardrailError_Stagnation(t *testing.T) {
	g, _ := NewEvolutionGuardrails(WithMaxStagnantGenerations(5))
	event := GuardrailEvent{
		Rule:      "stagnation",
		ErrorCode: ErrCodeStagnation,
		Message:   "no improvement for 5 generations",
		Score:     75.0,
	}
	ge := g.ToGuardrailError(event)
	require.NotNil(t, ge)
	assert.Equal(t, ErrCodeStagnation, ge.Code)
	assert.Equal(t, float64(5), ge.Threshold)
	assert.Error(t, ge)
}

func TestToGuardrailError_BaselineRegression(t *testing.T) {
	g, _ := NewEvolutionGuardrails(WithBaselineScore(85.0))
	event := GuardrailEvent{
		Rule:      "baseline_regression",
		ErrorCode: ErrCodeBaselineRegression,
		Message:   "best score regressed below baseline",
		Score:     70.0,
	}
	ge := g.ToGuardrailError(event)
	require.NotNil(t, ge)
	assert.Equal(t, ErrCodeBaselineRegression, ge.Code)
	assert.Equal(t, 85.0, ge.Threshold)
	assert.Equal(t, 70.0, ge.Score)
	assert.Error(t, ge)
}

func TestToGuardrailError_LineageConcentration(t *testing.T) {
	g, _ := NewEvolutionGuardrails(WithMaxLineageShare(0.5))
	event := GuardrailEvent{
		Rule:      "lineage_concentration",
		ErrorCode: ErrCodeLineageConcentration,
		Message:   "lineage concentration exceeds threshold",
		Score:     85.0,
	}
	ge := g.ToGuardrailError(event)
	require.NotNil(t, ge)
	assert.Equal(t, ErrCodeLineageConcentration, ge.Code)
	assert.Equal(t, 0.5, ge.Threshold)
	assert.Error(t, ge)
}

func TestToGuardrailError_UnknownRule(t *testing.T) {
	g, _ := NewEvolutionGuardrails()
	event := GuardrailEvent{
		Rule:      "unknown_rule",
		ErrorCode: "",
		Message:   "unknown event",
	}
	ge := g.ToGuardrailError(event)
	assert.Nil(t, ge)
}

func TestGuardrailEvent_ErrorCodeInAutomatedDecision(t *testing.T) {
	defer discardLogs()()
	g, _ := NewEvolutionGuardrails(WithBaselineScore(85.0))
	ctx := context.Background()

	// Trigger baseline regression.
	g.PostEvolveCheck(ctx, 90.0, 1, nil)
	result := g.PostEvolveCheck(ctx, 80.0, 2, nil)

	require.Len(t, result.Events, 1)
	event := result.Events[0]
	assert.Equal(t, ErrCodeBaselineRegression, event.ErrorCode)

	// Simulate automated decision: should stop based on error code.
	if event.ErrorCode == ErrCodeBaselineRegression || event.ErrorCode == ErrCodeUnevaluatedPopulation {
		assert.True(t, result.ShouldStop)
	} else {
		assert.False(t, result.ShouldStop)
	}

	// Converting to GuardrailError for retry/rollback logic.
	guardrailErr := g.ToGuardrailError(event)
	require.NotNil(t, guardrailErr)
	assert.Equal(t, ErrCodeBaselineRegression, guardrailErr.Code)
	assert.Equal(t, 85.0, guardrailErr.Threshold)
	assert.Equal(t, 80.0, guardrailErr.Score)
}

func TestGuardrailEvent_ScoreFieldPopulated(t *testing.T) {
	defer discardLogs()()
	g, _ := NewEvolutionGuardrails(WithBaselineScore(50.0))
	ctx := context.Background()

	// Unevaluated population event should have score.
	result := g.PreEvolveCheck(ctx, 75.0, 1, 100, 60)
	require.Len(t, result.Events, 1)
	assert.Equal(t, 75.0, result.Events[0].Score)

	// Baseline regression event should have score.
	result2 := g.PostEvolveCheck(ctx, 40.0, 2, nil)
	require.Len(t, result2.Events, 1)
	assert.Equal(t, 40.0, result2.Events[0].Score)
}

func TestValidateToolSet(t *testing.T) {
	defer discardLogs()()

	t.Run("within_bound_no_stop", func(t *testing.T) {
		g, _ := NewEvolutionGuardrails(WithMaxToolsEnabled(3), WithRequireAnyTool(true))
		res := g.ValidateToolSet(1, []string{"a", "b"})
		assert.False(t, res.ShouldStop, "a 2-tool set within bound 3 must not stop")
		assert.Empty(t, res.Events)
	})

	t.Run("exceeds_bound_stops_and_counts", func(t *testing.T) {
		g, _ := NewEvolutionGuardrails(WithMaxToolsEnabled(2))
		res := g.ValidateToolSet(1, []string{"a", "b", "c"})
		assert.True(t, res.ShouldStop, "a 3-tool set against bound 2 must stop")
		require.Len(t, res.Events, 1)
		assert.Equal(t, ErrCodeInvalidToolSet, res.Events[0].ErrorCode)
		assert.Equal(t, "tool_set_upper_bound", res.Events[0].Rule)
		// The event is recorded so the metrics/metrics observer can count it.
		assert.Len(t, g.Events(), 1)
	})

	t.Run("zero_tools_rejected_when_required", func(t *testing.T) {
		g, _ := NewEvolutionGuardrails(WithRequireAnyTool(true))
		res := g.ValidateToolSet(1, nil)
		assert.True(t, res.ShouldStop, "zero enabled tools must stop when requireAnyTool")
		require.Len(t, res.Events, 1)
		assert.Equal(t, "tool_set_empty", res.Events[0].Rule)
	})

	t.Run("zero_tools_allowed_when_not_required", func(t *testing.T) {
		// Text-only strategies are legitimate; the check is opt-in.
		g, _ := NewEvolutionGuardrails()
		res := g.ValidateToolSet(1, nil)
		assert.False(t, res.ShouldStop)
	})

	t.Run("disabled_bound_does_not_cap", func(t *testing.T) {
		g, _ := NewEvolutionGuardrails() // MaxToolsEnabled == 0 (disabled)
		res := g.ValidateToolSet(1, []string{"a", "b", "c", "d", "e"})
		assert.False(t, res.ShouldStop, "a disabled upper bound must not reject any set size")
	})

	t.Run("exactly_at_bound_ok", func(t *testing.T) {
		g, _ := NewEvolutionGuardrails(WithMaxToolsEnabled(3))
		res := g.ValidateToolSet(1, []string{"a", "b", "c"})
		assert.False(t, res.ShouldStop, "a set exactly at the bound is allowed")
	})
}

// TestValidateToolSetKnownTools covers §8.6-3: a whitelist naming tools that are
// not registered is NOT a narrower tool set. At runtime it intersects to zero and
// the executors fall back to the FULL set, so such a strategy silently becomes
// the broadest one. The guardrail must catch that at selection time.
func TestValidateToolSetKnownTools(t *testing.T) {
	defer discardLogs()()

	t.Run("all_names_known_no_stop", func(t *testing.T) {
		g, _ := NewEvolutionGuardrails(WithKnownTools([]string{"web_search", "calculator"}))
		res := g.ValidateToolSet(1, []string{"calculator"})
		assert.False(t, res.ShouldStop)
		assert.Empty(t, res.Events)
	})

	t.Run("unknown_name_stops", func(t *testing.T) {
		g, _ := NewEvolutionGuardrails(WithKnownTools([]string{"web_search"}))
		res := g.ValidateToolSet(7, []string{"web_search", "hallucinated_tool"})
		assert.True(t, res.ShouldStop, "an unregistered tool name must stop promotion")
		require.Len(t, res.Events, 1)
		assert.Equal(t, "tool_set_unknown_name", res.Events[0].Rule)
		assert.Equal(t, ErrCodeInvalidToolSet, res.Events[0].ErrorCode)
		assert.Equal(t, GuardrailCritical, res.Events[0].Level)
		assert.Equal(t, 7, res.Events[0].Generation)
		assert.Contains(t, res.Events[0].Message, "hallucinated_tool")
		// Only the unknown name is scored, not the whole set.
		assert.Equal(t, float64(1), res.Events[0].Score)
		assert.Len(t, g.Events(), 1, "event must be recorded for the metrics observer")
	})

	t.Run("all_names_unknown_stops", func(t *testing.T) {
		// The worst case: zero intersection with the registry, which the
		// executors would promote to "all tools enabled".
		g, _ := NewEvolutionGuardrails(WithKnownTools([]string{"web_search"}))
		res := g.ValidateToolSet(1, []string{"x", "y"})
		assert.True(t, res.ShouldStop)
		require.Len(t, res.Events, 1)
		assert.Equal(t, float64(2), res.Events[0].Score)
	})

	t.Run("no_vocabulary_disables_check", func(t *testing.T) {
		// Deployments that cannot enumerate their registry keep prior behavior.
		g, _ := NewEvolutionGuardrails()
		res := g.ValidateToolSet(1, []string{"anything"})
		assert.False(t, res.ShouldStop)
	})

	t.Run("empty_vocabulary_disables_check", func(t *testing.T) {
		g, _ := NewEvolutionGuardrails(WithKnownTools([]string{"  ", ""}))
		res := g.ValidateToolSet(1, []string{"anything"})
		assert.False(t, res.ShouldStop, "a blank-only vocabulary must disable, not reject-all")
	})

	t.Run("vocabulary_names_are_trimmed", func(t *testing.T) {
		g, _ := NewEvolutionGuardrails(WithKnownTools([]string{" web_search "}))
		res := g.ValidateToolSet(1, []string{"web_search"})
		assert.False(t, res.ShouldStop)
	})

	t.Run("empty_tool_set_skips_vocabulary_check", func(t *testing.T) {
		// An empty set is the requireAnyTool rule's business, not this one's;
		// it must not also produce an unknown-name event.
		g, _ := NewEvolutionGuardrails(WithKnownTools([]string{"web_search"}))
		res := g.ValidateToolSet(1, nil)
		assert.False(t, res.ShouldStop)
		assert.Empty(t, res.Events)
	})

	t.Run("both_rules_can_fire", func(t *testing.T) {
		g, _ := NewEvolutionGuardrails(
			WithMaxToolsEnabled(1),
			WithKnownTools([]string{"web_search"}),
		)
		res := g.ValidateToolSet(1, []string{"web_search", "ghost"})
		assert.True(t, res.ShouldStop)
		assert.Len(t, res.Events, 2, "over-bound and unknown-name are independent findings")
	})
}
