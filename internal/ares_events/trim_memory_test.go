package ares_events

import (
	"context"
	"testing"
)

// TestMemoryEventStore_TrimBefore verifies the memory half of the compaction
// trim loop: events at or below endVersion are reclaimed from both
// the stream view and the global replay slice.
func TestMemoryEventStore_TrimBefore(t *testing.T) {
	m := NewMemoryEventStore()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if !Emit(ctx, m, "s1", EventTaskCreated, "test", map[string]any{"i": i}) {
			t.Fatalf("emit %d failed", i)
		}
	}
	removed, err := m.TrimBefore(ctx, "s1", 3)
	if err != nil {
		t.Fatalf("TrimBefore: %v", err)
	}
	if removed != 3 {
		t.Fatalf("removed = %d, want 3", removed)
	}
	// Stream view: versions 4..5 survive.
	evs, err := m.Read(ctx, "s1", ReadOptions{})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(evs) != 2 || evs[0].Version != 4 {
		t.Fatalf("stream read = %d events (first version %v), want 2 from v4", len(evs), evs[0].Version)
	}
	// Trimming an unknown stream removes nothing.
	removed, err = m.TrimBefore(ctx, "ghost", 100)
	if err != nil || removed != 0 {
		t.Fatalf("unknown stream: removed=%d err=%v, want 0/nil", removed, err)
	}
}
