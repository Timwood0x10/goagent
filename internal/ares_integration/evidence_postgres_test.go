package ares_integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/evidence"
)

// TestEvidencePostgresStoreRoundTrip locks the REVIEW #33 contract against a
// real Postgres: append → query (source/kind/time-window filters, newest-first
// ordering, limit-after-sort) → aggregate. This is the exact surface whose
// absence let the uuid-vs-text DDL mismatch ship (#33 root cause).
func TestEvidencePostgresStoreRoundTrip(t *testing.T) {
	pool := getTestPool(t)
	if pool == nil {
		return
	}
	defer func() { _ = pool.Close() }()

	runMigrations(t, pool)
	ctx := context.Background()

	store, err := evidence.NewPostgresStore(pool)
	require.NoError(t, err)

	t.Cleanup(func() {
		_, _ = pool.GetDB().ExecContext(ctx, "DELETE FROM evidence_records WHERE source LIKE 'it-evidence-%'")
	})

	base := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	mk := func(id, source string, kind evidence.EvidenceKind, value float64, offset time.Duration) evidence.Evidence {
		return evidence.Evidence{
			ID:        id,
			Source:    source,
			Kind:      kind,
			Payload:   mustJSON(t, map[string]any{"value": value}),
			Timestamp: base.Add(offset),
		}
	}

	srcA, srcB := "it-evidence-a", "it-evidence-b"
	require.NoError(t, store.Append(ctx, mk("ev-1", srcA, "fitness", 0.5, 1*time.Second)))
	require.NoError(t, store.Append(ctx, mk("ev-2", srcA, "fitness", 0.9, 3*time.Second)))
	require.NoError(t, store.Append(ctx, mk("ev-3", srcA, "execution_trace", 0.7, 2*time.Second)))
	require.NoError(t, store.Append(ctx, mk("ev-4", srcB, "fitness", 0.1, 4*time.Second)))

	t.Run("query_all_orders_newest_first", func(t *testing.T) {
		got, err := store.Query(ctx, evidence.Filter{Source: srcA})
		require.NoError(t, err)
		require.Len(t, got, 3)
		assert.Equal(t, "ev-2", got[0].ID, "newest first")
		assert.Equal(t, "ev-3", got[1].ID)
		assert.Equal(t, "ev-1", got[2].ID)
	})

	t.Run("query_kind_filter", func(t *testing.T) {
		got, err := store.Query(ctx, evidence.Filter{Source: srcA, Kind: "fitness"})
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, "ev-2", got[0].ID)
		assert.Equal(t, "ev-1", got[1].ID)
	})

	t.Run("query_time_window_inclusive_bounds", func(t *testing.T) {
		got, err := store.Query(ctx, evidence.Filter{
			Source: srcA,
			Since:  base.Add(2 * time.Second), // inclusive lower bound
			Until:  base.Add(3 * time.Second), // inclusive upper bound
		})
		require.NoError(t, err)
		require.Len(t, got, 2, "Since/Until are inclusive on both ends")
	})

	t.Run("limit_applies_after_sorting", func(t *testing.T) {
		got, err := store.Query(ctx, evidence.Filter{Source: srcA, Limit: 1})
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "ev-2", got[0].ID, "top-N must be the most recent N")
	})

	t.Run("aggregate_averages_payload_values", func(t *testing.T) {
		sum, err := store.Aggregate(ctx, evidence.Filter{Source: srcA, Kind: "fitness"}, func(vs []float64) float64 {
			total := 0.0
			for _, v := range vs {
				total += v
			}
			return total
		})
		require.NoError(t, err)
		assert.InDelta(t, 1.4, sum, 1e-9)
	})

	t.Run("append_with_generated_id_round_trips", func(t *testing.T) {
		e := mk("", srcA, "fitness", 0.3, 5*time.Second)
		require.NoError(t, store.Append(ctx, e))
		got, err := store.Query(ctx, evidence.Filter{Source: srcA})
		require.NoError(t, err)
		require.NotEmpty(t, got)
		assert.NotEmpty(t, got[0].ID, "generated ID must be persisted and read back")
	})
}

// TestEvidencePostgresTTLExpiry locks the TTL contract: a record whose
// (ts + ttl) has passed must be (a) hidden from Query and (b) physically
// removed by CleanupExpired, while a zero-TTL record is never expired.
func TestEvidencePostgresTTLExpiry(t *testing.T) {
	pool := getTestPool(t)
	if pool == nil {
		return
	}
	defer func() { _ = pool.Close() }()

	runMigrations(t, pool)
	ctx := context.Background()

	store, err := evidence.NewPostgresStore(pool)
	require.NoError(t, err)

	src := "it-evidence-ttl"
	t.Cleanup(func() {
		_, _ = pool.GetDB().ExecContext(ctx, "DELETE FROM evidence_records WHERE source = $1", src)
	})

	now := time.Now().UTC()
	// Expired: timestamped 10s ago with a 1s TTL → already past retention.
	expired := evidence.Evidence{
		ID:        "ttl-expired",
		Source:    src,
		Kind:      "fitness",
		Payload:   mustJSON(t, map[string]any{"value": 1.0}),
		Timestamp: now.Add(-10 * time.Second),
		TTL:       time.Second,
	}
	// Live: 1h TTL, well within window.
	live := evidence.Evidence{
		ID:        "ttl-live",
		Source:    src,
		Kind:      "fitness",
		Payload:   mustJSON(t, map[string]any{"value": 2.0}),
		Timestamp: now,
		TTL:       time.Hour,
	}
	// Immortal: zero TTL never expires.
	immortal := evidence.Evidence{
		ID:        "ttl-zero",
		Source:    src,
		Kind:      "fitness",
		Payload:   mustJSON(t, map[string]any{"value": 3.0}),
		Timestamp: now.Add(-10 * time.Second),
		TTL:       0,
	}
	require.NoError(t, store.Append(ctx, expired))
	require.NoError(t, store.Append(ctx, live))
	require.NoError(t, store.Append(ctx, immortal))

	t.Run("query_hides_expired_rows", func(t *testing.T) {
		got, err := store.Query(ctx, evidence.Filter{Source: src})
		require.NoError(t, err)
		ids := make(map[string]bool, len(got))
		for _, e := range got {
			ids[e.ID] = true
		}
		assert.False(t, ids["ttl-expired"], "expired row must be filtered out of reads")
		assert.True(t, ids["ttl-live"], "live row must remain queryable")
		assert.True(t, ids["ttl-zero"], "zero-TTL row never expires")
	})

	t.Run("cleanup_deletes_only_expired", func(t *testing.T) {
		deleted, err := store.CleanupExpired(ctx)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, deleted, int64(1), "at least the expired row is purged")

		var remaining int
		row := pool.GetDB().QueryRowContext(ctx,
			"SELECT count(*) FROM evidence_records WHERE source = $1", src)
		require.NoError(t, row.Scan(&remaining))
		assert.Equal(t, 2, remaining, "live + zero-TTL rows survive cleanup")

		// Idempotent: a second pass removes nothing.
		deleted2, err := store.CleanupExpired(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(0), deleted2, "second cleanup pass is a no-op")
	})
}

// mustJSON marshals v or fails the test.
func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}
