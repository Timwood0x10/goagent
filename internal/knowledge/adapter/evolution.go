package adapter

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/knowledge"
	ares_evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
)

// FromStrategy converts an evolution Strategy into a KnowledgeObject.
// The object type is set to ObjectDecision so it appears in decision-related queries.
func FromStrategy(s *ares_evolution.Strategy, ns string) *knowledge.KnowledgeObject {
	if s == nil {
		return nil
	}

	summary := s.Name
	if summary == "" {
		summary = fmt.Sprintf("Strategy %s (v%d)", s.ID, s.Version)
	}
	if len(summary) > 200 {
		summary = summary[:200] + "..."
	}

	tags := []string{"evolution", "strategy"}
	if s.StrategyMutationType != "" {
		tags = append(tags, s.StrategyMutationType)
	}

	return &knowledge.KnowledgeObject{
		ID:         fmt.Sprintf("evo_%s_v%d", s.ID, s.Version),
		Type:       knowledge.ObjectDecision,
		Namespace:  ns,
		Summary:    summary,
		Confidence: scoreToConfidence(s.Score),
		CreatedAt:  s.CreatedAt,
		UpdatedAt:  time.Now(),
		Tags:       tags,
		Metadata: map[string]any{
			"strategy_id":            s.ID,
			"version":                s.Version,
			"parent_id":              s.ParentID,
			"mutation_type":          s.StrategyMutationType,
			"mutation_desc":          s.MutationDesc,
			"score":                  s.Score,
			"strategy_prompt_length": len(s.PromptTemplate),
		},
	}
}

// FromDecisionEvidence converts one lifecycle decision-evidence record
// (promote/rollback, source="lifecycle", evolution loop closure) into a
// decision KnowledgeObject. It is the counterpart of FromStrategy: lineage
// answers "where did this strategy come from", decision evidence answers
// "why was it promoted or rolled back, and at what score" — only the latter
// can answer an agent asking "why did we roll back last time".
//
// Double filtering contract (mirrors the provider side):
//   - Source filtering happens at query time ("lifecycle");
//   - payload filtering happens HERE: a record without an "action" field is
//     NOT a decision and yields nil, so a future emitter sharing the source
//     cannot leak into decision queries.
//
// Malformed JSON yields nil too — one corrupt record must not break the
// stream, and it must never panic.
//
// Args:
//   - ev: the evidence record (Payload is JSON with action/value/strategy_id/reason).
//   - ns: the knowledge namespace.
//
// Returns:
//   - *knowledge.KnowledgeObject: the decision object, or nil when the record
//     is not a decision (no action) or is undecodable.
func FromDecisionEvidence(ev evidence.Evidence, ns string) *knowledge.KnowledgeObject {
	if len(ev.Payload) == 0 {
		return nil
	}
	var payload struct {
		Action     string  `json:"action"`
		Value      float64 `json:"value"`
		StrategyID string  `json:"strategy_id"`
		Reason     string  `json:"reason"`
		Timestamp  string  `json:"timestamp"`
	}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return nil
	}
	if payload.Action == "" {
		return nil // not a decision — a plain fitness/observability record
	}

	summary := fmt.Sprintf("Strategy %s %s", payload.StrategyID, payload.Action)
	if payload.Reason != "" {
		summary += ": " + payload.Reason
	}
	if len(summary) > 200 {
		summary = summary[:200] + "..."
	}

	createdAt := ev.Timestamp
	if payload.Timestamp != "" {
		if ts, err := time.Parse(time.RFC3339, payload.Timestamp); err == nil {
			createdAt = ts
		}
	}

	return &knowledge.KnowledgeObject{
		ID:         ev.ID,
		Type:       knowledge.ObjectDecision,
		Namespace:  ns,
		Summary:    summary,
		Confidence: scoreToConfidence(payload.Value),
		CreatedAt:  createdAt,
		UpdatedAt:  time.Now(),
		Tags:       []string{"evolution", "decision", payload.Action},
		Metadata: map[string]any{
			"action":      payload.Action,
			"strategy_id": payload.StrategyID,
			"score":       payload.Value,
			"reason":      payload.Reason,
		},
	}
}

// FromStrategies converts a slice of evolution Strategies into KnowledgeObjects.
func FromStrategies(strategies []*ares_evolution.Strategy, ns string) []*knowledge.KnowledgeObject {
	objects := make([]*knowledge.KnowledgeObject, 0, len(strategies))
	for _, s := range strategies {
		if obj := FromStrategy(s, ns); obj != nil {
			objects = append(objects, obj)
		}
	}
	return objects
}

// scoreToConfidence maps an evolution score [-inf, +inf] to a [0, 1] confidence.
// Score 0 → 0.5, positive → higher, negative → lower, clamped to [0.1, 0.99].
func scoreToConfidence(score float64) float64 {
	// Sigmoid: σ(x/2) = 1 / (1 + e^(-x/2))
	// Score 0 → 0.5, Score 5 → 0.92, Score -5 → 0.08
	c := 1.0 / (1.0 + math.Exp(-score/2.0))
	if c < 0.1 {
		return 0.1
	}
	if c > 0.99 {
		return 0.99
	}
	return c
}
