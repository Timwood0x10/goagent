package evolution

// gate_eval_warn_test.go locks the skip-visibility contract: every skip emits
// one structured warn naming the missing component and bumps SkippedCount, so
// a misconfigured eval gate is
// operator-visible.

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
	"github.com/Timwood0x10/ares/internal/runtime/eval"
)

func newWarnCapturingGate(t *testing.T, registry *eval.EvaluatorRegistry, runner *eval.AgentTestRunner, suite eval.TestSuite) (*EvalGate, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	g := NewEvalGate(registry, runner, suite, DefaultEvalGateConfig(),
		WithEvalGateLogger(logger))
	return g, &buf
}

func TestEvalGate_SkipEmitsWarnPerMissingComponent(t *testing.T) {
	ctx := context.Background()

	t.Run("missing_registry_warns_and_counts", func(t *testing.T) {
		g, buf := newWarnCapturingGate(t, nil, nil, eval.TestSuite{})
		pass, _, _ := g.Check(ctx, &mutation.Strategy{}, nil)
		assert.True(t, pass, "non-strict keeps the pass-through contract")
		assert.Equal(t, 1, g.SkippedCount())
		assert.Contains(t, buf.String(), "registry")
	})

	t.Run("missing_runner_warns_and_counts", func(t *testing.T) {
		runner, err := eval.NewAgentTestRunner(&fakeExecutor{output: "ok"})
		require.NoError(t, err)
		registry := eval.NewEvaluatorRegistry()
		g, buf := newWarnCapturingGate(t, registry, nil, eval.TestSuite{TestCases: []eval.TestCase{{ID: "c1", Input: "hi"}}})
		_, _ = runner, runner
		pass, _, _ := g.Check(ctx, &mutation.Strategy{}, nil)
		assert.True(t, pass)
		assert.Equal(t, 1, g.SkippedCount())
		assert.Contains(t, buf.String(), "runner")
	})

	t.Run("missing_suite_warns_and_counts", func(t *testing.T) {
		runner, err := eval.NewAgentTestRunner(&fakeExecutor{output: "ok"})
		require.NoError(t, err)
		registry := eval.NewEvaluatorRegistry()
		g, buf := newWarnCapturingGate(t, registry, runner, eval.TestSuite{})
		pass, _, _ := g.Check(ctx, &mutation.Strategy{}, nil)
		assert.True(t, pass)
		assert.Equal(t, 1, g.SkippedCount())
		assert.Contains(t, buf.String(), "test suite")
	})
}
