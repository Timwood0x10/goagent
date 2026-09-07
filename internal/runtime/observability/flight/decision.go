package flight

import (
	"sync"
	"time"
)

// DecisionType classifies what kind of decision was made.
type DecisionType string

const (
	DecisionToolSelect      DecisionType = "tool_selection"
	DecisionModelSelect     DecisionType = "model_selection"
	DecisionMemoryRetrieval DecisionType = "memory_retrieval"
	DecisionRetry           DecisionType = "retry"
	DecisionRouting         DecisionType = "routing"
)

// Decision records why an agent made a specific choice.
type Decision struct {
	ID         string         `json:"id"`
	AgentID    string         `json:"agent_id"`
	Type       DecisionType   `json:"type"`
	Candidates []string       `json:"candidates"`
	Selected   string         `json:"selected"`
	Reason     string         `json:"reason"`
	Confidence float64        `json:"confidence"`
	Timestamp  time.Time      `json:"timestamp"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// DecisionLog records all decisions made by agents.
type DecisionLog struct {
	decisions []Decision
	mu        sync.RWMutex
	cap       int
}

// maxDecisionEvents is the ring cap for decisions.
const maxDecisionEvents = 200

// NewDecisionLog creates an empty decision log.
func NewDecisionLog() *DecisionLog {
	return &DecisionLog{
		decisions: make([]Decision, 0, 32),
		cap:       maxDecisionEvents,
	}
}

// Add records a decision.
func (l *DecisionLog) Add(d Decision) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.decisions = append(l.decisions, d)
	// P1-2: ring cap — drop the oldest decision when the cap is exceeded.
	if l.cap > 0 && len(l.decisions) > l.cap {
		l.decisions = l.decisions[len(l.decisions)-l.cap:]
	}
}

// All returns all decisions.
func (l *DecisionLog) All() []Decision {
	l.mu.RLock()
	defer l.mu.RUnlock()
	result := make([]Decision, len(l.decisions))
	copy(result, l.decisions)
	return result
}

// FilterByAgent returns decisions for a specific agent.
func (l *DecisionLog) FilterByAgent(agentID string) []Decision {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var result []Decision
	for _, d := range l.decisions {
		if d.AgentID == agentID {
			result = append(result, d)
		}
	}
	return result
}

// FilterByType returns decisions of a specific type.
func (l *DecisionLog) FilterByType(dType DecisionType) []Decision {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var result []Decision
	for _, d := range l.decisions {
		if d.Type == dType {
			result = append(result, d)
		}
	}
	return result
}

// Len returns the number of recorded decisions.
func (l *DecisionLog) Len() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.decisions)
}
