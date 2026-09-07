package ares_events

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	apperrors "github.com/Timwood0x10/ares/internal/errors"
)

// CompactableEventStore wraps an EventStore to automatically trigger compaction
// when a stream exceeds the configured event threshold.
//
// Usage pattern:
//
//	store := NewCompactableEventStore(
//	    pgStore,           // underlying EventStore (Postgres or Memory)
//	    summaryRepo,       // SummaryRepository for persisting summaries
//	    trimStore,         // TrimAwareStore for deleting compacted events
//	    DefaultCompactionConfig(),
//	)
//	// Use store.Append() as normal — compaction runs automatically in background.
type CompactableEventStore struct {
	EventStore
	compactor *Compactor
	trimStore TrimAwareStore
	mu        sync.Mutex

	// Track which streams have been recently checked to avoid redundant checks.
	// Key: streamID, value: last version at which compaction was checked.
	lastChecked map[string]int64

	// archiveSink archives round records at task-terminal boundaries and before
	// compaction. nil = no archiving. Set via WithArchiveSink.
	archiveSink ArchiveSink
	// archiveMu protects roundCounter and lastArchivedVersion. It is separate
	// from mu so I/O (stream Read, sink call) never holds mu.
	archiveMu sync.Mutex
	// roundCounter maps streamID -> next round number to assign (1-based).
	roundCounter map[string]int
	// lastArchivedVersion maps streamID -> stream version through which rounds
	// are archived. Reads for archiving start at this version (inclusive).
	lastArchivedVersion map[string]int64

	// lctx is the store-owned lifecycle context for background compaction work.
	// It is intentionally decoupled from any single Append caller's context:
	// compaction is best-effort maintenance that must outlive the request that
	// triggered it (a per-request ctx is cancelled when that request returns,
	// which would abort compaction mid-flight and starve streams fed by
	// short-lived requests). Call Close to cancel lctx and stop in-flight
	// compaction goroutines on shutdown.
	lctx    context.Context
	lcancel context.CancelFunc

	// wg tracks in-flight background compaction workers launched by Append so
	// Close can join them (bounded by compactionTimeout) before closing the
	// underlying store — a worker must never run against a closing store.
	// closed is set by Close under mu to stop new Append-launched workers from
	// racing with the WaitGroup wait (Add-after-Wait is a WaitGroup misuse).
	wg     sync.WaitGroup
	closed bool
}

// NewCompactableEventStore creates a new auto-compacting event store wrapper.
//
// Parameters:
//   - store: The underlying EventStore (PostgresEventStore or MemoryEventStore)
//   - repo: Repository for persisting event summaries
//   - trimStore: Optional TrimAwareStore for trimming compacted events (nil = no trimming)
//   - config: Compaction configuration
func NewCompactableEventStore(
	store EventStore,
	repo SummaryRepository,
	trimStore TrimAwareStore,
	config CompactionConfig,
) (*CompactableEventStore, error) {
	if store == nil {
		return nil, apperrors.New("store must not be nil")
	}
	if repo == nil {
		return nil, apperrors.New("summary repository must not be nil")
	}

	c := &CompactableEventStore{
		EventStore:          store,
		trimStore:           trimStore,
		lastChecked:         make(map[string]int64),
		roundCounter:        make(map[string]int),
		lastArchivedVersion: make(map[string]int64),
	}
	// Background compaction outlives any single request, so derive its lifecycle
	// context from Background (cancelled by Close) rather than a caller ctx.
	c.lctx, c.lcancel = context.WithCancel(context.Background())

	c.compactor = NewCompactor(store, repo, config)

	// If a trim store is provided and trimming is enabled, wire it into the compactor.
	if trimStore != nil && config.EnableTrimming {
		c.compactor = c.compactor.WithTrimStore(trimStore)
	}

	return c, nil
}

// compactionTimeout is the maximum duration allowed for a single compaction check.
const compactionTimeout = 30 * time.Second

// Close cancels the store's lifecycle context and waits for in-flight
// background compaction workers to finish (bounded by compactionTimeout),
// then closes the underlying EventStore if it implements io.Closer. It is
// safe to call multiple times. Callers should call Close during shutdown so
// compaction goroutines do not outlive the store; each compaction run is
// bounded by compactionTimeout, so the wait below never blocks indefinitely.
func (s *CompactableEventStore) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.lcancel()

	// Join in-flight compaction workers so none is still running against the
	// underlying store after Close returns. The worker group itself respects
	// lctx (cancelled above) and compactionTimeout, so the wait is bounded.
	waitCh := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(waitCh)
	}()
	select {
	case <-waitCh:
	case <-time.After(compactionTimeout + 5*time.Second):
	}

	if closer, ok := s.EventStore.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

// Append writes events to the store and then checks if compaction is needed.
func (s *CompactableEventStore) Append(
	ctx context.Context,
	streamID string,
	events []*Event,
	expectedVersion int64,
) error {
	err := s.EventStore.Append(ctx, streamID, events, expectedVersion)
	if err != nil {
		return err
	}

	// Detect a terminal event in the appended batch synchronously (before
	// launching the goroutine) so the async path never reads the caller's
	// slice. Non-terminal appends skip the archive scan entirely to avoid
	// re-reading the in-progress round on every Append; the pre-compaction
	// drain in maybeCompact is the safety net for rounds that accumulate
	// without a triggering Append.
	hasTerminal := s.archiveSink != nil && len(filterTerminalEvents(events)) > 0

	// Compaction is best-effort I/O (read stream, write summary, trim) that must
	// not block the event write path, so it runs asynchronously. The compaction
	// context is derived from the STORE's lifecycle context (s.lctx), not the
	// caller's ctx: a per-request ctx is cancelled when that request returns,
	// which would abort compaction mid-flight and starve streams fed by
	// short-lived requests. s.lctx is cancelled by Close on shutdown, so
	// in-flight workers stop promptly during teardown. Each run is still bounded
	// by compactionTimeout. The errgroup manages the worker goroutine; the
	// waiter goroutine releases the timeout context once the group completes.
	// The waiter is panic-free (only g.Wait + cancel) and cannot leak: the
	// workers respect gCtx, so they return as soon as s.lctx is cancelled or the
	// timeout fires, unblocking g.Wait and letting the waiter exit.
	compactCtx, cancel := context.WithTimeout(s.lctx, compactionTimeout)

	// Register the waiter with the store's WaitGroup so Close can join it.
	// The closed check runs under mu against Close's closed=true assignment:
	// if Close has already begun, Append skips launching (no Add-after-Wait);
	// if Append wins the race, Close's wg.Wait covers this worker.
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		return nil
	}
	s.wg.Add(1)
	s.mu.Unlock()

	// Waiter: drain the group asynchronously so Append stays non-blocking, then
	// release the timeout context. See the comment above for why this goroutine
	// is safe (managed lifecycle via errgroup, respects caller ctx, panic-free).
	go func() {
		defer s.wg.Done()
		defer cancel()
		if hasTerminal {
			if err := s.drainPendingRounds(compactCtx, streamID); err != nil {
				log.Warn("archive: drain pending rounds failed", "stream_id", streamID, "error", err)
			}
		}
		s.maybeCompact(compactCtx, streamID)
	}()

	return nil
}

// Read returns events for a stream. When the underlying store returns empty
// but summaries exist for the stream, it falls back to returning the summaries
// as synthetic events. This prevents ReplaySession from breaking after compaction
// has trimmed old raw events.
func (s *CompactableEventStore) Read(ctx context.Context, streamID string, opts ReadOptions) ([]*Event, error) {
	events, err := s.EventStore.Read(ctx, streamID, opts)
	if err != nil {
		return nil, err
	}
	if len(events) > 0 {
		return events, nil
	}

	// Underlying store returned empty — check summaries as fallback.
	if s.compactor == nil || s.compactor.repo == nil {
		return events, nil
	}
	summaries, summaryErr := s.compactor.repo.FindByStreamID(ctx, streamID)
	if summaryErr != nil || len(summaries) == 0 {
		// No summaries either, return the original empty result.
		return events, nil
	}

	// Convert summaries to synthetic events.
	synthetic := make([]*Event, 0, len(summaries))
	for _, sum := range summaries {
		synthetic = append(synthetic, &Event{
			ID:       sum.ID,
			StreamID: sum.StreamID,
			Type:     EventType("event.summary"),
			Payload: map[string]any{
				"summary_text":  sum.SummaryText,
				"event_count":   sum.EventCount,
				"start_version": sum.StartVersion,
				"end_version":   sum.EndVersion,
				"outcome":       sum.Outcome,
			},
			Version:   sum.EndVersion,
			Timestamp: sum.CreatedAt,
		})
	}
	return synthetic, nil
}

// Debounce divisor: skip compaction check until version advances by at least
// threshold/4 since the last check, reducing redundant I/O on busy streams.
const compactionCheckDivisor = 4

// maybeCompact checks if a stream needs compaction and runs it if so.
// Uses debouncing to avoid redundant checks on every Append.
func (s *CompactableEventStore) maybeCompact(ctx context.Context, streamID string) {
	if s.compactor == nil {
		return
	}
	// Get version outside the lock to avoid holding mu during I/O.
	version, err := s.StreamVersion(ctx, streamID)
	if err != nil {
		log.Debug("compaction: failed to get version", "stream_id", streamID, "error", err)
		return
	}

	s.mu.Lock()
	lastCheck := s.lastChecked[streamID]
	threshold := s.compactor.config.Threshold

	if version <= int64(threshold) || version-lastCheck < int64(threshold)/compactionCheckDivisor {
		s.mu.Unlock()
		return
	}
	s.lastChecked[streamID] = version
	s.mu.Unlock()

	// Pre-compaction archive flush (safety net). Drains ALL pending rounds
	// so the compaction core cannot trim raw events belonging to an
	// un-archived round (which would permanently lose its RoundRecord). Must
	// run BEFORE CheckAndCompact. Best-effort: never fails compaction.
	if s.archiveSink != nil {
		if archiveErr := s.drainPendingRounds(ctx, streamID); archiveErr != nil {
			log.Warn("compaction: pre-compaction archive drain failed", "stream_id", streamID, "error", archiveErr)
		}
	}

	didCompact, err := s.compactor.CheckAndCompact(ctx, streamID)
	if err != nil {
		log.Error("compaction: automatic compaction failed",
			"stream_id", streamID,
			"error", err,
		)
		return
	}

	if didCompact && s.trimStore != nil && s.compactor.config.EnableTrimming && s.compactor.repo != nil {
		summaries, err := s.compactor.repo.FindByStreamID(ctx, streamID)
		if err == nil && len(summaries) > 0 {
			latest := summaries[len(summaries)-1]
			if _, trimErr := s.trimStore.TrimBefore(ctx, streamID, latest.EndVersion); trimErr != nil {
				log.Warn("compaction: post-compaction trim failed", "error", trimErr)
			}
		}
	}

	if didCompact {
		log.Info("compaction: automatic compaction completed",
			"stream_id", streamID,
			"current_version", version,
		)
	}
}

// ForceCompact forces immediate compaction of a stream regardless of thresholds.
func (s *CompactableEventStore) ForceCompact(ctx context.Context, streamID string) (bool, error) {
	return s.compactor.ForceCompact(ctx, streamID)
}

// CleanupSummaries removes expired summaries based on TTL.
func (s *CompactableEventStore) CleanupSummaries(ctx context.Context) (int64, error) {
	return s.compactor.CleanupOldSummaries(ctx)
}

// GetSummariesForStream returns all summaries for a given stream.
func (s *CompactableEventStore) GetSummariesForStream(ctx context.Context, streamID string) ([]*EventSummary, error) {
	if s.compactor == nil || s.compactor.repo == nil {
		return nil, errors.New("event store: compactor not configured")
	}
	return s.compactor.repo.FindByStreamID(ctx, streamID)
}

// GetSummariesForAgent returns all summaries for an agent across all tasks.
func (s *CompactableEventStore) GetSummariesForAgent(ctx context.Context, agentID string) ([]*EventSummary, error) {
	if s.compactor == nil || s.compactor.repo == nil {
		return nil, errors.New("event store: compactor not configured")
	}
	return s.compactor.repo.FindByAgentID(ctx, agentID)
}

// WithCustomSummarizer replaces the default rule-based summarizer with a custom one
// (e.g., an LLM-powered summarizer for richer summaries).
func (s *CompactableEventStore) WithCustomSummarizer(summarizer EventSummarizer) *CompactableEventStore {
	s.compactor.summarizer = summarizer
	return s
}

// WithArchiveSink attaches a round-archive sink. The sink is invoked at round
// boundaries (task-terminal events) and before compaction, so a round's record
// is durable before the compaction core can discard the raw events. nil is a
// no-op. Returns the store for chaining.
func (s *CompactableEventStore) WithArchiveSink(sink ArchiveSink) *CompactableEventStore {
	s.archiveSink = sink
	return s
}

// archiveReadLimit caps the number of events read per archive scan page so a
// single archivePendingRoundsOnce call stays bounded even on very long
// streams. Rounds that span more than this many events are handled by paging:
// the scan accumulates events across pages until it reaches the terminal.
const archiveReadLimit = 500

// maxArchiveDrainRounds caps the number of rounds a single drain may archive,
// bounding work when a stream accumulates many terminals before compaction.
// Any residual is picked up by the next drain.
const maxArchiveDrainRounds = 1000

// archivePendingRounds archives the next un-archived round for the stream and
// returns its error. It is a thin wrapper around archivePendingRoundsOnce that
// discards the "archived" flag, preserved for direct unit-testing of the
// single-round path. Callers that must flush ALL pending rounds before
// compaction use drainPendingRounds instead.
func (s *CompactableEventStore) archivePendingRounds(ctx context.Context, streamID string) error {
	_, err := s.archivePendingRoundsOnce(ctx, streamID)
	return err
}

// drainPendingRounds repeatedly archives pending rounds until no un-archived
// terminal event remains (or an error/cancellation occurs). It is the
// pre-compaction safety net: every pending round must be flushed BEFORE
// CheckAndCompact so the compaction core cannot trim raw events belonging to
// an un-archived round, which would permanently lose its RoundRecord.
func (s *CompactableEventStore) drainPendingRounds(ctx context.Context, streamID string) error {
	for range maxArchiveDrainRounds {
		archived, err := s.archivePendingRoundsOnce(ctx, streamID)
		if err != nil {
			return err
		}
		if !archived {
			return nil
		}
	}
	return nil
}

// archivePendingRoundsOnce archives the next un-archived round (if any) for
// the stream. It pages through the un-archived window, accumulating the
// round's events until it finds the next terminal event (task completed or
// failed) or runs out of events. Paging ensures rounds that span more than
// archiveReadLimit events are archived completely — earlier events are never
// orphaned from their round record.
//
// When no terminal event is found, the round boundary (lastArchivedVersion)
// is left UNCHANGED so the next call re-scans from the same boundary and
// captures the in-progress events once the terminal arrives. (Advancing past
// non-terminal events would orphan them from their round record.)
//
// The function is safe for concurrent use and never holds archiveMu during
// stream I/O or the sink call (mirroring maybeCompact's lock discipline).
//
// Returns:
//   - archived: true when a round was archived (the sink was invoked).
//   - error: nil on success or "nothing to archive"; a wrapped error on read
//     or sink failure. A sink failure returns archived=true because the round
//     was already claimed (best-effort, no rollback).
func (s *CompactableEventStore) archivePendingRoundsOnce(ctx context.Context, streamID string) (bool, error) {
	if s.archiveSink == nil {
		return false, nil
	}

	// Step 1: snapshot the round boundary (last archived terminal version).
	s.archiveMu.Lock()
	roundStart := s.lastArchivedVersion[streamID]
	s.archiveMu.Unlock()

	// Step 2: page through the un-archived window, accumulating events until
	// the next terminal event or the end of the stream. lastSeen is both the
	// read cursor (ReadOptions.FromVersion is inclusive) and the dedup filter,
	// so the inclusive overlap event from the previous page is skipped.
	var roundEvents []*Event
	var terminal *Event
	lastSeen := roundStart
	for {
		if err := ctx.Err(); err != nil {
			return false, fmt.Errorf("archive: context: %w", err)
		}
		page, err := s.EventStore.Read(ctx, streamID, ReadOptions{
			FromVersion: lastSeen,
			Direction:   ReadAscending,
			Limit:       archiveReadLimit,
		})
		if err != nil {
			return false, fmt.Errorf("archive: read stream %q: %w", streamID, err)
		}
		if len(page) == 0 {
			break
		}
		for _, ev := range page {
			if ev == nil || ev.Version <= lastSeen {
				continue // skip inclusive overlap + the archived boundary terminal
			}
			roundEvents = append(roundEvents, ev)
			if ev.Type == EventTaskCompleted || ev.Type == EventTaskFailed {
				terminal = ev
				break
			}
		}
		lastSeen = page[len(page)-1].Version
		if terminal != nil {
			break
		}
		// Partial page => end of stream reached without a terminal.
		if len(page) < archiveReadLimit {
			break
		}
	}

	if terminal == nil {
		// No terminal yet — leave the round boundary unchanged so the
		// in-progress events are retained for the round record.
		return false, nil
	}

	// Step 3: compare-and-swap the round assignment under the lock.
	s.archiveMu.Lock()
	if current, ok := s.lastArchivedVersion[streamID]; ok && current != roundStart {
		// Another goroutine already advanced the boundary — round was claimed.
		s.archiveMu.Unlock()
		return false, nil
	}
	round := s.roundCounter[streamID] + 1
	s.archiveMu.Unlock()

	// Step 4: invoke the sink WITHOUT holding the lock. On failure the round
	// boundary is NOT advanced: drainPendingRounds re-scans the same window
	// next tick and retries the sink, so a transient I/O error degrades to a
	// delay instead of permanently losing the round record (the boundary used
	// to move BEFORE the durable write, letting compaction trim events whose
	// archive copy never landed).
	if err := s.archiveSink(ctx, round, streamID, roundEvents); err != nil {
		return false, fmt.Errorf("archive: sink round %d stream %q (boundary not advanced; will retry): %w", round, streamID, err)
	}

	// Sink succeeded — commit the round assignment.
	s.archiveMu.Lock()
	if current, ok := s.lastArchivedVersion[streamID]; ok && current != roundStart {
		// A concurrent claimant advanced the boundary while we were sinking.
		// Keep theirs; our sink wrote an overlapping-but-complete record,
		// which downstream dedup tolerates better than data loss.
		s.archiveMu.Unlock()
		return true, nil
	}
	s.roundCounter[streamID] = round
	s.lastArchivedVersion[streamID] = terminal.Version
	s.archiveMu.Unlock()
	return true, nil
}
