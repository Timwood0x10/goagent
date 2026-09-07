package aresrecovery

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDeterministicScorer_ScoreAgent_AllSuccess verifies that a perfect
// agent (all success, no retries/recoveries, fast) scores 1.0.
func TestDeterministicScorer_ScoreAgent_AllSuccess(t *testing.T) {
	scorer := NewDeterministicScorer()
	r := AgentResult{
		AgentID:     "a1",
		Success:     10,
		Fail:        0,
		Rate:        1.0,
		AvgLatency:  0,
		AvgRetries:  0,
		AvgRecovers: 0,
	}
	score := scorer.ScoreAgent(r)
	assert.InDelta(t, 1.0, score, 0.0001, "perfect agent must score 1.0")
}

// TestDeterministicScorer_ScoreAgent_AllFail verifies that an all-fail
// agent scores 0.0 (success rate = 0 dominates).
func TestDeterministicScorer_ScoreAgent_AllFail(t *testing.T) {
	scorer := NewDeterministicScorer()
	r := AgentResult{
		AgentID:     "a2",
		Success:     0,
		Fail:        10,
		Rate:        0.0,
		AvgLatency:  0,
		AvgRetries:  0,
		AvgRecovers: 0,
	}
	score := scorer.ScoreAgent(r)
	// success_rate=0 dominates (weight 0.70), but latency/retry/recover
	// components are 1.0 (zero values = best). So total = 0 + 0.15 + 0.10 + 0.05 = 0.30.
	assert.InDelta(t, 0.30, score, 0.01, "all-fail agent with 0 latency/retries/recovers scores 0.30 (non-success components)")
}

// TestDeterministicScorer_ScoreAgent_NoHistory verifies the neutral prior.
func TestDeterministicScorer_ScoreAgent_NoHistory(t *testing.T) {
	scorer := NewDeterministicScorer()
	r := AgentResult{
		AgentID: "a3",
	}
	score := scorer.ScoreAgent(r)
	assert.InDelta(t, 0.5, score, 0.0001, "no history must yield neutral 0.5")
}

// TestDeterministicScorer_Deterministic verifies that the same input
// always produces the same output (no randomness).
func TestDeterministicScorer_Deterministic(t *testing.T) {
	scorer := NewDeterministicScorer()
	r := AgentResult{
		AgentID:     "a4",
		Success:     7,
		Fail:        3,
		Rate:        0.7,
		AvgLatency:  5 * time.Second,
		AvgRetries:  1.5,
		AvgRecovers: 0.5,
	}
	score1 := scorer.ScoreAgent(r)
	score2 := scorer.ScoreAgent(r)
	assert.InDelta(t, score1, score2, 0.000001, "scorer must be deterministic")
}

// TestDeterministicScorer_DifferentOutcomesDifferentScores verifies that
// two different outcome distributions produce different scores (core
// assertion: score ordering must reflect real performance differences).
func TestDeterministicScorer_DifferentOutcomesDifferentScores(t *testing.T) {
	scorer := NewDeterministicScorer()

	// Agent A: 90% success, fast, no retries.
	good := AgentResult{
		Success: 9, Fail: 1,
		AvgLatency:  1 * time.Second,
		AvgRetries:  0,
		AvgRecovers: 0,
	}

	// Agent B: 20% success, slow, many retries.
	bad := AgentResult{
		Success: 2, Fail: 8,
		AvgLatency:  20 * time.Second,
		AvgRetries:  4,
		AvgRecovers: 2,
	}

	scoreGood := scorer.ScoreAgent(good)
	scoreBad := scorer.ScoreAgent(bad)
	assert.Greater(t, scoreGood, scoreBad,
		"better agent must score higher (good=%.4f, bad=%.4f)",
		scoreGood, scoreBad)
}

// TestDeterministicScorer_ScoreSnapshot_AggregatesAgents verifies the
// snapshot-level aggregation averages per-agent scores.
func TestDeterministicScorer_ScoreSnapshot_AggregatesAgents(t *testing.T) {
	scorer := NewDeterministicScorer()
	snap := AttributionSnapshot{
		PerAgent: []AgentResult{
			{Success: 10, Fail: 0, AvgLatency: 0}, // 1.0
			{Success: 0, Fail: 10, AvgLatency: 0}, // 0.0
		},
		PerCapability: []CapabilityResult{},
	}
	score := scorer.ScoreSnapshot(snap)
	// Agent 1 (all success, fast): 1.0. Agent 2 (all fail, fast): 0.30.
	// Average = (1.0 + 0.30) / 2 = 0.65.
	assert.InDelta(t, 0.65, score, 0.01, "snapshot must average agent scores (1.0 + 0.30) / 2")
}

// TestDeterministicScorer_ScoreSnapshot_EmptyReturnsNeutral verifies empty
// snapshot returns the neutral prior.
func TestDeterministicScorer_ScoreSnapshot_EmptyReturnsNeutral(t *testing.T) {
	scorer := NewDeterministicScorer()
	score := scorer.ScoreSnapshot(AttributionSnapshot{})
	assert.InDelta(t, 0.5, score, 0.0001)
}

// TestDeterministicScorer_ScoreAttribution_EndToEnd verifies the full
// pipeline: attribution → snapshot → score.
func TestDeterministicScorer_ScoreAttribution_EndToEnd(t *testing.T) {
	scorer := NewDeterministicScorer()
	attr := NewExecutionAttribution()

	// Record 3 successes and 1 failure for agent-1.
	attr.RecordWithMetrics("agent-1", "code", true, 2*time.Second, 0, 0)
	attr.RecordWithMetrics("agent-1", "code", true, 3*time.Second, 0, 0)
	attr.RecordWithMetrics("agent-1", "code", true, 1*time.Second, 1, 0)
	attr.RecordWithMetrics("agent-1", "code", false, 10*time.Second, 2, 1)

	score := scorer.ScoreAttribution(attr)
	// Should be between 0 and 1, and reflect 75% success rate.
	assert.True(t, score > 0.0 && score < 1.0, "score must be in (0,1), got %f", score)
	// 75% success × 0.70 = 0.525 from success alone; rest from latency/retry/recover
	// which are non-zero, so total > 0.525.
	assert.Greater(t, score, 0.5, "75%% success rate with moderate metrics should score > 0.5, got %f", score)
}

// TestDeterministicScorer_ReproducibleAcrossRuns verifies that the same
// attribution data fed twice produces identical scores (reproducibility).
func TestDeterministicScorer_ReproducibleAcrossRuns(t *testing.T) {
	scorer := NewDeterministicScorer()

	// Build identical attribution stores.
	build := func() *ExecutionAttribution {
		a := NewExecutionAttribution()
		a.RecordWithMetrics("agent-1", "code", true, 2*time.Second, 0, 0)
		a.RecordWithMetrics("agent-1", "code", true, 3*time.Second, 1, 0)
		a.RecordWithMetrics("agent-1", "code", false, 8*time.Second, 2, 1)
		a.RecordWithMetrics("agent-2", "code", true, 1*time.Second, 0, 0)
		a.RecordWithMetrics("agent-2", "code", false, 15*time.Second, 3, 2)
		return a
	}

	score1 := scorer.ScoreAttribution(build())
	score2 := scorer.ScoreAttribution(build())
	assert.InDelta(t, score1, score2, 0.000001, "scorer must be reproducible")
}

// TestDeterministicScorer_TwoDistributions_DifferentOrdering verifies:
// two different task distributions produce different score orderings, and
// the ordering is reproducible with a fixed seed (trivially true since the
// scorer has no randomness).
func TestDeterministicScorer_TwoDistributions_DifferentOrdering(t *testing.T) {
	scorer := NewDeterministicScorer()

	// Distribution 1: agent-1 excels (90% success, fast),
	// agent-2 struggles (30% success, slow).
	dist1A := AgentResult{
		Success: 9, Fail: 1,
		AvgLatency:  2 * time.Second,
		AvgRetries:  0.5,
		AvgRecovers: 0,
	}
	dist1B := AgentResult{
		Success: 3, Fail: 7,
		AvgLatency:  15 * time.Second,
		AvgRetries:  3,
		AvgRecovers: 1,
	}

	// Distribution 2: agent-1 struggles (30% success, slow),
	// agent-2 excels (90% success, fast).
	// (swapped roles)
	dist2A := dist1B // agent-1 now has the bad profile
	dist2B := dist1A // agent-2 now has the good profile

	score1A := scorer.ScoreAgent(dist1A)
	score1B := scorer.ScoreAgent(dist1B)

	// In distribution 1: agent-1 > agent-2.
	require.Greater(t, score1A, score1B,
		"dist1: agent-1 must outscore agent-2")

	// In distribution 2: agent-1 < agent-2 (swapped profiles).
	score2A := scorer.ScoreAgent(dist2A)
	score2B := scorer.ScoreAgent(dist2B)
	require.Less(t, score2A, score2B,
		"dist2: agent-1 must be outscored by agent-2")

	// The ordering is inverted between distributions.
	assert.NotEqual(t,
		score1A > score1B,
		score2A > score2B,
		"score ordering must flip between distributions")
}

// TestNormalizeLatency_Boundaries tests the latency normalization boundaries.
func TestNormalizeLatency_Boundaries(t *testing.T) {
	assert.InDelta(t, 1.0, normalizeLatency(0), 0.0001)
	assert.InDelta(t, 0.0, normalizeLatency(latencyCeiling), 0.0001)
	assert.InDelta(t, 0.0, normalizeLatency(2*latencyCeiling), 0.0001)
	// Midpoint: 15s out of 30s ceiling → 0.5
	assert.InDelta(t, 0.5, normalizeLatency(15*time.Second), 0.01)
}

// TestNormalizeCount_Boundaries tests the count normalization boundaries.
func TestNormalizeCount_Boundaries(t *testing.T) {
	assert.InDelta(t, 1.0, normalizeCount(0, retryCeiling), 0.0001)
	assert.InDelta(t, 0.0, normalizeCount(retryCeiling, retryCeiling), 0.0001)
	assert.InDelta(t, 0.0, normalizeCount(retryCeiling+1, retryCeiling), 0.0001)
	// 2.5 out of 5 → 0.5
	assert.InDelta(t, 0.5, normalizeCount(2.5, retryCeiling), 0.01)
}

// TestClamp01 tests the clamp helper.
func TestClamp01(t *testing.T) {
	assert.InDelta(t, 0.0, clamp01(-1), 0.0001)
	assert.InDelta(t, 0.5, clamp01(0.5), 0.0001)
	assert.InDelta(t, 1.0, clamp01(2), 0.0001)
	assert.InDelta(t, 0.0, clamp01(0), 0.0001)
	assert.InDelta(t, 1.0, clamp01(1), 0.0001)
}
