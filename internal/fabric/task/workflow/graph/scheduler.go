// package graph - provides dynamic agent orchestration with pluggable scheduling.

package graph

import "sync"

// Scheduler defines the interface for node scheduling.
type Scheduler interface {
	// Select returns the next node ID to execute from the ready queue.
	Select(ready []string) string
}

// DefaultScheduler provides FIFO scheduling, consistent with Workflow Engine.
type DefaultScheduler struct{}

// NewDefaultScheduler creates a new default scheduler.
func NewDefaultScheduler() *DefaultScheduler {
	return &DefaultScheduler{}
}

// Select returns the first ready node (FIFO).
func (s *DefaultScheduler) Select(ready []string) string {
	if len(ready) == 0 {
		return ""
	}
	return ready[0]
}

// PriorityScheduler provides priority-based scheduling.
type PriorityScheduler struct {
	priorities map[string]int
}

// NewPriorityScheduler creates a new priority scheduler.
func NewPriorityScheduler(priorities map[string]int) *PriorityScheduler {
	if priorities == nil {
		priorities = make(map[string]int)
	}
	return &PriorityScheduler{priorities: priorities}
}

// Select returns the ready node with the highest priority.
func (s *PriorityScheduler) Select(ready []string) string {
	if len(ready) == 0 {
		return ""
	}

	bestNode := ready[0]
	bestPriority := s.getPriority(bestNode)

	for _, nodeID := range ready[1:] {
		priority := s.getPriority(nodeID)
		if priority > bestPriority {
			bestNode = nodeID
			bestPriority = priority
		}
	}

	return bestNode
}

// getPriority returns the priority for a node ID, defaulting to 0.
func (s *PriorityScheduler) getPriority(nodeID string) int {
	if s == nil || s.priorities == nil {
		return 0
	}
	priority, ok := s.priorities[nodeID]
	if !ok {
		return 0
	}
	return priority
}

// ShortJobScheduler provides shortest-job-first scheduling.
type ShortJobScheduler struct {
	estimates map[string]int // estimated latency in milliseconds
}

// NewShortJobScheduler creates a new short-job scheduler.
func NewShortJobScheduler(estimates map[string]int) *ShortJobScheduler {
	if estimates == nil {
		estimates = make(map[string]int)
	}
	return &ShortJobScheduler{estimates: estimates}
}

// Select returns the ready node with the shortest estimated execution time.
func (s *ShortJobScheduler) Select(ready []string) string {
	if len(ready) == 0 {
		return ""
	}

	bestNode := ready[0]
	bestEstimate := s.getEstimate(bestNode)

	for _, nodeID := range ready[1:] {
		estimate := s.getEstimate(nodeID)
		if estimate < bestEstimate {
			bestNode = nodeID
			bestEstimate = estimate
		}
	}

	return bestNode
}

// getEstimate returns the estimated latency for a node ID.
// For unknown nodes, returns a reasonable default value (1000ms) to ensure
// they can still be scheduled but with lower priority than known short jobs.
func (s *ShortJobScheduler) getEstimate(nodeID string) int {
	if s == nil || s.estimates == nil {
		return 1000
	}
	estimate, ok := s.estimates[nodeID]
	if !ok {
		return 1000
	}
	return estimate
}

// RoundRobinScheduler cycles through ready nodes in order, distributing
// execution fairly across all ready tasks. Each call to Select advances
// the internal cursor by one position.
type RoundRobinScheduler struct {
	mu     sync.Mutex
	cursor int
}

// NewRoundRobinScheduler creates a new round-robin scheduler with cursor at 0.
func NewRoundRobinScheduler() *RoundRobinScheduler {
	return &RoundRobinScheduler{}
}

// Select returns the next ready node in round-robin order.
func (s *RoundRobinScheduler) Select(ready []string) string {
	if len(ready) == 0 {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cursor >= len(ready) {
		s.cursor = 0
	}
	node := ready[s.cursor]
	s.cursor++
	return node
}

// WeightedFairScheduler distributes execution proportionally to each node's
// configured weight. Nodes with higher weight are selected more frequently.
// When all weights are equal, it behaves like round-robin.
//
// Selection uses a bounded deficit-round-robin (DRR) scheme: each Select adds
// the node's weight to its credit counter, the ready node with the highest
// credit is served, and the winner is then charged the sum of ready weights so
// its credit drops below the others. Because each call adds totalWeight and
// subtracts totalWeight, the sum of all counters is invariant, so no counter
// grows without limit regardless of how many Select calls are made. This fixes
// the previous implementation, which incremented every ready node's counter on
// each call and never decremented, producing unbounded growth and collapsing to
// "always pick the first ready node" once counters saturated.
type WeightedFairScheduler struct {
	mu      sync.Mutex
	weights map[string]int
	counter map[string]int // bounded DRR credit counter per node
}

// NewWeightedFairScheduler creates a weighted fair scheduler.
// Nodes not in the weights map default to weight 1.
func NewWeightedFairScheduler(weights map[string]int) *WeightedFairScheduler {
	if weights == nil {
		weights = make(map[string]int)
	}
	return &WeightedFairScheduler{
		weights: weights,
		counter: make(map[string]int),
	}
}

// Select picks the ready node with the highest accumulated credit, implementing
// bounded weighted fair queuing. Ties go to the first ready node so selection
// stays deterministic. See WeightedFairScheduler for the boundedness argument.
func (s *WeightedFairScheduler) Select(ready []string) string {
	if len(ready) == 0 {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Accumulate each ready node's weight as its service credit (the DRR
	// quantum). Higher-weight nodes earn credit faster and are selected more
	// often. Non-ready nodes keep their credit frozen until they reappear.
	totalWeight := 0
	for _, nodeID := range ready {
		weight := s.weight(nodeID)
		s.counter[nodeID] += weight
		totalWeight += weight
	}

	// Pick the node with the highest accumulated credit.
	bestNode := ready[0]
	bestCredit := s.counter[bestNode]
	for _, nodeID := range ready[1:] {
		if credit := s.counter[nodeID]; credit > bestCredit {
			bestCredit = credit
			bestNode = nodeID
		}
	}

	// Charge the winner one full round of service (the sum of ready weights) so
	// its credit drops below the others. This DRR subtract is what keeps
	// counters bounded: the sum of counters is invariant across calls, so no
	// counter can grow without limit.
	s.counter[bestNode] -= totalWeight
	return bestNode
}

// weight returns the configured weight for a node, defaulting to 1 for unknown
// or non-positive weights so every ready node remains schedulable.
func (s *WeightedFairScheduler) weight(nodeID string) int {
	if w := s.weights[nodeID]; w > 0 {
		return w
	}
	return 1
}
