package ares_events

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPostgresEventStore_SubscribeBurstDeliversAll is the 1.4 integration
// regression: a burst exceeding defaultEventReadLimit (100) permanently
// wedged the subscriber — the poll re-read the same oldest 100 rows, the
// batch came back empty (all delivered), and the cursor never advanced, so
// E101+ were never delivered. The burst interleaves identical and distinct
// timestamps to also exercise the tie window at page boundaries.
//
// Requires TEST_POSTGRES_DSN; skips otherwise (Docker down).
func TestPostgresEventStore_SubscribeBurstDeliversAll(t *testing.T) {
	pool := getTestPool(t)
	defer func() { _ = pool.Close() }()
	cleanupEvents(t, pool)

	store := newTestPostgresEventStore(t, pool)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	streamID := fmt.Sprintf("test-sub-burst-%d", time.Now().UnixNano())

	ch, err := store.Subscribe(ctx, EventFilter{StreamIDs: []string{streamID}})
	require.NoError(t, err)

	// Give the subscriber a moment to start polling.
	time.Sleep(200 * time.Millisecond)

	const total = 250

	// Timestamps: groups of 5 share a value → ties straddle the LIMIT cut.
	base := time.Now().Add(-time.Hour)
	events := make([]*Event, 0, total)
	for i := 0; i < total; i++ {
		events = append(events, &Event{
			Type:      EventTaskCreated,
			Payload:   map[string]any{"index": i},
			Timestamp: base.Add(time.Duration(i/5) * time.Millisecond),
		})
	}
	require.NoError(t, store.Append(ctx, streamID, events, 0))

	// Poll interval is 1s: at least 3 pages (100+100+50) are needed, plus
	// re-read polls — 15s is generous headroom without risking a hang.
	seen := make(map[string]bool, total)
	deadline := time.After(15 * time.Second)
	for len(seen) < total {
		select {
		case evt := <-ch:
			require.NotNil(t, evt)
			assert.False(t, seen[evt.ID], "event %s re-delivered", evt.ID)
			seen[evt.ID] = true
		case <-deadline:
			t.Fatalf("subscription stalled: delivered %d/%d events", len(seen), total)
		case <-ctx.Done():
			t.Fatal("context cancelled before full delivery")
		}
	}

	assert.Len(t, seen, total, "no event lost, no event duplicated")

	cancel()
	_, ok := <-ch
	assert.False(t, ok, "channel should be closed after context cancel")
}
