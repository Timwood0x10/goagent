package main

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/ares_events"
)

// TestNewServeEventStoreMemoryMode locks the default path: no storage
// configured → the archive-enabled compactable store, byte-for-byte the
// pre-PG construction, with a non-nil closer.
func TestNewServeEventStoreMemoryMode(t *testing.T) {
	cfg := ares_config.NewMinimalConfig("http://localhost:11434", "", "")
	cfg.Storage.Enabled = false

	store, closeStore, err := newServeEventStore(cfg)
	require.NoError(t, err)
	require.NotNil(t, closeStore)

	// The compactable wrapper IS the memory-mode contract (round_N.json
	// archive + compaction/trim); a bare memory store here would silently
	// drop archiving.
	require.IsType(t, &ares_events.CompactableEventStore{}, store,
		"memory mode must build the archive-enabled compactable store")
	require.NoError(t, closeStore())
}

// TestNewServeEventStorePostgresMode locks the persistence path: storage
// configured → a PostgresEventStore whose construction ensures the events
// schema and whose writes are visible to a second, independently constructed
// store (the restart-persistence contract M4.1 wires). Skipped without
// TEST_POSTGRES_DSN, following the pg_store_test.go skip convention.
func TestNewServeEventStorePostgresMode(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set, skipping integration test")
	}
	u, err := url.Parse(dsn)
	require.NoError(t, err, "TEST_POSTGRES_DSN must be a postgres:// URL")
	port := 5432
	if p := u.Port(); p != "" {
		port, err = strconv.Atoi(p)
		require.NoError(t, err, "TEST_POSTGRES_DSN port must be numeric")
	}
	password, _ := u.User.Password()
	sslMode := "disable"
	if s := u.Query().Get("sslmode"); s != "" {
		sslMode = s
	}
	cfg := ares_config.NewMinimalConfig("http://localhost:11434", "", "")
	cfg.Storage = ares_config.StorageConfig{
		Enabled:  true,
		Host:     u.Hostname(),
		Port:     port,
		Username: u.User.Username(),
		Password: password,
		Database: strings.TrimPrefix(u.Path, "/"),
		SSLMode:  sslMode,
	}

	store, closeStore, err := newServeEventStore(cfg)
	require.NoError(t, err)
	defer func() { require.NoError(t, closeStore()) }()
	require.IsType(t, &ares_events.PostgresEventStore{}, store,
		"storage.enabled must build the Postgres event store")

	// Construction must have ensured the schema: an append succeeds without
	// any prior `ares db migrate` run.
	ctx := context.Background()
	stream := "serve-store-test-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	require.NoError(t, store.Append(ctx, stream, []*ares_events.Event{
		{Type: ares_events.EventTaskCreated, Payload: map[string]any{"task_id": "t1"}},
	}, 0))

	// A second, independently constructed store over the same config sees the
	// write — the durability a restart relies on.
	store2, closeStore2, err := newServeEventStore(cfg)
	require.NoError(t, err)
	defer func() { require.NoError(t, closeStore2()) }()
	got, err := store2.Read(ctx, stream, ares_events.ReadOptions{})
	require.NoError(t, err)
	require.Len(t, got, 1, "events must be visible to a fresh store instance (persistence)")
	require.Equal(t, ares_events.EventTaskCreated, got[0].Type)
}
