# ARES AgentOS 架构文档（单一事实源）

> 分工：本文档管**设计依据、分层规则、路线图**；RUNTIME.md 管**当前运行时实况**（主链/调度/数据流/状态机/断线台账，全部带 file:line 锚点）。
> 历史附录 A/B/C（ARCHITECTURE_AND_DATAFLOW / ARES_PROJECT_REVIEW / TOOL_DAG_MAINLINE_DESIGN）已折并入本文档与 RUNTIME.md，原文档已删除（commit 96c3ed70 收拢；本版为 2026-09-08 追踪重写版）。

---

## 一、定位与不变量

ARES 是 **"Agent 操作系统"**：Agent 不是被编排的工作流节点，而是**被调度的进程**——对话历史是可序列化、可迁移、可恢复的进程状态，由 checkpoint + lease + epoch fencing 三件套坐实。

**架构不变量**（实施与重构时不得违反）：

| 不变量 | 实现位置 |
|---|---|
| 一个内核：所有 agent/task 调度决策经过 internal/kernel | scheduler.go；kernel 不 import runtime（architecture_test.go 锁定） |
| 一张图：workflow/engine.MutableDAG 是全仓唯一任务图载体（L1 作动面 + L2 图） | fabric/task/workflow/engine/mutable_dag.go |
| 一条主线：cmd/ares 唯一 CLI 入口；L2 router 是唯一生产执行路径 | cmd/ares 7 文件；fabric/agent/l2graph.go |
| 无领导者调度："B 完成→C 就绪"由织物状态机推导，不靠中心编排 | fabric/task/dag.go |
| 执行量子可恢复：yield 时 checkpoint 持久化，resume 从断点续跑 | fabric/task/quantum.go:48 |
| Epoch fencing：过期持有者不能驱动已易主任务 | fabric/task/fabric.go:267 Acquire / ownerLocked:669 |
| 协作式抢占：不做 OS 式硬抢占，只在量子边界 | fabric.go:570 Preempt |
| 规划者不执行工具，只生长图（深度上限 10） | fabric/agent/planner_cognition.go |
| syscall 调用者身份来自 kernelctx，绝不信任 LLM 参数 | agentsyscall/syscall.go |
| 接口定义在消费者侧 | 全仓惯例 |
| "Agent 可弃，Task 持久"：agent 死亡靠租约过期+快照复活，任务状态不丢 | lifecycle.go + recovery.go |

## 二、分层

```
入口层   cmd/ares（7 文件）· sdk/（极简 SDK，与 CLI 共用同一引擎）· api/（deprecated→sdk，迁移窗口）
组装层   internal/ares_bootstrap（唯一装配根：依赖注入、G1/G2/G3 门禁装配）
核心     internal/kernel（唯一调度器：Scheduler·CapabilityExecutor·Orchestrator=系统运行时）
         internal/fabric（唯一编排层：agent/=Agent Fabric·task/=Task Fabric+workflow/engine·planprojection）
         internal/runtime（生命周期+子系统：ares_evolution(GA v1)+evolution(v2)·memory·eval·arena·observability(+flight)·protocol·archive）
基础设施  tools·knowledge·storage·llm·ares_events·agentipc·agentsyscall·aresrecovery·introspect·ares_config·ares_security + ~15 小 ares_*/领域包
边缘     compat/（provide_llm 日落门）· services/embedding · examples/_fixtures（降级 demo）
```

层级边界：kernel（调度）⊄ fabric（编排）——`fabric_executor.go` 是 kernel→fabric 的桥接。runtime 承担生命周期编排，agentfabric.Spawn 是其 spawn 原语，职责边界 = 编排(Manager) vs 原语(Spawn)。

## 三、收敛收尾计划（2026-09-08 起）

收敛主体已完成（38→7 CLI 文件、fabric/kernel/runtime 三核心落位、冻结巡查在线）。剩余工作按里程碑组织，**每步验收统一为：build + vet + golangci-lint + test 全绿；涉及删除的追加 grep 全仓引用清零**。

### M1 — 文档与事实对齐 ✅（本次）
- RUNTIME.md 落盘（运行时全景 + 断线台账 15 项，全带锚点）；本文档重写为设计+路线图，过时附录（chatCognition 路径描述、已修缺陷的开放状态等）折并删除。
- 旧缺陷台账状态重标：P0-1a（会话 idle TTL）✅已修；P0-1b（session_id 斜杠校验，agent.go:2214）✅已修；P0-1c（answer 后同 ID 重提交 harvest，agent.go:2265）✅已修；P1-4（ReAct 双实现）✅已随 M4-D 删除。其余未修项折入 M2-M4。

### M2 — 代码修缮批（小步，每项独立提交）
| # | 项 | 位置 | 改动 |
|---|---|---|---|
| 1 | 并发度=1（bug 级） | agent.go:1063 | `WithMaxConcurrent(0)` → 显式 peer 数或 `kernel.max_concurrent` 配置 |
| 2 | answer 失败不释放会话 | l2graph.go:395 | Fail 路径也调 ReleaseSession + 测试 |
| 3 | CanRetry 语义矛盾 | task.go:53 vs :61 | 修代码使 0=不重试（与注释一致），查所有零值 RetryPolicy 构造点 |
| 4 | RestartPolicy.Backoff 不 sleep | recovery.go:55 | 补 backoff 睡眠 |
| 5 | 死旋钮 dispatch_timeout | kernel.go:618 | 删配置项 |
| 6 | 孤儿符号 | ErrAgentNotIdle / EventTaskStolen / StateDeclared | 删 |

### M3 — 死代码下葬批
1. agentipc DualTrackDispatcher 生产链（协作主题保留 bus.Send）
2. SkillOutcomeRecorder（写侧饿死）
3. memory.finalize 事件类型
4. sub.Agent 的 messageQueue 路径（peer 直连必失败），保留身份壳
5. distilled_memories 三件套（schema 幽灵，建议删不接线——RAG 已有 experiences/knowledge 两路）

### M4 — 半接环补全批（按业务价值排）
1. **事件持久化**（价值最高）：`storage.enabled` 时 serve 接 PostgresEventStore 替换内存版（serve.go:191）——重启不再清零 fitness 证据
2. **answer 合成器**：复用 planner 的 assembleContext 沿图路径收集前驱输出 + 一次 LLM 总结（l2graph.go:374 TODO）
3. 经验→spawn 先验读侧：planner assembleContext 注入 ExperiencePrior
4. skill 置信写侧：修 tool_observer 事件形状（或随 M3.2 删）

### M5 — api/、agents/ 下葬（独立 PR，前置 M1-M3）
- api/ 生产引用迁到 sdk/（按 MIGRATION.md）；agents/ 只留 StrategySource 等活符号，sub 壳下葬。
- compat/ 以 provide_llm 解耦为门，删除时按惯例留 `TODO(tech-debt)`。

### 完成状态总表（历史收敛，一行化）
- Phase 0 冻结巡查 ✅（scripts/check_convergence_freeze.sh + freeze-manifest）
- Phase 1 内核单一化 ✅ · Phase 2b fabric 合并 ✅ · M4-D ReAct 删除 ✅（tag convergence/pre-m4-d）
- Phase 3 运行时服务化 ✅ · Phase 4 CLI 收敛 38→7 ✅（345 顶层符号零丢失；review 修订后 344：cognitionTaskExecutor 并入 cognitionExecutor）· Phase 5 验证 ✅
- tool DAG 主线 M0/M1/M1.5/M2/M3/M4/M5/M6 ✅ 落地（增量编译器/L2 图容器/planner 生长/上下文图拼装/ReAct 删除/L1 能力图/统计回灌 fitness）

## 四、回滚

- M4-D 之前的形态：tag `convergence/pre-m4-d`。
- 每个删除性里程碑（M3/M5）独立 commit，可单独 revert。
- 策略级回滚运行时已内建（G1 门 + 30s watch + 黑名单，RUNTIME.md §3）。

## 五、高风险事项（保留自旧计划）

1. `workflow/engine/` 严禁删除——L1 进化作动面（DAGPatchExecutor/WorkflowGenome）+ L2 图载体。
2. `cmd/ares` 非测试文件数是 freeze-check 的锚点，改动需同步 manifest。
3. 双包同名 `evolution`（ares_evolution v1 / evolution v2）**分层保留不合并**——M6 fitness 长在 v1。
4. `compat/`、`api/` 的删除以各自日落门（provide_llm 解耦 / sdk 迁移）为前提，不提前物理删除。

## 六、验证基线（当前状态，2026-09-08）

`go build ./...` ✅ · `go vet ./...` ✅ · `golangci-lint run ./...` 0 issues ✅ · `go test ./...` 132 包全过 ✅ · freeze-check ✅ · 最近 10 个 commit 逐一独立构建通过（bisectability）✅
