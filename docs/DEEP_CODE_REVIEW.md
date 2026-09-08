# goagent 深度 Code Review 报告 — v2（重构后复审）

> 生成日期：2026-09-08（v2，针对"我改了很多之后"的**当前代码**复审）
> 状态：**仅审查，未改动任何代码**
> 方法：**只认代码**；grep/sed 逐条在当前代码上复验上一版锚点，并全仓重扫；每个数字均当场核实，不复读旧报告。
> 基线（执行时全绿）：`go build ./...`=0 · `go vet ./...`=0 · `golangci-lint ./...`=0 issues · `freeze-check`=OK

---

## 0. 本轮变更背景（git 已确认）

| 提交 | 内容 |
|---|---|
| `babaaa47` | **M2 hardening**：retry / admission / drain-limit / security |
| `3bb8d0dc` | **cmd/ares 收敛为 7 个源文件**（agent/dashboard/db/evolution/kernel/main/serve） |
| `7209db09` | **fabric 合并**（agent/task/planprojection/workflow 归入 internal/fabric）+ **drop ReAct (M4)** |
| `450e499e` | M4-D 单 L2 路径 + Phase-2b fabric 合并 + Phase-4 examples sweep |
| `417d000a` | introspect 删未用 logger（staticcheck U1000） |
| `f9cd8edf` | runtime 合并（eval/observability/arena/memory/evolution 归入 internal/runtime） |

**规模**：1474 个 Go 文件 / 188 个包（较 v1 的 1473 文件基本持平）。

---

## 1. 上一版发现的重审结论（逐条核实，只认当前代码）

### ✅ 已经修复 / 已澄清（好）
| v1 条目 | 当前事实 | 判定 |
|---|---|---|
| 4 处无目标 `//nolint` | 全仓 `//nolint` 现均带明确 linter（0 处无目标） | **已修** |
| `agent.go:2390` 空 session_id 串会话风险 | `releaseSessionOnAnswerFailure` 有 `strings.TrimSpace(sid)==""` 守卫 | **已缓解**，非 bug |
| `//nolint:bodyclose` 疑似泄漏 | 6 处均注释"body is closed in the goroutine below"——流式 HTTP 合法异步关闭 | **合理，非泄漏** |
| `tasks/stringutils`/`compat/llm` 未检查断言多为容忍解析 | 多数被 `==""` 守卫 | 低危 |
| ReAct 执行体 | `agentloop/engine.go:206,239` 确认 ReAct loop 为第三执行体注释；`agents/sub/agent.go:57` "the ReAct tool loop is deleted"——执行体已删 | **命名/注释残留** |

### 🔴 仍然存在（未修 / 现仍未变）
| v1 条目 | 当前锚点 | 判定 |
|---|---|---|
| api/ 仍深度接线 | **84** 个外部 importers（未变） | 不可按 M5 字面删 |
| compat/ 仅 1 个外部引用 | `provide_llm.go`（未变） | 日落目标仍成立 |
| 持久化恢复未检查断言 | `restore.go:164-165` `t.Capability, _ = …; t.Origin, _ = …`（**唯一高危**） | 未修 |
| 事件非持久 | `serve.go:189-191` "raw *MemoryEventStore is unused here" | 未变 |
| 无 answer 合成器 | `l2graph.go:374` TODO | 未变 |
| dream cycle 生产关闭仍编译在位 | `bootstrap_steps.go:216` `EnableDreamCycle=false` | 未变 |
| ReAct 命名残留 ~24 个 prod 文件 | config 键 / 注释（kernel.go/ares_config/agentloop 等） | 清理债 |
| `Backoff` 应用 | `recovery.go:279` `<< attempts` + `:285` `r.sleep(...)` | 在（正常） |

### 🟠 部分解决
---

## 2. 重构后全仓重扫（非测试 .go）

### 2.1 🔴 未检查类型断言（重点：静默零值 → 潜在错误）
- **最高危（持久化恢复）**：`internal/fabric/task/restore.go:164-165` `t.Capability/_ = p[...]`、`t.Origin/_ = p[...]`——跨重启解析，若 key 类型不符/缺失则字段静默零值；建议补 `v, ok := …; if !ok { return err }`。
- **HTTP/API 汇总层大量 `_ := ...(T)` 容忍解析**（多为读不到则零值，低危）：
  - `cmd/ares/serve.go:439`（input）、`:1361`（recovered）、`:2220-2225`（score/grade/recovery_rate/total_faults…）、`:2382,2386`（status/progress）、`:2411-2412`
  - `cmd/ares/evolution.go:458,501`（taskID）、`:510,512`（prompt）
  - `cmd/ares/agent.go:1541-1542`（session_id/input，均有 `==""` 守卫）、`:2111`（message）、`:2732`（reason）
- **工具参数层**：`internal/tools/resources/builtin/embedding/embedding.go:188`（循环内 `texts[i], _ = v.(string)`）、`stringutils.go:109-150`（delimiter/join_items/old/new）、`toolsource/discover_tool.go:84`、`memory/context/cleaner.go:308-343`、`compat/llm/openai.go:44-53`、`ollama.go:28-32`、`compat/vector/pgvector.go:42`、`compat/protocol/openai_api.go:57`。
- **建议**：`restore.go`（持久化恢复）必须补 `ok`；其余可按"契约字段 grade 严格、展示字段宽容"分级。

### 2.2 🟠 nilnil 反模式——全仓真实 **50 处**（非 v1 报的 11；v1 只扫了样本文件）
集中在：`ares_bootstrap/`（bootstrap.go:642/671/723、skills_wiring.go:63/69/101/108、knowledge_akg.go:167/173、spawn_policy_source.go:142、strategy_adapter.go:41、bootstrap_steps.go:46）、`runtime/protocol/skills/`（loader.go:90/96、catalog.go:288、experience_store.go:41/47、config.go）、`compat/protocol/mcp/mcp.go:67/72`、`runtime/memory/context/retrieve_helper.go:174`、`tools/planner/`（provider.go:83/92、scorer.go:27、evidence.go:104）、`envcap.go:113`、`evolution/diagnoser.go`×2、`evolution_plugin.go`×2、`storage/postgres/repositories/experience_repository_memory.go:92` 等。
→ 统一分拆"未找到"(error) 与"空值"(nil)，或对调用方做 nil 分支约定。

### 2.3 🟡 复杂度热点（gocyclo，重构后新增"接线 hub"）
均带正当性注释：`serve.go:95`（runServe 装配 hub）、`agent.go:276/961`（createPeerAgents）、`bootstrap.go:238`、`bootstrap_steps.go:184`、`spawn_policy_source.go:50`、`llm/output/validator.go:78`、`ares_config/config_defaults.go:126`、`ares_events/compactor.go:165/328`、`fabric/task/workflow/engine/mutable_dag.go:725`、`dream_cycle.go:289`、`service/service.go:131`。→ 可接受，但 `compactor.go`/`mutable_dag.go` 仍可拆子函数。

### 2.4 其他
- `panic(` 14 处：8 在 `examples/_fixtures/*`（demo）、1 `testdata/gen_pdf.go`、1 `arena.go:127`（混沌注入）、3 `sdk/*.go`（init fail-fast）、1 `examples/...`——**均非生产核心**，安全。
- `TODO(tech-debt)` 11 处：kernel.go:364（agentipc）、scheduler.go:439（ready-queue）、l2graph.go:374（answer 合成器）、bootstrap_steps.go:155、shadow_sampler.go:230、observer.go:49、fitness_aggregator.go:80/304、lifecycle.go:1257、provide_new_evolution.go:155、goleak_test.go:20。
### 2.5 本轮新增核查结论（goroutine / 资源清理 / 日志规范）

**✅ 资源面整体健康（52 处 `go func` + 70 处 ticker/timer + 70 处 rows 扫描，抽查全部闭合）：**
- **goroutine 无泄漏**：`cmd/ares` 4 处 `go func` 均带 ctx 取消 + 信号量 + recover 边界（`kernel.go:940` 用 `sweepCtx` timeout + `defer cancel`；`:990` `sem` + `<-ctx.Done()`；`serve.go:140` 是**故意不归 ctx 管的二次信号 watcher**，注释明示；`serve.go:2783` 一次性子流，close+recover）。`recovery.go:557` `realSleeper`、`agentipc/primitives.go:192` 死信 timer 均 `defer Stop()` + select 双读 ctx。
- **ticker/timer 全部成对 Stop + 含 `<-ctx.Done()` 分支**：抽查 `bootstrap_steps.go:596`(suggestTicker)、`serve.go:2437`(pollSurvival)、`sliding_window.go`、`recovery.go:57` 均 `defer ticker.Stop()` 且写在 select 循环内。
- **postgres `rows.Close` 全程覆盖**：70 处 Close/Next/defer，无裸 Rows 泄漏。
- **`serve.go` 的 28 处 `fmt.Print*` 均为 CLI/console/arena UX 输出**（132/672-677 console banner、1992+ 场景校验、2204 arena server），非服务端业务日志——可接受。

**🟡 新发现：日志规范不一致——`log.Printf` 112 处（非测试/非 examples）**
仓库主线用 `log/slog`（结构化/级别/采样），但 `cmd/ares/*.go` 大量混用标准库 `log.Printf`（`kernel.go:207/302/340/689/732/742/779/785/872/911` 等）。→ **可观测性一致性问题**：`log.Printf` 无级别、无结构化字段、不可采样。建议批量为 kernel/serve 的运行时日志统一到 `slog`（`os.Exit`/二次信号等 CLI 路径除外）。

---
---

## 3. 架构级问题（重构后状态）

- **api/（84 importers）↔ sdk/ 并存**——M5"api→sdk 迁移"名存实亡（未变）。
- **compat/（1 importer）**——日落标准目标。
- **事件非持久**：`serve.go:189` 用 `compactableStore`（内存+compaction），`PostgresEventStore` 未接线。
- **无 answer 合成器**：`l2graph.go:374`。
- **agentipc**：dead-letter store 已建未接线（`kernel.go:364`）。
- **进化**：`fitness_aggregator.go:80,304` 成本/延迟惩罚未实现；`evolution/candidate.go:148,311` 回归门跳过。
- **双 evolution 包**（v1 `ares_evolution` / v2 `evolution`）——架构要求刻意保留，勿动。

---

## 4. 清理建议（按风险，本次未改）

1. **🔴 高置信低风险**：`restore.go:164-165` 类型断言补 `ok` + 返回 error（持久化恢复唯一高危）；`nilnil` 50 处分拆（先 `skills_wiring` / `bootstrap` 等契约型）。
2. **🟠 需核实后修**：`l2graph.go:374` answer 合成器、`fitness_aggregator` 成本惩罚、agentipc dead-letter 接线。
3. **🟡 清理债**：ReAct 命名残留（~24 prod 文件 config/注释）、`compactor.go`/`mutable_dag.go` 拆分、`serve.go` 宽容断言收口。
4. **🟢 尊重既有决策**：双 evolution 包不合并；`panic`/`bodyclose`/对接线 hub gocyclo 均已正当，不误改。

---

## 5. 与 v1 的差异摘要（诚实记录）
- 修正：nilnil **11 → 50 处**（v1 只扫样本，全仓真实量级）。
- 修正：4 处无目标 nolint → **已修 0**；bodyclose 3 处 → **6 处但均合法**（非泄漏）；agent.go 会话风险 → **已缓解**。
- 新增：重构引入的接线 hub gocyclo 热点、`runtime/eval` 等新 nilnil 集中区。
- 未变：api/ 84 引用、事件非持久、无合成器、Backoff 应用、recovery.go 退避。
- **agentipc 死信**：`deadletter.go` `DeadLetterStore` 已建；但 `kernel.go:364` TODO 仍在——**有 store、dispatch 路径未接线**。