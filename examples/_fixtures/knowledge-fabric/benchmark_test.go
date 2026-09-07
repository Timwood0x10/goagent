package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/compiler"
	"github.com/Timwood0x10/ares/internal/knowledge/planner"
	"github.com/Timwood0x10/ares/internal/knowledge/provider"
	"github.com/Timwood0x10/ares/internal/knowledge/runtime"
)

// Benchmark token savings of AKF pipeline vs naive full-dump.
//
// Run:  go test -bench=BenchmarkAKFSavings -benchmem -v ./examples/knowledge-fabric/
// Print details: go test -run TestAKFSavings -v ./examples/knowledge-fabric/

// rawArticles simulates full-length article content for each demo article.
// Each Raw is ~300-600 chars of realistic Chinese technical documentation.
var rawArticles = map[string]string{
	"wf-engine":       `工作流引擎是ARES系统的核心编排组件，负责将用户请求转化为可执行的步骤序列。系统目前维护两套工作流系统：第一套是Workflow Engine，采用配置驱动方式，通过YAML文件定义工作流结构，支持热重载和人工介入（HITL）。第二套是Graph System，使用代码驱动的Fluent Builder API，支持条件边和可插拔调度器。两套系统的数据流基本一致：YAML配置文件通过WorkflowLoader加载，转换为Workflow和Step结构体，然后由DAG引擎创建有向无环图，最终通过Executor执行并返回WorkflowResult。两套并存的原因是配置驱动的灵活性在某些场景下不够，需要代码方式支持运行时动态加减节点。`,
	"wf-dag":          `为什么ARES需要两套工作流系统？这是一个经过深思熟虑的设计决策。最初我们只有Workflow Engine，它通过YAML配置定义工作流，优点是声明式、可读性强、适合运维人员使用。但随着使用场景的复杂化，我们发现配置驱动的方式存在固有局限：无法根据运行时条件动态调整拓扑结构、难以实现条件分支。因此我们开发了第二套Graph System，它使用Fluent Builder API在代码中构建工作流，支持条件边和动态路由（SetRouter）；受控循环由Kernel侧LoopPlugin轮次节拍承担。两套系统并行运行，服务不同用户群体，底层共享相同的DAG语义。`,
	"mem-distill":     `记忆蒸馏是ARES实现自主学习的核心技术。Agent在工作过程中会产生大量对话历史、工具调用结果和运行时数据，如果不加筛选地全部保留，记忆会迅速膨胀到无法管理。记忆蒸馏借鉴了人类遗忘和提炼的机制：不是简单存储原始记录，而是从中提取可复用的经验。整个蒸馏架构分为三层：第一层是原始数据（Raw），保留完整的对话历史；第二层是标准化层（Normalizer），将不同格式的原始数据统一为标准化文本；第三层是摘要层（Summary），通过LLM压缩为简洁的知识摘要。关键设计原则是：提炼可复用的经验模式，而不是简单存储聊天记录。`,
	"mem-normalizer":  `Memory Distillation的标准化层是整个管线的第一个重要环节。标准化层的职责是将来自不同渠道的原始记忆——包括对话文本、工具调用日志、运行时状态快照——统一为结构化的标准化表达。标准化的流程分为四个步骤：第一步是Normalizer，对原始文本进行清洗和格式化；第二步是Classifier，根据内容类型进行分类（决策类、架构类、问题类等）；第三步是Scorer，根据重要性和置信度打分；第四步是Distiller，将评分后的记忆压缩为最终的KnowledgeObject。标准化的核心目标是让来自不同渠道的记忆可以被同一套Distiller处理，实现知识处理的统一化。`,
	"tool-calling":    `工具调用是ARES Agent与外部世界交互的主要方式。系统支持四条工具调用路径，根据场景自动选择最优方案。Path1是LLM驱动调用：Agent通过parseToolCalls解析LLM返回的工具调用请求，然后通过CallTool执行，最终由Registry.Execute完成实际调用。Path2是Planner兜底：当LLM选错工具或传错参数时，Planner通过语义分析重新规划工具调用序列，Bridge.Execute作为入口，经过Planner.Plan生成新的执行计划，通过executeStepWithFallback确保异常情况下有兜底逻辑。Path3是Workflow Graph：通过ToolNode.Execute在工作流中执行工具调用，支持前置和后置事件钩子。Path4是MCP外部工具：通过MCP协议调用外部注册的工具服务。四路径的设计原则是：当LLM不靠谱时用确定性引擎兜底。`,
	"tool-planner":    `Planner兜底策略是ARES工具调用可靠性的最后一道防线。当LLM在工具选择或参数传递上出现错误时，Planner不会直接失败，而是启动重规划流程。具体流程是：Bridge.Execute检测到LLM返回的工具调用异常后，调用Planner.Plan重新生成工具调用计划，然后通过executeStepWithFallback分步骤执行。Fallback机制包括：参数修正（自动补全或纠正参数）、工具替换（选择功能相近的替代工具）、步骤回退（回退到上一步重新决策）。Planner的另一个重要功能是语义分析：它能够理解用户请求的深层意图，而不仅仅是工具名称的匹配。例如用户说'查天气'，Planner能自动匹配到weather.query工具，即使LLM没有正确返回工具名称。`,
	"dag-conditional": `DAG条件拓扑是ARES工作流系统的核心能力，分工明确。第一是条件跳过：条件边（Edge条件谓词）在运行时决定边是否可走，条件不满足的分支被剪掉——这由Graph System承担。第二是动态路由（SetRouter）：节点完成后根据执行结果决定下一步走向哪个节点。第三是受控循环：由Kernel侧LoopPlugin轮次节拍承担，kernel.loop_round_quanta把调度quantum聚合为轮次，kernel.loop_max_iterations限制轮次预算，轮次结束触发OnRoundEnd做checkpoint flush、记忆路由与演化记录。`,
	"dag-checkpoint":  `状态检查点是ARES工作流系统的容错机制。通过WithCheckpointStore方法配置持久化后端，系统会在每个步骤执行完成后自动保存StepResult到检查点存储。检查点存储支持多种后端：PostgreSQL（生产环境推荐），SQLite（单机开发测试），Redis（缓存加速）。检查点的写入采用非阻塞方式，写入失败不会中断工作流的正常执行。除了Workflow Engine外，Graph包也支持检查点功能：在每个节点执行完成后，保存已执行节点集合（executed set）和完整State快照到检查点。这样即使系统崩溃重启，也能从最近的检查点恢复执行，不需要重新运行整个工作流。`,
	"evol-intro":      `自主进化是ARES Agent实现持续自我改进的核心机制。进化管线基于遗传算法（GA）实现，包含五个关键组件：TournamentSelection（锦标赛选择：从种群中选择表现优异的个体作为父代），UniformCrossover（均匀交叉：将两个父代的策略参数进行混合重组），Mutation（五类变异操作：参数扰动、结构变异、Prompt重写、工具重选、策略替换），TieredScorer（分层评分器：从正确性、效率、鲁棒性、可解释性四个维度综合评分），DreamCycle（梦想周期：定期触发进化流程，生成并测试候选策略）。进化管线与记忆蒸馏紧密配合：蒸馏出的KnowledgeObject作为经验知识影响GA的适应度评分，让进化方向更接近实际使用场景。`,
	"chaos-intro":     `混沌工程（Arena）是ARES验证Agent鲁棒性的测试框架。Arena支持13种故障注入类型，涵盖网络分区、内存损坏、MCP断连、LLM超时、工具异常等。测试分为两种模式：Survival Mode（生存模式）在Agent运行期间随机注入故障，计算ResilienceScore（鲁棒性评分），评估Agent在持续干扰下的稳定运行能力。Scenario Mode（场景模式）通过YAML文件定义有序的故障序列，模拟真实生产环境的故障演进。Chaos与市场做市模块的故障注入集成，让量化交易策略也能在混沌环境下验证鲁棒性。`,
	"cross-wf-mem":    `工作流引擎与记忆蒸馏的集成是ARES知识闭环的重要组成部分。当执行失败时，Kernel恢复循环会触发记忆查询流程：执行体死亡或租约到期使任务重新排队，恢复循环查询经验库搜索类似历史，从历史经验中提取恢复策略，为任务绑定替换执行体并从checkpoint续跑，spawn时注入蒸馏出的经验先验。这种模式让系统能从历史经验中学习，不断提升故障恢复的成功率。`,
	"cross-tool-mem":  `工具调用与记忆蒸馏的集成实现了工具层面的知识复用。每次工具执行完成后，执行结果会被自动蒸馏为KnowledgeObject存入知识库。当Agent后续遇到类似的请求时，Tool Execution Bridge会在调用LLM之前先查询相关记忆，从历史经验中直接获取工具选择方案和参数配置。这显著减少了LLM调用次数，降低了Token消耗和响应延迟。集成链路是：ToolExecution → 结果蒸馏 → KnowledgeObject → 后续查询匹配 → 经验复用。`,
	"wf-engine-ext":   `工作流引擎的扩展机制建立在PluginBus之上：插件实现RuntimePlugin接口（Name、Capabilities、Start、Stop），并可选实现WorkflowHook接口在步骤边界拦截（BeforeStep、AfterStep）。能力位由Capability常量声明：CapObserver、CapCheckpoint、CapRouter、CapLoop、CapMemory、CapEvolution、CapTool、CapRecovery、CapInterrupt，总线按能力做服务发现（PluginsByCap）。内置插件包括ObserverPlugin（事件镜像）、CheckpointPlugin（检查点持久化）、ToolPlugin（工具执行）、LoopPlugin（轮次时钟）、EvolutionPlugin（演化建议）、ArenaPlugin（故障注入）、InterruptPlugin（人工介入）、BasicRecoveryPlugin（恢复）。生命周期顺序是载荷性的：Register必须先于Start——Start之后Register返回ErrBusAlreadyStarted，且只有Start才把EventBus引用交给插件，注册晚了插件的服务发现永远为空。`,
}

func TestAKFSavings(t *testing.T) {
	ctx := context.Background()

	// ── Build test data ──────────────────────────────────
	reg := buildRegistry()
	objects := allObjects()
	t.Logf("Total in-memory articles: %d", len(objects))

	// ── Naive: dump ALL Raw content ─────────────────────
	var naiveTotal int
	var naiveParts []string
	for _, obj := range objects {
		raw := rawArticles[obj.ID]
		if raw == "" {
			raw = obj.Summary
		}
		naiveTotal += estimateTokens(raw)
		naiveParts = append(naiveParts, fmt.Sprintf("=== %s (%s) ===\n%s", obj.ID, obj.Type, raw))
	}
	naiveFull := strings.Join(naiveParts, "\n\n")
	naiveTokens := estimateTokens(naiveFull)

	// ── AKF: Planner → Runtime → Compiler ───────────────
	pipeline := knowledge.NewKnowledgePipeline(nil, nil, nil, nil)
	qp := &simpleQueryPlanner{}
	sd := planner.NewSourceDiscovery(reg, qp)
	planr := planner.NewKnowledgePlanner()

	rt := runtime.New(
		planr, sd, reg, pipeline,
		[]runtime.Linker{&runtime.DefaultLinker{}},
		[]runtime.Reducer{&runtime.DefaultReducer{}},
	)

	start := time.Now()
	graph, err := rt.Execute(ctx,
		"ARES 工作流引擎设计，DAG 增强，条件边，循环，检查点",
		knowledge.TokenBudget{MaxTokens: 5000, ForGraph: 3000},
		&runtime.Config{MaxConcurrentProviders: 5},
	)
	if err != nil {
		t.Fatal(err)
	}
	execTime := time.Since(start)

	comp := compiler.NewDefaultCompiler()
	compiled, err := comp.Compile(ctx, graph, compiler.CompileConfig{
		Formats: []compiler.Format{compiler.FormatPrompt},
	})
	if err != nil {
		t.Fatal(err)
	}

	akfTokens := compiled.Metrics.OutputTokens
	var akfTokenCount int
	if akfTokens > 0 {
		akfTokenCount = akfTokens
	} else {
		akfTokenCount = estimateTokens(compiled.Formats[compiler.FormatPrompt])
	}

	// ── Report ───────────────────────────────────────────
	savingPct := float64(naiveTokens-akfTokenCount) / float64(naiveTokens) * 100
	compressionRatio := float64(naiveTokens) / float64(akfTokenCount)

	fmt.Println("\n═══ AKF Token Savings Report ═══")
	fmt.Println()
	fmt.Printf("Naive (all %d articles full Raw):  %10d tokens\n", len(objects), naiveTokens)
	fmt.Printf("AKF   (Runtime + Compiler):        %10d tokens\n", akfTokenCount)
	fmt.Printf("Saved:                             %10d tokens  (%.1f%% reduction)\n",
		naiveTokens-akfTokenCount, savingPct)
	fmt.Printf("Compression ratio:                 %10.1f×\n", compressionRatio)
	fmt.Printf("Execution time:                    %10v\n", execTime)
	fmt.Printf("Nodes in graph:                    %10d\n", len(graph.Nodes))
	fmt.Printf("Edges in graph:                    %10d\n", len(graph.Edges))
	fmt.Println()

	// Breakdown per optimization stage
	summariesOnly := 0
	for _, obj := range objects {
		summariesOnly += estimateTokens(obj.Summary)
	}
	summaryVsRawPct := float64(naiveTokens-summariesOnly) / float64(naiveTokens) * 100

	fmt.Println("═══ Savings Breakdown ═══")
	fmt.Printf("  Stage 1 — Summary vs Raw:       %10d → %6d  (%5.1f%% reduction)\n",
		naiveTotal, summariesOnly, summaryVsRawPct)
	fmt.Printf("  Stage 2 — Relations added back:  %10s\n", "+relations (structural)")
	fmt.Printf("  Stage 3 — Reducer prune + TokenBudget: %d→%d tokens\n",
		summariesOnly, akfTokenCount)
	fmt.Println()

	// Realistic estimate for large-scale scenario
	fmt.Println("═══ Large-Scale Projection (50,000 articles) ═══")
	bigNaive := 50000 * (naiveTokens / len(objects))
	bigAKF := akfTokenCount // AKF culls to TokenBudget regardless of corpus size
	bigPct := float64(bigNaive-bigAKF) / float64(bigNaive) * 100
	fmt.Printf("  Naive full dump:    ~%d tokens  ($%.4f @ GPT-4o)\n",
		bigNaive, float64(bigNaive)*0.00000002)
	fmt.Printf("  AKF compiled:       ~%d tokens  ($%.4f)\n",
		bigAKF, float64(bigAKF)*0.00000002)
	fmt.Printf("  Savings:            %.1f%%  (%d×)\n", bigPct, bigNaive/bigAKF)

	fmt.Println()
	fmt.Println("═══ Actual AKF Output Sample (first 300 chars) ═══")
	out := compiled.Formats[compiler.FormatPrompt]
	if len(out) > 300 {
		out = out[:300] + "..."
	}
	fmt.Println(out)
}

// ── Helpers ───────────────────────────────────────

func buildRegistry() *provider.ProviderRegistry {
	reg := provider.NewProviderRegistry()
	_ = reg.Register(&memoryProvider{
		name:    "docs-articles",
		objects: allObjects(),
	})
	return reg
}

func allObjects() []*knowledge.KnowledgeObject {
	objs := []*knowledge.KnowledgeObject{
		{
			ID: "wf-engine", Type: knowledge.ObjectArchitecture,
			Summary:    "工作流引擎：两套工作流系统。Workflow Engine（配置驱动 DAG，YAML 定义，热重载，HITL）和 Graph System（代码驱动 Fluent Builder，条件边，可插拔调度器）。数据流：YAML → WorkflowLoader → Workflow+Step → DAG → Executor → WorkflowResult",
			Confidence: 0.95,
			Tags:       []string{"workflow", "engine", "dag", "architecture"},
		},
		{
			ID: "wf-dag", Type: knowledge.ObjectDecision,
			Summary:    "为什么会有两套工作流系统：配置驱动的灵活性不够，需要代码动态加减节点、运行时修改拓扑结构。于是写了第二套 Graph System——Fluent Builder API、条件边、可插拔调度器。两套并行，服务不同用户群体。",
			Confidence: 0.92,
			Tags:       []string{"workflow", "dag", "decision", "architecture"},
		},
		{
			ID: "mem-distill", Type: knowledge.ObjectMemory,
			Summary:    "记忆蒸馏：Agent 学会遗忘和提炼。从词频分析起步，最终走向三层架构。对话历史 → RawExperience → Normalizer → KnowledgeObject。关键是提炼可复用的经验，不是简单存聊天记录。",
			Confidence: 0.94,
			Tags:       []string{"memory", "distillation", "architecture"},
		},
		{
			ID: "mem-normalizer", Type: knowledge.ObjectArchitecture,
			Summary:    "Memory Distillation 的标准化层：将原始记忆（对话、工具调用结果、运行时数据）统一为标准化经验。Normalizer → Classifier → Scorer → Distiller。目标是让不同来源的记忆可用同一套 Distiller 处理。",
			Confidence: 0.88,
			Tags:       []string{"memory", "normalizer", "pipeline", "architecture"},
		},
		{
			ID: "tool-calling", Type: knowledge.ObjectArchitecture,
			Summary:    "工具调用四条路径：Path1 LLM 驱动调用（parseToolCalls → CallTool → Registry.Execute），Path2 Planner 兜底（语义分析→能力规划→评分），Path3 Workflow Graph（ToolNode.Execute），Path4 MCP 外部工具。当 LLM 不靠谱时用确定性引擎兜底。",
			Confidence: 0.96,
			Tags:       []string{"tool", "calling", "planner", "mcp", "architecture"},
		},
		{
			ID: "tool-planner", Type: knowledge.ObjectDecision,
			Summary:    "Planner 兜底策略：当 LLM 选错工具或传错参数时，Planner 通过语义分析重新规划。Bridge.Execute → Planner.Plan → executeStepWithFallback。确定性引擎作为最后一道防线。",
			Confidence: 0.90,
			Tags:       []string{"tool", "planner", "decision", "fallback"},
		},
		{
			ID: "dag-conditional", Type: knowledge.ObjectArchitecture,
			Summary:    "DAG 条件拓扑：条件分支与动态路由由 Graph System 承担——条件边（Edge 条件谓词）实现条件跳过，SetRouter 做动态路由。受控循环由 Kernel 侧 LoopPlugin 轮次节拍承担：kernel.loop_round_quanta 聚合轮次，kernel.loop_max_iterations 限制预算。",
			Confidence: 0.91,
			Tags:       []string{"dag", "conditional", "router", "loop", "new-feature"},
		},
		{
			ID: "dag-checkpoint", Type: knowledge.ObjectDecision,
			Summary:    "状态检查点：WithCheckpointStore 每步完成后自动持久化 StepResult。支持 PostgreSQL/SQLite/Redis 后端。非阻塞写入，失败不中断执行。Graph 包也支持每节点后保存 executed 集合 + State 快照。",
			Confidence: 0.87,
			Tags:       []string{"dag", "checkpoint", "persistence", "new-feature"},
		},
		{
			ID: "evol-intro", Type: knowledge.ObjectDecision,
			Summary:    "自主进化（GA Pipeline）：TournamentSelection + UniformCrossover + 5 类 Mutation + TieredScorer + DreamCycle。Agent 可以自我改进策略参数和 Prompt。配合 Memory Distillation 使用，蒸馏出的 KnowledgeObject 影响进化方向。",
			Confidence: 0.85,
			Tags:       []string{"evolution", "ga", "pipeline", "memory"},
		},
		{
			ID: "chaos-intro", Type: knowledge.ObjectDocument,
			Summary:    "混沌工程（Arena）：13 种故障注入类型。Survival Mode（随机攻击+ResilienceScore），Scenario Mode（YAML 定义有序故障序列）。Agent 在混沌环境下验证鲁棒性。和市场做市（Market Making）的 Chaos 集成。",
			Confidence: 0.83,
			Tags:       []string{"chaos", "testing", "resilience", "arena"},
		},
		{
			ID: "cross-wf-mem", Type: knowledge.ObjectMemory,
			Summary:    "工作流引擎调用记忆蒸馏：当 DAG 步骤执行失败时，Kernel 恢复循环查询经验库——执行体死亡或租约到期触发任务重排队，恢复循环为任务绑定替换执行体并从 checkpoint 续跑，spawn 时注入蒸馏出的经验先验，匹配成功后自动调整恢复策略。",
			Confidence: 0.80,
			Tags:       []string{"workflow", "memory", "recovery", "cross-ref"},
		},
		{
			ID: "cross-tool-mem", Type: knowledge.ObjectMemory,
			Summary:    "工具调用与记忆蒸馏集成：工具执行结果被蒸馏为 KnowledgeObject，后续同类请求直接关联经验。Tool Execution Bridge 在运行前查询相关记忆，减少 LLM 调用次数。",
			Confidence: 0.82,
			Tags:       []string{"tool", "memory", "distillation", "cross-ref"},
		},
		{
			ID: "wf-engine-ext", Type: knowledge.ObjectCode,
			Summary:    "工作流引擎扩展：PluginBus 上注册 RuntimePlugin（Name/Capabilities/Start/Stop），可选实现 WorkflowHook（BeforeStep/AfterStep）拦截步骤边界。按 Capability 做服务发现（PluginsByCap）。Register 必须先于 Start，否则插件拿不到 EventBus。",
			Confidence: 0.78,
			Tags:       []string{"workflow", "plugin", "extension", "code"},
		},
	}
	return objs
}

// estimateTokens approximates token count for mixed Chinese/English text.
// Chinese: ~1.5 tokens/char; English: ~0.25 tokens/char
func estimateTokens(s string) int {
	var chinese, english int
	for _, r := range s {
		if r > 0x4E00 && r < 0x9FFF {
			chinese++
		} else if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			english++
		}
	}
	return chinese*2 + english/3 + len(s)/10 // approx: Chinese ~2B, English ~0.33B, punctuation
}
