// regression_gate_wiring.go builds the PRODUCTION arena regression gate from
// the evolution gate config: the eval LLM client drives an
// LLMArenaScorer, and the eval suite file supplies the preserved-case list
// (the same suite the G3 eval gate scores in absolute terms — the regression
// gate re-runs it as a candidate-vs-active A/B).
//
// Configuration contract (evolution.gates.*):
//   - regression_enabled unset/false → NO gate (opt-in: each Check costs
//     2×regression_runs LLM scoring rounds);
//   - regression_enabled true but no eval_suite / no eval LLM client →
//     Bootstrap FAILS: an explicitly armed gate must not silently skip
//     (fail closed, same posture as eval_strict).
package ares_bootstrap

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Timwood0x10/ares/internal/ares_config"
	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
	evoService "github.com/Timwood0x10/ares/internal/runtime/ares_evolution/service"
	"github.com/Timwood0x10/ares/internal/runtime/eval"
)

// errRegressionGateNotConfigured signals the INTENTIONAL absence of the
// regression gate (regression_enabled=false). The caller tolerates exactly
// this error and skips wiring; any other error from buildRegressionGate
// means an ARMED gate is broken and fails bootstrap.
var errRegressionGateNotConfigured = errors.New("bootstrap: arena regression gate not configured")

// buildRegressionGate constructs the arena regression gate. enabled=false
// returns (nil, errRegressionGateNotConfigured) — honest absence, the caller
// skips wiring. enabled=true with missing prerequisites (suite path, LLM
// client, unloadable or empty suite) returns a real error — fail closed.
func buildRegressionGate(
	enabled bool,
	client eval.LLMClient,
	gates ares_config.EvolutionGateConfig,
) (*evolution.ArenaRegressionGate, error) {
	if !enabled {
		return nil, errRegressionGateNotConfigured
	}
	if strings.TrimSpace(gates.EvalSuite) == "" {
		return nil, fmt.Errorf("bootstrap: regression gate enabled (evolution.gates.regression_enabled) but no eval_suite is configured — the gate needs the preserved-case suite")
	}
	if client == nil {
		return nil, fmt.Errorf("bootstrap: regression gate enabled (evolution.gates.regression_enabled) but no eval LLM client is wired")
	}
	suite, err := eval.NewLoader().Load(gates.EvalSuite)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: load regression suite %q: %w", gates.EvalSuite, err)
	}
	cases := make([]any, 0, len(suite.TestCases))
	for _, tc := range suite.TestCases {
		if strings.TrimSpace(tc.Input) != "" {
			cases = append(cases, tc.Input)
		}
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("bootstrap: regression suite %q contains no usable cases (empty inputs)", gates.EvalSuite)
	}
	scorer, err := evoService.NewLLMArenaScorer(evoService.LLMArenaScorerConfig{Client: client})
	if err != nil {
		return nil, fmt.Errorf("bootstrap: build arena scorer: %w", err)
	}
	gate, err := evolution.NewArenaRegressionGate(scorer, cases, evolution.ArenaRegressionGateConfig{
		Runs:       gates.RegressionRuns,
		MinWinRate: gates.RegressionMinWinRate,
	})
	if err != nil {
		return nil, fmt.Errorf("bootstrap: build arena regression gate: %w", err)
	}
	return gate, nil
}
