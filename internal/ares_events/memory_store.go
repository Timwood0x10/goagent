package ares_events

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// MemoryEventStore is an in-memory implementation of EventStore.
// Use for development, testing, and prototyping. Not for production.
type MemoryEventStore struct {
	mu          sync.RWMutex
	events      []*Event
	streams     map[string][]*Event
	versions    map[string]int64
	subscribers []subscription
	closed      bool
	ctx         context.Context
	cancel      context.CancelFunc
	nextSubID   atomic.Int64
	dropped     atomic.Int64
}

type subscription struct {
	id     int64
	filter EventFilter
	ch     chan *Event
}

// NewMemoryEventStore creates a new in-memory EventStore.
func NewMemoryEventStore() *MemoryEventStore {
	ctx, cancel := context.WithCancel(context.Background())
	return &MemoryEventStore{
		streams:  make(map[string][]*Event),
		versions: make(map[string]int64),
		ctx:      ctx,
		cancel:   cancel,
	}
}

// Append writes events to a stream with optimistic concurrency control.
func (s *MemoryEventStore) Append(_ context.Context, streamID string, events []*Event, expectedVersion int64) error {
	if streamID == "" {
		return ErrStreamNotFound
	}
	if len(events) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrEventStoreClosed
	}

	currentVersion := s.versions[streamID]

	// Optimistic concurrency control.
	// expectedVersion < 0: no check, append after current version.
	// expectedVersion == 0: auto-detect, append after current version (no conflict).
	// expectedVersion > 0: must match current version, otherwise ErrVersionConflict.
	if expectedVersion > 0 && currentVersion != expectedVersion {
		return ErrVersionConflict
	}
	startVersion := currentVersion

	// versionCounter advances only for non-nil events so skipped nil entries
	// never leave holes in the stream's version sequence (a nil event must not
	// consume a version number, unlike the old raw-index arithmetic).
	versionCounter := int64(0)
	for _, event := range events {
		if event == nil {
			continue
		}
		versionCounter++
		event.Version = startVersion + versionCounter
		if event.Version <= 0 {
			return fmt.Errorf("version overflow: computed %d", event.Version)
		}
		event.StreamID = streamID
		if event.Timestamp.IsZero() {
			event.Timestamp = timeNow()
		}
		if event.ID == "" {
			event.ID = NewEventID()
		}

		s.events = append(s.events, event)
		s.streams[streamID] = append(s.streams[streamID], event)
		s.versions[streamID] = event.Version

		// clone the event before notifying subscribers. The stored
		// *Event pointer is shared across all readers and the internal
		// streams slice; a subscriber that mutates the event would race
		// with concurrent Read/Append callers. The clone gives each
		// subscriber its own copy (read-only convention enforced by
		// design — the original is never mutated after Append).
		clone := *event
		s.notifySubscribers(&clone)
	}

	return nil
}

// Read returns events from a stream with optional filtering.
func (s *MemoryEventStore) Read(_ context.Context, streamID string, opts ReadOptions) ([]*Event, error) {
	if streamID == "" {
		return nil, ErrStreamNotFound
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil, ErrEventStoreClosed
	}

	stream := s.streams[streamID]
	if len(stream) == 0 {
		return []*Event{}, nil
	}

	// Filter by FromVersion (inclusive per ReadOptions contract).
	var filtered []*Event
	for _, event := range stream {
		if event.Version >= opts.FromVersion {
			filtered = append(filtered, event)
		}
	}

	// Filter by Since.
	if !opts.Since.IsZero() {
		var byTime []*Event
		for _, event := range filtered {
			if event.Timestamp.After(opts.Since) || event.Timestamp.Equal(opts.Since) {
				byTime = append(byTime, event)
			}
		}
		filtered = byTime
	}

	// Sort by direction.
	if opts.Direction == ReadDescending {
		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].Version > filtered[j].Version
		})
	}

	// Apply limit.
	if opts.Limit > 0 && len(filtered) > opts.Limit {
		filtered = filtered[:opts.Limit]
	}

	return filtered, nil
}

// ReadAll returns events across all streams.
func (s *MemoryEventStore) ReadAll(_ context.Context, opts ReadOptions) ([]*Event, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return nil, ErrEventStoreClosed
	}

	// FromVersion is per-stream and meaningless across streams,
	// so ReadAll ignores it (consistent with pg_store behavior).
	filtered := make([]*Event, len(s.events))
	copy(filtered, s.events)

	if !opts.Since.IsZero() {
		var byTime []*Event
		for _, event := range filtered {
			if event.Timestamp.After(opts.Since) || event.Timestamp.Equal(opts.Since) {
				byTime = append(byTime, event)
			}
		}
		filtered = byTime
	}

	if opts.Direction == ReadDescending {
		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].Timestamp.After(filtered[j].Timestamp)
		})
	} else {
		sort.Slice(filtered, func(i, j int) bool {
			return filtered[i].Timestamp.Before(filtered[j].Timestamp)
		})
	}

	if opts.Limit > 0 && len(filtered) > opts.Limit {
		filtered = filtered[:opts.Limit]
	}

	if filtered == nil {
		return nil, nil
	}
	return filtered, nil
}

// Subscribe returns a channel that receives matching events.
//
// PERF: Uses atomic counter for subscription IDs instead of UUID to reduce
// allocation. Subscribers slice is pre-allocated to reduce grow-copy overhead.
func (s *MemoryEventStore) Subscribe(ctx context.Context, filter EventFilter) (<-chan *Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil, ErrEventStoreClosed
	}

	ch := make(chan *Event, 64) // Larger buffer to reduce drops in burst scenarios.
	sub := subscription{
		id:     s.nextSubID.Add(1),
		filter: filter,
		ch:     ch,
	}
	s.subscribers = append(s.subscribers, sub)

	// Spawn a single goroutine per subscriber for cleanup when ctx is done.
	// The goroutine also exits when the store closes (s.ctx cancelled by
	// Close), so a subscriber with a never-cancelled caller context cannot
	// leak after Close (CRIT-2 fix).
	go func(id int64) {
		select {
		case <-ctx.Done():
			s.unsubscribe(id)
		case <-s.ctx.Done():
			// Store closed: Close() already closed every subscriber channel,
			// so there is nothing to clean up.
		}
	}(sub.id)

	return ch, nil
}

// StreamVersion returns the current version of a stream.
func (s *MemoryEventStore) StreamVersion(_ context.Context, streamID string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return 0, ErrEventStoreClosed
	}

	return s.versions[streamID], nil
}

// Close closes the store and all subscriber channels.
// Returns ErrEventStoreClosed if already closed.
func (s *MemoryEventStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return ErrEventStoreClosed
	}

	s.closed = true
	s.cancel()
	for _, sub := range s.subscribers {
		close(sub.ch)
	}
	s.subscribers = nil
	return nil
}

// Stats returns runtime metrics for the store. Currently exposes the count of
// events dropped because a subscriber's channel buffer was full, enabling
// monitoring/debugging of data loss in the event pipeline.
func (s *MemoryEventStore) Stats() map[string]int64 {
	return map[string]int64{
		"dropped_events": s.dropped.Load(),
	}
}

// SubscriberCount returns the number of currently registered subscribers.
// It exists for tests that assert a subscription is released when its context
// is cancelled (leak checks); production code should not need it.
func (s *MemoryEventStore) SubscriberCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.subscribers)
}

// notifySubscribers sends an event to all matching subscribers (non-blocking).
func (s *MemoryEventStore) notifySubscribers(event *Event) {
	for _, sub := range s.subscribers {
		if s.matchesFilter(event, sub.filter) {
			select {
			case sub.ch <- event:
			default:
				s.dropped.Add(1)
			}
		}
	}
}

// matchesFilter checks if an event matches a subscription filter.
func (s *MemoryEventStore) matchesFilter(event *Event, filter EventFilter) bool {
	// Filter by stream IDs.
	if len(filter.StreamIDs) > 0 {
		found := false
		for _, id := range filter.StreamIDs {
			if id == event.StreamID {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Filter by event types.
	if len(filter.Types) > 0 {
		found := false
		for _, t := range filter.Types {
			if t == event.Type {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Filter by timestamp.
	if !filter.Since.IsZero() && event.Timestamp.Before(filter.Since) {
		return false
	}

	return true
}

// unsubscribe removes a subscriber by ID and closes its channel.
// Safe to call concurrently with Close() because both hold s.mu.Lock().
func (s *MemoryEventStore) unsubscribe(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, sub := range s.subscribers {
		if sub.id == id {
			close(sub.ch)
			s.subscribers = append(s.subscribers[:i], s.subscribers[i+1:]...)
			return
		}
	}
}

// timeNow returns the current time. Extracted for testability.
var timeNow = time.Now

// TrimBefore removes all events with version <= endVersion from the given
// stream (the memory half of the compaction-trim loop). It implements
// TrimAwareStore so NewCompactableStoreWithArchive can wire it as the trim
// target: compaction summarizes a round, the archive sink persists it, and
// this call reclaims the raw events — without it the in-memory store grows
// without bound in long-running serve processes.
//
// The global events slice is rebuilt without the trimmed entries so replay /
// Read stay consistent with the per-stream view.
//
// Args:
//   - ctx: unused (memory operations are synchronous and unbounded-time).
//   - streamID: the stream to trim.
//   - endVersion: remove events with version <= this value.
//
// Returns:
//   - int64: the number of events removed.
//   - error: nil (unknown streams remove nothing).
func (m *MemoryEventStore) TrimBefore(ctx context.Context, streamID string, endVersion int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	removed := int64(0)
	if keep, ok := m.streams[streamID]; ok {
		// Fresh backing array: keep[:0] would rewrite the shared slice in
		// place, polluting results callers still hold from earlier Reads.
		filtered := make([]*Event, 0, len(keep))
		for _, ev := range keep {
			if ev.Version <= endVersion {
				removed++
				continue
			}
			filtered = append(filtered, ev)
		}
		m.streams[streamID] = filtered
	}
	if removed > 0 {
		global := make([]*Event, 0, len(m.events)-int(removed))
		for _, ev := range m.events {
			if ev.StreamID == streamID && ev.Version <= endVersion {
				continue
			}
			global = append(global, ev)
		}
		m.events = global
	}
	return removed, nil
}
