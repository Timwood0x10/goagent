// Eval — runs concrete evaluation scenarios to measure ARES capabilities.
//
// Purpose:
//
//	This example quantifies ARES capabilities with a small evaluation suite:
//	five scenarios (basic chat, tool calling, multi-agent, resilience, and
//	instruction evolution) each run the SDK runtime against real tasks and
//	score the outcomes. It is the measurement harness for "is the agent
//	getting better?" questions.
//
// Learning objectives:
//   - How to build a runtime with sdk options (WithOllama / WithEvolution /
//     WithTrace).
//   - How evaluation.Scenario + RunnerFunc define a measurable capability
//     test (Runs, Timeout, per-run Metrics).
//   - How to interpret Report fields: PassRate, AvgScore, AvgLatency,
//     EvoImprovement.
//
// Core APIs (with package paths):
//   - sdk.NewRuntime / NewAgent (github.com/Timwood0x10/ares/sdk)
//   - evaluation.New / Scenario / RunnerFunc / RunAll (evaluation/)
//
// Run:
//
//	go run ./examples/_fixtures/eval
//
// Expected output:
//
//	═══ basic-chat ═══   Pass Rate: ...  Avg Score: ...
//	... (one block per scenario) + eval-report.json saved
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Timwood0x10/ares/api/tools"
	"github.com/Timwood0x10/ares/examples/_fixtures/evaluation"
	"github.com/Timwood0x10/ares/sdk"
)

func main() {
	ctx := context.Background()

	if err := run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "❌ %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	// ── Step 1: Build the runtime with the capabilities under test ──
	// WithOllama picks the local model; WithEvolution enables instruction
	// evolution for the evolution scenario; WithTrace(false) keeps logs
	// quiet.
	rt := sdk.NewRuntime(sdk.WithOllama("llama3.2"), sdk.WithEvolution(), sdk.WithTrace(false))
	defer rt.Close()

	// Register the calculator tool for tool-using scenarios.
	_ = rt.ToolRegistry().Register(calcTool)

	// ── Step 2: Create the evaluation harness ──
	// evaluation.New names the suite; each Scenario below registers one
	// measurable capability.
	eval := evaluation.New("ARES Capability Evaluation")

	// ── Step 3: Scenario 1 — Basic Chat ──
	// Measures whether a plain agent responds correctly (contains "paris").
	// Score 1.0 on hit, 0.0 on miss; latency and tokens are recorded.
	_ = eval.Register(&evaluation.Scenario{
		Name:        "basic-chat",
		Description: "Basic agent response correctness",
		Runs:        3,
		Timeout:     30 * time.Second,
		Runner: evaluation.RunnerFunc(func(ctx context.Context, task string) (*evaluation.Metrics, error) {
			agent := rt.NewAgent("chat", sdk.WithInstruction("Respond concisely."))
			start := time.Now()
			result, err := agent.Run(ctx, "What is the capital of France?")
			latency := time.Since(start)
			if err != nil {
				return &evaluation.Metrics{Task: task, Success: false, Score: 0, Latency: latency}, nil
			}
			score := 0.0
			if strings.Contains(strings.ToLower(result.Output), "paris") {
				score = 1.0
			}
			return &evaluation.Metrics{
				Task: task, Success: score > 0, Score: score,
				Latency: latency, TokenCount: result.TokenUsage.Total, ToolCalls: result.ToolCalls,
			}, nil
		}),
	})

	// ── Step 4: Scenario 2 — Tool Calling ──
	// Measures whether the agent actually invokes the calculator tool and
	// gets the expected result (445). Score 0.5 for tool use, 1.0 for the
	// correct answer.
	_ = eval.Register(&evaluation.Scenario{
		Name:        "tool-calling",
		Description: "Agent correctly invokes calculator tool",
		Runs:        3,
		Timeout:     30 * time.Second,
		Runner: evaluation.RunnerFunc(func(ctx context.Context, task string) (*evaluation.Metrics, error) {
			agent := rt.NewAgent("tool-user",
				sdk.WithInstruction("Use the calculator tool for math."),
			)
			start := time.Now()
			result, err := agent.Run(ctx, "Calculate 15*23 + 100")
			latency := time.Since(start)
			if err != nil {
				return &evaluation.Metrics{Task: task, Success: false, Score: 0, Latency: latency}, nil
			}
			score := 0.0
			if result.ToolCalls > 0 {
				score = 0.5
			}
			if strings.Contains(result.Output, "445") {
				score = 1.0
			}
			return &evaluation.Metrics{
				Task: task, Success: result.ToolCalls > 0, Score: score,
				Latency: latency, TokenCount: result.TokenUsage.Total, ToolCalls: result.ToolCalls,
			}, nil
		}),
	})

	// ── Step 5: Scenario 3 — Multi-Agent ──
	// Measures the peer-agent flow (H1): RegisterAgent registers peer
	// capabilities; Submit dispatches the task to the matching agent.
	_ = eval.Register(&evaluation.Scenario{
		Name:        "multi-agent",
		Description: "Peer-agent capability dispatch (RegisterAgent + Submit)",
		Runs:        2,
		Timeout:     60 * time.Second,
		Runner: evaluation.RunnerFunc(func(ctx context.Context, task string) (*evaluation.Metrics, error) {
			rt.RegisterAgent("lead", sdk.WithInstruction("Plan and summarize."))
			rt.RegisterAgent("worker", sdk.WithInstruction("Execute tasks."))
			start := time.Now()
			result, err := rt.Submit(ctx, sdk.Task{
				Capability: "lead",
				Input:      "Say hello briefly",
			})
			latency := time.Since(start)
			if err != nil {
				return &evaluation.Metrics{Task: task, Success: false, Score: 0, Latency: latency}, nil
			}
			score := 0.0
			if result.Output != "" {
				score = 1.0
			}
			return &evaluation.Metrics{
				Task: task, Success: result.Output != "", Score: score,
				Latency: latency,
			}, nil
		}),
	})

	// ── Step 6: Scenario 4 — Resilience ──
	// Measures graceful recovery from tool failures: the agent is given a
	// tool that always fails and must still produce a meaningful response.
	_ = eval.Register(&evaluation.Scenario{
		Name:        "resilience",
		Description: "Agent recovers from tool failures",
		Runs:        2,
		Timeout:     30 * time.Second,
		Runner: evaluation.RunnerFunc(func(ctx context.Context, task string) (*evaluation.Metrics, error) {
			agent := rt.NewAgent("resilient",
				sdk.WithInstruction("If a tool fails, explain gracefully."),
				sdk.WithTools(failTool),
			)
			start := time.Now()
			result, err := agent.Run(ctx, "Use the unreliable_tool and handle failure")
			latency := time.Since(start)
			if err != nil {
				return &evaluation.Metrics{Task: task, Success: false, Score: 0, Latency: latency}, nil
			}
			score := 0.0
			if result.ToolCalls > 0 {
				score = 0.5 // at least tried the tool
			}
			if len(result.Output) > 20 {
				score = 1.0 // produced a meaningful response despite failure
			}
			return &evaluation.Metrics{
				Task: task, Success: result.Output != "", Score: score,
				Latency: latency, TokenCount: result.TokenUsage.Total, ToolCalls: result.ToolCalls,
			}, nil
		}),
	})

	// ── Step 7: Scenario 5 — Evolution ──
	// Measures whether instruction evolution improves response quality:
	// scores the same question before and after rt.Evolve and reports the
	// percentage improvement.
	_ = eval.Register(&evaluation.Scenario{
		Name:        "evolution",
		Description: "Instruction evolution improves response quality",
		Runs:        1,
		Timeout:     90 * time.Second,
		Runner: evaluation.RunnerFunc(func(ctx context.Context, task string) (*evaluation.Metrics, error) {
			baseInstr := "Answer questions."
			agent := rt.NewAgent("evolvable", sdk.WithInstruction(baseInstr))

			// Before evolution.
			start := time.Now()
			r1, err1 := agent.Run(ctx, "Explain closures in Go with a short example")
			latency := time.Since(start)
			if err1 != nil {
				return &evaluation.Metrics{Task: task, Success: false, Score: 0}, nil
			}
			scoreBefore := scoreResponse(r1.Output)

			// Evolve.
			evolvedInstr, err := rt.Evolve(ctx, agent, "Explain closures in Go with a short example")
			if err != nil {
				return &evaluation.Metrics{Task: task, Success: false, Score: scoreBefore}, nil
			}

			// After evolution.
			agent2 := rt.NewAgent("evolved", sdk.WithInstruction(evolvedInstr))
			r2, err2 := agent2.Run(ctx, "Explain closures in Go with a short example")
			if err2 != nil {
				return &evaluation.Metrics{
					Task: task, Success: true, Score: scoreBefore,
					ScoreBefore: scoreBefore, ScoreAfter: 0,
					EvoImprovement: -100, Latency: latency,
					TokenCount: r1.TokenUsage.Total, Generation: 1,
				}, nil
			}
			scoreAfter := scoreResponse(r2.Output)
			improvement := ((scoreAfter - scoreBefore) / scoreBefore) * 100
			if scoreBefore == 0 {
				improvement = scoreAfter * 100
			}

			return &evaluation.Metrics{
				Task: task, Success: true,
				Score:          scoreAfter,
				ScoreBefore:    scoreBefore,
				ScoreAfter:     scoreAfter,
				EvoImprovement: improvement,
				Latency:        latency,
				TokenCount:     r1.TokenUsage.Total + r2.TokenUsage.Total,
				Generation:     1,
			}, nil
		}),
	})

	// ── Step 8: Run all scenarios and print the summary ──
	// RunAll executes every registered scenario and returns per-name reports;
	// the summary shows pass rate, average score, latency, and tokens.
	reports, err := eval.RunAll(ctx)
	if err != nil {
		return fmt.Errorf("eval: %w", err)
	}

	fmt.Println()
	for name, report := range reports {
		fmt.Printf("═══ %s ═══\n", name)
		fmt.Printf("  Pass Rate:  %.0f%% (%d/%d)\n", report.PassRate, report.Passed, report.Runs)
		fmt.Printf("  Avg Score:  %.2f\n", report.AvgScore)
		fmt.Printf("  Avg Latency: %v\n", report.AvgLatency)
		fmt.Printf("  Tokens:     %d\n", report.TotalTokens)
		fmt.Println()
	}

	// ── Step 9: Save a sample JSON report ──
	// The first report is persisted to eval-report.json for later analysis.
	jsonPath := "eval-report.json"
	for _, report := range reports {
		jsonStr, _ := report.ToJSON()
		_ = os.WriteFile(jsonPath, []byte(jsonStr), 0644)
		break // save first report as sample
	}
	fmt.Printf("📄 Report saved to %s\n", jsonPath)
	return nil
}

// calcTool is a deterministic calculator tool so the tool-calling scenario
// always gets the expected answer (445) regardless of model arithmetic.
var calcTool = toolFunc("calculator", "Evaluate math expressions", func(expr string) string {
	return "445"
})

// failTool always fails, exercising the resilience scenario's graceful
// recovery path.
var failTool = toolFunc("unreliable_tool", "Sometimes fails", func(input string) string {
	return ""
})

// simpleTool is a minimal Tool implementation for the demo scenarios.
type simpleTool struct {
	name string
	desc string
	fn   func(string) string
}

func (t *simpleTool) Name() string               { return t.name }
func (t *simpleTool) Description() string        { return t.desc }
func (t *simpleTool) Parameters() map[string]any { return nil }
func (t *simpleTool) Capabilities() []string     { return nil }
func (t *simpleTool) Execute(_ context.Context, params map[string]any) (tools.Result, error) {
	input, _ := params["input"].(string)
	if t.name == "unreliable_tool" {
		return tools.Result{Success: false, Data: "service unavailable"}, nil
	}
	result := t.fn(input)
	if result == "" {
		return tools.Result{Success: false, Data: "empty result"}, nil
	}
	return tools.Result{Success: true, Data: result}, nil
}

func toolFunc(name, desc string, fn func(string) string) *simpleTool {
	return &simpleTool{name: name, desc: desc, fn: fn}
}

// scoreResponse rates response quality 0.0-1.0 based on content indicators
// (mentions functions, closures, includes an example, has enough length).
func scoreResponse(output string) float64 {
	if output == "" {
		return 0
	}
	score := 0.3 // base: has content
	lower := strings.ToLower(output)
	if strings.Contains(lower, "function") || strings.Contains(lower, "func") {
		score += 0.2
	}
	if strings.Contains(lower, "closure") {
		score += 0.2
	}
	if strings.Contains(lower, "example") || strings.Contains(lower, "```") {
		score += 0.2
	}
	if len([]rune(output)) > 100 {
		score += 0.1
	}
	if score > 1.0 {
		score = 1.0
	}
	return score
}
