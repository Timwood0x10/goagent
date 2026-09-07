package evolution

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	ares_arena "github.com/Timwood0x10/ares/internal/runtime/arena"
)

// funcLLMClient adapts a function to the LLMClient interface.
type funcLLMClient func(ctx context.Context, prompt string) (string, error)

func (f funcLLMClient) Generate(ctx context.Context, prompt string) (string, error) {
	return f(ctx, prompt)
}

// newArenaCaseInput builds a standard TestCaseInput for the scorer tests.
func newArenaCaseInput(strategy string) ares_arena.TestCaseInput {
	return ares_arena.TestCaseInput{Strategy: strategy, TestCase: "case: add(a,b)", Index: 0}
}

// isExecPrompt reports whether a prompt is the single-case execution prompt
// (vs grading). It matches the execution template marker ("...final output...").
func isExecPrompt(prompt string) bool {
	return strings.Contains(prompt, "your final output")
}

// isBatchExecPrompt reports whether a prompt is the multi-case batch execution
// prompt, which asks the model to handle each numbered task.
func isBatchExecPrompt(prompt string) bool {
	return strings.Contains(prompt, "numbered task") || strings.Contains(prompt, "EACH numbered task")
}

// isBatchEvalPrompt reports whether a prompt is the multi-case batch grading
// prompt, which asks for one numeric score per line.
func isBatchEvalPrompt(prompt string) bool {
	return strings.Contains(prompt, "one numeric score per line")
}

func TestLLMArenaScorer_Score_Success(t *testing.T) {
	client := funcLLMClient(func(_ context.Context, prompt string) (string, error) {
		if isExecPrompt(prompt) {
			return "agent output: 3", nil
		}
		return "0.85", nil
	})
	scorer, err := NewLLMArenaScorer(LLMArenaScorerConfig{Client: client})
	if err != nil {
		t.Fatalf("NewLLMArenaScorer returned error: %v", err)
	}

	score, err := scorer.Score(context.Background(), newArenaCaseInput("old instructions"))
	if err != nil {
		t.Fatalf("Score returned error: %v", err)
	}
	if score != 0.85 {
		t.Errorf("Score() = %v, want 0.85", score)
	}
}

func TestLLMArenaScorer_Score_ClampsOutOfRange(t *testing.T) {
	client := funcLLMClient(func(_ context.Context, prompt string) (string, error) {
		if isExecPrompt(prompt) {
			return "out", nil
		}
		return "1.7", nil // above 1 must clamp to 1
	})
	scorer, err := NewLLMArenaScorer(LLMArenaScorerConfig{Client: client})
	if err != nil {
		t.Fatalf("NewLLMArenaScorer returned error: %v", err)
	}
	score, err := scorer.Score(context.Background(), newArenaCaseInput("new instructions"))
	if err != nil {
		t.Fatalf("Score returned error: %v", err)
	}
	if score != 1.0 {
		t.Errorf("Score() = %v, want 1.0 (clamped)", score)
	}
}

func TestLLMArenaScorer_Score_ParsesTextScore(t *testing.T) {
	client := funcLLMClient(func(_ context.Context, prompt string) (string, error) {
		if isExecPrompt(prompt) {
			return "handled", nil
		}
		return "The quality is 0.62 out of 1", nil
	})
	scorer, err := NewLLMArenaScorer(LLMArenaScorerConfig{Client: client})
	if err != nil {
		t.Fatalf("NewLLMArenaScorer returned error: %v", err)
	}
	score, err := scorer.Score(context.Background(), newArenaCaseInput("instructions"))
	if err != nil {
		t.Fatalf("Score returned error: %v", err)
	}
	if score != 0.62 {
		t.Errorf("Score() = %v, want 0.62", score)
	}
}

func TestNewLLMArenaScorer_NilClient(t *testing.T) {
	_, err := NewLLMArenaScorer(LLMArenaScorerConfig{Client: nil})
	if err == nil {
		t.Fatal("NewLLMArenaScorer with nil client should error")
	}
}

func TestLLMArenaScorer_Score_BadInput(t *testing.T) {
	scorer, err := NewLLMArenaScorer(LLMArenaScorerConfig{Client: funcLLMClient(nil)})
	if err != nil {
		t.Fatalf("NewLLMArenaScorer returned error: %v", err)
	}

	t.Run("non TestCaseInput type", func(t *testing.T) {
		_, err := scorer.Score(context.Background(), "not a case input")
		if err == nil {
			t.Fatal("Score with wrong input type should error")
		}
	})

	t.Run("non-string strategy", func(t *testing.T) {
		_, err := scorer.Score(context.Background(), ares_arena.TestCaseInput{Strategy: 42, TestCase: "c"})
		if err == nil {
			t.Fatal("Score with non-string strategy should error")
		}
	})

	t.Run("empty instructions", func(t *testing.T) {
		_, err := scorer.Score(context.Background(), newArenaCaseInput(""))
		if err == nil {
			t.Fatal("Score with empty instructions should error")
		}
	})
}

func TestLLMArenaScorer_Score_ExecError(t *testing.T) {
	client := funcLLMClient(func(_ context.Context, _ string) (string, error) {
		return "", errors.New("llm down")
	})
	scorer, err := NewLLMArenaScorer(LLMArenaScorerConfig{Client: client})
	if err != nil {
		t.Fatalf("NewLLMArenaScorer returned error: %v", err)
	}
	_, err = scorer.Score(context.Background(), newArenaCaseInput("instructions"))
	if err == nil || !strings.Contains(err.Error(), "execute") {
		t.Fatalf("Score should propagate exec error, got %v", err)
	}
}

func TestLLMArenaScorer_Score_EmptyOutput(t *testing.T) {
	client := funcLLMClient(func(_ context.Context, prompt string) (string, error) {
		if isExecPrompt(prompt) {
			return "   ", nil // whitespace-only output
		}
		return "0.5", nil
	})
	scorer, err := NewLLMArenaScorer(LLMArenaScorerConfig{Client: client})
	if err != nil {
		t.Fatalf("NewLLMArenaScorer returned error: %v", err)
	}
	_, err = scorer.Score(context.Background(), newArenaCaseInput("instructions"))
	if err == nil {
		t.Fatal("Score should error on empty agent output")
	}
}

func TestLLMArenaScorer_Score_GradeError(t *testing.T) {
	client := funcLLMClient(func(_ context.Context, prompt string) (string, error) {
		if isExecPrompt(prompt) {
			return "output", nil
		}
		return "", errors.New("grade failed")
	})
	scorer, err := NewLLMArenaScorer(LLMArenaScorerConfig{Client: client})
	if err != nil {
		t.Fatalf("NewLLMArenaScorer returned error: %v", err)
	}
	_, err = scorer.Score(context.Background(), newArenaCaseInput("instructions"))
	if err == nil || !strings.Contains(err.Error(), "grade") {
		t.Fatalf("Score should propagate grade error, got %v", err)
	}
}

func TestLLMArenaScorer_Score_UnparseableScore(t *testing.T) {
	client := funcLLMClient(func(_ context.Context, prompt string) (string, error) {
		if isExecPrompt(prompt) {
			return "output", nil
		}
		return "no numbers here", nil
	})
	scorer, err := NewLLMArenaScorer(LLMArenaScorerConfig{Client: client})
	if err != nil {
		t.Fatalf("NewLLMArenaScorer returned error: %v", err)
	}
	_, err = scorer.Score(context.Background(), newArenaCaseInput("instructions"))
	if err == nil || !strings.Contains(err.Error(), "parse score") {
		t.Fatalf("Score should error on unparseable grade, got %v", err)
	}
}

// TestLLMArenaScorer_RegressionComparison verifies the LLM-backed scorer can
// power the ares_arena regression tester used by the candidate gate 3: old
// (good) instructions score higher than new (bad) instructions on the same
// preserved case, producing a confident regression.
func TestLLMArenaScorer_RegressionComparison(t *testing.T) {
	// LLMArenaScorer implements BatchScorer, so the regression tester takes the
	// batch path. The mock returns batch output/scores based on which strategy
	// is being scored (old = good, new = bad).
	client := funcLLMClient(func(_ context.Context, prompt string) (string, error) {
		switch {
		case isBatchExecPrompt(prompt):
			if strings.Contains(prompt, "old instructions") {
				return "5\n5\n5\n5\n5", nil // good: correct sums
			}
			return "0\n0\n0\n0\n0", nil // bad: wrong answers
		case isBatchEvalPrompt(prompt):
			// The grading prompt embeds the executed outputs; old produced "5".
			if strings.Contains(prompt, "Output 1: 5") {
				return "0.9\n0.9\n0.9\n0.9\n0.9", nil
			}
			return "0.2\n0.2\n0.2\n0.2\n0.2", nil
		default:
			// Single-case paths should not be hit in this test.
			return "0.5", nil
		}
	})
	scorer, err := NewLLMArenaScorer(LLMArenaScorerConfig{Client: client})
	if err != nil {
		t.Fatalf("NewLLMArenaScorer returned error: %v", err)
	}

	tester, err := ares_arena.NewRegressionTesterWithScorer(scorer)
	if err != nil {
		t.Fatalf("NewRegressionTesterWithScorer returned error: %v", err)
	}

	result, err := tester.Run(context.Background(), ares_arena.RegressionConfig{
		OldStrategy:  "old instructions",
		NewStrategy:  "new instructions",
		BaselineRuns: 5,
		CompareRuns:  5,
		Confidence:   0.05,
		TestCases:    []any{"preserved case one", "preserved case two"},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result == nil {
		t.Fatal("Run returned nil result")
	}
	if !result.Confident {
		t.Error("expected a confident regression for an obvious quality drop")
	}
	if result.NewAvg >= result.OldAvg {
		t.Errorf("expected new avg below old avg: old=%f new=%f", result.OldAvg, result.NewAvg)
	}
}

// TestLLMArenaScorer_ScoreBatch verifies the batch scorer collapses count
// executions+gradings into exactly 2 LLM calls and returns count scores.
func TestLLMArenaScorer_ScoreBatch(t *testing.T) {
	var calls int
	client := funcLLMClient(func(_ context.Context, prompt string) (string, error) {
		calls++
		if isBatchExecPrompt(prompt) {
			return "5\n0", nil // two executed outputs
		}
		return "0.8\n0.1", nil // two grades
	})
	scorer, err := NewLLMArenaScorer(LLMArenaScorerConfig{Client: client})
	if err != nil {
		t.Fatalf("NewLLMArenaScorer returned error: %v", err)
	}

	scores, err := scorer.ScoreBatch(context.Background(), "add numbers", 2, []any{"case-1", "case-2"})
	if err != nil {
		t.Fatalf("ScoreBatch returned error: %v", err)
	}
	if len(scores) != 2 {
		t.Fatalf("ScoreBatch returned %d scores, want 2", len(scores))
	}
	if scores[0] != 0.8 || scores[1] != 0.1 {
		t.Errorf("ScoreBatch scores = %v, want [0.8 0.1]", scores)
	}
	if calls != 2 {
		t.Errorf("ScoreBatch made %d LLM calls, want exactly 2 (batch merge)", calls)
	}
}

func TestLLMArenaScorer_ScoreBatch_ZeroCount(t *testing.T) {
	scorer, err := NewLLMArenaScorer(LLMArenaScorerConfig{Client: funcLLMClient(nil)})
	if err != nil {
		t.Fatalf("NewLLMArenaScorer returned error: %v", err)
	}
	scores, err := scorer.ScoreBatch(context.Background(), "add", 0, nil)
	if err != nil {
		t.Fatalf("ScoreBatch(0) returned error: %v", err)
	}
	if len(scores) != 0 {
		t.Fatalf("ScoreBatch(0) returned %d scores, want 0", len(scores))
	}
}

func TestLLMArenaScorer_ScoreBatch_BadStrategy(t *testing.T) {
	scorer, err := NewLLMArenaScorer(LLMArenaScorerConfig{Client: funcLLMClient(nil)})
	if err != nil {
		t.Fatalf("NewLLMArenaScorer returned error: %v", err)
	}
	if _, err := scorer.ScoreBatch(context.Background(), 42, 2, nil); err == nil {
		t.Fatal("ScoreBatch with non-string strategy should error")
	}
}

func TestLLMArenaScorer_ScoreBatch_TooFewOutputs(t *testing.T) {
	client := funcLLMClient(func(_ context.Context, prompt string) (string, error) {
		if isBatchExecPrompt(prompt) {
			return "5", nil // only 1 output, want 3
		}
		return "0.8\n0.1\n0.3", nil
	})
	scorer, err := NewLLMArenaScorer(LLMArenaScorerConfig{Client: client})
	if err != nil {
		t.Fatalf("NewLLMArenaScorer returned error: %v", err)
	}
	_, err = scorer.ScoreBatch(context.Background(), "add", 3, []any{"a", "b", "c"})
	if err == nil || !strings.Contains(err.Error(), "batch execute returned") {
		t.Fatalf("ScoreBatch should error on too-few outputs, got %v", err)
	}
}

func TestLLMArenaScorer_ScoreBatch_TooFewScores(t *testing.T) {
	client := funcLLMClient(func(_ context.Context, prompt string) (string, error) {
		if isBatchExecPrompt(prompt) {
			return "5\n0\n5", nil
		}
		return "0.8\n0.1", nil // only 2 scores, want 3
	})
	scorer, err := NewLLMArenaScorer(LLMArenaScorerConfig{Client: client})
	if err != nil {
		t.Fatalf("NewLLMArenaScorer returned error: %v", err)
	}
	_, err = scorer.ScoreBatch(context.Background(), "add", 3, []any{"a", "b", "c"})
	if err == nil || !strings.Contains(err.Error(), "batch grade returned") {
		t.Fatalf("ScoreBatch should error on too-few scores, got %v", err)
	}
}

// batchCountingScorer implements BatchScorer and counts calls to prove the
// regression tester collapses all runs into one batch call. The counter is
// mutex-guarded because the tester scores old/new strategies concurrently.
type batchCountingScorer struct {
	mu         sync.Mutex
	scores     []float64
	batchCalls int
}

func (b *batchCountingScorer) Score(_ context.Context, _ any) (float64, error) {
	return 0, errors.New("single Score should not be called when batch is available")
}

func (b *batchCountingScorer) ScoreBatch(_ context.Context, _ any, count int, _ []any) ([]float64, error) {
	b.mu.Lock()
	b.batchCalls++
	b.mu.Unlock()
	out := make([]float64, count)
	for i := range out {
		out[i] = b.scores[i%len(b.scores)]
	}
	return out, nil
}

// TestRegressionTester_UsesBatchScorer verifies the regression tester collapses
// each strategy's runs into a single ScoreBatch call (batch merge).
func TestRegressionTester_UsesBatchScorer(t *testing.T) {
	scorer := &batchCountingScorer{scores: []float64{0.9}}
	tester, err := ares_arena.NewRegressionTesterWithScorer(scorer)
	if err != nil {
		t.Fatalf("NewRegressionTesterWithScorer returned error: %v", err)
	}

	result, err := tester.Run(context.Background(), ares_arena.RegressionConfig{
		OldStrategy:  "old",
		NewStrategy:  "new",
		BaselineRuns: 5,
		CompareRuns:  3,
		Confidence:   0.05,
		TestCases:    []any{"case-1"},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result == nil {
		t.Fatal("Run returned nil result")
	}
	scorer.mu.Lock()
	batchCalls := scorer.batchCalls
	scorer.mu.Unlock()
	if batchCalls != 2 {
		t.Errorf("expected 2 ScoreBatch calls (old+new), got %d", batchCalls)
	}
	if len(result.OldScores) != 5 || len(result.NewScores) != 3 {
		t.Errorf("unexpected score counts: old=%d new=%d, want 5 and 3", len(result.OldScores), len(result.NewScores))
	}
}

// nonBatchScorer implements only Scorer (no ScoreBatch) to prove backward
// compatibility: the regression tester must fall back to per-run Score calls.
// The counter is mutex-guarded because the tester scores old/new concurrently.
type nonBatchScorer struct {
	mu    sync.Mutex
	score float64
	calls int
}

func (n *nonBatchScorer) Score(_ context.Context, _ any) (float64, error) {
	n.mu.Lock()
	n.calls++
	n.mu.Unlock()
	return n.score, nil
}

func TestRegressionTester_FallsBackToSingleScorer(t *testing.T) {
	scorer := &nonBatchScorer{score: 0.7}
	tester, err := ares_arena.NewRegressionTesterWithScorer(scorer)
	if err != nil {
		t.Fatalf("NewRegressionTesterWithScorer returned error: %v", err)
	}

	result, err := tester.Run(context.Background(), ares_arena.RegressionConfig{
		OldStrategy:  "old",
		NewStrategy:  "new",
		BaselineRuns: 4,
		CompareRuns:  2,
		Confidence:   0.05,
		TestCases:    []any{"case-1"},
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result == nil {
		t.Fatal("Run returned nil result")
	}
	scorer.mu.Lock()
	calls := scorer.calls
	scorer.mu.Unlock()
	if calls != 6 {
		t.Errorf("expected 6 single Score calls (4 old + 2 new), got %d", calls)
	}
}
