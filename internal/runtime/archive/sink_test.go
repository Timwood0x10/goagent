package archive

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// Potential bug scenarios tested below:
//  1. Nil ArchiveWriter — the sink returns an error instead of panicking
//     (TestNewEventArchiveSink_NilWriter).
//  2. Writer returns an error — the sink propagates the error without
//     panicking (TestNewEventArchiveSink_WriterError).
//  3. Cancelled context — the sink returns a wrapped context.Canceled error
//     without calling the writer (TestNewEventArchiveSink_CancelledContext).

// fakeWriter is a test ArchiveWriter that records every RecordRound call.
// It optionally returns a configured error to simulate write failures.
type fakeWriter struct {
	recorded []RoundRecord
	err      error
	flushErr error
}

func (f *fakeWriter) RecordRound(_ context.Context, record RoundRecord) error {
	if f.err != nil {
		return f.err
	}
	f.recorded = append(f.recorded, record)
	return nil
}

func (f *fakeWriter) Flush(_ context.Context) error {
	return f.flushErr
}

func TestNewEventArchiveSink_RecordsRound(t *testing.T) {
	w := &fakeWriter{}
	sink := NewEventArchiveSink(w)

	events := []*ares_events.Event{
		{
			Type: ares_events.EventTaskCompleted,
			Payload: map[string]any{
				ares_events.EventKeyTask:   "implement the feature",
				ares_events.EventKeyResult: "done",
			},
		},
		{
			Type: ares_events.EventToolCallCompleted,
			Payload: map[string]any{
				"tool_name": toolCodeRunner,
				"output":    "exit code: 0",
			},
		},
	}

	err := sink(context.Background(), 1, "stream-1", events)
	require.NoError(t, err)
	require.Len(t, w.recorded, 1, "RecordRound must be called exactly once")

	record := w.recorded[0]
	assert.Equal(t, 1, record.Round)
	assert.Equal(t, actionImplement, record.Action, "action inferred from task text")
	assert.Equal(t, verdictPass, record.Verdict.GoVet, "verdict extracted from code_runner output")
	assert.NotEmpty(t, record.Summary)
}

func TestNewEventArchiveSink_WriterError(t *testing.T) {
	writerErr := errors.New("disk full")
	w := &fakeWriter{err: writerErr}
	sink := NewEventArchiveSink(w)

	events := []*ares_events.Event{
		{
			Type: ares_events.EventTaskCompleted,
			Payload: map[string]any{
				ares_events.EventKeyTask: "do work",
			},
		},
	}

	err := sink(context.Background(), 1, "stream-1", events)
	require.Error(t, err)
	assert.ErrorIs(t, err, writerErr, "writer error must be propagated")
	assert.Len(t, w.recorded, 0, "failed record must not be appended")
}

func TestNewEventArchiveSink_NilWriter(t *testing.T) {
	// Bug scenario 1: nil writer must return an error, not panic.
	sink := NewEventArchiveSink(nil)
	err := sink(context.Background(), 1, "stream-1",
		[]*ares_events.Event{
			{Type: ares_events.EventTaskCompleted,
				Payload: map[string]any{ares_events.EventKeyTask: "work"}},
		})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

func TestNewEventArchiveSink_CancelledContext(t *testing.T) {
	// Bug scenario 3: cancelled context must return a wrapped error without
	// calling the writer.
	w := &fakeWriter{}
	sink := NewEventArchiveSink(w)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := sink(ctx, 1, "stream-1",
		[]*ares_events.Event{
			{Type: ares_events.EventTaskCompleted,
				Payload: map[string]any{ares_events.EventKeyTask: "work"}},
		})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Len(t, w.recorded, 0, "writer must not be called when ctx is cancelled")
}

func TestNewEventArchiveSink_InferAction(t *testing.T) {
	tests := []struct {
		name       string
		taskText   string
		wantAction string
	}{
		{"fix keyword", "fix the broken build", actionFix},
		{"bug keyword", "investigate a bug in auth", actionFix},
		{"review keyword", "review the PR changes", actionReview},
		{"plan keyword", "plan the migration strategy", actionPlan},
		{"design keyword", "design the new API", actionPlan},
		{"implement keyword", "implement the feature", actionImplement},
		{"no keyword defaults to implement", "do some work", actionImplement},
		{"empty task defaults to implement", "", actionImplement},
		// Word-boundary regression cases: substrings inside longer words must
		// NOT match (previously "prefix"→fix, "plane"→plan, "debug"→bug).
		{"prefix is not fix", "refactor the prefix handler", actionImplement},
		{"plane is not plan", "board the plane now", actionImplement},
		{"debug is not bug", "debug the failing test", actionImplement},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := []*ares_events.Event{
				{
					Type: ares_events.EventTaskCompleted,
					Payload: map[string]any{
						ares_events.EventKeyTask: tt.taskText,
					},
				},
			}
			got := inferAction(events)
			assert.Equal(t, tt.wantAction, got)
		})
	}
}

func TestNewEventArchiveSink_InferAction_NilEvents(t *testing.T) {
	assert.Equal(t, actionImplement, inferAction(nil))
	assert.Equal(t, actionImplement, inferAction([]*ares_events.Event{}))
}

func TestNewEventArchiveSink_InferAction_FromMessageContent(t *testing.T) {
	// When no EventKeyTask is present, inferAction should fall back to
	// EventMessageAdded keyContent payloads.
	events := []*ares_events.Event{
		{
			Type: ares_events.EventMessageAdded,
			Payload: map[string]any{
				keyContent: "let's review the code",
			},
		},
	}
	assert.Equal(t, actionReview, inferAction(events))
}

func TestNewEventArchiveSink_EmptyEvents(t *testing.T) {
	// BuildRoundRecord rejects empty events with ErrNoEvents; the sink must
	// propagate this error rather than calling the writer.
	w := &fakeWriter{}
	sink := NewEventArchiveSink(w)

	err := sink(context.Background(), 1, "stream-1", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoEvents)
	assert.Len(t, w.recorded, 0)
}

func TestNewEventArchiveSink_FlushDelegates(t *testing.T) {
	// The sink itself doesn't expose Flush, but the underlying writer does.
	// This test confirms the fakeWriter's Flush works as expected for
	// integration with the ArchiveWriter contract.
	w := &fakeWriter{}
	err := w.Flush(context.Background())
	require.NoError(t, err)

	w.flushErr = errors.New("flush failed")
	err = w.Flush(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, w.flushErr)
}
