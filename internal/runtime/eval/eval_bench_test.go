package eval

import (
	"context"
	"testing"
	"time"
)

// BenchmarkEvaluator benchmarks the evaluation process.
func BenchmarkExactMatchEvaluator_Evaluate(b *testing.B) {
	evaluator := NewExactMatchEvaluator()
	ctx := context.Background()

	testCase := TestCase{
		ID:             "bench-1",
		ExpectedOutput: "expected output",
	}

	result := TestResult{
		ActualOutput: "expected output",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = evaluator.Evaluate(ctx, testCase, result)
	}
}

func BenchmarkToolUsageEvaluator_Evaluate(b *testing.B) {
	evaluator := NewToolUsageEvaluator()
	ctx := context.Background()

	testCase := TestCase{
		ID:            "bench-1",
		ExpectedTools: []string{"tool1", "tool2", "tool3", "tool4", "tool5"},
	}

	result := TestResult{
		ToolsUsed: []string{"tool1", "tool2", "tool3"},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = evaluator.Evaluate(ctx, testCase, result)
	}
}

func BenchmarkAgentTestRunner_RunSingle(b *testing.B) {
	executor := &MockExecutor{
		output:    "benchmark output",
		toolsUsed: []string{"tool1", "tool2"},
		tokens:    100,
	}

	runner, err := NewAgentTestRunner(executor)
	if err != nil {
		b.Fatalf("failed to create runner: %v", err)
	}
	ctx := context.Background()

	testCase := TestCase{
		ID:      "bench-1",
		Input:   "benchmark input",
		Timeout: Duration(5 * time.Second),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = runner.RunSingle(ctx, testCase)
	}
}

func BenchmarkReportGenerator_GenerateMarkdown(b *testing.B) {
	generator := NewReportGenerator()

	suite := TestSuite{
		Name:        "Benchmark Suite",
		Description: "Benchmark description",
		TestCases:   make([]TestCase, 10),
	}

	for i := range suite.TestCases {
		suite.TestCases[i] = TestCase{
			ID:   string(rune('A' + i)),
			Name: "Test",
		}
	}

	results := make([]TestResult, 10)
	for i := range results {
		results[i] = TestResult{
			TestCaseID:   string(rune('A' + i)),
			ActualOutput: "output",
			Duration:     100 * time.Millisecond,
			TokensUsed:   50,
			Timestamp:    time.Now(),
			Metrics:      map[string]float64{"accuracy": 0.9},
		}
	}

	scores := make([][]EvalScore, 10)
	for i := range scores {
		scores[i] = []EvalScore{{Metric: "accuracy", Score: 0.9}}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = generator.GenerateMarkdown(suite, results, scores)
	}
}

func BenchmarkLoader_Load(b *testing.B) {
	loader := NewLoader()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = loader.Load("../../../test/eval/basic.yaml")
	}
}
