package runtime

import "time"

// StepStatus represents the execution status of a workflow step.
type StepStatus string

const (
	StepStatusPending   StepStatus = "pending"
	StepStatusRunning   StepStatus = "running"
	StepStatusCompleted StepStatus = "completed"
	StepStatusFailed    StepStatus = "failed"
	StepStatusSkipped   StepStatus = "skipped"
)

// Step is a lightweight mirror of engine.Step used in WorkflowHook calls.
// It prevents a direct import dependency from runtime → workflow/engine.
type Step struct {
	ID        string
	Name      string
	AgentType string
	Status    StepStatus
	Output    string
	Error     string
	StartedAt time.Time
}

// StepResult is a lightweight mirror of engine.StepResult.
type StepResult struct {
	StepID   string
	Name     string
	Status   StepStatus
	Output   string
	Error    string
	Duration time.Duration
	Metadata map[string]string
}
