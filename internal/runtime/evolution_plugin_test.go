package runtime

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockStrategyProvider returns a fixed recommendation for testing.
type mockStrategyProvider struct {
	rec *RuntimeRecommendation
	err error
}

func (m *mockStrategyProvider) GetRecommendation(_ context.Context) (*RuntimeRecommendation, error) {
	return m.rec, m.err
}

// mockOutcomeRecorder records the last outcome for testing.
type mockOutcomeRecorder struct {
	last    *ExecutionOutcome
	callErr error
}

func (m *mockOutcomeRecorder) RecordOutcome(_ context.Context, outcome ExecutionOutcome) error {
	m.last = &outcome
	return m.callErr
}

func TestEvolutionPlugin_Name(t *testing.T) {
	p := NewEvolutionPlugin("test-evo", nil, nil)
	assert.Equal(t, "test-evo", p.Name())

	p2 := NewEvolutionPlugin("", nil, nil)
	assert.Equal(t, "evolution", p2.Name())
}

func TestEvolutionPlugin_Capabilities(t *testing.T) {
	p := NewEvolutionPlugin("test", nil, nil)
	assert.Contains(t, p.Capabilities(), CapEvolution)
}

func TestEvolutionPlugin_StartStop(t *testing.T) {
	p := NewEvolutionPlugin("test", nil, nil)
	assert.NoError(t, p.Start(context.Background(), nil))
	assert.NoError(t, p.Stop(context.Background()))
}

func TestEvolutionPlugin_RecommendWithoutProvider(t *testing.T) {
	p := NewEvolutionPlugin("test", nil, nil)
	rec, err := p.Recommend(context.Background(), ExecutionState{})
	// A nil provider is a valid "evolution disabled" state, not an error.
	assert.NoError(t, err, "nil provider must be a no-op, not an error")
	assert.Nil(t, rec, "nil provider must return a nil recommendation")
}

func TestEvolutionPlugin_RecommendFromProvider(t *testing.T) {
	expected := &RuntimeRecommendation{
		PreferredAgent: "agent-alpha",
		Confidence:     0.85,
		MutationHint:   "increase_temperature",
	}
	provider := &mockStrategyProvider{rec: expected}
	p := NewEvolutionPlugin("test", provider, nil)

	rec, err := p.Recommend(context.Background(), ExecutionState{})
	assert.NoError(t, err)
	require.NotNil(t, rec)
	assert.Equal(t, "agent-alpha", rec.PreferredAgent)
	assert.Equal(t, 0.85, rec.Confidence)
}

func TestEvolutionPlugin_RecommendReturnsCopy(t *testing.T) {
	expected := &RuntimeRecommendation{PreferredAgent: "a", Confidence: 1.0}
	provider := &mockStrategyProvider{rec: expected}
	p := NewEvolutionPlugin("test", provider, nil)

	rec1, _ := p.Recommend(context.Background(), ExecutionState{})
	rec2, _ := p.Recommend(context.Background(), ExecutionState{})
	assert.NotSame(t, rec1, rec2, "each call should return a new copy")
	assert.Equal(t, rec1.PreferredAgent, rec2.PreferredAgent)
}

func TestEvolutionPlugin_RecommendUsesCache(t *testing.T) {
	var callCount atomic.Int32
	provider := &mockStrategyProvider{
		rec: &RuntimeRecommendation{PreferredAgent: "a", Confidence: 1.0},
	}
	// Wrap provider to count calls.
	wrapped := &strategyProviderWithCounter{inner: provider, count: &callCount}
	p := NewEvolutionPlugin("test", wrapped, nil, WithCacheTTL(time.Minute))

	_, _ = p.Recommend(context.Background(), ExecutionState{})
	_, _ = p.Recommend(context.Background(), ExecutionState{})
	_, _ = p.Recommend(context.Background(), ExecutionState{})

	assert.Equal(t, int32(1), callCount.Load(), "provider should be called once, then cached")
}

func TestEvolutionPlugin_RecommendCacheExpiry(t *testing.T) {
	var callCount atomic.Int32
	provider := &mockStrategyProvider{
		rec: &RuntimeRecommendation{PreferredAgent: "a", Confidence: 1.0},
	}
	wrapped := &strategyProviderWithCounter{inner: provider, count: &callCount}
	p := NewEvolutionPlugin("test", wrapped, nil, WithCacheTTL(50*time.Millisecond))

	_, _ = p.Recommend(context.Background(), ExecutionState{})
	time.Sleep(60 * time.Millisecond)
	_, _ = p.Recommend(context.Background(), ExecutionState{})

	assert.Equal(t, int32(2), callCount.Load(), "cache should expire after TTL")
}

func TestEvolutionPlugin_RecommendProviderError(t *testing.T) {
	provider := &mockStrategyProvider{err: errors.New("store unavailable")}
	p := NewEvolutionPlugin("test", provider, nil)

	_, err := p.Recommend(context.Background(), ExecutionState{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "store unavailable")
}

func TestEvolutionPlugin_RecordOutcomeWithoutRecorder(t *testing.T) {
	p := NewEvolutionPlugin("test", nil, nil)
	err := p.RecordOutcome(context.Background(), ExecutionOutcome{ExecutionID: "e1"})
	assert.NoError(t, err)
}

func TestEvolutionPlugin_RecordOutcome(t *testing.T) {
	recorder := &mockOutcomeRecorder{}
	p := NewEvolutionPlugin("test", nil, recorder)

	outcome := ExecutionOutcome{
		ExecutionID: "e1",
		TotalSteps:  5,
		FailedSteps: 1,
	}
	err := p.RecordOutcome(context.Background(), outcome)
	assert.NoError(t, err)
	require.NotNil(t, recorder.last)
	assert.Equal(t, "e1", recorder.last.ExecutionID)
	assert.Equal(t, 5, recorder.last.TotalSteps)
}

func TestEvolutionPlugin_RecordOutcomeError(t *testing.T) {
	recorder := &mockOutcomeRecorder{callErr: errors.New("db full")}
	p := NewEvolutionPlugin("test", nil, recorder)

	err := p.RecordOutcome(context.Background(), ExecutionOutcome{ExecutionID: "e1"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "db full")
}

func TestEvolutionPlugin_StopClearsCache(t *testing.T) {
	provider := &mockStrategyProvider{
		rec: &RuntimeRecommendation{PreferredAgent: "a", Confidence: 1.0},
	}
	p := NewEvolutionPlugin("test", provider, nil)

	_, _ = p.Recommend(context.Background(), ExecutionState{})
	_ = p.Stop(context.Background())

	// After Stop, the cache is cleared but provider still works.
	rec, err := p.Recommend(context.Background(), ExecutionState{})
	assert.NoError(t, err)
	require.NotNil(t, rec)
	assert.Equal(t, "a", rec.PreferredAgent)
}

func TestEvolutionPlugin_NilProviderAfterConstruct(t *testing.T) {
	p := NewEvolutionPlugin("test", nil, nil)

	// Calling with nil provider should not panic and should not error.
	rec, err := p.Recommend(context.Background(), ExecutionState{})
	assert.NoError(t, err, "nil provider must be a no-op, not an error")
	assert.Nil(t, rec, "nil provider must return a nil recommendation")
}

// TestEvolutionPlugin_ProviderReturnsNilNil verifies that a provider returning
// (nil, nil) is treated as "no recommendation available" (success, empty) — not
// as an error. This is the documented StrategyProvider contract; previously the
// plugin misjudged a legitimate empty recommendation as a failure.
func TestEvolutionPlugin_ProviderReturnsNilNil(t *testing.T) {
	provider := &mockStrategyProvider{rec: nil, err: nil}
	p := NewEvolutionPlugin("test", provider, nil)

	rec, err := p.Recommend(context.Background(), ExecutionState{})
	assert.NoError(t, err, "nil,nil from provider must not be an error")
	assert.Nil(t, rec, "nil recommendation must be returned as-is")

	// A second call must still succeed and not surface a cached error.
	rec2, err2 := p.Recommend(context.Background(), ExecutionState{})
	assert.NoError(t, err2)
	assert.Nil(t, rec2)
}

// TestEvolutionPlugin_NilProviderVsProviderError verifies the plugin
// distinguishes "no provider configured" (no-op, no error) from a real provider
// error (surfaced). Only a non-nil error is treated as a failure.
func TestEvolutionPlugin_NilProviderVsProviderError(t *testing.T) {
	// No provider: no error.
	nilP := NewEvolutionPlugin("test", nil, nil)
	_, err := nilP.Recommend(context.Background(), ExecutionState{})
	assert.NoError(t, err)

	// Provider returns an error: surfaced.
	errP := NewEvolutionPlugin("test", &mockStrategyProvider{err: errors.New("boom")}, nil)
	_, err = errP.Recommend(context.Background(), ExecutionState{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

// strategyProviderWithCounter wraps a StrategyProvider and counts calls.
type strategyProviderWithCounter struct {
	inner StrategyProvider
	count *atomic.Int32
}

func (w *strategyProviderWithCounter) GetRecommendation(ctx context.Context) (*RuntimeRecommendation, error) {
	w.count.Add(1)
	return w.inner.GetRecommendation(ctx)
}
