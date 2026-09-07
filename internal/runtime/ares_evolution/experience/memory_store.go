package experience

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryExperienceStore is an in-memory implementation of ExperienceStore.
// It is thread-safe and suitable for testing and development purposes.
type MemoryExperienceStore struct {
	mu      sync.RWMutex
	exps    []NormalizedExperience
	config  ExperienceStoreConfig
	indices *storeIndices
}

// storeIndices holds optional indices for faster queries.
type storeIndices struct {
	strategyIndex map[string][]int
	taskTypeIndex map[string][]int
}

// NewMemoryExperienceStore creates a new in-memory experience store.
// The store is thread-safe and supports concurrent access.
//
// Args:
//
//	cfg - configuration for the store.
//
// Returns:
//
//	*MemoryExperienceStore - the initialized store.
func NewMemoryExperienceStore(cfg ExperienceStoreConfig) *MemoryExperienceStore {
	store := &MemoryExperienceStore{
		exps:   make([]NormalizedExperience, 0),
		config: cfg,
	}

	if cfg.EnableIndexing {
		store.indices = &storeIndices{
			strategyIndex: make(map[string][]int),
			taskTypeIndex: make(map[string][]int),
		}
	}

	return store
}

// Append adds a normalized experience to the store.
// Returns an error if the experience is invalid or the store is full.
//
// Args:
//
//	ctx - timeout and cancellation context.
//	exp - the experience to store.
//
// Returns:
//
//	error - ErrInvalidExperience if validation fails, ErrStoreFull if at capacity.
func (s *MemoryExperienceStore) Append(ctx context.Context, exp NormalizedExperience) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := validateExperience(exp); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check capacity.
	if s.config.MaxSize > 0 && len(s.exps) >= s.config.MaxSize {
		return ErrStoreFull
	}

	// Append experience.
	s.exps = append(s.exps, exp)

	// Update indices if enabled.
	if s.indices != nil {
		s.updateIndices(exp, len(s.exps)-1)
	}

	return nil
}

// AppendBatch adds multiple experiences in a single operation.
// Returns an error if any experience is invalid or the store would exceed capacity.
//
// Args:
//
//	ctx - timeout and cancellation context.
//	exps - the experiences to store.
//
// Returns:
//
//	error - ErrInvalidExperience if validation fails, ErrStoreFull if would exceed capacity.
func (s *MemoryExperienceStore) AppendBatch(ctx context.Context, exps []NormalizedExperience) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if len(exps) == 0 {
		return nil
	}

	// Validate all experiences first.
	for _, exp := range exps {
		if err := validateExperience(exp); err != nil {
			return err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check capacity.
	if s.config.MaxSize > 0 && len(s.exps)+len(exps) > s.config.MaxSize {
		return ErrStoreFull
	}

	// Append all experiences.
	startIdx := len(s.exps)
	s.exps = append(s.exps, exps...)

	// Update indices if enabled.
	if s.indices != nil {
		for i, exp := range exps {
			s.updateIndices(exp, startIdx+i)
		}
	}

	return nil
}

// Query retrieves experiences filtered by strategy_id and time range.
// Returns experiences sorted by CreatedAt in descending order.
//
// Args:
//
//	ctx - timeout and cancellation context.
//	strategyID - the strategy identifier to filter by.
//	startTime - the start of the time range (inclusive).
//	endTime - the end of the time range (inclusive).
//
// Returns:
//
//	[]NormalizedExperience - matching experiences.
//	error - context cancellation error.
func (s *MemoryExperienceStore) Query(ctx context.Context, strategyID string, startTime, endTime time.Time) ([]NormalizedExperience, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []NormalizedExperience

	// Use index if available.
	if s.indices != nil {
		if idxs, ok := s.indices.strategyIndex[strategyID]; ok {
			for _, idx := range idxs {
				if idx < len(s.exps) {
					exp := s.exps[idx]
					if exp.CreatedAt.Compare(startTime) >= 0 && exp.CreatedAt.Compare(endTime) <= 0 {
						result = append(result, exp)
					}
				}
			}
		}
	} else {
		// Linear scan.
		for _, exp := range s.exps {
			if exp.StrategyID == strategyID &&
				exp.CreatedAt.Compare(startTime) >= 0 &&
				exp.CreatedAt.Compare(endTime) <= 0 {
				result = append(result, exp)
			}
		}
	}

	// Sort by CreatedAt descending.
	sort.Slice(result, func(i, j int) bool {
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})

	return result, nil
}

// QueryByTaskType retrieves experiences for a specific task type.
// Returns experiences sorted by Score in descending order.
//
// Args:
//
//	ctx - timeout and cancellation context.
//	taskType - the task type to filter by.
//	limit - maximum number of experiences to return (0 for no limit).
//
// Returns:
//
//	[]NormalizedExperience - matching experiences.
//	error - context cancellation error.
func (s *MemoryExperienceStore) QueryByTaskType(ctx context.Context, taskType string, limit int) ([]NormalizedExperience, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []NormalizedExperience

	// Use index if available.
	if s.indices != nil {
		if idxs, ok := s.indices.taskTypeIndex[taskType]; ok {
			for _, idx := range idxs {
				if idx < len(s.exps) {
					result = append(result, s.exps[idx])
				}
			}
		}
	} else {
		// Linear scan.
		for _, exp := range s.exps {
			if exp.TaskType == taskType {
				result = append(result, exp)
			}
		}
	}

	// Sort by Score descending.
	sort.Slice(result, func(i, j int) bool {
		return result[i].Score > result[j].Score
	})

	// Apply limit.
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}

	return result, nil
}

// GetStatistics returns aggregate statistics for a strategy.
// Statistics include: avg_score, success_rate, total_experiences.
//
// Args:
//
//	ctx - timeout and cancellation context.
//	strategyID - the strategy identifier to get statistics for.
//
// Returns:
//
//	map[string]float64 - statistical metrics.
//	error - context cancellation error.
func (s *MemoryExperienceStore) GetStatistics(ctx context.Context, strategyID string) (map[string]float64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var strategyExps []NormalizedExperience

	// Use index if available.
	if s.indices != nil {
		if idxs, ok := s.indices.strategyIndex[strategyID]; ok {
			for _, idx := range idxs {
				if idx < len(s.exps) {
					strategyExps = append(strategyExps, s.exps[idx])
				}
			}
		}
	} else {
		// Linear scan.
		for _, exp := range s.exps {
			if exp.StrategyID == strategyID {
				strategyExps = append(strategyExps, exp)
			}
		}
	}

	stats := make(map[string]float64)

	if len(strategyExps) == 0 {
		return stats, nil
	}

	// Calculate statistics.
	var totalScore float64
	var successCount int

	for _, exp := range strategyExps {
		totalScore += exp.Score
		if exp.Outcome == "success" {
			successCount++
		}
	}

	stats["total_experiences"] = float64(len(strategyExps))
	stats["avg_score"] = totalScore / float64(len(strategyExps))
	stats["success_rate"] = float64(successCount) / float64(len(strategyExps))

	return stats, nil
}

// GetTaskTypeStatistics returns aggregate statistics for a task type.
// Statistics include: avg_score, experience_count, success_rate.
//
// Args:
//
//	ctx - timeout and cancellation context.
//	taskType - the task type to get statistics for.
//
// Returns:
//
//	map[string]float64 - statistical metrics.
//	error - context cancellation error.
func (s *MemoryExperienceStore) GetTaskTypeStatistics(ctx context.Context, taskType string) (map[string]float64, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	var taskExps []NormalizedExperience

	if s.indices != nil {
		if idxs, ok := s.indices.taskTypeIndex[taskType]; ok {
			for _, idx := range idxs {
				if idx < len(s.exps) {
					taskExps = append(taskExps, s.exps[idx])
				}
			}
		}
	} else {
		for _, exp := range s.exps {
			if exp.TaskType == taskType {
				taskExps = append(taskExps, exp)
			}
		}
	}

	stats := make(map[string]float64)

	if len(taskExps) == 0 {
		return stats, nil
	}

	var totalScore float64
	var successCount int
	var uniqueStrategies int
	strategySeen := make(map[string]bool)

	for _, exp := range taskExps {
		totalScore += exp.Score
		if exp.Outcome == "success" {
			successCount++
		}
		if !strategySeen[exp.StrategyID] && exp.StrategyID != "" {
			strategySeen[exp.StrategyID] = true
			uniqueStrategies++
		}
	}

	stats["experience_count"] = float64(len(taskExps))
	stats["avg_score"] = totalScore / float64(len(taskExps))
	stats["success_rate"] = float64(successCount) / float64(len(taskExps))
	stats["unique_strategies"] = float64(uniqueStrategies)

	// Simple confidence based on sample count.
	n := float64(len(taskExps))
	if n > 100 {
		stats["confidence"] = 1.0
	} else {
		stats["confidence"] = n / 100.0
	}

	return stats, nil
}

// updateIndices updates the store indices with a new experience.
// Must be called with lock held.
func (s *MemoryExperienceStore) updateIndices(exp NormalizedExperience, idx int) {
	// Update strategy index.
	if exp.StrategyID != "" {
		s.indices.strategyIndex[exp.StrategyID] = append(
			s.indices.strategyIndex[exp.StrategyID],
			idx,
		)
	}

	// Update task type index.
	if exp.TaskType != "" {
		s.indices.taskTypeIndex[exp.TaskType] = append(
			s.indices.taskTypeIndex[exp.TaskType],
			idx,
		)
	}
}

// validateExperience validates that an experience has all required fields.
func validateExperience(exp NormalizedExperience) error {
	if exp.StrategyID == "" {
		return ErrInvalidExperience
	}
	if exp.TaskType == "" {
		return ErrInvalidExperience
	}
	if exp.ID == "" {
		return ErrInvalidExperience
	}
	return nil
}
