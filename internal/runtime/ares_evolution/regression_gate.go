// regression_gate.go provides the arena-backed RELATIVE regression gate for
// the StrategyLifecycle verify pipeline. It is the arena wiring the plan's
// M4 line item called for: the existing EvalGate scores a candidate in
// ABSOLUTE terms (avg >= MinScore), which cannot catch a candidate that
// passes the floor yet still significantly degrades the CURRENT active
// strategy on the cases that matter. This gate runs the preserved-case
// A/B comparison through ares_arena.RegressionTester (paired runs, Welch's
// t-test) and rejects a candidate only on a statistically significant
// regression — an improvement or a statistically indistinguishable tie
// passes (the shadow/staging channel owns the "is it better" question;
// this gate owns "did it get worse").
//
// Pipeline position:
//
//	G1 guardrail → G2 shadow → G3 eval suite → arena regression → staging
//
// The gate is opt-in (evolution.gates.regression_enabled): each Check runs
// 2×runs LLM scoring rounds, so it must never silently activate.
package evolution

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	ares_arena "github.com/Timwood0x10/ares/internal/runtime/arena"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// DefaultRegressionGateTimeout bounds one gate Check (both strategy sides,
// all runs). The lifecycle's Submit drives gates on the evolution heartbeat,
// so a hung LLM must not stall the pipeline indefinitely. 5 minutes covers
// the default 5×2 scoring rounds with margin for provider latency.
const DefaultRegressionGateTimeout = 5 * time.Minute

// ArenaRegressionGateConfig configures the arena regression gate.
type ArenaRegressionGateConfig struct {
	// Runs is the per-strategy run count (baseline and compare each run the
	// preserved cases this many times). Default 5.
	Runs int
	// MinWinRate is the informational floor surfaced in the pass reason; the
	// REJECT decision is significance-based (Confident && NewAvg < OldAvg),
	// not win-rate-based. Default 0.55.
	MinWinRate float64
	// Confidence is the significance level for Welch's t-test. Default 0.05.
	Confidence float64
	// Timeout bounds one Check. Zero means DefaultRegressionGateTimeout.
	Timeout time.Duration
}

// ArenaRegressionGate is the preserved-case A/B regression gate over the
// ares_arena.RegressionTester. It implements VerifyGate.
type ArenaRegressionGate struct {
	tester *ares_arena.RegressionTester
	cases  []any
	cfg    ArenaRegressionGateConfig
	logger *slog.Logger
	// rejected counts gate rejections (observability). Atomic: the
	// lifecycle runs gates OUTSIDE its mutex (Submit unlocks before the
	// gate loop), so two concurrent Submits can run Check concurrently —
	// a plain increment would be a data race.
	rejected atomic.Int64
	// checked counts Check invocations (observability, same reasoning).
	checked atomic.Int64
}

// Compile-time guarantee: the gate satisfies the lifecycle's VerifyGate.
var _ VerifyGate = (*ArenaRegressionGate)(nil)

// NewArenaRegressionGate builds the gate from a scorer (typically
// service.LLMArenaScorer over the eval LLM client) and the preserved-case
// suite. A nil scorer or empty case list is a construction error: the wiring
// layer must not build a gate that can never discriminate. The case slice is
// defensively copied — the gate is a long-lived object and must not race a
// caller that reuses its slice.
func NewArenaRegressionGate(scorer ares_arena.Scorer, testCases []any, cfg ArenaRegressionGateConfig) (*ArenaRegressionGate, error) {
	if scorer == nil {
		return nil, fmt.Errorf("arena regression gate: scorer must not be nil")
	}
	if len(testCases) == 0 {
		return nil, fmt.Errorf("arena regression gate: preserved-case suite is empty")
	}
	if cfg.Runs <= 0 {
		cfg.Runs = 5
	}
	if cfg.MinWinRate <= 0 {
		cfg.MinWinRate = 0.55
	}
	if cfg.Confidence <= 0 {
		cfg.Confidence = 0.05
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultRegressionGateTimeout
	}
	tester, err := ares_arena.NewRegressionTesterWithScorer(scorer)
	if err != nil {
		return nil, fmt.Errorf("arena regression gate: build tester: %w", err)
	}
	return &ArenaRegressionGate{
		tester: tester,
		cases:  append([]any(nil), testCases...),
		cfg:    cfg,
		logger: slog.Default(),
	}, nil
}

// Name identifies the gate in lifecycle logs and rejection records.
func (g *ArenaRegressionGate) Name() string { return "arena_regression" }

// Config returns the effective gate configuration (defaults filled in) so
// wiring layers can assert what was actually built.
func (g *ArenaRegressionGate) Config() ArenaRegressionGateConfig { return g.cfg }

// Rejected reports how many candidates this gate has rejected.
func (g *ArenaRegressionGate) Rejected() int64 { return g.rejected.Load() }

// Checked reports how many candidates this gate has evaluated.
func (g *ArenaRegressionGate) Checked() int64 { return g.checked.Load() }

// Check runs the preserved-case A/B comparison: active strategy (baseline)
// vs candidate (compare), paired over the same test cases. The gate rejects
// ONLY on a statistically significant regression (Confident && NewAvg <
// OldAvg) — a tie passes to the staging channel, which owns the "is it
// better" verdict. Scorer failures fail CLOSED: a candidate that cannot be
// regression-checked must not ride an unverified promote path.
func (g *ArenaRegressionGate) Check(ctx context.Context, cand, active *mutation.Strategy) (bool, float64, string) {
	if cand == nil || active == nil {
		return false, 0, "arena regression: nil candidate or active strategy"
	}
	g.checked.Add(1)

	checkCtx, cancel := context.WithTimeout(ctx, g.cfg.Timeout)
	defer cancel()

	result, err := g.tester.Run(checkCtx, ares_arena.RegressionConfig{
		// The prompt template is the artifact the GA mutates; it is what
		// the eval executor also runs through, so both gates discriminate
		// on the same surface.
		OldStrategy:  active.PromptTemplate,
		NewStrategy:  cand.PromptTemplate,
		BaselineRuns: g.cfg.Runs,
		CompareRuns:  g.cfg.Runs,
		TestSuite:    "lifecycle-regression",
		Confidence:   g.cfg.Confidence,
		MinWinRate:   g.cfg.MinWinRate,
		TestCases:    g.cases,
	})
	if err != nil {
		g.rejected.Add(1)
		return false, 0, fmt.Sprintf("arena regression run failed (rejected fail-closed): %v", err)
	}

	if result.Confident && result.NewAvg < result.OldAvg {
		g.rejected.Add(1)
		reason := fmt.Sprintf(
			"regression: preserved-suite avg dropped %.3f -> %.3f (win rate %.2f, p=%.4f, samples=%d)",
			result.OldAvg, result.NewAvg, result.WinRate, result.PValue, result.Samples,
		)
		g.logger.WarnContext(ctx, "arena regression gate rejected candidate",
			"candidate", cand.ID, "reason", reason)
		return false, result.WinRate, reason
	}

	// Pass: improvement or statistically indistinguishable tie. The win
	// rate rides as the score so the gate log records which kind of pass.
	var verdict string
	switch {
	case result.Confident && result.NewAvg > result.OldAvg:
		verdict = "significant improvement"
	case result.Confident:
		verdict = "significant tie"
	default:
		verdict = "no significant difference"
	}
	return true, result.WinRate, fmt.Sprintf(
		"preserved-suite ok: old %.3f vs new %.3f, win rate %.2f, p=%.4f, samples=%d (%s)",
		result.OldAvg, result.NewAvg, result.WinRate, result.PValue, result.Samples, verdict,
	)
}
