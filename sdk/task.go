package sdk

import (
	"context"
	"time"
)

// Task is the minimal unit of work a caller submits to a Runtime
// (极简 SDK — NewRuntime → RegisterAgent → Submit →
// 结果). It is deliberately field-light: the Runtime resolves the executor by
// capability, so callers never construct agents or reference any internal
// scheduling/leadership concept.
type Task struct {
	// ID is an optional caller-provided identifier. When empty, the Runtime
	// assigns one.
	ID string
	// Capability selects the registered agent that can handle this task
	// (exact match on the capability the agent was registered with). When
	// empty, any registered agent may handle it.
	Capability string
	// Input is the task content passed to the agent.
	Input string
	// Timeout caps the total wall-clock duration (<=0 = no limit).
	Timeout time.Duration
}

// RegisterAgent creates an agent and registers it as the handler for its
// capability (极简 SDK — 不暴露 leader/sub/kernel 概念). The agent is
// named after the capability; opts configure it (WithInstruction/WithTools/
// ...). The first agent registered for a capability wins; a later
// RegisterAgent for the same capability does not replace it.
//
// The returned *Agent is fully configurable via opts and can also be run
// directly via Run — Submit is the uniform entry point, not the only one.
//
// Capability must be non-empty: an agent with no capability is not reachable
// by Submit.
func (r *Runtime) RegisterAgent(capability string, opts ...AgentOption) *Agent {
	if capability == "" {
		capability = "agent"
	}
	a := r.NewAgent(capability, opts...)
	r.agentMu.Lock()
	defer r.agentMu.Unlock()
	if _, ok := r.agentByCapability[capability]; !ok {
		r.agentByCapability[capability] = a
		// also register the shared-scheduler executor so Submit drives
		// the agent through the fabric, not a direct call. Route through
		// sched.RegisterExecutor so the write hits the scheduler's own execMu
		// (no cross-lock race with the scheduler's reads).
		r.ensureScheduler()
		r.sched.RegisterExecutor(capability, &sdkAgentExecutor{agent: a})
	}
	return a
}

// Submit dispatches a task to the agent registered for its capability and
// returns the execution result (极简 SDK 闭环). The task goes through the
// SAME scheduling path as the kernel: fabric.Create → kernelscheduler
// (Schedule → Acquire → RunQuantum via the registered agent) → COMPLETED →
// result (合并 SDK 和 kernel 两条路径 — the SDK and the kernel share
// one scheduler; no divergent direct-run path). When no agent is registered
// for the task's capability, a capability-named agent is created on demand —
// a runtime never refuses a well-formed task just because it was not
// pre-registered.
//
// Timeout, when > 0, bounds the whole dispatch (and the execution — the
// executor receives the same context). The returned error wraps the agent's
// execution error; context cancellation surfaces as
// context.Canceled/DeadlineExceeded .
func (r *Runtime) Submit(ctx context.Context, t Task) (*Result, error) {
	return r.submitThroughScheduler(ctx, t)
}

// lookupAgent returns the agent registered for the capability, or nil. An
// empty capability returns any registered agent (the map iteration order is
// unspecified but stable within a process; prefer an explicit capability).
func (r *Runtime) lookupAgent(capability string) *Agent {
	r.agentMu.Lock()
	defer r.agentMu.Unlock()
	if capability != "" {
		return r.agentByCapability[capability]
	}
	for _, a := range r.agentByCapability {
		return a
	}
	return nil
}
