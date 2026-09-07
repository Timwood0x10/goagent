// Package agentfabric implements the ARES Kernel Lifecycle pillar (P3 of
// docs/zh/architecture/ares-runtime.md): Agent as a disposable, peer-equivalent cognitive
// process managed by the Runtime.
//
// Design invariants :
//   - Agent is a same-level cognitive process — A ≡ B ≡ C; parent/child only
//     carries spawn provenance, NOT a permission hierarchy.
//   - Task is durable, Agent is disposable — Agent death ≠ Task death.
//   - Kernel does not think — Agent decides; Kernel enforces.
//   - spawn is a syscall, not an orchestration API.
//
// The Fabric owns Agent registry, lifecycle primitives (spawn / suspend /
// resume / retire / kill / recover), the Process Tree (spawn causality) and
// the three-layer Context separation (Task Shared / Agent Private / IPC
// Messages). It does NOT schedule — that is the Scheduler's job (taskfabric).
package agentfabric
