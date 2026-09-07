// Package actionlog implements an append-only action store for audit and
// replay (ares-vs-prime-agent 5.3, medium-high priority). Every agent action
// (tool call, handoff, task result) is recorded as a durable Entry so the
// session can be audited or replayed — complementing event-sourced recovery
// with an explicit action log. The store is in-memory and concurrency-safe.
package actionlog

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Entry is one recorded action.
type Entry struct {
	// ID is the stable action identifier (used for replay ordering).
	ID string
	// SessionID scopes the entry to a session.
	SessionID string
	// AgentID is the acting agent.
	AgentID string
	// Action is the action name (e.g. "tool.call", "handoff", "task.result").
	Action string
	// Payload carries action-specific data for replay/audit.
	Payload map[string]any
	// Timestamp records when the action happened.
	Timestamp time.Time
}

// maxEntries caps the in-memory log: the store had no eviction, so a
// long-lived process grew entries forever. Oldest entries are evicted first;
// replay of fully-evicted sequences degrades to "start ID not found", which
// callers already treat as recoverable.
const maxEntries = 10000

// Store is an append-only action log with replay. It is safe for concurrent
// use.
type Store struct {
	mu      sync.RWMutex
	entries []Entry
	ids     map[string]struct{} // O(1) duplicate detection; guarded by mu
}

// NewStore creates an empty action store.
func NewStore() *Store {
	return &Store{
		entries: make([]Entry, 0),
		ids:     make(map[string]struct{}),
	}
}

// Append records an action. The entry's ID must be non-empty; duplicates are
// rejected so replay can rely on stable IDs (idempotency).
func (s *Store) Append(ctx context.Context, e Entry) error {
	if e.ID == "" {
		return errors.New("actionlog: entry ID must not be empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.ids[e.ID]; dup {
		return fmt.Errorf("actionlog: duplicate entry ID %q", e.ID)
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	s.entries = append(s.entries, e)
	s.ids[e.ID] = struct{}{}
	if len(s.entries) > maxEntries {
		evict := len(s.entries) - maxEntries
		for i := 0; i < evict; i++ {
			delete(s.ids, s.entries[i].ID)
		}
		s.entries = append(s.entries[:0], s.entries[evict:]...)
	}
	return nil
}

// List returns all entries in append order for a session.
func (s *Store) List(sessionID string) []Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		if e.SessionID == sessionID {
			out = append(out, e)
		}
	}
	return out
}

// Replay returns entries starting AFTER the given start ID, in append order,
// for replay of an action sequence. The start ID is the last already-handled
// action (recovery resumes from what follows it, so nothing is re-applied).
// When startID is empty, all entries for the session are returned.
func (s *Store) Replay(sessionID, startID string) ([]Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	start := 0
	if startID != "" {
		found := false
		for i, e := range s.entries {
			if e.SessionID == sessionID && e.ID == startID {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("actionlog: start ID %q not found for session %q", startID, sessionID)
		}
	}

	out := make([]Entry, 0)
	for _, e := range s.entries[start:] {
		if e.SessionID == sessionID {
			out = append(out, e)
		}
	}
	return out, nil
}

// Count returns the total number of stored entries.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}
