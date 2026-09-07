// Package report provides human-readable evolution report generation
// from distilled knowledge items. It turns the output of the distillation
// pipeline (problem/solution memories, conflict resolutions, evidence)
// into structured reports for operators and downstream consumers.
package report

import (
	"context"
	"errors"
	"time"
)

// ConfidenceBucket groups knowledge items by their confidence range.
// Items are bucketed into Low/Medium/High bands to give a quick overview
// of evidence quality distribution.
type ConfidenceBucket string

const (
	// ConfidenceLow groups items with confidence in [0.0, 0.4).
	ConfidenceLow ConfidenceBucket = "low"
	// ConfidenceMedium groups items with confidence in [0.4, 0.7).
	ConfidenceMedium ConfidenceBucket = "medium"
	// ConfidenceHigh groups items with confidence in [0.7, 1.0].
	ConfidenceHigh ConfidenceBucket = "high"
)

// KnowledgeItem is the canonical distilled knowledge representation used
// by the report package. It abstracts the underlying Memory / StoredExperience
// types so the report generator depends only on this interface-shaped input.
type KnowledgeItem struct {
	// ID uniquely identifies the knowledge item.
	ID string `json:"id"`
	// Category is the knowledge category (e.g., "fact", "solution", "preference", "rule").
	Category string `json:"category"`
	// Problem is the abstract problem statement.
	Problem string `json:"problem"`
	// Solution is the concise solution or insight.
	Solution string `json:"solution"`
	// Score is the importance / confidence score in [0, 1].
	Score float64 `json:"score"`
	// Source identifies where this knowledge originated (e.g., conversation ID).
	Source string `json:"source"`
	// StrategyID, when non-empty, links this knowledge to a specific strategy.
	StrategyID string `json:"strategy_id,omitempty"`
	// TaskType, when non-empty, links this knowledge to a task category.
	TaskType string `json:"task_type,omitempty"`
	// PromptTemplate, when non-empty, links this knowledge to a prompt template.
	PromptTemplate string `json:"prompt_template,omitempty"`
	// EvidenceKey, when non-empty, is the stable evidence key (phenotype hash).
	EvidenceKey string `json:"evidence_key,omitempty"`
	// ConflictResolved indicates whether this item was the result of a conflict resolution.
	ConflictResolved bool `json:"conflict_resolved,omitempty"`
	// ResolutionStrategy records how a conflict was resolved (e.g., "replace", "version").
	ResolutionStrategy string `json:"resolution_strategy,omitempty"`
	// CreatedAt is when this knowledge item was first distilled.
	CreatedAt time.Time `json:"created_at"`
	// Metadata holds additional structured data.
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// SummarySection summarizes the distilled knowledge corpus.
type SummarySection struct {
	// TotalItems is the total number of knowledge items considered.
	TotalItems int `json:"total_items"`
	// ByCategory counts items per category.
	ByCategory map[string]int `json:"by_category"`
	// ByConfidence counts items per confidence bucket.
	ByConfidence map[ConfidenceBucket]int `json:"by_confidence"`
	// AverageScore is the mean score across all items (0 if no items).
	AverageScore float64 `json:"average_score"`
	// MaxScore is the highest score observed (0 if no items).
	MaxScore float64 `json:"max_score"`
	// MinScore is the lowest score observed (0 if no items).
	MinScore float64 `json:"min_score"`
}

// TopKnowledgeSection lists the highest-scoring knowledge items.
type TopKnowledgeSection struct {
	// Items is the sorted list of top knowledge items (highest score first).
	Items []KnowledgeItem `json:"items"`
	// Limit is the configured maximum number of items shown.
	Limit int `json:"limit"`
}

// ConflictResolutionSection summarizes conflict resolutions applied
// during distillation.
type ConflictResolutionSection struct {
	// TotalConflicts is the total number of conflicts that were resolved.
	TotalConflicts int `json:"total_conflicts"`
	// ByStrategy counts resolutions per strategy (e.g., "replace": 3, "version": 1).
	ByStrategy map[string]int `json:"by_strategy"`
	// RecentResolutions lists the most recent resolved items.
	RecentResolutions []KnowledgeItem `json:"recent_resolutions"`
}

// EvolutionTrend captures the change in a knowledge category over time.
type EvolutionTrend struct {
	// Category is the knowledge category this trend applies to.
	Category string `json:"category"`
	// Count is the number of items in this category.
	Count int `json:"count"`
	// AverageScore is the mean score of items in this category.
	AverageScore float64 `json:"average_score"`
	// LatestCreatedAt is the most recent creation timestamp in this category.
	LatestCreatedAt time.Time `json:"latest_created_at"`
}

// TrendSection summarizes evolution trends per category.
type TrendSection struct {
	// Trends is the list of per-category trends.
	Trends []EvolutionTrend `json:"trends"`
	// GeneratedAt is when the trends were computed.
	GeneratedAt time.Time `json:"generated_at"`
}

// Recommendation is an actionable suggestion derived from the knowledge corpus.
type Recommendation struct {
	// ID uniquely identifies this recommendation.
	ID string `json:"id"`
	// Title is a short human-readable summary.
	Title string `json:"title"`
	// Rationale explains why the recommendation was made.
	Rationale string `json:"rationale"`
	// Priority is one of "low", "medium", "high".
	Priority string `json:"priority"`
	// TargetStrategyID, when set, identifies the strategy this applies to.
	TargetStrategyID string `json:"target_strategy_id,omitempty"`
	// TargetTaskType, when set, identifies the task type this applies to.
	TargetTaskType string `json:"target_task_type,omitempty"`
	// RelatedItemIDs lists the knowledge item IDs that justify this recommendation.
	RelatedItemIDs []string `json:"related_item_ids,omitempty"`
}

// RecommendationSection groups all actionable recommendations.
type RecommendationSection struct {
	// Recommendations is the list of recommendations, sorted by priority.
	Recommendations []Recommendation `json:"recommendations"`
	// Total is the total number of recommendations.
	Total int `json:"total"`
}

// Report is the structured form of an evolution report.
// It is suitable for programmatic consumption and can be formatted
// into human-readable text via Format().
type Report struct {
	// GeneratedAt is when the report was produced.
	GeneratedAt time.Time `json:"generated_at"`
	// TenantID is the tenant scope this report covers (may be empty for global).
	TenantID string `json:"tenant_id"`
	// Summary contains aggregate statistics.
	Summary SummarySection `json:"summary"`
	// TopKnowledge lists the highest-scoring items.
	TopKnowledge TopKnowledgeSection `json:"top_knowledge"`
	// Conflicts summarizes applied conflict resolutions.
	Conflicts ConflictResolutionSection `json:"conflicts"`
	// Trends summarizes per-category evolution trends.
	Trends TrendSection `json:"trends"`
	// Recommendations lists actionable recommendations.
	Recommendations RecommendationSection `json:"recommendations"`
}

// KnowledgeSource provides distilled knowledge items to the report generator.
// Implementations may wrap an ExperienceStore, distillation Memory slice,
// or any other source of distilled knowledge.
type KnowledgeSource interface {
	// ListKnowledge returns all distilled knowledge items for the given tenant.
	// Returns an empty slice (not nil) when no items exist.
	ListKnowledge(ctx context.Context, tenantID string) ([]KnowledgeItem, error)
}

// ReportConfig holds configuration for the report generator.
type ReportConfig struct {
	// TopN is the maximum number of top knowledge items to include.
	// Default: 10.
	TopN int
	// MinScore filters out items with score below this threshold (0 disables).
	MinScore float64
	// IncludeConflicts controls whether the conflicts section is populated.
	IncludeConflicts bool
	// IncludeTrends controls whether the trends section is populated.
	IncludeTrends bool
	// IncludeRecommendations controls whether recommendations are generated.
	IncludeRecommendations bool
}

// DefaultReportConfig returns a ReportConfig with sensible defaults.
func DefaultReportConfig() *ReportConfig {
	return &ReportConfig{
		TopN:                   10,
		MinScore:               0.0,
		IncludeConflicts:       true,
		IncludeTrends:          true,
		IncludeRecommendations: true,
	}
}

// Sentinel errors for the report package.
var (
	// ErrInvalidConfig is returned when the report configuration is invalid.
	ErrInvalidConfig = errors.New("invalid report configuration")
	// ErrEmptySource is returned when the knowledge source returns no items
	// and the caller requested a non-empty report.
	ErrEmptySource = errors.New("knowledge source is empty")
)
