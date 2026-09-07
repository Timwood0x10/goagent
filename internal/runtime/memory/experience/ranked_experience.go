// Package experience provides experience ranking data structures.
package experience

import "time"

// RankedExperience represents an experience with its ranking score.
// This is the output of the experience ranking process.
type RankedExperience struct {
	// Experience is the experience being ranked.
	Experience *Experience `json:"experience"`

	// FinalScore is the final ranking score.
	// This score combines semantic similarity, usage boost, and recency boost.
	FinalScore float64 `json:"final_score"`

	// SemanticScore is the semantic similarity score from vector search.
	// This represents how well the experience matches the query semantically.
	SemanticScore float64 `json:"semantic_score"`

	// UsageBoost is the boost from experience usage count.
	// This is calculated as min(log(1 + usage_count) * 0.05, 0.2).
	UsageBoost float64 `json:"usage_boost"`

	// RecencyBoost is the boost from experience recency.
	// This is calculated as exp(-age_days / 30) * 0.05.
	RecencyBoost float64 `json:"recency_boost"`

	// ConflictChecked indicates whether this experience has been checked for conflicts.
	ConflictChecked bool `json:"conflict_checked"`

	// ConflictResolved indicates whether this experience resolved a conflict.
	// If true, this experience was selected as the best in its conflict group.
	ConflictResolved bool `json:"conflict_resolved"`
}

// Experience represents a distilled task experience.
// This is the core data structure for experience storage and retrieval.
type Experience struct {
	// ID is the unique identifier of the experience.
	ID string `json:"id"`

	// TenantID is the tenant identifier for multi-tenancy isolation.
	TenantID string `json:"tenant_id"`

	// Type is the type of experience.
	// Valid values: "success" or "failure".
	Type string `json:"type"`

	// Problem is the abstract problem statement.
	// This is the target for embedding generation.
	Problem string `json:"problem"`

	// Solution is the concise solution approach.
	Solution string `json:"solution"`

	// Constraints are important constraints or context for the solution.
	Constraints string `json:"constraints"`

	// Embedding is the vector embedding of the problem.
	// This is used for semantic similarity search.
	Embedding []float64 `json:"embedding"`

	// EmbeddingModel is the name of the embedding model used.
	EmbeddingModel string `json:"embedding_model"`

	// EmbeddingVersion is the version of the embedding model.
	EmbeddingVersion int `json:"embedding_version"`

	// Score is the overall score of the experience.
	Score float64 `json:"score"`

	// Success indicates whether the original task was successful.
	Success bool `json:"success"`

	// AgentID is the identifier of the agent that generated this experience.
	AgentID string `json:"agent_id"`

	// UsageCount is the number of times this experience was successfully used.
	// This is used as a reinforcement signal.
	UsageCount int `json:"usage_count"`

	// DecayAt is the time when this experience should be considered expired.
	// If zero, the experience never expires.
	DecayAt time.Time `json:"decay_at"`

	// CreatedAt is the time when this experience was created.
	CreatedAt time.Time `json:"created_at"`
}

// ExperienceType constants.
const (
	// ExperienceTypeSuccess represents a successful experience.
	ExperienceTypeSuccess = "success"

	// ExperienceTypeFailure represents a failed experience.
	ExperienceTypeFailure = "failure"
)

// GetUsageCount returns the usage count of the experience.
// Returns the usage count.
func (e *Experience) GetUsageCount() int {
	return e.UsageCount
}
