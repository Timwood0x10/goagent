// Package experience provides task result data structures for experience distillation.
package experience

// TaskResult represents the result of a task execution.
// This is the input for experience distillation.
type TaskResult struct {
	// Task is the description of the task that was executed.
	Task string `json:"task"`

	// Context provides additional context about the task execution.
	Context string `json:"context"`

	// Result is the output or outcome of the task execution.
	Result string `json:"result"`

	// Success indicates whether the task execution was successful.
	// Only successful tasks are distilled into experiences.
	Success bool `json:"success"`

	// AgentID is the identifier of the agent that executed the task.
	AgentID string `json:"agent_id"`

	// TenantID is the tenant identifier for multi-tenancy isolation.
	TenantID string `json:"tenant_id"`

	// UsedExperienceID is the experience ID that was used during task execution.
	// This is used for reinforcement signal tracking.
	UsedExperienceID string `json:"used_experience_id,omitempty"`
}
