package archive

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// Action-keyword regexes use \b word boundaries so that substrings inside
// longer words do not mislabel the action (e.g. "prefix" no longer matches
// "fix", "plane" no longer matches "plan", "debug" no longer matches "bug").
// Matching is case-insensitive.
var (
	reActionFix    = regexp.MustCompile(`\b(fix|bug)\b`)
	reActionReview = regexp.MustCompile(`\breview\b`)
	reActionPlan   = regexp.MustCompile(`\b(plan|design)\b`)
)

// NewEventArchiveSink returns an ares_events.ArchiveSink that builds a
// RoundRecord from events via BuildRoundRecord and persists it through w.
//
// This is the bridge between ares_events (which defines ArchiveSink) and
// ares_archive (which implements ArchiveWriter). It breaks the import cycle:
// ares_events owns the func type, ares_archive provides the implementation,
// and the serve wiring connects them.
//
// Failures are returned so the caller can log them; they never block
// compaction (best effort, §4 of the strategy doc). The sink infers the
// round action from the task text (defaulting to "implement") so that
// Validate always passes for well-formed events.
//
// Args:
//   - w: the ArchiveWriter that persists the built record. Must be non-nil;
//     a nil writer causes the returned sink to return an error on every call
//     rather than panicking.
//
// Returns:
//   - ares_events.ArchiveSink: a function matching the ArchiveSink contract.
func NewEventArchiveSink(w ArchiveWriter) ares_events.ArchiveSink {
	return func(ctx context.Context, round int, streamID string, events []*ares_events.Event) error {
		if w == nil {
			return errors.New("archive sink: writer is nil")
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("archive sink: context: %w", err)
		}
		action := inferAction(events)
		record, err := BuildRoundRecord(ctx, round, action, events, nil)
		if err != nil {
			return fmt.Errorf("archive sink: build record: %w", err)
		}
		// Stamp the stream identity so per-stream round numbers do not collide
		// on disk: every stream restarts at round 1, and a stream-agnostic
		// filename would let two streams' round_1.json overwrite each other
		// (REVIEW #50). The writer routes each stream to its own subdirectory.
		record.StreamID = streamID
		if err := w.RecordRound(ctx, *record); err != nil {
			return fmt.Errorf("archive sink: record round %d: %w", round, err)
		}
		return nil
	}
}

// inferAction examines the event stream's task text and infers which allowed
// action the round represents. The heuristic checks for keywords (in priority
// order) using word-boundary matching: "fix"/"bug" → "fix"; "review" →
// "review"; "plan"/"design" → "plan". Word boundaries prevent false positives
// like "prefix" matching "fix" or "plane" matching "plan". When no keyword
// matches (or no task text is found), it defaults to "implement".
//
// The returned value is always one of the allowed actions
// (plan|implement|fix|review) so that RoundRecord.Validate passes.
//
// Args:
//   - events: the round's events. The function scans EventKeyTask payloads
//     and EventMessageAdded "content" payloads for task text.
//
// Returns:
//   - string: one of "plan", "implement", "fix", "review".
func inferAction(events []*ares_events.Event) string {
	for _, ev := range events {
		if ev == nil {
			continue
		}
		text := ""
		if t, ok := ev.Payload[ares_events.EventKeyTask].(string); ok {
			text = t
		} else if ev.Type == ares_events.EventMessageAdded {
			if c, ok := ev.Payload[keyContent].(string); ok {
				text = c
			}
		}
		if text == "" {
			continue
		}
		lower := strings.ToLower(text)
		switch {
		case reActionFix.MatchString(lower):
			return actionFix
		case reActionReview.MatchString(lower):
			return actionReview
		case reActionPlan.MatchString(lower):
			return actionPlan
		}
	}
	return actionImplement
}
