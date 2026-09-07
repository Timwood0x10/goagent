package evolution

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/knowledge"
	ares_evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
)

// mockStrategyStore implements StrategyStore for tests.
type mockStrategyStore struct {
	mu      sync.Mutex
	active  *ares_evolution.Strategy
	history []*ares_evolution.Strategy
}

func (m *mockStrategyStore) GetActive(_ context.Context) (*ares_evolution.Strategy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active, nil
}

func (m *mockStrategyStore) GetHistory(_ context.Context, _ string, n int) ([]*ares_evolution.Strategy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n > len(m.history) {
		n = len(m.history)
	}
	return m.history[:n], nil
}

func TestNew(t *testing.T) {
	p := New("evo", &mockStrategyStore{})
	if p.Name() != "evo" {
		t.Fatalf("expected name 'evo', got %q", p.Name())
	}
}

func TestNewEmptyName(t *testing.T) {
	p := New("", &mockStrategyStore{})
	if p.Name() != "" {
		t.Fatalf("expected empty name, got %q", p.Name())
	}
}

func TestIntentMatch(t *testing.T) {
	p := New("test", &mockStrategyStore{})

	tests := []struct {
		goal    string
		wantGeq float64
	}{
		{"what was the decision", 0.9},
		{"evolution history", 0.9},
		{"strategy for optimization", 0.9},
		{"why did we choose this", 0.9},
		{"improve performance", 0.9},
		{"hello world", 0.3},
		{"", 0.3},
	}

	for _, tt := range tests {
		t.Run(tt.goal, func(t *testing.T) {
			got := p.IntentMatch(knowledge.Intent{Goal: tt.goal})
			if got < tt.wantGeq {
				t.Errorf("IntentMatch(%q) = %.2f, want >= %.2f", tt.goal, got, tt.wantGeq)
			}
		})
	}
}

func TestStreamNoActiveStrategy(t *testing.T) {
	store := &mockStrategyStore{}
	p := New("test", store)

	objCh, errCh := p.Stream(context.Background(), knowledge.Intent{Scope: knowledge.Scope{MaxObjects: 10}})
	var objs []*knowledge.KnowledgeObject
	for obj := range objCh {
		objs = append(objs, obj)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	default:
	}

	if len(objs) != 0 {
		t.Fatalf("expected 0 objects for no active strategy, got %d", len(objs))
	}
}

func TestStreamWithActiveStrategy(t *testing.T) {
	now := time.Now()
	store := &mockStrategyStore{
		active: &ares_evolution.Strategy{
			ID:        "s1",
			Version:   5,
			Score:     3.0,
			Params:    map[string]any{"temp": 0.7},
			CreatedAt: now,
		},
		history: []*ares_evolution.Strategy{
			{ID: "s1", Version: 4, Score: 2.5, CreatedAt: now.Add(-24 * time.Hour)},
			{ID: "s1", Version: 3, Score: 2.0, CreatedAt: now.Add(-48 * time.Hour)},
		},
	}
	p := New("evo-test", store)

	objCh, errCh := p.Stream(context.Background(), knowledge.Intent{Scope: knowledge.Scope{MaxObjects: 10}})
	var objs []*knowledge.KnowledgeObject
	for obj := range objCh {
		objs = append(objs, obj)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	default:
	}

	if len(objs) != 3 {
		t.Fatalf("expected 3 objects (1 active + 2 history), got %d", len(objs))
	}

	// First object should be the active strategy (v5).
	if objs[0].Metadata["version"] != 5 {
		t.Errorf("expected first object to be v5 (active), got version %v", objs[0].Metadata["version"])
	}
}

func TestStreamMaxResults(t *testing.T) {
	now := time.Now()
	store := &mockStrategyStore{
		active: &ares_evolution.Strategy{
			ID: "s1", Version: 10, Score: 3.0, CreatedAt: now,
		},
		history: []*ares_evolution.Strategy{
			{ID: "s1", Version: 9, Score: 2.9, CreatedAt: now.Add(-1 * time.Hour)},
			{ID: "s1", Version: 8, Score: 2.8, CreatedAt: now.Add(-2 * time.Hour)},
		},
	}
	p := New("test", store)

	// MaxObjects=1 should only return the active strategy.
	objCh, _ := p.Stream(context.Background(), knowledge.Intent{Scope: knowledge.Scope{MaxObjects: 1}})
	var objs []*knowledge.KnowledgeObject
	for obj := range objCh {
		objs = append(objs, obj)
	}
	if len(objs) != 1 {
		t.Fatalf("expected 1 object (MaxObjects=1), got %d", len(objs))
	}
}

func TestStreamCancelContext(t *testing.T) {
	store := &mockStrategyStore{
		active: &ares_evolution.Strategy{
			ID: "s1", Version: 1, Score: 1.0, CreatedAt: time.Now(),
		},
	}
	p := New("test", store)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	objCh, _ := p.Stream(ctx, knowledge.Intent{Scope: knowledge.Scope{MaxObjects: 10}})
	var objs []*knowledge.KnowledgeObject
	for obj := range objCh {
		objs = append(objs, obj)
	}
	if len(objs) != 0 {
		t.Fatalf("expected 0 objects with cancelled context, got %d", len(objs))
	}
}

// ---- decision-trail consumer with double filtering ----

// mustMustNot... helper builds an evidence record with the given payload.
func mustPayload(t *testing.T, v map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestStreamWithEvidenceStoreLocksDoubleFilter drives the decision-trail
// consumer at the PROVIDER level: the evidence store carries BOTH decision
// records (source="lifecycle", payload has an action) AND ordinary runtime
// fitness samples (same source + KindFitness but NO action). The producer must
// emit only the decision records — the second filter lives in FromDecisionEvidence.
func TestStreamWithEvidenceStoreLocksDoubleFilter(t *testing.T) {
	now := time.Now()
	store := &mockStrategyStore{
		active: &ares_evolution.Strategy{
			ID: "strategy-active", Version: 3, Score: 3.0, CreatedAt: now,
		},
		history: []*ares_evolution.Strategy{
			{ID: "strategy-active", Version: 2, Score: 2.8, CreatedAt: now.Add(-24 * time.Hour)},
		},
	}

	evStore := evidence.NewMemoryStore()
	// Seed: 2 decision records + 3 runtime fitness samples (no action field).
	require.NoError(t, evStore.Append(context.Background(), evidence.Evidence{
		ID: "d_promote_1", Source: "lifecycle", Kind: evidence.KindFitness,
		Payload:   mustPayload(t, map[string]any{"action": "promote", "value": 81.0, "strategy_id": "strategy-v2", "reason": "shadow win 0.72"}),
		Timestamp: now.Add(-1 * time.Minute),
	}))
	require.NoError(t, evStore.Append(context.Background(), evidence.Evidence{
		ID: "d_rollback_1", Source: "lifecycle", Kind: evidence.KindFitness,
		Payload:   mustPayload(t, map[string]any{"action": "rollback", "value": 15.0, "strategy_id": "strategy-v1", "reason": "window mean declined"}),
		Timestamp: now.Add(-2 * time.Minute),
	}))
	for i := 0; i < 3; i++ {
		require.NoError(t, evStore.Append(context.Background(), evidence.Evidence{
			ID: "runtime_sample", Source: "lifecycle", Kind: evidence.KindFitness,
			// No action field — must be dropped by the second filter.
			Payload:   mustPayload(t, map[string]any{"value": 0.8, "component": "scheduler"}),
			Timestamp: now.Add(-3 * time.Minute),
		}))
	}

	p := New("evo", store).WithEvidenceStore(evStore)
	objCh, _ := p.Stream(context.Background(), knowledge.Intent{Scope: knowledge.Scope{MaxObjects: 100}})
	var objs []*knowledge.KnowledgeObject
	for obj := range objCh {
		objs = append(objs, obj)
	}

	// Expected: 2 decision-only KnowledgeObjects. The lineage objects are
	// decisions too, so filter by the presence of an "action" metadata key,
	// which only decision-evidence consumers set.
	var decisions []*knowledge.KnowledgeObject
	for _, o := range objs {
		if _, ok := o.Metadata["action"]; ok {
			decisions = append(decisions, o)
		}
	}
	if len(decisions) != 2 {
		t.Fatalf("expected exactly 2 decision-trail objects, got %d (objs=%d)", len(decisions), len(objs))
	}

	// The 3 runtime fitness samples must NOT leak into the decision trail.
	for _, d := range decisions {
		if len(d.Tags) == 3 {
			hasAction := false
			for _, tag := range d.Tags {
				if tag == "promote" || tag == "rollback" {
					hasAction = true
				}
			}
			if !hasAction {
				t.Errorf("decision object %s missing action tag: tags=%v", d.ID, d.Tags)
			}
		}
	}
}

// TestStreamZeroValueEvStoreKeepsLineageOnly locks the backward-compat contract:
// an EvolutionProvider constructed WITHOUT WithEvidenceStore must stream exactly
// the lineage (active + history) and produce zero decision-trail objects, even
// when a store happens to exist in the underlying strategy store.
func TestStreamZeroValueEvStoreKeepsLineageOnly(t *testing.T) {
	now := time.Now()
	store := &mockStrategyStore{
		active: &ares_evolution.Strategy{
			ID: "s1", Version: 5, Score: 3.0, CreatedAt: now,
		},
		history: []*ares_evolution.Strategy{
			{ID: "s1", Version: 4, Score: 2.5, CreatedAt: now.Add(-24 * time.Hour)},
		},
	}
	// Note: NO WithEvidenceStore call — evStore stays nil.
	p := New("evo", store)

	objCh, _ := p.Stream(context.Background(), knowledge.Intent{Scope: knowledge.Scope{MaxObjects: 100}})
	var objs []*knowledge.KnowledgeObject
	for obj := range objCh {
		objs = append(objs, obj)
	}
	// Only lineage: active v5 + history v4 = 2.
	if len(objs) != 2 {
		t.Fatalf("expected 2 lineage objects, got %d", len(objs))
	}
	for _, o := range objs {
		if _, ok := o.Metadata["action"]; ok {
			t.Fatalf("decision-trail object leaked to lineage-only stream: id=%s", o.ID)
		}
	}
}

// TestStreamEvidenceStoreErrorDegradesToLineageOnly locks the best-effort
// contract: when the evidence store fails, the stream degrades to lineage-only
// output rather than erroring the whole stream.
func TestStreamEvidenceStoreErrorDegradesToLineageOnly(t *testing.T) {
	now := time.Now()
	store := &mockStrategyStore{
		active: &ares_evolution.Strategy{
			ID: "s1", Version: 5, Score: 3.0, CreatedAt: now,
		},
		history: []*ares_evolution.Strategy{
			{ID: "s1", Version: 4, Score: 2.5, CreatedAt: now.Add(-24 * time.Hour)},
		},
	}
	p := New("evo", store).WithEvidenceStore(&errStore{})

	objCh, errCh := p.Stream(context.Background(), knowledge.Intent{Scope: knowledge.Scope{MaxObjects: 100}})
	var objs []*knowledge.KnowledgeObject
	for obj := range objCh {
		objs = append(objs, obj)
	}
	var gotErr error
	select {
	case gotErr = <-errCh:
	default:
	}
	if gotErr != nil {
		t.Fatalf("evStore failure must NOT propagate to the stream: %v", gotErr)
	}
	if len(objs) != 2 {
		t.Fatalf("expected lineage-only 2 objects on store error, got %d", len(objs))
	}
}

// errStore is an evidenceStore whose Query always fails.
type errStore struct{}

func (errStore) Query(context.Context, evidence.Filter) ([]evidence.Evidence, error) {
	return nil, &boomErr{}
}

type boomErr struct{}

func (boomErr) Error() string { return "boom" }

// TestStream_LimitDistributionAcrossSegments_E3 locks the limit contract:
// MaxObjects is the TOTAL cap across the three stream segments (active →
// history → decision trail). An earlier segment must consume from the same
// budget, so a small MaxObjects degrades gracefully to lineage-only output and
// NEVER overflows the caller's requested object count.
func TestStream_LimitDistributionAcrossSegments_E3(t *testing.T) {
	now := time.Now()
	store := &mockStrategyStore{
		active: &ares_evolution.Strategy{ID: "s1", Version: 5, Score: 3.0, CreatedAt: now},
		history: []*ares_evolution.Strategy{
			{ID: "s1", Version: 4, Score: 2.8, CreatedAt: now.Add(-24 * time.Hour)},
			{ID: "s1", Version: 3, Score: 2.5, CreatedAt: now.Add(-48 * time.Hour)},
		},
	}
	evStore := evidence.NewMemoryStore()
	for i := 0; i < 3; i++ {
		require.NoError(t, evStore.Append(context.Background(), evidence.Evidence{
			ID:        "d_promote",
			Source:    "lifecycle",
			Kind:      evidence.KindFitness,
			Payload:   mustPayload(t, map[string]any{"action": "promote", "value": 80.0 + float64(i), "strategy_id": "s1"}),
			Timestamp: now.Add(-time.Duration(i+1) * time.Minute),
		}))
	}
	p := New("evo", store).WithEvidenceStore(evStore)

	collect := func(max int) []*knowledge.KnowledgeObject {
		t.Helper()
		objCh, _ := p.Stream(context.Background(), knowledge.Intent{Scope: knowledge.Scope{MaxObjects: max}})
		var objs []*knowledge.KnowledgeObject
		for obj := range objCh {
			objs = append(objs, obj)
		}
		return objs
	}

	// The total must never exceed MaxObjects, and the segments consume the same
	// budget in order (active first, then history, then decision trail).
	t.Run("max_1_yields_only_active", func(t *testing.T) {
		objs := collect(1)
		require.Len(t, objs, 1)
		assert.Equal(t, "evo_s1_v5", objs[0].ID)
	})

	t.Run("max_3_yields_lineage_only", func(t *testing.T) {
		objs := collect(3)
		require.Len(t, objs, 3) // active + 2 history, budget exhausted
		assert.Equal(t, "evo_s1_v5", objs[0].ID)
	})

	t.Run("max_6_yields_full_three_segments", func(t *testing.T) {
		objs := collect(6) // 1 active + 2 history + 3 decisions
		require.Len(t, objs, 6)
		// Last three carry the decision action metadata.
		for _, o := range objs[3:] {
			assert.Equal(t, "promote", o.Metadata["action"], "decision segment must carry the action")
		}
	})

	t.Run("max_0_defaults_to_20", func(t *testing.T) {
		objs := collect(0)
		assert.Len(t, objs, 6, "unset MaxObjects defaults to a large budget")
	})
}
