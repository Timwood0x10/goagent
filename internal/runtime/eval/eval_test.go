package eval

import (
	"context"
	"testing"
	"time"
)

func TestExactMatchEvaluator_Evaluate(t *testing.T) {
	evaluator := NewExactMatchEvaluator()
	ctx := context.Background()

	tests := []struct {
		name        string
		testCase    TestCase
		result      TestResult
		expectScore float64
	}{
		{
			name:        "exact match",
			testCase:    TestCase{ExpectedOutput: "hello"},
			result:      TestResult{ActualOutput: "hello"},
			expectScore: 1.0,
		},
		{
			name:        "no match",
			testCase:    TestCase{ExpectedOutput: "hello"},
			result:      TestResult{ActualOutput: "world"},
			expectScore: 0.0,
		},
		{
			name:        "no expected output",
			testCase:    TestCase{ExpectedOutput: ""},
			result:      TestResult{ActualOutput: "anything"},
			expectScore: 1.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scores, err := evaluator.Evaluate(ctx, tt.testCase, tt.result)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(scores) == 0 {
				t.Fatal("expected at least one score")
			}

			if scores[0].Score != tt.expectScore {
				t.Errorf("expected score %f, got %f", tt.expectScore, scores[0].Score)
			}
		})
	}
}

func TestToolUsageEvaluator_Evaluate(t *testing.T) {
	evaluator := NewToolUsageEvaluator()
	ctx := context.Background()

	tests := []struct {
		name        string
		testCase    TestCase
		result      TestResult
		expectScore float64
	}{
		{
			name:        "all tools used",
			testCase:    TestCase{ExpectedTools: []string{"tool1", "tool2"}},
			result:      TestResult{ToolsUsed: []string{"tool1", "tool2"}},
			expectScore: 1.0,
		},
		{
			name:        "partial tools used",
			testCase:    TestCase{ExpectedTools: []string{"tool1", "tool2", "tool3"}},
			result:      TestResult{ToolsUsed: []string{"tool1"}},
			expectScore: 1.0 / 3.0,
		},
		{
			name:        "no expected tools",
			testCase:    TestCase{ExpectedTools: []string{}},
			result:      TestResult{ToolsUsed: []string{"tool1"}},
			expectScore: 1.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scores, err := evaluator.Evaluate(ctx, tt.testCase, tt.result)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(scores) == 0 {
				t.Fatal("expected at least one score")
			}

			if scores[0].Score != tt.expectScore {
				t.Errorf("expected score %f, got %f", tt.expectScore, scores[0].Score)
			}
		})
	}
}

func TestAgentTestRunner_RunSingle(t *testing.T) {
	// Mock executor
	executor := &MockExecutor{
		output:    "test output",
		toolsUsed: []string{"tool1"},
		tokens:    100,
	}

	runner, err := NewAgentTestRunner(executor)
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}
	ctx := context.Background()

	testCase := TestCase{
		ID:      "test-1",
		Input:   "test input",
		Timeout: Duration(5 * time.Second),
	}

	result, err := runner.RunSingle(ctx, testCase)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.TestCaseID != testCase.ID {
		t.Errorf("test case ID mismatch")
	}

	if result.ActualOutput != "test output" {
		t.Errorf("output mismatch")
	}

	if result.TokensUsed != 100 {
		t.Errorf("tokens mismatch")
	}
}

func TestLoader_Load(t *testing.T) {
	loader := NewLoader()

	// Test loading valid file
	suite, err := loader.Load("../../../test/eval/basic.yaml")
	if err != nil {
		t.Fatalf("failed to load: %v", err)
	}

	if suite.Name == "" {
		t.Error("expected non-empty suite name")
	}

	if len(suite.TestCases) == 0 {
		t.Error("expected at least one test case")
	}

	// Test path traversal protection
	_, err = loader.Load("../../../etc/passwd")
	if err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestReportGenerator_GenerateMarkdown(t *testing.T) {
	generator := NewReportGenerator()

	suite := TestSuite{
		Name:        "Test Suite",
		Description: "Test description",
		TestCases: []TestCase{
			{ID: "tc1", Name: "Test 1"},
		},
	}

	results := []TestResult{
		{
			TestCaseID:   "tc1",
			ActualOutput: "output",
			Duration:     100 * time.Millisecond,
			TokensUsed:   50,
			Timestamp:    time.Now(),
			Metrics:      map[string]float64{"accuracy": 0.9},
		},
	}

	scores := [][]EvalScore{
		{{Metric: "accuracy", Score: 0.9}},
	}

	markdown, err := generator.GenerateMarkdown(suite, results, scores)
	if err != nil {
		t.Fatalf("failed to generate: %v", err)
	}

	if markdown == "" {
		t.Error("expected non-empty markdown")
	}

	// Check for key sections
	if !contains(markdown, "# Evaluation Report") {
		t.Error("missing report header")
	}

	if !contains(markdown, "Test Suite") {
		t.Error("missing suite name")
	}
}

func TestRunEvaluation(t *testing.T) {
	loader := NewLoader()
	executor := &MockExecutor{output: "test", toolsUsed: []string{}, tokens: 10}
	runner, err := NewAgentTestRunner(executor)
	if err != nil {
		t.Fatalf("failed to create runner: %v", err)
	}
	evaluator := NewExactMatchEvaluator()

	ctx := context.Background()

	results, scores, err := RunEvaluation(ctx, loader, runner, evaluator, "../../../test/eval/basic.yaml")
	if err != nil {
		t.Fatalf("evaluation failed: %v", err)
	}

	if len(results) == 0 {
		t.Error("expected at least one result")
	}

	if len(scores) != len(results) {
		t.Error("scores count should match results count")
	}
}

// MockExecutor implements AgentExecutor for testing.
type MockExecutor struct {
	output    string
	toolsUsed []string
	tokens    int
	err       error
}

func (e *MockExecutor) Execute(ctx context.Context, input string) (string, []string, int, error) {
	return e.output, e.toolsUsed, e.tokens, e.err
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || contains(s[1:], substr)))
}
