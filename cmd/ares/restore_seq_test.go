package main

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/fabric/task"
)

// TestMaxRestoredTaskSeq locks the extraction contract for every
// counter-derived task-ID family the seeder must dominate after a durable
// restore (see maxRestoredTaskSeq for the family list).
func TestMaxRestoredTaskSeq(t *testing.T) {
	tests := []struct {
		name string
		ids  []string
		want int64
	}{
		{"empty fabric", nil, 0},
		{"no counter derived ids", []string{"uuid-550e8400e29b", "step:root"}, 0},
		{"peer plan submissions", []string{"peer-plan-2", "peer-plan-7", "peer-plan-1"}, 7},
		{"session node tasks", []string{"sess/sess-auto-3/d0/ares#1", "sess/sess-auto-11/d1/tool#2"}, 11},
		{"syscall task ids", []string{"task-ares/plan-1", "task-tool/web-search-4"}, 4},
		{"plan loop round tasks", []string{"plan-agent-A-5/r1#step"}, 5},
		{"mixed families take the max", []string{"peer-plan-2", "sess/sess-auto-3/d0/ares#1", "task-ares/plan-9"}, 9},
		{"non numeric tail ignored", []string{"peer-plan-abc", "peer-plan-"}, 0},
		{"zero tail ignored", []string{"peer-plan-0"}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, maxRestoredTaskSeq(tc.ids))
		})
	}
}

// TestSeedPeerTaskSeqPastRestoredIDs reproduces the restart collision the
// seeder exists to prevent: a fresh process mints peer-plan-N from a reset
// counter while the restored fabric still holds the previous boot's
// peer-plan-N. Without the seed the next submission fails Create with
// ErrTaskExists; with it the minted ID is strictly greater than every
// restored counter value, so the exact Create call submitPeerTask makes
// succeeds.
func TestSeedPeerTaskSeqPastRestoredIDs(t *testing.T) {
	fabric := taskfabric.NewFabric()
	restored := []string{
		"peer-plan-2",
		"peer-plan-4",
		"sess/sess-auto-3/d0/ares#1",
		"task-ares/plan-9",
	}
	for _, id := range restored {
		require.NoError(t, fabric.Create(&taskfabric.Task{ID: id, Capability: "ares/plan"}))
	}

	seedPeerTaskSeq(maxRestoredTaskSeq(fabric.IDs()))

	// The sequence must now dominate every restored peer-plan N (4), even
	// though the max came from the task- family (9) — over-seeding only
	// skips values, it never under-seeds a family.
	require.GreaterOrEqual(t, peerTaskSeq.Load(), int64(9),
		"sequence must dominate the max restored counter value")

	// The next minted root-task ID must not collide with any restored task.
	next := fmt.Sprintf("peer-plan-%d", peerTaskSeq.Add(1))
	require.NoError(t, fabric.Create(&taskfabric.Task{ID: next, Capability: "ares/plan"}),
		"next minted peer-plan ID must not collide with restored tasks")
}

// TestSeedPeerTaskSeqGrowOnly locks the grow-only contract: a restore whose
// max N is below the live sequence (fresh store, or a busier process) must
// never move the counter backwards.
func TestSeedPeerTaskSeqGrowOnly(t *testing.T) {
	before := peerTaskSeq.Load()
	seedPeerTaskSeq(before - 10)
	require.Equal(t, before, peerTaskSeq.Load(), "seed below the current value must be a no-op")
}
