package eval

import (
	"context"
	"errors"
	"strings"
	"testing"
)

var errMockJudgeFailure = errors.New("mock LLM failure")

// capturingClient records the last prompt sent to the LLM so tests can assert
// on the rendered prompt text.
type capturingClient struct {
	response   string
	lastPrompt string
}

// Generate implements LLMClient and captures the prompt.
func (m *capturingClient) Generate(_ context.Context, prompt string) (string, error) {
	m.lastPrompt = prompt
	return m.response, nil
}

func TestEvaluateWithDimensions_NormalScoring(t *testing.T) {
	client := &mockLLMClient{response: `{"correctness": 3, "completeness": 2, "efficiency": 2, "safety": 2, "reason": "good"}`}

	e, err := NewLLMJudgeEvaluator(client, WithDimensionAveraging())
	if err != nil {
		t.Fatalf("NewLLMJudgeEvaluator failed: %v", err)
	}

	scores, err := e.Evaluate(context.Background(), TestCase{
		Input:          "test input",
		ExpectedOutput: "expected",
	}, TestResult{
		ActualOutput: "actual output",
	})

	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}
	if len(scores) != 1 {
		t.Fatalf("expected 1 score, got %d", len(scores))
	}
	if scores[0].Metric != "llm_judge_dimension_avg" {
		t.Errorf("metric = %q, want llm_judge_dimension_avg", scores[0].Metric)
	}
	// (3/3 + 2/3 + 2/2 + 2/2) / 4 = (1 + 0.667 + 1 + 1) / 4 = 0.917
	if scores[0].Score != 0.917 {
		t.Errorf("score = %f, want 0.917", scores[0].Score)
	}
}

func TestEvaluateWithDimensions_PartialScores(t *testing.T) {
	client := &mockLLMClient{response: `{"correctness": 1, "completeness": 1, "efficiency": 1, "safety": 2, "reason": "needs improvement"}`}

	e, err := NewLLMJudgeEvaluator(client, WithDimensionAveraging())
	if err != nil {
		t.Fatalf("NewLLMJudgeEvaluator failed: %v", err)
	}

	scores, err := e.Evaluate(context.Background(), TestCase{}, TestResult{})
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}
	// (1/3 + 1/3 + 1/2 + 2/2) / 4 = (0.333 + 0.333 + 0.5 + 1) / 4 = 0.542
	if scores[0].Score != 0.542 {
		t.Errorf("score = %f, want 0.542", scores[0].Score)
	}
}

func TestEvaluateWithDimensions_ZeroScores(t *testing.T) {
	client := &mockLLMClient{response: `{"correctness": 0, "completeness": 0, "efficiency": 0, "safety": 0, "reason": "terrible"}`}

	e, err := NewLLMJudgeEvaluator(client, WithDimensionAveraging())
	if err != nil {
		t.Fatalf("NewLLMJudgeEvaluator failed: %v", err)
	}

	scores, err := e.Evaluate(context.Background(), TestCase{}, TestResult{})
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}
	if scores[0].Score != 0.0 {
		t.Errorf("score = %f, want 0.0", scores[0].Score)
	}
}

func TestEvaluateWithDimensions_Clamping(t *testing.T) {
	client := &mockLLMClient{response: `{"correctness": 5, "completeness": 4, "efficiency": 3, "safety": 3, "reason": "over max"}`}

	e, err := NewLLMJudgeEvaluator(client, WithDimensionAveraging())
	if err != nil {
		t.Fatalf("NewLLMJudgeEvaluator failed: %v", err)
	}

	scores, err := e.Evaluate(context.Background(), TestCase{}, TestResult{})
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}
	if scores[0].Score != 1.0 {
		t.Errorf("score = %f, want 1.0", scores[0].Score)
	}
}

func TestEvaluateWithDimensions_InvalidJSONResponse(t *testing.T) {
	client := &mockLLMClient{response: `not json`}

	e, err := NewLLMJudgeEvaluator(client, WithDimensionAveraging())
	if err != nil {
		t.Fatalf("NewLLMJudgeEvaluator failed: %v", err)
	}

	_, err = e.Evaluate(context.Background(), TestCase{}, TestResult{})
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestEvaluateWithDimensions_LLMError(t *testing.T) {
	client := &mockLLMClient{response: "", err: errMockJudgeFailure}

	e, err := NewLLMJudgeEvaluator(client, WithDimensionAveraging())
	if err != nil {
		t.Fatalf("NewLLMJudgeEvaluator failed: %v", err)
	}

	_, err = e.Evaluate(context.Background(), TestCase{}, TestResult{})
	if err == nil {
		t.Fatal("expected error from LLM failure")
	}
}

func TestEvaluateWithDimensions_ChineseDefaultPrompt(t *testing.T) {
	// All dimensions at max: (3/3 + 3/3 + 2/2 + 2/2) / 4 = 1.0
	client := &mockLLMClient{response: `{"correctness": 3, "completeness": 3, "efficiency": 2, "safety": 2, "reason": "good"}`}

	// Default is Chinese prompt.
	e, err := NewLLMJudgeEvaluator(client, WithDimensionAveraging())
	if err != nil {
		t.Fatalf("NewLLMJudgeEvaluator failed: %v", err)
	}

	scores, err := e.Evaluate(context.Background(), TestCase{
		Input:          "test",
		ExpectedOutput: "expected",
	}, TestResult{ActualOutput: "actual"})
	if err != nil {
		t.Fatalf("Evaluate with CN prompt failed: %v", err)
	}
	if scores[0].Score != 1.0 {
		t.Errorf("score = %f, want 1.0", scores[0].Score)
	}
}

func TestEvaluateWithDimensions_EnglishPrompt(t *testing.T) {
	client := &mockLLMClient{response: `{"correctness": 3, "completeness": 3, "efficiency": 2, "safety": 2, "reason": "excellent"}`}

	e, err := NewLLMJudgeEvaluator(client, WithEnglishPrompt(), WithDimensionAveraging())
	if err != nil {
		t.Fatalf("NewLLMJudgeEvaluator failed: %v", err)
	}

	scores, err := e.Evaluate(context.Background(), TestCase{
		Input:          "test",
		ExpectedOutput: "expected",
	}, TestResult{ActualOutput: "actual"})
	if err != nil {
		t.Fatalf("Evaluate with EN prompt failed: %v", err)
	}
	if scores[0].Score != 1.0 {
		t.Errorf("score = %f, want 1.0", scores[0].Score)
	}
}

// TestEvaluateWithDimensions_LiteralNotCascaded is a regression test: test-case
// content that itself contains "{{.Field}}" markers must be rendered verbatim
// instead of being re-substituted by subsequent replacements (the old
// strings.ReplaceAll pipeline cascaded: an Input containing {{.ExpectedOutput}}
// was rewritten after the ExpectedOutput replacement).
func TestEvaluateWithDimensions_LiteralNotCascaded(t *testing.T) {
	client := &capturingClient{response: `{"correctness": 3, "completeness": 3, "efficiency": 2, "safety": 2, "reason": "ok"}`}

	e, err := NewLLMJudgeEvaluator(client, WithDimensionAveraging())
	if err != nil {
		t.Fatalf("NewLLMJudgeEvaluator failed: %v", err)
	}

	verbatim := "{{.ActualOutput}} and {{.ExpectedOutput}}"
	tc := TestCase{
		Input:          verbatim,
		ExpectedOutput: "EXPECTED-VALUE",
	}
	result := TestResult{ActualOutput: "ACTUAL-VALUE"}

	scores, err := e.Evaluate(context.Background(), tc, result)
	if err != nil {
		t.Fatalf("Evaluate failed: %v", err)
	}
	if len(scores) != 1 || scores[0].Score != 1.0 {
		t.Errorf("score = %v, want 1.0", scores)
	}
	if !strings.Contains(client.lastPrompt, verbatim) {
		t.Errorf("rendered prompt must keep %q verbatim, got: %s", verbatim, client.lastPrompt)
	}
}
