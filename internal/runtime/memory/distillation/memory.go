// Package distillation provides memory distillation functionality for
// agent experience extraction.
//
// Public DTOs and the ExperienceRepository interface live in the
// api/experience package. This internal file re-exports them as type
// aliases so existing internal call sites continue to compile without
// churn. New internal code should prefer the api/experience types
// directly.
package distillation

import (
	"github.com/Timwood0x10/ares/api/experience"
)

// Public type aliases. These keep the internal package's existing API
// surface stable while routing all canonical definitions through
// api/experience.
type (
	// MemoryType is the classified memory type.
	MemoryType = experience.MemoryType

	// Memory is a single distilled knowledge fragment.
	Memory = experience.Memory

	// ExtractionMethod indicates how an experience was extracted.
	ExtractionMethod = experience.ExtractionMethod

	// Experience is a problem-solution pair extracted from conversation.
	Experience = experience.Experience

	// ResolutionStrategy defines how to resolve memory conflicts.
	ResolutionStrategy = experience.ResolutionStrategy

	// StoredExperience is the write DTO for ExperienceStore.
	StoredExperience = experience.StoredExperience

	// ExperienceRepository is the storage-agnostic contract for
	// experience persistence. External modules implement this with any
	// vector database (PostgreSQL pgvector, SQLite-vec, Weaviate, Qdrant,
	// Milvus, etc.).
	ExperienceRepository = experience.ExperienceRepository

	// ExperienceStore writes experiences to an external experience
	// system. The distiller uses it when configured via
	// WithExperienceStore.
	ExperienceStore = experience.ExperienceStore
)

// Re-exported constants for backwards-compatible internal access.
const (
	// MemoryKnowledge represents distilled factual knowledge.
	MemoryKnowledge = experience.MemoryKnowledge
	// MemoryPreference represents distilled user preferences.
	MemoryPreference = experience.MemoryPreference
	// MemoryInteraction represents distilled interaction patterns.
	MemoryInteraction = experience.MemoryInteraction
	// MemoryProfile represents distilled user profile information.
	MemoryProfile = experience.MemoryProfile

	// ExtractionDirect indicates a direct user-assistant pair extraction.
	ExtractionDirect = experience.ExtractionDirect
	// ExtractionCrossTurn indicates a multi-turn conversation extraction.
	ExtractionCrossTurn = experience.ExtractionCrossTurn

	// ReplaceOld replaces the old memory with the new one.
	ReplaceOld = experience.ReplaceOld
	// KeepOld discards the new memory and retains the existing one.
	KeepOld = experience.KeepOld
	// KeepBoth keeps both versions (used for competing solutions).
	KeepBoth = experience.KeepBoth
	// Merge merges the memories (reserved for future use).
	Merge = experience.Merge
)

// Compile-time guard: ensure the aliases satisfy the public interface
// contracts expected by external callers.
var (
	_ ExperienceRepository = (ExperienceRepository)(nil)
	_ ExperienceStore      = (ExperienceStore)(nil)
)
