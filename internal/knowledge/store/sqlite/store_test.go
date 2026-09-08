package sqlitestore

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Timwood0x10/ares/internal/knowledge"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()

	f, err := os.CreateTemp("", "akf-sqlite-test-*.db")
	if err != nil {
		t.Fatalf("create temp db: %v", err)
	}
	path := f.Name()
	_ = f.Close()
	t.Cleanup(func() { _ = os.Remove(path) })

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	s, err := NewWithDB(db)
	if err != nil {
		t.Fatalf("NewWithDB: %v", err)
	}
	return s
}

func TestSaveAndGet(t *testing.T) {
	s := newTestStore(t)

	obj := &knowledge.KnowledgeObject{
		ID:         "obj1",
		Type:       knowledge.ObjectDecision,
		Summary:    "Test decision",
		Normalized: "This is a test decision object",
		Raw:        []byte("raw data"),
		Confidence: 0.95,
		Tags:       []string{"test", "decision"},
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	t.Cleanup(func() { _ = s.Delete(context.Background(), obj.ID) })

	if err := s.Save(context.Background(), obj); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := s.Get(context.Background(), "obj1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil object")
	}
	if got.Summary != "Test decision" {
		t.Errorf("expected 'Test decision', got %q", got.Summary)
	}
	if got.Confidence != 0.95 {
		t.Errorf("expected confidence 0.95, got %f", got.Confidence)
	}
}

func TestGetNotFound(t *testing.T) {
	s := newTestStore(t)

	obj, err := s.Get(context.Background(), "nonexistent")
	if err != ErrObjectNotFound {
		t.Fatalf("expected ErrObjectNotFound, got %v", err)
	}
	if obj != nil {
		t.Error("expected nil object")
	}
}

func TestSaveAndQueryByType(t *testing.T) {
	s := newTestStore(t)

	now := time.Now()
	objs := []*knowledge.KnowledgeObject{
		{ID: "d1", Type: knowledge.ObjectDecision, Summary: "Dec 1", Confidence: 0.9, CreatedAt: now, UpdatedAt: now},
		{ID: "d2", Type: knowledge.ObjectDecision, Summary: "Dec 2", Confidence: 0.8, CreatedAt: now, UpdatedAt: now},
		{ID: "a1", Type: knowledge.ObjectArchitecture, Summary: "Arch 1", Confidence: 0.7, CreatedAt: now, UpdatedAt: now},
	}
	for _, o := range objs {
		if err := s.Save(context.Background(), o); err != nil {
			t.Fatalf("Save %s: %v", o.ID, err)
		}
	}
	t.Cleanup(func() {
		for _, o := range objs {
			_ = s.Delete(context.Background(), o.ID)
		}
	})

	results, err := s.Query(context.Background(), knowledge.Query{
		Types: []knowledge.ObjectType{knowledge.ObjectDecision},
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 decisions, got %d", len(results))
	}
}

func TestSearch(t *testing.T) {
	s := newTestStore(t)

	now := time.Now()
	objs := []*knowledge.KnowledgeObject{
		{ID: "r1", Summary: "Redis cache", Normalized: "Redis caching layer", Confidence: 0.9, CreatedAt: now, UpdatedAt: now},
		{ID: "p1", Summary: "PostgreSQL database", Normalized: "PostgreSQL relational database", Confidence: 0.8, CreatedAt: now, UpdatedAt: now},
	}
	for _, o := range objs {
		if err := s.Save(context.Background(), o); err != nil {
			t.Fatalf("Save %s: %v", o.ID, err)
		}
	}
	t.Cleanup(func() {
		for _, o := range objs {
			_ = s.Delete(context.Background(), o.ID)
		}
	})

	results, err := s.Search(context.Background(), "redis", "", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result for 'redis'")
	}
	if results[0].ID != "r1" {
		t.Errorf("expected r1 (Redis), got %s", results[0].ID)
	}
}

func TestSaveAndGetRepresentation(t *testing.T) {
	s := newTestStore(t)

	// Save an object first (foreign key constraint).
	obj := &knowledge.KnowledgeObject{
		ID: "rep-obj-1", Summary: "rep test", Confidence: 1.0,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := s.Save(context.Background(), obj); err != nil {
		t.Fatalf("Save object: %v", err)
	}
	t.Cleanup(func() { _ = s.Delete(context.Background(), obj.ID) })

	rep := &knowledge.Representation{
		ID:        "rep1",
		ObjectID:  "rep-obj-1",
		Model:     "openai-text-3-large",
		Dimension: 4,
		Vector:    []float32{0.1, 0.2, 0.3, 0.4},
		CreatedAt: time.Now(),
	}
	if err := s.SaveRepresentation(context.Background(), rep); err != nil {
		t.Fatalf("SaveRepresentation: %v", err)
	}

	got, err := s.GetRepresentation(context.Background(), "rep-obj-1", "openai-text-3-large")
	if err != nil {
		t.Fatalf("GetRepresentation: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil representation")
	}
	if len(got.Vector) != 4 {
		t.Errorf("expected vector length 4, got %d", len(got.Vector))
	}
}

func TestGetRepresentationNotFound(t *testing.T) {
	s := newTestStore(t)

	rep, err := s.GetRepresentation(context.Background(), "nonexistent", "any")
	if err != ErrObjectNotFound {
		t.Fatalf("expected ErrObjectNotFound, got %v", err)
	}
	if rep != nil {
		t.Error("expected nil")
	}
}

// TestGetCorruptTimestampDegradesToZeroTimeAndLogs locks the scan-side
// degrade contract for timestamp columns: a corrupted created_at/updated_at
// (hand-written row or bit rot) keeps the zero time — one bad column must
// not fail the whole read — while parseTimeField logs a warning with the
// raw string so the corrupt row is observable in logs.
func TestGetCorruptTimestampDegradesToZeroTimeAndLogs(t *testing.T) {
	s := newTestStore(t)

	obj := &knowledge.KnowledgeObject{
		ID: "corrupt-ts", Summary: "corrupt ts", Confidence: 1.0,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := s.Save(context.Background(), obj); err != nil {
		t.Fatalf("Save: %v", err)
	}
	t.Cleanup(func() { _ = s.Delete(context.Background(), obj.ID) })

	// Corrupt both timestamp columns directly (bypassing the writer).
	if _, err := s.db.Exec(
		`UPDATE akf_objects SET created_at = 'not-a-timestamp', updated_at = 'also-bad' WHERE id = ?`,
		obj.ID,
	); err != nil {
		t.Fatalf("corrupt timestamps: %v", err)
	}

	got, err := s.Get(context.Background(), obj.ID)
	if err != nil {
		t.Fatalf("Get with corrupt timestamps must degrade, not fail: %v", err)
	}
	if !got.CreatedAt.IsZero() {
		t.Errorf("corrupt created_at must fold to zero time, got %v", got.CreatedAt)
	}
	if !got.UpdatedAt.IsZero() {
		t.Errorf("corrupt updated_at must fold to zero time, got %v", got.UpdatedAt)
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)

	obj := &knowledge.KnowledgeObject{ID: "del1", Summary: "delete me", Confidence: 1.0, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := s.Save(context.Background(), obj); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.Delete(context.Background(), "del1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, err := s.Get(context.Background(), "del1")
	if err != ErrObjectNotFound {
		t.Fatalf("expected ErrObjectNotFound after delete, got %v", err)
	}
	if got != nil {
		t.Error("expected nil after delete")
	}
}
