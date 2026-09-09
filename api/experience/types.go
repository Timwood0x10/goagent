// Package experience is the DEPRECATED public alias of internal/llmexp (M5).
// New code MUST import internal/llmexp; this package exists only for
// external consumers and is scheduled for removal.
package experience

import "github.com/Timwood0x10/ares/internal/llmexp"

type (
	// MemoryType defines the four types of distilled memory.
	MemoryType = llmexp.MemoryType
	// ExtractionMethod defines how an experience was extracted from
	// conversation.
	ExtractionMethod = llmexp.ExtractionMethod
	// ResolutionStrategy defines how to resolve conflicts between memories.
	ResolutionStrategy = llmexp.ResolutionStrategy
	// Experience represents a problem-solution pair extracted from a
	// conversation.
	Experience = llmexp.Experience
	// StoredExperience represents an experience entry to be persisted via
	// ExperienceStore.
	StoredExperience = llmexp.StoredExperience
	// Memory represents a single distilled knowledge fragment.
	Memory = llmexp.Memory
	// ExperienceStore defines the interface for writing experiences to an
	// external experience system.
	ExperienceStore = llmexp.ExperienceStore
)

const (
	// MemoryKnowledge represents distilled factual knowledge.
	MemoryKnowledge = llmexp.MemoryKnowledge
	// MemoryPreference represents distilled user preferences.
	MemoryPreference = llmexp.MemoryPreference
	// MemoryInteraction represents distilled interaction patterns.
	MemoryInteraction = llmexp.MemoryInteraction
	// MemoryProfile represents distilled user profile information.
	MemoryProfile = llmexp.MemoryProfile

	// ExtractionDirect indicates a direct user-assistant pair extraction.
	ExtractionDirect = llmexp.ExtractionDirect
	// ExtractionCrossTurn indicates a multi-turn conversation extraction.
	ExtractionCrossTurn = llmexp.ExtractionCrossTurn

	// ReplaceOld replaces the old memory with the new one.
	ReplaceOld = llmexp.ReplaceOld
	// KeepOld discards the new memory and retains the existing one.
	KeepOld = llmexp.KeepOld
	// KeepBoth keeps both versions (used for competing solutions).
	KeepBoth = llmexp.KeepBoth
	// Merge merges the memories (reserved for future use).
	Merge = llmexp.Merge
)
