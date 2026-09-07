package evolution

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	ares_arena "github.com/Timwood0x10/ares/internal/runtime/arena"
)

// DefaultArenaExecutePrompt instructs the LLM to act as an agent following a
// given strategy (the preserved-case instructions) on a concrete task.
// {instructions} is replaced with the strategy (old or new) instruction string,
// {case} with the preserved case description.
const DefaultArenaExecutePrompt = `You are an AI agent. Follow these instructions strictly:

{instructions}

Now handle this task:

{case}

Produce your final output.`

// DefaultArenaEvalPrompt asks the LLM to score an agent output on [0,1].
// {case} is the task, {output} the agent output to grade.
//
// NOTE: keep this grading prompt deliberately open-ended ("rate how well the
// output handles the task") rather than using strict anchored rubrics. A
// stricter rubric makes the grader fluctuate more between samples, inflating
// variance and weakening the regression significance test. Measured on agnes
// (2026-08-11): open-ended prompt -> old avg 0.45, p=0.0297 (significant);
// anchored prompt -> old avg 0.26, p=0.393 (not significant).
const DefaultArenaEvalPrompt = `On a scale from 0.0 to 1.0, rate how well the following agent output ` +
	`handles the task, considering correctness, completeness, and adherence to the instructions.

Task:
{case}

Agent output:
{output}

Reply with ONLY the numeric score (for example: 0.85).`

// DefaultArenaBatchExecutePrompt executes one strategy over many tasks in a
// single LLM call. {instructions} is the strategy; {items} is replaced with
// numbered "Task <i>: <case>" lines. The model replies with one result per line.
const DefaultArenaBatchExecutePrompt = `You are an AI agent. Follow these instructions strictly:

{instructions}

Handle EACH numbered task below, in order. For each task, output ONLY the result on its own line.

{items}`

// DefaultArenaBatchEvalPrompt grades many (task, output) pairs in one LLM call.
// {items} is replaced with numbered "Task <i>: ... Output <i>: ..." blocks. The
// model replies with exactly one numeric score per line, in order.
const DefaultArenaBatchEvalPrompt = `On a scale from 0.0 to 1.0, rate how well each agent output handles its ` +
	`task, considering correctness, completeness, and adherence to the instructions.

{items}

Reply with ONLY one numeric score per line, in order, for example:
0.85
0.30
0.00`

// LLMArenaScorer implements ares_arena.Scorer by driving a real LLM: it asks
// the LLM to act as an agent following a strategy (old or new instructions) on
// a preserved case, then asks it to grade the produced output on [0,1]. This
// wires the evolution candidate gate 3 regression check to a live model.
//
// It is safe to run concurrently; the underlying LLMClient must be.
type LLMArenaScorer struct {
	client          LLMClient // Generate(ctx, prompt) -> output; must not be nil
	execPrompt      string    // single-case execution prompt template
	evalPrompt      string    // single-case grading prompt template
	batchExecPrompt string    // multi-case execution prompt template
	batchEvalPrompt string    // multi-case grading prompt template
}

// LLMArenaScorerConfig configures an LLMArenaScorer.
type LLMArenaScorerConfig struct {
	// Client is the LLM client used for execution and grading.
	Client LLMClient

	// ExecPrompt is the execution prompt template; when empty the default is
	// used. It must contain {instructions} and {case} placeholders.
	ExecPrompt string

	// EvalPrompt is the grading prompt template; when empty the default is
	// used. It must contain {case} and {output} placeholders.
	EvalPrompt string

	// BatchExecPrompt is the multi-case execution prompt template; when empty
	// the default is used. It must contain {instructions} and {items}.
	BatchExecPrompt string

	// BatchEvalPrompt is the multi-case grading prompt template; when empty the
	// default is used. It must contain {items}.
	BatchEvalPrompt string
}

// NewLLMArenaScorer creates an LLMArenaScorer from config.
// Args:
//
//	cfg - scorer configuration; Client must be non-nil.
//
// Returns:
//
//	*LLMArenaScorer - the configured scorer.
//	error - when the client is nil.
func NewLLMArenaScorer(cfg LLMArenaScorerConfig) (*LLMArenaScorer, error) {
	if cfg.Client == nil {
		return nil, errors.New("LLMArenaScorer: client must not be nil")
	}
	execPrompt := cfg.ExecPrompt
	if execPrompt == "" {
		execPrompt = DefaultArenaExecutePrompt
	}
	evalPrompt := cfg.EvalPrompt
	if evalPrompt == "" {
		evalPrompt = DefaultArenaEvalPrompt
	}
	batchExecPrompt := cfg.BatchExecPrompt
	if batchExecPrompt == "" {
		batchExecPrompt = DefaultArenaBatchExecutePrompt
	}
	batchEvalPrompt := cfg.BatchEvalPrompt
	if batchEvalPrompt == "" {
		batchEvalPrompt = DefaultArenaBatchEvalPrompt
	}
	return &LLMArenaScorer{
		client:          cfg.Client,
		execPrompt:      execPrompt,
		evalPrompt:      evalPrompt,
		batchExecPrompt: batchExecPrompt,
		batchEvalPrompt: batchEvalPrompt,
	}, nil
}

// Score implements ares_arena.Scorer. The input must be a
// ares_arena.TestCaseInput whose Strategy carries the instruction string and
// whose TestCase carries the preserved case. It performs two LLM calls: one to
// produce the agent output and one to grade it on [0,1].
// Args:
//
//	ctx - cancellation and timeout context.
//	input - a *TestCaseInput{Strategy, TestCase, Index}.
//
// Returns:
//
//	float64 - the graded quality in [0,1].
//	error - on a malformed input, an LLM failure, or an unparseable score.
func (s *LLMArenaScorer) Score(ctx context.Context, input any) (float64, error) {
	ti, ok := input.(ares_arena.TestCaseInput)
	if !ok {
		return 0, fmt.Errorf("LLMArenaScorer: unexpected input type %T", input)
	}
	instructions, ok := ti.Strategy.(string)
	if !ok {
		return 0, fmt.Errorf("LLMArenaScorer: strategy must be a string instruction, got %T", ti.Strategy)
	}
	if instructions == "" {
		return 0, errors.New("LLMArenaScorer: instructions are empty")
	}
	caseStr := caseToString(ti.TestCase)

	// Step 1: execute the strategy on the preserved case.
	execPrompt := strings.NewReplacer(
		"{instructions}", instructions,
		"{case}", caseStr,
	).Replace(s.execPrompt)
	output, err := s.client.Generate(ctx, execPrompt)
	if err != nil {
		return 0, fmt.Errorf("LLMArenaScorer: execute: %w", err)
	}
	if strings.TrimSpace(output) == "" {
		return 0, errors.New("LLMArenaScorer: agent produced empty output")
	}

	// Step 2: grade the produced output on [0,1].
	evalPrompt := strings.NewReplacer(
		"{case}", caseStr,
		"{output}", output,
	).Replace(s.evalPrompt)
	rawScore, err := s.client.Generate(ctx, evalPrompt)
	if err != nil {
		return 0, fmt.Errorf("LLMArenaScorer: grade: %w", err)
	}

	score, err := parseArenaScore(rawScore)
	if err != nil {
		return 0, fmt.Errorf("LLMArenaScorer: parse score: %w", err)
	}
	return score, nil
}

// ScoreBatch implements ares_arena.BatchScorer, collapsing count executions and
// gradings into exactly two LLM calls (one batch execute, one batch grade).
// This drastically reduces the request count for rate-limited providers: a full
// regression run becomes 2 calls instead of 2×count.
// Args:
//
//	ctx - cancellation and timeout context.
//	strategy - the instruction string shared by all runs.
//	count - number of scores to produce.
//	testCases - the preserved case suite; cycled when shorter than count.
//
// Returns:
//
//	[]float64 - exactly count graded scores in [0,1].
//	error - on a malformed strategy, LLM failure, or unparseable batch output.
func (s *LLMArenaScorer) ScoreBatch(ctx context.Context, strategy any, count int, testCases []any) ([]float64, error) {
	if count <= 0 {
		return []float64{}, nil
	}
	instructions, ok := strategy.(string)
	if !ok {
		return nil, fmt.Errorf("LLMArenaScorer: strategy must be a string instruction, got %T", strategy)
	}
	if instructions == "" {
		return nil, errors.New("LLMArenaScorer: instructions are empty")
	}

	cases := make([]any, count)
	for i := range count {
		if len(testCases) > 0 {
			cases[i] = testCases[i%len(testCases)]
		} else {
			cases[i] = ""
		}
	}

	// Step 1: execute the strategy over all cases in one call.
	items := buildNumberedItems(cases)
	execPrompt := strings.NewReplacer(
		"{instructions}", instructions,
		"{items}", items,
	).Replace(s.batchExecPrompt)
	rawOutput, err := s.client.Generate(ctx, execPrompt)
	if err != nil {
		return nil, fmt.Errorf("LLMArenaScorer: batch execute: %w", err)
	}
	outputs := splitOutputLines(rawOutput, count)
	if len(outputs) < count {
		return nil, fmt.Errorf("LLMArenaScorer: batch execute returned %d outputs, want %d", len(outputs), count)
	}

	// Step 2: grade all (case, output) pairs in one call.
	pairs := buildNumberedItemsWithOutput(cases, outputs)
	evalPrompt := strings.NewReplacer("{items}", pairs).Replace(s.batchEvalPrompt)
	rawScores, err := s.client.Generate(ctx, evalPrompt)
	if err != nil {
		return nil, fmt.Errorf("LLMArenaScorer: batch grade: %w", err)
	}

	return parseBatchScores(rawScores, count)
}

// buildNumberedItems renders cases as numbered task lines for the batch execute
// prompt, e.g. "Task 1: <case>" (one per line).
func buildNumberedItems(cases []any) string {
	var b strings.Builder
	for i, c := range cases {
		fmt.Fprintf(&b, "Task %d: %s\n", i+1, caseToString(c))
	}
	return strings.TrimRight(b.String(), "\n")
}

// buildNumberedItemsWithOutput renders (case, output) pairs as numbered blocks
// for the batch grading prompt.
func buildNumberedItemsWithOutput(cases []any, outputs []string) string {
	var b strings.Builder
	for i := range cases {
		fmt.Fprintf(&b, "Task %d: %s\nOutput %d: %s\n\n", i+1, caseToString(cases[i]), i+1, outputs[i])
	}
	return strings.TrimRight(b.String(), "\n")
}

// splitOutputLines splits a batch-execute response into up to count non-empty
// trimmed lines, so each line maps to one run's agent output.
func splitOutputLines(raw string, count int) []string {
	lines := make([]string, 0, count)
	for ln := range strings.Lines(raw) {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" {
			continue
		}
		lines = append(lines, trimmed)
		if len(lines) >= count {
			break
		}
	}
	return lines
}

// parseBatchScores extracts exactly count numeric scores from a batch grading
// response, reading the first numeric value of each non-empty line. A response
// with fewer than count numeric scores is an error.
func parseBatchScores(resp string, count int) ([]float64, error) {
	scores := make([]float64, 0, count)
	for ln := range strings.Lines(resp) {
		if len(scores) >= count {
			break
		}
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" {
			continue
		}
		v := extractFirstFloat(trimmed)
		if v < 0 {
			continue // line had no number
		}
		if v > 1 {
			v = 1
		}
		scores = append(scores, v)
	}
	if len(scores) != count {
		return nil, fmt.Errorf("LLMArenaScorer: batch grade returned %d scores, want %d", len(scores), count)
	}
	return scores, nil
}

// caseToString renders a preserved case into a prompt-safe string. A string is
// used as-is; other values are rendered via %v so arbitrary case types work.
func caseToString(c any) string {
	if s, ok := c.(string); ok {
		return s
	}
	if c == nil {
		return ""
	}
	return fmt.Sprintf("%v", c)
}

// parseArenaScore parses a numeric score in [0,1] from an LLM grading response.
// It accepts a bare number ("0.85"), a number with surrounding text, or a JSON
// value such as {"score": 0.85}. Values outside [0,1] are clamped.
func parseArenaScore(resp string) (float64, error) {
	resp = strings.TrimSpace(resp)
	if resp == "" {
		return 0, errors.New("empty grading response")
	}
	// Extract the first floating-point number that looks like a score.
	num := extractFirstFloat(resp)
	if num < 0 {
		return 0, fmt.Errorf("no numeric score found in %q", resp)
	}
	// Clamp to the [0,1] score domain.
	if num > 1 {
		num = 1
	}
	return num, nil
}

// extractFirstFloat finds the first valid float literal in a string, returning
// -1 when none is found. It handles "0.85", ".85", "0.8500", and digits
// embedded in JSON/text.
func extractFirstFloat(s string) float64 {
	start := -1
	for i := 0; i < len(s); i++ {
		if s[i] == '-' && i+1 < len(s) && isDigit(s[i+1]) {
			if start == -1 {
				start = i
			}
			continue
		}
		if isDigit(s[i]) || s[i] == '.' {
			if start == -1 {
				start = i
			}
			continue
		}
		if start >= 0 {
			if v, err := strconv.ParseFloat(s[start:i], 64); err == nil {
				return v
			}
			start = -1
		}
	}
	if start >= 0 {
		if v, err := strconv.ParseFloat(s[start:], 64); err == nil {
			return v
		}
	}
	return -1
}

func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}
