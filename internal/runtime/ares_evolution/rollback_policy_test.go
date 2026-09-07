package evolution

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// --- RollbackPolicy tests ---

func TestNewRollbackPolicy_Defaults(t *testing.T) {
	p := NewRollbackPolicy()
	assert.NotNil(t, p)
	assert.Empty(t, p.ScoreHistory())
	assert.Equal(t, 0.15, p.degradationThreshold)
	assert.Equal(t, 5, p.windowSize)
	assert.Equal(t, 3, p.minSamples)
}

func TestNewRollbackPolicy_WithOptions(t *testing.T) {
	p := NewRollbackPolicy(
		WithDegradationThreshold(0.2),
		WithRollbackWindowSize(10),
		WithMinRollbackSamples(5),
	)
	assert.Equal(t, 0.2, p.degradationThreshold)
	assert.Equal(t, 10, p.windowSize)
	assert.Equal(t, 5, p.minSamples)
}

func TestRollbackPolicy_Evaluate_NoData(t *testing.T) {
	p := NewRollbackPolicy()
	decision := p.Evaluate()
	assert.False(t, decision.ShouldRollback)
	assert.Equal(t, "no score data available", decision.Reason)
}

func TestRollbackPolicy_Evaluate_InsufficientSamples(t *testing.T) {
	p := NewRollbackPolicy(WithMinRollbackSamples(3))
	p.RecordScore(1, 100.0)
	p.RecordScore(2, 95.0)

	decision := p.Evaluate()
	assert.False(t, decision.ShouldRollback)
	assert.Contains(t, decision.Reason, "insufficient samples")
}

func TestRollbackPolicy_Evaluate_StableScores(t *testing.T) {
	p := NewRollbackPolicy(
		WithDegradationThreshold(10.0), // High threshold so 0.5 is within
		WithMinRollbackSamples(3),
	)
	p.RecordScore(1, 100.0)
	p.RecordScore(2, 100.0)
	p.RecordScore(3, 99.5)

	// Degradation = reference(100) - current(99.5) = 0.5 < 10.0 threshold
	decision := p.Evaluate()
	assert.False(t, decision.ShouldRollback)
	assert.Contains(t, decision.Reason, "within threshold")
}

func TestRollbackPolicy_Evaluate_SuddenDrop(t *testing.T) {
	p := NewRollbackPolicy(
		WithDegradationThreshold(0.15),
		WithMinRollbackSamples(3),
	)
	p.RecordScore(1, 100.0)
	p.RecordScore(2, 100.0)
	p.RecordScore(3, 80.0) // Sudden drop > 0.15

	decision := p.Evaluate()
	assert.True(t, decision.ShouldRollback)
	assert.Contains(t, decision.Reason, "sudden score drop")
	assert.Equal(t, 20.0, decision.Degradation)
	assert.Equal(t, "immediate rollback recommended", decision.RecommendedAction)
}

func TestRollbackPolicy_Evaluate_GradualDecline(t *testing.T) {
	p := NewRollbackPolicy(
		WithDegradationThreshold(0.15),
		WithRollbackWindowSize(5),
		WithMinRollbackSamples(3),
	)
	// Simulate gradual decline over window.
	p.RecordScore(1, 100.0)
	p.RecordScore(2, 98.0)
	p.RecordScore(3, 96.0)
	p.RecordScore(4, 94.0)
	p.RecordScore(5, 92.0)

	decision := p.Evaluate()
	assert.True(t, decision.ShouldRollback)
	assert.Contains(t, decision.Reason, "gradual degradation")
	assert.Equal(t, "rollback to previous stable strategy", decision.RecommendedAction)
}

func TestRollbackPolicy_Reset(t *testing.T) {
	p := NewRollbackPolicy()
	p.RecordScore(1, 100.0)
	p.RecordScore(2, 95.0)
	assert.Len(t, p.ScoreHistory(), 2)

	p.Reset()
	assert.Empty(t, p.ScoreHistory())

	// After reset, evaluate should return no-data
	decision := p.Evaluate()
	assert.False(t, decision.ShouldRollback)
	assert.Equal(t, "no score data available", decision.Reason)
}

func TestRollbackPolicy_WindowTrimming(t *testing.T) {
	p := NewRollbackPolicy(WithRollbackWindowSize(3))
	// Record more scores than window size.
	p.RecordScore(1, 100.0)
	p.RecordScore(2, 98.0)
	p.RecordScore(3, 96.0)
	p.RecordScore(4, 94.0)

	history := p.ScoreHistory()
	assert.Len(t, history, 3) // Window size 3
	assert.Equal(t, 2, history[0].Generation)
	assert.Equal(t, 4, history[2].Generation)
}

func TestRollbackPolicy_ConcurrentAccess(t *testing.T) {
	p := NewRollbackPolicy()
	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(gen int) {
			defer wg.Done()
			p.RecordScore(gen, float64(100-gen))
			p.Evaluate()
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent access timeout - possible deadlock")
	}
}

// --- ActiveStrategyManager tests ---

// mockStrategyStore implements StrategyStore for testing.
type mockStrategyStore struct {
	mu      sync.Mutex
	active  *Strategy
	history []*Strategy
}

func newMockStrategyStore() *mockStrategyStore {
	return &mockStrategyStore{}
}

func (m *mockStrategyStore) GetActive(_ context.Context) (*Strategy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil {
		return nil, errors.New("no active strategy")
	}
	clone := *m.active
	return &clone, nil
}

func (m *mockStrategyStore) SetActive(_ context.Context, s *Strategy) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	clone := *s
	m.active = &clone
	m.history = append([]*Strategy{&clone}, m.history...)
	return nil
}

func (m *mockStrategyStore) GetHistory(_ context.Context, id string, n int) ([]*Strategy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n > len(m.history) {
		n = len(m.history)
	}
	result := make([]*Strategy, n)
	for i := 0; i < n; i++ {
		clone := *m.history[i]
		result[i] = &clone
	}
	return result, nil
}

func TestNewActiveStrategyManager_Validation(t *testing.T) {
	// Nil store should error
	_, err := NewActiveStrategyManager(nil, NewRollbackPolicy())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "strategy store must not be nil")
}

func TestNewActiveStrategyManager_NilRollbackPolicy(t *testing.T) {
	store := newMockStrategyStore()
	m, err := NewActiveStrategyManager(store, nil)
	require.NoError(t, err)
	assert.NotNil(t, m.RollbackPolicy())
}

func TestActiveStrategyManager_Deploy(t *testing.T) {
	store := newMockStrategyStore()
	m, err := NewActiveStrategyManager(store, NewRollbackPolicy())
	require.NoError(t, err)

	strategy := &mutation.Strategy{
		ID:      "strategy-1",
		Version: 1,
		Score:   95.0,
		Params:  map[string]any{"temperature": 0.7},
	}

	err = m.Deploy(context.Background(), strategy)
	require.NoError(t, err)

	// Current should be set
	current := m.Current()
	require.NotNil(t, current)
	assert.Equal(t, "strategy-1", current.ID)

	// Store should have the strategy
	active, err := store.GetActive(context.Background())
	require.NoError(t, err)
	require.NotNil(t, active)
	assert.Equal(t, "strategy-1", active.ID)
}

func TestActiveStrategyManager_Deploy_NilStrategy(t *testing.T) {
	store := newMockStrategyStore()
	m, _ := NewActiveStrategyManager(store, NewRollbackPolicy())

	err := m.Deploy(context.Background(), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "strategy must not be nil")
}

func TestActiveStrategyManager_Rollback(t *testing.T) {
	store := newMockStrategyStore()
	m, _ := NewActiveStrategyManager(store, NewRollbackPolicy())

	// Deploy first strategy
	s1 := &mutation.Strategy{ID: "strategy-1", Version: 1, Score: 90.0}
	err := m.Deploy(context.Background(), s1)
	require.NoError(t, err)

	// Deploy second strategy
	s2 := &mutation.Strategy{ID: "strategy-2", Version: 2, Score: 95.0}
	err = m.Deploy(context.Background(), s2)
	require.NoError(t, err)

	// Rollback should return the previous strategy (s1)
	rolledBack, err := m.Rollback(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "strategy-1", rolledBack.ID)

	// Current should be the rolled-back strategy
	current := m.Current()
	require.NotNil(t, current)
	assert.Equal(t, "strategy-1", current.ID)
}

func TestActiveStrategyManager_Rollback_NoPrevious(t *testing.T) {
	store := newMockStrategyStore()
	m, _ := NewActiveStrategyManager(store, NewRollbackPolicy())

	_, err := m.Rollback(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no previous strategy available for rollback")
}

func TestActiveStrategyManager_RecordScore(t *testing.T) {
	store := newMockStrategyStore()
	m, _ := NewActiveStrategyManager(store, NewRollbackPolicy())

	m.RecordScore(1, 100.0)
	m.RecordScore(2, 95.0)
	m.RecordScore(3, 90.0)

	// Should trigger rollback due to gradual decline.
	decision := m.RollbackPolicy().Evaluate()
	assert.True(t, decision.ShouldRollback)
}

func TestActiveStrategyManager_ConcurrentAccess(t *testing.T) {
	store := newMockStrategyStore()
	m, _ := NewActiveStrategyManager(store, NewRollbackPolicy())

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			s := &mutation.Strategy{ID: string(rune('A' + id)), Version: id, Score: float64(90 + id)}
			_ = m.Deploy(context.Background(), s)
			m.Current()
			m.Previous()
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent access timeout - possible deadlock")
	}
}

func TestActiveStrategyManager_Previous(t *testing.T) {
	store := newMockStrategyStore()
	m, _ := NewActiveStrategyManager(store, NewRollbackPolicy())

	// Initially, previous should be nil
	assert.Nil(t, m.Previous())

	s1 := &mutation.Strategy{ID: "s1", Version: 1}
	require.NoError(t, m.Deploy(context.Background(), s1))

	// After first deploy, previous should still be nil
	assert.Nil(t, m.Previous())

	s2 := &mutation.Strategy{ID: "s2", Version: 2}
	require.NoError(t, m.Deploy(context.Background(), s2))

	// After second deploy, previous should be s1
	prev := m.Previous()
	require.NotNil(t, prev)
	assert.Equal(t, "s1", prev.ID)
}
