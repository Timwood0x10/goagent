package memory

import (
	"context"

	memctx "github.com/Timwood0x10/ares/internal/runtime/memory/context"
)

// SetRetrievers configures the RAG retrievers used by BuildContext and
// BuildPromptMessages. Pass an empty slice to disable retrieval at runtime.
// Retrieval only fires when config.EnableRAG is true AND len(retrievers) > 0.
func (m *ProductionMemoryManager) SetRetrievers(retrievers []memctx.ContextRetriever) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retrievers = retrievers
}

// retrieveContextString runs RAG retrieval for BuildContext and returns the
// result as a formatted string. Returns empty when RAG is disabled or no
// snippets are retrieved. Best-effort: failures are logged, not propagated.
func (m *ProductionMemoryManager) retrieveContextString(ctx context.Context, input string) string {
	snippets := m.runRetrieval(ctx, input)
	return memctx.FormatSnippetsAsContext(snippets)
}

// retrieveForPrompt runs RAG retrieval for BuildPromptMessages and returns
// system Messages to prepend. Returns nil when RAG is disabled or empty.
func (m *ProductionMemoryManager) retrieveForPrompt(ctx context.Context, input string) []Message {
	snippets := m.runRetrieval(ctx, input)
	msgs := memctx.SnippetsToSystemMessages(snippets)
	if len(msgs) == 0 {
		return nil
	}
	out := make([]Message, len(msgs))
	copy(out, msgs)
	return out
}

// runRetrieval is the shared retrieval path. It snapshots retrievers and the
// RAG config under the lock (MemoryPatchExecutor.Apply mutates config fields
// under the same lock), checks the EnableRAG gate, and delegates to
// memctx.RunRetrieval which applies the canonical DefaultTopK /
// DefaultMinScore normalization.
func (m *ProductionMemoryManager) runRetrieval(ctx context.Context, input string) []memctx.ContextSnippet {
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
