package adapter

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/knowledge"
	ares_evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
)

func TestFromStrategyNil(t *testing.T) {
	if obj := FromStrategy(nil, "test"); obj != nil {
		t.Fatal("expected nil for nil strategy")
	}
}

func TestFromStrategy(t *testing.T) {
	now := time.Now()
	s := &ares_evolution.Strategy{
		ID:                   "strategy-123", // nolint:misspell // strategy abbreviation
		Name:                 "Optimized Redis Strategy",
		Version:              3,
		Score:                4.5,
		ParentID:             "strategy-122", // nolint:misspell // strategy abbreviation
		StrategyMutationType: "param_tweak",
		MutationDesc:         "Increased timeout from 30s to 60s",
		PromptTemplate:       "You are an agent that...",
		CreatedAt:            now,
	}

	obj := FromStrategy(s, "evo-ns")
	if obj == nil {
		t.Fatal("expected non-nil object")
	}

	if obj.Type != knowledge.ObjectDecision {
		t.Errorf("expected type %q, got %q", knowledge.ObjectDecision, obj.Type)
	}
	if obj.Namespace != "evo-ns" {
		t.Errorf("expected namespace 'evo-ns', got %q", obj.Namespace)
	}
	if obj.Summary != "Optimized Redis Strategy" {
		t.Errorf("expected summary 'Optimized Redis Strategy', got %q", obj.Summary)
	}
	if obj.Confidence < 0.5 || obj.Confidence > 0.99 {
		t.Errorf("confidence %.2f out of expected range [0.5, 0.99] for score 4.5", obj.Confidence)
	}

	// Check tags.
	tagMap := make(map[string]bool)
	for _, tag := range obj.Tags {
		tagMap[tag] = true
	}
	if !tagMap["evolution"] || !tagMap["strategy"] || !tagMap["param_tweak"] {
		t.Errorf("expected tags [evolution, strategy, param_tweak], got %v", obj.Tags)
	}

	// Check metadata.
	if obj.Metadata == nil {
		t.Fatal("expected non-nil metadata")
	}
	if obj.Metadata["strategy_id"] != "strategy-123" { // nolint:misspell // strategy abbreviation
		t.Errorf("expected strategy_id 'strategy-123', got %v", obj.Metadata["strategy_id"])
	}
	if obj.Metadata["version"] != 3 {
		t.Errorf("expected version 3, got %v", obj.Metadata["version"])
	}
	if obj.Metadata["score"] != 4.5 {
		t.Errorf("expected score 4.5, got %v", obj.Metadata["score"])
	}
}

func TestFromStrategyEmptyName(t *testing.T) {
	s := &ares_evolution.Strategy{
		ID:                   "s1",
		Version:              1,
		Score:                0,
		StrategyMutationType: "initial",
		CreatedAt:            time.Now(),
	}
	obj := FromStrategy(s, "ns")
	if obj == nil {
		t.Fatal("expected non-nil object")
	}
	// Should use fallback summary "Strategy s1 (v1)".
	if obj.Summary != "Strategy s1 (v1)" {
		t.Errorf("expected fallback summary, got %q", obj.Summary)
	}
}

func TestFromStrategies(t *testing.T) {
	strategies := []*ares_evolution.Strategy{
		{ID: "s1", Version: 1, Score: 1.0, CreatedAt: time.Now()},
		{ID: "s2", Version: 2, Score: 2.0, CreatedAt: time.Now()},
		nil,
	}

	objs := FromStrategies(strategies, "ns")
	if len(objs) != 2 {
		t.Fatalf("expected 2 objects (nil skipped), got %d", len(objs))
	}
}

func TestScoreToConfidence(t *testing.T) {
	tests := []struct {
		score float64
		want  float64
	}{
		{0, 0.5},
		{4.5, 0.9},  // ≈ 0.904
		{10, 0.99},  // clamped
		{-10, 0.1},  // clamped
		{-4.5, 0.1}, // ≈ 0.096
		{2.0, 0.73}, // ≈ 0.731
	}

	for _, tt := range tests {
		got := scoreToConfidence(tt.score)
		if got < tt.want-0.05 || got > tt.want+0.05 {
			t.Errorf("scoreToConfidence(%.1f) = %.2f, want ≈%.2f", tt.score, got, tt.want)
		}
	}
}

// TestFromDecisionEvidence_Cases is the table-driven unit test for the E3
// decision-trail consumer. It locks the discriminator contract: a decision
// record is identified by the "action" field in its payload, NOT by Kind —
// the runtime fitness samples share KindFitness with decisions but carry no
// action, so they must be dropped (return nil).
func TestFromDecisionEvidence_Cases(t *testing.T) {
	fits := func(timestamp string) json.RawMessage {
		raw, err := json.Marshal(map[string]any{
			"value": 0.8, "component": "scheduler", "timestamp": timestamp,
		})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	decision := func(action, strategyID string, score float64, reason string) json.RawMessage {
		raw, err := json.Marshal(map[string]any{
			"action": action, "value": score, "strategy_id": strategyID,
			"reason": reason, "timestamp": "2026-08-31T10:00:00Z",
		})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}

	cases := []struct {
		name     string
		payload  json.RawMessage
		wantNil  bool
		wantAct  string               // expected action tag/word
		wantSID  string               // expected strategy_id metadata
		wantType knowledge.ObjectType // expected object type
	}{
		{
			name:     "promote decision is a decision object",
			payload:  decision("promote", "strategy-v2", 78.0, "shadow win rate 0.71"),
			wantType: knowledge.ObjectDecision,
			wantAct:  "promote",
			wantSID:  "strategy-v2",
		},
		{
			name:     "rollback decision is a decision object",
			payload:  decision("rollback", "strategy-v1", 12.0, "window mean declined"),
			wantType: knowledge.ObjectDecision,
			wantAct:  "rollback",
			wantSID:  "strategy-v1",
		},
		{
			name:    "runtime fitness sample has no action -> nil",
			payload: fits("2026-08-31T10:00:00Z"),
			wantNil: true,
		},
		{
			name:    "empty payload -> nil, no panic",
			payload: nil,
			wantNil: true,
		},
		{
			name:    "malformed json -> nil, no panic",
			payload: json.RawMessage(`{"action":`),
			wantNil: true,
		},
		{
			name:    "json scalar (non-object) -> nil, no panic",
			payload: json.RawMessage(`42`),
			wantNil: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := evidence.Evidence{
				ID:        "ev-1",
				Source:    "lifecycle",
				Kind:      evidence.KindFitness,
				Payload:   tc.payload,
				Timestamp: time.Now(),
			}
			obj := FromDecisionEvidence(ev, "evo-ns")
			if tc.wantNil {
				if obj != nil {
					t.Fatalf("expected nil, got non-nil object %+v", obj)
				}
				return
			}
			if obj == nil {
				t.Fatal("expected non-nil object")
			}
			if obj.Type != tc.wantType {
				t.Errorf("expected type %q, got %q", tc.wantType, obj.Type)
			}
			if obj.Namespace != "evo-ns" {
				t.Errorf("expected namespace 'evo-ns', got %q", obj.Namespace)
			}
			tagMap := make(map[string]bool)
			for _, tag := range obj.Tags {
				tagMap[tag] = true
			}
			// Decision objects always carry [evolution, decision, <action>].
			if !tagMap["evolution"] || !tagMap["decision"] {
				t.Errorf("expected tags [evolution, decision], got %v", obj.Tags)
			}
			if tc.wantAct != "" && !tagMap[tc.wantAct] {
				t.Errorf("expected action tag %q in tags, got %v", tc.wantAct, obj.Tags)
			}
			if tc.wantSID != "" {
				if obj.Metadata == nil {
					t.Fatal("expected non-nil metadata")
				}
				if obj.Metadata["strategy_id"] != tc.wantSID {
					t.Errorf("expected strategy_id %q, got %v", tc.wantSID, obj.Metadata["strategy_id"])
				}
			}
			// Decision objects must carry a reason in their summary/metadata.
			if obj.Summary == "" {
				t.Errorf("expected non-empty summary for a decision object")
			}
		})
	}
}
