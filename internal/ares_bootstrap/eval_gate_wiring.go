// eval_gate_wiring.go builds the PRODUCTION G3 eval-suite gate: an
// AgentTestRunner whose executor runs regression test cases THROUGH the
// candidate strategy (its prompt template), scored by the registered LLM
// evaluators (llm_judge). This closes review item B.2 — the gate used to be
// constructed with a nil runner and an empty suite, making it a permanent
// pass-through while the docs claimed a four-gate pipeline.
//
// Configuration contract (evolution.gates.eval_suite, a file path;
// evolution.gates.eval_strict, E3):
//   - unset → NO G3 gate is built (honest absence, not a fake pass-through);
//     the pipeline degrades to G2 shadow (+ G1 guardrails at the scheduler).
//     With eval_strict the absence itself FAILS bootstrap — an unwired
//     gate must not silently pass every candidate.
//   - set but unloadable → Bootstrap FAILS. An operator who configured a
//     verification gate must not get a silently weaker pipeline from a typo.
package ares_bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
	"github.com/Timwood0x10/ares/internal/runtime/eval"
)

// errEvalGateNotConfigured signals the INTENTIONAL absence of the G3 gate
// (no registry/client/suite configured and strict mode off). Bootstrap
// tolerates it and runs the pipeline without G3; any other error from
// buildEvalGate means a CONFIGURED gate is broken and fails bootstrap.
var errEvalGateNotConfigured = errors.New("bootstrap: G3 eval gate not configured")

// llmEvalExecutor adapts an eval.LLMClient into an eval.AgentExecutor
// for the G3 gate. The candidate strategy's PromptTemplate is prepended to
// every test-case input, so candidate and active strategies produce genuinely
// different outputs and the judge scores discriminate between them.
//
// Sampling params (temperature/max_tokens) are NOT applied: the bare
// LLMClient.Generate interface carries no sampling options. The prompt
// template is the artifact the GA actually mutates, so it is the honest
// discriminator here. Thread-safe: lifecycle Submit can run concurrently.
type llmEvalExecutor struct {
	client eval.LLMClient

	mu             sync.Mutex
	promptTemplate string
}

func newLLMEvalExecutor(client eval.LLMClient) *llmEvalExecutor {
	return &llmEvalExecutor{client: client}
}

// setCandidate records the candidate whose prompt template the next Execute
// calls run through. Invoked by the gate's beforeRun hook per Submit.
func (e *llmEvalExecutor) setCandidate(cand *mutation.Strategy) {
	if cand == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.promptTemplate = cand.PromptTemplate
}

// Execute implements eval.AgentExecutor.
func (e *llmEvalExecutor) Execute(ctx context.Context, input string) (string, []string, int, error) {
	e.mu.Lock()
	tmpl := e.promptTemplate
	e.mu.Unlock()

	prompt := input
	if tmpl != "" {
		prompt = strings.TrimSpace(tmpl) + "\n\n" + input
	}
	out, err := e.client.Generate(ctx, prompt)
	if err != nil {
		return "", nil, 0, fmt.Errorf("eval executor generate: %w", err)
	}
	return out, nil, 0, nil
}

// buildEvalGate constructs the G3 verify gate from the evaluator registry,
// the eval LLM client, and the YAML-configured suite path.
//
// Absent inputs (no registry/client/suite) mean the gate is intentionally
// not configured: with strict=false it returns errEvalGateNotConfigured and
// bootstrap runs without G3 (the documented degradation, not a fake
// pass-through); with strict=true (evolution.gates.eval_strict, E3) the
// absence itself fails bootstrap — an unwired gate must not silently pass
// every candidate. A CONFIGURED suite that cannot be loaded always fails
// (fail closed — see the package comment).
func buildEvalGate(
	registry *eval.EvaluatorRegistry,
	client eval.LLMClient,
	suitePath string,
	minScore float64,
	strict bool,
) (*evolution.EvalGate, error) {
	if registry == nil || client == nil || strings.TrimSpace(suitePath) == "" {
		if strict {
			// Deliberately NOT wrapping the sentinel: the caller only
			// tolerates errEvalGateNotConfigured, so strict absence must
			// surface as a hard error and fail bootstrap.
			return nil, fmt.Errorf("bootstrap: G3 eval gate is strict (evolution.gates.eval_strict) but not configured (registry/client/suite missing)")
		}
		return nil, errEvalGateNotConfigured
	}
	suite, err := eval.NewLoader().Load(suitePath)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: load eval suite %q (evolution.gates.eval_suite): %w", suitePath, err)
	}
	if len(suite.TestCases) == 0 {
		return nil, fmt.Errorf("bootstrap: eval suite %q contains no test cases", suitePath)
	}

	exec := newLLMEvalExecutor(client)
	runner, err := eval.NewAgentTestRunner(exec)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: create eval runner: %w", err)
	}
	runner.SetRegistry(registry)

	gateCfg := evolution.DefaultEvalGateConfig()
	if minScore > 0 {
		gateCfg.MinScore = minScore
	}
	// StrictMode also governs the gate's own Check: once built, a runtime
	// loss of infrastructure rejects instead of passing (E3).
	gateCfg.StrictMode = strict
	gate := evolution.NewEvalGate(registry, runner, *suite, gateCfg,
		evolution.WithEvalGateBeforeRun(exec.setCandidate),
	)
	return gate, nil
}
