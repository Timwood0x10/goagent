package flight

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// ReplayStep represents a single step in a task replay.
type ReplayStep struct {
	StepNum   int            `json:"step_num"`
	EventType string         `json:"event_type"`
	AgentID   string         `json:"agent_id"`
	Payload   map[string]any `json:"payload"`
	Timestamp time.Time      `json:"timestamp"`
}

// ReplaySummary provides an overview of a replay session.
type ReplaySummary struct {
	TaskID     string        `json:"task_id"`
	TotalSteps int           `json:"total_steps"`
	Duration   time.Duration `json:"duration"`
	Agents     []string      `json:"agents"`
	EventTypes []string      `json:"event_types"`
	FirstEvent time.Time     `json:"first_event"`
	LastEvent  time.Time     `json:"last_event"`
}

// ReplaySession allows step-by-step replay of a task's ares_events.
type ReplaySession struct {
	taskID      string
	ares_events []*ares_events.Event
	currentIdx  int
}

// NewReplaySession creates a replay session by loading all ares_events for a task.
func NewReplaySession(ctx context.Context, eventStore ares_events.EventStore, taskID string) (*ReplaySession, error) {
	if eventStore == nil {
		return nil, errors.New("event store is nil")
	}

	evts, err := eventStore.Read(ctx, taskID, ares_events.ReadOptions{
		Direction: ares_events.ReadAscending,
		Limit:     10000,
	})
	if err != nil {
		return nil, fmt.Errorf("read ares_events for task %s: %w", taskID, err)
	}

	if len(evts) == 0 {
		return nil, fmt.Errorf("no ares_events found for task %s", taskID)
	}

	return &ReplaySession{
		taskID:      taskID,
		ares_events: evts,
		currentIdx:  -1,
	}, nil
}

// TotalSteps returns the total number of ares_events.
func (s *ReplaySession) TotalSteps() int {
	return len(s.ares_events)
}

// Step advances to the next event and returns it.
func (s *ReplaySession) Step() (*ReplayStep, error) {
	if s.currentIdx >= len(s.ares_events)-1 {
		return nil, errors.New("no more steps")
	}
	s.currentIdx++
	return s.currentStep(), nil
}

// StepTo jumps to a specific step number (0-indexed).
func (s *ReplaySession) StepTo(n int) (*ReplayStep, error) {
	if n < 0 || n >= len(s.ares_events) {
		return nil, fmt.Errorf("step %d out of range [0, %d)", n, len(s.ares_events))
	}
	s.currentIdx = n
	return s.currentStep(), nil
}

// Current returns the current step without advancing.
func (s *ReplaySession) Current() *ReplayStep {
	if s.currentIdx < 0 || s.currentIdx >= len(s.ares_events) {
		return nil
	}
	return s.currentStep()
}

// Summary returns an overview of the replay session.
func (s *ReplaySession) Summary() ReplaySummary {
	agentSet := make(map[string]struct{})
	typeSet := make(map[string]struct{})

	for _, e := range s.ares_events {
		agentSet[e.StreamID] = struct{}{}
		typeSet[string(e.Type)] = struct{}{}
	}

	agents := make([]string, 0, len(agentSet))
	for a := range agentSet {
		agents = append(agents, a)
	}

	types := make([]string, 0, len(typeSet))
	for t := range typeSet {
		types = append(types, t)
	}

	var duration time.Duration
	if len(s.ares_events) > 1 {
		duration = s.ares_events[len(s.ares_events)-1].Timestamp.Sub(s.ares_events[0].Timestamp)
	}

	return ReplaySummary{
		TaskID:     s.taskID,
		TotalSteps: len(s.ares_events),
		Duration:   duration,
		Agents:     agents,
		EventTypes: types,
		FirstEvent: s.ares_events[0].Timestamp,
		LastEvent:  s.ares_events[len(s.ares_events)-1].Timestamp,
	}
}

// IsFinished returns true if all steps have been replayed.
func (s *ReplaySession) IsFinished() bool {
	return s.currentIdx >= len(s.ares_events)-1
}

// Reset moves back to the beginning.
func (s *ReplaySession) Reset() {
	s.currentIdx = -1
}

func (s *ReplaySession) currentStep() *ReplayStep {
	evt := s.ares_events[s.currentIdx]
	return &ReplayStep{
		StepNum:   s.currentIdx,
		EventType: string(evt.Type),
		AgentID:   evt.StreamID,
		Payload:   evt.Payload,
		Timestamp: evt.Timestamp,
	}
}
