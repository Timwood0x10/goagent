package main

import (
	"context"
	"fmt"

	"github.com/Timwood0x10/ares/internal/aresrecovery"
	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
)

// strategyScoreAdapter bridges the aresrecovery.StrategyScoreWriter interface
// to the evolution system's StrategyStore (C2.3). It reads the currently
// active strategy, updates its Score, and persists it back — without
// changing which strategy is active.
//
// This adapter breaks what would otherwise be a circular import:
// aresrecovery cannot import evolution (evolution imports ares_events
// which is shared), so the wiring layer adapts evolution.StrategyStore
// into the StrategyScoreWriter interface here.
type strategyScoreAdapter struct {
	store evolution.StrategyStore
}

// newStrategyScoreAdapter creates a StrategyScoreWriter backed by the
// evolution system's StrategyStore. A nil store yields a nil adapter
// (the caller must check).
func newStrategyScoreAdapter(store evolution.StrategyStore) aresrecovery.StrategyScoreWriter {
	if store == nil {
		return nil
	}
	return &strategyScoreAdapter{store: store}
}

// WriteActiveScore reads the active strategy, updates its Score, and
// persists it. This is the zero-LLM score write-back (C2.3): the score
// comes from the deterministic scorer (aresrecovery.DeterministicScorer),
// not from an LLM evaluator.
//
// Args:
//   - ctx: for cancellation.
//   - score: the deterministic aggregate score, must be in [0,1].
//
// Returns:
//   - error: non-nil if no active strategy exists, the score is out of
//     range, or the store write fails.
func (a *strategyScoreAdapter) WriteActiveScore(ctx context.Context, score float64) error {
	if score < 0.0 || score > 1.0 {
		return fmt.Errorf("strategy score adapter: score %.4f out of range [0,1]", score)
	}
	st, err := a.store.GetActive(ctx)
	if err != nil {
		return fmt.Errorf("strategy score adapter: get active: %w", err)
	}
	if st == nil {
		return fmt.Errorf("strategy score adapter: no active strategy")
	}
	// Update Score in-place and persist. This does NOT change which
	// strategy is active (SetActive replaces the active entry, not the
	// identity).
	st.Score = score
	if err := a.store.SetActive(ctx, st); err != nil {
		return fmt.Errorf("strategy score adapter: set active: %w", err)
	}
	return nil
}
