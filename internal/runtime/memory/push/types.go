// Package push provides active knowledge recommendation by proactively pushing
// relevant distilled knowledge to strategies. It complements the report package
// (passive, human-readable) with an active delivery mechanism that targets
// strategies based on relevance criteria such as strategy ID, task type,
// prompt template, or evidence key.
package push

import (
	"context"
	"errors"
	"time"
)

// PushPolicy controls when the PushService delivers knowledge to targets.
type PushPolicy string

const (
	// PolicyOnDemand means knowledge is pushed only when PushRelevant is called.
	PolicyOnDemand PushPolicy = "on_demand"
	// PolicyScheduled means the service periodically pushes relevant knowledge on a ticker.
	PolicyScheduled PushPolicy = "scheduled"
	// PolicyEventTriggered means the service pushes when an external event arrives.
	PolicyEventTriggered PushPolicy = "event_triggered"
)

// RelevanceCriteria identifies which strategies a knowledge item applies to.
// At least one non-empty field must match for the item to be considered relevant.
// Empty fields act as wildcards: an item with no criteria set is relevant to all.
type RelevanceCriteria struct {
	// StrategyID, when non-empty, restricts to a specific strategy.
	StrategyID string
	// TaskType, when non-empty, restricts to a specific task type.
	TaskType string
	// PromptTemplate, when non-empty, restricts to a specific prompt template.
	PromptTemplate string
	// EvidenceKey, when non-empty, restricts to a specific evidence key (phenotype hash).
	EvidenceKey string
}

// IsEmpty reports whether the criteria has no constraints (matches everything).
func (r RelevanceCriteria) IsEmpty() bool {
	return r.StrategyID == "" && r.TaskType == "" && r.PromptTemplate == "" && r.EvidenceKey == ""
}

// PushTarget is a strategy or downstream consumer that can receive knowledge.
// Implementations record the criteria that identifies the strategy.
type PushTarget interface {
	// ID returns a unique target identifier (e.g., strategy ID).
	ID() string
	// Criteria returns the relevance criteria that should match this target.
	Criteria() RelevanceCriteria
	// Deliver delivers a knowledge item to the target.
	// Implementations must be safe for concurrent invocation.
	//
	// Args:
	//   ctx - timeout and cancellation context.
	//   item - the knowledge item to deliver.
	//
	// Returns:
	//   error - non-nil if delivery failed; the service logs and continues.
	Deliver(ctx context.Context, item KnowledgeItem) error
}

// KnowledgeItem is the distilled knowledge representation used by the push package.
// It mirrors report.KnowledgeItem but is defined locally to avoid an import cycle
// (the push package is consumed by the pipeline together with report, and the
// pipeline owns the conversion between them).
type KnowledgeItem struct {
	// ID uniquely identifies the knowledge item.
	ID string `json:"id"`
	// Category is the knowledge category.
	Category string `json:"category"`
	// Problem is the abstract problem statement.
	Problem string `json:"problem"`
	// Solution is the concise solution or insight.
	Solution string `json:"solution"`
	// Score is the importance / confidence score in [0, 1].
	Score float64 `json:"score"`
	// Source identifies where this knowledge originated.
	Source string `json:"source"`
	// StrategyID, when non-empty, links this knowledge to a strategy.
	StrategyID string `json:"strategy_id,omitempty"`
	// TaskType, when non-empty, links this knowledge to a task category.
	TaskType string `json:"task_type,omitempty"`
	// PromptTemplate, when non-empty, links this knowledge to a prompt template.
	PromptTemplate string `json:"prompt_template,omitempty"`
	// EvidenceKey, when non-empty, is the stable evidence key.
	EvidenceKey string `json:"evidence_key,omitempty"`
	// CreatedAt is when this knowledge item was first distilled.
	CreatedAt time.Time `json:"created_at"`
}

// KnowledgeProvider is the source interface for retrieving distilled knowledge.
// Implementations may wrap a distillation Memory slice or an experience store.
type KnowledgeProvider interface {
	// ListKnowledge returns all knowledge items available for pushing.
	//
	// Args:
	//   ctx - timeout and cancellation context.
	//
	// Returns:
	//   []KnowledgeItem - the available items (empty, not nil, when none).
	//   error - wrapped error if retrieval fails.
	ListKnowledge(ctx context.Context) ([]KnowledgeItem, error)
}

// PushResult records the outcome of a single push attempt to one target.
type PushResult struct {
	// TargetID is the ID of the target that received (or rejected) the item.
	TargetID string `json:"target_id"`
	// ItemID is the ID of the knowledge item that was pushed.
	ItemID string `json:"item_id"`
	// Delivered is true if Deliver succeeded.
	Delivered bool `json:"delivered"`
	// Error, when non-empty, records the delivery error message.
	Error string `json:"error,omitempty"`
}

// PushBatchResult records the outcome of pushing multiple items to multiple targets.
type PushBatchResult struct {
	// TotalTargets is the number of targets considered.
	TotalTargets int `json:"total_targets"`
	// TotalItems is the number of knowledge items considered.
	TotalItems int `json:"total_items"`
	// Delivered is the number of successful deliveries.
	Delivered int `json:"delivered"`
	// Skipped is the number of items skipped because they were not relevant.
	Skipped int `json:"skipped"`
	// Failed is the number of items that failed delivery.
	Failed int `json:"failed"`
	// Results is the per-delivery outcome list.
	Results []PushResult `json:"results"`
	// StartedAt is when the batch began.
	StartedAt time.Time `json:"started_at"`
	// FinishedAt is when the batch completed.
	FinishedAt time.Time `json:"finished_at"`
}

// PushConfig holds configuration for the PushService.
type PushConfig struct {
	// Policy selects the push policy. Default: PolicyOnDemand.
	Policy PushPolicy
	// MinScore is the minimum score for an item to be eligible for pushing (0 disables).
	MinScore float64
	// Interval is the push interval for PolicyScheduled. Default: 1 minute.
	Interval time.Duration
	// MaxItemsPerTarget caps the number of items delivered to a single target per batch.
	// 0 means unlimited.
	MaxItemsPerTarget int
}

// DefaultPushConfig returns a PushConfig with sensible defaults.
func DefaultPushConfig() *PushConfig {
	return &PushConfig{
		Policy:            PolicyOnDemand,
		MinScore:          0.5,
		Interval:          time.Minute,
		MaxItemsPerTarget: 10,
	}
}

// Sentinel errors for the push package.
var (
	// ErrInvalidConfig is returned when the push configuration is invalid.
	ErrInvalidConfig = errors.New("invalid push configuration")
	// ErrNoTargets is returned when there are no registered targets.
	ErrNoTargets = errors.New("no push targets registered")
	// ErrAlreadyRunning is returned when Start is called while a run is already active.
	ErrAlreadyRunning = errors.New("push service already running")
)
