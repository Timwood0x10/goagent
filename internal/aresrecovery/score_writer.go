package aresrecovery

import (
	"context"
	"log/slog"
	"time"
)

// StrategyScoreWriter is the write-back interface for the zero-LLM feedback
// loop. It abstracts the evolution system's StrategyStore so the
// aresrecovery package does not need to import the evolution package (which
// would create a circular dependency). The wiring layer (cmd/ares) adapts
// the evolution.StrategyStore into this interface.
//
// The contract: WriteActiveScore updates the currently-active strategy's
// Score field and persists it, without changing which strategy is active.
// This is the "score write-back" half of the zero-LLM feedback loop:
//
//	ExecutionAttribution → DeterministicScorer → StrategyScoreWriter →
//	StrategyStore → GA reads updated Score on next generation.
type StrategyScoreWriter interface {
	// WriteActiveScore updates the active strategy's Score in-place.
	// A score outside [0,1] is rejected. Returns an error if no active
	// strategy exists or the write fails.
	WriteActiveScore(ctx context.Context, score float64) error
}

// ScoredFeedbackAdapter extends EvolutionFeedbackAdapter with zero-LLM
// score write-back. It wraps the existing confidence-injection
// behavior and adds a periodic write of the deterministic scorer's
// aggregate score to the StrategyScoreWriter, so the active strategy's
// Score field tracks real execution outcomes without any LLM call.
//
// Thread-safe: the scorer and writer are stateless or thread-safe.
type ScoredFeedbackAdapter struct {
	inner  *EvolutionFeedbackAdapter
	scorer *DeterministicScorer
	writer StrategyScoreWriter
}

// NewScoredFeedbackAdapter wraps the existing confidence-injection adapter
// with zero-LLM score write-back. The inner adapter may be nil (confidence
// injection is skipped); the writer may be nil (score write-back is skipped).
// At least one must be non-nil or the adapter is a no-op.
func NewScoredFeedbackAdapter(
	inner *EvolutionFeedbackAdapter,
	scorer *DeterministicScorer,
	writer StrategyScoreWriter,
) *ScoredFeedbackAdapter {
	if scorer == nil {
		scorer = NewDeterministicScorer()
	}
	return &ScoredFeedbackAdapter{inner: inner, scorer: scorer, writer: writer}
}

// Apply runs both halves of the feedback loop:
// 1. Confidence injection (delegated to the inner adapter).
// 2. Zero-LLM score write-back (deterministic scorer → writer).
//
// Returns the number of agents whose confidence was updated (from the
// inner adapter). Score write-back errors are logged but do not affect
// the return value — the confidence injection is the primary feedback
// mechanism and a score write failure must not suppress it.
func (a *ScoredFeedbackAdapter) Apply(ctx context.Context) int {
	if a == nil {
		return 0
	}
	updated := 0
	if a.inner != nil {
		updated = a.inner.Apply(ctx)
	}
	// Write the deterministic aggregate score back to the active
	// strategy's Score field. This is the zero-LLM path: the score comes
	// from execution attribution, not from an LLM evaluator.
	if a.writer != nil && a.inner != nil && a.inner.source != nil {
		score := a.scorer.ScoreAttribution(a.inner.source)
		if err := a.writer.WriteActiveScore(ctx, score); err != nil {
			slog.Warn("aresrecovery: score write-back failed",
				slog.Float64("score", score),
				slog.String("error", err.Error()))
		}
	}
	return updated
}

// RunScoredFeedbackLoop periodically runs the scored feedback adapter. It
// is the scored replacement for RunEvolutionFeedbackLoop: it does everything
// the unscored loop did (confidence injection) plus the zero-LLM score
// write-back. Apply is idempotent.
//
// Args:
//   - ctx: stops the loop.
//   - adapter: the scored feedback adapter (nil disables the loop).
//   - interval: how often to apply; <= 0 uses a 10s default.
func RunScoredFeedbackLoop(ctx context.Context, adapter *ScoredFeedbackAdapter, interval time.Duration) {
	if adapter == nil {
		return
	}
	if interval <= 0 {
		interval = 10 * time.Second
	}
	apply := func(phase string) int {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("scored feedback loop panic recovered",
					slog.String("phase", phase),
					slog.Any("panic", r))
			}
		}()
		return adapter.Apply(ctx)
	}
	apply("startup")
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			apply("tick")
		case <-ctx.Done():
			return
		}
	}
}
