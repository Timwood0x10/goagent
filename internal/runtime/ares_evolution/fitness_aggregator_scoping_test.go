package evolution

// fitness_aggregator_scoping_test.go locks review fix #4: the rollback
// decision path (Window called WITH a strategy ID) requires the strategy's
// OWN "strategy" source to satisfy MinSamplesBeforeJudge — global sources
// (workflow/scheduler/recovery/dimension_eval) weight the mean but can never
// license a rollback decision on the strategy's behalf (design doc §4⑤
// principle 4: rollback decisions must rest on the strategy's own evidence).

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/evidence"
)

func seedStrategyFitness(t *testing.T, store *evidence.MemoryStore, strategyID string, value float64, n int) {
	t.Helper()
	base := time.Now().Add(-time.Duration(n+1) * time.Second)
	for i := 0; i < n; i++ {
		payload, err := json.Marshal(map[string]any{
			"value": value, "strategy_id": strategyID,
		})
		require.NoError(t, err)
		require.NoError(t, store.Append(context.Background(), evidence.Evidence{
			ID:        "strategy_" + strategyID + "_" + string(rune('a'+i)),
			Source:    "strategy",
			Kind:      evidence.KindFitness,
			Payload:   payload,
			Timestamp: base.Add(time.Duration(i) * time.Second),
		}))
	}
}

func seedGlobalFitness(t *testing.T, store *evidence.MemoryStore, source string, value float64, n int) {
	t.Helper()
	base := time.Now().Add(-time.Duration(n+1) * time.Minute)
	for i := 0; i < n; i++ {
		payload, err := json.Marshal(map[string]any{"value": value})
		require.NoError(t, err)
		require.NoError(t, store.Append(context.Background(), evidence.Evidence{
			ID:        source + "_" + string(rune('a'+i)),
			Source:    source,
			Kind:      evidence.KindFitness,
			Payload:   payload,
			Timestamp: base.Add(time.Duration(i) * time.Second),
		}))
	}
}

func TestAggregator_Window_StrategyScopedJudgeGate(t *testing.T) {
	store := evidence.NewMemoryStore()
	cfg := DefaultAggregatorConfig() // MinSamplesBeforeJudge: 10
	agg := NewRuntimeFitnessAggregator(store, cfg)
	ctx := context.Background()

	// 10 GLOBAL records (workflow 1.0) + only 2 own-strategy records.
	seedGlobalFitness(t, store, "workflow", 1.0, 10)
	seedStrategyFitness(t, store, "cand-1", 0.5, 2)

	// Rollback path (scoped): the strategy's own evidence (2 < 10) must NOT
	// be supplemented by the 10 global records.
	res := agg.Window(ctx, "cand-1")
	assert.False(t, res.Ok, "global sources must not satisfy the strategy-scoped judge gate")
	assert.Equal(t, 12, res.Count, "global records still count toward the reported window")
	// Weighted mean: (1.0×0.15 + 0.5×0.40) / 0.55 ≈ 0.636 — the strategy
	// source still weights the MEAN despite failing the judge gate.
	assert.InDelta(t, 0.636, res.Mean, 0.01)
	// The saturation-safe advance signal must carry the newest timestamp.
	assert.False(t, res.LastAt.IsZero(), "LastAt must report the newest in-window evidence timestamp")

	// Staging path (unscoped): total ≥ 10 → ok (pre-existing contract).
	assert.True(t, agg.Window(ctx, "").Ok, "unscoped window keeps the total-count gate")

	// Once the strategy's OWN samples reach the threshold, the scoped
	// window judges.
	seedStrategyFitness(t, store, "cand-1", 0.5, 8)
	assert.True(t, agg.Window(ctx, "cand-1").Ok, "10 own-strategy samples satisfy the scoped gate")
}

func TestAggregator_Window_ScopedExcludesOtherStrategies(t *testing.T) {
	store := evidence.NewMemoryStore()
	cfg := DefaultAggregatorConfig()
	agg := NewRuntimeFitnessAggregator(store, cfg)
	ctx := context.Background()

	// 10 samples for candidate A, 0 for candidate B.
	seedStrategyFitness(t, store, "cand-a", 1.0, 10)

	assert.True(t, agg.Window(ctx, "cand-a").Ok)

	resB := agg.Window(ctx, "cand-b")
	assert.False(t, resB.Ok, "another strategy's samples must not count for cand-b")
	assert.Equal(t, 0, resB.Count, "scoped count reports only the strategy's own samples")
}
