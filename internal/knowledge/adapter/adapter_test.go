package adapter

import (
	"strings"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/runtime/memory/distillation"
)

func TestFromMemory(t *testing.T) {
	m := &distillation.Memory{
		ID:         "mem_abc",
		Type:       distillation.MemoryKnowledge,
		Content:    "Key insight about caching strategies",
		Importance: 85,
		CreatedAt:  time.Now(),
	}

	obj := NewMemoryAdapter(0).FromMemory(m, "default")
	if obj == nil {
		t.Fatal("expected non-nil KnowledgeObject")
	}

	expectedID := "mem_mem_abc"
	if obj.ID != expectedID {
		t.Errorf("expected ID '%s', got '%s'", expectedID, obj.ID)
	}
	if obj.Type != knowledge.ObjectMemory {
		t.Errorf("expected ObjectMemory, got %s", obj.Type)
	}
	if obj.Confidence != 0.85 {
		t.Errorf("expected confidence 0.85, got %f", obj.Confidence)
	}
	if obj.Summary != "Key insight about caching strategies" {
		t.Errorf("unexpected summary: %s", obj.Summary)
	}
}

func TestFromMemoryNil(t *testing.T) {
	obj := NewMemoryAdapter(0).FromMemory(nil, "test")
	if obj != nil {
		t.Error("expected nil for nil input")
	}
}

func TestFromMemoryProfile(t *testing.T) {
	m := &distillation.Memory{
		ID:      "user_001",
		Type:    distillation.MemoryProfile,
		Content: "User prefers Python over Go for data processing",
	}

	obj := NewMemoryAdapter(0).FromMemory(m, "users")
	if obj.Type != knowledge.ObjectUser {
		t.Errorf("expected ObjectUser for profile memory, got %s", obj.Type)
	}
}

func TestFromMemoryTruncation(t *testing.T) {
	longContent := strings.Repeat("This is a very long memory content that should be truncated. ", 10)

	m := &distillation.Memory{
		ID:      "long",
		Type:    distillation.MemoryKnowledge,
		Content: longContent,
	}

	// Default adapter uses DefaultMaxMemoryContentLen (200).
	obj := NewMemoryAdapter(0).FromMemory(m, "test")
	if len(obj.Summary) > DefaultMaxMemoryContentLen+10 { // +10 for the "..." suffix
		t.Errorf("expected truncated summary near %d chars, got %d chars",
			DefaultMaxMemoryContentLen, len(obj.Summary))
	}
	if !strings.HasSuffix(obj.Summary, "...") {
		t.Errorf("expected truncated summary to end with '...', got %q", obj.Summary)
	}
}

// TestFromMemoryTruncation_RespectsConfiguredLength verifies that the
// truncation length is driven by the adapter's configured maxContentLen,
// not the hardcoded 200. This is the regression test for R16.
func TestFromMemoryTruncation_RespectsConfiguredLength(t *testing.T) {
	const configuredLen = 50
	longContent := strings.Repeat("abcdefghij", 20) // 200 chars, well above configuredLen

	m := &distillation.Memory{
		ID:      "cfg",
		Type:    distillation.MemoryKnowledge,
		Content: longContent,
	}

	obj := NewMemoryAdapter(configuredLen).FromMemory(m, "test")
	if got := len(obj.Summary); got > configuredLen+10 { // +10 for "..."
		t.Errorf("expected summary <= %d chars (configured %d + suffix), got %d",
			configuredLen+10, configuredLen, got)
	}
	if !strings.HasPrefix(obj.Summary, longContent[:configuredLen]) {
		t.Errorf("expected summary to start with first %d chars of content", configuredLen)
	}
	if !strings.HasSuffix(obj.Summary, "...") {
		t.Errorf("expected truncated summary to end with '...', got %q", obj.Summary)
	}
}

// TestFromMemoryTruncation_ConfiguredLongerThanDefault verifies that a
// configured length larger than the default is honored, so the adapter
// does not silently cap at 200.
func TestFromMemoryTruncation_ConfiguredLongerThanDefault(t *testing.T) {
	const configuredLen = 400
	longContent := strings.Repeat("x", 350) // > DefaultMaxMemoryContentLen, < configuredLen

	m := &distillation.Memory{
		ID:      "cfg-long",
		Type:    distillation.MemoryKnowledge,
		Content: longContent,
	}

	obj := NewMemoryAdapter(configuredLen).FromMemory(m, "test")
	// Content is shorter than the configured cap, so it should NOT be truncated.
	if obj.Summary != longContent {
		t.Errorf("expected no truncation when content (%d) <= configured len (%d); got %q",
			len(longContent), configuredLen, obj.Summary)
	}
}

func TestFromMemories(t *testing.T) {
	memories := []*distillation.Memory{
		{ID: "m1", Type: distillation.MemoryKnowledge, Content: "first"},
		{ID: "m2", Type: distillation.MemoryPreference, Content: "second"},
		{ID: "m3", Type: distillation.MemoryInteraction, Content: "third"},
	}

	objects := NewMemoryAdapter(0).FromMemories(memories, "ns")
	if len(objects) != 3 {
		t.Errorf("expected 3 objects, got %d", len(objects))
	}
}

func TestFromMemoriesWithNil(t *testing.T) {
	memories := []*distillation.Memory{
		{ID: "m1", Type: distillation.MemoryKnowledge, Content: "valid"},
		nil,
		{ID: "m2", Type: distillation.MemoryKnowledge, Content: "valid"},
	}

	objects := NewMemoryAdapter(0).FromMemories(memories, "ns")
	if len(objects) != 2 {
		t.Errorf("expected 2 objects (nil skipped), got %d", len(objects))
	}
}

// TestNewMemoryAdapter_Default verifies the default content cap.
func TestNewMemoryAdapter_Default(t *testing.T) {
	a := NewMemoryAdapter(0)
	if a.MaxContentLen() != DefaultMaxMemoryContentLen {
		t.Errorf("expected default %d, got %d", DefaultMaxMemoryContentLen, a.MaxContentLen())
	}
}

// TestNewMemoryAdapter_NegativeDefaults verifies that a negative or zero
// length falls back to the default.
func TestNewMemoryAdapter_NegativeDefaults(t *testing.T) {
	a := NewMemoryAdapter(-1)
	if a.MaxContentLen() != DefaultMaxMemoryContentLen {
		t.Errorf("expected default for negative input, got %d", a.MaxContentLen())
	}
}

// TestNewMemoryAdapter_Custom verifies a custom configured length is honored.
func TestNewMemoryAdapter_Custom(t *testing.T) {
	a := NewMemoryAdapter(123)
	if a.MaxContentLen() != 123 {
		t.Errorf("expected 123, got %d", a.MaxContentLen())
	}
}
