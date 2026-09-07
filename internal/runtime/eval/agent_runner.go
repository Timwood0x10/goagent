package eval

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrNilExecutor is returned when a nil executor is provided.
// Errors are defined in errors.go.

// AgentTestRunner runs test cases against an agent.
// It optionally holds an EvaluatorRegistry for dynamic evaluator selection.
type AgentTestRunner struct {
	executor AgentExecutor
	registry *EvaluatorRegistry
}

// NewAgentTestRunner creates a new agent test runner.
//
// Args:
//   - executor: the agent executor used to run test cases (must not be nil).
//
// Returns:
//   - *AgentTestRunner: configured runner instance.
//   - error: ErrNilExecutor if executor is nil.
func NewAgentTestRunner(executor AgentExecutor) (*AgentTestRunner, error) {
	if executor == nil {
		return nil, ErrNilExecutor
	}
	return &AgentTestRunner{executor: executor}, nil
}

// SetRegistry sets an optional evaluator registry on the runner.
// When set, RunAndEvaluate can resolve evaluators by name from the registry.
func (r *AgentTestRunner) SetRegistry(registry *EvaluatorRegistry) {
	r.registry = registry
}

// RunSuite runs all test cases in a suite.
func (r *AgentTestRunner) RunSuite(ctx context.Context, suite TestSuite) ([]TestResult, error) {
	results := make([]TestResult, 0, len(suite.TestCases))
	var errs []error

	for _, tc := range suite.TestCases {
		result, err := r.RunSingle(ctx, tc)
		if err != nil {
			// Record the failure and continue with the remaining cases so a
			// single broken case does not discard already-run and pending
			// results (consistent with ConcurrentRunner.RunSuite).
			errs = append(errs, fmt.Errorf("case %q: %w", tc.ID, err))
			continue
		}
		results = append(results, result)
	}

	if len(errs) > 0 {
		return results, errors.Join(errs...)
	}
	return results, nil
}

// RunSingle runs a single test case.
func (r *AgentTestRunner) RunSingle(ctx context.Context, testCase TestCase) (TestResult, error) {
	result := TestResult{
		TestCaseID: testCase.ID,
		Timestamp:  time.Now(),
		Metrics:    make(map[string]float64),
	}

	// Create timeout context
	timeout := testCase.Timeout.ToDuration()
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Execute the agent
	start := time.Now()
	output, toolsUsed, tokensUsed, err := r.executor.Execute(ctx, testCase.Input)
	result.Duration = time.Since(start)

	result.ActualOutput = output
	result.ToolsUsed = toolsUsed
	result.TokensUsed = tokensUsed

	if err != nil {
		result.Error = err.Error()
	}

	return result, nil
}

// RunAndEvaluate runs a test suite and evaluates results using an evaluator
// resolved from the optional registry.
//
// Args:
//   - ctx: context for cancellation and timeout control.
//   - suite: the test suite to run and evaluate.
//   - evaluatorName: name of the evaluator to look up in the registry.
//
// Returns:
//   - []TestResult: execution results for each test case.
//   - [][]EvalScore: evaluation scores for each result (parallel to results).
//   - error: execution error, or ErrNilRegistry/ErrEvaluatorNotFound if registry lookup fails.
func (r *AgentTestRunner) RunAndEvaluate(ctx context.Context, suite TestSuite, evaluatorName string) ([]TestResult, [][]EvalScore, error) {
	results, err := r.RunSuite(ctx, suite)
	if err != nil {
		return nil, nil, fmt.Errorf("run suite: %w", err)
	}

	if r.registry == nil {
		return nil, nil, ErrNilRegistry
	}

	eval, ok := r.registry.Get(evaluatorName)
	if !ok {
		return nil, nil, fmt.Errorf("%w: name=%q", ErrEvaluatorNotFound, evaluatorName)
	}

	scores, err := evaluateResults(ctx, suite, results, eval)
	if err != nil {
		return nil, nil, err
	}
	return results, scores, nil
}

// evaluateResults evaluates test results using the given evaluator and returns scores.
func evaluateResults(ctx context.Context, suite TestSuite, results []TestResult, eval Evaluator) ([][]EvalScore, error) {
	tcMap := make(map[string]TestCase, len(suite.TestCases))
	for _, tc := range suite.TestCases {
		tcMap[tc.ID] = tc
	}

	scores := make([][]EvalScore, len(results))
	for i, result := range results {
		tc, ok := tcMap[result.TestCaseID]
		if !ok {
			return nil, fmt.Errorf("evaluate: no test case for result %q", result.TestCaseID)
		}
		evalScores, err := eval.Evaluate(ctx, tc, result)
		if err != nil {
			return nil, fmt.Errorf("evaluate result %q: %w", result.TestCaseID, err)
		}
		scores[i] = evalScores
	}
	return scores, nil
}
