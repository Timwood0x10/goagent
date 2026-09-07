// package engine - Human-in-the-loop (HITL) support for pausing and resuming workflow execution.
package engine

import (
	"context"
	"sync"
)

// InterruptPoint marks a step as requiring human approval before execution.
type InterruptPoint struct {
	StepID  string         `json:"step_id"`
	Message string         `json:"message"`
	Payload map[string]any `json:"payload,omitempty"`
}

// InterruptResult carries the human's decision.
type InterruptResult struct {
	Approved bool           `json:"approved"`
	Feedback string         `json:"feedback,omitempty"`
	Data     map[string]any `json:"data,omitempty"`
}

// InterruptHandler is called when execution reaches an interrupt point.
// It should block until the human provides input.
type InterruptHandler func(ctx context.Context, point *InterruptPoint) (*InterruptResult, error)

// InterruptStore persists interrupt state for crash recovery.
type InterruptStore interface {
	// Save persists an interrupt point so it can survive crashes.
	Save(ctx context.Context, executionID string, point *InterruptPoint) error

	// Load retrieves a previously saved interrupt result for the given step.
	Load(ctx context.Context, executionID string, stepID string) (*InterruptResult, error)

	// Delete removes the interrupt state for the given step.
	Delete(ctx context.Context, executionID string, stepID string) error

	// ListPending returns all pending interrupt points for an execution.
	ListPending(ctx context.Context, executionID string) ([]*InterruptPoint, error)

	// SaveResult stores a human-provided interrupt result for crash recovery.
	SaveResult(ctx context.Context, executionID string, stepID string, result *InterruptResult) error
}

// MemoryInterruptStore is an in-memory implementation of InterruptStore.
// It is safe for concurrent use.
type MemoryInterruptStore struct {
	mu      sync.RWMutex
	points  map[string]map[string]*InterruptPoint  // executionID -> stepID -> point
	results map[string]map[string]*InterruptResult // executionID -> stepID -> result
}

// NewMemoryInterruptStore creates a new MemoryInterruptStore.
func NewMemoryInterruptStore() *MemoryInterruptStore {
	return &MemoryInterruptStore{
		points:  make(map[string]map[string]*InterruptPoint),
		results: make(map[string]map[string]*InterruptResult),
	}
}

// Save persists an interrupt point for crash recovery.
func (s *MemoryInterruptStore) Save(ctx context.Context, executionID string, point *InterruptPoint) error {
	if s == nil {
		return ErrInterruptStoreNil
	}
	if point == nil {
		return ErrInterruptPointNil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.points[executionID] == nil {
		s.points[executionID] = make(map[string]*InterruptPoint)
	}
	s.points[executionID][point.StepID] = point

	return nil
}

// Load retrieves a previously saved interrupt result.
func (s *MemoryInterruptStore) Load(ctx context.Context, executionID string, stepID string) (*InterruptResult, error) {
	if s == nil {
		return nil, ErrInterruptStoreNil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	stepResults, ok := s.results[executionID]
	if !ok {
		return nil, ErrInterruptNotFound
	}

	result, ok := stepResults[stepID]
	if !ok {
		return nil, ErrInterruptNotFound
	}

	return result, nil
}

// Delete removes the interrupt state for the given step.
func (s *MemoryInterruptStore) Delete(ctx context.Context, executionID string, stepID string) error {
	if s == nil {
		return ErrInterruptStoreNil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if steps, ok := s.points[executionID]; ok {
		delete(steps, stepID)
		if len(steps) == 0 {
			delete(s.points, executionID)
		}
	}

	if steps, ok := s.results[executionID]; ok {
		delete(steps, stepID)
		if len(steps) == 0 {
			delete(s.results, executionID)
		}
	}

	return nil
}

// ListPending returns all pending interrupt points for an execution.
func (s *MemoryInterruptStore) ListPending(ctx context.Context, executionID string) ([]*InterruptPoint, error) {
	if s == nil {
		return nil, ErrInterruptStoreNil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	stepPoints, ok := s.points[executionID]
	if !ok {
		return nil, nil
	}

	pending := make([]*InterruptPoint, 0, len(stepPoints))
	for stepID, point := range stepPoints {
		// A point is pending if it has no corresponding result.
		if stepResults, exists := s.results[executionID]; exists {
			if _, hasResult := stepResults[stepID]; hasResult {
				continue
			}
		}
		pending = append(pending, point)
	}

	return pending, nil
}

// SaveResult stores a human-provided interrupt result for crash recovery.
func (s *MemoryInterruptStore) SaveResult(ctx context.Context, executionID string, stepID string, result *InterruptResult) error {
	if s == nil {
		return ErrInterruptStoreNil
	}
	if result == nil {
		return ErrInterruptPointNil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.results[executionID] == nil {
		s.results[executionID] = make(map[string]*InterruptResult)
	}
	s.results[executionID][stepID] = result

	return nil
}
