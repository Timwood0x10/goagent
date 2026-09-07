package agentfabric

import (
	"errors"
	"fmt"
	"time"
)

// P3 resource governance — Agent Runtime resource governance, NOT cgroups.
//
// aresos-plan.md P3 (converged): the Kernel governs cognitive-execution
// budgets, not CPU timeslices. An agent runs with:
//
//	Agent A: token budget = 50k, tool budget = 100, deadline = 10m
//
// The Kernel enforces:
//
//	if budget.exceeded:   yield()          // cooperative, at quantum boundary
//	if deadline:         suspend / fail / handoff
//	if lease.expired:    requeue
//
// This file adds the budget side. Lease expiry is already covered by the
// taskfabric/recovery chain; cgroup-style CPU isolation is explicitly out of
// scope (aresos-plan.md 核心模型修正 §9).

// Governance is an agent's cognitive-execution budget. Zero values mean
// "unlimited" for that dimension. It is set at spawn time and read-only for
// the agent's lifetime (change via Retire+Spawn or ResetResource for the
// consumption counters).
type Governance struct {
	// TokenBudget is the max tokens an agent may consume across its life.
	TokenBudget int
	// ToolBudget is the max tool calls an agent may make.
	ToolBudget int
	// Deadline is the wall-clock lifetime from spawn. Zero = no deadline.
	Deadline time.Duration
}

// ErrResourceExceeded is returned by ConsumeResource when a budget is
// exhausted; callers (the scheduler at a quantum boundary) should treat it as
// a cooperative yield signal, not a crash.
var ErrResourceExceeded = errors.New("agentfabric: resource budget exceeded")

// governanceState is the per-agent consumption counters, guarded by Agent.mu.
type governanceState struct {
	cfg       Governance
	tokenUsed int
	toolUsed  int
	deadline  time.Time // zero if no deadline
}

// CheckResource reports whether the agent may consume the given token/tool
// amounts in the NEXT quantum WITHOUT recording consumption. It is the
// scheduler's pre-quantum gate (validate before execute, code_rules).
// ok=false means at least one budget would be exceeded.
func (f *Fabric) CheckResource(agentID string, token, tool int) (ok bool, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, found := f.agents[agentID]
	if !found {
		return false, ErrAgentNotFound
	}
	g, err := a.governanceLocked()
	if err != nil {
		return false, err
	}
	if g.cfg.TokenBudget > 0 && g.tokenUsed+token > g.cfg.TokenBudget {
		return false, nil
	}
	if g.cfg.ToolBudget > 0 && g.toolUsed+tool > g.cfg.ToolBudget {
		return false, nil
	}
	return true, nil
}

// ConsumeResource records token/tool consumption. It returns
// ErrResourceExceeded when a budget is exhausted — the cooperative yield
// signal the plan P3 requires. Consumption is only recorded on success; a
// failed quantum does not burn budget.
func (f *Fabric) ConsumeResource(agentID string, token, tool int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, found := f.agents[agentID]
	if !found {
		return ErrAgentNotFound
	}
	g, err := a.governanceLocked()
	if err != nil {
		return err
	}
	if token > 0 {
		if g.cfg.TokenBudget > 0 && g.tokenUsed+token > g.cfg.TokenBudget {
			return fmt.Errorf("%w: token budget %d (used %d + want %d)",
				ErrResourceExceeded, g.cfg.TokenBudget, g.tokenUsed, token)
		}
		g.tokenUsed += token
	}
	if tool > 0 {
		if g.cfg.ToolBudget > 0 && g.toolUsed+tool > g.cfg.ToolBudget {
			return fmt.Errorf("%w: tool budget %d (used %d + want %d)",
				ErrResourceExceeded, g.cfg.ToolBudget, g.toolUsed, tool)
		}
		g.toolUsed += tool
	}
	return nil
}

// DeadlineExceeded reports whether the agent's wall-clock deadline (if any)
// has passed. The scheduler checks this at quantum boundaries to suspend/fail
// a runaway agent instead of preempting it mid-reasoning.
func (f *Fabric) DeadlineExceeded(agentID string) (exceeded bool, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, found := f.agents[agentID]
	if !found {
		return false, ErrAgentNotFound
	}
	g, err := a.governanceLocked()
	if err != nil {
		return false, err
	}
	if g.cfg.Deadline <= 0 {
		return false, nil
	}
	return !f.now().Before(g.deadline), nil
}

// ResetResource clears the agent's consumption counters (and re-arms its
// deadline) — the "new quantum start" hook the plan P3 describes after a
// checkpoint/resume boundary.
func (f *Fabric) ResetResource(agentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, found := f.agents[agentID]
	if !found {
		return ErrAgentNotFound
	}
	a.mu.Lock()
	a.governance.tokenUsed = 0
	a.governance.toolUsed = 0
	if a.governance.cfg.Deadline > 0 {
		a.governance.deadline = f.now().Add(a.governance.cfg.Deadline)
	}
	a.mu.Unlock()
	return nil
}

// governanceLocked reads the agent's governance state under Agent.mu. Callers
// must hold Fabric.mu (registry lock). Every agent has a governance state from
// birth (zero-value = unlimited), so this never fails.
func (a *Agent) governanceLocked() (*governanceState, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.governance, nil
}

// BudgetUsage returns the current consumption (tokens/tools) for observability
// and audit.
func (f *Fabric) BudgetUsage(agentID string) (token, tool int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, found := f.agents[agentID]
	if !found {
		return 0, 0, ErrAgentNotFound
	}
	g, err := a.governanceLocked()
	if err != nil {
		return 0, 0, err
	}
	return g.tokenUsed, g.toolUsed, nil
}
