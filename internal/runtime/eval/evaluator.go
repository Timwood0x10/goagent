// Package eval provides LLM evaluation and benchmarking capabilities,
// including test suite execution, result reporting, and agent performance metrics.
package eval

//nolint: errcheck // best-effort operations: ResponseWriter writes, cleanup Close/Wait, deferred shutdown
import (
	"context"
	"sort"
	"strings"
	"sync"
)

// Evaluator evaluates test results and produces scores.
type Evaluator interface {
	// Evaluate evaluates a test result and returns scores.
	Evaluate(ctx context.Context, testCase TestCase, result TestResult) ([]EvalScore, error)
}

// ExactMatchEvaluator checks if the output exactly matches the expected output.
type ExactMatchEvaluator struct{}

// NewExactMatchEvaluator creates a new exact match evaluator.
func NewExactMatchEvaluator() *ExactMatchEvaluator {
	return &ExactMatchEvaluator{}
}

// Evaluate returns a score based on exact match.
func (e *ExactMatchEvaluator) Evaluate(ctx context.Context, testCase TestCase, result TestResult) ([]EvalScore, error) {
	_ = ctx // reserved for future use (e.g., LLM-based evaluation)
	if testCase.ExpectedOutput == "" {
		return []EvalScore{{Metric: "exact_match", Score: 1.0, Details: "no expected output specified"}}, nil
	}

	score := 0.0
	if result.ActualOutput == testCase.ExpectedOutput {
		score = 1.0
	}

	return []EvalScore{
		{
			Metric:  "exact_match",
			Score:   score,
			Details: "compares actual output to expected output",
		},
	}, nil
}

// KeywordPresenceEvaluator checks if specified keywords are present in the output.
type KeywordPresenceEvaluator struct {
	Keywords []string
}

// NewKeywordPresenceEvaluator creates a new keyword presence evaluator.
func NewKeywordPresenceEvaluator(keywords []string) *KeywordPresenceEvaluator {
	return &KeywordPresenceEvaluator{Keywords: keywords}
}

// Evaluate returns a score based on keyword presence.
func (e *KeywordPresenceEvaluator) Evaluate(ctx context.Context, testCase TestCase, result TestResult) ([]EvalScore, error) {
	if len(e.Keywords) == 0 {
		return []EvalScore{{Metric: "keyword_presence", Score: 1.0, Details: "no keywords specified"}}, nil
	}

	present := 0
	lowerOutput := strings.ToLower(result.ActualOutput)
	for _, keyword := range e.Keywords {
		if strings.Contains(lowerOutput, strings.ToLower(keyword)) {
			present++
		}
	}

	score := float64(present) / float64(len(e.Keywords))

	return []EvalScore{
		{
			Metric:  "keyword_presence",
			Score:   score,
			Details: "checks presence of specified keywords",
		},
	}, nil
}

// ToolUsageEvaluator checks if expected tools were used.
type ToolUsageEvaluator struct{}

// NewToolUsageEvaluator creates a new tool usage evaluator.
func NewToolUsageEvaluator() *ToolUsageEvaluator {
	return &ToolUsageEvaluator{}
}

// Evaluate returns a score based on tool usage.
func (e *ToolUsageEvaluator) Evaluate(ctx context.Context, testCase TestCase, result TestResult) ([]EvalScore, error) {
	if len(testCase.ExpectedTools) == 0 {
		return []EvalScore{{Metric: "tool_usage", Score: 1.0, Details: "no expected tools specified"}}, nil
	}

	used := 0
	for _, expectedTool := range testCase.ExpectedTools {
		for _, usedTool := range result.ToolsUsed {
			if expectedTool == usedTool {
				used++
				break
			}
		}
	}

	score := float64(used) / float64(len(testCase.ExpectedTools))

	return []EvalScore{
		{
			Metric:  "tool_usage",
			Score:   score,
			Details: "checks if expected tools were used",
		},
	}, nil
}

// ErrEmptyName is returned when an empty name is passed to Register.
// Errors are defined in errors.go.

// ErrNilEvaluator is returned when a nil evaluator is passed to Register.
// Errors are defined in errors.go.

// EvaluatorRegistry manages named evaluator instances.
// It provides thread-safe registration and retrieval of evaluators by name,
// enabling dynamic evaluator selection during test execution.
type EvaluatorRegistry struct {
	mu         sync.RWMutex
	evaluators map[string]Evaluator
}

// NewEvaluatorRegistry creates an empty evaluator registry ready for use.
func NewEvaluatorRegistry() *EvaluatorRegistry {
	return &EvaluatorRegistry{
		evaluators: make(map[string]Evaluator),
	}
}

// Register adds an evaluator by name.
//
// Args:
//   - name: unique identifier for the evaluator (must not be empty).
//   - eval: evaluator instance to register (must not be nil).
//
// Returns error if name is empty or eval is nil.
func (r *EvaluatorRegistry) Register(name string, eval Evaluator) error {
	if name == "" {
		return ErrEmptyName
	}
	if eval == nil {
		return ErrNilEvaluator
	}
	r.mu.Lock()
	r.evaluators[name] = eval
	r.mu.Unlock()
	return nil
}

// Get retrieves an evaluator by name.
//
// Args:
//   - name: the registered name of the desired evaluator.
//
// Returns:
//   - Evaluator: the matching evaluator instance, or nil if not found.
//   - bool: true if the evaluator was found, false otherwise.
func (r *EvaluatorRegistry) Get(name string) (Evaluator, bool) {
	r.mu.RLock()
	eval, ok := r.evaluators[name]
	r.mu.RUnlock()
	return eval, ok
}

// Names returns all registered evaluator names in sorted order.
// The returned slice is a copy; modifications do not affect the registry.
func (r *EvaluatorRegistry) Names() []string {
	r.mu.RLock()
	names := make([]string, 0, len(r.evaluators))
	for name := range r.evaluators {
		names = append(names, name)
	}
	r.mu.RUnlock()
	sort.Strings(names)
	return names
}
