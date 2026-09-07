package evolution

// gate_eval_test.go covers the EvalGate (G3) scoring logic that the
// lifecycle's pass-through path does not exercise: averageScores and the
// full Check path with a real AgentTestRunner backed by fake executor and
// evaluator. Previously only the pass-through branch had coverage (via the
// lifecycle tests), so a regression in the scoring path would have shipped
// silently.

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
	"github.com/Timwood0x10/ares/internal/runtime/eval"
)

// --- fakes ---

// fakeExecutor returns a canned output for every test case.
type fakeExecutor struct{ output string }

func (f *fakeExecutor) Execute(_ context.Context, _ string) (string, []string, int, error) {
	return f.output, nil, 1, nil
}

// fakeEvaluator scores every result with a fixed value, failing when
// failWith is non-nil (to exercise the "evaluator errored" path).
type fakeEvaluator struct {
	score    float64
	failWith error
}

func (f *fakeEvaluator) Evaluate(_ context.Context, _ eval.TestCase, _ eval.TestResult) ([]eval.EvalScore, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	return []eval.EvalScore{{Metric: "accuracy", Score: f.score}}, nil
}

// newTestEvalGate builds a fully wired G3 gate: a runner over a canned
// executor plus a registry containing one evaluator with the given score.
// The gate config pins EvaluatorName to the fake so the NAMED-evaluator
// scoring path (including its error branch) is exercised deterministically.
func newTestEvalGate(t *testing.T, evaluatorScore float64, evaluatorErr error) *EvalGate {
	t.Helper()
	runner, err := eval.NewAgentTestRunner(&fakeExecutor{output: "ok"})
	require.NoError(t, err)
	registry := eval.NewEvaluatorRegistry()
	require.NoError(t, registry.Register("fake", &fakeEvaluator{score: evaluatorScore, failWith: evaluatorErr}))
	runner.SetRegistry(registry)
	suite := eval.TestSuite{TestCases: []eval.TestCase{
		{ID: "case-1", Input: "hello"},
		{ID: "case-2", Input: "world"},
	}}
	cfg := DefaultEvalGateConfig()
	cfg.EvaluatorName = "fake"
	return NewEvalGate(registry, runner, suite, cfg)
}

// --- averageScores ---

func TestAverageScores(t *testing.T) {
	t.Run("empty inputs return zero", func(t *testing.T) {
		assert.Equal(t, 0.0, averageScores(nil, 0))
		assert.Equal(t, 0.0, averageScores([][]eval.EvalScore{{}}, 0))
	})
	t.Run("mean across cases and metrics", func(t *testing.T) {
		scores := [][]eval.EvalScore{
			{{Metric: "a", Score: 1.0}, {Metric: "b", Score: 0.0}},
			{{Metric: "a", Score: 0.5}},
		}
		// (1.0 + 0.0 + 0.5) / 3
		assert.InDelta(t, 0.5, averageScores(scores, 2), 0.0001)
	})
	t.Run("scores present but no metrics counted", func(t *testing.T) {
		assert.Equal(t, 0.0, averageScores([][]eval.EvalScore{{}, {}}, 2))
	})
}

// --- EvalGate.Check ---

func TestEvalGate_PassThroughWithoutInfrastructure(t *testing.T) {
	t.Run("nil registry", func(t *testing.T) {
		g := NewEvalGate(nil, nil, eval.TestSuite{}, DefaultEvalGateConfig())
		pass, score, reason := g.Check(context.Background(), &mutation.Strategy{}, nil)
		assert.True(t, pass)
		assert.Equal(t, 0.0, score)
		assert.Contains(t, reason, "not configured")
	})
	t.Run("empty suite", func(t *testing.T) {
		runner, err := eval.NewAgentTestRunner(&fakeExecutor{output: "ok"})
		require.NoError(t, err)
		g := NewEvalGate(eval.NewEvaluatorRegistry(), runner, eval.TestSuite{}, DefaultEvalGateConfig())
		pass, _, reason := g.Check(context.Background(), &mutation.Strategy{}, nil)
		assert.True(t, pass)
		assert.Contains(t, reason, "not configured")
	})
}

// TestEvalGate_StrictModeRejectsWhenUnconfigured verifies that with
// StrictMode=true, the gate fails closed (returns false) when no
// eval infrastructure is wired, instead of silently passing.
func TestEvalGate_StrictModeRejectsWhenUnconfigured(t *testing.T) {
	t.Run("nil registry strict mode rejects", func(t *testing.T) {
		cfg := DefaultEvalGateConfig()
		cfg.StrictMode = true
		g := NewEvalGate(nil, nil, eval.TestSuite{}, cfg)
		pass, _, reason := g.Check(context.Background(), &mutation.Strategy{}, nil)
		assert.False(t, pass, "strict mode must reject when registry is nil")
		assert.Contains(t, reason, "strict mode")
		assert.Contains(t, reason, "registry")
	})
	t.Run("nil runner strict mode rejects", func(t *testing.T) {
		cfg := DefaultEvalGateConfig()
		cfg.StrictMode = true
		registry := eval.NewEvaluatorRegistry()
		suite := eval.TestSuite{TestCases: []eval.TestCase{{ID: "c1", Input: "hi"}}}
		g := NewEvalGate(registry, nil, suite, cfg)
		pass, _, reason := g.Check(context.Background(), &mutation.Strategy{}, nil)
		assert.False(t, pass, "strict mode must reject when runner is nil")
		assert.Contains(t, reason, "strict mode")
		assert.Contains(t, reason, "runner")
	})
	t.Run("empty suite strict mode rejects", func(t *testing.T) {
		cfg := DefaultEvalGateConfig()
		cfg.StrictMode = true
		runner, err := eval.NewAgentTestRunner(&fakeExecutor{output: "ok"})
		require.NoError(t, err)
		registry := eval.NewEvaluatorRegistry()
		g := NewEvalGate(registry, runner, eval.TestSuite{}, cfg)
		pass, _, reason := g.Check(context.Background(), &mutation.Strategy{}, nil)
		assert.False(t, pass, "strict mode must reject when suite is empty")
		assert.Contains(t, reason, "strict mode")
		assert.Contains(t, reason, "test suite")
	})
}

// TestEvalGate_SkippedCountIncrements verifies that the skipped counter
// increments on each skip, and that the reason distinguishes the three
// missing-component scenarios.
func TestEvalGate_SkippedCountIncrements(t *testing.T) {
	g := NewEvalGate(nil, nil, eval.TestSuite{}, DefaultEvalGateConfig())
	assert.Equal(t, 0, g.SkippedCount())

	g.Check(context.Background(), &mutation.Strategy{}, nil)
	assert.Equal(t, 1, g.SkippedCount(), "first skip should increment counter")

	g.Check(context.Background(), &mutation.Strategy{}, nil)
	assert.Equal(t, 2, g.SkippedCount(), "second skip should increment counter")
}

func TestEvalGate_ScoringPath(t *testing.T) {
	t.Run("score above threshold passes", func(t *testing.T) {
		g := newTestEvalGate(t, 0.9, nil)
		pass, score, reason := g.Check(context.Background(), &mutation.Strategy{}, nil)
		assert.True(t, pass)
		assert.InDelta(t, 0.9, score, 0.0001)
		assert.Contains(t, reason, ">=")
	})
	t.Run("score below threshold rejects", func(t *testing.T) {
		g := newTestEvalGate(t, 0.3, nil)
		pass, score, reason := g.Check(context.Background(), &mutation.Strategy{}, nil)
		assert.False(t, pass)
		assert.InDelta(t, 0.3, score, 0.0001)
		assert.Contains(t, reason, "<")
	})
	t.Run("evaluator error rejects", func(t *testing.T) {
		g := newTestEvalGate(t, 0.9, errors.New("llm down"))
		pass, score, reason := g.Check(context.Background(), &mutation.Strategy{}, nil)
		assert.False(t, pass)
		assert.Equal(t, 0.0, score)
		assert.Contains(t, reason, "failed")
	})
	t.Run("multi-evaluator path averages across evaluators", func(t *testing.T) {
		runner, err := eval.NewAgentTestRunner(&fakeExecutor{output: "ok"})
		require.NoError(t, err)
		registry := eval.NewEvaluatorRegistry()
		require.NoError(t, registry.Register("strict", &fakeEvaluator{score: 0.4}))
		require.NoError(t, registry.Register("lenient", &fakeEvaluator{score: 1.0}))
		runner.SetRegistry(registry)
		suite := eval.TestSuite{TestCases: []eval.TestCase{{ID: "case-1", Input: "hello"}}}
		// EvaluatorName empty → run ALL evaluators and average: (0.4+1.0)/2.
		g := NewEvalGate(registry, runner, suite, DefaultEvalGateConfig())
		pass, score, _ := g.Check(context.Background(), &mutation.Strategy{}, nil)
		assert.True(t, pass)
		assert.InDelta(t, 0.7, score, 0.0001)
	})
	t.Run("all evaluators fail in multi path passes with skip reason", func(t *testing.T) {
		runner, err := eval.NewAgentTestRunner(&fakeExecutor{output: "ok"})
		require.NoError(t, err)
		registry := eval.NewEvaluatorRegistry()
		require.NoError(t, registry.Register("broken", &fakeEvaluator{failWith: errors.New("down")}))
		runner.SetRegistry(registry)
		suite := eval.TestSuite{TestCases: []eval.TestCase{{ID: "case-1", Input: "hello"}}}
		g := NewEvalGate(registry, runner, suite, DefaultEvalGateConfig())
		pass, _, reason := g.Check(context.Background(), &mutation.Strategy{}, nil)
		assert.True(t, pass)
		assert.Contains(t, reason, "no evaluators produced results")
	})
}

// --- G2 shadow verify gate ---

func TestShadowVerifyGate(t *testing.T) {
	t.Run("no shadow evidence FAILS CLOSED (review blocking item 1)", func(t *testing.T) {
		se := NewShadowEvaluator(DefaultShadowEvaluationConfig())
		lc := &StrategyLifecycle{shadow: se}
		pass, score, reason := shadowVerifyGate{lc}.Check(context.Background(), &mutation.Strategy{}, nil)
		assert.False(t, pass, "zero shadow evidence must reject, not pass through")
		assert.Equal(t, 0.0, score)
		assert.Contains(t, reason, "no shadow comparisons")
		assert.Contains(t, reason, "fail-closed")
	})
	t.Run("win rate below threshold rejects", func(t *testing.T) {
		se := NewShadowEvaluator(ShadowEvaluationConfig{Enabled: true, MinSamples: 2, MinWinRate: 0.9})
		se.RecordResult(0.9, 0.1) // shadow loses
		se.RecordResult(0.9, 0.1) // shadow loses
		lc := &StrategyLifecycle{shadow: se}
		pass, score, reason := shadowVerifyGate{lc}.Check(context.Background(), &mutation.Strategy{}, nil)
		assert.False(t, pass)
		assert.InDelta(t, 0.0, score, 0.0001)
		assert.Contains(t, reason, "below threshold")
	})
	t.Run("win rate above threshold passes", func(t *testing.T) {
		se := NewShadowEvaluator(ShadowEvaluationConfig{Enabled: true, MinSamples: 2, MinWinRate: 0.5})
		se.RecordResult(0.1, 0.9) // shadow wins
		se.RecordResult(0.1, 0.9) // shadow wins
		lc := &StrategyLifecycle{shadow: se}
		pass, score, _ := shadowVerifyGate{lc}.Check(context.Background(), &mutation.Strategy{}, nil)
		assert.True(t, pass)
		assert.InDelta(t, 1.0, score, 0.0001)
	})
}

// --- G2 gate registration wiring ---

func TestNewStrategyLifecycle_RegistersShadowGateFirst(t *testing.T) {
	se := NewShadowEvaluator(ShadowEvaluationConfig{Enabled: true, MinSamples: 2, MinWinRate: 0.5})
	evalGate := &mockGate{name: "eval", pass: true}

	lc := NewStrategyLifecycle(nil, nil, DefaultLifecycleConfig(),
		WithLifecycleShadowEvaluator(se),
		WithLifecycleGates(evalGate),
	)

	// The shadow gate must be prepended so the pipeline order is
	// G2 shadow → G3 eval, and l.shadow must actually be consumed by the
	// promote pipeline (regression: the evaluator used to be assigned but
	// never read).
	require.Len(t, lc.gates, 2)
	assert.Equal(t, "shadow", lc.gates[0].Name())
	assert.Equal(t, "eval", lc.gates[1].Name())

	// Without a shadow evaluator no shadow gate is injected.
	lc2 := NewStrategyLifecycle(nil, nil, DefaultLifecycleConfig(),
		WithLifecycleGates(evalGate),
	)
	require.Len(t, lc2.gates, 1)
	assert.Equal(t, "eval", lc2.gates[0].Name())
}

// --- blacklist pruning ---
//
// Blacklist entries store banUntil (rollBackGen + N); a Submit whose
// generation has reached banUntil prunes the entry and is accepted.

func TestStrategyLifecycle_BlacklistPrunedAcrossGenerations(t *testing.T) {
	lc, asm, _ := newTestLifecycle(t, DefaultLifecycleConfig())
	require.NoError(t, asm.Deploy(context.Background(),
		&mutation.Strategy{ID: "base", Version: 1, Score: 50.0},
	))

	// banUntil=1 and banUntil=2 are already expired at generation 5; the
	// "current" entry (banUntil=7) is still active.
	lc.mu.Lock()
	lc.blacklist["old-1"] = 1
	lc.blacklist["old-2"] = 2
	lc.blacklist["current"] = 7
	lc.mu.Unlock()

	lc.Submit(context.Background(), &mutation.Strategy{ID: "current", Version: 2, Score: 80.0}, 5)

	assert.Equal(t, "base", asm.Current().ID, "active-ban entry still blocks")

	lc.Submit(context.Background(), &mutation.Strategy{ID: "old-1", Version: 3, Score: 80.0}, 5)
	assert.Equal(t, "old-1", asm.Current().ID, "expired entry is pruned and accepted")

	lc.mu.Lock()
	defer lc.mu.Unlock()
	assert.NotContains(t, lc.blacklist, "old-1")
	assert.NotContains(t, lc.blacklist, "old-2")
	assert.Contains(t, lc.blacklist, "current")
}
