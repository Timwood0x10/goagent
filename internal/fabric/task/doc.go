// Package taskfabric is the ARES task projection layer (convergence
// Phase 2b: relocated from internal/taskfabric, package identity kept).
//
// It projects plan graphs into executable fabric tasks and tracks their
// lifecycle: the READY/RUNNING/COMPLETED/FAILED state machine, leases with
// epoch fencing, dependency gating (IsReady/ReadyTasks — the scheduler's
// work source, not a second DAG), incremental compilation of live-graph
// edits, checkpoints, and terminal-task reaping. The single task graph
// itself lives in workflow/ (engine.MutableDAG); this package is the
// execution projection of that plan.
package taskfabric
