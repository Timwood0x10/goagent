package taskfabric

import (
	"context"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/storage/postgres"
)

// pgRestoreTestPool connects to the TEST_POSTGRES_DSN database (skipping when
// unset — the pg_store_test.go skip convention) and returns a pool the test
// can build PostgresEventStores over. The DSN is parsed into a
// postgres.Config instead of using DefaultConfig so the test runs against
// whatever database the environment provides.
func pgRestoreTestPool(t *testing.T) *postgres.Pool {
	t.Helper()
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
	pool, err := postgres.NewPool(&postgres.Config{
		Host:     u.Hostname(),
		Port:     port,
		User:     u.User.Username(),
		Password: password,
		Database: strings.TrimPrefix(u.Path, "/"),
		SSLMode:  sslMode,
	})
	require.NoError(t, err, "connect to TEST_POSTGRES_DSN")
	t.Cleanup(func() { _ = pool.Close() })
	return pool
}

// pgCleanupEvents empties the events table so one test's rows never leak into
// another's restore fold.
func pgCleanupEvents(t *testing.T, pool *postgres.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), "DELETE FROM events")
	require.NoError(t, err, "cleanup events table")
}

// TestRestoreFromStorePostgresRoundTrip is the M4.1 persistence contract,
// end to end: a task lifecycle persisted through a PostgresEventStore by one
// fabric is folded back by a FRESH fabric + FRESH store instance over the
// same database — exactly the shape of a serve restart with storage.enabled.
// It also exercises the JSON-round-tripped payload forms (float64 numbers,
// []any dependencies) that the in-memory restore tests never hit.
func TestRestoreFromStorePostgresRoundTrip(t *testing.T) {
	pool := pgRestoreTestPool(t)
	pgCleanupEvents(t, pool)
	ctx := context.Background()

	// "Previous boot": a fabric with the PG-backed store persists a task that
	// yielded mid-execution with a checkpoint (crash mid-quantum) plus a task
	// that completed.
	store1, err := ares_events.NewPostgresEventStore(pool)
	require.NoError(t, err)
	f1 := NewFabric().WithEventStore(store1)

	require.NoError(t, f1.Create(&Task{
		ID:         "peer-plan-7",
		Capability: "ares/plan",
		Priority:   3,
		Origin:     "agent-root",
		RetryPolicy: RetryPolicy{
			MaxRetries: 2,
			Attempts:   1,
		},
	}))
	epoch1, err := f1.Acquire("peer-plan-7", "agent-a", time.Minute)
	require.NoError(t, err)
	require.NoError(t, f1.Start("peer-plan-7", "agent-a", epoch1))
	require.NoError(t, f1.Yield("peer-plan-7", "agent-a", epoch1, EncodeCheckpoint(DecodedCheckpoint{
		Payload:        map[string]any{"input": "audit the events table"},
		StepCheckpoint: map[string]any{"step": 3},
	})))

	require.NoError(t, f1.Create(&Task{ID: "peer-plan-8", Capability: "tool/echo"}))
	epoch8, err := f1.Acquire("peer-plan-8", "agent-a", time.Minute)
	require.NoError(t, err)
	require.NoError(t, f1.Complete("peer-plan-8", "agent-a", epoch8))

	// "Restart": a fresh store instance over the same pool, a fresh fabric.
	// The store construction itself re-ensures the schema idempotently.
	store2, err := ares_events.NewPostgresEventStore(pool)
	require.NoError(t, err)
	f2 := NewFabric().WithEventStore(store2)
	require.NoError(t, f2.RestoreFromStore(ctx))

	// Non-terminal task folds back to READY, unowned, with its checkpoint.
	got, err := f2.Task("peer-plan-7")
	require.NoError(t, err)
	require.Equal(t, StateReady, got.State, "non-terminal task must fold to READY")
	require.Empty(t, got.Owner, "lease must never be restored")
	require.Equal(t, "ares/plan", got.Capability)
	require.Equal(t, 3, got.Priority)
	require.Equal(t, "agent-root", got.Origin)
	require.Equal(t, RetryPolicy{MaxRetries: 2, Attempts: 1}, got.RetryPolicy)
	require.NotNil(t, got.Checkpoint, "checkpoint must survive the PG round-trip")
	dc, err := DecodeCheckpoint(got.Checkpoint)
	require.NoError(t, err)
	require.Equal(t, CurrentCheckpointSchemaVersion, dc.SchemaVersion)
	require.Equal(t, map[string]any{"input": "audit the events table"}, dc.Payload)
	require.Equal(t, map[string]any{"step": float64(3)}, dc.StepCheckpoint)

	// Terminal task stays terminal — a completed task is never revived.
	done, err := f2.Task("peer-plan-8")
	require.NoError(t, err)
	require.Equal(t, StateCompleted, done.State)

	// Fencing: the rebuilt fabric's first token must strictly dominate every
	// token the previous boot handed out, and the stale pre-crash holder must
	// be rejected.
	epoch2, err := f2.Acquire("peer-plan-7", "agent-b", time.Minute)
	require.NoError(t, err)
	require.Greater(t, epoch2, epoch1, "restored epoch must exceed all pre-restart epochs")
	err = f2.Complete("peer-plan-7", "agent-a", epoch1)
	require.ErrorIs(t, err, ErrEpochMismatch, "pre-restart epoch must not be accepted after restore")
}
