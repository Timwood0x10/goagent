// Package experience provides storage and retrieval of normalized experiences
// for the ARES evolution system. It abstracts the persistence layer for strategy
// evolution data and enables querying historical experiences.
package experience

import (
	"context"
	"errors"
	"time"
)

// NormalizedExperience represents a standardized, strategy-agnostic experience
// that can be stored and retrieved for future learning and strategy improvement.
type NormalizedExperience struct {
	// ID is the unique identifier of this experience.
	ID string `json:"id"`

	// StrategyID identifies the strategy that generated this experience.
	StrategyID string `json:"strategy_id"`

	// TaskType categorizes the task (e.g., "code_review", "bug_fix").
	TaskType string `json:"task_type"`

	// Problem describes the abstract problem statement.
	Problem string `json:"problem"`

	// Solution describes the successful approach or resolution.
	Solution string `json:"solution"`

	// Outcome indicates the result quality (e.g., "success", "partial", "failure").
	Outcome string `json:"outcome"`

	// Score is the effectiveness score of this experience (0-1).
	Score float64 `json:"score"`

	// Metadata holds additional structured data about the experience.
	Metadata map[string]interface{} `json:"metadata,omitempty"`

	// CreatedAt is the timestamp when this experience was recorded.
	CreatedAt time.Time `json:"created_at"`

	// TenantID is the tenant identifier for multi-tenancy isolation.
	TenantID string `json:"tenant_id"`

	// Normalizer-specific fields (added for GA/Memory/Tool fusion plan).

	// LatencyMs is the execution latency in milliseconds.
	LatencyMs int64 `json:"latency_ms,omitempty"`

	// WallTimeSeconds is the wall-clock execution time in seconds.
	WallTimeSeconds float64 `json:"wall_time_seconds,omitempty"`

	// ErrorRate is the error rate as a fraction [0, 1].
	ErrorRate float64 `json:"error_rate,omitempty"`

	// Success indicates whether the task was completed successfully.
	Success bool `json:"success,omitempty"`

	// Cost is the normalized computational cost (>= 0).
	Cost float64 `json:"cost,omitempty"`

	// MutationType describes the type of mutation used.
	MutationType string `json:"mutation_type,omitempty"`

	// ToolChain is a hash string representing the sequence of tools used.
	// This enables grouping experiences by tool usage patterns.
	ToolChain string `json:"tool_chain,omitempty"`

	// IsFiltered indicates whether this experience was filtered out as noise.
	IsFiltered bool `json:"is_filtered,omitempty"`

	// FilterReason describes why the experience was filtered (if applicable).
	FilterReason string `json:"filter_reason,omitempty"`
}

// ExperienceStore defines the interface for storing and retrieving normalized experiences.
// Implementations must be thread-safe and support concurrent access.
type ExperienceStore interface {
	// Append adds a normalized experience to the store.
	// Returns an error if the experience cannot be stored.
	//
	// Args:
	//   ctx - timeout and cancellation context.
	//   exp - the experience to store, must have valid StrategyID and TaskType.
	//
	// Returns:
	//   error - ErrInvalidExperience if validation fails, or storage error.
	Append(ctx context.Context, exp NormalizedExperience) error

	// AppendBatch adds multiple experiences in a single operation.
	// This is more efficient than calling Append multiple times.
	// Returns an error if any experience cannot be stored.
	//
	// Args:
	//   ctx - timeout and cancellation context.
	//   exps - the experiences to store, must not be empty.
	//
	// Returns:
	//   error - ErrInvalidExperience if validation fails, or storage error.
	AppendBatch(ctx context.Context, exps []NormalizedExperience) error

	// Query retrieves experiences filtered by strategy_id and time range.
	// Returns experiences sorted by CreatedAt in descending order.
	//
	// Args:
	//   ctx - timeout and cancellation context.
	//   strategyID - the strategy identifier to filter by.
	//   startTime - the start of the time range (inclusive).
	//   endTime - the end of the time range (inclusive).
	//
	// Returns:
	//   []NormalizedExperience - matching experiences, may be empty.
	//   error - storage error if query fails.
	Query(ctx context.Context, strategyID string, startTime, endTime time.Time) ([]NormalizedExperience, error)

	// QueryByTaskType retrieves experiences for a specific task type.
	// Returns experiences sorted by Score in descending order.
	//
	// Args:
	//   ctx - timeout and cancellation context.
	//   taskType - the task type to filter by.
	//   limit - maximum number of experiences to return (0 for no limit).
	//
	// Returns:
	//   []NormalizedExperience - matching experiences, may be empty.
	//   error - storage error if query fails.
	QueryByTaskType(ctx context.Context, taskType string, limit int) ([]NormalizedExperience, error)

	// GetStatistics returns aggregate statistics for a strategy.
	// Statistics include: avg_score, success_rate, total_experiences, etc.
	//
	// Args:
	//   ctx - timeout and cancellation context.
	//   strategyID - the strategy identifier to get statistics for.
	//
	// Returns:
	//   map[string]float64 - statistical metrics, may be empty.
	//   error - storage error if query fails.
	GetStatistics(ctx context.Context, strategyID string) (map[string]float64, error)

	// GetTaskTypeStatistics returns aggregate statistics for a task type.
	// This is used by MemoryAwareScorer to evaluate task-type-level performance
	// across all strategies.
	//
	// Statistics include: avg_score, experience_count, success_rate, confidence.
	//
	// Args:
	//   ctx - timeout and cancellation context.
	//   taskType - the task type to get statistics for.
	//
	// Returns:
	//   map[string]float64 - statistical metrics, may be empty.
	//   error - storage error if query fails.
	GetTaskTypeStatistics(ctx context.Context, taskType string) (map[string]float64, error)
}

// ExperienceStoreConfig holds configuration for experience store implementations.
type ExperienceStoreConfig struct {
	// MaxSize is the maximum number of experiences to store (0 for unlimited).
	MaxSize int `json:"max_size"`

	// EnableIndexing enables indexing for faster queries.
	EnableIndexing bool `json:"enable_indexing"`

	// RetentionDays is the number of days to retain experiences (0 for unlimited).
	RetentionDays int `json:"retention_days"`
}

// Store errors defined at package level.
var (
	// ErrInvalidExperience indicates that an experience validation failed.
	ErrInvalidExperience = errors.New("invalid experience: missing required fields")

	// ErrExperienceNotFound indicates that the requested experience was not found.
	ErrExperienceNotFound = errors.New("experience not found")

	// ErrStoreFull indicates that the store has reached its maximum capacity.
	ErrStoreFull = errors.New("experience store is full")
)
