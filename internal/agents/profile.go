// Package agents provides AgentProfile — a simple specialization mechanism
// for multi-role agent collaboration in ARES 0.3.0.
//
// Design principle (from AI Agents in Depth Ch.10):
// "Multiple agents don't need complex registries or message buses.
// They need explicit handoffs between roles with different instructions
// and tool sets, all within the same runtime."
package agents

import (
	"context"
	"sync"
)

// AgentProfile defines a specialized agent role.
// Each profile has its own system prompt (Instructions) and tool set.
// Roles are switched via Handoff, not by creating new agent instances.
type AgentProfile struct {
	// ID is the unique identifier for this profile.
	ID string

	// Role is the human-readable role name.
	// Examples: "researcher", "coder", "reviewer", "planner"
	Role string

	// Instructions is the system prompt for this role.
	// This replaces the static system prompt when this role is active.
	Instructions string

	// Tools is the list of tool names available to this role.
	// Empty means "use all tools".
	Tools []string

	// Metadata carries optional role-specific configuration.
	Metadata map[string]any
}

// ProfileRegistry manages agent profiles.
// Simple map-based registry — no complex lookup logic needed for 0.3.0.
type ProfileRegistry struct {
	mu       sync.RWMutex
	profiles map[string]*AgentProfile
}

// NewProfileRegistry creates an empty registry.
func NewProfileRegistry() *ProfileRegistry {
	return &ProfileRegistry{
		profiles: make(map[string]*AgentProfile),
	}
}

// Register adds or updates a profile. A nil profile is ignored —
// dereferencing p.ID would panic the caller's goroutine.
func (r *ProfileRegistry) Register(p *AgentProfile) {
	if p == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.profiles[p.ID] = p
}

// Get returns a profile by ID. Returns nil if not found.
func (r *ProfileRegistry) Get(id string) *AgentProfile {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.profiles[id]
}

// List returns all registered profiles.
func (r *ProfileRegistry) List() []*AgentProfile {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*AgentProfile, 0, len(r.profiles))
	for _, p := range r.profiles {
		result = append(result, p)
	}
	return result
}

// Has checks if a profile exists.
func (r *ProfileRegistry) Has(id string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.profiles[id]
	return ok
}

// Built-in role identifiers for the 0.3.0 default profile set.
const (
	RolePlanner    = "planner"
	RoleResearcher = "researcher"
	RoleCoder      = "coder"
	RoleReviewer   = "reviewer"
)

// DefaultProfiles returns the built-in role set for 0.3.0.
// These are the starting points; users can register custom profiles.
func DefaultProfiles() map[string]*AgentProfile {
	return map[string]*AgentProfile{
		RolePlanner: {
			ID:   RolePlanner,
			Role: RolePlanner,
			Instructions: `You are a task planner. Your job is to understand the user's request,
break it down into sub-tasks, and coordinate other agents to execute them.

Rules:
- Ask clarifying questions before planning if the request is ambiguous.
- Each sub-task should be assignable to a single specialist agent.
- Never write code or do research yourself — delegate.
- When all sub-tasks are complete, synthesize the final result.`,
			Tools: []string{"ask_clarifying_question", "save_requirement", "delegate_task"},
		},
		RoleResearcher: {
			ID:   RoleResearcher,
			Role: RoleResearcher,
			Instructions: `You are a researcher. Your job is to find accurate information,
verify claims, and provide well-sourced insights.

Rules:
- Always cite your sources. Never state unverified claims as facts.
- Use multiple independent sources to cross-check important facts.
- If you cannot find reliable information, say so explicitly.
- Return structured findings, not just raw text.`,
			Tools: []string{"web_search", "read_webpage", "calculate", "summarize"},
		},
		RoleCoder: {
			ID:   RoleCoder,
			Role: RoleCoder,
			Instructions: `You are a software engineer. Your job is to write, test, and debug code.

Rules:
- Write modular, well-commented code.
- Run tests before declaring completion.
- Handle errors gracefully.
- If a approach fails, try an alternative before giving up.`,
			Tools: []string{"write_file", "read_file", "execute_code", "run_tests", "debug"},
		},
		RoleReviewer: {
			ID:   RoleReviewer,
			Role: RoleReviewer,
			Instructions: `You are a code reviewer. Your job is to evaluate code quality,
correctness, and security.

Rules:
- Check for correctness bugs, not just style issues.
- Flag security concerns explicitly.
- Distinguish between "must fix" and "nice to have".
- Provide specific line references when possible.`,
			Tools: []string{"read_file", "run_linter", "analyze_complexity", "check_security"},
		},
	}
}

// ApplyToContext switches the active profile in the context.
// Called by the runtime when a Handoff transitions to a new role.
//
// NOTE: This method has zero production callers. To wire: register
// ProfileRegistry in bootstrap, then call ApplyToContext in the agent
// spawn path (e.g. agentfabric or kernelscheduler) to inject the
// selected profile into the context before the first Execute call.
// GetFromContext below is the read side; nothing reads it in production
// yet — the wiring is complete only when a spawn path calls ApplyToContext.
func (r *ProfileRegistry) ApplyToContext(ctx context.Context, profileID string) (context.Context, *AgentProfile, error) {
	profile := r.Get(profileID)
	if profile == nil {
		return ctx, nil, ErrProfileNotFound
	}
	// Store profile reference in context for downstream use
	return context.WithValue(ctx, ctxKeyProfile, profile), profile, nil
}

// GetFromContext retrieves the currently active profile.
func GetFromContext(ctx context.Context) *AgentProfile {
	if p, ok := ctx.Value(ctxKeyProfile).(*AgentProfile); ok {
		return p
	}
	return nil
}

// WithProfile returns a context carrying the given profile directly. It is
// the no-registry form of ProfileRegistry.ApplyToContext for executors that
// hold their role at construction time (the sub executor pins
// its profile via WithProfile and applies it at every task entry).
func WithProfile(ctx context.Context, p *AgentProfile) context.Context {
	if p == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKeyProfile, p)
}

// ctxKeyProfile is the context key for the active agent profile.
type ctxKey string

const ctxKeyProfile ctxKey = "ares_agent_profile"

// ErrProfileNotFound is returned when a requested profile doesn't exist.
var ErrProfileNotFound = &ProfileError{ProfileID: ""}

// ProfileError indicates a problem with a profile operation.
type ProfileError struct {
	ProfileID string
	Message   string
}

// Error returns a human-readable description of the profile error.
// It falls back to a generic message when no specific details are set.
func (e *ProfileError) Error() string {
	if e.ProfileID != "" {
		return "profile not found: " + e.ProfileID
	}
	if e.Message != "" {
		return e.Message
	}
	return "profile not found"
}
