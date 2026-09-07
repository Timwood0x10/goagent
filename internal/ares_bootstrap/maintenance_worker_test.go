package ares_bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_config"
)

// fakeCleaner counts CleanupExpired invocations and can be made to fail.
type fakeCleaner struct {
	calls atomic.Int64
	err   error
	panic bool
}

func (f *fakeCleaner) CleanupExpired(context.Context) (int64, error) {
	f.calls.Add(1)
	if f.panic {
		panic("boom")
	}
	return f.calls.Load(), f.err
}

// TestRunExpiredCleanup_AllCleanersInvoked verifies one pass calls every
// registered cleaner exactly once and keeps going after individual failures
// (error and panic) — maintenance must never take the process down.
func TestRunExpiredCleanup_AllCleanersInvoked(t *testing.T) {
	ok1 := &fakeCleaner{}
	bad := &fakeCleaner{err: errors.New("db down")}
	exploding := &fakeCleaner{panic: true}
	ok2 := &fakeCleaner{}

	cleaners := []NamedExpiryCleaner{
		{Name: "a", Cleaner: ok1},
		{Name: "bad", Cleaner: bad},
		{Name: "exploding", Cleaner: exploding},
		{Name: "d", Cleaner: ok2},
	}
	require.NotPanics(t, func() { runExpiredCleanup(context.Background(), cleaners) })

	require.Equal(t, int64(1), ok1.calls.Load())
	require.Equal(t, int64(1), bad.calls.Load())
	require.Equal(t, int64(1), exploding.calls.Load())
	require.Equal(t, int64(1), ok2.calls.Load(), "panic in earlier cleaner must not skip later ones")
}

// TestStartExpiryCleanupWorker_NoCleanersIsNoOp verifies the worker is not
// started (no goroutine on bgGroup) when nothing registered.
func TestStartExpiryCleanupWorker_NoCleanersIsNoOp(t *testing.T) {
	var comp Components
	comp.ExpiryCleaners = nil
	startExpiryCleanupWorker(context.Background(), &comp)
	// WaitBackground must return immediately: no goroutine was spawned.
	done := make(chan struct{})
	go func() { comp.WaitBackground(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WaitBackground blocked although no cleaner was wired")
	}
}

// TestWireExpiryCleaners_RegistersAllTables verifies that given a non-nil
// *sql.DB, sessions, conversations, knowledge_chunks_1024
// and secrets are all registered as named cleaners on Components. No DB
// connection is opened here — sql.Open is lazy — so this exercises pure
// wiring without a live Postgres.
func TestWireExpiryCleaners_RegistersAllTables(t *testing.T) {
	db, err := sql.Open("pq-stub", "")
	if err != nil {
		// The pq driver is registered via blank import in the package under
		// test; if sql.Open with a bogus driver fails, fall back to the real
		// driver name which is always present.
		db, err = sql.Open("postgres", "postgres://localhost/none?sslmode=disable")
	}
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	var comp Components
	cfg := &ares_config.Config{}
	wireExpiryCleaners(&comp, db, cfg)

	names := make(map[string]bool)
	for _, c := range comp.ExpiryCleaners {
		names[c.Name] = true
		require.NotNil(t, c.Cleaner, "cleaner %q must be non-nil", c.Name)
	}
	for _, want := range []string{"sessions", "conversations", "knowledge_chunks_1024", "secrets"} {
		require.True(t, names[want], "expiry cleaner %q must be registered", want)
	}
}

// TestWireExpiryCleaners_NilDBIsNoOp verifies a nil db registers nothing and
// does not panic (graceful skip when storage is unavailable).
func TestWireExpiryCleaners_NilDBIsNoOp(t *testing.T) {
	var comp Components
	require.NotPanics(t, func() { wireExpiryCleaners(&comp, nil, &ares_config.Config{}) })
	require.Empty(t, comp.ExpiryCleaners)
}

// TestDeriveSecretCleanupKey_Length verifies the derived key is always the
// 32 bytes NewSecretRepository requires, whether or not a JWT secret is set.
func TestDeriveSecretCleanupKey_Length(t *testing.T) {
	require.Len(t, deriveSecretCleanupKey(nil), 32)
	require.Len(t, deriveSecretCleanupKey(&ares_config.Config{}), 32)
	withSecret := &ares_config.Config{}
	withSecret.Security.JWTSecret = "some-secret"
	require.Len(t, deriveSecretCleanupKey(withSecret), 32)
}
