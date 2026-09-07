// Package aresrecovery implements the ARES Kernel Recovery subsystem
// (see docs/zh/architecture/ares-runtime.md): the independent responsibility of keeping durable
// tasks alive across agent deaths.
//
// Design invariants :
//   - Agent is disposable, Task is durable — Agent death ≠ Task death.
//   - Recovery is a distinct responsibility from Chaos (failure injection):
//     Chaos breaks things on purpose; Recovery proves the Runtime survives.
//
// The Recovery subsystem orchestrates three failure paths:
//  1. Lease expiry → requeue: a dead agent's lease expires; the task returns
//     to READY and another agent can acquire it (Task Fabric).
//  2. Checkpoint recovery: a task's preserved checkpoint is resumed by a new
//     agent (Task Fabric + Agent Fabric CognitiveState).
//  3. Agent restart: a crashed agent is replaced by a new one that picks up
//     the dead agent's cognitive checkpoint (Agent Fabric Recover).
//
// Chaos (Failure Injection + RecoveryVerification) is the verification
// harness: it kills agents deliberately, then invokes Recovery to prove the
// Runtime restores the tasks. Evolution (Runtime Adaptation) changes
// scheduling policy / agent population based on observed behavior.
package aresrecovery
