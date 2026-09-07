// Example 10 — GA Full Evolution: a complete genetic-algorithm pipeline
// using only the public api/evolution building blocks (no internal/ imports).
//
// Purpose:
//
//	Demonstrate how to build a GA that evolves agent strategies across
//	multiple generations, scoring them with a multi-objective fitness
//	function and promoting the best candidate.
//
// Learning objectives:
//   - Create a seed Strategy and a Mutator with param ranges and prompt pool.
//   - Build a Population (the GA core engine) with custom config.
//   - Score agents with a multi-objective function (quality + cost + latency).
//   - Apply memory-guided confidence to bias mutation scoring.
//   - Use a Promoter to evaluate and promote the champion strategy.
//
// Core APIs used:
//   - github.com/Timwood0x10/ares/api/evolution.Strategy
//   - github.com/Timwood0x10/ares/api/evolution.DefaultPopulationConfig
//   - github.com/Timwood0x10/ares/api/evolution.NewPopulation
//   - github.com/Timwood0x10/ares/api/evolution.Population (ScoreAgents, Evolve, BestStrategy, BestScore, Size, CurrentGeneration)
//   - github.com/Timwood0x10/ares/api/evolution.NewPromoter
//   - github.com/Timwood0x10/ares/api/evolution/mutation.NewMutator
//   - github.com/Timwood0x10/ares/api/evolution/mutation.Mutator.Mutate
//
// Run:
//
//	go run examples/10-ga-full-evolution/main.go
//
// Expected output:
//
//	═══ GA Full Evolution Demo (public API only) ═══
//	Seed strategy: id=root-strategy params=map[...]
//	Mutator configured with 6 param ranges, 4 prompts, 6 tools
//	Preview child: id=... version=2 mutation=param params=map[...]
//	Population initialized with 20 individuals
//	Memory-guided provider loaded (4 experiences)
//
//	═══ Starting GA Evolution ═══
//	  Gen 1: best=..., pop=20, tool=...
//	  ... (through Gen 5)
//
//	═══ Evolution Results: What GA Learned ═══
//	✅ Tool selection: ...
//	...
//	✅ GA full evolution demo completed
//
// Try modifying:
//   - popCfg fields (Size, EliteCount, MutationRate) to change GA dynamics.
//   - The ParamRanges in MutatorConfig to evolve different parameters.
//   - Generation loop count (currently 5) for longer evolution runs.
package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"time"

	pubevolution "github.com/Timwood0x10/ares/api/evolution"
	pubmutation "github.com/Timwood0x10/ares/api/evolution/mutation"
)

// exitf logs a formatted message and exits with code 1, canceling the
// context first to avoid the gocritic exitAfterDefer warning.
func exitf(cancel context.CancelFunc, format string, args ...any) {
	cancel()
	fmt.Printf(format+"\n", args...)
	os.Exit(1)
}

func main() {
	// 30-second timeout keeps the demo bounded; cancel on exit.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Println("═══ GA Full Evolution Demo (public API only) ═══")
	fmt.Println()

	// ── Step 1: Create the base (seed) strategy ──
	// The seed carries the initial prompt, version, and params that
	// mutation and crossover will explore. Score=-1 marks it unevaluated;
	// ScoreAgents will fill the score before Evolve runs.
	// Uses the mutation sub-package Strategy because Mutator lives there;
	// converted to the top-level evolution.Strategy when fed to Population.
	base := &pubmutation.Strategy{
		ID:             "root-strategy",
		Version:        1,
		PromptTemplate: "You are a helpful assistant. Complete the task efficiently.",
		Params: map[string]any{
			"temperature":   0.7,
			"top_k":         40,
			"max_tokens":    4096,
			"tool_selector": "auto", // tool selection strategy: auto/manual/priority
			"search_depth":  3,      // search depth
			"batch_size":    5,      // batch size
		},
	}
	fmt.Printf("Seed strategy: id=%s params=%v\n", base.ID, base.Params)

	// ── Step 2: Create a Mutator with param ranges + prompt pool + tool pool ──
	// Mutator lives in the evolution/mutation sub-package so it can carry
	// the full MutatorConfig (ranges/pools/probabilities). The top-level
	// evolution.NewMutator only takes probabilities, not ranges — sub-package
	// is the right entry point for external callers who need custom ranges.
	mutator, err := pubmutation.NewMutator(pubmutation.MutatorConfig{
		ParamRanges: map[string][]any{
			"temperature":   {0.1, 0.3, 0.5, 0.7, 0.9},
			"top_k":         {10, 20, 40, 60, 80, 100},
			"max_tokens":    {1024, 2048, 4096, 8192},
			"tool_selector": {"auto", "manual", "priority"},
			"search_depth":  {1, 2, 3, 4, 5},
			"batch_size":    {1, 3, 5, 10},
		},
		PromptPool: []string{
			"You are a helpful assistant. Complete the task efficiently.",
			"You are an expert programmer. Write clean, efficient code.",
			"You are a data analyst. Analyze data thoroughly and report findings.",
			"You are a system architect. Design robust and scalable solutions.",
		},
		ToolPool: []string{"search", "read", "write", "exec", "calculate", "code"},
		// Mutation probabilities — tune how aggressive evolution is.
		ParamMutationProb:  0.4,
		PromptMutationProb: 0.2,
	})
	if err != nil {
		exitf(cancel, "create mutator: %v", err)
	}
	fmt.Println("Mutator configured with 6 param ranges, 4 prompts, 6 tools")

	// ── Step 3: Mutate the base once to preview a child ──
	// Mutate applies param/prompt/tool mutations according to the
	// configured probabilities, returning a new child Strategy.
	previewChild, err := mutator.Mutate(ctx, base)
	if err != nil {
		exitf(cancel, "mutate preview: %v", err)
	}
	fmt.Printf("Preview child: id=%s version=%d mutation=%s params=%v\n",
		previewChild.ID, previewChild.Version, previewChild.MutationType, previewChild.Params)

	// ── Step 4: Create the Population (GA core engine) ──
	// Population lives at the top-level evolution package and consumes the
	// top-level Strategy — convert the mutation.Strategy seed here.
	pubBase := &pubevolution.Strategy{
		ID:             base.ID,
		Version:        base.Version,
		PromptTemplate: base.PromptTemplate,
		Params:         base.Params,
	}
	// DefaultPopulationConfig provides sane defaults; override the
	// fields below to tune population dynamics.
	popCfg := pubevolution.DefaultPopulationConfig()
	popCfg.Size = 20                        // number of individuals per generation
	popCfg.EliteCount = 3                   // top strategies carried over unchanged
	popCfg.MutationRate = 0.2               // probability of mutation per individual
	popCfg.SurvivalRate = 0.6               // fraction of population that survives selection
	popCfg.SelectionStrategy = "tournament" // tournament selection
	popCfg.TournamentSize = 3               // number of contestants per tournament
	population, err := pubevolution.NewPopulation(pubBase, popCfg)
	if err != nil {
		exitf(cancel, "create population: %v", err)
	}
	fmt.Printf("Population initialized with %d individuals\n", population.Size())

	// ── Step 5: Set up memory-guided hint provider (mock experience) ──
	// In production this would come from api/experience FeedbackService;
	// here we mock it to show how historical bias guides mutation scoring.
	hintProvider := &mockHintProvider{
		hints: []evolutionHint{
			{taskType: "code", tool: "search", confidence: 0.85},
			{taskType: "code", tool: "read", confidence: 0.72},
			{taskType: "data", tool: "calculate", confidence: 0.91},
			{taskType: "data", tool: "exec", confidence: 0.65},
		},
	}
	fmt.Println("Memory-guided provider loaded (4 experiences)")

	// ── Step 6: Run GA evolution for 5 generations ──
	// Each generation: score all agents, evolve (select + crossover +
	// mutate), then report the best strategy and current population size.
	fmt.Println("\n═══ Starting GA Evolution ═══")
	for gen := 0; gen < 5; gen++ {
		// Score every agent with the multi-objective scorer before evolving —
		// Evolve rejects agents with score=-1 (unevaluated).
		population.ScoreAgents(func(s *pubevolution.Strategy) float64 {
			return multiObjectiveScore(s, hintProvider)
		})

		if err := population.Evolve(ctx); err != nil {
			exitf(cancel, "evolve generation %d: %v", gen+1, err)
		}

		best := population.BestStrategy()
		toolSel := "auto"
		if best != nil {
			if v, ok := best.Params["tool_selector"]; ok {
				toolSel = fmt.Sprintf("%v", v)
			}
		}
		fmt.Printf("  Gen %d: best=%.1f, pop=%d, tool=%s\n",
			gen+1, population.BestScore(), population.Size(), toolSel)
	}

	// ── Step 7: Show evolution results — what GA learned ──
	// BestStrategy returns the highest-scoring individual; we print its
	// key evolved parameters and the final best score.
	fmt.Println("\n═══ Evolution Results: What GA Learned ═══")
	best := population.BestStrategy()
	if best != nil {
		fmt.Printf("✅ Tool selection: %v\n", best.Params["tool_selector"])
		fmt.Printf("✅ Search depth: %v\n", best.Params["search_depth"])
		fmt.Printf("✅ Prompt template: %q\n", best.PromptTemplate)
		fmt.Printf("✅ Best score: %.2f\n", population.BestScore())
		fmt.Printf("✅ Generation: %d\n", population.CurrentGeneration())
	}

	// ── Step 8: Build a Promoter and evaluate the champion's fate ──
	// NewPromoter creates a promoter with the given promotion criteria.
	// Evaluate checks whether the champion should be promoted/demoted;
	// Promote marks it as the active production strategy.
	promoter := pubevolution.NewPromoter(&pubevolution.PromotionCriteria{
		MinSampleCount:     1,
		MinSuccessRate:     0.5,
		MinConfidence:      0.5,
		ChampionHoldPeriod: 1,
		DemotionThreshold:  0.2,
		MaxChampionTenure:  10,
	})
	if best != nil {
		decision, err := promoter.Evaluate(ctx, best.ID, population.BestScore(), 0.85)
		if err != nil {
			exitf(cancel, "promoter evaluate: %v", err)
		}
		fmt.Printf("\nPromoter decision for champion: %s\n", decision)
		if err := promoter.Promote(ctx, best.ID); err != nil {
			exitf(cancel, "promote champion: %v", err)
		}
		fmt.Println("Champion promoted.")
	}

	fmt.Println("\n✅ GA full evolution demo completed")
}

// ── Multi-objective scorer ───────────────────────────────────────────

// multiObjectiveScore computes fitness from quality, cost, and latency.
// Memory-guided confidence from the hint provider biases quality upward.
func multiObjectiveScore(s *pubevolution.Strategy, hp *mockHintProvider) float64 {
	quality := scoreQuality(s)
	cost := scoreCost(s)
	latency := scoreLatency(s)

	// Memory-guided confidence bonus — historical evidence biases the score.
	confidence := hp.confidenceForStrategy(s)
	quality += confidence * 5.0

	// Multi-objective aggregation: quality prioritized, cost/latency penalized.
	finalScore := quality*0.6 - cost*0.25 - latency*0.15
	return max(0, finalScore)
}

// scoreQuality estimates strategy quality based on params.
func scoreQuality(s *pubevolution.Strategy) float64 {
	score := 50.0
	if v, ok := s.Params["temperature"]; ok {
		if t := toFloat64(v); t >= 0.5 && t <= 0.8 {
			score += 20
		} else if t < 0.3 || t > 0.9 {
			score -= 10
		}
	}
	if v, ok := s.Params["search_depth"]; ok {
		if d := toInt(v); d >= 3 && d <= 5 {
			score += 15
		} else if d < 2 {
			score -= 10
		}
	}
	if sel, ok := s.Params["tool_selector"]; ok {
		switch fmt.Sprintf("%v", sel) {
		case "priority":
			score += 10
		case "manual":
			score += 5
		}
	}
	return min(100, max(0, score))
}

// scoreCost estimates computational cost of a strategy.
func scoreCost(s *pubevolution.Strategy) float64 {
	cost := 10.0
	if v, ok := s.Params["max_tokens"]; ok {
		cost += float64(toInt(v)) / 500
	}
	if v, ok := s.Params["search_depth"]; ok {
		cost += float64(toInt(v)) * 5
	}
	if v, ok := s.Params["batch_size"]; ok {
		cost += float64(toInt(v)) * 2
	}
	return min(100, cost)
}

// scoreLatency estimates execution latency of a strategy.
func scoreLatency(s *pubevolution.Strategy) float64 {
	latency := 5.0
	if v, ok := s.Params["search_depth"]; ok {
		latency += float64(toInt(v)) * 8
	}
	if v, ok := s.Params["max_tokens"]; ok {
		latency += float64(toInt(v)) / 1000
	}
	return min(100, latency)
}

// ── Memory-guided provider (mock) ────────────────────────────────────

// evolutionHint represents a single piece of historical experience.
type evolutionHint struct {
	taskType   string
	tool       string
	confidence float64
}

// mockHintProvider simulates a memory-guided experience provider for the demo.
type mockHintProvider struct {
	hints []evolutionHint
}

// confidenceForStrategy returns the highest confidence hint matching the
// strategy's current tool_selector. Zero means no historical evidence.
func (m *mockHintProvider) confidenceForStrategy(s *pubevolution.Strategy) float64 {
	confidence := 0.0
	if sel, ok := s.Params["tool_selector"]; ok {
		for _, h := range m.hints {
			if fmt.Sprintf("%v", sel) == h.tool {
				confidence = max(confidence, h.confidence)
			}
		}
	}
	return confidence
}

// ── Helpers ──────────────────────────────────────────────────────────

// toFloat64 safely converts an any value to float64.
func toFloat64(v any) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case int:
		return float64(val)
	case string:
		f := 0.0
		_, _ = fmt.Sscanf(val, "%f", &f)
		return f
	default:
		return 0
	}
}

// toInt safely converts an any value to int.
func toInt(v any) int {
	switch val := v.(type) {
	case int:
		return val
	case float64:
		return int(val)
	case string:
		i := 0
		_, _ = fmt.Sscanf(val, "%d", &i)
		return i
	default:
		return 0
	}
}

func init() {
	// Seed the global rand so mutation/crossover vary across runs.
	_ = rand.New(rand.NewSource(time.Now().UnixNano()))
}
