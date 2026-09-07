// Scenario: scorer — live smoke test of the LLMArenaScorer.
//
// Purpose:
//
//	This scenario is the shallowest of the LLM evolution suite: it calls the
//	LLMArenaScorer on a single preserved case to verify that the scorer can
//	distinguish a good instruction from a bad one. It is the building block
//	underlying the gate-3 regression check used by the gate3 and release
//	scenarios.
//
// Learning objectives:
//   - How LLMArenaScorer drives the LLM in two steps: execute the strategy on
//     a task, then grade the produced output on [0,1].
//   - How a scorer implements the ares_arena.Scorer interface by accepting a
//     ares_arena.TestCaseInput{Strategy, TestCase, Index}.
//
// Core APIs (with package paths):
//   - evosvc.NewLLMArenaScorer (internal/ares_evolution/service)
//   - (*LLMArenaScorer).Score (implements ares_arena.Scorer)
//   - ares_arena.TestCaseInput (internal/ares_arena)
//
// Run:
//
//	go run ./examples/15-llm-evolution-suite scorer
//
// Expected output:
//
//	good strategy score: 0.xxx
//	bad strategy score:  0.xxx
//
// Optionally, set LLM_SMOKE_EXPECT_REGRESSION=1 to fail the run unless the
// bad strategy scores lower than the good one.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/Timwood0x10/ares/internal/llm"
	ares_arena "github.com/Timwood0x10/ares/internal/runtime/arena"
)

// runScorerSmoke performs a live smoke test of the LLMArenaScorer: it scores
// the good and bad strategies on a single preserved case and reports both
// scores. With LLM_SMOKE_EXPECT_REGRESSION=1 the run fails unless the bad
// strategy scores lower, asserting the scorer can distinguish quality.
func runScorerSmoke(ctx context.Context, client *llm.Client) {
	// ── Step 1: Build the arena scorer around the real LLM client ──
	// buildScorer (defined in main.go) wraps the client into an
	// LLMArenaScorer, which performs two LLM calls per Score: one to make the
	// agent act on the task, one to grade the output on [0,1].
	scorer, err := buildScorer(client)
	if err != nil {
		log.Fatalf("build arena scorer: %v", err)
	}

	// ── Step 2: Score the GOOD strategy on one preserved case ──
	// The preserved case carries concrete inputs so the model can actually
	// compute; TestCaseInput pairs the instruction (Strategy) with the task
	// (TestCase). A high score means the instruction produces a correct,
	// complete output.
	preservedCase := "Given numbers a and b, return their sum as an integer."
	oldScore, err := scorer.Score(ctx, ares_arena.TestCaseInput{
		Strategy: goodStrategy,
		TestCase: preservedCase,
	})
	if err != nil {
		log.Fatalf("score good strategy: %v", err)
	}

	// ── Step 3: Score the BAD strategy on the same case ──
	// Reusing the identical case keeps the comparison fair: only the
	// instruction changes, so any score gap is attributable to strategy
	// quality — the property the gate-3 regression check relies on.
	newScore, err := scorer.Score(ctx, ares_arena.TestCaseInput{
		Strategy: badStrategy,
		TestCase: preservedCase,
	})
	if err != nil {
		log.Fatalf("score bad strategy: %v", err)
	}

	// ── Step 4: Report and optionally assert the quality gap ──
	// The smoke test is informational by default; with the
	// LLM_SMOKE_EXPECT_REGRESSION env var it becomes an assertion that the
	// scorer can tell good from bad — useful to validate a provider before
	// trusting its gate-3 verdicts.
	fmt.Printf("good strategy score: %.3f\n", oldScore)
	fmt.Printf("bad strategy score:  %.3f\n", newScore)

	if os.Getenv("LLM_SMOKE_EXPECT_REGRESSION") == "1" && newScore >= oldScore {
		log.Fatalf("expected the bad strategy to score lower, got good=%.3f bad=%.3f", oldScore, newScore)
	}
	log.Printf("scorer smoke ok (good=%.3f, bad=%.3f)", oldScore, newScore)
}
