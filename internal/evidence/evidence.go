// Package evidence provides the universal data primitive for ARES.
//
// Every subsystem produces Evidence: Flight Recorder, Chaos, Memory, AKF, GA.
// GA consumes Evidence to compute fitness.
//
// Evidence is NOT a metric. It carries arbitrary payloads via Kind + Payload:
//
//	Flight → Kind: ExecutionTrace
//	Chaos  → Kind: Failure
//	Memory → Kind: Knowledge
//	AKF    → Kind: Insight
//	GA     → Kind: Fitness
//	LLM    → Kind: Critique
package evidence

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"
)

// EvidenceKind classifies the type of evidence.
type EvidenceKind string

const (
	KindExecutionTrace EvidenceKind = "execution_trace" // Flight Recorder
	KindFailure        EvidenceKind = "failure"         // Chaos Engineering
	KindKnowledge      EvidenceKind = "knowledge"       // Memory Distillation
	KindInsight        EvidenceKind = "insight"         // AKF
	KindFitness        EvidenceKind = "fitness"         // GA
	KindDimensionEval  EvidenceKind = "dimension_eval"  // Three-layer verifier diagnosis
)

// Evidence is the universal data primitive in ARES.
// Source identifies the producer. Kind classifies the content.
// Payload carries arbitrary data — it's NOT limited to metrics.
type Evidence struct {
	// ID is the unique evidence identifier.
	ID string `json:"id"`

	// Source identifies the producer, e.g. "flight", "chaos", "memory", "akf", "genome".
	Source string `json:"source"`

	// Kind classifies the evidence type, e.g. "execution_trace", "failure", "knowledge".
	Kind EvidenceKind `json:"kind"`

	// Payload carries arbitrary structured data.
	Payload json.RawMessage `json:"payload"`

	// Metadata holds labels and tags for filtering and aggregation.
	Metadata map[string]string `json:"metadata,omitempty"`

	// Timestamp is when the evidence was collected.
	Timestamp time.Time `json:"timestamp"`

	// TTL is the evidence retention duration. Zero means no expiry.
	TTL time.Duration `json:"ttl,omitempty"`
}

// Filter specifies criteria for evidence queries.
//
// TIME BOUNDS (review P1-4): Since and Until are INCLUSIVE on both ends —
// `since <= ts <= until`. MemoryStore skips `ts > Until` and `ts < Since`,
// PostgresStore compiles `ts >= Since AND ts <= Until`, so a record whose
// timestamp equals the boundary matches. Callers who need a half-open
// [since, until) semantics (e.g. abutting replay windows that must not share a
// boundary record) must adjust the boundary themselves — pass `until` minus one
// nanosecond so the inclusive store excludes the shared instant.
type Filter struct {
	Source string       `json:"source,omitempty"`
	Kind   EvidenceKind `json:"kind,omitempty"`
	Since  time.Time    `json:"since,omitempty"`
	Until  time.Time    `json:"until,omitempty"`
	Limit  int          `json:"limit,omitempty"`
}

// AggregateFn computes a single float64 value from a slice of float64 values.
// Used by EvidenceStore.Aggregate to compute metrics over evidence payloads.
type AggregateFn func(values []float64) float64

// Store persists and queries evidence.
type Store interface {
	// Append stores a new evidence record.
	Append(ctx context.Context, e Evidence) error

	// Query returns evidence matching the filter.
	// Results are ordered by timestamp descending.
	Query(ctx context.Context, filter Filter) ([]Evidence, error)

	// Aggregate computes a metric over matching evidence.
	// fn receives the extracted float64 values (caller must extract from Payload).
	Aggregate(ctx context.Context, filter Filter, fn AggregateFn) (float64, error)
}

// MemoryStore is an in-memory implementation of Store for testing and development.
type MemoryStore struct {
	mu   sync.RWMutex
	data []Evidence
	cap  int
}

// maxMemoryStoreRecords is the ring cap for the in-memory evidence store.
const maxMemoryStoreRecords = 1000

// NewMemoryStore creates an in-memory evidence store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		data: make([]Evidence, 0),
		cap:  maxMemoryStoreRecords,
	}
}

// Append adds evidence to the in-memory store.
func (s *MemoryStore) Append(_ context.Context, e Evidence) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = append(s.data, e)
	// P1-2: ring cap — drop the oldest record when the cap is exceeded.
	if s.cap > 0 && len(s.data) > s.cap {
		s.data = s.data[len(s.data)-s.cap:]
	}
	return nil
}

// Query returns evidence matching the filter from the in-memory store.
// Results are ordered by timestamp descending (newest first) to match the
// Store contract; Limit is applied AFTER sorting so callers asking for the
// top N receive the most recent N, not the oldest N. Expired records (those
// whose TTL has elapsed since Append) are excluded (TTL was previously
// a dead field).
func (s *MemoryStore) Query(_ context.Context, filter Filter) ([]Evidence, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now()
	var result []Evidence
	for _, e := range s.data {
		if filter.Source != "" && e.Source != filter.Source {
			continue
		}
		if filter.Kind != "" && e.Kind != filter.Kind {
			continue
		}
		if !filter.Since.IsZero() && e.Timestamp.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && e.Timestamp.After(filter.Until) {
			continue
		}
		// Skip expired records (zero TTL means no expiry).
		if e.TTL > 0 && now.Sub(e.Timestamp) > e.TTL {
			continue
		}
		result = append(result, e)
	}
	// Order by timestamp descending (newest first) per the Store contract.
	sort.Slice(result, func(i, j int) bool {
		return result[i].Timestamp.After(result[j].Timestamp)
	})
	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}
	return result, nil
}

// Aggregate computes a metric over matching evidence.
func (s *MemoryStore) Aggregate(ctx context.Context, filter Filter, fn AggregateFn) (float64, error) {
	results, err := s.Query(ctx, filter)
	if err != nil {
		return 0, err
	}
	values := make([]float64, 0, len(results))
	for _, e := range results {
		// Extract float64 from payload. Callers must ensure payload is a number.
		var v float64
		if err := json.Unmarshal(e.Payload, &v); err == nil {
			values = append(values, v)
		}
	}
	if len(values) == 0 {
		return 0, nil
	}
	return fn(values), nil
}

// Ensure MemoryStore implements Store.
var _ Store = (*MemoryStore)(nil)
