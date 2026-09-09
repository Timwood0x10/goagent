// Package llmexp provides the canonical experience-storage and
// memory-distillation DTOs.
//
// The types in this package are storage-agnostic. The ExperienceRepository
// interface (see repository.go) lets modules implement experience
// persistence with any vector database (PostgreSQL, SQLite-vec, pgvector,
// Weaviate, Qdrant, etc.) without coupling to ARES's internal storage layer.
//
// This package holds the canonical definitions previously exposed as
// api/experience (M5 migration); api/experience is now a pure forwarding
// layer.
package llmexp

import (
	"context"
	"time"
)

// MemoryType defines the four types of distilled memory.
type MemoryType string

const (
	// MemoryKnowledge represents distilled factual knowledge.
	MemoryKnowledge MemoryType = "knowledge"
	// MemoryPreference represents distilled user preferences.
	MemoryPreference MemoryType = "preference"
	// MemoryInteraction represents distilled interaction patterns.
	MemoryInteraction MemoryType = "interaction"
	// MemoryProfile represents distilled user profile information.
	MemoryProfile MemoryType = "profile"
)

// String returns the string representation of the MemoryType.
//
// The mapping is:
//
//	MemoryKnowledge   → "fact"
//	MemoryPreference  → "preference"
//	MemoryInteraction → "solution"
//	MemoryProfile     → "profile"
//
// Unknown values are returned verbatim.
func (m MemoryType) String() string {
	switch m {
	case MemoryKnowledge:
		return "fact"
	case MemoryPreference:
		return "preference"
	case MemoryInteraction:
		return "solution"
	case MemoryProfile:
		return "profile"
	default:
		return string(m)
	}
}

// ExtractionMethod defines how an experience was extracted from conversation.
type ExtractionMethod string

const (
	// ExtractionDirect indicates a direct user-assistant pair extraction.
	ExtractionDirect ExtractionMethod = "direct"
	// ExtractionCrossTurn indicates a multi-turn conversation extraction.
	ExtractionCrossTurn ExtractionMethod = "cross-turn"
)

// ResolutionStrategy defines how to resolve conflicts between memories.
type ResolutionStrategy string

const (
	// ReplaceOld replaces the old memory with the new one.
	ReplaceOld ResolutionStrategy = "replace"
	// KeepOld discards the new memory and retains the existing one. Used when
	// the stored memory carries strictly higher confidence than the incoming
	// one, so that a low-confidence duplicate cannot overwrite a high-confidence
	// fact.
	KeepOld ResolutionStrategy = "keep_old"
	// KeepBoth keeps both versions (used for competing solutions).
	KeepBoth ResolutionStrategy = "version"
	// Merge merges the memories (reserved for future use).
	Merge ResolutionStrategy = "merge"
)

// Experience represents a problem-solution pair extracted from a conversation.
//
// This is the core DTO exchanged between the distillation pipeline and the
// experience repository. Modules may construct instances directly
// when implementing a custom ExperienceRepository.
type Experience struct {
	// ID is the unique identifier for the experience.
	ID string
	// Type is the classified memory type. It is persisted to storage so the
	// distiller's solution cap and cross-session dedup can query by type.
	Type MemoryType
	// Problem is the abstract problem statement.
	Problem string
	// Solution is the concise solution approach.
	Solution string
	// Confidence is the importance score in the range [0, 1].
	Confidence float64
	// ExtractionMethod indicates how the experience was extracted.
	ExtractionMethod ExtractionMethod
	// Vector is the optional embedding vector for similarity search.
	Vector []float64
}

// StoredExperience represents an experience entry to be persisted via
// ExperienceStore. It is a write-oriented DTO used when syncing distilled
// memories to an external experience system.
type StoredExperience struct {
	// TenantID is the tenant identifier for multi-tenancy isolation.
	TenantID string
	// Type is the experience type (e.g., "solution", "heuristic", "strategy", "failure", "general").
	Type string
	// Problem is the abstract problem statement.
	Problem string
	// Solution is the concise solution approach.
	Solution string
	// Score is the importance score (0-1).
	Score float64
	// Source indicates where this experience originated from.
	Source string
	// Metadata holds additional structured data.
	Metadata map[string]interface{}
}

// Memory represents a single distilled knowledge fragment. It is the output
// DTO of the distiller's DistillConversation pipeline.
type Memory struct {
	// ID is the unique identifier for the memory.
	ID string
	// Type is the classified memory type.
	Type MemoryType
	// Content is the formatted memory content.
	Content string
	// Importance is the importance score in the range [0, 1].
	Importance float64
	// Source indicates the originating conversation.
	Source string
	// Vector is the embedding vector for similarity search.
	Vector []float64
	// TTL is the time-to-live for the memory.
	TTL time.Duration
	// CreatedAt is the memory creation timestamp.
	CreatedAt time.Time
	// ExpiresAt is the memory expiration timestamp.
	ExpiresAt time.Time
	// Metadata holds additional structured data.
	Metadata map[string]interface{}
}

// ExperienceStore defines the interface for writing experiences to an
// external experience system. The distiller uses it to sync distilled
// memories when configured via WithExperienceStore.
type ExperienceStore interface {
	// Create persists a new experience entry.
	Create(ctx context.Context, exp *StoredExperience) error
}
