// Package flight provides runtime intelligence for ares agents.
// It records execution timelines, call graphs, decisions, memory pipelines,
// and diagnostics — the "flight recorder" for multi-agent systems.
package flight

import (
	"sync"
	"time"
)

// EventType classifies a timeline event.
type EventType string

const (
	EventAgentStart EventType = "agent.start"
	EventAgentEnd   EventType = "agent.end"
	EventTaskEnd    EventType = "task.end"
	EventToolCall   EventType = "tool.call"
	EventToolResult EventType = "tool.result"
	EventLLMCall    EventType = "llm.call"
	EventLLMResult  EventType = "llm.result"
	EventWaiting    EventType = "waiting"
	EventError      EventType = "error"
	EventMemoryOp   EventType = "memory.op"
	EventDecision   EventType = "decision"
)

// TimelineEvent represents a single event in an agent's execution timeline.
type TimelineEvent struct {
	ID       string         `json:"id"`
	ParentID string         `json:"parent_id,omitempty"`
	AgentID  string         `json:"agent_id"`
	Type     EventType      `json:"type"`
	Name     string         `json:"name"`
	StartAt  time.Time      `json:"start_at"`
	EndAt    time.Time      `json:"end_at,omitempty"`
	Duration time.Duration  `json:"duration"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

// TimelineSummary aggregates time distribution.
type TimelineSummary struct {
	TotalDuration time.Duration `json:"total_duration"`
	ToolDuration  time.Duration `json:"tool_duration"`
	LLMDuration   time.Duration `json:"llm_duration"`
	WaitDuration  time.Duration `json:"wait_duration"`
	ErrorDuration time.Duration `json:"error_duration"`
	ToolPercent   float64       `json:"tool_percent"`
	LLMPercent    float64       `json:"llm_percent"`
	WaitPercent   float64       `json:"wait_percent"`
	EventCount    int           `json:"event_count"`
}

// Timeline holds ordered events for one or more agents.
type Timeline struct {
	events []TimelineEvent
	mu     sync.RWMutex
	cap    int
}

// maxTimelineEvents is the ring cap for timeline events, aligned with
// introspect's 300-entry default.
const maxTimelineEvents = 300

// NewTimeline creates an empty timeline.
func NewTimeline() *Timeline {
	return &Timeline{
		events: make([]TimelineEvent, 0, 64),
		cap:    maxTimelineEvents,
	}
}

// pairStartOf maps a result-type event to its start-type counterpart. When a
// result event is added, the most recent unpaired start event for the same
// agent is given an EndAt/Duration, so Summary's typeDuration stops returning
// zero (the previous behavior left every duration at 0).
var pairStartOf = map[EventType]EventType{
	EventToolResult: EventToolCall,
	EventLLMResult:  EventLLMCall,
	EventAgentEnd:   EventAgentStart,
}

// Add appends an event to the timeline. Result-type events (tool.result,
// llm.result, agent.end) pair with their matching start event, filling its
// EndAt and Duration. Pairing prefers an explicit ParentID link — the unpaired
// start event whose ID equals the result's ParentID — which is robust to
// out-of-order arrival and to overlapping calls within one agent. When no
// ParentID is present (or it matches nothing), pairing falls back to the most
// recent unpaired start event of the same agent.
func (t *Timeline) Add(event TimelineEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if startType, ok := pairStartOf[event.Type]; ok {
		paired := false
		// Prefer the explicit parent link when the caller provides one.
		if event.ParentID != "" {
			for i := len(t.events) - 1; i >= 0; i-- {
				prev := &t.events[i]
				if prev.ID == event.ParentID && prev.Type == startType &&
					prev.AgentID == event.AgentID && prev.EndAt.IsZero() {
					prev.EndAt = event.StartAt
					prev.Duration = event.StartAt.Sub(prev.StartAt)
					paired = true
					break
				}
			}
		}
		if !paired {
			for i := len(t.events) - 1; i >= 0; i-- {
				prev := &t.events[i]
				if prev.Type == startType && prev.AgentID == event.AgentID && prev.EndAt.IsZero() {
					prev.EndAt = event.StartAt
					prev.Duration = event.StartAt.Sub(prev.StartAt)
					break
				}
			}
		}
	}
	t.events = append(t.events, event)
	// P1-2: ring cap — drop the oldest event when the cap is exceeded.
	if t.cap > 0 && len(t.events) > t.cap {
		t.events = t.events[len(t.events)-t.cap:]
	}
}

// Events returns a copy of all events.
func (t *Timeline) Events() []TimelineEvent {
	t.mu.RLock()
	defer t.mu.RUnlock()
	result := make([]TimelineEvent, len(t.events))
	copy(result, t.events)
	return result
}

// FilterByAgent returns events for a specific agent.
func (t *Timeline) FilterByAgent(agentID string) []TimelineEvent {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var result []TimelineEvent
	for _, e := range t.events {
		if e.AgentID == agentID {
			result = append(result, e)
		}
	}
	return result
}

// FilterByType returns events of a specific type.
func (t *Timeline) FilterByType(eventType EventType) []TimelineEvent {
	t.mu.RLock()
	defer t.mu.RUnlock()
	var result []TimelineEvent
	for _, e := range t.events {
		if e.Type == eventType {
			result = append(result, e)
		}
	}
	return result
}

// Summary computes time distribution across event types.
func (t *Timeline) Summary() TimelineSummary {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var summary TimelineSummary
	summary.EventCount = len(t.events)

	for _, e := range t.events {
		summary.ToolDuration += typeDuration(e, EventToolCall, EventToolResult)
		summary.LLMDuration += typeDuration(e, EventLLMCall, EventLLMResult)
		summary.WaitDuration += typeDuration(e, EventWaiting)
		summary.ErrorDuration += typeDuration(e, EventError)
	}

	// Total = max(end) - min(start). Only consider events that have
	// a non-zero EndAt, since start-only events (e.g. agent.start,
	// task.start) do not set EndAt.
	if len(t.events) > 0 {
		minStart := t.events[0].StartAt
		var maxEnd time.Time
		for _, e := range t.events {
			if e.StartAt.Before(minStart) {
				minStart = e.StartAt
			}
			if !e.EndAt.IsZero() && (maxEnd.IsZero() || e.EndAt.After(maxEnd)) {
				maxEnd = e.EndAt
			}
		}
		if !maxEnd.IsZero() && maxEnd.After(minStart) {
			summary.TotalDuration = maxEnd.Sub(minStart)
		}
	}

	if summary.TotalDuration > 0 {
		summary.ToolPercent = float64(summary.ToolDuration) / float64(summary.TotalDuration) * 100
		summary.LLMPercent = float64(summary.LLMDuration) / float64(summary.TotalDuration) * 100
		summary.WaitPercent = float64(summary.WaitDuration) / float64(summary.TotalDuration) * 100
	}

	return summary
}

// Len returns the number of events.
func (t *Timeline) Len() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.events)
}

// typeDuration returns the event's duration if its type matches any of the given types.
func typeDuration(e TimelineEvent, types ...EventType) time.Duration {
	for _, tp := range types {
		if e.Type == tp {
			return e.Duration
		}
	}
	return 0
}
