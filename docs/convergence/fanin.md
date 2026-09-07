# Fan-In 审计表（Phase 0 产出，v2 修订版）

> 生成时间：2026-09-07；修订：按 code review #3/#5/#12 结论修正去向。
> 方法：`grep -rln "ares/internal/<pkg>" cmd internal sdk services --include="*.go" | grep -v 自包 | grep -v _test`，
> 计数单位 = 引用文件数（非测试）。顶层包（`compat`/`evaluation`）单独注明。
> 用途：Phase 1–4 合并时确定"去留"——高 fan-in 包移动时影响面大，需分步拆。

## 已完成：内核三件套（Phase 1，2026-09-07 落地）

| 包 | 非测试引用数 | 去向（已执行） |
|---|---|---|
| `internal/kernelscheduler` | 7 | `internal/kernel/` ✅ 已移，旧目录已删 |
| `internal/kernelctx` | 6 | `internal/kernel/ctx/`（子包形式，优于原计划单文件）✅ |
| `internal/system_runtime` | 4 | `internal/kernel/` ✅ 已移，旧目录已删 |

## 编排三件套（Phase 2b 合并目标；M4 通过后）

| 包 | 非测试引用数 | 合并去向 |
|---|---|---|
| `internal/taskfabric` | 25 | `internal/fabric/task/` |
| `internal/agentfabric` | 21 | `internal/fabric/agent/` |
| `internal/planprojection` | 3 | `internal/fabric/task/` |
| `internal/workflow`（engine/graph） | 16 | `internal/fabric/task/workflow/`（engine 只位移不删，M6 后/最后一步） |

## 运行时服务（Phase 3 合并目标）

| 包 | 非测试引用数 | 合并去向 |
|---|---|---|
| `internal/ares_evolution` (v1) | 29 | `internal/runtime/evolution/`（**分层保留**：持部署/闸门/fitness 接线，M6 落点在此；不删，#5） |
| `internal/ares_memory` | 21 | `internal/runtime/memory/`（同模块、按粒度分 API） |
| `internal/evolution` (v2) | 15 | `internal/runtime/evolution/`（持基因组/补丁引擎，继续推进） |
| `internal/ares_experience` | 12 | `internal/runtime/memory/`（经验蒸馏侧） |
| `internal/ares_observability` | 11 | `internal/runtime/observability/` |
| `internal/ares_runtime` | 10 | `internal/runtime/`（Manager = 生命周期编排者；`agentfabric.Spawn` 为其 spawn 原语，#1） |
| `internal/aresrecovery` | 16 | 保留恢复语义（`SpawnForRecovery` 不汇入 LLM syscall，收敛点 `agentfabric.Spawn`） |
| `internal/ares_protocol` | 7 | `internal/runtime/protocol/` |
| `internal/ares_skills` | 5 | `internal/runtime/protocol/` |
| `internal/ares_mcp` | 4 | `internal/runtime/protocol/` |
| `internal/ares_eval` | 4 | `internal/runtime/eval/` |
| `internal/eval` | 1（`ares_eval/evidence_bridge.go`） | 随 `ares_eval` 进 `internal/runtime/eval/` |

## 留主线：生产 CLI 依赖（不归档 examples，#3）

| 包 | 非测试引用数 | 去向 |
|---|---|---|
| `internal/ares_arena` | —（`cmd/ares/arena.go` 直接 import） | `internal/runtime/arena/` |
| `internal/ares_flight` | —（`cmd/ares/flight.go` 直接 import） | `internal/runtime/observability/flight/` |
| `internal/ares_archive` | —（`cmd/ares/recall.go` + `serve.go` 直接 import） | `internal/runtime/archive/` |

## 高 fan-in 基础设施（不动）

| 包 | 非测试引用数 | 处置 |
|---|---|---|
| `internal/errors` | 80 | 不动 |
| `internal/logger` | 58 | 不动 |
| `internal/ares_events` | 57 | 不动（事件总线） |
| `internal/agents` | 34 | 不动（Agent 接口主家，Phase 2b 统一时复审） |
| `internal/core` | 49 | 不动 |
| `internal/storage` | 30 | 不动 |
| `internal/evidence` | 30 | 不动 |
| `internal/ares_config` | 30 | 不动 |
| `internal/knowledge` | 22 | 不动 |
| `internal/llm` | 18 | 不动 |
| `internal/ares_bootstrap` | 13 | 组装根→runtime（Phase 3 定，或独立保留） |
| `internal/truncate` | 12 | 不动 |
| `internal/ares_callbacks` | 8 | 不动 |
| `internal/introspect` | 7 | 不动（dashboard 能力） |
| `internal/ares_security` | 5 | 不动 |
| `internal/agentipc` | 5 | protocol 或 fabric（Phase 2b 定；冻结不删） |
| `internal/scoreutil` | 5 | 不动 |
| `internal/agentsyscall` | 4 | protocol 或 fabric（Phase 2b 定；冻结不删） |
| `internal/feedback` | 4 | 不动 |
| `internal/ares_ratelimit` | 4 | 不动 |
| `internal/agentloop` | 3 | D5 已裁定冻结不删 |
| `internal/ares_shutdown` | 2 | 不动 |
| `internal/ares_ctxutil` | 2 | 不动 |
| `internal/detector` | 1（`sdk/quickstart.go`） | 留 |
| `internal/discovery` | 1（`provide_discovery.go`） | 留 |
| `internal/llmservice` | 0（仅 `api/service/llm` 引用） | 随 `api/` 留 |
| `internal/ares_integration` | 0（仅自包 `_test` 引用） | 测试资产，原样保留，不计入"≤15" |

## 顶层包（非 internal/）

| 包 | 引用情况 | 去向 |
|---|---|---|
| `sdk/` | 对外 API | 留（位置不动；内部第二套 Agent 接口改薄包装） |
| `api/` | `cmd/ares/{serve_routine,actions,tools,mcp}.go` + `internal/tools/...` 生产引用 | 留（公共 API 面） |
| `compat`（顶层） | 唯一生产引用 `provide_llm.go`（compat/llm/ollama/openai） | 留，日落门 = provide_llm 解耦 |
| `evaluation`（顶层） | 唯一引用 `examples/eval/main.go` | 随 examples 进 `_fixtures` |
| `services/embedding` | 0 Go 引用（独立进程） | 留，不进 Go 包合并范围 |
| `test/` `benchmarks/` | 非生产资产 | 原样保留，不计入"≤15" |

## examples 冻结约束

- `examples/` 当前 **34** 个顶层条目（33 目录 + README.md），清单见 `freeze-manifest.txt`
- **冻结规则**：不再新增 demo 目录；现有目录 Phase 4 降级为 `examples/_fixtures/`
- 巡查脚本 `scripts/check_convergence_freeze.sh`（CI 接入中）
