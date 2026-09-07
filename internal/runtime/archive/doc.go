// Package ares_archive provides archive-style round summarization for the
// closed-loop agent.
//
// Each conversation round is persisted as an independent RoundRecord under
// .context/rounds/round_N.json. Records are never merged (git-log-per-commit,
// not git-squash), so later rounds can reference "round N's conclusion" rather
// than a fragment of a compacted tool output.
//
// Retention follows a multi-level priority (see plan/context_compression_strategy.md):
//   - P0 architecture decisions and P3 identifiers (commit hash, PR#, IP:port)
//     are preserved verbatim and never truncated.
//   - P2 verification state (pass/fail) is preserved as a conclusion; the raw
//     P4 tool output is discarded.
//
// The archive is independent of the compaction core (internal/ares_events.Compactor):
// archive files survive compaction untouched. The integration point is the
// CompactableEventStore wrapper, which flushes the archive before compaction
// triggers via an ares_events.ArchiveSink callback (see archive_hook.go).
package archive
