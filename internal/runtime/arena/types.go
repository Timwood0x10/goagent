// Package arena provides a chaos engineering layer that proves ares is a
// self-healing runtime. Arena deliberately calls dangerous APIs on existing
// systems; the existing resurrection plugin, failover, and checkpoint recovery
// handle the rest.
package arena

import "time"

// ActionType classifies an arena action.
type ActionType string

const (
	ActionKillLeader       ActionType = "kill_leader"
	ActionKillAgent        ActionType = "kill_agent"
	ActionRemoveNode       ActionType = "remove_node"
	ActionRemoveEdge       ActionType = "remove_edge"
	ActionPauseAgent       ActionType = "pause_agent"
	ActionResumeAgent      ActionType = "resume_agent"
	ActionSlowAgent        ActionType = "slow_agent"
	ActionKillOrchestrator ActionType = "kill_orchestrator"
	ActionNetworkPartition ActionType = "network_partition"

	// Tool and infrastructure fault injection.
	ActionToolTimeout   ActionType = "tool_timeout"
	ActionMemoryCorrupt ActionType = "memory_corrupt"
	ActionMCPDisconnect ActionType = "mcp_disconnect"
	ActionLLMFailure    ActionType = "llm_failure"
)

// Action represents a single chaos action to inject.
type Action struct {
	ID        string         `json:"id" yaml:"id"`
	Type      ActionType     `json:"type" yaml:"type"`
	TargetID  string         `json:"target_id" yaml:"target_id"`
	SourceID  string         `json:"source_id,omitempty" yaml:"source_id,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at" yaml:"created_at"`
}

// Result holds the outcome of an action.
type Result struct {
	Success  bool          `json:"success"`
	Action   Action        `json:"action"`
	Error    string        `json:"error,omitempty"`
	Duration time.Duration `json:"duration"`
}

// Stats aggregates arena action statistics.
type Stats struct {
	TotalActions      int       `json:"total_actions"`
	SuccessfulActions int       `json:"successful_actions"`
	FailedActions     int       `json:"failed_actions"`
	LastAction        time.Time `json:"last_action"`
}
