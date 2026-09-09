package ares_bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_config"
)

// The regression-gate wiring contract: opt-in absence (nil, nil), fail-closed
// on armed-but-incomplete configuration, and a real gate from a loadable
// suite + LLM client.

// stubEvalClient satisfies eval.LLMClient (and structurally
// evoService.LLMClient). Its output is irrelevant — the wiring tests never
// run a Check.
type stubEvalClient struct{}

func (stubEvalClient) Generate(context.Context, string) (string, error) {
	return "0.5", nil
}

// writeSuiteFile writes a minimal eval.TestSuite YAML and returns its path.
func writeSuiteFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "suite.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

const suiteYAML = `
name: preserved
description: preserved-case suite
test_cases:
  - id: c1
    name: case one
    input: "Summarize the quarterly report."
  - id: c2
    name: case two
    input: "Refactor the parser loop."
`

func TestBuildRegressionGateDisabled(t *testing.T) {
	gate, err := buildRegressionGate(false, stubEvalClient{}, ares_config.EvolutionGateConfig{})
	require.ErrorIs(t, err, errRegressionGateNotConfigured, "disabled = the intentional-absence sentinel")
	assert.Nil(t, gate, "disabled = honest absence, not a pass-through")
}

func TestBuildRegressionGateFailClosed(t *testing.T) {
	t.Run("enabled_without_suite", func(t *testing.T) {
		_, err := buildRegressionGate(true, stubEvalClient{}, ares_config.EvolutionGateConfig{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no eval_suite")
	})

	t.Run("enabled_without_llm_client", func(t *testing.T) {
		gates := ares_config.EvolutionGateConfig{RegressionEnabled: true, EvalSuite: "whatever.yaml"}
		_, err := buildRegressionGate(true, nil, gates)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no eval LLM client")
	})

	t.Run("enabled_with_unloadable_suite", func(t *testing.T) {
		gates := ares_config.EvolutionGateConfig{
			RegressionEnabled: true,
			EvalSuite:         filepath.Join(t.TempDir(), "missing.yaml"),
		}
		_, err := buildRegressionGate(true, stubEvalClient{}, gates)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "load regression suite")
	})

	t.Run("enabled_with_empty_input_suite", func(t *testing.T) {
		path := writeSuiteFile(t, `
name: empty
test_cases:
  - id: c1
    input: "   "
`)
		gates := ares_config.EvolutionGateConfig{RegressionEnabled: true, EvalSuite: path}
		_, err := buildRegressionGate(true, stubEvalClient{}, gates)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no usable cases")
	})
}

func TestBuildRegressionGateBuildsFromSuite(t *testing.T) {
	path := writeSuiteFile(t, suiteYAML)
	gates := ares_config.EvolutionGateConfig{
		RegressionEnabled:    true,
		EvalSuite:            path,
		RegressionRuns:       3,
		RegressionMinWinRate: 0.6,
	}
	gate, err := buildRegressionGate(true, stubEvalClient{}, gates)
	require.NoError(t, err)
	require.NotNil(t, gate)
	assert.Equal(t, "arena_regression", gate.Name())
	// Config values flow through (non-zero) / defaults fill in.
	assert.Equal(t, 3, gate.Config().Runs)
	assert.Equal(t, 0.6, gate.Config().MinWinRate)
	assert.Equal(t, 0.05, gate.Config().Confidence, "confidence keeps its default")
}
