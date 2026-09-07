package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/truncate"
)

// ErrNilLLMClient is returned when a nil LLM client is provided to the judge evaluator.
// Errors are defined in errors.go.

// ErrInvalidJudgeResponse is returned when the LLM judge response cannot be parsed.
// Errors are defined in errors.go.

// LLMClient defines the interface for calling an LLM to generate text.
// This abstraction allows the evaluator to work with any LLM implementation.
type LLMClient interface {
	// Generate sends a prompt to the LLM and returns the generated text.
	Generate(ctx context.Context, prompt string) (string, error)
}

// ScaleType defines the scoring scale used by the LLM judge evaluator.
type ScaleType int

const (
	// ScaleOneToTen represents a 1-10 scoring scale.
	ScaleOneToTen ScaleType = iota + 1

	// ScaleOneToFive represents a 1-5 scoring scale.
	ScaleOneToFive

	// ScalePassFail represents a binary pass/fail scoring scale.
	ScalePassFail
)

// String returns a human-readable representation of the scale type.
func (s ScaleType) String() string {
	switch s {
	case ScaleOneToTen:
		return "1-10"
	case ScaleOneToFive:
		return "1-5"
	case ScalePassFail:
		return "pass/fail"
	default:
		return "unknown"
	}
}

// maxScore returns the maximum possible score for the given scale type.
func (s ScaleType) maxScore() float64 {
	switch s {
	case ScaleOneToTen:
		return 10.0
	case ScaleOneToFive:
		return 5.0
	case ScalePassFail:
		return 1.0
	default:
		return 10.0
	}
}

// judgeResponse represents the structured JSON response from the LLM judge.
type judgeResponse struct {
	Score  float64 `json:"score"`
	Reason string  `json:"reason"`
}

// judgeLanguage selects the language of the dimension-aware prompt used when
// WithDimensionAveraging is enabled. It is set by WithChinesePrompt and
// WithEnglishPrompt so the scalar and dimension prompts always agree on
// language, even when a custom scalar template is configured.
type judgeLanguage int

const (
	judgeLangCN judgeLanguage = iota
	judgeLangEN
)

// LLMJudgeEvaluator uses an LLM to evaluate agent output quality on open-ended tasks.
// It sends the test case input, expected output, and actual agent response to an LLM,
// which then returns a structured score and reasoning.
type LLMJudgeEvaluator struct {
	client                LLMClient             // LLM client interface for calling the judge model
	promptTmpl            *template.Template    // compiled scalar evaluation prompt template
	lang                  judgeLanguage         // dimension-prompt language when dimension averaging is enabled
	scale                 ScaleType             // scoring scale configuration
	useDimensionAveraging bool                  // when true, score = average of per-dimension scores (lower variance)
	dimensionBridge       *DimensionJudgeBridge // set by WithEvidenceStore; persists dimension judgments as evidence
	role                  string                // agent role being evaluated, carried into evidence metadata
	initErr               error                 // template parse failure captured by an option; fails construction
}

// LLMJudgeOption is a functional option for configuring LLMJudgeEvaluator.
type LLMJudgeOption func(*LLMJudgeEvaluator)

// WithPrompt sets a custom evaluation prompt template string.
// The template may use {{.Input}}, {{.ExpectedOutput}}, and {{.ActualOutput}} as variables.
// A template that fails to parse makes NewLLMJudgeEvaluator return an error:
// silently falling back to the default prompt would score candidates against
// a different prompt than the operator configured (eval gate integrity).
func WithPrompt(tmpl string) LLMJudgeOption {
	return func(e *LLMJudgeEvaluator) {
		parsed, err := template.New("judge").Parse(tmpl)
		if err != nil {
			e.initErr = fmt.Errorf("invalid judge prompt template: %w", err)
			return
		}
		e.promptTmpl = parsed
	}
}

// WithScale sets the scoring scale type for the evaluator.
func WithScale(scale ScaleType) LLMJudgeOption {
	return func(e *LLMJudgeEvaluator) {
		e.scale = scale
	}
}

// WithChinesePrompt configures the evaluator to use the Chinese default prompt.
// A parse failure makes NewLLMJudgeEvaluator fail fast (no silent fallback).
func WithChinesePrompt() LLMJudgeOption {
	return func(e *LLMJudgeEvaluator) {
		parsed, err := template.New("judge_cn").Parse(DefaultJudgePromptCN)
		if err != nil {
			e.initErr = fmt.Errorf("invalid Chinese judge prompt template: %w", err)
			return
		}
		e.promptTmpl = parsed
		e.lang = judgeLangCN
	}
}

// WithEnglishPrompt configures the evaluator to use the English default prompt.
// A parse failure makes NewLLMJudgeEvaluator fail fast (no silent fallback).
func WithEnglishPrompt() LLMJudgeOption {
	return func(e *LLMJudgeEvaluator) {
		parsed, err := template.New("judge_en").Parse(DefaultJudgePromptEN)
		if err != nil {
			e.initErr = fmt.Errorf("invalid English judge prompt template: %w", err)
			return
		}
		e.promptTmpl = parsed
		e.lang = judgeLangEN
	}
}

// WithDimensionAveraging enables per-dimension scoring and averaging.
// Instead of asking the LLM for a single total score, it asks for scores on
// four independent dimensions (correctness, completeness, efficiency, safety)
// and returns the average. This reduces variance by averaging multiple
// judgments per call.
//
// When enabled, Evaluate renders the language-appropriate dimension prompt
// (Chinese by default; WithChinesePrompt/WithEnglishPrompt select the
// language) and the final score is the mean of all normalized dimension
// scores. A custom WithPrompt template only affects the scalar path and is
// ignored under dimension averaging.
func WithDimensionAveraging() LLMJudgeOption {
	return func(e *LLMJudgeEvaluator) {
		e.useDimensionAveraging = true
	}
}

// WithEvidenceStore wires the universal evidence store so that dimension-avg
// judgments are persisted as KindDimensionEval Evidence through the bridge.
// This makes dimension_judge diagnoses queryable by the evolution Diagnoser in
// production, not just in tests. When unset, dimension judgments are not
// persisted (scalar-only compatibility path).
func WithEvidenceStore(store evidence.Store) LLMJudgeOption {
	return func(e *LLMJudgeEvaluator) {
		e.dimensionBridge = NewDimensionJudgeBridge(store)
	}
}

// WithRole records the agent role whose execution is being evaluated; it is
// carried into the evidence metadata (role=...) so failure clusters can be
// scoped per role.
func WithRole(role string) LLMJudgeOption {
	return func(e *LLMJudgeEvaluator) {
		e.role = role
	}
}

// NewLLMJudgeEvaluator creates a new LLM-based evaluator with the given client and options.
// By default, it uses the Chinese prompt template and a 1-10 scoring scale.
//
// Args:
//   - ctx: unused (reserved for future use).
//   - client: LLM client implementation for calling the judge model.
//   - opts: optional configuration functions (WithPrompt, WithScale, etc.).
//
// Returns:
//   - *LLMJudgeEvaluator: configured evaluator instance.
//   - error: ErrNilLLMClient if client is nil, or template parsing error.
func NewLLMJudgeEvaluator(client LLMClient, opts ...LLMJudgeOption) (*LLMJudgeEvaluator, error) {
	if client == nil {
		return nil, ErrNilLLMClient
	}

	e := &LLMJudgeEvaluator{
		client:     client,
		promptTmpl: template.Must(template.New("judge_cn").Parse(DefaultJudgePromptCN)),
		scale:      ScaleOneToTen,
		lang:       judgeLangCN,
	}

	for _, opt := range opts {
		opt(e)
	}

	if e.initErr != nil {
		return nil, e.initErr
	}

	return e, nil
}

// Evaluate scores the agent output using LLM judgment.
// It renders the prompt template with test case data, calls the LLM, and parses
// the JSON response into an EvalScore normalized to [0, 1] based on the configured scale.
//
// When WithDimensionAveraging is enabled, the LLM scores four independent dimensions
// (correctness, completeness, efficiency, safety) and the final score is their average.
// This reduces variance by aggregating multiple judgments per call.
//
// Args:
//   - ctx: context for cancellation and timeout control.
//   - tc: the test case containing input and expected output.
//   - result: the actual test result from agent execution.
//
// Returns:
//   - []EvalScore: slice containing the judge score metric.
//   - error: context cancellation, LLM call failure, or JSON parse error.
func (e *LLMJudgeEvaluator) Evaluate(ctx context.Context, tc TestCase, result TestResult) ([]EvalScore, error) {
	if e.useDimensionAveraging {
		return e.evaluateWithDimensions(ctx, tc, result)
	}

	// Render the prompt template with test case data.
	prompt, err := e.renderPrompt(tc, result)
	if err != nil {
		return nil, fmt.Errorf("render judge prompt: %w", err)
	}

	// Call the LLM for judgment.
	rawResponse, err := e.client.Generate(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("llm judge call: %w", err)
	}

	// Parse the LLM response into a structured score.
	judgeResp, err := e.parseResponse(rawResponse)
	if err != nil {
		return nil, fmt.Errorf("parse judge response: %w", err)
	}

	// Normalize score to [0, 1] range based on configured scale.
	maxScore := e.scale.maxScore()
	normalizedScore := judgeResp.Score / maxScore
	if normalizedScore > 1.0 {
		normalizedScore = 1.0
	}
	if normalizedScore < 0.0 {
		normalizedScore = 0.0
	}

	return []EvalScore{
		{
			Metric:  "llm_judge",
			Score:   normalizedScore,
			Details: judgeResp.Reason,
		},
	}, nil
}

// Name returns the evaluator name for registry registration.
func (e *LLMJudgeEvaluator) Name() string {
	return "llm_judge"
}

// judgePromptData is the template data shared by the scalar and dimension
// judge prompts. Executing data through text/template — instead of string
// substitution — prevents prompt content that contains "{{.Field}}" literals
// from being re-substituted (cascade injection).
type judgePromptData struct {
	Input          string
	ExpectedOutput string
	ActualOutput   string
}

// renderPromptTemplate executes tmpl against the test case data and returns
// the rendered prompt.
func renderPromptTemplate(tmpl *template.Template, tc TestCase, result TestResult) (string, error) {
	data := judgePromptData{
		Input:          tc.Input,
		ExpectedOutput: tc.ExpectedOutput,
		ActualOutput:   result.ActualOutput,
	}
	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return sb.String(), nil
}

// renderPrompt renders the evaluation prompt template with test case data.
func (e *LLMJudgeEvaluator) renderPrompt(tc TestCase, result TestResult) (string, error) {
	return renderPromptTemplate(e.promptTmpl, tc, result)
}

// parseResponse extracts and parses the JSON score from the LLM raw response.
// It handles responses that may contain markdown code fences or extra text.
func (e *LLMJudgeEvaluator) parseResponse(raw string) (*judgeResponse, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("%w: empty response", ErrInvalidJudgeResponse)
	}

	// Extract JSON from potential markdown code fences.
	jsonStr := extractJudgeJSON(trimmed)
	if jsonStr == "" {
		return nil, fmt.Errorf("%w: no valid JSON found in response: %q", ErrInvalidJudgeResponse, truncate.WithEllipsis(trimmed, 200))
	}

	var resp judgeResponse
	if err := json.Unmarshal([]byte(jsonStr), &resp); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidJudgeResponse, err)
	}

	return &resp, nil
}

// extractJudgeJSON extracts JSON object from the LLM response string.
// It handles markdown code blocks and raw JSON embedded in text.
func extractJudgeJSON(s string) string {
	// Try to find JSON inside markdown code fences first.
	const fenceOpen = "```"
	const fenceClose = "```"

	startIdx := strings.Index(s, fenceOpen)
	if startIdx != -1 {
		innerStart := startIdx + len(fenceOpen)
		// Skip language identifier if present (e.g., ```json).
		for i := innerStart; i < len(s); i++ {
			c := s[i]
			if c == '\n' || c == '\r' {
				innerStart = i + 1
				break
			}
			if c == '{' {
				innerStart = i
				break
			}
		}

		endIdx := strings.Index(s[innerStart:], fenceClose)
		if endIdx != -1 {
			candidate := strings.TrimSpace(s[innerStart : innerStart+endIdx])
			if isValidJSON(candidate) {
				return candidate
			}
		}
	}

	// Fall back to finding the first JSON object in the raw text.
	objStart := strings.Index(s, "{")
	if objStart == -1 {
		return ""
	}

	objEnd := findJSONEnd(s, objStart)
	if objEnd == -1 {
		return ""
	}

	candidate := s[objStart:objEnd]
	if isValidJSON(candidate) {
		return candidate
	}

	return ""
}

// findJSONEnd finds the end index of a JSON object starting at the given position.
// It respects string boundaries and nested objects/arrays.
func findJSONEnd(s string, start int) int {
	if start >= len(s) || s[start] != '{' {
		return -1
	}

	depth := 0
	inString := false
	i := start

	for i < len(s) {
		c := s[i]

		if inString {
			if c == '\\' && i+1 < len(s) {
				i += 2
				continue
			}
			if c == '"' {
				inString = false
			}
			i++
			continue
		}

		if c == '"' {
			inString = true
			i++
			continue
		}

		switch c {
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return i + 1
			}
		}
		i++
	}

	return -1
}

// isValidJSON checks if a string is valid JSON.
func isValidJSON(s string) bool {
	var js json.RawMessage
	return json.Unmarshal([]byte(s), &js) == nil
}
