package main

import (
	"context"
	"fmt"

	"github.com/Timwood0x10/ares/internal/agentipc"
	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/fabric/agent"
	"github.com/Timwood0x10/ares/internal/fabric/task"
)

// Kernel assembly entry (aresos-agentos-plan C1: leader path removed).
//
// wireKernelDispatcher assembles the single-track Task Fabric dispatch kernel:
// the legacy leader track has been deleted, so the dispatcher always routes
// through kernelFabricDispatcher — scoring first, then (after
// enableKernelExecution attaches the executor) real
// Create→Schedule→Acquire→RunQuantum execution. The PolicyFlag starts at
// PolicyTaskFabric with shadow mode OFF (there is no legacy track left to
// compare against).
//
// Returns:
//   - *agentipc.DualTrackDispatcher: the assembled kernel dispatcher.
//   - *agentipc.PolicyFlag: the execution policy flag.
func wireKernelDispatcher(
	subAgents []subAgentCapability,
) (*agentipc.DualTrackDispatcher, *agentipc.PolicyFlag) {
	flag := agentipc.NewPolicyFlag(agentipc.PolicyTaskFabric)
	newPath := &kernelFabricDispatcher{candidates: subAgents}
	// nil legacy track: the leader path is removed, so the "dual" track is
	// single-track from the start and shadow mode is off.
	return agentipc.NewDualTrackDispatcher(flag, nil, newPath, false), flag
}

// enableKernelExecution switches the kernel's Task Fabric path from scoring
// (shadow) to real execution: it attaches the submitting executor (Create with
// DAG edges — the kernelScheduler owns Schedule→Acquire→RunQuantum) to the
// dispatcher. Callers invoke this at startup (peer mode) in the same critical
// section as the flag set to PolicyTaskFabric.
//
// Args:
//   - kernel: the dispatcher assembled by wireKernelDispatcher.
//   - fabric: the Task Fabric that executes tasks.
func enableKernelExecution(
	kernel *agentipc.DualTrackDispatcher,
	fabric *taskfabric.Fabric,
) {
	// Turn shadow off first: with the new path about to become live, running
	// the previous path in shadow would re-dispatch every task (double
	// execution).
	kernel.SetShadow(false)
	// Replace the scoring-only path with the submitting one. IMPORTANT: the
	// dispatch only SUBMITS the task to the fabric (Create); the kernelScheduler
	// is the single executor (Schedule→Acquire→RunQuantum on every READY task).
	// Keeping the full execution in the dispatch path as well caused a
	// double-path race: both the dispatch and the scheduler tried to acquire the
	// same task, surfacing as "task not ready for acquire" in serve logs
	// (GAP #2 fix).
	exec := &kernelFabricDispatcher{
		candidates: kernelNewPathCandidates(kernel),
		executeFn: func(ctx context.Context, task *models.Task) error {
			return submitFabricTask(ctx, fabric, task)
		},
	}
	kernel.SetNewPath(exec)
}

// kernelNewPathCandidates extracts the candidate list from the kernel's new
// path so enableKernelExecution can rebuild it with an executor attached.
func kernelNewPathCandidates(kernel *agentipc.DualTrackDispatcher) []subAgentCapability {
	if fp, ok := kernel.NewPath().(*kernelFabricDispatcher); ok {
		return fp.candidates
	}
	return nil
}

// submitFabricTask SUBMITS a task to the Task Fabric (Create with DAG edges)
// WITHOUT executing it. Execution is the kernelScheduler's sole job: its
// drain runs Schedule→Acquire→RunQuantum on every READY task. The leader
// dispatch path must NOT also schedule the task — doing so created a
// double-path race where both the leader dispatch (executeFabricTask) and the
// kernelScheduler tried to acquire the same task, surfacing as
// "task not ready for acquire" in serve logs (GAP #2 fix).
//
// Args:
//   - ctx: task lifetime (unused; kept for signature symmetry).
//   - fabric: the Task Fabric that owns the task.
//   - task: the task to submit.
//
// Returns:
//   - error: fabric create error (ErrTaskExists is tolerated).
func submitFabricTask(
	ctx context.Context,
	fabric *taskfabric.Fabric,
	task *models.Task,
) error {
	if fabric == nil {
		return taskfabric.ErrTaskNotFound
	}
	// M4-D: single execution path. The dispatcher only materializes
	// L2-routable tasks — anything else would starve with no candidate
	// executor. Fail fast instead of creating an unrunnable task.
	if !agentfabric.IsL2Capability(string(task.AgentType)) {
		return fmt.Errorf("kernel bridge: agent type %q is not L2-routable (want ares/plan, ares/answer, ares/root, or tool/<name>)", task.AgentType)
	}
	var deps []string
	if task.Context != nil {
		deps = append([]string(nil), task.Context.Dependencies...)
	}
	if err := fabric.Create(&taskfabric.Task{
		ID:           task.TaskID,
		Capability:   string(task.AgentType),
		Dependencies: deps,
		Priority:     task.Priority,
		// Origin stays "" — kernel-bridge submissions are root tasks
		// (user/submitter-originated), not agent-created. Agent-created
		// tasks carry their caller via Task.Origin (create_task syscall).
		// RetryPolicy.MaxRetries counts TOTAL attempts, not retries-after-the-first
		// (taskfabric.CanRetry: Attempts < MaxRetries). MaxRetries: 1 therefore
		// grants ZERO retries — a transient failure finalizes FAILED immediately
		// (v0.3.0 review Bug 2). 2 = first attempt + one retry.
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 2},
		// Carry the submission-time metadata in the Checkpoint slot so the
		// scheduler's toModelTask can restore it for the executor (LLM path
		// needs the profile; the outcome recorder needs UsedExperienceID).
		// The envelope is the W3 versioned protocol (*CheckpointEnvelope);
		// a genuine progress checkpoint replaces it once a quantum runs
		// (RunQuantum yield).
		Checkpoint: &taskfabric.CheckpointEnvelope{
			UserProfile:      task.UserProfile,
			Payload:          task.Payload,
			UsedExperienceID: task.UsedExperienceID,
		},
	}); err != nil && err != taskfabric.ErrTaskExists {
		return fmt.Errorf("kernel fabric create: %w", err)
	}
	return nil
}

// subAgentCapability is the minimal capability surface the new-path scorer
// needs for one agent. Caps is the full declared capability set (C1 flat
// peers); Type is the primary capability (first of Caps, or the legacy single
// Type for sub-structure configs).
type subAgentCapability struct {
	ID   string
	Type string
	Caps []string
	Load float64
}
