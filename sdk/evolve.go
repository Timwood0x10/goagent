package sdk

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Timwood0x10/ares/api/tools"
	"github.com/Timwood0x10/ares/internal/logger"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// log is the package-level structured logger for the sdk package.
var log = logger.Module("sdk")

// Evolvable strategy parameter keys. Shared across base strategy creation,
// mutator ranges, scoring, and application so the dimension names stay
// in sync and the linter (goconst) stays quiet.
//
// Only dimensions with a direct Agent backing field are evolved:
//   - tool_selector → filters agent.tools
//   - search_depth  → sets agent.maxIter (deeper search = more ReAct iterations)
//
// The former scheduler_strategy, memory_threshold, and recovery_strategy
// dimensions were removed because they have no Agent-level backing field —
// they are kernel/runtime concepts, not agent inference parameters. Evolving
// dimensions that cannot be applied would be dishonest: the GA would search
// a space that has no effect on execution.
const (
	paramToolSelector = "tool_selector"
	paramSearchDepth  = "search_depth"
)

// Evolve runs an evolution cycle to improve an agent's instruction. It uses the
// LLM to generate variations, evaluates them against the given task, and returns
// the best-evolved instruction.
func (r *Runtime) Evolve(ctx context.Context, agent *Agent, task string) (string, error) {
	if agent == nil {
		return "", errors.New("evolve: agent is nil")
	}
	if !r.evoEnabled {
		return "", errors.New("evolution not enabled (use WithEvolution())")
	}

	if r.trace {
		log.Info("[ares:evolve] evolving agent on task", "name", agent.name, "task", task)
	}

	// Create base strategy with two actionable dimensions: tool selection
	// and search depth (ReAct iterations). Both have direct Agent backing
	// fields and are consumed during execution and application.
	base := &mutation.Strategy{
		ID:        fmt.Sprintf("sdk-%s", agent.name),
		Version:   1,
		Score:     -1,
		CreatedAt: time.Now(),
		Params: map[string]any{
			paramToolSelector: "auto", // auto / manual / priority
			paramSearchDepth:  3,      // 1-15: maps to maxIter (ReAct iterations)
		},
		PromptTemplate: agent.instruction,
	}

	// Create mutator for meaningful dimensions.
	mutator, err := mutation.NewMutator(
		mutation.WithParamRanges(evolvableParams()),
	)
	if err != nil {
		return "", fmt.Errorf("create mutator: %w", err)
	}

	// Create crossover operator (uses PyGAD-inspired operators).
	crosser, err := genome.NewCrossover(
		genome.WithSeed(42),
		genome.WithCrossoverType(genome.CrossoverUniform),
	)
	if err != nil {
		return "", fmt.Errorf("create crossover: %w", err)
	}

	// Create GA population.
	pop, err := genome.NewPopulation(ctx, base, mutator,
		genome.WithPopulationSize(10),
		genome.WithEliteCount(2),
		genome.WithMutationRate(0.3),
		genome.WithSurvivalRate(0.5),
		genome.WithSelectionStrategy("tournament"),
		genome.WithTournamentSelection(3),
	)
	if err != nil {
		return "", fmt.Errorf("create population: %w", err)
	}

	// Run evolution using actual execution as scorer (no LLM).
	scorer := func(s *mutation.Strategy) float64 {
		return executeAndScore(ctx, r, agent, task, s)
	}

	for gen := 0; gen < 3; gen++ {
		pop.ScoreAgents(scorer)
		if err := pop.Evolve(ctx, mutator, crosser); err != nil {
			return "", fmt.Errorf("evolve generation %d: %w", gen, err)
		}
	}

	// Get the best strategy.
	best := pop.BestStrategy()
	if best == nil {
		return "", errors.New("evolution produced no viable strategy")
	}

	if r.trace {
		stats := pop.Stats()
		log.Info("[ares:evolve] GA evolution complete", "generation", stats.Generation, "best_score", stats.BestScore, "avg_score", stats.AvgScore, "params", best.Params)
	}

	// Apply the evolved strategy's params to the agent.
	applyEvolvedParams(agent, best.Params)

	// Return the best-evolved instruction: the base prompt template enriched
	// with the evolved strategy parameters, so callers can apply the evolved
	// configuration to a new agent via WithInstruction.
	return buildEvolvedInstruction(agent.instruction, best), nil
}

// buildEvolvedInstruction composes the base instruction with the evolved
// strategy parameters into a single instruction string. It returns the base
// instruction unchanged when the strategy is nil or carries no parameters.
func buildEvolvedInstruction(base string, s *mutation.Strategy) string {
	if s == nil {
		return base
	}
	params := []struct {
		key string
		v   any
	}{
		{paramToolSelector, s.Params[paramToolSelector]},
		{paramSearchDepth, s.Params[paramSearchDepth]},
	}
	instruction := base + "\n\nEvolved strategy:"
	for _, p := range params {
		if p.v != nil {
			instruction += fmt.Sprintf("\n- %s: %v", p.key, p.v)
		}
	}
	return instruction
}

// evolvableParams returns the parameter ranges for meaningful evolution dimensions.
func evolvableParams() map[string]mutation.ParamRange {
	return map[string]mutation.ParamRange{
		paramToolSelector: {Values: []any{"auto", "manual", strategyPriority}},
		// Candidate depths span the default agent budget (defaultMaxIterations
		// = 10): evolving only values below 10 would permanently shrink the
		// budget below the default for every evolved agent.
		paramSearchDepth: {Values: []any{1, 3, 5, 8, 10, 15}},
	}
}

// executeAndScore runs the task with a given strategy and scores based on
// actual execution results: success, latency, and token efficiency.
// No LLM involved — pure execution-based evaluation.
func executeAndScore(ctx context.Context, r *Runtime, agent *Agent, task string, s *mutation.Strategy) float64 {
	evolvedAgent := &Agent{
		name:        agent.name,
		instruction: s.PromptTemplate,
		tools:       applyToolSelector(agent.tools, s.Params),
		runtime:     agent.runtime,
		humanInput:  agent.humanInput,
		maxIter:     applySearchDepth(agent.maxIter, s.Params),
		discovery:   agent.discovery,
		toolSource:  agent.toolSource,
		selector:    agent.selector,
	}

	start := time.Now()
	result, err := evolvedAgent.Run(ctx, task)
	duration := time.Since(start)

	if err != nil {
		log.Warn("[ares:evolve] execution failed", "err", err)
		return 10.0
	}

	successBonus := 50.0
	if result != nil && result.Output != "" {
		successBonus = 60.0
	}

	speedScore := 30.0 * (1.0 - min(1.0, duration.Seconds()/30.0))

	efficiencyScore := 10.0
	if result != nil && result.TokenUsage.Total > 0 {
		efficiencyScore = 20.0 * (1.0 - min(1.0, float64(result.TokenUsage.Total)/2000.0))
	}

	return successBonus + speedScore + efficiencyScore
}

// applyToolSelector filters the agent's tool list based on the strategy.
func applyToolSelector(toolList []tools.Tool, params map[string]any) []tools.Tool {
	selector, _ := params[paramToolSelector].(string)
	switch selector {
	case "priority":
		if len(toolList) > 3 {
			return toolList[:3]
		}
		return toolList
	case "manual":
		var filtered []tools.Tool
		for _, t := range toolList {
			if t.Name() == "search" || t.Name() == "read" {
				filtered = append(filtered, t)
			}
		}
		if len(filtered) > 0 {
			return filtered
		}
		return toolList
	default:
		return toolList
	}
}

// applyEvolvedParams applies the evolved strategy params to the agent.
// Both dimensions have direct Agent backing fields: tool_selector filters
// agent.tools, and search_depth sets agent.maxIter (deeper search = more
// ReAct iterations). There are no unwired dimensions — all evolved params are
// consumed.
func applyEvolvedParams(agent *Agent, params map[string]any) {
	if v, ok := params[paramToolSelector]; ok {
		if selector, isString := v.(string); isString {
			agent.tools = applyToolSelector(agent.tools, map[string]any{paramToolSelector: selector})
			log.Info("[ares:evolve] applied tool_selector (tools after filtering)", "selector", selector, "count", len(agent.tools))
		}
	}
	agent.maxIter = applySearchDepth(agent.maxIter, params)
	if v, ok := params[paramSearchDepth]; ok {
		log.Info("[ares:evolve] applied search_depth", "depth", v, "max_iter", agent.maxIter)
	}
}

// applySearchDepth maps the search_depth evolution dimension (1-15) to
// agent.maxIter. When the agent already has an explicit maxIter (>0), the
// evolved depth overrides it; when depth is absent or invalid, the original
// maxIter is preserved. The candidate range includes the default budget (10)
// so evolution can preserve or grow the budget instead of only shrinking it.
func applySearchDepth(currentMaxIter int, params map[string]any) int {
	v, ok := params[paramSearchDepth]
	if !ok {
		return currentMaxIter
	}
	depth, isInt := v.(int)
	if !isInt || depth < 1 {
		return currentMaxIter
	}
	return depth
}
