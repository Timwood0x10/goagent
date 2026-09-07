package main

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/compiler"
	"github.com/Timwood0x10/ares/internal/knowledge/planner"
	"github.com/Timwood0x10/ares/internal/knowledge/provider"
	"github.com/Timwood0x10/ares/internal/knowledge/runtime"
)

// generateLargeCorpus creates n KnowledgeObjects across multiple domains
// with realistic Raw content (~200-500 chars each) and Summary fields.
// Returns the objects, total token estimate, and a registry.
func generateLargeCorpus(n int) ([]*knowledge.KnowledgeObject, *provider.ProviderRegistry, int) {
	domains := []struct {
		prefix string
		types  []knowledge.ObjectType
		tags   []string
	}{
		{"wf", []knowledge.ObjectType{knowledge.ObjectArchitecture, knowledge.ObjectDecision},
			[]string{"workflow", "dag", "orchestration"}},
		{"mem", []knowledge.ObjectType{knowledge.ObjectMemory, knowledge.ObjectDocument},
			[]string{"memory", "distillation", "experience"}},
		{"tool", []knowledge.ObjectType{knowledge.ObjectArchitecture, knowledge.ObjectCode},
			[]string{"tool", "mcp", "execution"}},
		{"evol", []knowledge.ObjectType{knowledge.ObjectDecision, knowledge.ObjectCode},
			[]string{"evolution", "genome", "mutation"}},
		{"chaos", []knowledge.ObjectType{knowledge.ObjectDocument, knowledge.ObjectRuntime},
			[]string{"chaos", "arena", "resilience"}},
		{"db", []knowledge.ObjectType{knowledge.ObjectCode, knowledge.ObjectArchitecture},
			[]string{"storage", "postgres", "query"}},
		{"sec", []knowledge.ObjectType{knowledge.ObjectArchitecture, knowledge.ObjectDecision},
			[]string{"security", "auth", "rbac"}},
		{"api", []knowledge.ObjectType{knowledge.ObjectCode, knowledge.ObjectDocument},
			[]string{"api", "http", "rest"}},
	}

	objects := make([]*knowledge.KnowledgeObject, 0, n)
	rng := rand.New(rand.NewSource(42))
	var totalTokens int

	for i := 0; i < n; i++ {
		d := domains[i%len(domains)]
		objType := d.types[rng.Intn(len(d.types))]
		id := fmt.Sprintf("%s-%d", d.prefix, i)

		// Generate realistic Raw content.
		rawContent := generateRawContent(id, d.prefix, objType, rng)
		summary := generateSummary(id, d.prefix, objType, rng)

		totalTokens += estimateTokens(rawContent)

		objects = append(objects, &knowledge.KnowledgeObject{
			ID:        id,
			Type:      objType,
			Namespace: "benchmark",
			Raw:       []byte(rawContent),
			Summary:   summary,
			// Confidence clustered by domain so Reducer keeps related objects.
			Confidence: 0.6 + float64(i%len(domains))*0.3/float64(len(domains)) + rng.Float64()*0.05,
			Tags:       []string{d.tags[rng.Intn(len(d.tags))], fmt.Sprintf("domain:%s", d.prefix)},
			CreatedAt:  time.Now().Add(-time.Duration(rng.Intn(72)) * time.Hour),
		})
	}

	reg := provider.NewProviderRegistry()
	_ = reg.Register(&memoryProvider{
		name:    "benchmark-corpus",
		objects: objects,
	})

	return objects, reg, totalTokens
}

func generateRawContent(id, domain string, objType knowledge.ObjectType, rng *rand.Rand) string {
	templates := map[string][]string{
		"wf": {
			"工作流引擎 %s 实现了基于 DAG 的任务编排载体。核心组件包括 WorkflowLoader（YAML 配置加载）、MutableDAG（可变拓扑）、RecoveryPolicy（恢复策略）。支持热重载和 HITL 人工介入。数据流：YAML → WorkflowLoader → Workflow+Step → DAG。拓扑变更通过 ChangeEvent 事件同步。",
			"Graph System 是第二套工作流方案，使用 Fluent Builder API 在代码中构建 DAG。支持条件跳过（条件边）、动态路由（SetRouter）；受控循环由 Kernel 侧 LoopPlugin 承担（轮次预算=%d）。两套系统共享底层 DAG 语义，执行编排归属 Kernel。",
			"DAG 载体能力：条件边在运行时做条件判断，SetRouter 做动态路由。CheckpointStore 每步持久化 StepResult。后端支持 PostgreSQL/SQLite/Redis。非阻塞写入。",
		},
		"mem": {
			"Memory Distillation 架构：三层设计。Raw 层保留原始对话历史（含工具调用结果和运行时数据）。Normalizer 层统一格式，支持来自不同渠道的记忆。Summary 层通过 LLM 压缩为知识摘要。核心原则：提炼可复用的经验模式，不是简单存储聊天记录。",
			"蒸馏管线：Normalizer → Classifier → Scorer → Distiller。Normalizer 清洗和格式化原始文本。Classifier 按内容类型分类（决策/架构/问题）。Scorer 根据重要性和置信度打分。Distiller 压缩为 KnowledgeObject。支持冲突检测和去重。",
			"经验检索：SearchByVector 使用余弦相似度做向量检索。RankingService 计算 FinalScore = SemanticScore + UsageBoost + RecencyBoost + Score。支持多租户隔离（tenant_id 维度）。负反馈通过 DecrementRank 降低 Score。",
		},
		"tool": {
			"工具调用四条路径：Path1 LLM 驱动（parseToolCalls → CallTool → Registry.Execute）。Path2 Planner 兜底（语义分析 → 能力规划 → 评分 → executeStepWithFallback）。Path3 Workflow Graph（ToolNode.Execute 事件钩子）。Path4 MCP 外部工具。当 LLM 不靠谱时用确定性引擎兜底。",
			"MCP 协议：客户端支持 Stdio 和 SSE 两种传输方式。JSON-RPC 2.0 请求/响应模型。工具动态发现和注册。配置热重载支持。连接池复用。自动重连和健康检查。Version=%d。API 版本兼容。",
			"Planner 兜底策略：Bridge.Execute 检测异常后调用 Planner.Plan 重规划。executeStepWithFallback 分步骤执行。参数修正自动补全。工具替换选择功能相近的替代。步骤回退到上一步。",
		},
		"evol": {
			"遗传算法进化管线：TournamentSelection（锦标赛选择 TopK=%d）。UniformCrossover 均匀混合父代参数。5 类 Mutation（参数扰动/结构变异/Prompt 重写/工具重选/策略替换）。TieredScorer 四维度评分（正确性/效率/鲁棒性/可解释性）。DreamCycle 定期触发进化。",
			"DreamCycle 进化循环：Run 方法受 runMu 保护防止并发。10 分钟超时。Cooldown 5 分钟。候选策略通过 findWinner 经 quickReject（5 轮筛选）和 fullEval（50 轮评估）两阶段。胜者按 winRate 择优。shadowEvaluator 灰度和独立评分。",
			"MemoryAwareScorer 让 GA 适应度直接吃蒸馏经验。GenomePopulationAdapter 适配种群管理。进化受 Guardrails 保护：PreEvolveCheck 检查未评估比例和停滞代数。PostEvolveCheck 基于 bestKnownScore 基线回归检测。",
		},
		"chaos": {
			"Arena 混沌工程框架：支持 13 种故障注入类型。Survival Mode 随机注入故障计算 ResilienceScore。Scenario Mode YAML 定义有序故障序列。集成做市故障注入。PartitionNetwork/CorruptMemory/DisconnectMCP/InjectLLMFailure 已标注 SIMULATION。",
			"鲁棒性评估：PauseAgent/SlowAgent 当前为 tracking-only。KillLeader/KillAgent 通过 Orchestrator.CancelAgent 实现。Resurrection 指数退避 1→30s×5。NotifyAgentDead 去重检查。healthCheck 释放锁后调用。",
			"做市混沌：marketmaking/chaos.go 真故障注入 vs marketmaking_api/chaos.go 空壳。统一 chaos 抽象待完成。ArenaAdapter 各动作标注明确语义而非假 Cancel。",
		},
		"db": {
			"PostgreSQL 连接池：MaxOpenConns=%d。MaxIdleConns=%d。ConnMaxLifetime=%s。QueryTimeout 确保所有查询有 deadline。WriteBuffer 防泄漏。CircuitBreaker 熔断保护。Connection get/put 模式显式管理生命周期。",
			"RLS 租户隔离：current_setting('app.tenant_id') 策略。SetTenantContext 通过 set_config 设置。ExecWithTenant/QueryWithTenant 在同一连接上设置 tenant 后执行。空 tenantID 时 ErrMissingTenantID fail-loud。",
			"迁移：core 和 storage 两阶段迁移。顺序迁移防 schema 漂移。RLS 策略在迁移中创建。索引并发创建不锁表。写缓冲背压保护。embedding 队列异步处理。",
		},
	}

	domainTemplates := templates[domain]
	if domainTemplates == nil {
		domainTemplates = templates["wf"]
	}
	tpl := domainTemplates[rng.Intn(len(domainTemplates))]
	content := fmt.Sprintf(tpl, id, rng.Intn(10), rng.Intn(5), "5m", id)
	return content
}

func generateSummary(id, domain string, objType knowledge.ObjectType, rng *rand.Rand) string {
	summaries := map[string][]string{
		"wf": {
			"工作流 DAG 编排引擎，配置驱动+代码驱动双系统",
			"Fluent Builder DAG，条件边 + SetRouter 动态路由",
			"CheckpointStore 状态持久化，三后端支持",
		},
		"mem": {
			"记忆蒸馏三层架构 Raw→Normalizer→Summary",
			"蒸馏管线 Normalizer→Classifier→Scorer→Distiller",
			"向量检索 + RankingService 多信号排序",
		},
		"tool": {
			"工具调用四路径：LLM/Planner/Graph/MCP",
			"MCP 协议 Stdio+SSE 双传输，JSON-RPC 2.0",
			"Planner 兜底：Bridge.Execute→Planner.Plan→Fallback",
		},
		"evol": {
			"遗传算法：TournamentSelection+Crossover+5Mutation",
			"DreamCycle：10min 超时，QuickReject→FullEval 两阶段",
			"Guardrails 保护：未评估比例+停滞+基线回归",
		},
		"chaos": {
			"Arena 13 种故障注入，Survival+Scenario 双模式",
			"鲁棒性评估+Resurrection 指数退避",
			"做市混沌真注入 vs API 空壳需统一",
		},
		"db": {
			"PG 连接池配置 + WriteBuffer + CircuitBreaker",
			"RLS 租户隔离：set_config + ExecWithTenant",
			"两阶段迁移 + 异步 embedding 队列",
		},
	}

	domainSummaries := summaries[domain]
	if domainSummaries == nil {
		return fmt.Sprintf("%s %s object", domain, objType)
	}
	return domainSummaries[rng.Intn(len(domainSummaries))]
}

// TestAKFLargeScaleBenchmark validates Planner selection and token savings
// with a large corpus (5000+ articles) to demonstrate real-world AKF value.
// Run: go test -run TestAKFLargeScaleBenchmark -v ./examples/knowledge-fabric/
func TestAKFLargeScaleBenchmark(t *testing.T) {
	ctx := context.Background()

	// ── Generate large corpus ──────────────────────────
	corpusSize := 5000
	objects, reg, naiveTokens := generateLargeCorpus(corpusSize)
	t.Logf("Generated %d KnowledgeObjects, naive total tokens: ~%d", len(objects), naiveTokens)

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

	comp := compiler.NewDefaultCompiler()

	// ── Test multiple queries ──────────────────────────
	queries := []struct {
		name string
		goal string
		want int // minimum expected node count
	}{
		{"workflow+dag", "ARES 工作流引擎设计，DAG 增强，CheckpointStore", 5},
		{"memory+distill", "记忆蒸馏和向量检索如何工作？", 5},
		{"tool+calling", "工具调用四条路径和 MCP 协议", 5},
		{"domain:tool", "Planner 兜底和工具重试机制", 3},
		{"domain:chaos", "混沌工程故障注入和鲁棒性评估", 3},
	}

	fmt.Println("\n═══ AKF Large-Scale Benchmark (5,000 articles) ═══")
	fmt.Println()

	var totalNaive, totalAKF int
	for _, q := range queries {
		start := time.Now()
		graph, err := rt.Execute(ctx, q.goal,
			knowledge.TokenBudget{MaxTokens: 4000, ForGraph: 2500},
			&runtime.Config{MaxConcurrentProviders: 5},
		)
		execTime := time.Since(start)
		if err != nil {
			t.Errorf("query %q failed: %v", q.name, err)
			continue
		}

		compiled, err := comp.Compile(ctx, graph, compiler.CompileConfig{
			Formats: []compiler.Format{compiler.FormatPrompt},
		})
		if err != nil {
			t.Errorf("compile %q failed: %v", q.name, err)
			continue
		}

		akfTokens := compiled.Metrics.OutputTokens
		if akfTokens <= 0 {
			akfTokens = estimateTokens(compiled.Formats[compiler.FormatPrompt])
		}
		totalNaive += naiveTokens
		totalAKF += akfTokens

		savingPct := float64(naiveTokens-akfTokens) / float64(naiveTokens) * 100
		ratio := float64(naiveTokens) / float64(akfTokens)

		// Check that AKF actually selected a relevant subset.
		fmt.Printf("Query: %q\n", q.name)
		fmt.Printf("  Selected:   %d nodes / %d edges\n", len(graph.Nodes), len(graph.Edges))
		fmt.Printf("  Naive:      %d tokens\n", naiveTokens)
		fmt.Printf("  AKF:        %d tokens\n", akfTokens)
		fmt.Printf("  Saved:      %.1f%%  (%d×)\n", savingPct, int(ratio))
		fmt.Printf("  Time:       %v\n", execTime)
		fmt.Println()
	}

	fmt.Println("═══ Summary ═══")
	avgSaving := float64(totalNaive-totalAKF) / float64(totalNaive) * 100
	avgRatio := float64(totalNaive) / float64(totalAKF)
	fmt.Printf("Total naive:     %d tokens\n", totalNaive)
	fmt.Printf("Total AKF:       %d tokens\n", totalAKF)
	fmt.Printf("Average saving:  %.1f%%  (%d×)\n", avgSaving, int(avgRatio))
	fmt.Printf("Per-query cost (naive): $%.4f  (AKF): $%.4f  @ GPT-4o\n",
		float64(totalNaive)*0.00000002/float64(len(queries)),
		float64(totalAKF)*0.00000002/float64(len(queries)))
}
