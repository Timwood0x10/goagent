package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Timwood0x10/ares/internal/core/models"
	"github.com/Timwood0x10/ares/internal/fabric/task"
)

// kernelFabricDispatcher is the kernel's Task Fabric dispatch path. Its D()
// behavior depends on whether an executeFn is attached (enableKernelExecution):
//
//   - scoring mode (no executeFn): scores the task against the candidate
//     agents with the Kernel's capability-aware formula (taskfabric.Score/Pick)
//     and reports the would-be outcome. It never creates, acquires or executes
//     — a task is never double-run.
//   - execution mode (executeFn attached): runs the real Task Fabric path
//     (Create → Schedule → Acquire → RunQuantum).
type kernelFabricDispatcher struct {
	candidates []subAgentCapability
	executeFn  func(ctx context.Context, task *models.Task) error // nil = scoring only
}

// D routes the task through the kernel's Task Fabric path: scoring (no
// executeFn) or real execution (executeFn attached).
func (d *kernelFabricDispatcher) D(ctx context.Context, agentID, taskID string, payload any) error {
	task, err := taskFromPayload(taskID, payload)
	if err != nil {
		return fmt.Errorf("kernel fabric dispatch: %w", err)
	}
	if d.executeFn != nil {
		return d.executeFn(ctx, task)
	}
	if len(d.candidates) == 0 {
		return nil
	}
	cands := make([]taskfabric.Candidate, 0, len(d.candidates))
	for _, c := range d.candidates {
		caps := c.Caps
		if len(caps) == 0 {
			caps = []string{c.Type}
		}
		cands = append(cands, taskfabric.Candidate{
			AgentID:      c.ID,
			Capabilities: caps,
			Load:         c.Load,
			Confidence:   1.0, // shadow: no experience store wired here
		})
	}
	if winner := taskfabric.Pick(string(task.AgentType), cands); winner == nil {
		return taskfabric.ErrNoCapableCandidate
	}
	return nil
}

// taskFromPayload builds a models.Task from the agentipc dispatch arguments.
// The payload is a map carrying the task's AgentType (capability), its DAG
// dependencies (Task Fabric gate) and any opaque user
// data; absent metadata falls back to a default type.
func taskFromPayload(taskID string, payload any) (*models.Task, error) {
	if taskID == "" {
		return nil, errors.New("task id required")
	}
	task := models.NewTask(taskID, models.AgentTypeTop, nil)
	if m, ok := payload.(map[string]any); ok {
		if at, ok := m["agent_type"].(string); ok && at != "" {
			task.AgentType = models.AgentType(at)
		}
		// UserProfile arrives as the same-process struct reference (the
		// kernel dispatcher passes it through untouched) — OR as a plain
		// map after a JSON round-trip (web serve → HTTP → decode). Both are
		// restored so the executor never sees profile==nil and degrades to
		// executeByType — the serve no-op chain.
		if up, ok := m["user_profile"].(*models.UserProfile); ok && up != nil {
			task.UserProfile = up
		} else if raw, ok := m["user_profile"].(map[string]any); ok {
			if buf, err := json.Marshal(raw); err == nil {
				var up models.UserProfile
				if err := json.Unmarshal(buf, &up); err == nil {
					task.UserProfile = &up
				}
			}
		}
		// Dependencies arrive as []string when the payload passes through the
		// kernel dispatcher directly and as []any after a JSON round-trip —
		// accept both so the DAG gate is never silently dropped.
		switch deps := m["dependencies"].(type) {
		case []string:
			task.Context.Dependencies = append(task.Context.Dependencies, deps...)
		case []any:
			for _, dep := range deps {
				if s, ok := dep.(string); ok && s != "" {
					task.Context.Dependencies = append(task.Context.Dependencies, s)
				}
			}
		}
		task.Payload = m
	}
	return task, nil
}
