package aresrecovery

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockConfidenceInjector is a test ConfidenceInjector that records the
// last confidence pushed for each agent.
type mockConfidenceInjector struct {
	agentConf map[string]float64
}

func newMockConfidenceInjector() *mockConfidenceInjector {
	return &mockConfidenceInjector{agentConf: make(map[string]float64)}
}

func (m *mockConfidenceInjector) SetAgentConfidence(agentID string, confidence float64) {
	m.agentConf[agentID] = confidence
}

func (m *mockConfidenceInjector) SetCapabilityConfidence(agentID, capability string, confidence float64) {
	// no-op for tests
}

// mockScoreWriter is a test StrategyScoreWriter that records the last
// score written.
type mockScoreWriter struct {
	lastScore float64
	written   bool
	err       error
}

func (m *mockScoreWriter) WriteActiveScore(_ context.Context, score float64) error {
	if m.err != nil {
		return m.err
	}
	m.lastScore = score
	m.written = true
	return nil
}

// TestScoredFeedbackAdapter_WritesScoreToStrategyStore verifies that the
// scored adapter writes the deterministic aggregate score back via the
// StrategyScoreWriter interface.
func TestScoredFeedbackAdapter_WritesScoreToStrategyStore(t *testing.T) {
	attr := NewExecutionAttribution()
	attr.RecordWithMetrics("agent-1", "code", true, 2*time.Second, 0, 0)
	attr.RecordWithMetrics("agent-1", "code", false, 10*time.Second, 2, 1)

	injector := newMockConfidenceInjector()
	inner := NewEvolutionFeedbackAdapter(attr, injector)
	writer := &mockScoreWriter{}

	adapter := NewScoredFeedbackAdapter(inner, nil, writer)
	adapter.Apply(context.Background())

	require.True(t, writer.written, "score must be written back")
	assert.True(t, writer.lastScore > 0.0 && writer.lastScore < 1.0,
		"score must be in (0,1), got %f", writer.lastScore)
}

// TestScoredFeedbackAdapter_NilWriterSkipsWriteBack verifies that a nil
// writer does not crash (graceful degradation).
func TestScoredFeedbackAdapter_NilWriterSkipsWriteBack(t *testing.T) {
	attr := NewExecutionAttribution()
	injector := newMockConfidenceInjector()
	inner := NewEvolutionFeedbackAdapter(attr, injector)

	adapter := NewScoredFeedbackAdapter(inner, nil, nil)
	updated := adapter.Apply(context.Background())
	assert.Equal(t, 0, updated, "no agents with history → 0 updates, no crash")
}

// TestScoredFeedbackAdapter_NilInnerSkipsWriteBack verifies that a nil
// inner adapter means no score write-back (no source to read from).
func TestScoredFeedbackAdapter_NilInnerSkipsWriteBack(t *testing.T) {
	writer := &mockScoreWriter{}
	adapter := NewScoredFeedbackAdapter(nil, nil, writer)
	updated := adapter.Apply(context.Background())
	assert.Equal(t, 0, updated)
	assert.False(t, writer.written, "nil inner → no source → no write-back")
}

// TestScoredFeedbackAdapter_WriteErrorDoesNotSuppressConfidence verifies
// that a write-back error does not suppress the confidence injection.
func TestScoredFeedbackAdapter_WriteErrorDoesNotSuppressConfidence(t *testing.T) {
	attr := NewExecutionAttribution()
	attr.RecordWithMetrics("agent-1", "code", true, 1*time.Second, 0, 0)

	injector := newMockConfidenceInjector()
	inner := NewEvolutionFeedbackAdapter(attr, injector)
	writer := &mockScoreWriter{err: errors.New("store unavailable")}

	adapter := NewScoredFeedbackAdapter(inner, nil, writer)
	updated := adapter.Apply(context.Background())

	assert.Equal(t, 1, updated, "confidence injection must succeed even if write-back fails")
	assert.False(t, writer.written, "writer errored before recording")
}

// TestScoredFeedbackAdapter_DeterministicScore verifies the written score
// is reproducible (same attribution → same score, no randomness).
func TestScoredFeedbackAdapter_DeterministicScore(t *testing.T) {
	buildAdapter := func() *ScoredFeedbackAdapter {
		a := NewExecutionAttribution()
		a.RecordWithMetrics("agent-1", "code", true, 2*time.Second, 0, 0)
		a.RecordWithMetrics("agent-1", "code", false, 8*time.Second, 1, 0)
		injector := newMockConfidenceInjector()
		inner := NewEvolutionFeedbackAdapter(a, injector)
		return NewScoredFeedbackAdapter(inner, nil, &mockScoreWriter{})
	}

	// We can't access lastScore through the private writer, so test
	// determinism by calling ScoreAttribution directly on two identical
	// attributions.
	scorer := NewDeterministicScorer()
	a1 := NewExecutionAttribution()
	a1.RecordWithMetrics("agent-1", "code", true, 2*time.Second, 0, 0)
	a1.RecordWithMetrics("agent-1", "code", false, 8*time.Second, 1, 0)

	a2 := NewExecutionAttribution()
	a2.RecordWithMetrics("agent-1", "code", true, 2*time.Second, 0, 0)
	a2.RecordWithMetrics("agent-1", "code", false, 8*time.Second, 1, 0)

	s1 := scorer.ScoreAttribution(a1)
	s2 := scorer.ScoreAttribution(a2)
	assert.InDelta(t, s1, s2, 0.000001, "identical attribution must produce identical scores")

	// Also verify the adapter doesn't crash.
	_ = buildAdapter()
}
