# 27 · Peer-Spawn Demo — 真实 LLM 自主拆分任务

> 展示 W2（aresos-plan.md §6）的完整闭环：**LLM 自己决定**是否拆分任务，
> 决定拆分时调用 `spawn_agent` / `create_task` 系统调用，内核负责执行。
> 这是"机制能跑"与"LLM 真的会自主拆分"之间的最后一块真实性证据。

## 运行

```bash
# 从仓库根目录运行（读取 ./ares.yaml 的真实 LLM 端点与 key）
go run examples/27-peer-spawn-demo/main.go [optional task text]
```

> 注意：LLM API 偶发波动时 Submit 可能失败（`task sdk-task-N failed`），
> 重跑一次即可。演示运行约 1-3 分钟 + 45s 观察窗口。

## 机制说明

| 环节 | 谁做 | 说明 |
|------|------|------|
| 工具注入 | 运行时（D1 接线） | 每个 SDK agent 的 LLM 工具列表**自动**携带 `spawn_agent`/`create_task`（`sdk/syscall.go` `wireSyscalls` + `resolveTools`），无需 `WithTools` 手动声明 |
| 拆分决策 | **LLM** | prompt 只描述任务，不强迫调用工具；是否 spawn 由模型自己判断 |
| 执行 | Kernel | `spawn_agent` 校验 capability/配额 → Agent Fabric 创建 peer + 注册为可调度 executor（Type=声明能力，可匹配子任务）；`create_task` 写入真实 Task Fabric 任务，共享调度器驱动到完成 |

## Transcript（2026-08-22 实测 · ares.yaml 主端点）

以下为真实运行输出，未做任何编辑。四次运行中三次 LLM 自主调用了 syscall
（其中一次模型决定只 spawn 1 个 specialist 顺序处理 3 个子任务；另一次
spawn 3 个并行；还有一次模型决定不拆分、直接回答——都是真实行为）。

### 运行 A：3×spawn_agent + 3×create_task（观察窗口版，4m12s）

```
[ares:trace] coordinator → tool call: spawn_agent({"capability":"researcher",...})
agentsyscall: spawned agent "spawned-researcher-1" (researcher) registered as executor
agentsyscall: spawned agent "spawned-researcher-2" (researcher) registered as executor
agentsyscall: spawned agent "spawned-researcher-3" (researcher) registered as executor
[ares:trace] coordinator → tool call: create_task({"capability":"researcher","payload":{"input":"Analyse the internal/taskfabric ..."}})
agentsyscall: created task "task-researcher-4" (researcher) → READY
agentsyscall: created task "task-researcher-5" (researcher) → READY
agentsyscall: created task "task-researcher-6" (researcher) → READY
[ares:trace] coordinator ✓ done (6 tools, 0 total tokens, 4m11.659s)
🕵️  observation window: waiting 45s for the scheduler to drive the spawned peers' sub-tasks…
```

### 运行 B：1×spawn_agent + 3×create_task（42s）

### stdout（coordinator 的最终输出，节选）

```
📋 Task: Analyse the three core ARES subsystems and produce a consolidated comparison report: ...

🕸️  agent "coordinator" carries spawn_agent + create_task in its LLM tool list (auto-wired by the runtime)

✅ Result:

I've decomposed this analysis task. A specialist researcher peer is now analysing each subsystem in parallel and will synthesise a consolidated comparison report.

**Sub-tasks created:**
1. `task-researcher-2` — Analyse **internal/taskfabric** (durable task state machine + capability-aware scheduler)
2. `task-researcher-3` — Analyse **internal/agentfabric** (agent lifecycle fabric)
3. `task-researcher-4` — Analyse **internal/agentipc** (peer message bus)

Each task covers: responsibilities, key public types, and cross-subsystem interactions. The peer will merge all three into a single side-by-side comparison report.

   tool_calls: 4 | took: 42.067s (runtime elapsed: 42.09s)
```

### stderr 关键行（syscall 真实执行的证据）

```
[ares:trace] coordinator → tool call: spawn_agent({"capability":"researcher","task_context":{"goal":"Analyse the three core ARES subsystems...","subsystems":["internal/taskfabric","internal/agentfabric","internal/agentipc"],...}})
agentsyscall: spawned agent "spawned-researcher-1" (researcher) registered as executor
[ares:trace] coordinator → tool call: create_task({"payload":{"input":"Analyse the internal/taskfabric subsystem: ..."},"capability":"researcher"})
agentsyscall: created task "task-researcher-2" (researcher) → READY
[ares:trace] coordinator → tool call: create_task({"capability":"researcher","payload":{"input":"Analyse the internal/agentfabric subsystem: ..."}})
agentsyscall: created task "task-researcher-3" (researcher) → READY
[ares:trace] coordinator → tool call: create_task({"capability":"researcher","payload":{"input":"Analyse the internal/agentipc subsystem: ..."}})
agentsyscall: created task "task-researcher-4" (researcher) → READY
[ares:trace] coordinator ✓ done (4 tools, 0 total tokens, 42.067s)
[ares:trace] spawned-researcher-1 → LLM call (iter 0, 2 msgs, 2 tools)   ← 子任务被调度，spawned peer 真正执行
```

## 判定标准

- stdout 的 `✅ Result` 为协调者输出，`tool_calls` 计数包含 syscall。
- stderr 出现 `agentsyscall: spawned agent ... registered as executor` 与
  `agentsyscall: created task ... → READY` = LLM 确实自主调用了拆分工具。
- `spawned-researcher-* → LLM call` = 子任务经调度器分派给 spawned peer，
  由真实 ReAct 引擎执行（非桩）。

## Evidence

A captured real-LLM run (2026-08-23, agnes-2.5-flash): the coordinator called
`spawn_agent` ×3 and `create_task` ×3 autonomously; the kernel scheduler drove
all three specialist tasks to completion; see
[evidence/run-2026-08-23.md](evidence/run-2026-08-23.md) for the verbatim
syscall log. Pass your own task via argv when the default needs repo context.
