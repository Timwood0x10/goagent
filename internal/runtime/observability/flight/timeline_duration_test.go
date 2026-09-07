package flight

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestTimelineSummaryComputesPairDurations verifies the duration fix: a
// result event (tool.result / llm.result / agent.end) pairs with its start
// event so Summary's typeDuration returns the real elapsed time instead of
// zero (previously every duration was 0 because no event carried Duration).
func TestTimelineSummaryComputesPairDurations(t *testing.T) {
	tl := NewTimeline()
	base := time.Now()

	tl.Add(TimelineEvent{ID: "c1", AgentID: "a", Type: EventToolCall, StartAt: base})
	tl.Add(TimelineEvent{ID: "r1", AgentID: "a", Type: EventToolResult, StartAt: base.Add(2 * time.Second)})
	tl.Add(TimelineEvent{ID: "l1", AgentID: "a", Type: EventLLMCall, StartAt: base.Add(3 * time.Second)})
	tl.Add(TimelineEvent{ID: "lr1", AgentID: "a", Type: EventLLMResult, StartAt: base.Add(4 * time.Second)})

	s := tl.Summary()
	if s.ToolDuration != 2*time.Second {
		t.Fatalf("tool duration want 2s, got %v", s.ToolDuration)
	}
	if s.LLMDuration != 1*time.Second {
		t.Fatalf("llm duration want 1s, got %v", s.LLMDuration)
	}
	if s.TotalDuration != 4*time.Second {
		t.Fatalf("total duration want 4s, got %v", s.TotalDuration)
	}
}

// TestTimelinePairingIsPerAgent verifies pairing does not cross agents: a
// result event only pairs with a start event of the same agent.
func TestTimelinePairingIsPerAgent(t *testing.T) {
	tl := NewTimeline()
	base := time.Now()

	tl.Add(TimelineEvent{ID: "c1", AgentID: "a", Type: EventToolCall, StartAt: base})
	tl.Add(TimelineEvent{ID: "c2", AgentID: "b", Type: EventToolCall, StartAt: base.Add(time.Second)})
	tl.Add(TimelineEvent{ID: "r2", AgentID: "b", Type: EventToolResult, StartAt: base.Add(2 * time.Second)})

	evs := tl.Events()
	var aDur, bDur time.Duration
	for _, e := range evs {
		switch e.ID {
		case "c1":
			aDur = e.Duration
		case "c2":
			bDur = e.Duration
		}
	}
	require.Zero(t, aDur, "agent a's call must stay unpaired")
	require.Equal(t, time.Second, bDur, "agent b's call must pair with b's result")
}

// TestTimelinePairingPrefersParentID verifies a result event whose ParentID
// names a specific call pairs with exactly that call — even when another
// unpaired call for the same agent is more recent. Without the ParentID
// preference the out-of-order result would wrongly pair with the newest call.
func TestTimelinePairingPrefersParentID(t *testing.T) {
	tl := NewTimeline()
	base := time.Now()

	// Two overlapping calls on agent a; their results arrive out of order.
	tl.Add(TimelineEvent{ID: "call-1", AgentID: "a", Type: EventToolCall, StartAt: base})
	tl.Add(TimelineEvent{ID: "call-2", AgentID: "a", Type: EventToolCall, StartAt: base.Add(time.Second)})
	// call-1's result arrives last but carries its ParentID.
	tl.Add(TimelineEvent{ID: "res-1", ParentID: "call-1", AgentID: "a", Type: EventToolResult, StartAt: base.Add(4 * time.Second)})

	evs := tl.Events()
	var dur1, dur2 time.Duration
	for _, e := range evs {
		switch e.ID {
		case "call-1":
			dur1 = e.Duration
		case "call-2":
			dur2 = e.Duration
		}
	}
	require.Equal(t, 4*time.Second, dur1, "call-1 must pair with res-1 via ParentID")
	require.Zero(t, dur2, "call-2 must stay unpaired despite being more recent")
}

// TestTimelinePairingFallsBackWhenNoParentID verifies the fallback heuristic
// still works when the result carries no ParentID: it pairs with the most
// recent unpaired start event of the same agent (backward compatibility).
func TestTimelinePairingFallsBackWhenNoParentID(t *testing.T) {
	tl := NewTimeline()
	base := time.Now()

	tl.Add(TimelineEvent{ID: "call-1", AgentID: "a", Type: EventToolCall, StartAt: base})
	tl.Add(TimelineEvent{ID: "res-1", AgentID: "a", Type: EventToolResult, StartAt: base.Add(2 * time.Second)})

	evs := tl.Events()
	var dur time.Duration
	for _, e := range evs {
		if e.ID == "call-1" {
			dur = e.Duration
		}
	}
	require.Equal(t, 2*time.Second, dur, "fallback must pair without ParentID")
}

// TestTimelinePairingUnmatchedParentIDFallsBack verifies a ParentID that names
// an unknown/absent start event falls back to the recent-unpaired heuristic
// instead of silently dropping the pairing.
func TestTimelinePairingUnmatchedParentIDFallsBack(t *testing.T) {
	tl := NewTimeline()
	base := time.Now()

	tl.Add(TimelineEvent{ID: "call-1", AgentID: "a", Type: EventToolCall, StartAt: base})
	tl.Add(TimelineEvent{ID: "res-1", ParentID: "call-gone", AgentID: "a", Type: EventToolResult, StartAt: base.Add(2 * time.Second)})

	evs := tl.Events()
	var dur time.Duration
	for _, e := range evs {
		if e.ID == "call-1" {
			dur = e.Duration
		}
	}
	require.Equal(t, 2*time.Second, dur, "unmatched ParentID must fall back to recent-unpaired pairing")
}
