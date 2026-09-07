// Package experience provides evidence aggregation for the GA/Memory/Tool
// fusion system. This file implements EvidenceAggregator that aggregates
// NormalizedExperience values from ExperienceStore into multi-dimensional
// Evidence for strategy evaluation.
package experience

import (
	"context"
	"errors"
	"sync"
	"time"
)

// EvidenceAggregator defines the interface for aggregating normalized
// experiences into multi-dimensional Evidence for strategy evaluation.
type EvidenceAggregator interface {
	// Aggregate aggregates all experiences for a specific strategy.
	// Returns Evidence containing statistical summaries.
	//
	// Args:
	//   ctx - timeout and cancellation context.
	//   strategyID - the strategy identifier to aggregate.
	//
	// Returns:
	//   Evidence - aggregated statistics, empty if no experiences found.
	//   error - context cancellation or storage error.
	Aggregate(ctx context.Context, strategyID string) (Evidence, error)

	// AggregateByTaskType aggregates experiences for a specific task type.
	// Returns Evidence containing statistical summaries across all strategies.
	//
	// Args:
	//   ctx - timeout and cancellation context.
	//   taskType - the task type to aggregate.
	//
	// Returns:
	//   Evidence - aggregated statistics, empty if no experiences found.
	//   error - context cancellation or storage error.
	AggregateByTaskType(ctx context.Context, taskType string) (Evidence, error)

	// AggregateByTimeWindow aggregates experiences within a time window.
	// Supported window values: "hourly", "daily", "weekly".
	//
	// Args:
	//   ctx - timeout and cancellation context.
	//   strategyID - the strategy identifier to aggregate.
	//   window - the time window type ("hourly", "daily", "weekly").
	//
	// Returns:
	//   Evidence - aggregated statistics for the current time window.
	//   error - context cancellation, invalid window, or storage error.
	AggregateByTimeWindow(ctx context.Context, strategyID, window string) (Evidence, error)

	// RefreshAll refreshes all cached evidence aggregates.
	// This should be called periodically to update stale evidence.
	//
	// Args:
	//   ctx - timeout and cancellation context.
	//
	// Returns:
	//   error - context cancellation or storage error.
	RefreshAll(ctx context.Context) error
}

// AggregatorConfig holds configuration for the evidence aggregator.
type AggregatorConfig struct {
	// MinSampleCount is the minimum sample count for valid confidence.
	// Below this threshold, confidence is scaled down.
	// Default: 10.
	MinSampleCount int64

	// MaxSampleCount is the maximum sample count for full confidence.
	// Above this threshold, confidence saturates at 1.0.
	// Default: 1000.
	MaxSampleCount int64

	// CacheTTL is the time-to-live for cached evidence in seconds.
	// Cached evidence older than this is considered stale.
	// Default: 300 (5 minutes).
	CacheTTLSeconds int

	// EnableCache enables evidence caching to avoid repeated computation.
	// Default: true.
	EnableCache bool
}

// DefaultAggregatorConfig returns an AggregatorConfig with sensible defaults.
func DefaultAggregatorConfig() *AggregatorConfig {
	return &AggregatorConfig{
		MinSampleCount:  10,
		MaxSampleCount:  1000,
		CacheTTLSeconds: 300,
		EnableCache:     true,
	}
}

// DefaultEvidenceAggregator implements EvidenceAggregator with configurable
// aggregation parameters and thread-safe caching.
type DefaultEvidenceAggregator struct {
	store  ExperienceStore
	config *AggregatorConfig
	cache  *evidenceCache
}

// evidenceCache holds cached evidence with expiration tracking.
type evidenceCache struct {
	mu         sync.RWMutex
	byStrategy map[string]*cachedEvidence
	byTaskType map[string]*cachedEvidence
	byWindow   map[string]*cachedEvidence
}

// cachedEvidence holds an Evidence with its cache timestamp.
type cachedEvidence struct {
	evidence Evidence
	cachedAt time.Time
}

// NewDefaultEvidenceAggregator creates a new DefaultEvidenceAggregator.
//
// Args:
//
//	store - the experience store to read from (must not be nil).
//	config - aggregator configuration (nil uses defaults).
//
// Returns:
//
//	*DefaultEvidenceAggregator - the initialized aggregator.
func NewDefaultEvidenceAggregator(store ExperienceStore, config *AggregatorConfig) *DefaultEvidenceAggregator {
	if config == nil {
		config = DefaultAggregatorConfig()
	}

	aggregator := &DefaultEvidenceAggregator{
		store:  store,
		config: config,
	}

	if config.EnableCache {
		aggregator.cache = &evidenceCache{
			byStrategy: make(map[string]*cachedEvidence),
			byTaskType: make(map[string]*cachedEvidence),
			byWindow:   make(map[string]*cachedEvidence),
		}
	}

	return aggregator
}

// Aggregate aggregates all experiences for a specific strategy.
// Uses cached evidence if available and not stale.
//
// Args:
//
//	ctx - timeout and cancellation context.
//	strategyID - the strategy identifier to aggregate.
//
// Returns:
//
//	Evidence - aggregated statistics, empty if no experiences found.
//	error - context cancellation or storage error.
func (a *DefaultEvidenceAggregator) Aggregate(ctx context.Context, strategyID string) (Evidence, error) {
	if err := ctx.Err(); err != nil {
		return Evidence{}, err
	}

	if strategyID == "" {
		return Evidence{}, errors.New("strategy_id cannot be empty")
	}

	// Check cache first.
	if a.config.EnableCache && a.cache != nil {
		if cached, ok := a.getCachedByStrategy(strategyID); ok && !a.isStale(cached) {
			return cached.evidence, nil
		}
	}

	// Query all experiences for the strategy from ExperienceStore.
	startTime := time.Time{}
	endTime := time.Now()

	experiences, err := a.store.Query(ctx, strategyID, startTime, endTime)
	if err != nil {
		return Evidence{}, err
	}

	if len(experiences) == 0 {
		return Evidence{StrategyID: strategyID}, nil
	}

	evidence := AggregateEvidence(experiences)

	evidence.Confidence = a.calculateConfidence(evidence.SampleCount)

	if a.config.EnableCache && a.cache != nil {
		a.setCachedByStrategy(strategyID, evidence)
	}

	return evidence, nil
}

// AggregateByTaskType aggregates experiences for a specific task type.
// Uses cached evidence if available and not stale.
//
// Args:
//
//	ctx - timeout and cancellation context.
//	taskType - the task type to aggregate.
//
// Returns:
//
//	Evidence - aggregated statistics, empty if no experiences found.
//	error - context cancellation or storage error.
func (a *DefaultEvidenceAggregator) AggregateByTaskType(ctx context.Context, taskType string) (Evidence, error) {
	if err := ctx.Err(); err != nil {
		return Evidence{}, err
	}

	if taskType == "" {
		return Evidence{}, errors.New("task_type cannot be empty")
	}

	// Check cache first.
	if a.config.EnableCache && a.cache != nil {
		if cached, ok := a.getCachedByTaskType(taskType); ok && !a.isStale(cached) {
			return cached.evidence, nil
		}
	}

	// Query experiences for the task type from ExperienceStore.
	experiences, err := a.store.QueryByTaskType(ctx, taskType, 0)
	if err != nil {
		return Evidence{}, err
	}

	if len(experiences) == 0 {
		return Evidence{TaskType: taskType}, nil
	}

	evidence := AggregateEvidence(experiences)

	evidence.Confidence = a.calculateConfidence(evidence.SampleCount)

	if a.config.EnableCache && a.cache != nil {
		a.setCachedByTaskType(taskType, evidence)
	}

	return evidence, nil
}

// AggregateByTimeWindow aggregates experiences within a time window.
// Supported window values: "hourly", "daily", "weekly".
//
// Args:
//
//	ctx - timeout and cancellation context.
//	strategyID - the strategy identifier to aggregate.
//	window - the time window type ("hourly", "daily", "weekly").
//
// Returns:
//
//	Evidence - aggregated statistics for the current time window.
//	error - context cancellation, invalid window, or storage error.
func (a *DefaultEvidenceAggregator) AggregateByTimeWindow(ctx context.Context, strategyID, window string) (Evidence, error) {
	if err := ctx.Err(); err != nil {
		return Evidence{}, err
	}

	if strategyID == "" {
		return Evidence{}, errors.New("strategy_id cannot be empty")
	}

	startTime, endTime, err := a.getTimeWindow(window)
	if err != nil {
		return Evidence{}, err
	}

	cacheKey := strategyID + "|" + window
	if a.config.EnableCache && a.cache != nil {
		if cached, ok := a.getCachedByWindow(cacheKey); ok && !a.isStale(cached) {
			if a.windowMatches(cached.evidence.LastUpdated, startTime, endTime) {
				return cached.evidence, nil
			}
		}
	}

	// Query experiences within the time window from ExperienceStore.
	experiences, err := a.store.Query(ctx, strategyID, startTime, endTime)
	if err != nil {
		return Evidence{}, err
	}

	if len(experiences) == 0 {
		return Evidence{
			StrategyID:  strategyID,
			LastUpdated: endTime,
		}, nil
	}

	evidence := AggregateEvidence(experiences)

	evidence.Confidence = a.calculateConfidence(evidence.SampleCount)

	if a.config.EnableCache && a.cache != nil {
		a.setCachedByWindow(cacheKey, evidence)
	}

	return evidence, nil
}

// RefreshAll refreshes all cached evidence aggregates.
// Clears stale cache entries and forces recomputation.
//
// Args:
//
//	ctx - timeout and cancellation context.
//
// Returns:
//
//	error - context cancellation or storage error.
func (a *DefaultEvidenceAggregator) RefreshAll(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if !a.config.EnableCache || a.cache == nil {
		return nil
	}

	a.cache.mu.Lock()
	defer a.cache.mu.Unlock()

	a.cache.byStrategy = make(map[string]*cachedEvidence)
	a.cache.byTaskType = make(map[string]*cachedEvidence)
	a.cache.byWindow = make(map[string]*cachedEvidence)

	return nil
}

// calculateConfidence calculates confidence based on sample count.
// Uses piecewise linear scaling between min and max sample counts.
func (a *DefaultEvidenceAggregator) calculateConfidence(sampleCount int64) float64 {
	if sampleCount <= 0 {
		return 0.0
	}

	min := a.config.MinSampleCount
	max := a.config.MaxSampleCount

	if min <= 0 {
		min = 10
	}
	if max <= min {
		max = 1000
	}

	if sampleCount >= max {
		return 1.0
	}

	if sampleCount < min {
		return float64(sampleCount) / float64(min) * 0.5
	}

	rangeSize := max - min
	position := sampleCount - min
	confidence := float64(position)/float64(rangeSize)*0.5 + 0.5

	return confidence
}

// getTimeWindow calculates the start and end time for a given window type.
func (a *DefaultEvidenceAggregator) getTimeWindow(window string) (startTime, endTime time.Time, err error) {
	now := time.Now()

	switch window {
	case "hourly":
		startTime = now.Truncate(time.Hour)
		endTime = startTime.Add(time.Hour)
	case "daily":
		startTime = now.Truncate(24 * time.Hour)
		endTime = startTime.Add(24 * time.Hour)
	case "weekly":
		dayOffset := int(now.Weekday() - time.Monday)
		if dayOffset < 0 {
			dayOffset += 7
		}
		startTime = now.AddDate(0, 0, -dayOffset).Truncate(24 * time.Hour)
		endTime = startTime.AddDate(0, 0, 7)
	default:
		err = errors.New("invalid time window: must be hourly, daily, or weekly")
	}

	return startTime, endTime, err
}

// windowMatches checks if a timestamp falls within a time window.
func (a *DefaultEvidenceAggregator) windowMatches(timestamp, startTime, endTime time.Time) bool {
	return timestamp.Compare(startTime) >= 0 && timestamp.Compare(endTime) < 0
}

// isStale checks if cached evidence is stale based on TTL.
func (a *DefaultEvidenceAggregator) isStale(cached *cachedEvidence) bool {
	ttl := time.Duration(a.config.CacheTTLSeconds) * time.Second
	return time.Since(cached.cachedAt) > ttl
}

// getCachedByStrategy retrieves cached evidence by strategy ID.
func (a *DefaultEvidenceAggregator) getCachedByStrategy(strategyID string) (*cachedEvidence, bool) {
	a.cache.mu.RLock()
	defer a.cache.mu.RUnlock()

	cached, ok := a.cache.byStrategy[strategyID]
	return cached, ok
}

// setCachedByStrategy stores cached evidence by strategy ID.
func (a *DefaultEvidenceAggregator) setCachedByStrategy(strategyID string, evidence Evidence) {
	a.cache.mu.Lock()
	defer a.cache.mu.Unlock()

	a.cache.byStrategy[strategyID] = &cachedEvidence{
		evidence: evidence,
		cachedAt: time.Now(),
	}
}

// getCachedByTaskType retrieves cached evidence by task type.
func (a *DefaultEvidenceAggregator) getCachedByTaskType(taskType string) (*cachedEvidence, bool) {
	a.cache.mu.RLock()
	defer a.cache.mu.RUnlock()

	cached, ok := a.cache.byTaskType[taskType]
	return cached, ok
}

// setCachedByTaskType stores cached evidence by task type.
func (a *DefaultEvidenceAggregator) setCachedByTaskType(taskType string, evidence Evidence) {
	a.cache.mu.Lock()
	defer a.cache.mu.Unlock()

	a.cache.byTaskType[taskType] = &cachedEvidence{
		evidence: evidence,
		cachedAt: time.Now(),
	}
}

// getCachedByWindow retrieves cached evidence by window key.
func (a *DefaultEvidenceAggregator) getCachedByWindow(key string) (*cachedEvidence, bool) {
	a.cache.mu.RLock()
	defer a.cache.mu.RUnlock()

	cached, ok := a.cache.byWindow[key]
	return cached, ok
}

// setCachedByWindow stores cached evidence by window key.
func (a *DefaultEvidenceAggregator) setCachedByWindow(key string, evidence Evidence) {
	a.cache.mu.Lock()
	defer a.cache.mu.Unlock()

	a.cache.byWindow[key] = &cachedEvidence{
		evidence: evidence,
		cachedAt: time.Now(),
	}
}
