package ares_bootstrap

import (
	"context"
	"testing"

	"github.com/Timwood0x10/ares/internal/agents"
	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
)

func TestNewStrategySource_Nil(t *testing.T) {
	if src := NewStrategySource(nil); src != nil {
		t.Errorf("expected nil source for nil store, got %v", src)
	}
}

type fakeStore struct {
	active *evolution.Strategy
}

func (f *fakeStore) GetActive(ctx context.Context) (*evolution.Strategy, error) {
	return f.active, nil
}
func (f *fakeStore) SetActive(ctx context.Context, s *evolution.Strategy) error {
	return nil
}
func (f *fakeStore) GetHistory(ctx context.Context, id string, n int) ([]*evolution.Strategy, error) {
	return nil, nil
}

var _ evolution.StrategyStore = (*fakeStore)(nil)

func TestStrategySource_Converts(t *testing.T) {
	src := NewStrategySource(&fakeStore{
		active: &evolution.Strategy{
			ID:             "gen-7",
			PromptTemplate: "be concise",
			Params:         map[string]any{"temperature": 0.4},
		},
	})
	if src == nil {
		t.Fatal("expected non-nil source")
	}
	got, err := src.GetActiveStrategy(context.Background())
	if err != nil {
		t.Fatalf("GetActiveStrategy error = %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil active strategy")
	}
	if got.ID != "gen-7" {
		t.Errorf("ID = %q, want gen-7", got.ID)
	}
	if got.Prompt != "be concise" {
		t.Errorf("Prompt = %q, want 'be concise'", got.Prompt)
	}
	if got.Params["temperature"].(float64) != 0.4 {
		t.Errorf("Params = %+v", got.Params)
	}
}

func TestStrategySource_NilActive(t *testing.T) {
	src := NewStrategySource(&fakeStore{active: nil})
	got, err := src.GetActiveStrategy(context.Background())
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if got != nil {
		t.Errorf("expected nil strategy, got %+v", got)
	}
}

var _ agents.StrategySource = (*evolutionStrategySource)(nil)
