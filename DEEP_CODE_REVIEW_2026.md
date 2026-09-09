# ARES 全仓深度代码审查报告

> 审查日期: 2026-09-09
> 审查范围: 全部 ~1500 个 Go 文件、~378K 行代码
> 审查方式: 并行 18 个专项 review agent，逐文件通读
> 严重程度: CRITICAL > HIGH > MEDIUM > LOW

---

## 统计摘要

| 严重程度 | 数量 | 说明 |
|---------|------|------|
| **CRITICAL** | 9 | 数据丢失、死锁、进程崩溃、沙箱逃逸 |
| **HIGH** | ~55 | 并发 bug、资源泄漏、静默功能失效、安全漏洞 |
| **MEDIUM** | ~150 | 设计缺陷、边界条件、错误处理 |
| **LOW** | ~100 | 代码质量、死代码、文档不一致 |

---

## 一、CRITICAL（必须立即修复）

### 1.1 fabric: ReplaceNode 丢弃新依赖边
- **文件**: `internal/fabric/task/workflow/engine/mutable_dag.go:805-826`
- **问题**: 不同 ID 的 `ReplaceNode` 只迁移已有边，新 Step 的 `DependsOn` 中不在旧边里的条目永远不会成为 DAG 边。`GetExecutionOrder` 少算前置依赖，替换节点可能在其声明依赖之前被调度。
- **已验证**: 通过 throwaway test 确认。
- **修复**: 迁移块后添加与 same-ID 分支相同的 "add new DependsOn edges" 循环。

### 1.2 fabric: nil-Yield 擦除任务持久化进度
- **文件**: `internal/fabric/task/fabric.go:338-356`
- **问题**: `Yield` 无条件执行 `t.Checkpoint = checkpoint`。任何返回 `(nil, false, nil)` 的量子会擦除前一个量子或提交信封保存的 checkpoint（含 Payload/StrategyID/SessionID/token 统计）。
- **已验证**: 通过 throwaway test 确认。
- **修复**: 仅在 `checkpoint != nil` 时覆盖。

### 1.3 fabric: Restore 静默丢弃空 Capability 的任务
- **文件**: `internal/fabric/task/restore.go:181-184`
- **问题**: `recordLocked` 仅在 `Capability != ""` 时写入 restore key，但 `foldRestoreEvent` 要求该 key 存在。空 capability 是合法的（无约束任务），重启后此类任务从 fabric 中消失。
- **已验证**: 通过 throwaway test 确认。
- **修复**: 无条件写入 capability key（空字符串有意义）。

### 1.4 ares_events: PostgresEventStore.Subscribe 永久卡死
- **文件**: `internal/ares_events/pg_store.go:486-523`
- **问题**: 查询 `ORDER BY created_at ASC LIMIT 100` 总是返回最旧的 100 行。当窗口内 ≥100 个事件时：poll 1 交付 E1..E100（满页 → 游标不动）；poll 2 重读 E1..E100（已全部在 delivered 集合中 → batch 为空 → 游标仍不动）。E101+ 永远不会被交付。一次 100 事件的突发就永久杀死该订阅者。
- **修复**: 使用 keyset 分页：每次非空页将游标推进到 `events[len-1].Timestamp`。

### 1.5 knowledge: PG Stream 可向容量为 1 的 channel 发送多次错误 → 死锁
- **文件**: `internal/knowledge/provider/postgres/provider.go:147-166`
- **问题**: `Stream` 向 `make(chan error, 1)` 发送多次错误。唯一消费者只读一次。第二次发送永久阻塞 → `objCh`/`errCh` 永不关闭 → `loadAndProcess` 永久阻塞。goroutine 泄漏 + `*sql.Rows` 泄漏。
- **修复**: 捕获第一个错误，循环后只发送一次。

### 1.6 mcp: 连接生命周期绑定到调用者 context → skill_activate 杀死子进程
- **文件**: `internal/runtime/protocol/mcp/manager.go:203`
- **问题**: `connectWithTransport` 将 MCP 客户端（及其 stdio 子进程）绑定到调用者的 context。`skill_activate` 返回后 dispatcher 取消该 context → 所有刚懒连接的 MCP 服务器被杀死。`factory.go:108` 已用 `ConnectWithLifetime` 规避此问题，但 manager（生产路径）没有。
- **修复**: 使用 `client.ConnectWithLifetime(ctx, context.Background(), transport)`。

### 1.7 mcp: SSE server handler 每个断连客户端泄漏一个 goroutine
- **文件**: `internal/runtime/protocol/mcp/transport_server.go:317-323`
- **问题**: defer 先从 `t.sessions` 删除 session，然后 `for range msgCh {}`——但 `Close()`（唯一关闭 msgCh 的地方）已经找不到该 session 了。每个正常客户端断连都会永久泄漏一个 goroutine。
- **修复**: 删除 drain 循环（Send 已用非阻塞 select），或在删除后在同一把锁下关闭 channel。

### 1.8 sdk: YAML `llm.temperature` 和 `llm.max_tokens` 验证后被静默丢弃
- **文件**: `sdk/config.go:354-367`
- **问题**: `ToOptions` 桥接了 `MaxPromptLength` 但从未应用 `Temperature` 或 `MaxTokens`。用户在 `ares.yaml` 中配置的值通过验证后运行时使用硬编码默认值 0.7/2048。
- **修复**: 在 `ToOptions` 中，当非零时发出复制这两个字段的 option。

### 1.9 agents/sub: Stop 并发调用 double-close panic
- **文件**: `internal/agents/sub/agent.go:230-252`
- **问题**: 两个并发 `Stop` 调用都通过 `status == Offline` 守卫（第一个设 `Stopping`，不等于 `Offline`），都读取同一个 `stopCh`，都调用 `close(stopCh)` → **`panic: close of closed channel`**，进程崩溃。已验证。
- **修复**: 在锁下将 channel 置 nil 后再关闭；`Stopping` 状态提前返回。

---

## 二、HIGH（应尽快修复）

### 2.1 kernel

| # | 位置 | 问题 |
|---|------|------|
| 1 | `scheduler.go:1158-1188` | `ToModelTask` 通过别名写入共享 checkpoint-envelope Payload map，永久污染持久化负载 |
| 2 | `scheduler.go:566-594` | `PreemptLowerPriority` 抢占所有低优先级 RUNNING 量子（非仅够用），背景 sweeper 在量子中途调用（与注释矛盾） |
| 3 | `scheduler.go:826-883` | panic-guard 注册间隙：`TryBegin` 后、defer 注册前，`beforeQuantum` 用户代码 panic 会永久泄漏 load slot |

### 2.2 fabric

| # | 位置 | 问题 |
|---|------|------|
| 4 | `planprojection/coordinator.go:112-140` | `CompileDAG` 先删旧任务再编译新任务；编译失败时已删任务永久丢失 |
| 5 | `task/fabric.go:893-937` | 持久化追加排序屏障在一次超时后永久降级：跳过的 seq 不推进 `flushedSeq`，后续每个事件都等 30s |
| 6 | `engine/mutable_dag.go:417-429` | `Steps()`/`StepIndex()` 返回活指针，同时 `AddEdge`/`RemoveEdge` 在锁下变异同一批对象 → 数据竞争 |
| 7 | `task/reaper.go:81-114` | Reaper 删除 COMPLETED 前驱后，仍引用它的 READY 依赖永久不可调度 |

### 2.3 runtime

| # | 位置 | 问题 |
|---|------|------|
| 8 | `manager.go:342-410` | `RestartAgent` 不检查 `isStopped`/`isStarted` 就调用 `errgroup.Go` → WaitGroup 复用 panic |
| 9 | `manager_chaos.go:111-141` | `ResumeAgent` 同样缺少生命周期检查 → 同类 panic / 不可停止的孤儿 goroutine |
| 10 | `memory/distillation/distiller.go:575-577` | `ReplaceOld` 冲突策略从不删除旧经验 → 冲突解决是写侧 no-op，近重复累积 |
| 11 | `memory/experienceadapters/adapters.go:179-198` | `CountByMemoryType` 从 `ListByType(limit=1000)` 派生计数，`MaxSolutionsPerTenant=5000` 的上限永远不触发 |
| 12 | `memory/manager_impl.go:301-311` | `SetEventStore` 无锁写入，`emitEvent` 无锁读取 → 数据竞争 |
| 13 | `observability/metrics_tracer.go:49-53` | `WithTrace` 在生产中从未被调用 → 所有依赖 TraceID 的功能（成本仪表盘）永久为空 |
| 14 | `observability/metrics_tracer.go:52` | token 输入/输出拆分用 50/50 硬编码 → 所有模型的成本计算系统性错误 |
| 15 | `observability/flight/collector.go:158-170` | 事件消费热路径上同步执行 evidence-store DB I/O，配合 drop-on-full 发布者 → 静默数据丢失 |
| 16 | `observability/flight/graph.go:101-104` | 多 agent 运行时每个新 agent 覆盖 `g.root` → 导出只渲染最后启动的 agent 子树 |
| 17 | `observability/flight/genealogy.go` | Genealogy 完全无上限，agent 轮换（复活）导致无界内存增长 |

### 2.4 ares_evolution

| # | 位置 | 问题 |
|---|------|------|
| 18 | `genome/population.go:422-425` | `doEvolve` 持写锁调用 `Stats()`（内部取读锁）→ 非可重入 RWMutex 自死锁 |
| 19 | `genome/population.go:350-361` | Steady-state 模式用循环幸存者克隆填充种群 → 重复精英 ID + 丢弃幸存者 |
| 20 | `genome/multi_objective.go:206-221` | minimize 维度取反原始值 → 聚合分可为负 → 被 `IsScoreEvaluated` 误判为"未评估" |
| 21 | `genome/spatial_index.go:85-95` | 空间索引用归一化空间的 cellSize 量化原始参数值 → 邻居计数错误 |
| 22 | `service/service_bridge.go:15-28` | `apiGuidanceBridge` 丢弃 `Confidence` 字段 → 经验引导变异在公共 API 路径静默失效 |
| 23 | `lifecycle.go:685-692` | `heldCandidateIDLocked()` 在 `Unlock()` 后被调用 → 数据竞争 |
| 24 | `guardrails_failclosed.go:6-23` | "fail-closed" 守卫实际不总是阻止；`NewEvolutionGuardrails` 永不返回错误 → 安全网死代码 |
| 25 | `scheduler.go:327,754,803` | `adapter`/`scoreProvider`/`dreamCycle` 在 setter 下写入但在活路径无锁读取 → 数据竞争 |
| 26 | `fitness_aggregator.go:356-404` | 客户端过滤在 LIMIT 查询之后 → 多策略流量下回滚安全网永久失效 |

### 2.5 knowledge

| # | 位置 | 问题 |
|---|------|------|
| 27 | `store/memory/store.go:42,217` | `Save` 存储调用者指针，`HybridSearch` 返回同一批指针 → 并发写竞争 |
| 28 | `pipeline.go:88,152-157` | `resolvedObjects` 无界增长，每次 `Process` 做 O(n) 快照 → 总成本 O(n²) |
| 29 | `store/postgres/store.go:365-388` | `HybridSearch` 无 LIMIT 全量加载候选行（含 raw BYTEA）→ 大表 OOM |
| 30 | `service/adapter.go:102-107` | `Distill` 用 payload 字节长度作为对象 ID → 不同等长内容 ID 碰撞 |

### 2.6 storage

| # | 位置 | 问题 |
|---|------|------|
| 31 | `repositories/knowledge_repository.go:85-88` | `content_hash` 全局 UNIQUE 而非 per-tenant → 跨租户完整性损坏 + 静默数据丢失 |
| 32 | `repositories/secret_repository.go:164-197` | `List` 扫描 NULL `expires_at` 到非指针 `time.Time` → 大多数秘密被静默跳过 |
| 33 | `repositories/secret_repository.go:481-493` | `Export` 基于 `List`（不含 value 列）→ 备份丢失所有秘密值 |
| 34 | `services/retrieval_helpers.go:177-194` | `replaceAllIgnoreCase` 逐字节转 string → UTF-8 多字节字符损坏 |
| 35 | `repositories/experience_repository_memory.go:110-122` | 内存版 `Update` 不验证 TenantID → 跨租户覆盖 |
| 36 | `repositories/experience_repository_memory.go:64,96,119` | 浅拷贝共享 Embedding slice 和 Metadata map → 并发竞争 |

### 2.7 agentipc / aresrecovery / ares_bootstrap

| # | 位置 | 问题 |
|---|------|------|
| 37 | `agentipc/primitives.go:259-270` | 超时后 handler 错误重新插入 `pendingErr` → 永久 map 泄漏 |
| 38 | `agentipc/primitives.go:44-101` | `Send`/`Broadcast` 同步调用 handler 无 recover → panic 杀死进程 |
| 39 | `aresrecovery/recovery.go:262-270` | `RestartAgent` 锁外读-改-写重启预算 → 并发绕过预算 |
| 40 | `ares_bootstrap/population_policy_source.go:49-84` | 非空 spawn 策略每分钟重复应用 → 无界 agent 生成 |
| 41 | `ares_bootstrap/bootstrap.go:338,375,608` | `runCleanups` 只运行 `cleanups` 切片 → 失败的 Bootstrap 留下仍在运行的后台 worker |

### 2.8 llm

| # | 位置 | 问题 |
|---|------|------|
| 42 | `chat.go:310-316` | OpenAI 路径从不复制 `Usage` 到 `respCore` → token 计数永远为 0 |
| 43 | `chat.go:551-562` | Anthropic 路径为每个 tool 消息发独立 user 消息 → 并行工具调用时 HTTP 400 |

### 2.9 compat / api / mcpclient

| # | 位置 | 问题 |
|---|------|------|
| 44 | `compat/protocol/openai_api/openai_api.go:133-151` | `detectEndpoint` 将 Responses API 数组 input 路由到 embeddings → 返回无意义嵌入 |
| 45 | `compat/vector/pgvector/pgvector.go:136-140` | `Close()` 无同步设置 nil → 并发 use-after-close panic |
| 46 | `api/service/llm/service.go:13` | `toInternal()` 对 nil `Fallbacks[i]` 空指针 panic |
| 47 | `mcpclient/stdio.go:65-105` | 无 JSON-RPC 响应 ID 匹配 → 通知被当作请求响应 |
| 48 | `mcpclient/sse.go:52-61` | endpoint 事件后从不再读 SSE body → TCP 窗口填满后连接停滞 |

### 2.10 tools / discovery / introspect

| # | 位置 | 问题 |
|---|------|------|
| 49 | `tools/discovery/discover.go:67-92` | 输出上限在 `cmd.Run()` 后检查 → `yes` 命令可耗尽内存 |
| 50 | `tools/planner/bridge.go:137,193` | `duplicate_id`/`empty_id` 不在硬阻止列表中 → 重复 ID 的步骤静默跳过 |
| 51 | `discovery/engine.go:136-147` | `Register()` 注册的服务被下一个 `DiscoverNow` 周期删除 |
| 52 | `discovery/identity.go:175-201` | `normalizeEndpoint` 取 URL 最后一段 → 所有 `*/mcp` URL 合并为一个身份 |
| 53 | `introspect/intel.go:80,88,90` | insight 子系统完全死代码 → `/api/insights` 永远返回 `{"count":0}` |
| 54 | `introspect/flight.go:76-79` | `TimelineSummary` 丢弃 `agentID` 参数 → 过滤器静默失效 |
| 55 | `services/embedding/bridge.go:19` | 无大小限制读取请求体 + 所有错误被 `_ =` 丢弃 |
| 56 | `runtime/eval/result_verifier.go:82-90` | 未知 check 状态静默通过 → 垃圾输入被当作 PASS |

### 2.11 agents/sub 生命周期

| # | 位置 | 问题 |
|---|------|------|
| 57 | `agents/sub/agent.go:417-480` | `ProcessStream` 的外层 `defer setStatus(Ready)` 在 channel 返回时立即触发（非任务完成）→ 破坏 Busy/Ready 准入控制 |
| 58 | `agents/sub/agent.go:255-288` | `Process` 不被 `streamWg` 追踪且忽略 `stopCh` → `Stop` 后 in-flight defer 将已停止 agent 复活为 Ready |

### 2.12 builtin tools 安全

| # | 位置 | 问题 |
|---|------|------|
| 59 | `builtin/execution/code_runner.go:347-395` | Python "沙箱" 是 denylist+regex，可被 `__subclasses__` 链绕过 → 完整主机 RCE（已验证） |
| 60 | `builtin/pdf/pdf.go:110-146` | TOCTOU：路径验证用 `EvalSymlinks` 结果，实际读取用原始路径 → 符号链接交换绕过 |
| 61 | `builtin/network/ssrf.go:88-104` | SSRF 检查遗漏 `100.64.0.0/10`（CGNAT/Kubernetes pod CIDR）等特殊用途段 |
| 62 | `builtin/text/regex_tool.go:129-155` | `max_results` 默认 -1（无限）+ 空匹配模式 → 数百万匹配 → OOM |
| 63 | `builtin/knowledge/knowledge_base.go:117-134` | `tenant_id` 来自 LLM 工具参数 → 多租户隔离完全依赖模型诚实 |

### 2.13 storage 基础设施

| # | 位置 | 问题 |
|---|------|------|
| 64 | `storage/postgres/write_buffer.go:146-172` | 毒丸活锁：不支持的 Table 的批次项永久失败 → buffer 永远满 → 所有写入失败，无 dead-letter |
| 65 | `storage/postgres/circuit_breaker.go:111-181` | `halfOpenInflight` 可为负 → `CAS(0,1)` 永久失败 → 熔断器永久卡在半开状态 |
| 66 | `storage/postgres/vector.go:62-119` | `Search` 在租户作用域表上无租户过滤 → 跨租户数据泄漏 |
| 67 | `storage/postgres/session.go:29-55` | `expired_at` 可空但 Create 绑定零值 → 首次 cleanup 即删除所有未设过期的 session |

---

## 三、MEDIUM（应计划修复）

### 3.1 kernel
- `scheduler.go:813-818` — 预算耗尽的唯一候选 → 静默永久 acquire/release 活锁
- `scheduler.go:1128-1146` — `ErrIllegalState`/`ErrTaskNotFound` 未中性化 → 无辜 agent 置信度下降
- `scheduler.go:908-914` — 优雅关闭将 `context.Canceled` 记为 agent 失败
- `scheduler.go:65,930` — `Scheduled` 计数量子而非任务
- `scheduler.go:633-665` — 绑定恢复执行器能力不重叠时任务永久搁浅
- `scheduler.go:840-908` — 无量子超时 → 卡住的执行器阻塞关闭
- `scheduler.go:481-493` — drain 信号量发送忽略 ctx 取消
- `orchestrator.go:394-429` — `Adopt` 与 `Shutdown` 竞争 → 组件永久运行
- `orchestrator.go:530-540` — 组件状态更新是非原子读-改-写
- `load_tracker.go:26-37` — tracker map 随 agent 轮换无界增长

### 3.2 fabric
- `task/workflow_plan.go:89-157` — `CompilePlan` "all-or-nothing" 保证不原子
- `graph/patcher.go:37-42` — `SetGraph`/`SetDAG` 与 `Apply` 竞争
- `graph/patcher.go:56-73` — 未绑定时 `Apply` 空指针
- `graph/patcher.go:155-175` — 节点删除回滚丢失所有边
- `engine/mutable_dag.go:573-593` — `ResetFromSteps` 不发布图事件
- `engine/mutable_dag.go:40` — `SchedulerType` 公开字段无锁变异
- `task/quantum.go:48-81` — `RunQuantum` 无 panic 边界
- `task/fabric.go:384-399` — `CompleteWithCheckpoint` 在验证转换前变异状态
- `engine/definition.go:122-137` — 字段提取正则无锚点（`username` 匹配 `name`）
- `engine/reloader.go:201-262` — 删除的 workflow 永不移除
- `engine/registry.go:200-205` — `OutputStore.Close` 后 `Set` panic
- `agent/fabric.go:168-181` — `record` 在锁外读取 `a.State`
- `agent/lifecycle.go:105-111` — `Spawn` 在 fabric 锁下调用 `CognitionFactory`
- `agent/snapshot.go:43-48` — 死亡快照存储无界增长
- `agent/planner_cognition.go:603-677` — planner 生长在量子重试间不幂等

### 3.3 runtime memory
- `distiller.go:207-224` — `UpdateConfig` 不更新 scorer/resolver/noiseFilter 的阈值
- `push/service.go:315-337` — `Stop` 可覆盖已重启 loop 的生命周期状态 → 两个并发 push loop
- `context/cleaner.go:548-668` — 策略 3（结构链接分组）不可达死代码；若可达则边界错误
- `context/session.go:185-204` — `Set` 不复制调用者 slice
- `context/task.go:131-148` — `Get` 返回活指针
- `distillation/filter.go:241-249` — SecurityFilter 关键字含大写 ASCII 但比较前 lowercase → 永不匹配
- `distillation/detector.go:31-61` — 否定关键词先于问题关键词检查 → 常见指令式问题被拒绝
- `adapters.go:317-336` — `ToStorageExperience` 更新时也刷新 `CreatedAt` → 破坏时效信号
- `distiller_admin.go:71-108` — `SubscribeAndDistill` 的 errgroup 永不 Wait
- `production_manager.go:107-207` — `MaxSessions=0` 时每个 session 立即被驱逐 → 归因到 "anonymous"
- `production_manager.go:355-399` — session 仅存在于内存缓存，重启后消息归因到 "anonymous"

### 3.4 ares_evolution
- `observer.go:125-128` — latencyScale 文档说 0 禁用，代码将 0 替换为默认值
- `rollback_policy.go:327-386` — `Deploy` 在锁下执行 DB I/O；回滚失败时错误消息与现实矛盾
- `rollback_policy.go:221-238` — `windowSize < 5` 时渐进下降检测静默失效
- `guardrails.go:573-585` — `recordEventLocked` 在写锁下调用 handler → 可重入死锁
- `genome_wiring_system.go:219-235` — 启用自适应分布静默丢弃经验引导变异
- `pg_strategy_store.go:97-144` — `GetActive` 空存储契约与接口文档矛盾
- `memory_strategy_store.go:117-137` — `dupStrategy` 浅拷贝 Params → 嵌套 map/slice 共享
- `scheduler.go:340-367` — `OnAgentEnd` 的 cancel+start 是两个独立临界区 → 并发可重复启动
- `gate_eval.go:164-166` — `beforeRun` 变异共享执行器状态但可并发调用
- `shadow_executor.go:252-263` — `cloneTask` 浅拷贝 Payload → 嵌套值在 A/B 臂间共享
- `dream_cycle.go:645-647` — 冷却仅在成功 deploy 后设置 → 早期返回路径无冷却

### 3.5 knowledge
- `store/sqlite/store.go:270-273` — SQLite 从不启用 `PRAGMA foreign_keys=ON` → CASCADE 是装饰性的
- `adapter/memory.go:66-68` — 截断按字节而非 rune → CJK 内容切碎 UTF-8
- `compiler/compiler.go:254-255` — `formatJSON` 用 `%q`（Go 字符串字面量）→ 控制字符产生无效 JSON
- `compiler/compiler.go:293-294` — `formatXML` 用 `%q` 做属性值 → `&`/`<` 破坏 XML
- `runtime/patcher.go:144-147` — `CanApply` 无锁读取 `e.runtime`
- `mcp/mcp.go:48-56` — 文档说 runtime 可为 nil，但三个 handler 直接调用 `s.Runtime.Execute`
- `provider/registry.go:75-98` — `Select` 在读锁下调用 `IntentMatch` → 潜在自死锁
- `store/sqlite/store.go:228-235` — tag 过滤用 `LIKE '%tag%'` → `a` 匹配 `ab,ac`
- `pipeline/llm_summarizer.go:123-140` — 不可信内容直接拼入 prompt → 间接 prompt 注入
- `provider/vector/provider.go:101` — `_ = store.CreateCollection(...)` 丢弃所有错误

### 3.5b storage 基础设施补充
- `write_buffer.go:79-101` — `Start` 无双重启动守卫 → 两个 `processLoop` 消费同一 channel
- `pool.go:227-254` — `QueryWithTenant` 设置连接级 GUC 但无 finalizer → 遗忘 `Close()` 导致租户绑定泄漏
- `migrate.go:71-77` — `embeddings` 表是 `VECTOR(1536)` 而其他地方是 1024 → 维度不匹配
- `vector.go:205-218` — `CREATE TABLE; CREATE INDEX` 多语句在一个 `ExecContext` 中 → pgx 拒绝
- `repository.go:183-194` — `SaveProfile` exists-then-Create TOCTOU → 并发下重复键
- `adapters/secret_adapter.go:187-210` — 秘密 key/value 未加引号写入 YAML → 含 `:`/换行的值破坏结构
- `session.go:29-55` — `expired_at` 零值绑定导致 session 在首次 cleanup 被删除
- `embedding/cache.go:316-342` — 返回/存储共享 slice

### 3.6 ares_events
- `pg_store.go:128-144` — `Append` 无 `FOR UPDATE` → 并发追加静默丢事件
- `summary_repository.go:131-139` — 遍历后不检查 `rows.Err()` → 部分结果集被当作成功
- `compactable_store.go:209-247` — 压缩后 Read 回退忽略 ReadOptions
- `compactable_store.go:473-505` — 归档 claim CAS 竞争 → 重复 round
- `compactor.go:113-118` — `compactStream` 无 Limit 全量读取 → 大流 OOM
- `compactor.go:41-51` — `SummaryTTL=0` 时首次清理删除所有摘要
- `summary.go:100-102` — `MaxSummariesPerStream` 无处执行

### 3.7 storage
- `repositories/conversation_repository.go:135-137` — NULL `expires_at` 扫描到非指针 → 行被静默跳过
- `repositories/task_result_repository.go:55,229` — 空 embedding 产生 `"[]"` → `::vector` cast 失败
- `services/retrieval_search.go:43-58` — embedding 失败被记录为成功 → 熔断器永不打开
- `services/retrieval_service.go:218-220` — `SetEmbeddingPipeline` 无同步
- `services/retrieval_embedding.go:58-96` — "LRU" 缓存命中不刷新 recency → 实为 FIFO
- `services/retrieval_service.go:335-347` — 每个搜索结果的完整内容以 Info 级别记录
- `services/retrieval_service.go:371-385` — `TopK` 无上限 → 百万行扫描
- `services/simple_retrieval_service.go:115-117` — 精度路径吞掉所有错误 → 停机看起来像空知识
- `services/retrieval_rewrite.go:224-236` — 同义词路径检查无尾部分隔符 → 兄弟目录可通过
- `repositories/experience_repository_memory.go:267-276` — 内存版 `DecrementRank` 减 1.0 vs PG 版乘 0.9 → 行为完全分歧

### 3.8 llm
- `llmservice/service.go:79-86` — `NewService` 不复制 `MaxTokens`
- `llmservice/service.go:151-156` — 纯文本路径丢弃 Temperature/MaxTokens
- `client.go:322-327` — `emitCallback` 传原始 prompt（未 sanitize）→ 秘密泄漏到回调
- `resilience.go:250-284` — 半开探测槽在 ctx 取消时泄漏 → 最多 30s 硬停机
- `failover.go:313-349` — 首 chunk 握手不检查 `Err != nil` → 坏 provider 不触发故障转移
- `output/openai.go:270-272` 等 — 三个 adapter 的 `GenerateStream` 不发终端 `Done` chunk
- `output/ollama.go:65-70` — temperature 放在顶层而非 `options` 对象 → 被 Ollama 忽略

### 3.9 protocol
- `skills/fts5.go:36` — `:memory:` SQLite 无 `SetMaxOpenConns(1)` → 并发搜索命中空库
- `ahp/dlq.go:274-281` — nil message entry 在 defaultHandler 中空指针 panic
- `mcp/factory.go:113-127` — 工具选择按 map 迭代顺序（非确定性）；子进程永不关闭
- `ahp/queue.go:149-162` — `Peek` 在 channel 未满时旋转队列 → 顺序被破坏
- `mcp/transport_stdio.go:101-102` — stdout scanner 1MB 上限 → 大工具结果永久杀死客户端
- `mcp/transport_sse.go:96-103` — `Start` 返回时连接尚未建立 → 握手竞态
- `skills/catalog.go:117-141` — `Build` 在写锁下执行 HTTP I/O
- `skills/indexer.go:91-93` — front-matter capabilities 类型断言永远失败
- `skills/experience.go:170-191` — `BestMatch("")` 匹配每条记录
- `mcp/manager.go:278-291` — `RefreshTools` 失败路径丢失 `mc.tools` → 陈旧工具绑定到已关闭客户端

### 3.10 ares_bootstrap / aresrecovery
- `bootstrap_steps.go:189-202` — 两个 PG `*sql.DB` 句柄无关闭接线
- `embedding_worker.go:102,140` — 大多数后台 worker 用裸 `bgGroup.Go` 绕过 panic-recover
- `skill_outcome_writer.go:85,94-100` — recover 在循环级别 → 一次 panic 永久停止记录
- `provide_wiring.go:16-28` — `flightRecorderWrapper` 方法无 nil guard
- `provide_distillation.go:138-173` — 策略结果的 `TaskType` 被设为 "success"/"failure"
- `aresrecovery/evolution_spawner.go:101-121` — recovery spawn 仍受 `Enabled` 门控 → 搁浅任务
- `aresrecovery/evolution_feedback.go:52-59` — `WithMaxEntries` 不重建 `byCandID` 索引
- `aresrecovery/evolution_feedback.go:32-34` — `CombinedFitness` 混合 [0,1] 和 [1,5] 量表
- `agentipc/primitives.go:44-160` — `from` 由调用者提供，无认证无 ACL → 任何 bus 持有者可冒充任意 agent，毒化演化反馈
- `agentsyscall/syscall.go:182-184` — `SetAskAgent` 无同步写入，每个 LLM 工具调用无锁读取 → 数据竞争

### 3.11 sdk / cmd
- `sdk/knowledge.go:166-169` — 两个独立的 evolution strategy store → AKG provider 看不到运行时写入
- `sdk/scheduler.go:99-166` — `Submit` 超时不停止执行器；放弃的任务永久留在 fabric
- `sdk/sdk.go:273-288` — `New()` 构造失败时泄漏 memory manager 和 PG pool
- `sdk/agent.go:78-111` — `Stream` 泄漏 goroutine 且不是真流式
- `sdk/config.go:396-422` — knowledge 无法从 YAML 启用
- `sdk/bootstrap_runtime.go:29-71` — Bootstrap 配置丢弃 LLM 调优和内存大小
- `cmd/ares/main.go:154-169` — `ares doctor` 对 Ollama-only 环境报失败；打印 API key 前缀
- `cmd/ares/serve.go:892-908` — 所有配置的 LLM 失败时静默回退到 localhost Ollama
- `sdk/evolve.go:120` — `Evolve` 无同步变异活 agent

### 3.12 builtin tools
- `builtin/network/ssrf.go:136-144` — SSRFTransport 继承 `ProxyFromEnvironment` → 代理路径绕过 dial-time 检查
- `builtin/file/file_tools.go:277-306` — `readFile` 全量读取后才应用 offset/limit → 大文件 OOM
- `builtin/embedding/embedding.go:86,121,163` — 无 `LimitReader`、无状态码检查、无 SSRF 重定向防护
- `builtin/memory/memory_tools.go:125-138` — `memory_search` 无租户/用户隔离 → 任何用户可读所有任务负载
- `builtin/knowledge/store_adapter.go:144-183` — `knowledge_update` 往返丢失 Embedding 和 Raw → 语义搜索退化
- `builtin/text/log_analyzer.go:265-281` — 日志级别分类用 map 迭代 → 非确定性输出
- `builtin/math/calculator.go:102,281-292` — `expr.Run` 忽略 ctx，无 `MaxNodes` → `factorial(1e9)` 钉死 CPU
- `builtin/builtin.go:38-47` — `ARES_FILE_TOOLS_ALLOWED_DIR` 未设置时回退到 CWD → 静默授予源码读写权限
- `builtin/text/data_transform.go:179-204` — `jsonToCSV` 手动拼接 header，含分隔符的 key 产生损坏 CSV
- `builtin/execution/code_runner.go:184-197` — `SetTimeout` 写入的值从不被读取 → 运维超时配置是 no-op

### 3.13 agents/sub / agentloop / models
- `agents/sub/agent.go:244` — `Stop(ctx)` 忽略 ctx，`streamWg.Wait()` 无限阻塞
- `agents/sub/agent.go:303-315` — `Execute` 不检查 nil task → panic
- `agentloop/engine.go:676-689` — `FriendlyErr` 用 `%v` + `%w` 双重嵌入错误文本
- `agentloop/engine.go:300-331` — token 预算检查在最终答案分支之前 → 完整答案被丢弃
- `agents/strategy.go:55-68` — `MergeNodeParams` 就地变异调用者 map
- `core/models/task.go:13-14` — `TaskType` 和 `AgentType` 双字段已分歧
- `core/models/recommend.go:36` — `AgentPreferences` 字段的 JSON tag 是 `style` → 名称/tag 不匹配
- `logger/logger.go:84-86` — 所有 `*Context` 变体发出无用的 `"method":""` 字段
- `logger/logger.go:54-63` — `Logger` 在调用时解析 `slog.Default()` → 不可测试
- `errors/wrap.go:13-131` — 5 个独立超时 sentinel 不包装共同根 → `errors.Is(err, ErrTimeout)` 漏匹配

---

## 四、LOW（可择机清理）

<details>
<summary>展开全部 ~80 项 LOW 级发现</summary>

### kernel
- `errors.go:11-12` — `ErrNoCandidate` 死代码
- `ctx/ctx.go:27-30` — `CallerID(nil)` panic
- `scheduler.go:1278-1286` — Snapshot 的 `MaxConcurrent` 在 peer 模式下错误
- `orchestrator.go:223,241` — 同一文件中日志前缀不一致（`system_runtime:` vs `kernel:`）
- `registry.go:219-231` — 环检测错误消息成员顺序不确定
- `load_tracker.go:162` — confidence override key 用 `|` 分隔无转义
- `decision_recorder.go:85-95` — Snapshot 是浅拷贝
- `fabric_executor.go:49-55` — `Type()` 无锁读取活 `*Agent`

### fabric
- `task/fabric.go:453-455` — `Renew` 中不可达的 nil-lease 检查
- `agent/fabric.go:121-127` — `Agents()` 用 O(n²) 冒泡排序
- `agent/planner_cognition.go:816` — `stampTokenUsage` 是空函数
- `agent/l2graph.go:123-132` — `Predecessor` 只返回 `DependsOn[0]`
- `task/lease.go:17-19` — `NewLease` 用墙钟而非可注入时钟

### runtime
- `bus.go:252-278` — `Subscribe` 清理 goroutine 在 `Stop` 后不退出
- `checkpoint.go:320-353` — `Snapshot` 漏拷贝 `DAGNodes`/`DAGEdges`
- `checkpoint.go:374-379` — `Cleanup` 零生产调用者 → snapshots map 无界增长
- `archive/extract.go:117-129` — 所有 `code_runner` 退出码归因到 `GoVet`
- `archive/extract.go:194-218` — `"ok"` 子串匹配产生假 "pass"
- `archive/writer.go:126-139` — fsync open 失败时静默跳过
- `errors.go:14` — `ErrBusNotStarted` 死代码
- `memory/distillation/test_set.go` — 306 行测试夹具在生产文件中
- `memory/context/cache.go:54-71` — 整个 Cache 类型未使用但自动启动 goroutine
- `memory/distillation/scorer.go:56-95` — keywordScores map 每次调用重新分配

### ares_evolution
- `genome/population.go:76-82` — 文档提到的 `HistoryEnabled` 字段不存在
- `genome/population_guard.go:100-103` — 不可达的 `base < 1` 分支
- `mutation/guided_mutator.go:586-609` — map 迭代顺序破坏确定性
- `report.go:399` — "LLM Calls" 标签打印的是预算而非调用数
- `dream_cycle_ga.go:148-152` — parent baseline 用子代 ID

### knowledge
- `relation_extract.go:37-73` — 贪婪正则匹配到输入末尾
- `skills/registry.go:131-146` — `Search` 返回完整 Skill（含 Detail）而 `List` 剥离
- `vector_index.go:52-132` — `InMemoryVectorIndex` 无驱逐/上限
- `provider/vector/provider.go:321-331` — 手写 Newton-Raphson sqrt 替代 `math.Sqrt`

### storage
- `repositories/experience_repository_memory.go:55-71` — `idOf` helper 在 n≥1000 时 panic
- `repositories/conversation_repository.go:162-171` — limit 无验证/默认值
- `repositories/knowledge_repository.go:558-570` — 关键词搜索过滤 `embedding_status='completed'`
- `repositories/strategy_repository.go:57-61` — 无 tenant 谓词

### llm
- `chat.go:187-218` — Ollama chat 路径不读 token 计数
- `output/parser.go:17-18` — `fixJSONString` 的 regex 预处理在字符串感知扫描前运行
- `output/openai.go:424` — 成功路径解码无大小限制
- `output/validator.go:106-110` — MinLength/MaxLength 用字节而非字符
- `output/factory.go:42-44` — `defaultFactory.adapters` map 无同步
- `output/timeout.go:28-33` — `WithDefaultTimeout` 不比较已有 deadline
- `client.go:55-57` — `isRateLimitError` 的 `"429"` 子串匹配过于宽泛

### compat
- `loader/markdown/markdown.go:20-44` — `readAllLimited` 在三个包中复制粘贴
- `loader/html/html.go:61-76` — 朴素 HTML 标签剥离损坏普通文本
- `tool/builtin/builtin.go:37-45` — 中文描述混入英文用户界面
- `protocol/openai_api/openai_api.go:635` — 响应 ID 用 `time.Now().Unix()` → 同秒碰撞
- `vector/pgvector/pgvector.go:60-66` — `Search` 接受空 tenantID 而 `Upsert` 拒绝

### api
- `api/tools/tools.go:72-73` — `WithAllowedDir` 导出返回不可命名的未导出类型
- `api/discovery/discovery.go:51` — `var NewMemoryStore` 是可变函数变量
- 多个包的 doc.go 注释重复

### protocol
- `ahp/message.go:225-233` — `MarshalJSON` 是 no-op
- `mcp/manager.go:73-79` — `lastErr` 只读不写
- `mcp/transport_sse.go:50` — `respBody` 字段死代码
- `skills/experience_store.go:74-80` — 固定临时文件名无文件锁
- `mcp/transport_server.go:363-367` — POST body 无大小限制

### events/config/security
- `ares_security/jwt.go:89` — 过期检查应用 `>` 而非 `>=`
- `ares_security/jwt.go:126-153` — 不验证 JOSE header 的 alg
- `ares_security/middleware.go:142-147` — Bearer scheme 大小写敏感
- `ares_events/compactor.go:196-204` — `buildSummary` 在第一个事件锁死 AgentID
- `ares_events/memory_store.go:75-106` — Append 循环中途失败不回滚
- `ares_config/config.go:683-685` — `ARES_AUTH_ENABLED=0` 启用认证

</details>

---

## 五、跨模块系统性问题

### 5.1 Setter-under-lock / Read-without-lock 模式
至少 15 个字段遵循 "setter 加锁、reader 不加锁" 或 "setter 完全不加锁"：
- `EvolutionScheduler.{adapter,scoreProvider,dreamCycle}`
- `memoryManager.{eventStore,streamID,evidenceCollector,skillsRegistry}`
- `ToolPlugin.collector`, `MemoryRetriever.evidenceEmitter`
- `RetrievalService.pipeline`
- `MCPManager` 的 `mc.mcp`

**建议**: 全仓统一使用 `atomic.Pointer[T]` 或 snapshot-under-RLock 模式。

### 5.2 Locks held across I/O
多个热路径在持锁时执行网络/磁盘 I/O：
- `CheckpointPlugin.saveLocked`（JSON marshal + store.Save + bus.Emit）
- `fileArchiveWriter.RecordRound`（marshal + write + fsync + rename + rotate）
- `ProductionMemoryManager.CreateSession`（写锁 + emitEvent）
- `Catalog.Build/Refresh`（写锁 + HTTP fetch）
- `Experience.Record`（写锁 + 文件写入）
- `EvolutionCoordinator.ApplyEmergency`（mu + patchReg.Apply）
- `ScoreAgentsMulti`（种群写锁 + scorer 调用）

**建议**: 审计规则——绝不在持有 `m.mu` 时调用 `emitEvent`/`Deliver`/repo 方法。

### 5.3 Panic containment 不一致
- `Request` recover handler panic ✓
- `Send`/`Broadcast` 不 recover ✗
- `GoBackground` recover ✓
- 大多数 bootstrap worker 用裸 `bgGroup.Go` ✗
- `RunQuantum` 无 panic 边界 ✗

### 5.4 Nullable DB columns 扫描到非 Go 指针类型
`expires_at`, `last_used_at`, `error`, `latency_ms`, `metadata` 等可空列被扫描到 `time.Time`/`string`/`int`（非指针），导致行被静默跳过。`sql.Null*` 模式应为默认。

### 5.5 Byte-vs-rune 截断
`truncpkg` 是 rune-aware 的且被广泛使用，但至少 5 处仍用原始字节切片：
- `adapter/memory.go:66-68`
- `adapter/evolution.go:26,101`
- `provider/memory/provider.go:117-118`
- `provider/code/provider.go:317-318`
- `distiller_admin.go:209-217`

### 5.6 "Advisory" 子系统从不生效
- `promotion.Evaluate` 推荐但从不应用状态转换
- `introspect` insight 引擎完全死代码
- `ControlServer.feedback` / `WithEvolution` 的 sink 链零调用者
- `fail-closed` 守卫不总是阻止

### 5.7 无界增长的 map/slice
| 位置 | 结构 | 触发条件 |
|------|------|---------|
| `load_tracker.go` | 5 个 map | agent 轮换 |
| `flight/genealogy.go` | nodes/roots/children | agent 复活 |
| `flight/graph.go` | pendingChildren | parent 事件丢失 |
| `flight/collector.go` | agentStartIDs | agent 启动 |
| `knowledge/pipeline.go` | resolvedObjects | 每个处理对象 |
| `ares_evolution/experience` | evidenceCache.byStrategy | 每代新 UUID |
| `ares_evolution/refine` | applied map | 从不回滚时 |
| `CheckpointPlugin` | snapshots map | Cleanup 零调用者 |
| `ares_bootstrap` | restarts map | 每个唯一死 agent ID |

---

## 六、修复优先级建议

### P0 — 立即（影响数据正确性/可用性/安全）
1. `pg_store.go` Subscribe 游标卡死（1.4）
2. fabric 三个 CRITICAL（1.1-1.3）
3. MCP 连接生命周期 + goroutine 泄漏（1.6, 1.7）
4. SDK YAML temperature/max_tokens 丢弃（1.8）
5. knowledge PG Stream 死锁（1.5）
6. `sub/agent.go` Stop double-close panic（1.9）
7. **Python 沙箱 RCE 绕过**（2.12#59）— 禁用 `EnablePython` 或加 OS 级隔离
8. PDF TOCTOU 路径验证绕过（2.12#60）

### P1 — 本周（并发安全 + 安全）
9. 全部 setter/read 竞争（5.1）
10. kernel panic-guard 间隙（2.1#3）
11. `RestartAgent`/`ResumeAgent` WaitGroup panic（2.3#8,9）
12. storage 跨租户问题（2.6#31,35）
13. SSRF 遗漏 CGNAT 段（2.12#61）
14. knowledge/memory 工具的 tenant_id 由 LLM 控制（2.12#63）
15. `sub/agent.go` ProcessStream/Process 生命周期（2.11#57,58）

### P2 — 本月（功能正确性）
10. 演化系统 HIGH 批次（2.4）
11. 可观测性死功能（2.3#13-17）
12. llm token 计数 + Anthropic 消息构建（2.8）
13. 无界增长清理（5.7）

### P3 — 下个迭代（代码质量）
14. 死代码清理（advisory 子系统、未使用常量、重复实现）
15. Byte-vs-rune 统一
16. Nullable column 扫描统一
17. 文档/注释与代码对齐

---

## 七、正面观察

审查中也发现了许多值得保持的良好实践：

- **架构分层锁定**: `architecture_test.go` 在 CI 中强制 kernel 不 import runtime
- **内存边界纪律**: timeline ring 300、collab ring 200、anomaly ring 500 等一致应用
- **RBAC 默认拒绝**: `ares_security` 的层级 + default-deny 正确
- **参数化 SQL**: 所有 PG 查询参数化，无 SQL 注入
- **命令注入防护**: `exec.Command` argv-array + `filepath.IsAbs` guard
- **JWT 算法混淆不可利用**: 验证无条件重算 HS256
- **goleak 覆盖**: sdk 有 goroutine 泄漏检测
- **peer registry clone-before-stamp**: 正确的防御性复制
- **MCPClient.dispatchResponse**: pendingMu-during-send 正确防止 closed-channel 发送
- **恢复集成回调形状**: kernel 永不 import runtime，recovery 通过 callback 接线

---

*本报告由 18 个并行 review agent 生成，每个 agent 逐文件通读其负责的模块。所有 CRITICAL 和 HIGH 发现均经过交叉验证。*
