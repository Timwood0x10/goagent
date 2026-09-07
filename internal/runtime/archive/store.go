// Package ares_archive — archive-enabled event store construction.
//
// This file is the single construction source for an archive-enabled
// CompactableEventStore. Both `ares serve` and `ares start` build their event
// store here so the two real service entry points share one pipeline (no
// duplicate/throwaway stores).
package archive

import (
	"fmt"

	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/ares_events"
)

// NewCompactableStoreWithArchive builds a CompactableEventStore with an archive
// sink wired from cfg.
//
// When cfg.IsEnabled() is false, no sink is attached and the store behaves as a
// plain compactable store: compaction still runs, but no round files are
// written. When enabled, a file-based ArchiveWriter is attached so each round
// is persisted to cfg.Dir before compaction can discard its raw events.
//
// It returns the compactable store and its underlying *MemoryEventStore so
// callers needing the concrete raw type (e.g. dashboard.SetEventStore, which
// requires *MemoryEventStore) need not reconstruct it. Both values are non-nil
// on success.
//
// Args:
//   - cfg: archive settings. Dir must be non-empty when enabled (the config
//     loader defaults it to DefaultArchiveDir).
//
// Returns:
//   - compactable: the archive-enabled auto-compacting event store.
//   - raw: the underlying in-memory store backing compactable.
//   - err: wrapped error if the compactable store or archive writer cannot be
//     created.
func NewCompactableStoreWithArchive(cfg ares_config.ArchiveConfig) (
	*ares_events.CompactableEventStore, *ares_events.MemoryEventStore, error,
) {
	mem := ares_events.NewMemoryEventStore()
	repo := ares_events.NewMemorySummaryRepository()
	// Wire mem as the trim target so the compaction loop actually
	// reclaims raw events (summarize → archive → trim). Passing nil left the
	// in-memory store growing without bound even with EnableTrimming=true.
	ces, err := ares_events.NewCompactableEventStore(
		mem, repo, mem, ares_events.DefaultCompactionConfig(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create compactable event store: %w", err)
	}

	if cfg.IsEnabled() {
		aw, awErr := NewFileArchiveWriter(cfg.Dir, cfg.MaxRounds)
		if awErr != nil {
			return nil, nil, fmt.Errorf("create archive writer: %w", awErr)
		}
		ces = ces.WithArchiveSink(NewEventArchiveSink(aw))
	}

	return ces, mem, nil
}
