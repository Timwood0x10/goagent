// Command 22-evolution-blocks demonstrates composing the evolution system
// from the public api/evolution building blocks — WITHOUT importing any
// internal/ package. This is the integration path for external modules
// and AI assistants that want to assemble their own evolution pipeline.
//
// Purpose:
//
//	Show the full, external-friendly path to building a self-evolving
//	strategy pipeline: seed a base Strategy, build a Mutator, build a
//	Population, score agents, run a generation, and finally use a Promoter
//	to decide a candidate's fate. Every component comes from the public
//	api/evolution package, so no internal/ import is needed.
//
// Learning objectives:
//   - How a base Strategy seeds the genotype that evolution mutates.
//   - How NewMutator + MutationConfig perturb strategy params/prompts.
//   - How NewPopulation + DefaultPopulationConfig run a GA over a base.
//   - How ScorerFunc lets external callers plug in their own evaluator.
//   - How Evolve advances one generation and how BestStrategy tracks the
//     reigning champion.
//   - How NewPromoter + PromotionCriteria decide champion/demote/keep.
//
// Core APIs used:
//   - github.com/Timwood0x10/ares/api/evolution
//     Strategy, MutationConfig, NewMutator, Mutator.Mutate,
//     PopulationConfig, DefaultPopulationConfig, NewPopulation,
//     Population (Size, CurrentGeneration, BestScore, BestStrategy,
//     ScoreAgents, Evolve), ScorerFunc,
//     PromotionCriteria, NewPromoter, Promoter (Evaluate, Promote).
//
// Run:
//
//	go run examples/22-evolution-blocks/main.go
//
// Expected output (numeric values are deterministic per run):
//
//	Seed strategy: id=base-strategy-001 version=1 params=map[...]
//	Mutated child: id=... version=2 mutation=... params=map[...]
//	Population: size=10 generation=0 best_score=0.00
//	Scored 10 agents, best_score=<float>
//	After evolve: generation=1 best_score=<float>
//	  champion: id=... version=... params=map[...]
//	Promoter decision for champion: <state>: <reason>
//	Champion promoted.
//	Evolution blocks integration example completed.
//
// Configuration points to try:
//   - Raise popCfg.Size to 50 and observe slower but more thorough search.
//   - Set MutationConfig.ParamMutationProb to 0 to freeze params.
//   - Swap the mock ScorerFunc for an LLM-judge harness (interface is a
//     plain func(*Strategy) float64).
//   - Tighten PromotionCriteria (MinSuccessRate=0.95) to see candidates
//     rejected from championship.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	pubevolution "github.com/Timwood0x10/ares/api/evolution"
)

// exitf logs a formatted message and exits with code 1, canceling the
// context first to avoid the gocritic exitAfterDefer warning.
func exitf(cancel context.CancelFunc, format string, args ...any) {
	cancel()
	log.Printf(format+"\n", args...)
	os.Exit(1)
}

func main() {
	// Create a context with a 30-second timeout so no demo step can hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// ── Step 1: Define the seed strategy (the genotype) ──
	// A Strategy is both the phenotype and the genotype: ID/Version track
	// lineage, PromptTemplate is the instruction text, and Params holds the
	// knobs the mutator may perturb (temperature, top_k, max_tokens...).
	// Evolution always starts from one such seed.
	base := &pubevolution.Strategy{
		ID:             "base-strategy-001",
		Version:        1,
		PromptTemplate: "You are a helpful assistant. Answer concisely.",
		Params: map[string]any{
			"temperature": 0.7,
			"top_k":       40,
			"max_tokens":  2048,
		},
	}
	fmt.Printf("Seed strategy: id=%s version=%d params=%v\n", base.ID, base.Version, base.Params)

	// ── Step 2: Build a Mutator from the public API ──
	// NewMutator(model, cfg) constructs a mutator. `model` is reserved for
	// future LLM-guided mutation (may be empty). `cfg` controls mutation
	// probabilities: ParamMutationProb gates param perturbation and
	// PromptMutationProb gates prompt rewriting. A zero cfg falls back to
	// sensible defaults (0.3 each).
	mutator, err := pubevolution.NewMutator("ollama/llama3.2", pubevolution.MutationConfig{
		ParamMutationProb:  0.4, // 40% chance any single param is perturbed
		PromptMutationProb: 0.2, // 20% chance the prompt is rewritten
	})
	if err != nil {
		exitf(cancel, "create mutator: %v", err)
	}

	// ── Step 3: Mutate the base strategy once ──
	// mutator.Mutate(ctx, base) returns a child Strategy whose Params and/or
	// PromptTemplate have been perturbed according to MutationConfig.
	// MutationType on the child records which mutation kind was applied.
	// This single mutation shows what one offspring looks like before the
	// GA loop kicks in.
	child, err := mutator.Mutate(ctx, base)
	if err != nil {
		exitf(cancel, "mutate base strategy: %v", err)
	}
	fmt.Printf("Mutated child: id=%s version=%d mutation=%s params=%v\n",
		child.ID, child.Version, child.MutationType, child.Params)

	// ── Step 4: Build a Population from the public API ──
	// NewPopulation(base, cfg) creates a GA population seeded from `base`.
	// DefaultPopulationConfig() gives sensible elite/survival/selection
	// defaults; overriding Size scales the demo. The population starts at
	// generation 0 with best_score 0.00 (no scoring yet).
	popCfg := pubevolution.DefaultPopulationConfig()
	popCfg.Size = 10 // smaller population for the demo
	population, err := pubevolution.NewPopulation(base, popCfg)
	if err != nil {
		exitf(cancel, "create population: %v", err)
	}
	fmt.Printf("Population: size=%d generation=%d best_score=%.2f\n",
		population.Size(), population.CurrentGeneration(), population.BestScore())

	// ── Step 5: Score the population with a custom ScorerFunc ──
	// Evolve rejects agents whose score is still unevaluated (-1), so
	// scoring must happen before the first Evolve. External callers implement
	// ScorerFunc (func(*Strategy) float64) to plug in their own evaluator
	// — an LLM judge, a benchmark harness, a success-rate counter.
	// The mock here rewards lower temperature (stable answers) and higher
	// max_tokens (thorough answers), as a simple fitness proxy.
	population.ScoreAgents(func(s *pubevolution.Strategy) float64 {
		score := 0.0
		if t, ok := s.Params["temperature"].(float64); ok {
			score += (1.0 - t) // lower temp → higher score
		}
		if m, ok := s.Params["max_tokens"].(float64); ok {
			score += m / 4096.0 // more tokens → higher score (capped at 1.0)
		}
		return score
	})
	fmt.Printf("Scored %d agents, best_score=%.2f\n", population.Size(), population.BestScore())

	// ── Step 6: Run one generation of evolution ──
	// population.Evolve(ctx) applies mutation + crossover across the
	// population, evaluates fitness, and selects survivors. After one
	// generation BestStrategy() reflects the new reigning champion.
	// Any internal failure is wrapped and propagated as an error.
	if err := population.Evolve(ctx); err != nil {
		exitf(cancel, "evolve generation 1: %v", err)
	}
	best := population.BestStrategy() // may be nil if population is empty
	fmt.Printf("After evolve: generation=%d best_score=%.2f\n",
		population.CurrentGeneration(), population.BestScore())
	if best != nil {
		fmt.Printf("  champion: id=%s version=%d params=%v\n",
			best.ID, best.Version, best.Params)
	}

	// ── Step 7: Build a Promoter and evaluate the champion's fate ──
	// NewPromoter(criteria) builds a promoter that decides whether a
	// strategy should be promoted (champion), demoted, or kept, based on
	// accumulated evidence. PromotionCriteria knobs include MinSampleCount,
	// MinSuccessRate, MinConfidence, ChampionHoldPeriod, DemotionThreshold,
	// and MaxChampionTenure.
	promoter := pubevolution.NewPromoter(&pubevolution.PromotionCriteria{
		MinSampleCount:     1,   // demo: accept after 1 sample
		MinSuccessRate:     0.5, // require ≥50% success
		MinConfidence:      0.5, // require ≥50% confidence
		ChampionHoldPeriod: 1,
		DemotionThreshold:  0.2, // demote if success drops below 20%
		MaxChampionTenure:  10,
	})

	// promoter.Evaluate(ctx, id, successRate, confidence) returns a
	// "<state>: <reason>" string describing the candidate's fate. We feed
	// it a mock signal (0.9 success, 0.85 confidence) to show a promotion
	// path. Guard: passing an empty strategyID returns an error.
	decision, err := promoter.Evaluate(ctx, best.ID, 0.9, 0.85)
	if err != nil {
		exitf(cancel, "promoter evaluate: %v", err)
	}
	fmt.Printf("Promoter decision for champion: %s\n", decision)

	// ── Step 8: Promote the champion explicitly ──
	// promoter.Promote(ctx, id) forces the candidate into the champion slot,
	// bypassing the Evaluate decision. This is the final, explicit promotion
	// step that external callers use once they trust the evidence.
	if err := promoter.Promote(ctx, best.ID); err != nil {
		exitf(cancel, "promote champion: %v", err)
	}
	fmt.Println("Champion promoted.")

	fmt.Println("Evolution blocks integration example completed.")
}
