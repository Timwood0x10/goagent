package ares_events

import "context"

// ArchiveSink is the minimal archiving contract the CompactableEventStore
// depends on.
//
// It is defined here, in the ares_events package, rather than imported from
// ares_archive, to avoid a cyclic import: ares_archive imports ares_events
// (extraction takes []*Event), so ares_events must not import archive.
// The concrete bridge archive.NewEventArchiveSink returns a value that
// satisfies this function type, and the wiring layer (internal/api_impl)
// connects the two.
//
// The sink is invoked at round boundaries (task-terminal events) and before
// compaction triggers, so a round's record is durable before the compaction
// core can discard the raw events. Sink failures are best effort: the caller
// logs them and never fails the Append or compaction path (see
// plan/context_compression_strategy.md §4).
//
// Args:
//   - ctx: timeout/cancellation context.
//   - round: 1-based round number for the stream (incremented per task lifecycle).
//   - streamID: the event stream the round belongs to.
//   - events: the round's events (task-lifecycle and tool calls) to summarize.
//
// Returns:
//   - error: non-nil only on archive write failure; always logged, never fatal.
type ArchiveSink func(ctx context.Context, round int, streamID string, events []*Event) error

// filterTerminalEvents returns the events that mark a round boundary: task
// completion or task failure. These are the signals that a conversation round
// has ended and its record should be archived. Returns nil when no terminal
// event is present.
func filterTerminalEvents(events []*Event) []*Event {
	if len(events) == 0 {
		return nil
	}
	var terminal []*Event
	for _, ev := range events {
		if ev == nil {
			continue
		}
		if ev.Type == EventTaskCompleted || ev.Type == EventTaskFailed {
			terminal = append(terminal, ev)
		}
	}
	return terminal
}
