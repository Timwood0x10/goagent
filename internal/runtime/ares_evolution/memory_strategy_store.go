// Package evolution provides autonomous evolution system components.
package evolution

import (
	"context"
	"errors"
	"sync"

	"github.com/Timwood0x10/ares/internal/logger"
)

var esLog = logger.New("strategy_store")

// MemoryStrategyStore is an in-memory implementation of StrategyStore.
// It requires no external database and is safe for concurrent use, making it
// suitable for the default deployment where no persistent store is configured.
type MemoryStrategyStore struct {
	mu         sync.RWMutex
	active     *Strategy
	history    []*Strategy
	maxHistory int
}

// NewMemoryStrategyStore creates an in-memory strategy store.
// A non-positive maxHistory retains unlimited history.
//
// Args:
//
//	maxHistory - maximum number of historical strategies to retain.
//
// Returns:
//
//	*MemoryStrategyStore - the configured store.
func NewMemoryStrategyStore(maxHistory int) *MemoryStrategyStore {
	return &MemoryStrategyStore{
		maxHistory: maxHistory,
	}
}

// GetActive returns the currently deployed strategy.
// Returns nil (and no error) if no strategy has been stored yet.
func (s *MemoryStrategyStore) GetActive(_ context.Context) (*Strategy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return dupStrategy(s.active), nil
}

// SetActive persists a strategy as the active deployment.
//
// Args:
//
//	ctx - operation context for cancellation.
//	strategy - the strategy to persist (must not be nil).
//
// Returns:
//
//	error - non-nil if strategy is nil.
func (s *MemoryStrategyStore) SetActive(ctx context.Context, strategy *Strategy) error {
	if strategy == nil {
		return errors.New("strategy must not be nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.active = dupStrategy(strategy)
	s.history = append(s.history, dupStrategy(strategy))
	if s.maxHistory > 0 && len(s.history) > s.maxHistory {
		s.history = s.history[len(s.history)-s.maxHistory:]
	}

	esLog.Info(ctx, "strategy set active",
		"strategy_id", strategy.ID,
		"version", strategy.Version,
		"score", strategy.Score,
	)
	return nil
}

// GetHistory returns the last n strategies for the given strategy ID,
// ordered by version descending (newest first).
//
// Args:
//
//	ctx - operation context for cancellation.
//	id - the strategy identifier to filter by.
//	n - maximum number of entries to return (0 = all matched).
//
// Returns:
//
//	[]*Strategy - matched history entries, newest first.
//	error - always nil for this in-memory implementation.
func (s *MemoryStrategyStore) GetHistory(_ context.Context, id string, n int) ([]*Strategy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	matched := make([]*Strategy, 0, len(s.history))
	for _, st := range s.history {
		if st.ID == id {
			matched = append(matched, dupStrategy(st))
		}
	}

	// Reverse so newest (appended last) comes first.
	for i, j := 0, len(matched)-1; i < j; i, j = i+1, j-1 {
		matched[i], matched[j] = matched[j], matched[i]
	}

	if n > 0 && len(matched) > n {
		matched = matched[:n]
	}
	return matched, nil
}

// dupStrategy returns a deep copy of a strategy to prevent external mutation
// of internally retained state. Returns nil for a nil input.
func dupStrategy(s *Strategy) *Strategy {
	if s == nil {
		return nil
	}
	params := make(map[string]any, len(s.Params))
	for k, v := range s.Params {
		params[k] = v
	}
	return &Strategy{
		ID:                   s.ID,
		Name:                 s.Name,
		Version:              s.Version,
		Params:               params,
		ParentID:             s.ParentID,
		PromptTemplate:       s.PromptTemplate,
		StrategyMutationType: s.StrategyMutationType,
		MutationDesc:         s.MutationDesc,
		Score:                s.Score,
		CreatedAt:            s.CreatedAt,
	}
}

// Ensure MemoryStrategyStore implements StrategyStore.
var _ StrategyStore = (*MemoryStrategyStore)(nil)
