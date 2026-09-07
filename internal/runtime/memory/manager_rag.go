package memory

import (
	"context"

	memctx "github.com/Timwood0x10/ares/internal/runtime/memory/context"
)

// retrieveContextString runs RAG retrieval for BuildContext and returns the
// result as a formatted string ready to prepend to the context builder.
// Returns an empty string when RAG is disabled, no retrievers are configured,
// the input is empty, or retrieval yields no snippets.
//
// Retrieval failures are logged and do NOT propagate as errors — RAG is
// best-effort by design, and the chat loop must proceed even when the
// retriever backend is unavailable (code_rules §9: graceful degradation).
func (m *memoryManager) retrieveContextString(ctx context.Context, input string) string {
	snippets := m.runRetrieval(ctx, input)
	return memctx.FormatSnippetsAsContext(snippets)
}

// retrieveForPrompt runs RAG retrieval for BuildPromptMessages and returns
// the result as a slice of system Messages to prepend to the cleaned history.
// Returns nil when RAG is disabled or no snippets are retrieved.
func (m *memoryManager) retrieveForPrompt(ctx context.Context, input string) []Message {
	snippets := m.runRetrieval(ctx, input)
	msgs := memctx.SnippetsToSystemMessages(snippets)
	if len(msgs) == 0 {
		return nil
	}
	// Message is a type alias for memctx.Message, so a direct copy is safe.
	out := make([]Message, len(msgs))
	copy(out, msgs)
	return out
}

// runRetrieval is the shared retrieval path used by both retrieveContextString
// and retrieveForPrompt. It snapshots the retrievers and the RAG config under
// the config lock (MemoryPatchExecutor.Apply mutates config fields under the
// same lock), checks the EnableRAG gate, and delegates to memctx.RunRetrieval
// which applies the canonical DefaultTopK / DefaultMinScore normalization.
func (m *memoryManager) runRetrieval(ctx context.Context, input string) []memctx.ContextSnippet {
	// Snapshot config + retrievers under the lock so concurrent SetRetrievers
	// calls and config patches do not race with retrieval reads.
	m.mu.RLock()
	enableRAG := m.config.EnableRAG
	ragTopK := m.config.RAGTopK
	ragMinScore := m.config.RAGMinScore
	retrievers := make([]memctx.ContextRetriever, len(m.retrievers))
	copy(retrievers, m.retrievers)
	m.mu.RUnlock()

	if !enableRAG || input == "" {
		return nil
	}

	if len(retrievers) == 0 {
		return nil
	}

	snippets, err := memctx.RunRetrieval(ctx, retrievers, input, ragTopK, ragMinScore)
	if err != nil {
		log.Warn("RAG retrieval reported partial failures, proceeding with available snippets",
			"error", err, "snippet_count", len(snippets))
	}
	return snippets
}

// lastUserMessage returns the content of the most recent user message in the
// slice, or "" when no user message exists. Used as the RAG query when
// BuildPromptMessages is called without an explicit input parameter.
func lastUserMessage(messages []Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == memctx.RoleUser {
			return messages[i].Content
		}
	}
	return ""
}
