// Package fabric is the unified orchestration layer for ARES AgentOS.
//
// It consolidates four modules into a single fabric:
//
//   - agentfabric (agent lifecycle: Spawn/Suspend/Resume/Retire/Kill)
//   - taskfabric (task projection: state machine, Lease+Epoch fencing, DAG)
//   - workflow/engine (MutableDAG: the single task graph + L1 evolution surface)
//   - planprojection (graph→task incremental compilation)
//
// The original packages have been migrated into internal/fabric/
// sub-packages. Package names are preserved to minimize import churn.
package fabric
