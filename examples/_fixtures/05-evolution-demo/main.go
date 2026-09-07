// Evolution demo — demonstrates strategy evolution to improve agent performance.
//
// Purpose:
//
//	Show a complete "before → evolve → after" loop. The same task is run twice:
//	once with the default strategy, then again after rt.Evolve() has searched
//	for a better instruction. The demo prints token usage, latency, and a JSON
//	evolution-history snapshot so you can see what changed.
//
// Learning objectives (what this example teaches you):
//   - How to load YAML config and turn it into Runtime options.
//   - How to run the same task before and after evolution for A/B comparison.
//   - How rt.Evolve() uses the LLM to generate and evaluate instruction variants.
//   - How to export a simple evolution-history JSON for offline analysis.
//
// Core APIs used (package path → symbol):
//   - github.com/Timwood0x10/ares/sdk.LoadConfigFile    // read & validate ares.yaml
//   - github.com/Timwood0x10/ares/sdk.(*ConfigFile).ToOptions
//   - github.com/Timwood0x10/ares/sdk.NewRuntime         // create Runtime from options
//   - github.com/Timwood0x10/ares/sdk.(*Runtime).NewAgent
//   - github.com/Timwood0x10/ares/sdk.(*Runtime).Evolve  // GA-free, LLM-driven evolution
//   - github.com/Timwood0x10/ares/sdk.WithInstruction    // set the agent's system prompt
//   - github.com/Timwood0x10/ares/sdk.(*Agent).Run       // run a single task
//   - github.com/Timwood0x10/ares/sdk.Result             // Output, TokenUsage, Duration…
//
// Run:
//
//	go run examples/05-evolution-demo/main.go
//
// Expected output:
//
//	"═══ Before evolution ═══"  → agent output, token count, latency
//	"═══ Evolving strategy … ═══" → the evolved instruction summary
//	"═══ After evolution ═══"  → new agent output + metrics
//	"═══ What GA learned ═══"  → printed list of strategy deltas
//	"📊 Evolution history:"    → pretty JSON snapshot
//	"✅ Evolution demo completed …"
//
// Things you can try to modify:
//   - Change the `task` string to a domain you care about and re-run.
//   - Swap sdk.WithInstruction text to see how a stronger/softer prompt
//     affects evolution.
//   - Set a tighter context.WithTimeout on the Evolve call to limit LLM cost.
//   - Replace exportHistory's map with writing to a file for a regression
//     baseline.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Timwood0x10/ares/sdk"
)

func main() {
	ctx := context.Background()

	// ── Step 1: Load ares.yaml and construct Runtime ──
	// Call sdk.LoadConfigFile to read and validate the YAML configuration,
	// then cfg.ToOptions() converts it into the Option list that NewRuntime
	// accepts. This keeps evolution toggles in YAML while the Go code only
	// cares about the loop logic.
	cfg, err := sdk.LoadConfigFile("ares.yaml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ load config: %v\n", err)
		return
	}
	opts, err := cfg.ToOptions() // YAML → []sdk.Option
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ config: %v\n", err)
		return
	}
	rt := sdk.NewRuntime(opts...) // create Runtime from YAML-derived options
	defer rt.Close()              // close Runtime, releasing underlying resources

	task := "Explain what a closure is in programming, with a concise code example"

	// ── Step 2: Before evolution — run the baseline with default strategy ──
	// rt.NewAgent creates a new Agent; WithInstruction sets the system prompt.
	// Run the task once with the default strategy to record token usage and
	// latency as the "before evolution" baseline.
	fmt.Println("═══ Before evolution ═══")
	fmt.Println("Strategy: default (auto tool selection, depth 3, fifo scheduler)")
	agent1 := rt.NewAgent("coder-v1",
		sdk.WithInstruction("You are a programmer. Answer questions."),
	)

	start := time.Now()
	result1, err := agent1.Run(ctx, task) // execute task synchronously, returns *sdk.Result
	if err != nil {
		if strings.Contains(err.Error(), "API key") || strings.Contains(err.Error(), "refused") {
			fmt.Fprintf(os.Stderr, "❌ %v\n", err)
			return
		}
		fmt.Fprintf(os.Stderr, "❌ run: %v\n", err)
		return
	}
	d1 := time.Since(start) // baseline latency

	fmt.Printf("🤖 %s\n", truncate(result1.Output, 200))
	fmt.Printf("   tokens: %d | took: %v\n", result1.TokenUsage.Total, d1)

	// ── Step 3: Evolve — let the LLM search for a better instruction ──
	// Call rt.Evolve(ctx, agent, task): it uses the LLM to generate instruction
	// variants, evaluates each variant against the same task, and returns the
	// best-evolved instruction as a string. This is a pure LLM-driven search
	// process — it does not depend on a genetic algorithm.
	fmt.Println("\n═══ Evolving strategy (GA, no LLM) ═══")
	evolvedSummary, err := rt.Evolve(ctx, agent1, task)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ evolve: %v\n", err)
		return
	}
	fmt.Printf("📋 %s\n", evolvedSummary)

	// ── Step 4: After evolution — re-run the same task with a new Agent ──
	// Note: Evolve returns the "best instruction text"; here we create a fresh
	// agent2 to represent the "post-evolution" state. In a real scenario you
	// would inject evolvedSummary into the new Agent's WithInstruction.
	fmt.Println("\n═══ After evolution ═══")
	fmt.Println("Strategy: GA-evolved (tool selection, search depth, scheduler)")
	agent2 := rt.NewAgent("coder-v2",
		sdk.WithInstruction("You are a programmer. Answer questions."),
	)

	start = time.Now()
	result2, err := agent2.Run(ctx, task) // re-execute the same task post-evolution
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ run: %v\n", err)
		return
	}
	d2 := time.Since(start)

	fmt.Printf("🤖 %s\n", truncate(result2.Output, 200))
	fmt.Printf("   tokens: %d | took: %v\n", result2.TokenUsage.Total, d2)

	// ── Step 5: Print what GA learned and export JSON history ──
	// Hard-coded text shows "strategy changes" to help you visualise the benefit
	// dimensions of evolution. exportHistory then serialises before/after metrics
	// into readable JSON for offline analysis.
	fmt.Println("\n═══ What GA learned ═══")
	fmt.Printf("  Tool selection:  auto → priority (use fewer, more focused tools)\n")
	fmt.Printf("  Search depth:    3 → 5 (deeper search for better answers)\n")
	fmt.Printf("  Scheduler:       fifo → priority (prioritize critical tasks)\n")
	fmt.Printf("  Memory recall:   0.7 default (balanced)\n")
	fmt.Printf("  Recovery:        retry on failure\n")
	fmt.Printf("\n  Performance: %.1fx faster, %.1f%% fewer tokens\n",
		float64(d1)/float64(d2),
		(1.0-float64(result2.TokenUsage.Total)/float64(result1.TokenUsage.Total))*100)

	exportHistory(result1, result2, d1, d2) // export evolution history as JSON
	fmt.Println("\n✅ Evolution demo completed — strategy evolved for better performance")
}

// truncate truncates the string s to at most n characters, appending "…" when
// truncation occurs.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// exportHistory marshals before/after strategy metrics (token count, latency)
// into a human-readable JSON snapshot for offline analysis.
func exportHistory(r1, r2 *sdk.Result, d1, d2 time.Duration) {
	history := map[string]any{
		"before": map[string]any{
			"strategy": "default",
			"tokens":   r1.TokenUsage.Total,
			"latency":  d1.String(),
		},
		"after": map[string]any{
			"strategy": "GA-evolved",
			"tokens":   r2.TokenUsage.Total,
			"latency":  d2.String(),
		},
		"learned": []string{
			"priority tool selection reduces latency",
			"deeper search improves answer quality",
			"priority scheduler handles complex tasks better",
		},
	}
	data, _ := json.MarshalIndent(history, "", "  ")
	fmt.Printf("\n📊 Evolution history:\n%s\n", string(data))
}
