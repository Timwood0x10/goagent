# ARES Agent-OS Grand-Loop Demo

一个**零依赖、可运行**的 Agent-OS 大闭环演示（`aresos-plan.md` 附件 E）。

## 为什么有它

`aresos-plan.md` 的「唯一大闭环」要求：不是分散的测试，而是一个**连续的、可运行的、
文档化的 Demo**，证明 ARES 的核心思想——Agent 像进程一样存在、像线程一样被 Kernel
调度、像 Peer 一样协作、像进程一样死亡和恢复。

与 `examples/26-runtime-scheduling-demo`（真实 LLM + leader/sub 编排）不同，本 demo
**不需要 LLM、不需要配置文件**：`agentfabric` 与 `agentipc` 是纯内存库，任何机器上
`go run` 即打印完整故事。这正是「大闭环」的确定性验收——不依赖外部服务。

## 运行

```bash
go run examples/aresos-demo/main.go
```

## 故事线（7 步 + P3 治理）

```
[1] User → Agent A 收到大任务「分析 Rust 项目 FFI 安全」
[2] A 判断任务太大 → Spawn B / C / D（同级 Peer，parent 仅 provenance）
[3] B / C / D 并行调查，各自独立 Cognitive State
[4] A 中途死亡 → Task 存活，B / C / D 继续（Agent death ≠ Task death）
[5] Peer IPC 协作：B 反驳 A 的拆分假设 → C 独立验证
[5b] P3 资源治理：A2 带 token/tool/deadline 预算，检查/消耗/查询
[6] Kernel 恢复任务 → 替代者 A2 从 checkpoint 接续并做 synthesis
[7] 输出最终报告（汇总 B/C/D 发现 + 协作验证结果）
```

## 覆盖的计划验收（aresos-plan.md 附件 D/E）

| Case | 本 demo 对应步骤 | 库测试 |
|---|---|---|
| Case 1：Agent 独立完成 | [3] 各 Peer 独立产出 | `TestKernelSchedulerQuantumYieldResume` 等 |
| Case 2：Agent 自主拆分 | [2] A 自主 Spawn | `TestP3_4_EndToEndSpawnSynthesis` |
| Case 3：父死 Task 不死 | [4]+[6] 恢复+接续 | `TestP3_4_ParentDeathChildrenContinueTasks` |
| Case 4：Peer 真正协作 | [5] B 反驳+C 验证 | `TestP4_ChildCanCommunicateWithNonParent` |
| 大闭环 | [1]-[7] 全部 | `TestE2E_GrandLoop_CompleteAgentOS` |

## 核心模型演示点

- **无等级**：B/C/D 由 A spawn 但同为 IDLE 可调度（`SpawnSpec.ParentID` 仅记录
  provenance，见 `agentfabric.Children`）。
- **Kernel 只管机制**：本 demo 里 `agentfabric` 提供 Spawn/生命周期/checkpoint，
  `agentipc` 提供协作——「要不要拆、找谁」完全是 A 的认知（demo 层的决策函数）。
- **Quantum 边界**：每步 `CheckpointCognitive` 即一个认知执行边界（一轮 ReAct），
  非墙钟时间片（见 aresos-plan.md 核心模型修正 §9）。

## 用到的公共 API（无任何库层改动）

- `agentfabric.NewFabric` / `Spawn` / `SetCognitiveState` / `CheckpointCognitive` / `Kill` / `Children`
- `agentipc.NewBus` / `Register` / `Request`

> 源码注释含完整 API 路径与学习目标。同一故事作为连续测试：
> `internal/agentfabric/e2e_grand_loop_test.go`（`TestE2E_GrandLoop_CompleteAgentOS`）。
