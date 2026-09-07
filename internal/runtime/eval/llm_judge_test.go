package eval

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/truncate"
)

// mockLLMClient is a mock implementation of LLMClient for testing.
type mockLLMClient struct {
	response  string
	err       error
	callCount int
}

// Generate returns the configured response or error.
func (m *mockLLMClient) Generate(ctx context.Context, prompt string) (string, error) {
	m.callCount++
	if m.err != nil {
		return "", m.err
	}
	return m.response, nil
}

// TestNewLLMJudgeEvaluator_NilClient tests that nil client returns an error.
func TestNewLLMJudgeEvaluator_NilClient(t *testing.T) {
	_, err := NewLLMJudgeEvaluator(nil)
	if !errors.Is(err, ErrNilLLMClient) {
		t.Errorf("expected ErrNilLLMClient, got: %v", err)
	}
}

// TestNewLLMJudgeEvaluator_DefaultOptions tests default constructor with valid client.
func TestNewLLMJudgeEvaluator_DefaultOptions(t *testing.T) {
	client := &mockLLMClient{response: `{"score": 8, "reason": "good response"}`}
	eval, err := NewLLMJudgeEvaluator(client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eval.Name() != "llm_judge" {
		t.Errorf("expected name 'llm_judge', got: %s", eval.Name())
	}
	if eval.scale != ScaleOneToTen {
		t.Errorf("expected default scale ScaleOneToTen, got: %v", eval.scale)
	}
}

// TestNewLLMJudgeEvaluator_WithOptions tests constructor with custom options.
func TestNewLLMJudgeEvaluator_WithOptions(t *testing.T) {
	client := &mockLLMClient{}
	eval, err := NewLLMJudgeEvaluator(client,
		WithScale(ScaleOneToFive),
		WithEnglishPrompt(),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eval.scale != ScaleOneToFive {
		t.Errorf("expected ScaleOneToFive, got: %v", eval.scale)
	}
}

// TestNewLLMJudgeEvaluator_InvalidPromptFails ensures a custom prompt template
// that fails to parse fails construction instead of silently falling back to
// the default prompt (G3 gate integrity: a misconfigured judge must not score
// candidates against an unintended prompt).
func TestNewLLMJudgeEvaluator_InvalidPromptFails(t *testing.T) {
	client := &mockLLMClient{response: `{"score": 8, "reason": "ok"}`}
	if _, err := NewLLMJudgeEvaluator(client, WithPrompt("{{")); err == nil {
		t.Fatal("expected error for invalid prompt template, got nil")
	}
}

// TestEvaluate_NormalScoring tests successful evaluation with mock LLM returning valid JSON.
func TestEvaluate_NormalScoring(t *testing.T) {
	client := &mockLLMClient{
		response: `{"score": 8, "reason": "The answer is accurate and complete."}`,
	}
	eval, _ := NewLLMJudgeEvaluator(client)

	tc := TestCase{
		ID:             "tc-001",
		Name:           "Test basic question",
		Input:          "What is the capital of France?",
		ExpectedOutput: "Paris",
	}
	result := TestResult{
		TestCaseID:   "tc-001",
		ActualOutput: "The capital of France is Paris.",
		Timestamp:    time.Now(),
	}

	scores, err := eval.Evaluate(context.Background(), tc, result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(scores) != 1 {
		t.Fatalf("expected 1 score, got: %d", len(scores))
	}

	if scores[0].Metric != "llm_judge" {
		t.Errorf("expected metric 'llm_judge', got: %s", scores[0].Metric)
	}

	// Score should be normalized to 0-1 range (8/10 = 0.8).
	expectedScore := 0.8
	if scores[0].Score != expectedScore {
		t.Errorf("expected score %.1f, got: %.2f", expectedScore, scores[0].Score)
	}

	if scores[0].Details != "The answer is accurate and complete." {
		t.Errorf("unexpected details: %s", scores[0].Details)
	}
}

// TestEvaluate_MarkdownWrappedJSON tests parsing when LLM wraps JSON in markdown fences.
func TestEvaluate_MarkdownWrappedJSON(t *testing.T) {
	client := &mockLLMClient{
		response: "Here is my evaluation:\n\n```json\n{\"score\": 9, \"reason\": \"Excellent!\"}\n```\n\nDone.",
	}
	eval, _ := NewLLMJudgeEvaluator(client)

	tc := TestCase{ID: "tc-002", Input: "test input"}
	result := TestResult{TestCaseID: "tc-002", ActualOutput: "test output"}

	scores, err := eval.Evaluate(context.Background(), tc, result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(scores) == 0 || scores[0].Score != 0.9 {
		t.Errorf("expected score 0.9, got: %v", scores)
	}
}

// TestEvaluate_InvalidJSONResponse tests that invalid JSON in LLM response returns an error.
func TestEvaluate_InvalidJSONResponse(t *testing.T) {
	client := &mockLLMClient{
		response: "This is not JSON at all, just plain text.",
	}
	eval, _ := NewLLMJudgeEvaluator(client)

	tc := TestCase{ID: "tc-003", Input: "test"}
	result := TestResult{TestCaseID: "tc-003", ActualOutput: "output"}

	_, err := eval.Evaluate(context.Background(), tc, result)
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
	if !errors.Is(err, ErrInvalidJudgeResponse) {
		t.Errorf("expected ErrInvalidJudgeResponse, got: %v", err)
	}
}

// TestEvaluate_EmptyResponse tests that empty LLM response returns an error.
func TestEvaluate_EmptyResponse(t *testing.T) {
	client := &mockLLMClient{response: ""}
	eval, _ := NewLLMJudgeEvaluator(client)

	tc := TestCase{ID: "tc-004", Input: "test"}
	result := TestResult{TestCaseID: "tc-004", ActualOutput: "output"}

	_, err := eval.Evaluate(context.Background(), tc, result)
	if err == nil {
		t.Fatal("expected error for empty response")
	}
	if !errors.Is(err, ErrInvalidJudgeResponse) {
		t.Errorf("expected ErrInvalidJudgeResponse, got: %v", err)
	}
}

// TestEvaluate_LLMError tests that LLM call errors are properly propagated.
func TestEvaluate_LLMError(t *testing.T) {
	llmErr := errors.New("LLM service unavailable")
	client := &mockLLMClient{err: llmErr}
	eval, _ := NewLLMJudgeEvaluator(client)

	tc := TestCase{ID: "tc-005", Input: "test"}
	result := TestResult{TestCaseID: "tc-005", ActualOutput: "output"}

	_, err := eval.Evaluate(context.Background(), tc, result)
	if err == nil {
		t.Fatal("expected error from LLM call")
	}
	if !errors.Is(err, llmErr) {
		t.Errorf("expected original LLM error, got: %v", err)
	}
}

// TestEvaluate_ContextCancellation tests that context cancellation is handled gracefully.
func TestEvaluate_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	// Use a blocking mock that checks context before responding.
	client := &blockingMockClient{}
	eval, _ := NewLLMJudgeEvaluator(client)

	tc := TestCase{ID: "tc-006", Input: "test"}
	result := TestResult{TestCaseID: "tc-006", ActualOutput: "output"}

	_, err := eval.Evaluate(ctx, tc, result)
	if err == nil {
		t.Error("expected context cancellation error")
	}
}

// blockingMockClient is a mock that blocks until context is done, simulating a slow LLM.
type blockingMockClient struct{}

func (m *blockingMockClient) Generate(ctx context.Context, prompt string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// TestEvaluate_ScaleOneToFive tests score normalization with 1-5 scale.
func TestEvaluate_ScaleOneToFive(t *testing.T) {
	client := &mockLLMClient{
		response: `{"score": 4, "reason": "Good on 1-5 scale"}`,
	}
	eval, _ := NewLLMJudgeEvaluator(client, WithScale(ScaleOneToFive))

	tc := TestCase{ID: "tc-007", Input: "test"}
	result := TestResult{TestCaseID: "tc-007", ActualOutput: "output"}

	scores, err := eval.Evaluate(context.Background(), tc, result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expectedScore := 4.0 / 5.0 // 0.8
	if scores[0].Score != expectedScore {
		t.Errorf("expected score %.2f, got: %.2f", expectedScore, scores[0].Score)
	}
}

// TestEvaluate_ScalePassFail tests binary pass/fail normalization.
func TestEvaluate_ScalePassFail(t *testing.T) {
	tests := []struct {
		name     string
		rawScore float64
		want     float64
	}{
		{"pass", 1.0, 1.0},
		{"fail", 0.0, 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, _ := json.Marshal(judgeResponse{Score: tt.rawScore, Reason: tt.name})
			client := &mockLLMClient{response: string(resp)}
			eval, _ := NewLLMJudgeEvaluator(client, WithScale(ScalePassFail))

			tc := TestCase{ID: "tc-pf", Input: "test"}
			result := TestResult{TestCaseID: "tc-pf", ActualOutput: "output"}

			scores, err := eval.Evaluate(context.Background(), tc, result)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if scores[0].Score != tt.want {
				t.Errorf("expected score %.1f, got: %.2f", tt.want, scores[0].Score)
			}
		})
	}
}

// TestEvaluate_ScoreClamping tests that out-of-range scores are clamped to [0, 1].
func TestEvaluate_ScoreClamping(t *testing.T) {
	tests := []struct {
		name     string
		rawScore float64
		want     float64
	}{
		{"over_max", 15.0, 1.0},
		{"negative", -3.0, 0.0},
		{"zero", 0.0, 0.0},
		{"exact_max", 10.0, 1.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, _ := json.Marshal(judgeResponse{Score: tt.rawScore, Reason: "clamping test"})
			client := &mockLLMClient{response: string(resp)}
			eval, _ := NewLLMJudgeEvaluator(client)

			tc := TestCase{ID: "tc-clamp", Input: "test"}
			result := TestResult{TestCaseID: "tc-clamp", ActualOutput: "output"}

			scores, err := eval.Evaluate(context.Background(), tc, result)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if scores[0].Score != tt.want {
				t.Errorf("expected clamped score %.1f, got: %.2f", tt.want, scores[0].Score)
			}
		})
	}
}

// TestEvaluate_CustomPrompt tests custom prompt template option.
func TestEvaluate_CustomPrompt(t *testing.T) {
	customTmpl := `Input: {{.Input}}\nOutput: {{.ActualOutput}}\nRate 0-10 as JSON: {"score": N, "reason": "..."}`
	client := &mockLLMClient{
		response: `{"score": 7, "reason": "custom prompt worked"}`,
	}
	eval, err := NewLLMJudgeEvaluator(client, WithPrompt(customTmpl))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tc := TestCase{ID: "tc-custom", Input: "Hello"}
	result := TestResult{TestCaseID: "tc-custom", ActualOutput: "World"}

	scores, err := eval.Evaluate(context.Background(), tc, result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(scores) != 1 || scores[0].Score != 0.7 {
		t.Errorf("expected score 0.7, got: %v", scores)
	}
}

// TestEvaluate_EnglishPrompt tests English prompt template option.
func TestEvaluate_EnglishPrompt(t *testing.T) {
	client := &mockLLMClient{
		response: `{"score": 6, "reason": "English evaluation done."}`,
	}
	eval, _ := NewLLMJudgeEvaluator(client, WithEnglishPrompt())

	tc := TestCase{ID: "tc-en", Input: "question"}
	result := TestResult{TestCaseID: "tc-en", ActualOutput: "answer"}

	scores, err := eval.Evaluate(context.Background(), tc, result)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if scores[0].Score != 0.6 {
		t.Errorf("expected score 0.6, got: %.2f", scores[0].Score)
	}
}

// TestScaleType_String tests String representation of scale types.
func TestScaleType_String(t *testing.T) {
	tests := []struct {
		scale ScaleType
		want  string
	}{
		{ScaleOneToTen, "1-10"},
		{ScaleOneToFive, "1-5"},
		{ScalePassFail, "pass/fail"},
		{ScaleType(99), "unknown"},
	}

	for _, tt := range tests {
		got := tt.scale.String()
		if got != tt.want {
			t.Errorf("ScaleType(%d).String() = %q, want %q", tt.scale, got, tt.want)
		}
	}
}

// TestExtractJudgeJSON tests JSON extraction from various response formats.
func TestExtractJudgeJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "plain_json",
			input: `{"score": 8, "reason": "ok"}`,
			want:  `{"score": 8, "reason": "ok"}`,
		},
		{
			name:  "markdown_json_fence",
			input: "Here is the result:\n```json\n{\"score\": 9, \"reason\": \"good\"}\n```\n",
			want:  `{"score": 9, "reason": "good"}`,
		},
		{
			name:  "markdown_no_lang",
			input: "Result:\n```\n{\"score\": 7, \"reason\": \"avg\"}\n```\n",
			want:  `{"score": 7, "reason": "avg"}`,
		},
		{
			name:  "json_with_surrounding_text",
			input: "The score is: {\"score\": 5, \"reason\": \"meh\"} and that's it.",
			want:  `{"score": 5, "reason": "meh"}`,
		},
		{
			name:  "no_json",
			input: "Just some plain text with no JSON at all.",
			want:  "",
		},
		{
			name:  "empty_string",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJudgeJSON(tt.input)
			if got != tt.want {
				t.Errorf("extractJudgeJSON() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestIsValidJSON tests the JSON validation helper.
func TestIsValidJSON(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{`{"a": 1}`, true},
		{`[]`, true},
		{`"hello"`, true},
		{`not json`, false},
		{`{invalid`, false},
		{``, false},
	}

	for _, tt := range tests {
		got := isValidJSON(tt.input)
		if got != tt.want {
			t.Errorf("isValidJSON(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

// TestTruncateStr tests string truncation helper.
func TestTruncateStr(t *testing.T) {
	short := "hello"
	if truncate.WithEllipsis(short, 10) != short {
		t.Errorf("short string should not be truncated")
	}

	long := "this is a very long string that exceeds the limit"
	truncated := truncate.WithEllipsis(long, 10)
	if len(truncated) > 10 { // maxLen includes ellipsis
		t.Errorf("truncated string too long: %q (%d chars)", truncated, len(truncated))
	}
	if truncated[len(truncated)-3:] != "..." {
		t.Errorf("truncated string should end with '...': %q", truncated)
	}
}

// TestName_ReturnsCorrectName verifies the Name method returns the expected registry key.
func TestName_ReturnsCorrectName(t *testing.T) {
	client := &mockLLMClient{}
	eval, _ := NewLLMJudgeEvaluator(client)

	if eval.Name() != "llm_judge" {
		t.Errorf("Name() = %q, want 'llm_judge'", eval.Name())
	}
}

// TestEvaluatorInterfaceCompileTimeCheck ensures LLMJudgeEvaluator implements Evaluator.
var _ Evaluator = (*LLMJudgeEvaluator)(nil)
