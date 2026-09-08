package evidence

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvidenceKind(t *testing.T) {
	assert.Equal(t, EvidenceKind("execution_trace"), KindExecutionTrace)
	assert.Equal(t, EvidenceKind("failure"), KindFailure)
	assert.Equal(t, EvidenceKind("knowledge"), KindKnowledge)
	assert.Equal(t, EvidenceKind("insight"), KindInsight)
	assert.Equal(t, EvidenceKind("fitness"), KindFitness)
}

func TestNewMemoryStore(t *testing.T) {
	s := NewMemoryStore()
	require.NotNil(t, s)
	assert.Len(t, s.data, 0)
}

// TestNewEvidence_UnmarshalablePayloadKeepsZeroRawMessage locks the
// constructor's degrade contract: a payload json.Marshal rejects (e.g. a
// channel) yields a zero RawMessage instead of a panic or a silently bogus
// value — stores and queries already treat zero RawMessage as "no payload".
func TestNewEvidence_UnmarshalablePayloadKeepsZeroRawMessage(t *testing.T) {
	e := NewEvidence("test", KindKnowledge, make(chan int))
	require.NotEmpty(t, e.ID)
	require.NotNil(t, e.Metadata)
	assert.Nil(t, e.Payload, "unmarshalable payload must keep the zero RawMessage")
}

// TestNewEvidence_MarshalablePayloadRidesRawMessage is the happy path: the
// payload is JSON-encoded onto Payload.
func TestNewEvidence_MarshalablePayloadRidesRawMessage(t *testing.T) {
	e := NewEvidence("test", KindKnowledge, map[string]any{"v": 1.0})
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(e.Payload, &decoded))
	assert.InDelta(t, 1.0, decoded["v"], 1e-9)
}

func TestMemoryStore_Append(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	payload, _ := json.Marshal(0.85)
	e := Evidence{
		ID:        "evt-1",
		Source:    "chaos",
		Kind:      KindFailure,
		Payload:   payload,
		Timestamp: time.Now(),
	}

	err := s.Append(ctx, e)
	assert.NoError(t, err)
	assert.Len(t, s.data, 1)
}

func TestMemoryStore_Query_NoFilter(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	seedEvidence(ctx, t, s, 3)

	results, err := s.Query(ctx, Filter{})
	assert.NoError(t, err)
	assert.Len(t, results, 3)
}

func TestMemoryStore_Query_BySource(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	seedEvidence(ctx, t, s, 3)

	results, err := s.Query(ctx, Filter{Source: "chaos"})
	assert.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "chaos", results[0].Source)
}

func TestMemoryStore_Query_ByKind(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	seedEvidence(ctx, t, s, 3)

	results, err := s.Query(ctx, Filter{Kind: KindFitness})
	assert.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, KindFitness, results[0].Kind)
}

func TestMemoryStore_Query_WithLimit(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	seedEvidence(ctx, t, s, 5)

	results, err := s.Query(ctx, Filter{Limit: 2})
	assert.NoError(t, err)
	assert.Len(t, results, 2)
}

func TestMemoryStore_Query_ByTimeRange(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	now := time.Now()

	e1 := Evidence{ID: "e1", Source: "test", Kind: KindInsight, Payload: json.RawMessage(`0.5`), Timestamp: now.Add(-2 * time.Hour)}
	e2 := Evidence{ID: "e2", Source: "test", Kind: KindInsight, Payload: json.RawMessage(`0.6`), Timestamp: now.Add(-1 * time.Hour)}
	e3 := Evidence{ID: "e3", Source: "test", Kind: KindInsight, Payload: json.RawMessage(`0.7`), Timestamp: now}

	require.NoError(t, s.Append(ctx, e1))
	require.NoError(t, s.Append(ctx, e2))
	require.NoError(t, s.Append(ctx, e3))

	// Query last 90 minutes.
	results, err := s.Query(ctx, Filter{Since: now.Add(-90 * time.Minute)})
	assert.NoError(t, err)
	assert.Len(t, results, 2) // e2, e3
}

func TestMemoryStore_Query_NoMatch(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	seedEvidence(ctx, t, s, 3)

	results, err := s.Query(ctx, Filter{Source: "nonexistent"})
	assert.NoError(t, err)
	assert.Len(t, results, 0)
}

func TestMemoryStore_Aggregate_Mean(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	for i := 1; i <= 4; i++ {
		v := float64(i) * 0.1
		payload, _ := json.Marshal(v)
		require.NoError(t, s.Append(ctx, Evidence{
			ID:      fmt.Sprintf("e%d", i),
			Source:  "test",
			Kind:    KindFitness,
			Payload: payload,
		}))
	}

	mean := func(vals []float64) float64 {
		sum := 0.0
		for _, v := range vals {
			sum += v
		}
		return sum / float64(len(vals))
	}

	result, err := s.Aggregate(ctx, Filter{Kind: KindFitness}, mean)
	assert.NoError(t, err)
	assert.InDelta(t, 0.25, result, 0.001)
}

func TestMemoryStore_Aggregate_Empty(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	result, err := s.Aggregate(ctx, Filter{Source: "nonexistent"}, func(vals []float64) float64 { return 0 })
	assert.NoError(t, err)
	assert.Equal(t, 0.0, result)
}

func TestMemoryStore_ConcurrentAppend(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	const n = 100
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			payload, _ := json.Marshal(float64(i))
			_ = s.Append(ctx, Evidence{
				ID:      fmt.Sprintf("e%d", i),
				Source:  "concurrent",
				Kind:    KindInsight,
				Payload: payload,
			})
		}()
	}
	wg.Wait()

	results, err := s.Query(ctx, Filter{Source: "concurrent"})
	assert.NoError(t, err)
	assert.Len(t, results, n)
}

// ── Helpers ────────────────────────────────────

func seedEvidence(ctx context.Context, t *testing.T, s *MemoryStore, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		source := "flight"
		kind := KindExecutionTrace
		if i == 1 {
			source = "chaos"
			kind = KindFailure
		}
		if i == 2 {
			source = "genome"
			kind = KindFitness
		}
		payload, _ := json.Marshal(float64(i) * 0.1)
		require.NoError(t, s.Append(ctx, Evidence{
			ID:        fmt.Sprintf("e%d", i),
			Source:    source,
			Kind:      kind,
			Payload:   payload,
			Timestamp: time.Now(),
		}))
	}
}

// TestMemoryStore_TTLExpiry locks the TTL contract: the TTL field is honored —
// a record whose (timestamp + TTL) has elapsed is excluded from Query (zero
// TTL means no expiry).
func TestMemoryStore_TTLExpiry(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	base := time.Now().Add(-2 * time.Minute) // 2 minutes ago

	fresh := Evidence{ID: "fresh", Source: "ttl", Kind: KindFitness,
		Payload: json.RawMessage(`0.5`), Timestamp: base, TTL: time.Hour} // not expired
	expired := Evidence{ID: "expired", Source: "ttl", Kind: KindFitness,
		Payload: json.RawMessage(`0.7`), Timestamp: base, TTL: time.Minute} // expired (age 2m > ttl 1m)
	noTTL := Evidence{ID: "no-ttl", Source: "ttl", Kind: KindFitness,
		Payload: json.RawMessage(`0.9`), Timestamp: base} // zero TTL = never expires
	for _, e := range []Evidence{fresh, expired, noTTL} {
		require.NoError(t, s.Append(ctx, e))
	}

	got, err := s.Query(ctx, Filter{Source: "ttl"})
	require.NoError(t, err)
	var ids []string
	for _, e := range got {
		ids = append(ids, e.ID)
	}
	assert.ElementsMatch(t, []string{"fresh", "no-ttl"}, ids,
		"expired record must be filtered out; zero-TTL record must survive")
}

// TestPostgresGeneratedID_CollisionResistant locks the N9 contract: the
// client-side generated ID for a zero-ID append includes a content digest, so
// two distinct records with the same source and the same nanosecond timestamp
// do not collide (the old "%d-source" form did).
func TestPostgresGeneratedID_CollisionResistant(t *testing.T) {
	ts := time.Date(2026, 8, 26, 12, 0, 0, 123456789, time.UTC)
	sameSource := "collision-src"
	a := Evidence{Source: sameSource, Kind: KindFitness,
		Payload: json.RawMessage(`{"value":1}`), Timestamp: ts}
	b := Evidence{Source: sameSource, Kind: KindFitness,
		Payload: json.RawMessage(`{"value":2}`), Timestamp: ts} // same nano, different payload

	idA, idB := generatedEvidenceID(a), generatedEvidenceID(b)
	assert.NotEqual(t, idA, idB, "distinct payloads at the same nanosecond must not collide")

	// Determinism: retries of the SAME record produce the SAME id.
	assert.Equal(t, idA, generatedEvidenceID(a))
}
