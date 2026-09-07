package context

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Timwood0x10/ares/api/experience"
	"github.com/Timwood0x10/ares/internal/runtime/memory/embedding"
)

// ──────────────────────────── Test doubles ────────────────────────────

// fakeEmbedder is a minimal EmbeddingService that returns a fixed vector
// or a configurable error so tests can drive both success and failure
// paths of the MemoryRetriever embedder fallback.
type fakeEmbedder struct {
	vec []float64
	err error
	// calls records the texts passed to Embed so tests can assert routing.
	calls []string
	mu    sync.Mutex
}

func (f *fakeEmbedder) Embed(_ context.Context, text string) ([]float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, text)
	if f.err != nil {
		return nil, f.err
	}
	if f.vec != nil {
		return f.vec, nil
	}
	return []float64{0.1, 0.2, 0.3}, nil
}

func (f *fakeEmbedder) EmbedWithPrefix(_ context.Context, _, _ string) ([]float64, error) {
	return f.vec, nil
}

func (f *fakeEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float64, error) {
	out := make([][]float64, len(texts))
	for i := range texts {
		out[i] = f.vec
	}
	return out, nil
}

func (f *fakeEmbedder) HealthCheck(_ context.Context) error { return nil }

func (f *fakeEmbedder) GetModel() string { return "fake-model" }

func (f *fakeEmbedder) GetTimeout() time.Duration { return 0 }

// fakePipeline is a minimal EmbeddingPipeline used to exercise the
// pipeline-first embedding path without depending on the real pipeline.
type fakePipeline struct {
	model string
	err   error
}

func (p *fakePipeline) BuildSpec(kind embedding.EmbeddingKind, payload any) (embedding.EmbeddingSpec, error) {
	query, ok := payload.(string)
	if !ok {
		return embedding.EmbeddingSpec{}, errors.New("fakePipeline: payload must be string")
	}
	return embedding.BuildMemoryQuerySpec(query, p.model, 1, 0), nil
}

func (p *fakePipeline) Embed(_ context.Context, _ embedding.EmbeddingSpec) ([]float64, error) {
	if p.err != nil {
		return nil, p.err
	}
	return []float64{0.5, 0.5, 0.5}, nil
}

func (p *fakePipeline) Model() string { return p.model }

// fakeRepo is a configurable ExperienceRepository mock. It records the
// last SearchByVector arguments so tests can assert tenant and limit
// propagation, and returns the preconfigured experiences (or error).
type fakeRepo struct {
	experiences []experience.Experience
	err         error

	lastVector  []float64
	lastTenant  string
	lastLimit   int
	searchCalls int
	mu          sync.Mutex
}

func (r *fakeRepo) SearchByVector(_ context.Context, vector []float64, tenantID string, limit int) ([]experience.Experience, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.searchCalls++
	r.lastVector = append([]float64(nil), vector...)
	r.lastTenant = tenantID
	r.lastLimit = limit
	if r.err != nil {
		return nil, r.err
	}
	out := make([]experience.Experience, len(r.experiences))
	copy(out, r.experiences)
	return out, nil
}

func (r *fakeRepo) GetByMemoryType(_ context.Context, _ string, _ experience.MemoryType) ([]experience.Experience, error) {
	return nil, nil
}

func (r *fakeRepo) CountByMemoryType(_ context.Context, _ string, _ experience.MemoryType) (int, error) {
	return 0, nil
}

func (r *fakeRepo) Update(_ context.Context, _ *experience.Experience) error { return nil }

func (r *fakeRepo) Delete(_ context.Context, _ string) error { return nil }

func (r *fakeRepo) DeleteBatch(_ context.Context, _ []string) error { return nil }

func (r *fakeRepo) Create(_ context.Context, _ *experience.Experience) error { return nil }

// ──────────────────────────── NewMemoryRetriever ────────────────────────────

func TestNewMemoryRetriever_Validation(t *testing.T) {
	t.Run("nil embedder and pipeline returns error", func(t *testing.T) {
		_, err := NewMemoryRetriever(nil, nil, &fakeRepo{}, "tenant-1", 0.5)
		if err == nil {
			t.Fatal("expected error when embedder and pipeline are both nil")
		}
	})
	t.Run("nil repo returns error even with embedder", func(t *testing.T) {
		_, err := NewMemoryRetriever(&fakeEmbedder{}, nil, nil, "tenant-1", 0.5)
		if err == nil {
			t.Fatal("expected error when experience repository is nil")
		}
	})
	t.Run("nil repo returns error even with pipeline", func(t *testing.T) {
		_, err := NewMemoryRetriever(nil, &fakePipeline{model: "m"}, nil, "tenant-1", 0.5)
		if err == nil {
			t.Fatal("expected error when experience repository is nil")
		}
	})
	t.Run("empty tenantID defaults to default", func(t *testing.T) {
		r, err := NewMemoryRetriever(&fakeEmbedder{}, nil, &fakeRepo{}, "", 0.5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if r.tenantID != memoryDefaultTenant {
			t.Errorf("tenantID = %q, want %q", r.tenantID, memoryDefaultTenant)
		}
	})
	t.Run("non-positive minScore defaults to 0.4", func(t *testing.T) {
		r, err := NewMemoryRetriever(&fakeEmbedder{}, nil, &fakeRepo{}, "t", 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if r.minScore != DefaultMinScore {
			t.Errorf("minScore = %v, want %v", r.minScore, DefaultMinScore)
		}
	})
	t.Run("explicit tenantID and minScore preserved", func(t *testing.T) {
		r, err := NewMemoryRetriever(&fakeEmbedder{}, nil, &fakeRepo{}, "tenant-7", 0.9)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if r.tenantID != "tenant-7" {
			t.Errorf("tenantID = %q, want %q", r.tenantID, "tenant-7")
		}
		if r.minScore != 0.9 {
			t.Errorf("minScore = %v, want 0.9", r.minScore)
		}
	})
	t.Run("pipeline-only construction succeeds", func(t *testing.T) {
		r, err := NewMemoryRetriever(nil, &fakePipeline{model: "m"}, &fakeRepo{}, "t", 0.5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if r.pipeline == nil {
			t.Error("pipeline should be set")
		}
	})
}

// ──────────────────────────── Retrieve ────────────────────────────

func TestMemoryRetriever_Retrieve_EmptyInput(t *testing.T) {
	r, err := NewMemoryRetriever(&fakeEmbedder{}, nil, &fakeRepo{}, "t", 0.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	t.Run("empty string returns empty slice, no embed call", func(t *testing.T) {
		snippets, err := r.Retrieve(context.Background(), "", 5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if snippets == nil {
			t.Fatal("expected non-nil empty slice for empty input")
		}
		if len(snippets) != 0 {
			t.Errorf("expected 0 snippets, got %d", len(snippets))
		}
	})
	t.Run("whitespace-only input is not treated as empty", func(t *testing.T) {
		fe := &fakeEmbedder{}
		repo := &fakeRepo{}
		r, err := NewMemoryRetriever(fe, nil, repo, "t", 0.5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		snippets, err := r.Retrieve(context.Background(), "   ", 5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(fe.calls) != 1 {
			t.Errorf("expected embed to be called once for whitespace input, got %d", len(fe.calls))
		}
		if len(snippets) != 0 {
			t.Errorf("expected 0 snippets for empty repo, got %d", len(snippets))
		}
	})
}

func TestMemoryRetriever_Retrieve_Success(t *testing.T) {
	exps := []experience.Experience{
		{
			ID:               "exp-1",
			Problem:          "How to retry a flaky HTTP call",
			Solution:         "Use exponential backoff with jitter",
			Confidence:       0.8,
			ExtractionMethod: experience.ExtractionDirect,
		},
		{
			ID:               "exp-2",
			Problem:          "How to handle context cancellation",
			Solution:         "Propagate ctx and select on ctx.Done",
			Confidence:       0.6,
			ExtractionMethod: experience.ExtractionCrossTurn,
		},
	}
	repo := &fakeRepo{experiences: exps}
	embedder := &fakeEmbedder{}

	t.Run("embedder fallback path", func(t *testing.T) {
		r, err := NewMemoryRetriever(embedder, nil, repo, "tenant-1", 0.5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		snippets, err := r.Retrieve(context.Background(), "retry flaky HTTP", 5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(snippets) != 2 {
			t.Fatalf("expected 2 snippets, got %d", len(snippets))
		}

		// Sorted by Score descending.
		if snippets[0].Score < snippets[1].Score {
			t.Errorf("snippets not sorted by score descending: %v then %v",
				snippets[0].Score, snippets[1].Score)
		}
		if snippets[0].Source != "experience" {
			t.Errorf("expected source 'experience', got %q", snippets[0].Source)
		}
		if snippets[0].Content == "" {
			t.Error("expected non-empty content")
		}
		if snippets[0].Metadata["id"] != "exp-1" {
			t.Errorf("expected metadata id 'exp-1', got %v", snippets[0].Metadata["id"])
		}

		// Assert repository was called with the embedder's vector and the
		// configured tenant, and that topK propagated.
		if repo.lastTenant != "tenant-1" {
			t.Errorf("tenant = %q, want %q", repo.lastTenant, "tenant-1")
		}
		if repo.lastLimit != 5 {
			t.Errorf("limit = %d, want 5", repo.lastLimit)
		}
		if len(repo.lastVector) == 0 {
			t.Error("expected non-empty vector passed to repo")
		}
	})

	t.Run("pipeline-first path preferred over embedder", func(t *testing.T) {
		embedder := &fakeEmbedder{}
		r, err := NewMemoryRetriever(embedder, &fakePipeline{model: "pipe-model"}, repo, "t", 0.5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		_, err = r.Retrieve(context.Background(), "retry flaky HTTP", 5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(embedder.calls) != 0 {
			t.Errorf("embedder should not be called when pipeline is set; got %d calls",
				len(embedder.calls))
		}
	})

	t.Run("topK <= 0 defaults to 5", func(t *testing.T) {
		repo := &fakeRepo{experiences: exps}
		r, err := NewMemoryRetriever(&fakeEmbedder{}, nil, repo, "t", 0.5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		_, err = r.Retrieve(context.Background(), "query", 0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if repo.lastLimit != DefaultTopK {
			t.Errorf("limit = %d, want %d", repo.lastLimit, DefaultTopK)
		}
	})
}

func TestMemoryRetriever_Retrieve_EmbedFailure(t *testing.T) {
	t.Run("embedder failure returns wrapped error", func(t *testing.T) {
		embedErr := errors.New("embedding service down")
		embedder := &fakeEmbedder{err: embedErr}
		r, err := NewMemoryRetriever(embedder, nil, &fakeRepo{}, "t", 0.5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		_, retrieveErr := r.Retrieve(context.Background(), "some query", 5)
		if retrieveErr == nil {
			t.Fatal("expected error when embedding fails")
		}
		if !errors.Is(retrieveErr, embedErr) {
			t.Errorf("expected wrapped error to contain embedErr; got %v", retrieveErr)
		}
	})

	t.Run("pipeline failure returns wrapped error", func(t *testing.T) {
		pipeErr := errors.New("pipeline down")
		r, err := NewMemoryRetriever(nil, &fakePipeline{model: "m", err: pipeErr}, &fakeRepo{}, "t", 0.5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		_, retrieveErr := r.Retrieve(context.Background(), "some query", 5)
		if retrieveErr == nil {
			t.Fatal("expected error when pipeline embed fails")
		}
		if !errors.Is(retrieveErr, pipeErr) {
			t.Errorf("expected wrapped error to contain pipeErr; got %v", retrieveErr)
		}
	})

	t.Run("repository failure returns wrapped error", func(t *testing.T) {
		repoErr := errors.New("repo unreachable")
		repo := &fakeRepo{err: repoErr}
		r, err := NewMemoryRetriever(&fakeEmbedder{}, nil, repo, "t", 0.5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		_, retrieveErr := r.Retrieve(context.Background(), "some query", 5)
		if retrieveErr == nil {
			t.Fatal("expected error when repo fails")
		}
		if !errors.Is(retrieveErr, repoErr) {
			t.Errorf("expected wrapped error to contain repoErr; got %v", retrieveErr)
		}
	})
}

func TestMemoryRetriever_Retrieve_MinScoreFilter(t *testing.T) {
	exps := []experience.Experience{
		{ID: "high", Problem: "p1", Solution: "s1", Confidence: 0.9},
		{ID: "mid", Problem: "p2", Solution: "s2", Confidence: 0.5},
		{ID: "low", Problem: "p3", Solution: "s3", Confidence: 0.2},
		{ID: "boundary", Problem: "p4", Solution: "s4", Confidence: 0.4},
	}
	repo := &fakeRepo{experiences: exps}

	t.Run("snippets below minScore are dropped, boundary kept", func(t *testing.T) {
		r, err := NewMemoryRetriever(&fakeEmbedder{}, nil, repo, "t", 0.4)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		snippets, err := r.Retrieve(context.Background(), "query", 5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		ids := make(map[string]bool, len(snippets))
		for _, s := range snippets {
			ids[s.Metadata["id"].(string)] = true
			if s.Score < 0.4 {
				t.Errorf("snippet id=%s below minScore was returned (score=%v)",
					s.Metadata["id"], s.Score)
			}
		}
		if !ids["high"] || !ids["mid"] || !ids["boundary"] {
			t.Errorf("expected high/mid/boundary to survive; got %v", ids)
		}
		if ids["low"] {
			t.Errorf("snippet 'low' (confidence 0.2) should have been filtered out")
		}
	})

	t.Run("all snippets filtered yields empty slice not nil", func(t *testing.T) {
		repo := &fakeRepo{experiences: []experience.Experience{
			{ID: "low", Problem: "p", Solution: "s", Confidence: 0.1},
		}}
		r, err := NewMemoryRetriever(&fakeEmbedder{}, nil, repo, "t", 0.5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		snippets, err := r.Retrieve(context.Background(), "query", 5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if snippets == nil {
			t.Fatal("expected non-nil empty slice when all filtered")
		}
		if len(snippets) != 0 {
			t.Errorf("expected 0 snippets, got %d", len(snippets))
		}
	})

	t.Run("confidence clamped to [0,1]", func(t *testing.T) {
		repo := &fakeRepo{experiences: []experience.Experience{
			{ID: "neg", Problem: "p", Solution: "s", Confidence: -1.5},
			{ID: "over", Problem: "p", Solution: "s", Confidence: 2.0},
			{ID: "ok", Problem: "p", Solution: "s", Confidence: 0.6},
		}}
		r, err := NewMemoryRetriever(&fakeEmbedder{}, nil, repo, "t", 0.0)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Force minScore to 0 so clamping is observable rather than filtered.
		r.minScore = 0
		snippets, err := r.Retrieve(context.Background(), "query", 5)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		scores := make(map[string]float64, len(snippets))
		for _, s := range snippets {
			scores[s.Metadata["id"].(string)] = s.Score
		}
		if scores["neg"] != 0 {
			t.Errorf("negative confidence should clamp to 0; got %v", scores["neg"])
		}
		if scores["over"] != 1 {
			t.Errorf("over-1 confidence should clamp to 1; got %v", scores["over"])
		}
		if scores["ok"] != 0.6 {
			t.Errorf("in-range confidence should be preserved; got %v", scores["ok"])
		}
	})
}

func TestMemoryRetriever_Retrieve_TopKLimit(t *testing.T) {
	// Five experiences with distinct scores so the topK cut is observable.
	exps := []experience.Experience{
		{ID: "a", Problem: "p", Solution: "s", Confidence: 0.9},
		{ID: "b", Problem: "p", Solution: "s", Confidence: 0.8},
		{ID: "c", Problem: "p", Solution: "s", Confidence: 0.7},
		{ID: "d", Problem: "p", Solution: "s", Confidence: 0.6},
		{ID: "e", Problem: "p", Solution: "s", Confidence: 0.5},
	}
	repo := &fakeRepo{experiences: exps}
	r, err := NewMemoryRetriever(&fakeEmbedder{}, nil, repo, "t", 0.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r.minScore = 0 // disable filter to isolate topK behavior

	snippets, err := r.Retrieve(context.Background(), "query", 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(snippets) != 3 {
		t.Fatalf("expected 3 snippets, got %d", len(snippets))
	}

	// Verify they are the top 3 by score descending.
	wantOrder := []string{"a", "b", "c"}
	for i, want := range wantOrder {
		if snippets[i].Metadata["id"] != want {
			t.Errorf("position %d: want id %q, got %v", i, want, snippets[i].Metadata["id"])
		}
	}
}

// ──────────────────────────── Concurrent use ────────────────────────────

func TestMemoryRetriever_Retrieve_ConcurrentSafe(t *testing.T) {
	exps := []experience.Experience{
		{ID: "exp-1", Problem: "p", Solution: "s", Confidence: 0.7},
	}
	repo := &fakeRepo{experiences: exps}
	r, err := NewMemoryRetriever(&fakeEmbedder{}, nil, repo, "t", 0.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	const goroutines = 20
	g, ctx := errgroup.WithContext(context.Background())
	for i := 0; i < goroutines; i++ {
		g.Go(func() error {
			_, err := r.Retrieve(ctx, "query", 5)
			return err
		})
	}
	if err := g.Wait(); err != nil {
		t.Errorf("unexpected error in concurrent Retrieve: %v", err)
	}
	if repo.searchCalls != goroutines {
		t.Errorf("expected %d repo calls, got %d", goroutines, repo.searchCalls)
	}
}

// ──────────────────────────── Content formatting ────────────────────────────

func TestFormatExperienceContent(t *testing.T) {
	cases := []struct {
		name     string
		problem  string
		solution string
		want     string
	}{
		{name: "both filled", problem: "p", solution: "s",
			want: "Problem: p\nSolution: s"},
		{name: "only problem", problem: "p", solution: "",
			want: "Problem: p"},
		{name: "only solution", problem: "", solution: "s",
			want: "Solution: s"},
		{name: "both empty", problem: "", solution: "",
			want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatExperienceContent(tc.problem, tc.solution)
			if got != tc.want {
				t.Errorf("formatExperienceContent(%q, %q) = %q, want %q",
					tc.problem, tc.solution, got, tc.want)
			}
		})
	}
}

// ──────────────────────────── DedupSnippets ────────────────────────────

func TestDedupSnippets(t *testing.T) {
	t.Run("nil input returns nil", func(t *testing.T) {
		if got := DedupSnippets(nil); got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})
	t.Run("empty input returns nil", func(t *testing.T) {
		if got := DedupSnippets([]ContextSnippet{}); got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})
	t.Run("dedup by Source+Content keeps highest score", func(t *testing.T) {
		in := []ContextSnippet{
			{Source: "experience", Content: "same", Score: 0.5},
			{Source: "experience", Content: "same", Score: 0.9},
			{Source: "experience", Content: "same", Score: 0.7},
			{Source: "experience", Content: "different", Score: 0.3},
			{Source: "memory", Content: "same", Score: 0.2}, // different Source, kept
		}
		got := DedupSnippets(in)
		if len(got) != 3 {
			t.Fatalf("expected 3 snippets after dedup, got %d", len(got))
		}

		// Find the kept "experience/same" entry; it should be the highest score.
		var sameScore float64
		var sameCount int
		for _, s := range got {
			if s.Source == "experience" && s.Content == "same" {
				sameCount++
				sameScore = s.Score
			}
		}
		if sameCount != 1 {
			t.Errorf("expected exactly one experience/same snippet, got %d", sameCount)
		}
		if sameScore != 0.9 {
			t.Errorf("expected highest score 0.9 to be kept, got %v", sameScore)
		}
	})
	t.Run("preserves first-occurrence order", func(t *testing.T) {
		in := []ContextSnippet{
			{Source: "a", Content: "1", Score: 0.1},
			{Source: "b", Content: "2", Score: 0.2},
			{Source: "c", Content: "3", Score: 0.3},
		}
		got := DedupSnippets(in)
		if len(got) != 3 {
			t.Fatalf("expected 3 snippets, got %d", len(got))
		}
		for i, want := range []string{"a", "b", "c"} {
			if got[i].Source != want {
				t.Errorf("position %d: want source %q, got %q", i, want, got[i].Source)
			}
		}
	})
	t.Run("ties keep earliest entry", func(t *testing.T) {
		// Two snippets with identical Source/Content/Score. The first
		// metadata map should win (stable left-to-right).
		first := map[string]any{"ord": "first"}
		second := map[string]any{"ord": "second"}
		in := []ContextSnippet{
			{Source: "x", Content: "y", Score: 0.5, Metadata: first},
			{Source: "x", Content: "y", Score: 0.5, Metadata: second},
		}
		got := DedupSnippets(in)
		if len(got) != 1 {
			t.Fatalf("expected 1 snippet, got %d", len(got))
		}
		if got[0].Metadata["ord"] != "first" {
			t.Errorf("expected first entry to win ties; got %v", got[0].Metadata["ord"])
		}
	})
}

func TestSortSnippetsByScore(t *testing.T) {
	in := []ContextSnippet{
		{Source: "a", Content: "low", Score: 0.1},
		{Source: "b", Content: "high", Score: 0.9},
		{Source: "c", Content: "mid", Score: 0.5},
		{Source: "d", Content: "mid2", Score: 0.5},
	}
	SortSnippetsByScore(in)

	want := []float64{0.9, 0.5, 0.5, 0.1}
	for i, w := range want {
		if in[i].Score != w {
			t.Errorf("position %d: want score %v, got %v", i, w, in[i].Score)
		}
	}
	// Stable: mid should come before mid2 (original order).
	if in[1].Content != "mid" || in[2].Content != "mid2" {
		t.Errorf("stable sort did not preserve order for equal scores: %v then %v",
			in[1].Content, in[2].Content)
	}
}

func TestSortSnippetsByScore_NilSafe(t *testing.T) {
	// Should not panic on nil or empty slice.
	SortSnippetsByScore(nil)
	SortSnippetsByScore([]ContextSnippet{})
}
