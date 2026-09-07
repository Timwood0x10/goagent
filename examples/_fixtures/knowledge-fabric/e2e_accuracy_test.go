package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/compiler"
	"github.com/Timwood0x10/ares/internal/knowledge/planner"
	"github.com/Timwood0x10/ares/internal/knowledge/runtime"
)

type evalCase struct {
	name    string
	goal    string
	queries []string
	// keyFacts are the MUST-preserve facts that any correct answer needs.
	keyFacts []string
}

var evalCases = []evalCase{
	{
		name: "workflow-dag",
		goal: "ARES 工作流引擎设计，DAG 增强，CheckpointStore",
		queries: []string{
			"ARES 有几套工作流系统？它们是什么？",
			"CheckpointStore 支持哪些后端？",
		},
		keyFacts: []string{
			"两套", "Workflow Engine", "Graph System",
			"PostgreSQL", "SQLite", "Redis",
			"DAG", "Step",
		},
	},
	{
		name: "memory-distillation",
		goal: "记忆蒸馏和向量检索",
		queries: []string{
			"记忆蒸馏有几层架构？分别是什么？",
			"RankingService 如何计算最终得分？",
		},
		keyFacts: []string{
			"三层", "Raw", "Normalizer", "Summary",
			"SemanticScore", "UsageBoost", "RecencyBoost", "Score",
		},
	},
	{
		name: "tool-calling",
		goal: "工具调用四条路径和 MCP 协议",
		queries: []string{
			"ARES 支持哪四条工具调用路径？",
			"Planner 兜底策略是什么？",
		},
		keyFacts: []string{
			"四条", "LLM", "Planner", "Graph", "MCP",
			"Bridge.Execute", "Planner.Plan", "executeStepWithFallback",
		},
	},
	{
		name: "evolution",
		goal: "遗传算法进化管线和 DreamCycle",
		queries: []string{
			"遗传算法有哪些变异操作？",
			"DreamCycle 如何防止并发执行？",
		},
		keyFacts: []string{
			"参数扰动", "结构变异", "Prompt重写", "工具重选", "策略替换",
			"runMu", "QuickReject", "FullEval",
		},
	},
	{
		name: "chaos",
		goal: "混沌工程故障注入和鲁棒性评估",
		queries: []string{
			"Arena 支持多少种故障注入？有哪些模式？",
			"Resurrection 的退避策略是什么？",
		},
		keyFacts: []string{
			"13种", "Survival", "Scenario",
			"指数退避", "1→30s", "5",
		},
	},
}

// TestAKFEndToEndAccuracy measures how well the AKF pipeline preserves
// critical facts compared to dumping all raw content directly into the prompt.
// Run: go test -run TestAKFEndToEndAccuracy -v ./examples/knowledge-fabric/
func TestAKFEndToEndAccuracy(t *testing.T) {
	ctx := context.Background()

	// ── Build corpus and pipeline ──────────────────────
	objects, reg, _ := generateLargeCorpus(5000)

	pipeline := knowledge.NewKnowledgePipeline(nil, nil, nil, nil)
	qp := &simpleQueryPlanner{}
	sd := planner.NewSourceDiscovery(reg, qp)
	planr := planner.NewKnowledgePlanner()

	rt := runtime.New(
		planr, sd, reg, pipeline,
		[]runtime.Linker{&runtime.DefaultLinker{}},
		[]runtime.Reducer{&runtime.DefaultReducer{}},
	)

	comp := compiler.NewDefaultCompiler()

	// ── Evaluate each case ─────────────────────────────
	fmt.Println("\n═══ AKF End-to-End Accuracy Assessment ═══")
	fmt.Println("Corpus: 5,000 KnowledgeObjects")

	var totalRawFacts, totalAKFFacts int
	var totalRawScore, totalAKFScore float64

	for _, c := range evalCases {
		t.Logf("Evaluating: %s", c.name)

		// ── AKF pipeline output ─────────────────────────
		graph, err := rt.Execute(ctx, c.goal,
			knowledge.TokenBudget{MaxTokens: 4000, ForGraph: 2500},
			&runtime.Config{MaxConcurrentProviders: 5},
		)
		if err != nil {
			t.Errorf("execute %q failed: %v", c.name, err)
			continue
		}

		compiled, err := comp.Compile(ctx, graph, compiler.CompileConfig{
			Formats: []compiler.Format{compiler.FormatPrompt},
		})
		if err != nil {
			t.Errorf("compile %q failed: %v", c.name, err)
			continue
		}
		akfOutput := compiled.Formats[compiler.FormatPrompt]

		// ── Raw context: find relevant objects ─────────
		var rawBuilder strings.Builder
		rawTokens := 0
		var relevantObjs []*knowledge.KnowledgeObject
		for _, obj := range objects {
			// Match by goal keywords (simple relevance filter).
			if isRelevant(obj, c.goal) {
				relevantObjs = append(relevantObjs, obj)
				content := string(obj.Raw)
				if content == "" {
					content = obj.Summary
				}
				fmt.Fprintf(&rawBuilder, "=== %s (%s) ===\n%s\n\n", obj.ID, obj.Type, content)
				rawTokens += estimateTokens(content)
			}
		}
		rawOutput := rawBuilder.String()

		// ── Score key facts ─────────────────────────────
		rawScore := scoreFacts(rawOutput, c.keyFacts)
		akfScore := scoreFacts(akfOutput, c.keyFacts)

		fmt.Printf("\n  %s (%d relevant articles):\n", c.name, len(relevantObjs))
		fmt.Printf("    Raw context:     %5d tokens, facts %2d/%-2d (%5.1f%%)\n",
			rawTokens, rawScore.found, rawScore.total, rawScore.pct)
		fmt.Printf("    AKF context:     %5d tokens, facts %2d/%-2d (%5.1f%%)\n",
			compiled.Metrics.OutputTokens, akfScore.found, akfScore.total, akfScore.pct)
		fmt.Printf("    Token reduction: %5.1f×\n", float64(rawTokens)/float64(compiled.Metrics.OutputTokens))

		totalRawFacts += rawScore.found
		totalAKFFacts += akfScore.found
		totalRawScore += rawScore.pct
		totalAKFScore += akfScore.pct
	}

	// ── Final summary ──────────────────────────────────
	fmt.Println("\n═══ Overall Results ═══")
	fmt.Printf("Raw  fact preservation:  %5.1f%%\n", totalRawScore/float64(len(evalCases)))
	fmt.Printf("AKF  fact preservation:  %5.1f%%\n", totalAKFScore/float64(len(evalCases)))
	fmt.Printf("Difference:              %+5.1f%%\n",
		totalAKFScore/float64(len(evalCases))-totalRawScore/float64(len(evalCases)))
}

type factScore struct {
	found int
	total int
	pct   float64
}

func scoreFacts(output string, facts []string) factScore {
	outputLower := strings.ToLower(output)
	found := 0
	for _, fact := range facts {
		if strings.Contains(outputLower, strings.ToLower(fact)) {
			found++
		}
	}
	return factScore{
		found: found,
		total: len(facts),
		pct:   float64(found) / float64(len(facts)) * 100,
	}
}

// isRelevant checks whether a KnowledgeObject is relevant to a goal by
// matching its tags and type against the goal keywords.
func isRelevant(obj *knowledge.KnowledgeObject, goal string) bool {
	goalLower := strings.ToLower(goal)
	for _, tag := range obj.Tags {
		if strings.Contains(goalLower, strings.ToLower(tag)) {
			return true
		}
	}
	domainKeyword := map[string][]string{
		"workflow": {"workflow", "dag", "step", "checkpoint"},
		"memory":   {"memory", "distill", "vector", "retriev"},
		"tool":     {"tool", "mcp", "planner", "execut"},
		"evol":     {"evol", "genome", "mutation", "dream", "ga"},
		"chaos":    {"chaos", "arena", "resilience", "fault"},
		"db":       {"storage", "postgres", "sql", "pool"},
		"sec":      {"security", "auth", "rbac", "permission"},
		"api":      {"api", "http", "rest", "endpoint"},
	}
	for _, tags := range domainKeyword {
		for _, kw := range tags {
			if strings.Contains(goalLower, kw) {
				// Check if object has a tag matching this domain.
				for _, tag := range obj.Tags {
					if strings.Contains(kw, tag) || strings.Contains(tag, strings.Split(kw, "_")[0]) {
						return true
					}
				}
			}
		}
	}
	return false
}
