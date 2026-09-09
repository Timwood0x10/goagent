package adapter

import (
	"fmt"
	"time"

	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/runtime/memory/distillation"
)

// DefaultMaxMemoryContentLen is the default cap on Memory.Content length when
// copying it into KnowledgeObject.Summary. It bounds LLM token cost for the
// downstream Summarizer without discarding content shorter than the cap.
// The historical value (200) is retained as the default to preserve
// backward compatibility; override via NewMemoryAdapter.
const DefaultMaxMemoryContentLen = 200

// MemoryAdapter converts distillation.Memory objects into KnowledgeObjects.
// The zero value is NOT valid; use NewMemoryAdapter.
//
// The adapter caps how much of Memory.Content is copied into
// KnowledgeObject.Summary before the pipeline Summarizer runs. This is
// distinct from the Summarizer's own MaxSummaryLen: the cap here only
// truncates the raw input feeding the Summarizer, it does not determine
// the final summary length.
type MemoryAdapter struct {
	// maxContentLen is the maximum number of characters of Memory.Content
	// copied into KnowledgeObject.Summary. Values <= 0 fall back to
	// DefaultMaxMemoryContentLen at conversion time.
	maxContentLen int
}

// NewMemoryAdapter creates a MemoryAdapter with the given max content length.
// If maxContentLen <= 0, DefaultMaxMemoryContentLen is used.
func NewMemoryAdapter(maxContentLen int) *MemoryAdapter {
	if maxContentLen <= 0 {
		maxContentLen = DefaultMaxMemoryContentLen
	}
	return &MemoryAdapter{maxContentLen: maxContentLen}
}

// MaxContentLen returns the configured content cap. Exposed for tests and
// diagnostics so callers can verify the adapter was wired with the
// intended length rather than the default.
func (a *MemoryAdapter) MaxContentLen() int {
	if a == nil || a.maxContentLen <= 0 {
		return DefaultMaxMemoryContentLen
	}
	return a.maxContentLen
}

// FromMemory converts a distillation.Memory into a KnowledgeObject.
// This is the bridge between the existing Memory Distillation pipeline
// and the AKF KnowledgeObject model.
//
// Memory.Content is truncated to a.maxContentLen characters (plus a "..."
// sentinel) before being placed in KnowledgeObject.Summary so the value
// respects the adapter's configured cap instead of a hardcoded constant.
func (a *MemoryAdapter) FromMemory(m *distillation.Memory, ns string) *knowledge.KnowledgeObject {
	if m == nil {
		return nil
	}

	objType := memoryTypeToObjectType(m.Type)
	summary := m.Content
	if max := a.MaxContentLen(); len(summary) > max {
		// Truncate by runes, not bytes, to avoid splitting multi-byte
		// UTF-8 characters (e.g. CJK).
		runes := []rune(summary)
		if len(runes) > max {
			summary = string(runes[:max]) + "..."
		}
	}

	return &knowledge.KnowledgeObject{
		ID:         fmt.Sprintf("mem_%s", m.ID),
		Type:       objType,
		Namespace:  ns,
		Summary:    summary,
		Confidence: clampConfidence(float64(m.Importance) / 100.0),
		CreatedAt:  m.CreatedAt,
		UpdatedAt:  time.Now(),
	}
}

// FromMemories converts a slice of distillation.Memory into KnowledgeObjects.
func (a *MemoryAdapter) FromMemories(memories []*distillation.Memory, ns string) []*knowledge.KnowledgeObject {
	objects := make([]*knowledge.KnowledgeObject, 0, len(memories))
	for _, m := range memories {
		if obj := a.FromMemory(m, ns); obj != nil {
			objects = append(objects, obj)
		}
	}
	return objects
}

func memoryTypeToObjectType(mt distillation.MemoryType) knowledge.ObjectType {
	switch mt {
	case distillation.MemoryKnowledge:
		return knowledge.ObjectMemory
	case distillation.MemoryPreference:
		return knowledge.ObjectMemory
	case distillation.MemoryInteraction:
		return knowledge.ObjectMemory
	case distillation.MemoryProfile:
		return knowledge.ObjectUser
	default:
		return knowledge.ObjectMemory
	}
}

// clampConfidence clamps a confidence score to the [0, 1] range.
func clampConfidence(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
