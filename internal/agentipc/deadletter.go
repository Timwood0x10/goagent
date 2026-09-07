package agentipc

import (
	"sync"
	"time"
)

// DeadLetter is one failed IPC request preserved for diagnosis and manual or
// automatic redelivery. The bus records every request
// that could not be delivered or timed out, so multi-agent messaging failures
// are observable instead of vanishing at the error-return boundary.
type DeadLetter struct {
	// ID is a monotonically increasing sequence within the store.
	ID uint64
	// From / To / Topic / Payload mirror the failed request.
	From    string
	To      string
	Topic   string
	Payload any
	// TraceID is the causal chain the failed request belonged to:
	// join it against logs and collaboration receipts for the full story.
	TraceID string
	// Reason is why delivery failed (e.g. ErrAgentNotRegistered / ErrTimeout).
	Reason string
	// At is when the failure was recorded.
	At time.Time
}

// DeadLetterStore is a bounded FIFO of failed IPC requests. Thread-safe.
// Capacity is capped so a long-running process cannot grow it without limit
// (bounded aggregation — same ring policy as the flight aggregates).
type DeadLetterStore struct {
	mu       sync.Mutex
	next     uint64
	capacity int
	entries  []DeadLetter
}

// NewDeadLetterStore creates a store capped at capacity entries (oldest
// evicted first). A capacity <= 0 falls back to the default (1024).
func NewDeadLetterStore(capacity int) *DeadLetterStore {
	if capacity <= 0 {
		capacity = 1024
	}
	return &DeadLetterStore{capacity: capacity}
}

// Record appends a failed request, evicting the oldest entry when full.
func (s *DeadLetterStore) Record(from, to, topic string, payload any, reason, traceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	s.entries = append(s.entries, DeadLetter{
		ID:      s.next,
		From:    from,
		To:      to,
		Topic:   topic,
		Payload: payload,
		TraceID: traceID,
		Reason:  reason,
		At:      time.Now(),
	})
	if len(s.entries) > s.capacity {
		s.entries = s.entries[1:]
	}
}

// Snapshot returns a copy of the dead letters (oldest first).
func (s *DeadLetterStore) Snapshot() []DeadLetter {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]DeadLetter, len(s.entries))
	copy(out, s.entries)
	return out
}

// Count returns the number of dead letters currently retained.
func (s *DeadLetterStore) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}
