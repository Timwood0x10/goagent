package adapter

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/pipeline"
	memorystore "github.com/Timwood0x10/ares/internal/knowledge/store/memory"
	"github.com/Timwood0x10/ares/internal/runtime/memory/distillation"
)

// testDistiller is a minimal Distiller that returns fixed memories.
type testDistiller struct{}

func (d *testDistiller) DistillConversation(_ context.Context, _ string, _ []distillation.Message, _, _ string) ([]distillation.Memory, error) {
	return []distillation.Memory{
		{
			ID:         "mem1",
			Type:       distillation.MemoryKnowledge,
			Content:    "Redis was chosen for caching due to low latency.",
			Importance: 85.0,
			CreatedAt:  time.Now(),
		},
		{
			ID:         "mem2",
			Type:       distillation.MemoryInteraction,
			Content:    "Team decided on PostgreSQL for persistence.",
			Importance: 70.0,
			CreatedAt:  time.Now(),
		},
	}, nil
}

// testStore is a minimal in-memory KnowledgeStore for testing.
type testStore struct {
	objects map[string]*knowledge.KnowledgeObject
}

func newTestStore() *testStore {
	return &testStore{objects: make(map[string]*knowledge.KnowledgeObject)}
}

func (s *testStore) Save(_ context.Context, objects ...*knowledge.KnowledgeObject) error {
	for _, obj := range objects {
		if obj.ID == "" {
			return fmt.Errorf("empty ID")
		}
		s.objects[obj.ID] = obj
	}
	return nil
}

func (s *testStore) Get(_ context.Context, id string) (*knowledge.KnowledgeObject, error) {
	obj, ok := s.objects[id]
	if !ok {
		return nil, fmt.Errorf("not found: %s", id)
	}
	return obj, nil
}

func (s *testStore) Query(_ context.Context, _ knowledge.Query) ([]*knowledge.KnowledgeObject, error) {
	var result []*knowledge.KnowledgeObject
	for _, obj := range s.objects {
		result = append(result, obj)
	}
	return result, nil
}

func (s *testStore) Delete(_ context.Context, id string) error {
	delete(s.objects, id)
	return nil
}

func (s *testStore) Search(_ context.Context, _ string, _ string, _ int) ([]*knowledge.KnowledgeObject, error) {
	return nil, nil
}

func (s *testStore) SaveRepresentation(_ context.Context, _ *knowledge.Representation) error {
	return nil
}

func (s *testStore) GetRepresentation(_ context.Context, _, _ string) (*knowledge.Representation, error) {
	return nil, nil
}

func (s *testStore) HybridSearch(_ context.Context, _ knowledge.HybridSearchRequest) ([]knowledge.ScoredObject, error) {
	return nil, nil
}

func (s *testStore) ListByStatus(_ context.Context, _ string, _ knowledge.ObjectStatus, _ int) ([]*knowledge.KnowledgeObject, error) {
	return nil, nil
}

func (s *testStore) UpdateStatus(_ context.Context, _ string, _ knowledge.ObjectStatus) error {
	return nil
}

func (s *testStore) Promote(_ context.Context, _ string, _ *knowledge.Quality) error {
	return nil
}

func TestDistillBridge_NilDistiller(t *testing.T) {
	bridge := NewDistillBridge(nil, nil, nil, "test")
	_, err := bridge.DistillConversation(context.Background(), "conv1", nil, "t1", "u1")
	if err == nil {
		t.Error("expected error for nil distiller")
	}
}

func TestDistillBridge_NoMessages(t *testing.T) {
	bridge := NewDistillBridge(&testDistiller{}, nil, nil, "test")
	_, err := bridge.DistillConversation(context.Background(), "conv1", []distillation.Message{}, "t1", "u1")
	if err == nil {
		t.Error("expected error for empty messages")
	}
}

func TestDistillBridge_FullPipeline(t *testing.T) {
	store := newTestStore()
	bridge := NewDistillBridge(&testDistiller{}, nil, store, "akf")

	msgs := []distillation.Message{
		{Role: "user", Content: "Why Redis?"},
		{Role: "assistant", Content: "Redis for caching."},
	}

	objects, err := bridge.DistillConversation(context.Background(), "conv1", msgs, "t1", "u1")
	if err != nil {
		t.Fatalf("DistillConversation error: %v", err)
	}
	if len(objects) == 0 {
		t.Fatal("expected at least one KnowledgeObject")
	}

	// Verify objects are in store.
	for _, obj := range objects {
		got, err := store.Get(context.Background(), obj.ID)
		if err != nil {
			t.Errorf("object %q not found in store: %v", obj.ID, err)
		}
		if got == nil {
			t.Errorf("object %q is nil in store", obj.ID)
		}
	}
}

func TestDistillBridge_ConfidenceClamped(t *testing.T) {
	store := newTestStore()
	bridge := NewDistillBridge(&testDistiller{}, nil, store, "akf")

	msgs := []distillation.Message{
		{Role: "user", Content: "test"},
		{Role: "assistant", Content: "test response"},
	}

	objects, err := bridge.DistillConversation(context.Background(), "conv2", msgs, "t1", "u1")
	if err != nil {
		t.Fatalf("DistillConversation error: %v", err)
	}

	for _, obj := range objects {
		if obj.Confidence < 0 || obj.Confidence > 1 {
			t.Errorf("confidence %f out of range [0,1]", obj.Confidence)
		}
	}
}

func TestDistillBridge_WithPipeline(t *testing.T) {
	store := newTestStore()

	// Create a minimal pipeline with just a normalizer.
	pipeline := knowledge.NewKnowledgePipeline(
		[]knowledge.Normalizer{&pipeline.DefaultNormalizer{MaxRawBytes: 1024}},
		nil, nil, nil,
	)

	bridge := NewDistillBridge(&testDistiller{}, pipeline, store, "akf")

	msgs := []distillation.Message{
		{Role: "user", Content: "Why Redis?"},
		{Role: "assistant", Content: "Redis for caching."},
	}

	objects, err := bridge.DistillConversation(context.Background(), "conv3", msgs, "t1", "u1")
	if err != nil {
		t.Fatalf("DistillConversation with pipeline error: %v", err)
	}
	if len(objects) == 0 {
		t.Fatal("expected at least one KnowledgeObject with pipeline")
	}
}

// gateDistiller is a configurable ConversationDistiller for quality-gate
// tests. It returns the memories it was constructed with, allowing each
// table-driven case to inject different content.
type gateDistiller struct {
	memories []distillation.Memory
}

// DistillConversation implements ConversationDistiller by returning the
// pre-configured memories, ignoring all arguments.
func (d *gateDistiller) DistillConversation(_ context.Context, _ string, _ []distillation.Message, _, _ string) ([]distillation.Memory, error) {
	return d.memories, nil
}

// TestDistillBridge_QualityGate verifies the 0.2.9 write-loop phases:
// relation extraction, quality-gate scoring, and lifecycle promotion.
// Each case builds a DistillBridge via NewDistillBridgeWithGate with a real
// memstore, nil embedding (skips dedup), and the default relation extractor.
func TestDistillBridge_QualityGate(t *testing.T) {
	tests := []struct {
		name       string
		content    string
		gate       knowledge.QualityGateConfig
		wantStatus knowledge.ObjectStatus
	}{
		{
			name:       "decent_content_promoted_with_relations",
			content:    "This commit fixes the login page bug in the auth module",
			gate:       knowledge.DefaultQualityGateConfig(),
			wantStatus: knowledge.StatusActive,
		},
		{
			name:       "short_content_still_promoted_by_default_gate",
			content:    "x",
			gate:       knowledge.DefaultQualityGateConfig(),
			wantStatus: knowledge.StatusActive,
		},
		{
			name:    "high_threshold_blocks_promotion",
			content: "This commit fixes the login page bug in the auth module",
			gate: knowledge.QualityGateConfig{
				MinExtraction:     0.5,
				MinConsistency:    0.5,
				MinFinalScore:     0.95,
				MaxFactsPerIngest: 50,
				EnableDedup:       true,
				DedupThreshold:    0.85,
			},
			wantStatus: knowledge.StatusCandidate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := memorystore.New()
			distiller := &gateDistiller{
				memories: []distillation.Memory{
					{
						ID:         "mem1",
						Type:       distillation.MemoryKnowledge,
						Content:    tt.content,
						Importance: 80.0,
						CreatedAt:  time.Now(),
					},
				},
			}
			bridge := NewDistillBridgeWithGate(
				distiller,
				nil, // pipeline: nil; Summary feeds relation extraction
				store,
				nil, // emb: nil skips embedding + dedup
				tt.gate,
				knowledge.NewRelationExtractor(),
				"akf",
				"", // model: empty
			)

			msgs := []distillation.Message{
				{Role: "user", Content: "q"},
				{Role: "assistant", Content: "a"},
			}

			objects, err := bridge.DistillConversation(context.Background(), "conv1", msgs, "t1", "u1")
			if err != nil {
				t.Fatalf("DistillConversation error: %v", err)
			}
			if len(objects) != 1 {
				t.Fatalf("expected 1 object, got %d", len(objects))
			}

			obj := objects[0]
			if obj.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", obj.Status, tt.wantStatus)
			}
			if obj.Quality == nil {
				t.Error("expected non-nil Quality after quality gate")
			}
			if obj.Confidence <= 0 {
				t.Errorf("expected Confidence > 0, got %f", obj.Confidence)
			}

			// Verify the stored object reflects the same lifecycle state.
			got, gErr := store.Get(context.Background(), obj.ID)
			if gErr != nil {
				t.Fatalf("object not found in store: %v", gErr)
			}
			if got.Status != tt.wantStatus {
				t.Errorf("stored Status = %q, want %q", got.Status, tt.wantStatus)
			}
			if got.Quality == nil {
				t.Error("expected non-nil Quality in store")
			}
		})
	}
}
