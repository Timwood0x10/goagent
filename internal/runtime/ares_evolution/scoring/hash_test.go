package scoring

import (
	"testing"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

const (
	testPromptThink    = "think"
	testPromptTemplate = "test"
)

func makeStrategy(params map[string]any, prompt string) *mutation.Strategy {
	return &mutation.Strategy{
		ID:             "test-id",
		ParentID:       "parent-id",
		Version:        3,
		Name:           "test-name",
		Params:         params,
		PromptTemplate: prompt,
		Score:          42.5,
	}
}

func TestStrategyHash_NilStrategy(t *testing.T) {
	hash, err := StrategyHash(nil)
	if err == nil {
		t.Fatal("expected error for nil strategy, got nil")
	}
	if hash != 0 {
		t.Fatalf("expected hash 0 for nil strategy, got %d", hash)
	}
	if err != ErrNilStrategy {
		t.Fatalf("expected ErrNilStrategy, got %v", err)
	}
}

func TestStrategyHash_SameContentSameHash(t *testing.T) {
	s1 := makeStrategy(map[string]any{testParamTemperature: 0.7, "top_k": 40}, testPromptThink)
	s2 := makeStrategy(map[string]any{testParamTemperature: 0.7, "top_k": 40}, testPromptThink)

	h1, err := StrategyHash(s1)
	if err != nil {
		t.Fatalf("StrategyHash(s1) failed: %v", err)
	}
	h2, err := StrategyHash(s2)
	if err != nil {
		t.Fatalf("StrategyHash(s2) failed: %v", err)
	}
	if h1 != h2 {
		t.Fatalf("same content should produce same hash: h1=%d, h2=%d", h1, h2)
	}
}

func TestStrategyHash_DifferentContentDifferentHash(t *testing.T) {
	tests := []struct {
		name  string
		giveA *mutation.Strategy
		giveB *mutation.Strategy
	}{
		{
			name:  "different temperature",
			giveA: makeStrategy(map[string]any{testParamTemperature: 0.7}, testPromptThink),
			giveB: makeStrategy(map[string]any{testParamTemperature: 0.5}, testPromptThink),
		},
		{
			name:  "different param key",
			giveA: makeStrategy(map[string]any{testParamTemperature: 0.7}, testPromptThink),
			giveB: makeStrategy(map[string]any{"top_k": 40}, testPromptThink),
		},
		{
			name:  "different prompt",
			giveA: makeStrategy(map[string]any{testParamTemperature: 0.7}, testPromptThink),
			giveB: makeStrategy(map[string]any{testParamTemperature: 0.7}, "creative"),
		},
		{
			name:  "empty vs non-empty params",
			giveA: makeStrategy(map[string]any{}, testPromptThink),
			giveB: makeStrategy(map[string]any{testParamTemperature: 0.7}, testPromptThink),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h1, _ := StrategyHash(tt.giveA)
			h2, _ := StrategyHash(tt.giveB)

			if h1 == h2 {
				t.Fatalf("different strategies should produce different hashes: both = %d", h1)
			}
		})
	}
}

func TestStrategyHash_MetadataIgnored(t *testing.T) {
	base := &mutation.Strategy{
		ID:             "id-aaa",
		ParentID:       "parent-111",
		Version:        5,
		Name:           "name-alpha",
		Params:         map[string]any{testParamTemperature: 0.7},
		PromptTemplate: testPromptThink,
		Score:          99.9,
	}

	variants := []*mutation.Strategy{
		{
			ID:             "id-bbb",
			ParentID:       "parent-222",
			Version:        99,
			Name:           "name-beta",
			Params:         map[string]any{testParamTemperature: 0.7},
			PromptTemplate: testPromptThink,
			Score:          -1,
		},
		{
			ID:             "id-ccc",
			ParentID:       "",
			Version:        0,
			Name:           "",
			Params:         map[string]any{testParamTemperature: 0.7},
			PromptTemplate: testPromptThink,
			Score:          0,
		},
	}

	baseHash, _ := StrategyHash(base)

	for i, v := range variants {
		vHash, _ := StrategyHash(v)
		if baseHash != vHash {
			t.Errorf("variant %d: metadata change should not affect hash: base=%d, variant=%d", i, baseHash, vHash)
		}
	}
}

func TestStrategyHash_ParamOrderIndependence(t *testing.T) {
	s1 := &mutation.Strategy{
		Params:         map[string]any{"z_param": 1, "a_param": 2, "m_param": 3},
		PromptTemplate: testPromptTemplate,
	}
	s2 := &mutation.Strategy{
		Params:         map[string]any{"a_param": 2, "m_param": 3, "z_param": 1},
		PromptTemplate: testPromptTemplate,
	}

	h1, _ := StrategyHash(s1)
	h2, _ := StrategyHash(s2)

	if h1 != h2 {
		t.Fatalf("param insertion order should not affect hash: h1=%d, h2=%d", h1, h2)
	}
}

func TestStrategyHash_NilParams(t *testing.T) {
	s := &mutation.Strategy{
		Params:         nil,
		PromptTemplate: "prompt-only",
	}

	hash, err := StrategyHash(s)
	if err != nil {
		t.Fatalf("StrategyHash with nil params failed: %v", err)
	}
	if hash == 0 {
		t.Fatal("hash should be non-zero for non-nil strategy with nil params")
	}
}

func TestStrategyHash_NonZeroHash(t *testing.T) {
	s := makeStrategy(map[string]any{"key": "value"}, "template")

	hash, err := StrategyHash(s)
	if err != nil {
		t.Fatalf("StrategyHash failed: %v", err)
	}
	if hash == 0 {
		t.Fatal("non-nil strategy should produce non-zero hash")
	}
}
