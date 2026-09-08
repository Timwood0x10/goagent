# goagent 深度 Code Review 报告

> 生成日期：2026-09-08
> 状态：**仅审查，未改动任何代码**（按用户指示"只先出完整发现清单"）
> 方法原则：**只认代码**——所有结论均带 `file:line` 证据，并逐项对照 `RUNTIME.md`/`ARCHITECTURE.md` 台账核实。

---

## 0. 范围与方法

- **规模**：188 个包 / 1473 个 Go 文件（不含 vendor）。
- **质量门基线**（执行时全绿）：
  - `golangci-lint run ./...` → 0 issues（已开启 `unused`，故符号级死代码已被清）
  - `go vet ./...` → 0
  - `scripts/check_convergence_freeze.sh` → OK（无新增 examples/ / internal/ 顶层条目）
  - `go build ./...` → 0
- **方法**：
  1. 全仓反模式扫描（`panic`/`fmt.Println`/`ioutil`/`time.Sleep`/`_ =`/`//nolint`/`go func`/`placeholder`/`deprecated` 等）
  2. 按包债务密度排序（`internal/*` 下债务标记计数）
  3. 核心并发模块精读：`internal/aresrecovery/recovery.go`、`internal/runtime/bus.go`
  4. 逐项对照台账核实代码（结果见 §1）
- **覆盖说明**：全部模块达"扫描级"；`runtime/kernel/fabric/agentipc/bootstrap` 达"精读级"。标注 `[需核实]` 者为扫描级、未精读。

### 按包债务密度排序（internal/ 下）

| 包 | 债务标记数 |
|---|---|
| internal/runtime/ | 177 |
| internal/fabric/ | 76 |
| internal/storage/ | 56 |
| internal/ares_bootstrap/ | 48 |
| internal/agentipc/ | 44 |
| internal/tools/ | 26 |
| internal/knowledge/ | 25 |
| internal/llm/ | 21 |
| internal/introspect/ | 20 |
| internal/ares_config/ | 19 |
| internal/kernel/ | 13 |
| internal/ares_events/ | 10 |
| internal/agents/ | 7 |
| internal/agentsyscall/ | 6 |
| internal/evidence/ · discovery/ · ares_shutdown/ | 3 |
| internal/ares_ratelimit/ · ares_integration/ · agentloop/ · feedback/ · core/ · ares_security/ | 1–2 |
| **0 标记（扫描级干净）**：truncate/ · scoreutil/ · logger/ · llmservice/ · errors/ · detector/ · aresrecovery/ · ares_ctxutil/ · ares_callbacks/ | 0 |

### 全仓反模式扫描（非测试 .go）

| 红旗 | 计数 | 备注 |
|---|---|---|
| `_ = ` 忽略返回值 | 356 | 多数为 `_ = ctx` / `_ = w.Write` 等有意忽略；含 ~10 处**未检查类型断言**（见 §3.1） |
| `//nolint` | 128 | 多为 `gosec`（参数化 SQL，合理）；含 4 处**无目标 nolint**（见 §3.3）、3 处 `bodyclose`（见 §3.4） |
| `go func` | 52 | 多为合法后台 worker；需逐处确认 ctx 取消（见 §4） |
| `unimplemented`/`placeholder`/`not implemented` | 73 | 多为 SQL `$N` 占位、MCP URI 模板占位、"bootstrap placeholder DAG" 设计描述；**真正缺口**见 §2 runtime |
| `deprecated` | 7 | 多为 `log.Warn` 弃用通知 + 已删别名 |
| `TODO` | 18 | |
| `FIXME`/`HACK`/`XXX` | 1 | |
| `panic(` / `fmt.Println` / `ioutil.` / `time.Sleep`(prod) | 0 | 生产基础卫生良好 |

---

## 1. 🔴 最关键结论：文档比代码更"脏"

既有 `RUNTIME.md` §6 台账 / `ARCHITECTURE.md` M2/M4 多处与代码不符。**照台账"全清"会误删活代码。**

| 既有台账/RUNTIME 断言 | 代码事实（file:line） | 判定 |
|---|---|---|
| M5 "api/ deprecated 下葬" | `api/` 被 **84** 个外部文件引用（serve.go / agent.go / ares_bootstrap / llm / memory / knowledge / fabric/agent …） | **活着，不可删** |
| #3 并发度 = 1 | `scheduler.go:497` 第三跳回退 `fabricCandidateCount()`（peer 模式 = 活 IDLE agent 数） | 陈旧 |
| #4 内存 EventStore 前提 | `serve.go:189` 用 `compactableStore`（非裸 MemoryEventStore） | 前提错 |
| #5 answer 失败不释放会话 | `l2graph.go:412` 终态已 `ReleaseSession` | 已修 |
| #12 dispatch_timeout 无消费者 | `kernel.go:724-727` 有消费者 | 错 |
| #13 CanRetry 注释/代码矛盾 | `task.go:53-65` 注释"0 = no retries"，代码 `Attempts < MaxRetries` 在 `MaxRetries=0` 返回 false，二者一致 | **误报（已过时）** |
| #14 RestartPolicy.Backoff 从不 sleep | `recovery.go:279` `delay := r.policy.Backoff << attempts` 紧跟 `:285` `r.sleep(ctx, delay)`——生产 `RestartAgent` 路径确实应用退避 | **误报（已过时）** |

→ **`ARCHITECTURE.md` 的 M2/M4 因转录上述错误前提而不可信**；清理第一步必须是校准文档，而非删代码。

> 注：第 1 节 #13、#14 两项推翻了此前可行性评估中"准确真 bug"的结论——经精读代码后证伪。这进一步印证"清理必须只认代码 + 先修文档"。

---

## 2. 逐模块发现（按债务密度）

严重度图例：🔴 高危（数据/并发/活代码风险） · 🟠 中危（设计债/资源/弹性） · 🟡 低危（代码异味/清理债）。

### 🔴/🟠 internal/runtime/（177 标记，最大面）
- **进化 fitness 缺 cost/latency 惩罚**：`ares_evolution/fitness_aggregator.go:80,304` + 注释 `:82` "It is not implemented because task…"——设计文档要求的成本/延迟惩罚项**从未实现**，进化只优化正确性。🟠 设计缺口。
- **回归门被跳过**：`evolution/candidate.go:148,311` "v1 placeholder until ares_arena wiring"——回归门在 arena 接线前被跳过。🟠 缺口。
- **`DreamCycle` 关门子系统**：`bootstrap_steps.go:216` `EnableDreamCycle=false`，仍编译并接线（`genome_wiring_system.go` 的 `buildDreamCycle` / `SetDreamCycle`）。⚠️ 编译在位但生产关闭——可拆但量大。
- **已废弃调用者未清理**：`arena/metrics.go:81,91,101`（`RecordRecovery`/`RecordFailover`/`RecordConsistency`）、`arena/scenario.go:338`（`RunScenario`）仍 `log.Warn` 弃用却仍被调用。🟡 清理债。
- `observability/flight/genealogy.go:65`、`recorder.go:40` 占位血缘——🟡 表面。

### 🟠 internal/fabric/（76）
- **持久化恢复静默错填**：`task/restore.go:168-169` `t.Capability, _ = …; t.Origin, _ = …`——**未检查类型断言发生在跨重启的 checkpoint 恢复路径**，断言失败 → 字段静默成零值，可能把任务恢复成错误能力/来源。🟠 数据完整性。
- **未检查断言**：`agent/planner_cognition.go:335` `rootPrompt, _ = task.Payload["input"].(string)` 静默空串。🟡
- **复杂度热点**：`workflow/engine/mutable_dag.go`(×2 gocyclo)、`task/workflow`——🟡。
- **`DualTrack.Dispatch` 死方法**：`agentipc/policy.go:105` 全仓 prod 零调用；但 dispatcher 仍作 IPC 外观被用——**只删该方法，勿删整链**。🟡

### 🟠 internal/agentipc/（44）
- **IPC 无重试/死信语义**：`kernel.go:364` `TODO(tech-debt): agentipc has no retry/dead-letter semantics`——真实弹性缺口。🟠
- DualTrack 外观保留（见上）。

### 🟠 internal/llm/（21）
- **`//nolint:bodyclose` ×3**（`client.go`）：HTTP response body 未关闭的抑制——**潜在连接/文件描述符泄漏**，需核实是流式场景 legit 还是真漏。🟠 `[需核实]`
- `failover.go:134,499` 已删别名（deprecated alias removed）——✅ 干净。

### 🟠 internal/ares_bootstrap/（48）
- **`nilnil` 反模式 ×4+**：`bootstrap.go:642,671,723`、`strategy_adapter.go`×2、`provide_*`——`(nil,nil)` 混淆"未找到"与"无错"。🟠
- `bootstrap_steps.go:511` `legacySched, _ = ….(*EvolutionScheduler)` 未检查，但随后 `if legacySched != nil` 守卫——**安全但异味**，建议改 `_, ok` 显式。

### 🟠 internal/storage/（56）
- `postgres/embedding_queue.go:706` `_, _ = result.RowsAffected()` 忽略错误——🟡
- `repositories/experience_repository_memory.go:92` `return nil, nil`——🟠 nilnil
- `//nolint:gosec` ×4（postgres store）多为参数化 SQL——🟡 `[需核实]`

### 🟡 internal/tools/（26）
- `resources/builtin/embedding/embedding.go:188` `texts[i], _ = v.(string)` 循环内未检查断言 → 静默空串元素。🟡
- `memory_tools.go`×2 / `file_tools.go`×1 gocyclo——🟡

### 🟡 internal/knowledge/（25）
- `store/sqlite/store.go:342,372,373,529` `time.Parse` 错误忽略 → 坏数据归零时间。🟡
- `store/postgres/store.go`×4 gosec——🟡

### 🟡 internal/introspect/（20）
- `api.go:78,94,121` `_, _ = w.Write(...)` HTTP 响应写错误忽略（best-effort）。🟡

### 🟡 internal/ares_config/（19）/ kernel/（13）/ ares_events/（10）/ agents（7）/ agentsyscall（6）
- `kernel/scheduler.go:439` `TODO(tech-debt): per-agent local ready-queue` 设计注记；`architecture_test.go:58` 守住 kernel≠runtime 边界——✅。
- `ReAct` 全仓 25 处引用（含 `ares_config`、`sdk`、`sub_cognition`、`agentloop/doc.go`）——多为 config 键/命名残留，**执行逻辑是否仍活需核实**。🟡 `[需核实]`
- `agents/sub/agent.go:526`、`agentsyscall/plan.go:252` `go func` 需确认 ctx 取消。

### ✅ 已确认干净（0 债务标记，扫描级）
`truncate/` · `scoreutil/` · `logger/` · `llmservice/` · `errors/` · `detector/` · `aresrecovery/` · `ares_ctxutil/` · `ares_callbacks/`

---

## 3. 跨模块共性问题（最该先修）

### 3.1 🔴 未检查类型断言（~10 处，静默数据损坏/错误零值）
统一改 `v, ok := x.(T); if !ok { … }`：
- `cmd/ares/evolution.go:512` `prompt, _ = taskPayload["task_desc"].(string)`
- `internal/tools/resources/builtin/embedding/embedding.go:188` `texts[i], _ = v.(string)`
- `internal/fabric/agent/planner_cognition.go:335` `rootPrompt, _ = task.Payload["input"].(string)`
- `internal/fabric/task/restore.go:168-169` `t.Capability, _ = …; t.Origin, _ = …`（**持久化恢复路径**，最高危）
- `internal/runtime/memory/context/cleaner.go:377,417` `url, _ = args["URL"].(string)`
- `internal/evidence/collector.go:23` `raw, _ = json.Marshal(payload)`
- `internal/knowledge/store/sqlite/store.go:342,372,373,529` `time.Parse` 忽略
- `internal/ares_bootstrap/bootstrap_steps.go:511` `legacySched, _ = ….(*EvolutionScheduler)`（有 nil 守卫，安全但异味）

### 3.2 🟠 `nilnil` 反模式（11 处）
返回 `(nil, nil)` 混淆"未找到"与"无错误"，调用方易误判。统一拆分：
- `internal/ares_bootstrap/bootstrap.go:642,671,723`
- `internal/runtime/evolution/diagnoser.go:120,163`
- `internal/runtime/evolution_plugin.go:115,149`（带 `//nolint:nilnil`，文档约定"无 provider/no recommendation"——仍建议改为显式 error）
- `internal/runtime/ares_evolution/strategy_adapter.go`×2
- `internal/storage/postgres/repositories/experience_repository_memory.go:92`

### 3.3 🟡 无目标的 `//nolint`（4 处）
抑制**所有** linter，隐藏问题。应指定具体 linter：
- `sdk/sdk.go` · `internal/tools/planner/analyzer.go` · `internal/storage/postgres/repositories/strategy_repository.go` · `internal/storage/postgres/pool.go`

### 3.4 🟠 `//nolint:bodyclose` ×3（`internal/llm/client.go`）
HTTP response body 未关闭的抑制——潜在连接/文件描述符泄漏，需核实是否真漏。🟠 `[需核实]`

### 3.5 🟡 已弃用调用者未移除
- `internal/runtime/arena/metrics.go:81,91,101`（`RecordRecovery`/`RecordFailover`/`RecordConsistency`）
- `internal/runtime/arena/scenario.go:338`（`RunScenario`）

### 3.6 🟡 复杂度热点（gocyclo 抑制）
- `internal/ares_events/compactor.go`×3
- `internal/fabric/task/workflow/engine/mutable_dag.go`×2
- `internal/tools/resources/builtin/memory/memory_tools.go`×2
- `internal/runtime/ares_evolution/service/service.go`×2
- `internal/tools/resources/builtin/file/file_tools.go`×1
- `cmd/ares/agent.go`×1

---

## 4. 架构级设计缺陷

- **`api/` 与 `sdk/` 并存且 api 仍深度接线** → 迁移未真正完成（M5 名存实亡）。
- **`compat/` 仅 1 个外部引用**（`internal/ares_bootstrap/provide_llm.go`）→ 标准日落目标。
- **事件非持久**：`serve.go` 用 `compactableStore`（内存 + archive），`PostgresEventStore` 未接线 → 重启丢事件。
- **无 answer 合成器**：`l2graph.go:374` `TODO(tech-debt): no summarizer is wired`——终态直接透传，无摘要聚合。
- **进化 fitness 不含成本/延迟**、**回归门跳过**（见 §2 runtime）。
- **agentipc 无重试/死信**（见 §2 agentipc）。
- **双 evolution 包**（v1 `ares_evolution` / v2 `evolution`）——架构文档要求**刻意保留不合并**，勿动。
- **goroutine 生命周期**：核心 `bus.go` 用 `ctx.Done()` 退订模式良好；但其余 ~50 处 `go func` 需逐处确认 ctx 取消（扫描级，未全精读）。

---

## 5. 清理建议顺序（待批准后再动手；本次未改代码）

1. **先校准文档**：按本报告修正 `RUNTIME.md` §6 与 `ARCHITECTURE.md` M2/M4（#3/#4/#5/#12/#13/#14 标 stale，#1 路径、#9 行号修正，api/ 标注"活着"）。
2. **高置信、低风险**（纯改不删、可单测）：
   - ① 10 处类型断言补 `ok`（§3.1）
   - ② 11 处 `nilnil` 拆分（§3.2）
   - ③ 4 处无目标 `//nolint` 指定 linter（§3.3）
   - ④ arena 弃用调用者清理（§3.5）
   - ⑤ 删 `DualTrack.Dispatch` 死方法（§2 fabric）
3. **需核实后修**：`llm/client.go` bodyclose（§3.4）、`ReAct` 残留是否活、`go func` ctx 取消（§4）。
4. **中风险结构清理**：`compat/` 日落、`DreamCycle`/legacy scheduler 拆除、事件持久化接线。
5. **架构级决策（需拍板）**：api/→sdk 真正迁移、`distilled` 可选特性去留、answer 合成器、agentipc 重试/死信。

---

## 6. 诚实声明

- 本次**零代码改动**，仅产出审查报告（按"只先出完整发现清单"指示）。
- 全覆盖为"扫描级"；`runtime/kernel/fabric/agentipc/bootstrap` 为"精读级"。标注 `[需核实]` 的 3–4 项未精读，结论为"疑似"。
- 推翻了此前可行性评估中 #13、#14 两项"真 bug"结论——经精读代码后证伪。
- 所有清理动作须沿用仓库收敛纪律：每步 `build+vet+lint+test` 全绿、删除项 `grep` 全仓引用清零、独立 commit。
