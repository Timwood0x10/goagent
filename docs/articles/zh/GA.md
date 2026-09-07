# 遗传算法（GA）完整实现与模块协作解析（0.3.x）

> 本文档基于对代码库的详细扫描，系统梳理 `ares_evolution` 模块中遗传算法（Genetic Algorithm，简称 GA）的完整实现，以及它如何通过事件驱动、编排、评估、组装、反馈等机制与其他模块配合实现「策略自主进化」。

## 目录
1. [总体架构](#1-总体架构)
2. [基因（基因组）数据结构](#2-基因基因组数据结构)
3. [种群初始化](#3-种群初始化)
4. [主进化循环 doEvolve](#4-主进化循环-doevolve)
5. [选择算子（7 种）](#5-选择算子7-种)
6. [交叉算子（3 种）](#6-交叉算子3-种)
7. [变异算子与概率分配](#7-变异算子与概率分配)
8. [自适应与多样性机制](#8-自适应与多样性机制)
9. [适应度评估与打分](#9-适应度评估与打分)
10. [DreamCycle 编排（GA / ES 双模式）](#10-dreamcycle-编排ga--es-双模式)
11. [与其他模块的协作闭环](#11-与其他模块的协作闭环)
12. [公共 API 层](#12-公共-api-层)
13. [进化闭环时序](#13-进化闭环时序)

---

## 1. 总体架构

GA 采用三层实现 + 一层对外 API：

| 层 | 目录 | 职责 |
|---|---|---|
| 核心层 | `internal/runtime/ares_evolution/genome` | 基因/种群、选择、交叉、多样性、适应度、多目标、NSGA-II |
| 变异层 | `internal/runtime/ares_evolution/mutation` | 参数/提示词/工具变异、引导变异、自适应概率分配 |
| 编排层 | `internal/runtime/ares_evolution` | DreamCycle 双模式编排、调度器、守卫、适配器、反馈、评分管线 |
| 公共 API | `api/evolution` | 对外暴露的进化接口封装 |

三者关系：`genome.Population` 是 GA 的运算核心（不感知外部业务），`mutation` 提供遗传算子，`DreamCycle` / `GenomePopulationAdapter` 负责把 GA 接入系统事件流并驱动其运行。

---

## 2. 基因（基因组）数据结构

基因（一个个体 / 一条策略）定义为 `mutation.Strategy`（[types.go](../../../internal/runtime/ares_evolution/mutation/types.go)），核心字段：

| 字段 | 说明 |
|---|---|
| `ID` / `ParentID` | 个体唯一标识 / 父代标识（root 为空；交叉子代为 `A×B`） |
| `Version` | 单调递增版本号 |
| `Params map[string]any` | 可变参数（temperature、top_k、max_steps、memory_limit、conflict_threshold 等） |
| `PromptTemplate string` | 行为提示词模板 |
| `StrategyMutationType` | 个体来源：`parameter` / `prompt` / `tool` / `crossover` / `root` |
| `MutationDesc` | 变异描述（可读） |
| `Score float64` | 权威适应度（`-1` = 未评估） |
| `SelectionScore float64` | 选择用适应度（可被 fitness sharing 修改，不影响上报的 Score） |
| `DimensionScores map[string]float64` | 多目标模式下各维度得分 |
| `GenerationCreated int` | 进入种群的代数（用于 `AgentMaxAge` 淘汰） |
| `EvidenceKey` | 由 prompt + 数字参数推导的稳定键，用于表型证据查找 |

默认参数取值范围（`DefaultParamRanges`）：
- `temperature`: {0.1, 0.3, 0.5, 0.7, 0.9}
- `top_k`: {10, 20, 40, 80}
- `max_steps`: {5, 10, 15, 20}
- `memory_limit`: {3, 5, 10}
- `conflict_threshold`: {0.85, 0.90, 0.95}

---

## 3. 种群初始化

`NewPopulation`（[population.go](../../../internal/runtime/ares_evolution/genome/population.go#L101-L151)）：
1. 从 base 策略克隆出 `root` 个体（`StrategyMutationType=MutationRoot`，`MutationDesc="root strategy"`）。
2. 若目标种群大小 `Size > 1`，用变异器对 root 克隆批量变异，补齐 `Size-1` 个初始变体。
3. 使用确定性随机源 `rand.New(rand.NewSource(seed))` 保证可复现；`seed=0` 时用 `time.Now().UnixNano()`。
4. 校验 `EliteCount ≤ Size`、`MinMutationRate ≤ MaxMutationRate`。

`Population` 结构体还维护：`bestScore`（停滞检测）、`bestEver`（历代最优，用于部署）、`bestEverGeneration`、`paretoFront`（多目标最优前沿）、`stagnantGens`、`currentMutationRate`（运行时自适应变异率）、`recoveryActions`、`history`。

---

## 4. 主进化循环 doEvolve

`doEvolve`（[population.go](../../../internal/runtime/ares_evolution/genome/population.go#L258-L428)）是核心循环，流程为：

```
validate → lock → 按分数排序 → 选存活者 → 保精英 → 促进prompt多样性 →
生成子代(选择父代→交叉→按变异率变异) → 组装下一代 → 代数+1 →
更新bestEver → fitness sharing → 自适应变异率 → 停滞重置 → 多样化恢复 → 回调
```

三种进化模式通过 `evolveConfig` 差异化：

| 模式 | 入口 | 行为差异 |
|---|---|---|
| **Evolve**（全代际） | `Evolve()` | 按 `SurvivalRate`（默认 0.6）留存活者，全部存活者作为父池，保留精英，填充到 `Size` |
| **EvolveSteadyState** | `EvolveSteadyState(replaceRate)` | 仅替换 `replaceRate×Size`（默认 0.3，钳制到 [0.1,0.5]）个最差个体，适合在线学习 |
| **EvolveOnIdle** | `EvolveOnIdle()` | 父池仅取存活者中前 `BreedingPoolRatio` 比例，用于系统空闲时零 LLM 消耗进化（仅做数据运算） |

关键步骤细节：
- **淘汰过期个体**：`AgentMaxAge > 0` 时移除年龄超限个体，root 与 `GenerationCreated==0` 的个体永不淘汰。
- **子代生成** `generateOffspring`：父代经选择器二选一（无选择器则随机），`Crossover` 生成子代，再按 `currentMutationRate` 概率调用 `Mutator(typeof=1)`。
- **精英保护**：精英保留原分数，不参与 fitness sharing 惩罚。
- **组装补齐**：若子代不足 `Size`，从存活者克隆补齐。

`EvolveAfterScoring`（原子接口）在一次调用内完成：`ScoreAgents → EvolveOnIdle → ScoreAgents → appendHistory`，消除用未评估个体进化的风险。

---

## 5. 选择算子（7 种）

选择器实现 `Selection` 接口（`Select(ctx, population, n)`），由 `buildSelector` 按 `SelectionStrategy` 分发（[selection.go](../../../internal/runtime/ares_evolution/genome/selection.go)）：

| 策略 key | 实现 | 说明 |
|---|---|---|
| `tournament`（默认） | `TournamentSelection` | 随机挑 k 个（默认 3，最小 2），选最高分者；k 越大选择压力越强。Fisher-Yates 部分洗牌选唯一下标 |
| `rank` | `RankSelection` | 线性排序选择：最优权重 N、最差 1，降低超强个体早期主导 |
| `sus` | `SUSSelection` | 随机起点 + 等距采样的随机通用采样，降低遗传漂移 |
| `roulette` | `RouletteWheelSelection` | 适应度比例选择，分数平移为非负 |
| `truncation` | `TruncationSelection` | 确定性取前 n 名（精英截断） |
| `lineage_rank` | `LineageRankSelection` | 排序基础上按血统（ParentID）占比惩罚过度代表血统，防血统塌缩 |
| `nsga2`/`nondominated` | `NondominatedSortingSelection` | NSGA-II：非支配排序 + 拥挤距离；无多目标数据时回退 tournament |

所有选择算子统一使用 `effectiveScore()`（优先 `SelectionScore`，否则 `Score`），使 fitness sharing 能影响全部算子。未评估个体（`Score<0`）总排最后。

---

## 6. 交叉算子（3 种）

交叉在 `genome.Crossover`（[crossover.go](../../../internal/runtime/ares_evolution/genome/crossover.go)），参数重组方式由 `WithCrossoverType` 配置：

| 类型 | 实现 | 说明 |
|---|---|---|
| `CrossoverUniform`（默认） | `uniformCrossParams` | 每个参数独立以 50% 概率取自父 A 或父 B |
| `CrossoverTwoPoint` | `twoPointCrossParams` | 排序后取两个切点，中间段来自父 B，两侧来自父 A |
| `CrossoverSegment` | `segmentCrossParams` | 随机连续块来自父 B，其余来自父 A |

提示词模板继承由 `PromptCrossoverMode` 控制：
- `PromptInherit`（默认）：继承高分父代的模板。
- `PromptHalfSplit`：半句交叉，「父 A 前半 + 父 B 后半」（按 rune 计数，避免拆散多字节字符）。
- `PromptUniform`：随机取任意父代模板，促进提示词多样性。

子代 `ParentID = A×B`、`Version = max(A,B)+1`、`StrategyMutationType=MutationCrossover`、`Score=-1`。ID 可用 UUID 或确定性计数器（`det-cross-{a}-{b}-{n}`）。

---

## 7. 变异算子与概率分配

### 7.1 概率分配（70/15/15）

`Mutator.Mutate`（[mutator.go](../../../internal/runtime/ares_evolution/mutation/mutator.go#L86-L196)）按概率为每个子代选择变异类型：

| 池可用情况 | 参数变异 | 提示词变异 | 工具变异 |
|---|---|---|---|
| prompt + tool 池皆有（标准） | **0.70** | **0.15** | **0.15** |
| 仅 prompt 池 | 0.80 | 0.20 | 0.00 |
| 仅 tool 池 | 0.80 | 0.00 | 0.20 |
| 均无池 | 1.00 | 0.00 | 0.00 |

池为空时该类型概率被重分配。`AdaptiveDistribution`（[adaptive_distribution.go](../../../internal/runtime/ares_evolution/mutation/adaptive_distribution.go)）可基于历史胜负动态调整这三类概率（参数 0.30–0.90、提示词 0.05–0.50、工具 0.05–0.50，含探索下限 0.03、学习率 0.10）。

### 7.2 参数变异子算子

`mutateParameter` 内部再按 70/10/10/10 组合：
- **70% 单值变异** `mutateSingleParam`：随机选一个可变参数（键在 paramRanges 中），从候选值里挑一个与当前不同值。
- **10% 交换** `mutateSwap`：交换两个随机参数的值。
- **10% 反转** `mutateInversion`：反转一段连续参数值子序列。
- **10% 打乱** `mutateScramble`：随机子集参数值做 Fisher-Yates 洗牌。

### 7.3 提示词 / 工具变异

- `mutatePrompt`：从 `promptPool` 选一个与当前不同的模板。
- `mutateTool`：从 `toolPool` 替换 `Params["tools"]`；父代无 tools 键时初始化为池首项。

变异后子代 `ParentID=父ID`、`Version=父+1`、`StrategyMutationType` 相应标记，ID 用 UUID 或确定性 `det-mut-{parent}-{n}`。

变异层还提供 `guided_mutator.go`（定向变异）与 `llm_hint_provider.go`（LLM 提示），可从经验/反馈中获取变异方向。

---

## 8. 自适应与多样性机制

集中在 [adaptive.go](../../../internal/runtime/ares_evolution/genome/adaptive.go) 与 [population_guard.go](../../../internal/runtime/ares_evolution/genome/population_guard.go)。

### 8.1 多样性指标（v2）

`DiversityReport` 将多样性拆为三维度，加权综合（默认 数值 40%、类别 40%、血统 20%）：
- **Numeric**：数值参数平均两两归一化距离；大种群用随机邻域采样（O(n×sampleSize)）替代 O(n²)。
- **Categorical**：prompt 模板、tools 配置差异。
- **Lineage**：父 ID 浓度；`DominantLineageShare` 为最大血统占比（>0.6 视为血统塌缩）。

### 8.2 突变率自适应 `adjustMutationRateLocked`

- **紧急模式**：多样性 < 0.05 → 强制最大变异率。
- **低多样性**：低于阈值时按亏空比例放大 1.5x–2.5x。
- **高多样性**（>3×阈值）：按 0.95 温和衰减。
- **中多样性**：维持当前率，仅当远超基线时衰减。
- **地板保护**：阈值以下强制 ≥0.15。

### 8.3 适应度共享（Fitness Sharing）

`applyFitnessSharing`：对已评估个体计算共享距离（niche），对拥挤区域（邻居距离 < `nicheRadius`）的个体按 `crowdCount×shareSigma` 惩罚其 `SelectionScore`（精英豁免）。小种群用精确距离矩阵，大种群用采样，超大种群用空间索引（`spatial_index.go`）。

### 8.4 停滞重置与多样化恢复

`handleStagnationLocked`：连续 `MaxStagnantGenerations` 代无改进时，把最差 1/3（剔除精英）替换为「精英的强扰动克隆」（40% 概率保留原参数，其余 ±80% 扰动），注入新基因。低多样性时 `injectFreshMutantsLocked` 注入全新变异体。三类恢复动作（变异率提升 / 停滞重置 / 新鲜注入）统一记录到 `recoveryActions` 并输出结构化日志。

---

## 9. 适应度评估与打分

- 哨兵值 `ScoreUnevaluated = -1.0`，`IsScoreEvaluated(score) = score >= 0`（[score.go](../../../internal/runtime/ares_evolution/genome/score.go)）。
- `Population.ScoreAgents(scorer)`：在读写锁外调用外部 scorer（可能阻塞数秒的 LLM/IO），捕获 panic 标记为未评估，写回分数并重置 `SelectionScore`，随后更新 `bestEver` / Pareto 前沿。
- `ScoreAgentsMulti(scorer)`：多目标打分，同时写 `DimensionScores` 与聚合 `Score`。
- 打分来源多样：`scoring` 提供 `TieredScorer`（缓存 + 预算门控 LLM + 启发式）、`MemoryAwareScorer`（证据加分 + 成本/时延惩罚）、`CachedScorer`、`BatchScorer`（批量预填缓存）。

---

## 10. DreamCycle 编排（GA / ES 双模式）

[DreamCycle](../../../internal/runtime/ares_evolution/dream_cycle.go) 是进化中枢，`EvolutionMode` 二选一：

| 模式 | 流程 |
|---|---|
| `ModeEvolutionStrategy`（默认，(1+λ)） | 变异当前策略 → 竞技场测试候选 → 部署最优 |
| `ModeGeneticAlgorithm` | 打分种群 → 选择/交叉/变异（一代）→ 部署最优 |

`Run()` 的主流程（[dream_cycle.go](../../../internal/runtime/ares_evolution/dream_cycle.go#L284-L343)）：
1. `runMu` 串行化，防止重复进化（EV-01）。
2. 10 分钟超时控制。
3. 检查 `Enabled`、冷却时间（默认 5 分钟）、任务数下限（默认 10）。
4. 委托 `scheduler.ShouldEvolve()` 判断是否进化。
5. 按 `EvolutionMode` 路由到 `runGAEvolution` 或 `runESEvolution`。

GA 路径 `runGAEvolution`（[dream_cycle_ga.go](../../../internal/runtime/ares_evolution/dream_cycle_ga.go#L40-L117)）：
1. 终止条件：超 `MaxGenerations` 或 `BestEverScore ≥ TargetFitness`。
2. `ScoreAgents(buildGAScorer)`：scorer 用竞技场 tester 对未评估个体跑回归（baseline 为当前最优）。
3. `Evolve` 或 `EvolveSteadyState` 跑一代。
4. `BestStrategy()` 取最优，记录血统，`deployWinner` 部署。

`deployWinner`（[dream_cycle.go](../../../internal/runtime/ares_evolution/dream_cycle.go#L486-L606)）统一处理 GA/ES 的部署后逻辑：守卫后置检查 → 影子评估（ShadowEvaluator）→ 尺寸校验 → `ActiveStrategyManager.Deploy` → 指标埋点 → 记录 hint outcome。

---

## 11. 与其他模块的协作闭环

### 11.1 事件驱动调度（ares_callbacks）
`EvolutionScheduler.Register()`（[scheduler.go](../../../internal/runtime/ares_evolution/scheduler.go#L297-L320)）订阅 `ares_callbacks.EventAgentEnd`。`OnAgentEnd` 在任务完成时触发，异步经 errgroup 运行 Adapter（`adapter.Run`）。

`shouldEvolve` 判定（[scheduler.go](../../../internal/runtime/ares_evolution/scheduler.go#L336-L399)）：
- 最小间隔保护（默认 5 分钟）。
- 三种触发模式：`TriggerOnIdle`（分数退化 ≥15% 或累计 ≥100 条触发周期探索）、`TriggerOnThreshold`、`TriggerOnDemand`。
- 分数窗口 `scoreWindowSize=50`，可靠性最少 20 条。

### 11.2 候选评估（ares_arena）
打分复用 `TesterInterface.Run`（`findWinner` 的两阶段：QuickReject N=5 → 全量 N=50，errgroup 并行 + 自适应批量）。GA 的 `buildGAScorer` 也调用同一 tester 以 `CandidateScore` 作为适应度。

### 11.3 系统组装闭环（GenomePopulationAdapter）
`GenomePopulationAdapter`（[genome_wiring.go](../../../internal/runtime/ares_evolution/genome_wiring.go)）实现 `AdapterRunner`，把 `genome.Population` 接入调度器。其 `Run`（[genome_wiring_run.go](../../../internal/runtime/ares_evolution/genome_wiring_run.go#L45-L102)）：
```
buildRunScorer → 前置守卫 → EvolveAfterScoring →
recordOutcomes(反馈闭环) → 后置守卫 → submitToCoordinator → deployBestStrategy
```

### 11.4 评分管线集成
`buildRunScorer` 优先用 `TieredScorer`（缓存 + 预算门控 LLM + 启发式），`batchScorer` 批量预填 `scoreCache`，`MemoryAwareScorer` 做证据加分。失败时降级启发式（避免统一的 50.0 假象）。

### 11.5 记忆 / 经验 / 反馈
- **经验**：`FlightToExperienceAdapter` 订阅 `EventTaskFailed` 等事件，把诊断记录转为 `Experience`。
- **反馈闭环**：`recordOutcomesLocked` 对比子代与父代分数（含 `A×B` 取均值），赢得 `AdaptiveDistribution.RecordOutcome`（更新变异概率分布）与 `FeedbackRecorder.Register`（经验强化）。
- **提示学习**：`hintProvider.RecordStrategyOutcome` 记录胜负，供 `llm_hint_provider` 引导后续变异。

### 11.6 协调器与系统落地
`submitToCoordinator` 将进化结果生成为 diff patches，`Source=SourceGA`、`Reason="GA: population evolution result"`、`Priority=6`，提交 `coordinator.Submit` 并 `Evaluate`。协调器经 FitnessGenome 聚合适应度（0-100 缩放）做 apply/reject/delay/drop 决策，通过 PatchExecutors 落地到运行中 agent 的 DAG、调度器、知识配置。

### 11.7 守卫（Guardrails）
`EvolutionGuardrails`（[guardrails.go](../../../internal/runtime/ares_evolution/guardrails.go)）：
- **前置** `PreEvolveCheck`：未评估个体占比 >50% → Critical 停止；停滞超阈值 → Warning。
- **后置** `PostEvolveCheck`：基线回退 → Critical 停止；改进跟踪 / 停滞计数；血统集中度超 `MaxLineageShare`（默认 0.8）→ Warning。
- 事件机器可读（`ErrCode*`），支持 `ToGuardrailError` 供自动回滚/降级。

### 11.8 可观测性
`MetricsRecorder`（`RecordEvolutionDeploy/Shadow/SetEvolutionScore`）、`observability.PrometheusMetrics`、`recordEvolutionGuardrail`、结构化日志（`logger.New("genome"/"adapter")`）、`report_hook` 与 `report_saver` 记录进化轨迹。

### 11.9 晋升子系统
`promotion` 的 `DefaultPromoter` 依据成功率和置信度对策略做 champion/非 champion 晋升与降级，作为 GA 进化之外的策略生命周期管理。

---

## 12. 公共 API 层

[api/evolution](../../../api/evolution/evolution.go) 封装内部实现，暴露：
- `Strategy` / `Lineage` 数据模型。
- `DreamCycle` 接口（`Run/SetEnabled/IsEnabled/TaskCount`）及 `NewDreamCycle`。
- `Population` 接口（`Agents/Size/CurrentGeneration/BestScore/BestStrategy/ScoreAgents/Evolve`）及 `NewPopulation`，`ScorerFunc` 允许外部注入自己的评估器。
- `Mutator`（公共变异入口）与 `Promoter`（晋升/降级）。

---

## 13. 进化闭环时序

```
任务完成
  └─ ares_callbacks 触发 EventAgentEnd
       └─ EvolutionScheduler.OnAgentEnd
            ├─ shouldEvolve(冷却/退化/周期) 通过
            └─ checkGuardrails(前置守卫) 通过
                 └─ adapter.Run(异步)
                      ├─ buildRunScorer(缓存+预算LLM批量打分)
                      ├─ 前置守卫 PreEvolveCheck
                      ├─ EvolveAfterScoring
                      │    ├─ ScoreAgents(竞技场tester打分)
                      │    ├─ EvolveOnIdle
                      │    │    ├─ 排序 → 存活 → 精英
                      │    │    ├─ 选择(7选1) → 交叉(3类) → 变异(70/15/15)
                      │    │    ├─ fitness sharing
                      │    │    └─ 变异率自适应/停滞重置/新鲜注入
                      │    └─ ScoreAgents(再打分)
                      ├─ recordOutcomes(→AdaptiveDistribution/FeedbackRecorder/hint)
                      ├─ 后置守卫 PostEvolveCheck(血统/基线回退)
                      ├─ submitToCoordinator(→diff patches→协调器→PatchExecutors落地)
                      └─ deployBestStrategy(→ActiveStrategyManager→线上agent消费)
```

**核心思想**：GA 通过「任务完成 → 调度器判断退化/周期 → 种群打分 → 选择/交叉/变异 → 守卫拦截 → 部署最优 → 反馈记录」的闭环，把进化结果不断回流到变异概率、经验库、协调器与线上运行策略，形成可持续的自主进化。