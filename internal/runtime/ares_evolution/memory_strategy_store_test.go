package evolution

import (
	"context"
	"sync"
	"testing"
)

func TestMemoryStrategyStore_Empty(t *testing.T) {
	s := NewMemoryStrategyStore(0)
	got, err := s.GetActive(context.Background())
	if err != nil {
		t.Fatalf("GetActive error = %v", err)
	}
	if got != nil {
		t.Errorf("expected nil active strategy, got %+v", got)
	}
}

func TestMemoryStrategyStore_SetGet(t *testing.T) {
	s := NewMemoryStrategyStore(0)
	in := &Strategy{
		ID:             "s1",
		Version:        1,
		Params:         map[string]any{"temperature": 0.5},
		PromptTemplate: "act as X",
	}
	if err := s.SetActive(context.Background(), in); err != nil {
		t.Fatalf("SetActive error = %v", err)
	}
	got, err := s.GetActive(context.Background())
	if err != nil {
		t.Fatalf("GetActive error = %v", err)
	}
	if got == nil || got.ID != "s1" {
		t.Fatalf("unexpected active: %+v", got)
	}
	if got.PromptTemplate != "act as X" {
		t.Errorf("PromptTemplate = %q", got.PromptTemplate)
	}

	// Deep-copy isolation: mutating the input after storing must not leak in.
	in.Params["temperature"] = 0.9
	in.PromptTemplate = "mutated"
	got2, _ := s.GetActive(context.Background())
	if got2.PromptTemplate != "act as X" {
		t.Errorf("stored value was mutated: %q", got2.PromptTemplate)
	}
	if got2.Params["temperature"].(float64) != 0.5 {
		t.Errorf("stored params mutated: %v", got2.Params["temperature"])
	}
}

func TestMemoryStrategyStore_History(t *testing.T) {
	s := NewMemoryStrategyStore(0)
	mk := func(v int) *Strategy { return &Strategy{ID: "s1", Version: v} }
	for v := 1; v <= 3; v++ {
		if err := s.SetActive(context.Background(), mk(v)); err != nil {
			t.Fatalf("SetActive v=%d: %v", v, err)
		}
	}
	hist, err := s.GetHistory(context.Background(), "s1", 0)
	if err != nil {
		t.Fatalf("GetHistory error = %v", err)
	}
	if len(hist) != 3 {
		t.Fatalf("history len = %d, want 3", len(hist))
	}
	if hist[0].Version != 3 {
		t.Errorf("history[0].Version = %d, want 3 (newest first)", hist[0].Version)
	}

	lim, _ := s.GetHistory(context.Background(), "s1", 1)
	if len(lim) != 1 || lim[0].Version != 3 {
		t.Errorf("limited history = %+v, want [{3}]", lim)
	}

	other, _ := s.GetHistory(context.Background(), "nope", 0)
	if len(other) != 0 {
		t.Errorf("other history len = %d, want 0", len(other))
	}
}

func TestMemoryStrategyStore_MaxHistory(t *testing.T) {
	s := NewMemoryStrategyStore(2)
	for v := 1; v <= 4; v++ {
		_ = s.SetActive(context.Background(), &Strategy{ID: "s1", Version: v})
	}
	hist, _ := s.GetHistory(context.Background(), "s1", 0)
	if len(hist) != 2 {
		t.Fatalf("trimmed history len = %d, want 2", len(hist))
	}
	if hist[0].Version != 4 || hist[1].Version != 3 {
		t.Errorf("trimmed history = %+v, want [4,3]", hist)
	}
}

func TestMemoryStrategyStore_SetActiveNil(t *testing.T) {
	s := NewMemoryStrategyStore(0)
	if err := s.SetActive(context.Background(), nil); err == nil {
		t.Error("expected error for nil strategy")
	}
}

func TestMemoryStrategyStore_Concurrent(t *testing.T) {
	s := NewMemoryStrategyStore(0)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(v int) {
			defer wg.Done()
			_ = s.SetActive(context.Background(), &Strategy{ID: "s1", Version: v})
			_, _ = s.GetActive(context.Background())
			_, _ = s.GetHistory(context.Background(), "s1", 0)
		}(i)
	}
	wg.Wait()
}
