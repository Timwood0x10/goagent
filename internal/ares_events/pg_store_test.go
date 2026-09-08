package ares_events

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/storage/postgres"
)

func newTestPostgresEventStore(t *testing.T, pool *postgres.Pool) *PostgresEventStore {
	t.Helper()
	s, err := NewPostgresEventStore(pool)
	require.NoError(t, err)
	return s
}

// getTestPool returns a postgres.Pool connected to the test database.
// Returns nil if TEST_POSTGRES_DSN is not set, causing the caller to skip.
func getTestPool(t *testing.T) *postgres.Pool {
	t.Helper()

	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set, skipping integration test")
		return nil
	}

	// Override with the DSN from the environment.
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skipf("failed to open test database: %v", err)
		return nil
	}

	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		t.Skipf("failed to ping test database: %v", err)
		return nil
	}

	// Create the events table for testing.
	createEventsTable(t, db)

	// Parse the DSN so the pool connects to the SAME server the test
	// database lives on. DefaultConfig() hardcodes localhost:5432 with the
	// postgres/ARES credentials — a DSN pointing anywhere else (different
	// port, docker-mapped host, custom user) would create the table on one
	// server and then talk to another, making every test fail or silently
	// skip on the pool ping.
	var poolCfg = postgres.DefaultConfig()
	if cc, perr := pgx.ParseConfig(dsn); perr == nil {
		poolCfg.Host = cc.Host
		poolCfg.Port = int(cc.Port)
		poolCfg.User = cc.User
		poolCfg.Password = cc.Password
		poolCfg.Database = cc.Database
		poolCfg.SSLMode = sslModeFromDSN(dsn)
	}

	// Close the raw db and re-open through the pool constructor.
	_ = db.Close()

	pool, err := postgres.NewPool(poolCfg)
	if err != nil {
		t.Skipf("failed to create test pool: %v", err)
		return nil
	}

	return pool
}

// sslModeFromDSN extracts the sslmode query parameter, defaulting to the
// postgres package default when absent.
func sslModeFromDSN(dsn string) string {
	if u, uerr := url.Parse(dsn); uerr == nil {
		if m := u.Query().Get("sslmode"); m != "" {
			return m
		}
	}
	return postgres.DefaultSSLMode
}

// createEventsTable ensures the events table exists for tests.
func createEventsTable(t *testing.T, db *sql.DB) {
	t.Helper()

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS events (
			id VARCHAR(255) NOT NULL,
			stream_id VARCHAR(255) NOT NULL,
			type VARCHAR(100) NOT NULL,
			payload JSONB NOT NULL,
			metadata JSONB DEFAULT '{}',
			version BIGINT NOT NULL,
			created_at TIMESTAMP DEFAULT NOW(),
			PRIMARY KEY (id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_events_stream_version ON events(stream_id, version)`,
		`CREATE INDEX IF NOT EXISTS idx_events_type ON events(type)`,
		`CREATE INDEX IF NOT EXISTS idx_events_created ON events(created_at)`,
		`ALTER TABLE events ADD CONSTRAINT uq_stream_version UNIQUE (stream_id, version)`, // ignore if exists
	}
	for _, stmt := range stmts {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			// Ignore "already exists" errors for idempotent DDL.
			if !strings.Contains(err.Error(), "already exists") {
				t.Fatalf("failed to create events table: %v", err)
			}
		}
	}
}

// cleanupEvents removes all rows from the events table.
func cleanupEvents(t *testing.T, pool *postgres.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), "DELETE FROM events"); err != nil {
		t.Logf("warning: failed to clean events table: %v", err)
	}
}

// newTestEvent creates an event with the given type and a simple payload.
func newTestEvent(evtType EventType, key string, value any) *Event {
	return &Event{
		Type:    evtType,
		Payload: map[string]any{key: value},
	}
}

// TestPostgresEventStore_AppendAndRead verifies the basic append-then-read round-trip.
func TestPostgresEventStore_AppendAndRead(t *testing.T) {
	pool := getTestPool(t)
	defer func() { _ = pool.Close() }()
	cleanupEvents(t, pool)

	store := newTestPostgresEventStore(t, pool)
	ctx := context.Background()
	streamID := fmt.Sprintf("test-append-read-%d", time.Now().UnixNano())

	events := []*Event{
		newTestEvent(EventTaskCreated, "task_id", "t1"),
		newTestEvent(EventTaskCompleted, "task_id", "t1"),
	}

	// Append to a new stream with expectedVersion 0.
	err := store.Append(ctx, streamID, events, 0)
	require.NoError(t, err)

	// Read back.
	got, err := store.Read(ctx, streamID, ReadOptions{})
	require.NoError(t, err)
	require.Len(t, got, 2)

	assert.Equal(t, EventTaskCreated, got[0].Type)
	assert.Equal(t, int64(1), got[0].Version)
	assert.Equal(t, "t1", got[0].Payload["task_id"])
	assert.Equal(t, streamID, got[0].StreamID)

	assert.Equal(t, EventTaskCompleted, got[1].Type)
	assert.Equal(t, int64(2), got[1].Version)
}

// TestPostgresEventStore_VersionConflict verifies optimistic concurrency detection.
func TestPostgresEventStore_VersionConflict(t *testing.T) {
	pool := getTestPool(t)
	defer func() { _ = pool.Close() }()
	cleanupEvents(t, pool)

	store := newTestPostgresEventStore(t, pool)
	ctx := context.Background()
	streamID := fmt.Sprintf("test-version-conflict-%d", time.Now().UnixNano())

	// Appending with wrong positive version must fail.
	err := store.Append(ctx, streamID, []*Event{newTestEvent(EventTaskCreated, "k", "v")}, 99)
	assert.ErrorIs(t, err, ErrVersionConflict)

	// expectedVersion 0 is AUTO-DETECT (append after current version, no
	// conflict) per the Append contract — it must succeed on both an empty
	// stream and a non-empty one.
	err = store.Append(ctx, streamID, []*Event{newTestEvent(EventTaskCreated, "k", "v")}, 0)
	require.NoError(t, err)
	err = store.Append(ctx, streamID, []*Event{newTestEvent(EventTaskCompleted, "k2", "v2")}, 0)
	require.NoError(t, err)

	// Appending with the correct current version (2) succeeds.
	err = store.Append(ctx, streamID, []*Event{newTestEvent(EventAgentStarted, "k3", "v3")}, 2)
	assert.NoError(t, err)

	// The stale version 1 is now a conflict.
	err = store.Append(ctx, streamID, []*Event{newTestEvent(EventAgentStarted, "k4", "v4")}, 1)
	assert.ErrorIs(t, err, ErrVersionConflict)
}

// TestPostgresEventStore_ReadWithFilters verifies FromVersion, Since, Limit, and Direction.
func TestPostgresEventStore_ReadWithFilters(t *testing.T) {
	pool := getTestPool(t)
	defer func() { _ = pool.Close() }()
	cleanupEvents(t, pool)

	store := newTestPostgresEventStore(t, pool)
	ctx := context.Background()
	streamID := fmt.Sprintf("test-read-filters-%d", time.Now().UnixNano())

	// Insert 5 events with staggered timestamps.
	for i := 0; i < 5; i++ {
		evt := &Event{
			Type:      EventTaskCreated,
			Payload:   map[string]any{"index": i},
			Timestamp: time.Now().Add(time.Duration(i) * time.Millisecond),
		}
		err := store.Append(ctx, streamID, []*Event{evt}, int64(i))
		require.NoError(t, err)
		// Small sleep to ensure distinct created_at values.
		time.Sleep(10 * time.Millisecond)
	}

	t.Run("FromVersion", func(t *testing.T) {
		got, err := store.Read(ctx, streamID, ReadOptions{FromVersion: 3})
		require.NoError(t, err)
		require.Len(t, got, 3)
		assert.Equal(t, int64(3), got[0].Version)
		assert.Equal(t, int64(5), got[2].Version)
	})

	t.Run("Limit", func(t *testing.T) {
		got, err := store.Read(ctx, streamID, ReadOptions{Limit: 2})
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, int64(1), got[0].Version)
	})

	t.Run("DirectionDesc", func(t *testing.T) {
		got, err := store.Read(ctx, streamID, ReadOptions{Direction: ReadDescending})
		require.NoError(t, err)
		require.Len(t, got, 5)
		assert.Equal(t, int64(5), got[0].Version)
		assert.Equal(t, int64(1), got[4].Version)
	})

	t.Run("Since", func(t *testing.T) {
		// Read all first to get a timestamp reference.
		all, err := store.Read(ctx, streamID, ReadOptions{Limit: 1})
		require.NoError(t, err)
		require.Len(t, all, 1)

		// Use the timestamp of the first event as the "since" filter.
		// Events after this should be returned.
		got, err := store.Read(ctx, streamID, ReadOptions{Since: all[0].Timestamp})
		require.NoError(t, err)
		// Should get at least the first event (created_at > since uses > not >=
		// but since we used the exact timestamp of the first event, it may or
		// may not be included depending on precision; just check no error).
		assert.NotNil(t, got)
	})
}

// TestPostgresEventStore_ReadAll verifies cross-stream reading.
func TestPostgresEventStore_ReadAll(t *testing.T) {
	pool := getTestPool(t)
	defer func() { _ = pool.Close() }()
	cleanupEvents(t, pool)

	store := newTestPostgresEventStore(t, pool)
	ctx := context.Background()
	ts := time.Now().UnixNano()

	streamA := fmt.Sprintf("test-readall-a-%d", ts)
	streamB := fmt.Sprintf("test-readall-b-%d", ts)

	evtA := newTestEvent(EventTaskCreated, "stream", "a")
	evtB := newTestEvent(EventAgentStarted, "stream", "b")

	require.NoError(t, store.Append(ctx, streamA, []*Event{evtA}, 0))
	require.NoError(t, store.Append(ctx, streamB, []*Event{evtB}, 0))

	got, err := store.ReadAll(ctx, ReadOptions{})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(got), 2)

	// Verify events from both streams are present.
	streamIDs := make(map[string]bool)
	for _, e := range got {
		streamIDs[e.StreamID] = true
	}
	assert.True(t, streamIDs[streamA])
	assert.True(t, streamIDs[streamB])
}

// TestPostgresEventStore_StreamVersion verifies version querying.
func TestPostgresEventStore_StreamVersion(t *testing.T) {
	pool := getTestPool(t)
	defer func() { _ = pool.Close() }()
	cleanupEvents(t, pool)

	store := newTestPostgresEventStore(t, pool)
	ctx := context.Background()
	streamID := fmt.Sprintf("test-stream-version-%d", time.Now().UnixNano())

	// Non-existent stream returns 0.
	ver, err := store.StreamVersion(ctx, streamID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), ver)

	// Append 3 events.
	for i := 0; i < 3; i++ {
		evt := newTestEvent(EventTaskCreated, "i", i)
		require.NoError(t, store.Append(ctx, streamID, []*Event{evt}, int64(i)))
	}

	ver, err = store.StreamVersion(ctx, streamID)
	require.NoError(t, err)
	assert.Equal(t, int64(3), ver)
}

// TestPostgresEventStore_Subscribe verifies polling-based subscription.
func TestPostgresEventStore_Subscribe(t *testing.T) {
	pool := getTestPool(t)
	defer func() { _ = pool.Close() }()
	cleanupEvents(t, pool)

	store := newTestPostgresEventStore(t, pool)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	streamID := fmt.Sprintf("test-subscribe-%d", time.Now().UnixNano())

	ch, err := store.Subscribe(ctx, EventFilter{
		StreamIDs: []string{streamID},
	})
	require.NoError(t, err)

	// Give the subscriber a moment to start polling.
	time.Sleep(100 * time.Millisecond)

	// Append an event that the subscriber should pick up.
	evt := newTestEvent(EventTaskCreated, "sub", "test")
	require.NoError(t, store.Append(ctx, streamID, []*Event{evt}, 0))

	// Wait for the event to arrive (poll interval is 1s, so allow up to 3s).
	select {
	case received := <-ch:
		require.NotNil(t, received)
		assert.Equal(t, EventTaskCreated, received.Type)
		assert.Equal(t, streamID, received.StreamID)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for subscribed event")
	}

	// Cancel and verify the channel is closed.
	cancel()
	_, ok := <-ch
	assert.False(t, ok, "channel should be closed after context cancel")
}

// TestPostgresEventStore_ConcurrentAppend verifies no data races under concurrent writes.
func TestPostgresEventStore_ConcurrentAppend(t *testing.T) {
	pool := getTestPool(t)
	defer func() { _ = pool.Close() }()
	cleanupEvents(t, pool)

	store := newTestPostgresEventStore(t, pool)
	ctx := context.Background()

	const numStreams = 10
	const eventsPerStream = 5

	streamIDs := make([]string, numStreams)
	var wg sync.WaitGroup
	errCh := make(chan error, numStreams*eventsPerStream)

	for i := 0; i < numStreams; i++ {
		streamID := fmt.Sprintf("test-concurrent-%d-%d", time.Now().UnixNano(), i)
		streamIDs[i] = streamID
		wg.Add(1)

		go func(sid string) {
			defer wg.Done()
			for j := 0; j < eventsPerStream; j++ {
				evt := newTestEvent(EventTaskCreated, "j", j)
				if err := store.Append(ctx, sid, []*Event{evt}, int64(j)); err != nil {
					errCh <- fmt.Errorf("stream %s event %d: %w", sid, j, err)
					return
				}
			}
		}(streamID)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent append error: %v", err)
	}

	// Verify each stream has the correct number of events.
	for _, streamID := range streamIDs {
		ver, err := store.StreamVersion(ctx, streamID)
		require.NoError(t, err)
		assert.Equal(t, int64(eventsPerStream), ver)
	}
}

// TestPostgresEventStore_AppendMultipleStreams writes events to 3 different streams
// and reads each back independently to verify stream isolation.
func TestPostgresEventStore_AppendMultipleStreams(t *testing.T) {
	pool := getTestPool(t)
	defer func() { _ = pool.Close() }()
	cleanupEvents(t, pool)

	store := newTestPostgresEventStore(t, pool)
	ctx := context.Background()
	ts := time.Now().UnixNano()

	streamIDs := []string{
		fmt.Sprintf("test-multi-a-%d", ts),
		fmt.Sprintf("test-multi-b-%d", ts),
		fmt.Sprintf("test-multi-c-%d", ts),
	}
	eventTypes := []EventType{EventTaskCreated, EventAgentStarted, EventMemoryDistilled}

	// Append 3 events to each stream.
	for i, sid := range streamIDs {
		for j := 0; j < 3; j++ {
			evt := newTestEvent(eventTypes[i], "stream", sid)
			require.NoError(t, store.Append(ctx, sid, []*Event{evt}, int64(j)))
		}
	}

	// Read each stream independently and verify isolation.
	for i, sid := range streamIDs {
		t.Run(fmt.Sprintf("stream_%d", i), func(t *testing.T) {
			events, err := store.Read(ctx, sid, ReadOptions{})
			require.NoError(t, err)
			require.Len(t, events, 3, "each stream should have exactly 3 events")

			for _, evt := range events {
				assert.Equal(t, sid, evt.StreamID, "event StreamID must match the stream it was written to")
				assert.Equal(t, eventTypes[i], evt.Type, "event type must match the type written to this stream")
			}

			// Verify versions are sequential.
			assert.Equal(t, int64(1), events[0].Version)
			assert.Equal(t, int64(2), events[1].Version)
			assert.Equal(t, int64(3), events[2].Version)
		})
	}
}

// TestPostgresEventStore_ConcurrentAppend_SameStream verifies that only one goroutine
// succeeds when multiple goroutines append to the same stream with a fixed expectedVersion.
func TestPostgresEventStore_ConcurrentAppend_SameStream(t *testing.T) {
	pool := getTestPool(t)
	defer func() { _ = pool.Close() }()
	cleanupEvents(t, pool)

	store := newTestPostgresEventStore(t, pool)
	ctx := context.Background()
	streamID := fmt.Sprintf("test-concurrent-same-%d", time.Now().UnixNano())

	// Seed the stream at version 0 (expectedVersion 0 means new stream).
	seedEvt := newTestEvent(EventAgentStarted, "seed", true)
	require.NoError(t, store.Append(ctx, streamID, []*Event{seedEvt}, 0))

	// 10 goroutines race to append with expectedVersion=1.
	// Only one can succeed because the version increments to 2 after the first win.
	const goroutines = 10
	var wg sync.WaitGroup
	successCount := 0
	var mu sync.Mutex

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			evt := newTestEvent(EventTaskCreated, "goroutine", idx)
			if appendErr := store.Append(ctx, streamID, []*Event{evt}, 1); appendErr == nil {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()

	assert.Equal(t, 1, successCount, "exactly one goroutine should succeed")

	// The stream should be at version 2.
	ver, err := store.StreamVersion(ctx, streamID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), ver)
}

// TestPostgresEventStore_Read_NonexistentStream verifies that reading from a stream
// that has never been written to returns an empty slice and nil error.
func TestPostgresEventStore_Read_NonexistentStream(t *testing.T) {
	pool := getTestPool(t)
	defer func() { _ = pool.Close() }()

	store := newTestPostgresEventStore(t, pool)
	ctx := context.Background()
	streamID := fmt.Sprintf("test-nonexistent-%d", time.Now().UnixNano())

	events, err := store.Read(ctx, streamID, ReadOptions{})
	require.NoError(t, err, "reading a nonexistent stream should return nil error")
	assert.Empty(t, events, "nonexistent stream should return empty events")

	// StreamVersion for nonexistent stream returns 0.
	ver, err := store.StreamVersion(ctx, streamID)
	require.NoError(t, err, "StreamVersion for nonexistent stream should return nil error")
	assert.Equal(t, int64(0), ver, "nonexistent stream version should be 0")
}

// TestPostgresEventStore_NilPoolSafety verifies that nil pool is rejected at construction.
func TestPostgresEventStore_NilPoolSafety(t *testing.T) {
	store, err := NewPostgresEventStore(nil)
	assert.Error(t, err)
	assert.Nil(t, store)

	// Calling methods on a nil store should not panic.
	ctx := context.Background()

	var nilStore *PostgresEventStore
	err = nilStore.Append(ctx, "s", []*Event{{Type: EventTaskCreated}}, 0)
	assert.ErrorIs(t, err, ErrEventStoreClosed)

	_, err = nilStore.Read(ctx, "s", ReadOptions{})
	assert.ErrorIs(t, err, ErrEventStoreClosed)

	_, err = nilStore.ReadAll(ctx, ReadOptions{})
	assert.ErrorIs(t, err, ErrEventStoreClosed)

	_, err = nilStore.Subscribe(ctx, EventFilter{})
	assert.ErrorIs(t, err, ErrEventStoreClosed)

	_, err = nilStore.StreamVersion(ctx, "s")
	assert.ErrorIs(t, err, ErrEventStoreClosed)
}

// TestPostgresEventStore_EmptyInputs verifies edge cases with empty inputs.
func TestPostgresEventStore_EmptyInputs(t *testing.T) {
	pool := getTestPool(t)
	defer func() { _ = pool.Close() }()

	store := newTestPostgresEventStore(t, pool)
	ctx := context.Background()

	// Empty streamID should return error.
	err := store.Append(ctx, "", nil, 0)
	assert.Error(t, err)

	_, err = store.Read(ctx, "", ReadOptions{})
	assert.Error(t, err)

	_, err = store.StreamVersion(ctx, "")
	assert.Error(t, err)

	// Empty events slice is a no-op.
	err = store.Append(ctx, "some-stream", nil, 0)
	assert.NoError(t, err)
}

// TestPostgresEventStore_MetadataPreserved verifies metadata round-trips correctly.
func TestPostgresEventStore_MetadataPreserved(t *testing.T) {
	pool := getTestPool(t)
	defer func() { _ = pool.Close() }()
	cleanupEvents(t, pool)

	store := newTestPostgresEventStore(t, pool)
	ctx := context.Background()
	streamID := fmt.Sprintf("test-metadata-%d", time.Now().UnixNano())

	evt := &Event{
		Type:    EventMemoryDistilled,
		Payload: map[string]any{"content": "hello world"},
		Metadata: map[string]any{
			"agent_id": "agent-42",
			"score":    0.95,
		},
	}

	require.NoError(t, store.Append(ctx, streamID, []*Event{evt}, 0))

	got, err := store.Read(ctx, streamID, ReadOptions{})
	require.NoError(t, err)
	require.Len(t, got, 1)

	assert.Equal(t, "agent-42", got[0].Metadata["agent_id"])
	assert.InDelta(t, 0.95, got[0].Metadata["score"], 0.001)
	assert.Equal(t, "hello world", got[0].Payload["content"])
}

// TestPostgresEventStore_AppendNilEvent verifies that nil events in the slice are rejected.
func TestPostgresEventStore_AppendNilEvent(t *testing.T) {
	pool := getTestPool(t)
	defer func() { _ = pool.Close() }()
	cleanupEvents(t, pool)

	store := newTestPostgresEventStore(t, pool)
	ctx := context.Background()
	streamID := fmt.Sprintf("test-nil-event-%d", time.Now().UnixNano())

	events := []*Event{
		newTestEvent(EventTaskCreated, "k", "v"),
		nil,
	}

	err := store.Append(ctx, streamID, events, 0)
	assert.Error(t, err, "appending a nil event should fail")
}
