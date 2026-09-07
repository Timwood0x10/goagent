// Package embedding provides canonical embedding types and a unified pipeline
// for generating vector embeddings across memory distillation, storage, and retrieval.
package embedding

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// EmbeddingKind identifies the semantic category of the content being embedded.
type EmbeddingKind string

const (
	KindMemoryExperience  EmbeddingKind = "memory_experience"
	KindMemoryQuery       EmbeddingKind = "memory_query"
	KindToolResultSummary EmbeddingKind = "tool_result_summary"
	KindTaskResult        EmbeddingKind = "task_result"
)

// MemoryExperienceInput carries the fields needed to build a memory experience spec.
type MemoryExperienceInput struct {
	MemoryType string
	Problem    string
	Solution   string
}

// EmbeddingSpec represents the exact embedding contract for an object.
// It captures every dimension that affects the output vector so that
// the same object always produces the same spec hash.
type EmbeddingSpec struct {
	Kind    EmbeddingKind
	Text    string
	Prefix  string
	Model   string
	Version int
	Dim     int
	Hash    string
}

// computeHash returns a deterministic hash from the spec fields that affect
// the embedding output: kind, prefix, text, model, version, and dim.
func computeHash(kind EmbeddingKind, prefix, text, model string, version, dim int) string {
	input := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%d\x00%d", kind, prefix, text, model, version, dim)
	h := sha256.Sum256([]byte(input))
	return fmt.Sprintf("%x", h[:16])
}

// BuildMemoryQuerySpec creates an EmbeddingSpec for a memory search query.
// The canonical text is the raw query string. The prefix is "query:".
func BuildMemoryQuerySpec(query, model string, version, dim int) EmbeddingSpec {
	return EmbeddingSpec{
		Kind:    KindMemoryQuery,
		Text:    query,
		Prefix:  "query:",
		Model:   model,
		Version: version,
		Dim:     dim,
		Hash:    computeHash(KindMemoryQuery, "query:", query, model, version, dim),
	}
}

// BuildMemoryExperienceSpec creates an EmbeddingSpec for a memory experience.
// The canonical text is a stable field-driven format, NOT a map dump.
func BuildMemoryExperienceSpec(memoryType, problem, solution, model string, version, dim int) EmbeddingSpec {
	var b strings.Builder
	b.WriteString("MemoryType: ")
	b.WriteString(memoryType)
	b.WriteString("\nProblem: ")
	b.WriteString(problem)
	b.WriteString("\nSolution: ")
	b.WriteString(solution)

	text := b.String()
	prefix := "memory:"

	return EmbeddingSpec{
		Kind:    KindMemoryExperience,
		Text:    text,
		Prefix:  prefix,
		Model:   model,
		Version: version,
		Dim:     dim,
		Hash:    computeHash(KindMemoryExperience, prefix, text, model, version, dim),
	}
}
