package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/embedding"
	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/provider/store"
	memorystore "github.com/Timwood0x10/ares/internal/knowledge/store/memory"
)

// fakeEmbedding is a deterministic EmbeddingService used to exercise the
// StoreProvider's best-effort query-embedding path without an external
// service. It maps "redis" → unit vector [1,0] so objects seeded with a
// matching representation get a non-zero vector score.
type fakeEmbedding struct{}

func (fakeEmbedding) Embed(_ context.Context, text string) ([]float64, error) {
	if text == "fail" {
		return nil, errors.New("simulated embed failure")
	}
	return []float64{1.0, 0.0}, nil
}
func (fakeEmbedding) EmbedWithPrefix(_ context.Context, text, _ string) ([]float64, error) {
	return fakeEmbedding{}.Embed(context.Background(), text)
}
func (fakeEmbedding) EmbedBatch(_ context.Context, texts []string) ([][]float64, error) {
	out := make([][]float64, len(texts))
	for i, t := range texts {
		out[i], _ = fakeEmbedding{}.Embed(context.Background(), t)
	}
	return out, nil
}
func (fakeEmbedding) HealthCheck(_ context.Context) error { return nil }
func (fakeEmbedding) GetModel() string                    { return "fake" }
func (fakeEmbedding) GetTimeout() time.Duration           { return 0 }

var _ embedding.EmbeddingService = fakeEmbedding{}

func TestStoreProvider_Name(t *testing.T) {
	p := store.New("akg_store", memorystore.New(), nil, "m", "ns")
	if got := p.Name(); got != "akg_store" {
		t.Errorf("Name() = %q, want %q", got, "akg_store")
	}
}

func TestStoreProvider_IntentMatch(t *testing.T) {
	tests := []struct {
		name  string
		types []knowledge.ObjectType
		want  float64
	}{
		{
			name:  "no_types_returns_default_score",
			types: nil,
			want:  0.6,
		},
		{
			name:  "memory_type_returns_high_score",
			types: []knowledge.ObjectType{knowledge.ObjectMemory},
			want:  0.75,
		},
		{
			name:  "decision_type_returns_high_score",
			types: []knowledge.ObjectType{knowledge.ObjectDecision},
			want:  0.75,
		},
		{
			name:  "document_type_returns_high_score",
			types: []knowledge.ObjectType{knowledge.ObjectDocument},
			want:  0.75,
		},
		{
			name:  "code_type_returns_baseline_score",
			types: []knowledge.ObjectType{knowledge.ObjectCode},
			want:  0.5,
		},
		{
			name:  "issue_type_returns_baseline_score",
			types: []knowledge.ObjectType{knowledge.ObjectIssue},
			want:  0.5,
		},
		{
			name:  "mixed_match_and_nonmatch_returns_match_score",
			types: []knowledge.ObjectType{knowledge.ObjectCode, knowledge.ObjectMemory},
			want:  0.75,
		},
		{
			name:  "all_nonmatch_types_return_baseline_score",
			types: []knowledge.ObjectType{knowledge.ObjectCode, knowledge.ObjectIssue, knowledge.ObjectCommit},
			want:  0.5,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := store.New("test", memorystore.New(), nil, "m", "ns")
			got := p.IntentMatch(knowledge.Intent{Scope: knowledge.Scope{Types: tc.types}})
			if got != tc.want {
				t.Errorf("IntentMatch(types=%v) = %v, want %v", tc.types, got, tc.want)
			}
		})
	}
}

func TestStoreProvider_Stream_RecallsActiveObjects_LexicalOnly(t *testing.T) {
	ctx := context.Background()
	ms := memorystore.New()

	seed := []*knowledge.KnowledgeObject{
		{
			ID: "akg:auth:1", Type: knowledge.ObjectMemory, Namespace: "ns",
			Summary:    "router.go fixes the auth bypass in middleware",
			Normalized: "router.go fixes auth bypass middleware",
			Status:     knowledge.StatusActive, Confidence: 0.8, CreatedAt: time.Now(),
		},
		{
			ID: "akg:cache:1", Type: knowledge.ObjectDecision, Namespace: "ns",
			Summary:    "chose redis for the caching layer",
			Normalized: "redis caching layer decision",
			Status:     knowledge.StatusActive, Confidence: 0.85, CreatedAt: time.Now(),
		},
	}
	if err := ms.Save(ctx, seed...); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	// No EmbeddingService: lexical-only recall.
	p := store.New("akg_store", ms, nil, "m", "ns")
	objCh, errCh := p.Stream(ctx, knowledge.Intent{
		Goal:  "redis cache",
		Scope: knowledge.Scope{Types: []knowledge.ObjectType{knowledge.ObjectMemory, knowledge.ObjectDecision}, MaxObjects: 10},
	})

	var got []*knowledge.KnowledgeObject
	for obj := range objCh {
		got = append(got, obj)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected at least one recalled object, got none")
	}

	seen := make(map[string]bool, len(got))
	for _, o := range got {
		seen[o.ID] = true
	}
	if !seen["akg:cache:1"] {
		t.Errorf("expected akg:cache:1 to be recalled by 'redis cache' query, got %v", seen)
	}
}

func TestStoreProvider_Stream_FiltersOutCandidateStatus(t *testing.T) {
	ctx := context.Background()
	ms := memorystore.New()

	seed := []*knowledge.KnowledgeObject{
		{
			ID: "akg:active:1", Type: knowledge.ObjectMemory, Namespace: "ns",
			Summary:    "auth bypass fixed in router",
			Normalized: "auth bypass fixed router",
			Status:     knowledge.StatusActive, Confidence: 0.8, CreatedAt: time.Now(),
		},
		{
			ID: "akg:candidate:1", Type: knowledge.ObjectMemory, Namespace: "ns",
			Summary:    "auth bypass candidate rumour",
			Normalized: "auth bypass candidate rumour",
			Status:     knowledge.StatusCandidate, Confidence: 0.4, CreatedAt: time.Now(),
		},
	}
	if err := ms.Save(ctx, seed...); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	p := store.New("akg_store", ms, nil, "m", "ns")
	objCh, errCh := p.Stream(ctx, knowledge.Intent{
		Goal:  "auth bypass",
		Scope: knowledge.Scope{MaxObjects: 10},
	})

	var got []*knowledge.KnowledgeObject
	for obj := range objCh {
		got = append(got, obj)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("stream error: %v", err)
	}

	for _, o := range got {
		if o.ID == "akg:candidate:1" {
			t.Error("candidate-status object leaked into active-only stream")
		}
	}
	if len(got) == 0 {
		t.Fatal("expected at least one active object recalled, got none")
	}
}

func TestStoreProvider_Stream_WithEmbedding_UsesQueryVector(t *testing.T) {
	ctx := context.Background()
	ms := memorystore.New()

	// Seed an active object whose representation matches the fake embedding
	// (vec [1,0]) so vector recall produces a non-zero VectorScore.
	obj := &knowledge.KnowledgeObject{
		ID: "akg:vec:1", Type: knowledge.ObjectDocument, Namespace: "ns",
		Summary: "redis cache doc", Normalized: "redis cache doc",
		Status: knowledge.StatusActive, Confidence: 0.7, CreatedAt: time.Now(),
	}
	if err := ms.Save(ctx, obj); err != nil {
		t.Fatalf("seed save: %v", err)
	}
	if err := ms.SaveRepresentation(ctx, &knowledge.Representation{
		ID: "rep_akg:vec:1", ObjectID: "akg:vec:1", Model: "fake",
		Dimension: 2, Vector: []float32{1.0, 0.0}, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed representation: %v", err)
	}

	p := store.New("akg_store", ms, fakeEmbedding{}, "fake", "ns")
	objCh, errCh := p.Stream(ctx, knowledge.Intent{
		Goal:  "redis",
		Scope: knowledge.Scope{MaxObjects: 10},
	})

	var got []*knowledge.KnowledgeObject
	for o := range objCh {
		got = append(got, o)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if len(got) != 1 || got[0].ID != "akg:vec:1" {
		t.Errorf("expected akg:vec:1 recalled via vector, got %v", got)
	}
	// StoreProvider must populate Relevance from FinalScore so the
	// retriever's collectSnippets (which filters on Relevance) does not
	// drop AKG-distilled facts as zero-relevance noise.
	if got[0].Relevance <= 0 {
		t.Errorf("expected Relevance > 0 (mirrored from FinalScore), got %v", got[0].Relevance)
	}
}

func TestStoreProvider_Stream_EmbedErrorFallsBackToLexical(t *testing.T) {
	ctx := context.Background()
	ms := memorystore.New()

	seed := &knowledge.KnowledgeObject{
		ID: "akg:lex:1", Type: knowledge.ObjectMemory, Namespace: "ns",
		Summary: "fail fallback lexical", Normalized: "fail fallback lexical",
		Status: knowledge.StatusActive, Confidence: 0.7, CreatedAt: time.Now(),
	}
	if err := ms.Save(ctx, seed); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	// fakeEmbedding returns an error for goal == "fail"; the provider must
	// fall back to lexical-only search and still recall the object via lexical
	// overlap (goal token "fail" matches the object content).
	p := store.New("akg_store", ms, fakeEmbedding{}, "fake", "ns")
	objCh, errCh := p.Stream(ctx, knowledge.Intent{
		Goal:  "fail",
		Scope: knowledge.Scope{MaxObjects: 10},
	})

	var got []*knowledge.KnowledgeObject
	for o := range objCh {
		got = append(got, o)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected lexical-only fallback to still recall the object")
	}
}

func TestStoreProvider_Stream_RespectsContextCancellation(t *testing.T) {
	ctx := context.Background()
	ms := memorystore.New()

	// Seed many objects so the stream has work to interrupt.
	seed := make([]*knowledge.KnowledgeObject, 0, 200)
	for i := 0; i < 200; i++ {
		seed = append(seed, &knowledge.KnowledgeObject{
			ID: "akg:cancel:" + itoa(i), Type: knowledge.ObjectMemory, Namespace: "ns",
			Summary: "cancel token", Normalized: "cancel token",
			Status: knowledge.StatusActive, Confidence: 0.5, CreatedAt: time.Now(),
		})
	}
	if err := ms.Save(ctx, seed...); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	p := store.New("akg_store", ms, nil, "m", "ns")
	cancelCtx, cancel := context.WithCancel(ctx)
	objCh, _ := p.Stream(cancelCtx, knowledge.Intent{
		Goal:  "cancel",
		Scope: knowledge.Scope{MaxObjects: 200},
	})

	// Read one object then cancel; the stream must terminate without panic.
	if o, ok := <-objCh; !ok || o == nil {
		t.Fatal("expected at least one object before cancel")
	}
	cancel()
	for range objCh {
		// Drain without asserting count; cancellation timing is racy.
	}
}

// itoa is a tiny strconv-free int→string helper to keep the test dependency-free.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
