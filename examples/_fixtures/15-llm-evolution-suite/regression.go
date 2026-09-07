// Scenario: regression — preserved-case regression comparison (real LLM).
//
// Purpose:
//
//	This scenario is the middle tier of the LLM evolution suite: it compares
//	a stable instruction set against a candidate instruction set over a
//	preserved case suite, exactly as the candidate gate-3 check does. It
//	reports a statistical verdict (Welch's t-test) on whether the new
//	strategy regresses the preserved cases.
//
// Learning objectives:
//   - How ares_arena.RegressionTester pairs old vs new strategies over a
//     preserved suite and computes significance.
//   - How to interpret the regression result fields: OldAvg, NewAvg, WinRate,
//     Confident, PValue.
//   - Why the bad strategy is "harmless but wrong" (a safety-triggering one
//     like "always answer zero" can make the model refuse and garble scores).
//
// Core APIs (with package paths):
//   - ares_arena.NewRegressionTesterWithScorer (internal/ares_arena)
//   - (*RegressionTester).Run with ares_arena.RegressionConfig
//   - ares_arena.RegressionResult
//
// Run:
//
//	go run ./examples/15-llm-evolution-suite regression
//
// Expected output:
//
//	old avg / new avg / win rate / confident / p-value
//	RESULT: REGRESSION detected — new strategy is significantly worse.
//
// Tune BaselineRuns/CompareRuns to match your provider's rate limit.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/Timwood0x10/ares/internal/llm"
	ares_arena "github.com/Timwood0x10/ares/internal/runtime/arena"
)

// runRegressionDemo runs a real-LLM preserved-case regression comparison
// exactly as the candidate gate 3 does: it scores the old (stable) and new
// (candidate) instructions against the preserved case suite via the
// LLMArenaScorer + ares_arena.RegressionTester, then reports whether the new
// strategy regresses the preserved cases.
//
// Each strategy side is run 5 times per case (2 LLM calls per run: execute +
// grade) so the verdict is statistically meaningful; tune the run counts to
// match your provider's rate limit.
func runRegressionDemo(ctx context.Context, client *llm.Client) {
	// ── Step 1: Build the scorer, then wrap it in a regression tester ──
	// NewRegressionTesterWithScorer needs only the scorer (no arena Service):
	// the regression path itself never touches a live runtime, so this is the
	// same scorer-only construction the candidate gate-3 check uses.
	scorer, err := buildScorer(client)
	if err != nil {
		log.Fatalf("build arena scorer: %v", err)
	}

	tester, err := ares_arena.NewRegressionTesterWithScorer(scorer)
	if err != nil {
		log.Fatalf("build regression tester: %v", err)
	}

	// ── Step 2: Log the strategies under comparison ──
	// goodStrategy is the stable coder instruction; badStrategy is a
	// harmless-but-wrong instruction (off-by-one) that should score lower on
	// the preserved cases without tripping the model's safety refusal.
	log.Printf("old strategy: %q", goodStrategy)
	log.Printf("new strategy: %q", badStrategy)
	log.Printf("preserved cases: %d", len(preservedCases))

	// ── Step 3: Run the preserved-case regression comparison ──
	// Run scores each side BaselineRuns/CompareRuns times per case and runs
	// Welch's t-test at Confidence=0.05. MinWinRate is the floor for the
	// preserved-suite win rate. TestSuite names the scenario for reporting.
	result, err := tester.Run(ctx, ares_arena.RegressionConfig{
		OldStrategy:  goodStrategy,
		NewStrategy:  badStrategy,
		BaselineRuns: 5,
		CompareRuns:  5,
		Confidence:   0.05,
		MinWinRate:   0.55,
		TestSuite:    "smoke-preserved-cases",
		TestCases:    preservedCases,
	})
	if err != nil {
		log.Fatalf("run regression: %v", err)
	}

	// ── Step 4: Print the raw comparison and the verdict ──
	// A regression is a statistically significant drop (Confident && NewAvg <
	// OldAvg); the win rate and p-value quantify how decisive the drop is.
	fmt.Println("── Regression result ──")
	fmt.Printf("old avg: %.4f (scores=%v)\n", result.OldAvg, result.OldScores)
	fmt.Printf("new avg: %.4f (scores=%v)\n", result.NewAvg, result.NewScores)
	fmt.Printf("win rate (new>=old): %.3f\n", result.WinRate)
	fmt.Printf("confident: %v\n", result.Confident)
	fmt.Printf("p-value: %.4f\n", result.PValue)
	fmt.Printf("samples: %d\n", result.Samples)

	if result.Confident && result.NewAvg < result.OldAvg {
		fmt.Println("RESULT: REGRESSION detected — new strategy is significantly worse.")
	} else {
		fmt.Println("RESULT: NO regression — new strategy is not significantly worse.")
	}
	log.Printf("regression demo done")
}
