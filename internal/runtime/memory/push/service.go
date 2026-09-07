// Package push provides active knowledge recommendation to strategies.
package push

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// PushService proactively pushes relevant distilled knowledge to registered targets.
// Implementations must be safe for concurrent use and cancelable via context.
type PushService interface {
	// RegisterTarget registers a target to receive pushed knowledge.
	// Registering the same target ID replaces the previous target.
	RegisterTarget(target PushTarget)
	// UnregisterTarget removes a target by ID. No-op if not registered.
	UnregisterTarget(targetID string)
	// PushRelevant pushes knowledge items to all matching targets.
	// On-demand semantics; safe to call repeatedly.
	//
	// Args:
	//   ctx - timeout and cancellation context.
	//
	// Returns:
	//   *PushBatchResult - the per-delivery outcome summary.
	//   error - wrapped error if retrieval fails (partial delivery errors are recorded in results).
	PushRelevant(ctx context.Context) (*PushBatchResult, error)
	// Start begins the scheduled or event-triggered push loop.
	// Returns immediately; the loop runs until ctx is cancelled.
	// Calling Start while a loop is already running returns ErrAlreadyRunning.
	//
	// Args:
	//   ctx - lifecycle context; cancelling stops the loop.
	//
	// Returns:
	//   error - non-nil if a loop is already running or config is invalid for scheduling.
	Start(ctx context.Context) error
	// Stop signals the running loop to stop and waits for it to drain.
	// Safe to call multiple times; no-op if not running.
	Stop()
}

// DefaultPushService is the canonical PushService implementation.
// It holds interfaces (KnowledgeProvider, targets) not concrete types,
// and protects shared state with sync.RWMutex.
type DefaultPushService struct {
	provider KnowledgeProvider
	config   *PushConfig

	mu      sync.RWMutex
	targets map[string]PushTarget

	// Lifecycle management for Start/Stop.
	runMu     sync.Mutex
	cancelFn  context.CancelFunc
	doneCh    chan struct{}
	isRunning bool
}

// NewPushService creates a new DefaultPushService.
//
// Args:
//
//	provider - the knowledge provider to read from (must not be nil).
//	config - push configuration (nil uses defaults).
//
// Returns:
//
//	*DefaultPushService - the configured service.
func NewPushService(provider KnowledgeProvider, config *PushConfig) (*DefaultPushService, error) {
	if provider == nil {
		return nil, fmt.Errorf("push service: %w", ErrInvalidConfig)
	}
	if config == nil {
		config = DefaultPushConfig()
	}
	if config.MinScore < 0 || config.MinScore > 1 {
		return nil, fmt.Errorf("push service: min_score must be in [0,1]: %w", ErrInvalidConfig)
	}
	if config.Policy == PolicyScheduled && config.Interval <= 0 {
		return nil, fmt.Errorf("push service: scheduled policy requires positive interval: %w", ErrInvalidConfig)
	}
	return &DefaultPushService{
		provider: provider,
		config:   config,
		targets:  make(map[string]PushTarget),
	}, nil
}

// RegisterTarget registers a target to receive pushed knowledge.
// Registering the same target ID replaces the previous target.
//
// Args:
//
//	target - the target to register (must not be nil and must have a non-empty ID).
func (s *DefaultPushService) RegisterTarget(target PushTarget) {
	if target == nil || target.ID() == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targets[target.ID()] = target
}

// UnregisterTarget removes a target by ID. No-op if not registered.
//
// Args:
//
//	targetID - the ID of the target to remove.
func (s *DefaultPushService) UnregisterTarget(targetID string) {
	if targetID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.targets, targetID)
}

// listTargets returns a snapshot of registered targets under the read lock.
func (s *DefaultPushService) listTargets() []PushTarget {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PushTarget, 0, len(s.targets))
	for _, t := range s.targets {
		out = append(out, t)
	}
	return out
}

// PushRelevant pushes knowledge items to all matching targets.
// Items are filtered by MinScore, then matched against each target's criteria,
// then delivered (with MaxItemsPerTarget cap). Delivery errors are logged
// and recorded in the result but do not abort the batch.
//
// Args:
//
//	ctx - timeout and cancellation context.
//
// Returns:
//
//	*PushBatchResult - the per-delivery outcome summary.
//	error - wrapped error if the provider fails or no targets are registered.
func (s *DefaultPushService) PushRelevant(ctx context.Context) (*PushBatchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("push relevant: %w", err)
	}

	targets := s.listTargets()
	if len(targets) == 0 {
		return nil, fmt.Errorf("push relevant: %w", ErrNoTargets)
	}

	items, err := s.provider.ListKnowledge(ctx)
	if err != nil {
		return nil, fmt.Errorf("push relevant: list knowledge: %w", err)
	}

	// Sort targets by ID for deterministic delivery order.
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].ID() < targets[j].ID()
	})

	startedAt := time.Now()
	result := &PushBatchResult{
		TotalTargets: len(targets),
		TotalItems:   len(items),
		StartedAt:    startedAt,
		Results:      []PushResult{},
	}

	for _, target := range targets {
		if err := ctx.Err(); err != nil {
			break
		}
		s.pushToTarget(ctx, target, items, result)
	}

	result.FinishedAt = time.Now()
	return result, nil
}

// pushToTarget delivers all relevant items to a single target.
// Updates the result in place; not safe for concurrent calls with the same result.
func (s *DefaultPushService) pushToTarget(ctx context.Context, target PushTarget, items []KnowledgeItem, result *PushBatchResult) {
	criteria := target.Criteria()
	delivered := 0
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return
		}
		if !s.isEligible(item) {
			result.Skipped++
			continue
		}
		if !matchesCriteria(criteria, item) {
			result.Skipped++
			continue
		}
		if s.config.MaxItemsPerTarget > 0 && delivered >= s.config.MaxItemsPerTarget {
			result.Skipped++
			continue
		}
		if err := target.Deliver(ctx, item); err != nil {
			result.Failed++
			result.Results = append(result.Results, PushResult{
				TargetID:  target.ID(),
				ItemID:    item.ID,
				Delivered: false,
				Error:     err.Error(),
			})
			slog.WarnContext(ctx, "[PushService] delivery failed",
				"target_id", target.ID(), "item_id", item.ID, "error", err)
			continue
		}
		result.Delivered++
		delivered++
		result.Results = append(result.Results, PushResult{
			TargetID:  target.ID(),
			ItemID:    item.ID,
			Delivered: true,
		})
	}
}

// isEligible returns true if the item meets the minimum score threshold.
func (s *DefaultPushService) isEligible(item KnowledgeItem) bool {
	if s.config.MinScore <= 0 {
		return true
	}
	return item.Score >= s.config.MinScore
}

// matchesCriteria reports whether an item matches the given relevance criteria.
// Empty criteria fields act as wildcards. If criteria is fully empty,
// all items match (relevance is universal).
//
// Args:
//
//	criteria - the target's criteria.
//	item - the knowledge item being evaluated.
//
// Returns:
//
//	bool - true if the item is relevant to the target.
func matchesCriteria(criteria RelevanceCriteria, item KnowledgeItem) bool {
	if criteria.IsEmpty() {
		return true
	}
	if criteria.StrategyID != "" && criteria.StrategyID != item.StrategyID {
		return false
	}
	if criteria.TaskType != "" && criteria.TaskType != item.TaskType {
		return false
	}
	if criteria.PromptTemplate != "" && criteria.PromptTemplate != item.PromptTemplate {
		return false
	}
	if criteria.EvidenceKey != "" && criteria.EvidenceKey != item.EvidenceKey {
		return false
	}
	return true
}

// Start begins the scheduled or event-triggered push loop.
// The loop runs until ctx is cancelled or Stop is called.
// For PolicyOnDemand, Start is a no-op and returns nil (push is driven by PushRelevant).
//
// Args:
//
//	ctx - lifecycle context; cancelling stops the loop.
//
// Returns:
//
//	error - non-nil if a loop is already running or config is invalid.
func (s *DefaultPushService) Start(ctx context.Context) error {
	if s.config.Policy == PolicyOnDemand {
		return nil
	}

	s.runMu.Lock()
	if s.isRunning {
		s.runMu.Unlock()
		return fmt.Errorf("push start: %w", ErrAlreadyRunning)
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancelFn = cancel
	s.doneCh = make(chan struct{})
	s.isRunning = true
	s.runMu.Unlock()

	switch s.config.Policy {
	case PolicyScheduled:
		go s.scheduledLoop(runCtx)
	case PolicyEventTriggered:
		// Event-triggered mode waits for external TriggerEvent calls;
		// the loop just keeps the service alive until ctx is cancelled.
		go s.eventLoop(runCtx)
	default:
		// Unknown policy: cancel immediately to release resources.
		s.runMu.Lock()
		s.isRunning = false
		s.cancelFn = nil
		s.runMu.Unlock()
		cancel()
		return fmt.Errorf("push start: unknown policy %q: %w", s.config.Policy, ErrInvalidConfig)
	}
	return nil
}

// Stop signals the running loop to stop and waits for it to drain.
// Safe to call multiple times; no-op if not running.
func (s *DefaultPushService) Stop() {
	s.runMu.Lock()
	if !s.isRunning {
		s.runMu.Unlock()
		return
	}
	cancelFn := s.cancelFn
	doneCh := s.doneCh
	s.runMu.Unlock()

	if cancelFn != nil {
		cancelFn()
	}
	if doneCh != nil {
		<-doneCh
	}

	s.runMu.Lock()
	s.isRunning = false
	s.cancelFn = nil
	s.doneCh = nil
	s.runMu.Unlock()
}

// scheduledLoop runs a periodic push on the configured interval until ctx is cancelled.
func (s *DefaultPushService) scheduledLoop(ctx context.Context) {
	defer s.finishLoop()

	ticker := time.NewTicker(s.config.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.InfoContext(ctx, "[PushService] scheduled loop stopped")
			return
		case <-ticker.C:
			if _, err := s.PushRelevant(ctx); err != nil {
				slog.WarnContext(ctx, "[PushService] scheduled push failed", "error", err)
			}
		}
	}
}

// eventLoop keeps the service alive for event-triggered pushes.
// It does nothing itself; external callers invoke PushRelevant when events arrive.
func (s *DefaultPushService) eventLoop(ctx context.Context) {
	defer s.finishLoop()

	<-ctx.Done()
	slog.InfoContext(ctx, "[PushService] event loop stopped")
}

// finishLoop resets the lifecycle state when a loop exits, whether via Stop
// or via external context cancellation. Without the isRunning reset, a
// loop cancelled by its ctx (rather than Stop) would leave the service
// permanently stuck in ErrAlreadyRunning on the next Start.
func (s *DefaultPushService) finishLoop() {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.doneCh != nil {
		close(s.doneCh)
	}
	s.isRunning = false
	s.cancelFn = nil
	s.doneCh = nil
}
