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
      → fabric.Schedule → Acquire (CAS+epoch fencing, fabric.go:267)
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
- **防双执行两层**：`Acquire` CAS（READY/SUSPENDED 只有一个赢家）+ 全局 `epoch++` fencing——A 租约过期被 B 接管后，A 迟到的 complete/release 被 `ErrEpochMismatch` 拒绝（fabric.go:669）。
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

**经验环 ⚠️ 半闭合**：
- 蒸馏→GA 突变 hints ✅（provide_distillation.go:90）
- 蒸馏→RAG prompt 注入 ✅（retriever_wiring.go:80）
- ExperiencePrior→CognitiveState **只写不读**（lifecycle.go:116 写入后，L2 路径无读方）
- skill 经验→调度置信 **只读不写**（recorder 的事件形状无生产发射方，bootstrap.go:317 自认"starved"）

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
| 内存 | **生产事件总线**（serve.go:191 MemoryEventStore——PG 版完整但未接线）、默认 evidence store、无 PG 时策略库 |
| 文件 | round_N.json 归档（原子写+轮转）、~/.ares/experience.json |

## 6. 断线台账（诚实清单）

| # | 断线 | 证据 | 处置建议 |
|---|---|---|---|
| 1 | agentipc DualTrackDispatcher 整链死线：`enableKernelExecution` 挂了 executeFn 但全仓生产零调用 `.Dispatch()`，HTTP 直捅 fabric | kernel.go:367-443 | 删或改构（协作主题仍用 bus.Send） |
| 2 | peer 直连消息必失败：SendMessage 要求非 nil 队列而 peer 壳传 nil；仅 3 个协作主题能绕行 | evolution.go:412, agent.go:1799 | 删 sub 消息队列路径 |
| 3 | **生产并发度=1**：`WithMaxConcurrent(0)` 回退 `ExecutorCount()`，peer 模式静态池故意空 → limit=1 | agent.go:1063, scheduler.go:468 | 显式设为 peer 数（bug 级） |
| 4 | 事件不持久：生产用内存 EventStore → 重启清零 fitness 证据 | serve.go:191 | 接 PostgresEventStore |
| 5 | answer 失败不释放会话（30min TTL 兜底） | l2graph.go:406 | Fail 路径也 Release |
| 6 | answer 无合成器：内容自持或报"no answer content"，不综合前驱输出 | l2graph.go:374 TODO | 专用 answer 路径走 assembleContext+LLM |
| 7 | 经验→spawn 先验只写不读 | lifecycle.go:116 | planner 读回或删 |
| 8 | skill 置信只读不写（recorder 饿死） | bootstrap.go:317 | 修事件形状或删 |
| 9 | distilled_memories 表零生产调用（schema 幽灵） | tool_deps.go:19 | 删（RAG 已两路） |
| 10 | IPC 无重试/死信 | agentipc/deadletter.go 空挂 | 随 #1 处置 |
| 11 | memory.finalize 事件：无发射方无订阅方 | types.go:70 | 删 |
| 12 | 死旋钮 `dispatch_timeout` 解析无消费者 | kernel.go:618 | 删 |
| 13 | CanRetry 注释与代码矛盾：注释 0=不重试，代码 0=无限重试 | task.go:53 vs :61 | 修语义 |
| 14 | RestartPolicy.Backoff 从不 sleep | recovery.go:55 | 补睡眠或删字段 |
| 15 | 孤儿符号：ErrAgentNotIdle / EventTaskStolen / StateDeclared | agent.go:114 等 | 删 |

## 7. 旁观者模块（不在热路径）

- sub.Agent 壳（agents/sub）：纯身份层，注册进 runtime.Manager 只服务 HTTP kill/resume legacy 回退。
- runtime.Manager（leader 运行时）：注册表+HTTP 操作面，"agents are scheduled, not orchestrated"（serve.go:382）。
- PluginBus 能力插件：只注册 LoopPlugin（轮次时钟），CapCheckpoint/CapMemory/CapEvolution 无生产注册者。
- legacy evolution scheduler / dream cycle：config gate 关闭。
- compat/、api/（deprecated，等 provide_llm 解耦）、arena、dashboard 遗留面。
