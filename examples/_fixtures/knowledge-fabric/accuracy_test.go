package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/compiler"
	"github.com/Timwood0x10/ares/internal/knowledge/planner"
	"github.com/Timwood0x10/ares/internal/knowledge/runtime"
)

// keyFacts defines the critical facts per article that MUST be preserved
// in the AKF compiled output for the pipeline to be "accurate enough".
// Each fact is a short phrase that should appear in the Summary or output.
var keyFacts = map[string][]string{
	"wf-engine": {
		"两套工作流系统", "Workflow Engine", "Graph System",
		"配置驱动", "YAML", "热重载", "HITL",
		"代码驱动", "Fluent Builder", "条件边",
	},
	"wf-dag": {
		"两套", "配置驱动", "灵活性不够", "动态加减节点",
		"条件边", "可插拔调度器",
	},
	"mem-distill": {
		"Memory Distillation", "原始数据", "Raw",
		"标准化层", "Normalizer", "摘要层", "Summary",
		"可复用的经验",
	},
	"tool-calling": {
		"四条路径", "Path1", "LLM驱动", "parseToolCalls",
		"Path2", "Planner兜底", "语义分析",
		"Path3", "Workflow Graph", "ToolNode",
		"Path4", "MCP", "确定性引擎兜底",
	},
	"dag-conditional": {
		// 0.3.1 真相口径：条件跳过/动态路由由 Graph System（条件边 +
		// SetRouter）承担；受控循环由 Kernel 侧 LoopPlugin 轮次节拍承担；
		// 引擎侧的 ConditionFunc/NodeRouter/LoopConfig 已删除。关键词与
		// 文案同步收敛，禁止再引用已删除的引擎符号。
		//
		// 子图嵌套（Step.SubWorkflow）同样已删除：该字段从未有执行器消费，
		// graph 侧也没有任何子图实现，属于与 LoopConfig 同性质的死声明。
		"条件跳过", "动态路由", "条件边",
		"受控循环", "LoopPlugin",
	},
	"dag-checkpoint": {
		"检查点", "WithCheckpointStore", "StepResult",
		"PostgreSQL", "SQLite", "Redis",
		"非阻塞", "executed", "State",
	},
}

// TestAKFAccuracy measures content preservation across the AKF pipeline.
// Run: go test -run TestAKFAccuracy -v ./examples/knowledge-fabric/
func TestAKFAccuracy(t *testing.T) {
	ctx := context.Background()
	reg := buildRegistry()
	objects := allObjects()

	// ── Build AKF pipeline ─────────────────────────────
	pipeline := knowledge.NewKnowledgePipeline(nil, nil, nil, nil)
	qp := &simpleQueryPlanner{}
	sd := planner.NewSourceDiscovery(reg, qp)
	planr := planner.NewKnowledgePlanner()

	rt := runtime.New(
		planr, sd, reg, pipeline,
		[]runtime.Linker{&runtime.DefaultLinker{}},
		[]runtime.Reducer{&runtime.DefaultReducer{}},
	)

	graph, err := rt.Execute(ctx,
		"ARES 工作流引擎，DAG，记忆蒸馏，工具调用，混沌工程，进化",
		knowledge.TokenBudget{MaxTokens: 5000, ForGraph: 3000},
		&runtime.Config{MaxConcurrentProviders: 5},
	)
	if err != nil {
		t.Fatal(err)
	}

	comp := compiler.NewDefaultCompiler()
	compiled, err := comp.Compile(ctx, graph, compiler.CompileConfig{
		Formats: []compiler.Format{compiler.FormatPrompt},
	})
	if err != nil {
		t.Fatal(err)
	}
	output := compiled.Formats[compiler.FormatPrompt]

	// ── Accuracy measurements ──────────────────────────

	fmt.Println("\n═══ AKF Content Accuracy Report ═══")
	fmt.Println()

	// 1. Per-article key fact retention
	var totalFacts, preservedFacts int
	fmt.Printf("%-30s %s\n", "Article", "Fact Retention")
	fmt.Println(strings.Repeat("─", 65))
	for _, obj := range objects {
		facts := keyFacts[obj.ID]
		if len(facts) == 0 {
			continue
		}
		preserved := 0
		for _, fact := range facts {
			if containsCI(output, fact) {
				preserved++
			}
		}
		pct := float64(preserved) / float64(len(facts)) * 100
		totalFacts += len(facts)
		preservedFacts += preserved
		fmt.Printf("%-30s %2d/%-2d (%5.1f%%)\n", obj.ID, preserved, len(facts), pct)
	}
	fmt.Println(strings.Repeat("─", 65))
	overallPct := float64(preservedFacts) / float64(totalFacts) * 100
	fmt.Printf("%-30s %2d/%-2d (%5.1f%%)  ← overall\n",
		"TOTAL", preservedFacts, totalFacts, overallPct)
	fmt.Println()

	// 2. Summary vs Raw: how much of the raw content is captured in Summary?
	fmt.Println("═══ Summary Fidelity (Raw → Summary compression) ═══")
	fmt.Printf("%-30s %12s %12s %12s\n", "Article", "Raw chars", "Summary chars", "Fidelity")
	fmt.Println(strings.Repeat("─", 70))
	var totalFidelity float64
	var fidelityCount int
	for _, obj := range objects {
		raw := rawArticles[obj.ID]
		if raw == "" || obj.Summary == "" {
			continue
		}
		rawTokens := estimateTokens(raw)
		sumTokens := estimateTokens(obj.Summary)
		// Measure content overlap: extract key nouns/terms from raw, check in summary
		rawTerms := extractKeyTerms(raw)
		matchCount := 0
		for _, term := range rawTerms {
			if containsCI(obj.Summary, term) {
				matchCount++
			}
		}
		termFidelity := float64(matchCount) / float64(len(rawTerms)) * 100
		if len(rawTerms) > 0 {
			totalFidelity += termFidelity
			fidelityCount++
		}
		fmt.Printf("%-30s %8d tok → %7d tok  (%5.1f%% terms preserved)\n",
			obj.ID, rawTokens, sumTokens, termFidelity)
	}
	avgFidelity := totalFidelity / float64(fidelityCount)
	fmt.Println(strings.Repeat("─", 70))
	fmt.Printf("Average Summary fidelity:                              %5.1f%%\n", avgFidelity)
	fmt.Println()

	// 4. Token efficiency vs accuracy tradeoff

	// 4. Token efficiency vs accuracy tradeoff
	naiveTokens := 0
	for _, obj := range objects {
		raw := rawArticles[obj.ID]
		if raw == "" {
			raw = obj.Summary
		}
		naiveTokens += estimateTokens(raw)
	}
	akfTokens := compiled.Metrics.OutputTokens
	if akfTokens <= 0 {
		akfTokens = estimateTokens(output)
	}

	fmt.Println("═══ Efficiency × Accuracy Tradeoff ═══")
	fmt.Printf("Tokens (naive):        %6d\n", naiveTokens)
	fmt.Printf("Tokens (AKF):          %6d\n", akfTokens)
	fmt.Printf("Token reduction:       %5.1f%%\n",
		float64(naiveTokens-akfTokens)/float64(naiveTokens)*100)
	fmt.Printf("Fact retention:        %5.1f%%\n", overallPct)
	fmt.Printf("Summary fidelity:      %5.1f%%\n", avgFidelity)
	fmt.Println()

	// Summary score
	fmt.Println("═══ Overall Assessment ═══")
	score := (overallPct + avgFidelity) / 2
	fmt.Printf("AKF Accuracy Score:    %5.1f%%\n", score)
	fmt.Printf("AKF Efficiency Score:  %5.1f%%\n",
		float64(naiveTokens-akfTokens)/float64(naiveTokens)*100)
	fmt.Printf("Combined Score:        %5.1f%%\n", (score+float64(naiveTokens-akfTokens)/float64(naiveTokens)*100)/2)
	fmt.Println()
}

// ── Helpers ───────────────────────────────────────

func containsCI(s, substr string) bool {
	return strings.Contains(
		strings.ToLower(s),
		strings.ToLower(substr),
	)
}

// extractKeyTerms pulls important technical terms from text.
func extractKeyTerms(s string) []string {
	// Collect technical patterns: CamelCase, ALL_CAPS, quoted terms, Chinese technical terms
	terms := make(map[string]bool)

	// CamelCase / PascalCase / ALL_CAPS identifiers
	re1 := regexp.MustCompile(`[A-Z][a-zA-Z0-9_]{2,}|[A-Z]{2,}`)
	for _, m := range re1.FindAllString(s, -1) {
		if !isStopWord(m) {
			terms[m] = true
		}
	}

	// Chinese multi-char key phrases (2-6 chars)
	re2 := regexp.MustCompile(`[\x{4e00}-\x{9fff}]{2,8}`)
	chinesePhrases := re2.FindAllString(s, -1)

	// Score Chinese phrases by frequency
	phraseFreq := make(map[string]int)
	for _, p := range chinesePhrases {
		phraseFreq[p]++
	}

	// Keep only phrases that appear frequently or are clearly technical
	for p, freq := range phraseFreq {
		if freq >= 2 || len(p) >= 4 {
			terms[p] = true
		}
	}

	result := make([]string, 0, len(terms))
	for t := range terms {
		result = append(result, t)
	}
	return result
}

// isStopWord filters out common English stop-words that slip through CamelCase matching.
func isStopWord(s string) bool {
	stops := map[string]bool{
		"THE": true, "AND": true, "FOR": true, "THAT": true, "WITH": true,
		"THIS": true, "FROM": true, "WILL": true, "CAN": true, "ARE": true,
		"HAS": true, "WAS": true, "NOT": true, "BUT": true, "YOU": true,
		"ALL": true, "ANY": true, "EACH": true, "MORE": true, "SOME": true,
		"WHEN": true, "WHAT": true, "WHICH": true, "INTO": true, "THAN": true,
	}
	return stops[s]
}
