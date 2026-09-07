// Package sdk — Context cleaner factory.
//
// The differential turn-aware context cleaner lives in an internal package
// (internal/runtime/memory/context), so external consumers cannot construct it
// directly. This file re-exports a factory through the public SDK surface so
// host applications (e.g. ARES POLIS) can wire ARES's advanced context
// compression into their own chat pipelines.
package sdk

import (
	"github.com/Timwood0x10/ares/api/core"
	memctx "github.com/Timwood0x10/ares/internal/runtime/memory/context"
)

// NewContextCleaner returns an ARES turn-aware ContextCleaner backed by the
// differential compressor. It applies role-aware compression (tool results
// collapsed, code blocks compacted, turns summarized) instead of naive
// truncation.
//
// Usage:
//
//	cleaner := sdk.NewContextCleaner()
//	cleaned := cleaner.CleanWithTurns(messages, core.CleanOptions{...})
//
// Returns:
//
//	core.ContextCleaner - ready-to-use cleaner; never nil.
func NewContextCleaner() core.ContextCleaner {
	return &contextCleanerAdapter{inner: memctx.NewContextCleaner()}
}

// contextCleanerAdapter bridges the internal memctx.ContextCleaner (whose
// message type is the internal context.Message) onto the public
// core.ContextCleaner interface (core.Message). CleanOptions and CleanerStats
// are shared aliases, so only the message slice needs conversion.
type contextCleanerAdapter struct {
	inner *memctx.ContextCleaner
}

// Clean implements core.ContextCleaner.
func (a *contextCleanerAdapter) Clean(messages []core.Message, opts ...core.CleanOptions) []core.Message {
	cleaned := a.inner.Clean(toInternalMessages(messages), opts...)
	return toCoreMessages(cleaned)
}

// CleanWithTurns implements core.ContextCleaner.
func (a *contextCleanerAdapter) CleanWithTurns(messages []core.Message, opts ...core.CleanOptions) []core.Message {
	cleaned := a.inner.CleanWithTurns(toInternalMessages(messages), opts...)
	return toCoreMessages(cleaned)
}

// Stats implements core.ContextCleaner.
func (a *contextCleanerAdapter) Stats() core.CleanerStats {
	return a.inner.Stats()
}

// ResetStats implements core.ContextCleaner.
func (a *contextCleanerAdapter) ResetStats() {
	a.inner.ResetStats()
}

// toInternalMessages converts the public core.Message slice into the
// internal context.Message shape used by the cleaner.
func toInternalMessages(messages []core.Message) []memctx.Message {
	out := make([]memctx.Message, len(messages))
	for i, m := range messages {
		out[i] = memctx.Message{
			Role:         string(m.Role),
			Content:      m.Content,
			Time:         m.Time,
			TurnID:       m.TurnID,
			EventKind:    m.EventKind,
			ParentID:     m.ParentID,
			ArtifactRefs: m.ArtifactRefs,
		}
	}
	return out
}

// toCoreMessages converts cleaned internal messages back to the public
// core.Message shape. ID/SessionID/Metadata are not carried by the internal
// message type; callers that need them should re-apply them after cleaning.
func toCoreMessages(messages []memctx.Message) []core.Message {
	out := make([]core.Message, len(messages))
	for i, m := range messages {
		out[i] = core.Message{
			Role:         core.MessageRole(m.Role),
			Content:      m.Content,
			Time:         m.Time,
			TurnID:       m.TurnID,
			EventKind:    m.EventKind,
			ParentID:     m.ParentID,
			ArtifactRefs: m.ArtifactRefs,
		}
	}
	return out
}
