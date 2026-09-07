// Package context provides chat-loop context assembly utilities: message
// cleaning, RAG retrieval, session/user context, and memory retrieval.
//
// This file defines the canonical ContextRetriever interface and the
// ContextSnippet DTO that all retriever implementations must produce. The
// MemoryRetriever (see memory_retriever.go) is the in-tree implementation
// that surfaces distilled experiences from the memory distillation
// subsystem back into the LLM prompt. External integrators (e.g. the
// knowledge package's AKG adapter) implement the same interface against
// their own backends.
package context

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
)

// ContextSnippet is a retrieved piece of context for prompt augmentation.
//
// It is the unit of context that retrievers hand back to the chat-loop
// context builder. The builder concatenates snippets (after dedup) into
// the system / user prompt so the LLM can reason over prior distilled
// knowledge and experiences.
type ContextSnippet struct {
	// Source identifies where the snippet came from. Common values are
	// "memory", "experience", and "knowledge". Retrievers may introduce
	// additional sources as long as they remain stable for downstream
	// filtering.
	Source string
	// Content is the rendered text to inject into the prompt. It MUST be
	// non-empty for any snippet that survives filtering.
	Content string
	// Score is the relevance / confidence score in the range [0, 1].
	// Higher is better. Snippets below the retriever's MinScore are
	// discarded before returning.
	Score float64
	// Metadata carries optional structured fields (e.g. experience ID,
	// extraction method) for downstream consumers. May be nil.
	Metadata map[string]any
}

// ContextRetriever retrieves relevant context snippets for a given input.
//
// Implementations MUST be safe for concurrent use. Callers may invoke
// Retrieve from multiple goroutines simultaneously against the same
// instance.
//
// Args:
//
//	ctx   - operation context, honoured for cancellation and timeout.
//	input - the query / prompt text to retrieve context for. An empty
//	        input MUST yield an empty slice with a nil error.
//	topK  - maximum number of snippets to return. When topK <= 0 the
//	        implementation applies its own default.
//
// Returns:
//
//	[]ContextSnippet - matching snippets, sorted by Score descending.
//	error            - any error encountered; never returned together
//	                   with a non-nil slice.
type ContextRetriever interface {
	Retrieve(ctx context.Context, input string, topK int) ([]ContextSnippet, error)
}

// snippetKey computes the deduplication key for a ContextSnippet.
//
// The key is a function of Source and Content so that two snippets
// claiming the same provenance and body are considered duplicates
// regardless of their Score or Metadata. The key is hashed to keep
// the dedup map bounded for large inputs.
func snippetKey(s ContextSnippet) string {
	h := sha256.Sum256([]byte(s.Source + "\x00" + s.Content))
	return hex.EncodeToString(h[:])
}

// DedupSnippets deduplicates a slice of ContextSnippet by Source+Content,
// keeping the highest-score copy for each distinct key.
//
// When two snippets share the same Source and Content but differ in
// Score, the one with the higher Score wins. Ties are broken in favor
// of the earlier entry (stable left-to-right). The returned slice is
// not re-sorted; callers that need score-descending order should sort
// the result explicitly.
//
// Args:
//
//	snippets - input slice. May be nil or empty. The input is not
//	           mutated.
//
// Returns:
//
//	[]ContextSnippet - deduplicated slice preserving first-occurrence
//	                   order. Returns nil for nil input.
func DedupSnippets(snippets []ContextSnippet) []ContextSnippet {
	if len(snippets) == 0 {
		return nil
	}

	best := make(map[string]ContextSnippet, len(snippets))
	order := make([]string, 0, len(snippets))

	for _, s := range snippets {
		key := snippetKey(s)
		if existing, ok := best[key]; ok {
			if s.Score > existing.Score {
				best[key] = s
			}
			continue
		}
		best[key] = s
		order = append(order, key)
	}

	out := make([]ContextSnippet, 0, len(order))
	for _, key := range order {
		out = append(out, best[key])
	}
	return out
}

// SortSnippetsByScore sorts snippets in-place by Score descending.
//
// Snippets with equal scores keep their original relative order (stable
// sort). This helper is exposed so callers can re-sort after merging
// snippets from multiple retrievers.
func SortSnippetsByScore(snippets []ContextSnippet) {
	sort.SliceStable(snippets, func(i, j int) bool {
		return snippets[i].Score > snippets[j].Score
	})
}
