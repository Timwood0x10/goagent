# ARES AgentOS — 运行时全景图

> 本文档是 2026-09-08 代码追踪的快照，所有结论带 file:line 锚点，只认代码不认文档。
> 与 ARCHITECTURE.md 的分工：ARCHITECTURE.md 管"为什么这样设计 + 路线图"，本文档管"现在到底怎么跑"。

---

## 1. 核心执行主链（生产 peer 模式，一条线）

```
POST /api/tasks (agent.go:381)
  → submitPeerTask (agent.go:1487)
      · session_id 归一（空则 sess-auto-N）
      · capability 一律归一为 ares/plan (agent.go:1504)
  → ensureSessionAdmission (agent.go:2197)
      · SessionRegistry.InitSession 建 L2Graph + 订阅增量编译
      · root 任务编译（CompileNode，零工作量子）
  → Task Fabric Create (fabric.go:211)
      · READY + 盖章当前 strategy_id（归因起点，agent.go:996）
  ══════════ kernel Scheduler drain 循环（唯一执行者）══════════
  Run (scheduler.go:307)：500ms ticker + 5 类任务事件订阅加速 + 抢占 watcher
  drain (scheduler.go:441)：
      reconcileFabricDeaths → ResumableTasks(READY+SUSPENDED) → PreemptLowerPriority
      → 有界并发 execute（⚠️ 生产并发度=1，见 §6-3）
  executeWithCandidates (scheduler.go:659)：
      评分选人 score = capability重叠×(1−load)×confidence×(1+priority) (task/scheduler.go:71)
      → fabric.Schedule → Acquire (CAS+epoch fencing, fabric.go:283)
      → 心跳续租 ttl/3 (scheduler.go:786)
  RunQuantum (quantum.go:48)：
      Start → Quantum++ → executor.ExecuteStep
      → err:  Fail（预算内回 READY / 耗尽 FAILED, fabric.go:387）
      → !Done: Yield（SUSPENDED + checkpoint 存 envelope）
      → Done:  CompleteWithCheckpoint（COMPLETED）
  ═══════════════════════════════════════════════════════════════
  winner 的 routerCognition (l2graph.go:278) 四路分发：
      ares/plan  → plannerCognition.ExecuteStep (planner_cognition.go:206)
      tool/<n>   → toolCognition（一次 CallTool）
      ares/answer→ answerCognition（终态 + ReleaseSession）
      ares/root  → rootCognition（prompt 写入 envelope）

  planner 量子内部（planner_cognition.go:206）：
      找图（SessionID→L2Graph）→ 深度护栏（>=10 强制 answer）
      → assembleContext（root prompt + 前驱工具输出，:325）
      → L1 先验提示 + 演化策略注入（PromptTemplate→system msg, :249）
      → 一次 LLM 调用
      → 有 tool_calls: L2Graph.AddToolNode（过 L1 enabled/budget 约束）
          → 图事件 → CompileCoordinator (coordinator.go:650)
          → ApplyChange → fabric.CompileNode → 新任务 READY（循环回到 drain）
      → 无 tool_calls: growAnswerNode → 终态 → ReleaseSession
      → 会话收割: sessionReaper (agent.go:1222, keep-set+grace 30s)

  终态事件（带 strategy_id）→ 六路消费者：
      RuntimeObserver → fitness(1.0/0.0) → GA 环（§3）
      蒸馏订阅 → experiences 表 → GA hints / RAG（§3）
      FlightRecorder / introspect / ScoredFeedbackLoop / 恢复循环
```

关键事实：
- **任务的最大来源是 planner 自己长图**，不是用户——每个 tool/plan/answer 节点都是 fabric 任务。
- GA 孵化的是 Agent（人口），不直接造任务。
- 所有提交归一到 `ares/plan` 首量子，L2 router 是唯一执行路径（ReAct chatCognition 已删）。

## 2. 调度机制（internal/kernel/scheduler.go）

- **无 leader**：依赖完成让任务变 READY，调度器只是 `ResumableTasks()` 的消费者；DAG 成环在提交时 Kahn 检测拒绝（agent.go:2595）。
- **三重驱动**：500ms ticker；任务事件订阅（完成即触发下一轮）；独立抢占 watcher（高优 READY 协作抢占低优 RUNNING，只在量子间，scheduler.go:328）。
- **防双执行两层**：`Acquire` CAS（READY/SUSPENDED 只有一个赢家）+ 全局 `epoch++` fencing——A 租约过期被 B 接管后，A 迟到的 complete/release 被 `ErrEpochMismatch` 拒绝（fabric.go:695）。
- **防饿死**：全零分候选 → 去置信度的兜底排名（task/scheduler.go:98）；`ErrNoCapableCandidate` 是合法等待态，每 tick 重试。
- **恢复绑定**：`boundExecutors[taskID]` 绕过候选池（scheduler.go:598），终态自动解绑（:888）。
- **stale-winner 三分支**（scheduler.go:736）：有替补→Release；有恢复循环→Release+提名；都没有→保租约等 TTL。
- **panic 三层防线**：单任务 goroutine recover / executor panic 释放 load 槽（EndNeutral, :822）/ drain 级 recover。

## 3. 反馈环（哪些闭合）

**进化环 ✅ 全闭合**：
```
task Create 盖章 strategy_id (agent.go:996)
→ 完成事件带章 → RuntimeObserver 归因打分 (observer.go:221)
→ evidence(source="strategy") → RuntimeFitnessAggregator 加权
   (strategy .40 / dimension_eval .25 / workflow .15 / scheduler .15 / recovery .05,
    fitness_aggregator.go:54)
→ 5min ticker → lifecycle.Submit → 门链
   G1 护栏(rollback_policy.go:355) → G2 shadow 门(lifecycle.go:517, fail-closed)
   → G3 eval 门(gate_eval.go, 需配置 eval_suite)
→ promote → StrategyStore.SetActive
→ planner 下一个量子读 GetActiveStrategy → 新 PromptTemplate/Params 生效
   (planner_cognition.go:249)  ← 无重启无推送
→ 部署后 30s watch 循环监测降级 → 自动回滚 + 黑名单 3 代 (lifecycle.go:905)
```
短板：evidence 默认内存存储（provide_new_evolution.go:138）→ 重启清零；shadow 打分用 ReplayScorer 历史窗口对比，非候选特异 A/B。

**经验环 ✅ 闭合（M4 二批）**：
- 蒸馏→GA 突变 hints ✅（provide_distillation.go:90）
- 蒸馏→RAG prompt 注入 ✅（retriever_wiring.go:80）
- ExperiencePrior→CognitiveState 读侧 ✅（M4.3：executing_agent_id 盖章→planner 注入先验 system 消息）
- skill 经验→调度置信 ✅ 写读闭环（M4.4：taskfabric 终态事件 capability 键→skillOutcomeWriter→Experience；读侧 ConfidenceForMeasured 去遮蔽——tracker 中性 1.0 不再挤掉先验，测量值恒胜先验）

**恢复环 ✅ 闭合**：租约过期 → CheckExpiredLeases 回 READY → 恢复循环按死亡快照原地复活（终身 5 次）或 `recovery-<task>-<ts>` 替身绑定 → 终态解绑。

## 4. 状态机

**Agent Fabric**（agent.go:13, lifecycle.go）：
```
(∅)─Spawn→IDLE⇄SUSPENDED；IDLE/RUNNING→Retire→RETIRED(终态)
任意状态─Kill→(∅)+认知快照存 snapshotStore（供复活）
⚠️ RUNNING 状态生产无人驱动（SetRunning/SetIdle 仅测试调用）——生产 agent 一生 IDLE
```

**Task Fabric**（state.go:29 完整迁移矩阵）：
```
Create→READY─Acquire→LEASED─Start→RUNNING─{Complete→COMPLETED | Fail→FAILED | Yield→SUSPENDED}
SUSPENDED─(下轮 drain re-acquire)→LEASED；过期租约→CheckExpiredLeases→READY
每迁移都过 ownerLocked 三重校验（Owner+Epoch+状态）
```

**Session**：map 存在性即状态；answer 完成→Release→reaper 收割；⚠️ answer **失败**不释放，靠 30min idle TTL 兜底（已知泄漏模式，session_registry.go:192 注释自认）。

**Strategy**：CANDIDATE→(G1/G2/G3)→ACTIVE→(watch 降级)→回滚+黑名单；promote 节流 MinActiveDuration。

**System Runtime**：六支柱 adopt 进 orchestrator（kernel.go:93 依赖边），逆拓扑 30s 预算优雅停机。

**死亡级联**（chaos kill 执行中 agent）：Kill+快照 → 下轮 drain 候选消失+stale-winner 分支 → 在途量子 Fail 回 READY/租约过期回 READY → 复活或替身 → 终态解绑。核心设计："**Agent 可弃，Task 持久**"。

## 5. 存储布局

| 存储 | 内容 |
|---|---|
| PostgreSQL | events、event_summaries、evolution_strategies、evolution_rollback_events、agent_checkpoints、evidence_records、experiences(向量+异步回填 embedding)、knowledge_chunks、secrets、tools… |
| SQLite | akf_objects/akf_representations（知识图谱）、skills FTS5 |
| 内存 | 无 PG 时的生产事件总线（compactableStore：归档+压缩）、默认 evidence store、无 PG 时策略库（PG 模式事件总线见 PostgreSQL 行，M4.1 起接线） |
| 文件 | round_N.json 归档（原子写+轮转）、~/.ares/experience.json |

## 6. 断线台账（诚实清单；2026-09-09 全清——17 项全 ✅；§8 收敛遗留同步全清）

| # | 断线 | 证据 | 状态 |
|---|---|---|---|
| 1 | agentipc DualTrackDispatcher `.Dispatch()` 死方法：生产零调用，HTTP 直捅 fabric | agentipc/policy.go | ✅ 已删（死方法+snapshot/compareShadow/Mismatches 孤儿链；外观类型保留——协作主题仍用 bus.Send） |
| 2 | peer 直连消息必失败：SendMessage 要求非 nil 队列而 peer 壳传 nil；仅 3 个协作主题能绕行 | evolution.go:412, agent.go:1799 | ✅ 已修（M3.4，e1ae5ebc）：SendMessage/ReceiveMessage/messageQueue 路径删除（TODO(tech-debt) 留痕）；peer 主题改报错，三个协作主题（delegate/pipeline/orchestrate）保留为唯一通道 |
| 3 | **生产并发度=1** | agent.go:1063, scheduler.go drainLimit | ✅ 已修：auto 模式取 max(静态注册表, fabric 空闲候选数)；新增 `kernel.max_concurrent` 配置 |
| 4 | 事件不持久：serve 用 compactableStore（内存+归档，非裸 MemoryEventStore）→ 重启丢事件流与 fitness 证据 | serve.go:189 | ✅ 已修（M4.1，前批）：`storage.enabled` 时 newServeEventStore 走 PostgresEventStore（fail-loud）；M4 三批补 `storage.events_retention_days` retention cleaner（默认 0 永不清除）。PG 无 round 归档/compaction 是设计使然（表即持久历史） |
| 5 | answer 任务终态失败不释放会话 | l2graph.go | ✅ 已修：`task.failed`(state=FAILED, capability=ares/answer) 事件订阅即释放，reaper 正常收割 |
| 6 | answer 无合成器：内容自持或报"no answer content"，不综合前驱输出 | l2graph.go:374 TODO | ✅ 已修（M4.2，e1ae5ebc）：answerSynthesizer 复用 assembleContext 前驱历史 + 无工具 LLM 调用合成终答，失败降级 gap body 不烧重试预算 |
| 7 | 经验→spawn 先验只写不读 | lifecycle.go:116 | ✅ 已修（M4.3，e1ae5ebc）：executing_agent_id 盖章→planner 经验先验读侧（experiencePriorSystemMessage，注入先导 system 消息，4096 rune 截断） |
| 8 | skill 置信只读不写（recorder 饿死） | bootstrap.go:317 | ✅ 已修（M4.4，2026-09-08 二批）：老 recorder 双重死结（sub_task.result 无发射方 + task_desc pattern 读侧永不查询）→ 换 taskfabric 终态事件流（recordLocked 盖 capability 键）→ skillOutcomeWriter 写 `Experience.Record(capability, capability, rate)`；连带修读侧遮蔽（tracker 中性 1.0 挤掉先验）——scheduler 对未测量候选置零让 `Fabric.Schedule` 填先验，测量值恒胜先验。闭环测试 skill_outcome_writer_test |
| 9 | distilled_memories 表零生产调用（schema 幽灵） | tool_deps.go:19 | ✅ 已修（M5 前小批）：repo interface/impl + distilled_memory_search 工具 + `db create-table`/`db check-rls` 幽灵 CLI 子命令下葬（`DistilledRepo` 全仓零生产赋值——表零读写坐实）；user_profile 保留（memoryMgr 路径是活的，蒸馏库分支删除）；DDL 留在 migrate_storage.go（存量库幂等，TODO(tech-debt) 留痕）。RAG 两路（experiences/knowledge_chunks）不受影响 |
| 10 | IPC 无重试/死信 | agentipc/deadletter.go 空挂 | ✅ 已修（M5 前小批，观察侧）：实况修正——store 并非空挂（Send/Request/Broadcast 5 处失败路径已 Record），真缺口是**写而不读**。闭环：evolutionIPCBridge.DeadLetterCount() 访问器 + serve 30s 周期日志（计数变化时 Warn + reason 聚合）；不自动重投递（handler 拒绝非瞬态失败，重投是操作员决策）——测试 deadletter_visibility_test |
| 11 | memory.finalize 事件：无发射方无订阅方 | types.go:70 | ✅ 已删（M3.3，含文档枚举清理） |
| 12 | ~~死旋钮 dispatch_timeout~~ | — | ✅ 已删（常量+字段+解析+默认值） |
| 13 | CanRetry 语义矛盾 | task.go | ✅ 已修：`Attempts < MaxRetries`，0/负=不重试；CompilePlan 未设预算默认 2 |
| 14 | RestartPolicy.Backoff 从不 sleep | recovery.go | ✅ 已修：`min(Backoff<<attempts, MaxBackoff)`，ctx 可取消，sleeper 可注入 |
| 15 | 孤儿符号 ErrAgentNotIdle / EventTaskStolen / StateDeclared | — | ✅ 已删（含 ares_events/introspect 联动清理） |
| 16 | ~~flushAppends 无 deadline~~（**误报**，2026-09-08 核实） | fabric.go:693/697 `flushAppendTimeout=10s`、`flushOrderWaitTimeout=30s` 均已存在；排序屏障超时→跳过、must-persist 失败→记 divergence 日志，丢/重试语义已被代码行为定义 | ✅ 已具备，非缺陷（DEEP_CODE_REVIEW 误报；grep 的 `\|` 字面量误判） |
| 17 | **HasCapableExecutor 与派发器"能否调度"双实现** | executor_registry.go `buildCandidates`；fabric_executor.go `appendFabricCandidates` | ✅ 已修（2026-09-08）：新增共享 `buildCandidates(taskID)`，`HasCapableExecutor` 与调度共用同一候选源，谓词=对共享候选 `Score(task.Capability, cand)>0`。**连带修复隐藏 bug**：旧 fabric 分支构造候选不带 Confidence → `Score` 恒 0 → 该分支实为死代码（fabric-only 能力 agent 被判"无候选"）；`TestHasCapableExecutorSourcesFabricCandidates` 锁死新行为 |

另（深审 DEEP_CODE_REVIEW 对账补充）：restore.go 持久化恢复的未检查断言已修（坏 payload 拒绝折叠）；10 处类型断言/11 处 nilnil/4 处无目标 nolint 已处置；arena 四个弃用成员确认为"仅测试调用"，已留 `TODO(tech-debt)` 痕。**api/ 91 引用已清零（M5 内部化，2026-09-09）：internal/sdk/cmd/compat 无 api/ import，api/ 降级纯转发层供 examples；剩余 api/discovery|evolution 无内部消费者**。

## 7. 旁观者模块（不在热路径）

- sub.Agent（agents/sub）：身份层 + recovery 保险执行面（newPeerExecutor 走 cognitionExecutor 驱动 L2 router）。死面（messageHandler/heartbeatSender/actionLog/tools 字段/EnableTools/SubAgentCognition）已随 M5 收官批下葬。
- runtime.Manager（leader 运行时）：注册表+HTTP 操作面，"agents are scheduled, not orchestrated"（serve.go:382）。
- PluginBus 能力插件：只注册 LoopPlugin（轮次时钟），CapCheckpoint/CapMemory/CapEvolution 无生产注册者。
- legacy evolution scheduler / dream cycle：config gate 关闭。
- compat/（内部引用已清零，目录留待 0.4.x release-note 决策）、api/（纯转发层，examples 在用）、arena、dashboard 遗留面。

## 8. 架构收敛遗留（2026-09-09 架构评审台账；同日全收敛——7/7 处置完毕）

| # | 问题 | 状态与处置 |
|---|------|------------|
| **A1** | 双认知路径并存 | ✅ 定性收敛（2026-09-09）：agentloop 是 SDK `Agent.Run` 的 by-design 同步 ReAct 执行体（examples 经 sdk 间接消费），L2 router 是 serve 的图生长执行面——产品语义不同，非重复实现；两者共享 llmcore 契约原语（LLMMessage/Tool/ToolExecutor），漂移面集中在 tool whitelist 语义（已有 engine_test 锁定）。退役 agentloop = SDK 产品决策，超出代码收敛范围。 |
| **A2** | fabric→runtime 反向依赖无测试保护 | ✅ 已修（2026-09-09）：新增 `internal/fabric/task/architecture_test.go` `TestFabricCoreMustNotImportRuntime`——锁 task 顶层 + agent + planprojection 三包禁 import internal/runtime（测试跳过；workflow/ 子树的 evolution-patch 应用面为既定评审过的 seam，gate 注释明示边界）。 |
| **A3** | `CostUSD` 恒 0，缺模型价目表 | ✅ 已决策移除（2026-09-09）：USD 货币化不做——token 维度即成本信号（costPenalty 1/(1+tokens/100k)），StrategySample.CostUSD 占位字段与 cost_usd payload 键已删。observability 的 `ARES_cost_usd_total` 是独立既有指标面，另行评估。 |
| **A4** | `distilled_memories` 幽灵 DB 表 | ✅ 已修（2026-09-09）：migrate_storage.go 的整族 DDL（表+RLS+7 索引+content_hash+去重索引+updated_at，即原语句 9-12）删除——新部署不再建废表；存量库不受影响（语句本就 IF NOT EXISTS，删除对其惰性）。删表数据属操作员决策，不进 schema migration。 |
| **A5** | `compat/` 整删待决策 | 保持（既定边界）：patch 线（v0.3.x）删导出包是 breaking change，整删属 0.4.x release-note 决策（compat/doc.go 书面政策）。内部引用已清零（2026-09-09）。 |
| **A6** | AKG BETA、新老 `arena` 并存 | ✅ 已修（2026-09-09）：实况修正——`internal/runtime/arena.go`（ArenaPlugin，plugin-bus 时代的故障注入 demo）零生产消费，已下葬（architecture_test 符号清单同步）；`internal/runtime/arena/`（RegressionTester 家族）是活包（5 生产消费者），无双实现残留。AKG BETA 状态见 knowledgeapi doc（稳定性计划属产品线）。 |
| **A7** | `api/evolution`、`api/discovery` 未内部化 | ✅ 已修（2026-09-09）：M5 三步完成——api/evolution（含 genome/mutation 子包）→ `internal/evoapi`，api/discovery → `internal/discoveryapi`；api/ 侧转发层 + test/apifwd 编译锁扩展（含方法集断言）；go doc 前后符号 IDENTICAL；examples 6 目录经转发层继续编译。api/ 目录现为纯转发层，整删只差 examples 迁移 + 0.4.x 决策。 |

### 台账说明
本节 2026-09-09 全收敛：A1 定性、A2 锁、A3 决策移除、A4/A6 下葬、A7 内部化；A5 保持既定 0.4.x 边界。无开放项。
