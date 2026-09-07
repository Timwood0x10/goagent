package genome

import (
	"context"
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// makeTestStrategy is a test helper that creates a Strategy with sensible defaults.
func makeTestStrategy(id string, score float64, version int, params map[string]any, prompt string) *mutation.Strategy {
	return &mutation.Strategy{
		ID:             id,
		ParentID:       "",
		Version:        version,
		Params:         params,
		PromptTemplate: prompt,
		Score:          score,
		CreatedAt:      time.Now(),
	}
}

func TestNewCrossover(t *testing.T) {
	t.Run("default construction succeeds", func(t *testing.T) {
		c, err := NewCrossover()
		if err != nil {
			t.Fatalf("NewCrossover() error = %v", err)
		}
		if c == nil {
			t.Fatal("NewCrossover() returned nil")
		}
		if c.rng == nil {
			t.Fatal("c.rng should not be nil")
		}
	})

	t.Run("with seed option", func(t *testing.T) {
		c, err := NewCrossover(WithSeed(42))
		if err != nil {
			t.Fatalf("NewCrossover(WithSeed(42)) error = %v", err)
		}
		if c == nil {
			t.Fatal("expected non-nil crossover")
		}
	})
}

//nolint:gocyclo // Test function with comprehensive test cases
func TestCrossover(t *testing.T) {
	tests := []struct {
		name        string
		a           *mutation.Strategy
		b           *mutation.Strategy
		seed        int64
		wantErr     bool
		errContains string
		checkChild  func(t *testing.T, child *mutation.Strategy, a, b *mutation.Strategy)
	}{
		{
			name: "basic crossover produces valid child",
			a: makeTestStrategy("parent-a", 0.8, 1, map[string]any{
				"temperature": 0.7,
				"top_k":       40,
			}, "prompt-a"),
			b: makeTestStrategy("parent-b", 0.6, 2, map[string]any{
				"temperature": 0.3,
				"max_steps":   10,
			}, "prompt-b"),
			seed:    42,
			wantErr: false,
			checkChild: func(t *testing.T, child *mutation.Strategy, a, b *mutation.Strategy) {
				if child == nil {
					t.Fatal("child should not be nil")
				}
				if child.ID == "" {
					t.Error("child ID should not be empty")
				}
				if child.Score != -1 {
					t.Errorf("child Score = %v, want -1 (unevaluated)", child.Score)
				}
				if child.PromptTemplate != a.PromptTemplate {
					t.Errorf("child PromptTemplate = %q, want %q (from higher-scoring parent A)", child.PromptTemplate, a.PromptTemplate)
				}
			},
		},
		{
			name:    "child has different ID from both parents",
			a:       makeTestStrategy("aaa-111", 0.5, 1, map[string]any{"temp": 0.5}, "p1"),
			b:       makeTestStrategy("bbb-222", 0.5, 1, map[string]any{"temp": 0.9}, "p2"),
			seed:    99,
			wantErr: false,
			checkChild: func(t *testing.T, child *mutation.Strategy, a, b *mutation.Strategy) {
				if child.ID == a.ID {
					t.Error("child ID should differ from parent A")
				}
				if child.ID == b.ID {
					t.Error("child ID should differ from parent B")
				}
			},
		},
		{
			name:    "child version is greater than both parents",
			a:       makeTestStrategy("a", 0.5, 3, map[string]any{"x": 1}, "p"),
			b:       makeTestStrategy("b", 0.5, 5, map[string]any{"y": 2}, "p"),
			seed:    42,
			wantErr: false,
			checkChild: func(t *testing.T, child *mutation.Strategy, a, b *mutation.Strategy) {
				wantVersion := maxVersion(a.Version, b.Version) + 1
				if child.Version != wantVersion {
					t.Errorf("child Version = %d, want %d", child.Version, wantVersion)
				}
				if child.Version <= a.Version {
					t.Error("child version should be > parent A version")
				}
				if child.Version <= b.Version {
					t.Error("child version should be > parent B version")
				}
			},
		},
		{
			name: "child params are union of parent params",
			a: makeTestStrategy("a", 0.5, 1, map[string]any{
				"temperature": 0.7,
				"top_p":       0.9,
			}, "p1"),
			b: makeTestStrategy("b", 0.5, 1, map[string]any{
				"temperature": 0.3,
				"max_tokens":  100,
			}, "p2"),
			seed:    42,
			wantErr: false,
			checkChild: func(t *testing.T, child *mutation.Strategy, a, b *mutation.Strategy) {
				expectedKeys := map[string]struct{}{
					"temperature": {},
					"top_p":       {},
					"max_tokens":  {},
				}
				for k := range expectedKeys {
					if _, exists := child.Params[k]; !exists {
						t.Errorf("child missing expected key %q", k)
					}
				}
				if len(child.Params) != len(expectedKeys) {
					t.Errorf("child Params length = %d, want %d", len(child.Params), len(expectedKeys))
				}
			},
		},
		{
			name: "crossover with identical parents returns valid child",
			a: makeTestStrategy("same-id", 0.8, 2, map[string]any{
				"temperature": 0.7,
				"top_k":       40,
			}, "same-prompt"),
			b: makeTestStrategy("same-id", 0.8, 2, map[string]any{
				"temperature": 0.7,
				"top_k":       40,
			}, "same-prompt"),
			seed:    42,
			wantErr: false,
			checkChild: func(t *testing.T, child *mutation.Strategy, a, b *mutation.Strategy) {
				if child == nil {
					t.Fatal("child should not be nil for identical parents")
				}
				for k, v := range a.Params {
					cv, ok := child.Params[k]
					if !ok {
						t.Errorf("child missing key %q from identical parent", k)
						continue
					}
					if cv != v {
						t.Errorf("child[%s] = %v, want %v (identical parents)", k, cv, v)
					}
				}
			},
		},
		{
			name: "crossover where one parent has extra params",
			a: makeTestStrategy("a", 0.5, 1, map[string]any{
				"temperature": 0.7,
				"top_k":       40,
				"max_steps":   15,
			}, "pa"),
			b: makeTestStrategy("b", 0.5, 1, map[string]any{
				"temperature": 0.3,
			}, "pb"),
			seed:    42,
			wantErr: false,
			checkChild: func(t *testing.T, child *mutation.Strategy, a, b *mutation.Strategy) {
				// Child must have all keys from A (including extra ones).
				for k := range a.Params {
					if _, ok := child.Params[k]; !ok {
						t.Errorf("child missing key %q from parent A which had extra params", k)
					}
				}
				// Child must have all keys from B.
				for k := range b.Params {
					if _, ok := child.Params[k]; !ok {
						t.Errorf("child missing key %q from parent B", k)
					}
				}
			},
		},
		{
			name: "crossover where params overlap partially",
			a: makeTestStrategy("a", 0.9, 1, map[string]any{
				"shared_param": "value_a",
				"only_a_param": "a_only",
				"another_a":    100,
			}, "prompt_a"),
			b: makeTestStrategy("b", 0.4, 1, map[string]any{
				"shared_param": "value_b",
				"only_b_param": "b_only",
				"another_b":    200,
			}, "prompt_b"),
			seed:    123,
			wantErr: false,
			checkChild: func(t *testing.T, child *mutation.Strategy, a, b *mutation.Strategy) {
				// Check union of keys.
				allKeys := collectParamKeys(a.Params, b.Params)
				if len(child.Params) != len(allKeys) {
					t.Errorf("child param count = %d, want %d (union size)", len(child.Params), len(allKeys))
				}
				// shared_param must come from either A or B.
				sp, ok := child.Params["shared_param"]
				if !ok {
					t.Error("child should have shared_param")
				} else if sp != "value_a" && sp != "value_b" {
					t.Errorf("shared_param = %v, want value_a or value_b", sp)
				}
				// only_a_param and only_b_param must exist.
				if _, ok := child.Params["only_a_param"]; !ok {
					t.Error("child should inherit only_a_param from A")
				}
				if _, ok := child.Params["only_b_param"]; !ok {
					t.Error("child should inherit only_b_param from B")
				}
			},
		},
		{
			name:    "prompt template inherited from higher-scoring parent",
			a:       makeTestStrategy("a", 0.9, 1, map[string]any{"t": 1}, "high_score_prompt"),
			b:       makeTestStrategy("b", 0.3, 1, map[string]any{"t": 2}, "low_score_prompt"),
			seed:    42,
			wantErr: false,
			checkChild: func(t *testing.T, child *mutation.Strategy, a, b *mutation.Strategy) {
				if child.PromptTemplate != a.PromptTemplate {
					t.Errorf("PromptTemplate = %q, want %q (higher-scoring parent A)", child.PromptTemplate, a.PromptTemplate)
				}
			},
		},
		{
			name:    "prompt template from B when B scores higher",
			a:       makeTestStrategy("a", 0.2, 1, map[string]any{"t": 1}, "low_prompt"),
			b:       makeTestStrategy("b", 0.95, 1, map[string]any{"t": 2}, "high_prompt"),
			seed:    42,
			wantErr: false,
			checkChild: func(t *testing.T, child *mutation.Strategy, a, b *mutation.Strategy) {
				if child.PromptTemplate != b.PromptTemplate {
					t.Errorf("PromptTemplate = %q, want %q (higher-scoring parent B)", child.PromptTemplate, b.PromptTemplate)
				}
			},
		},
		{
			name:        "nil parent A returns error",
			a:           nil,
			b:           makeTestStrategy("b", 0.5, 1, map[string]any{"t": 1}, "p"),
			wantErr:     true,
			errContains: "parent strategy must not be nil",
		},
		{
			name:        "nil parent B returns error",
			a:           makeTestStrategy("a", 0.5, 1, map[string]any{"t": 1}, "p"),
			b:           nil,
			wantErr:     true,
			errContains: "parent strategy must not be nil",
		},
		{
			name:        "both nil parents returns error",
			a:           nil,
			b:           nil,
			wantErr:     true,
			errContains: "parent strategy must not be nil",
		},
		{
			name: "deterministic results with same seed",
			a: makeTestStrategy("a", 0.5, 1, map[string]any{
				"p1": 10,
				"p2": 20,
				"p3": 30,
				"p4": 40,
				"p5": 50,
			}, "prompt"),
			b: makeTestStrategy("b", 0.5, 1, map[string]any{
				"p1": 100,
				"p2": 200,
				"p3": 300,
				"p4": 400,
				"p5": 500,
			}, "prompt"),
			seed:    999,
			wantErr: false,
			checkChild: func(t *testing.T, child *mutation.Strategy, a, b *mutation.Strategy) {
				// Run again with same seed and compare.
				c2, _ := NewCrossover(WithSeed(999))
				child2, err := c2.Crossover(context.Background(), a, b)
				if err != nil {
					t.Fatalf("second crossover error: %v", err)
				}
				if len(child.Params) != len(child2.Params) {
					t.Fatalf("param count differs: %d vs %d", len(child.Params), len(child2.Params))
				}
				for k := range child.Params {
					if child.Params[k] != child2.Params[k] {
						t.Errorf("param %q differs between runs: %v vs %v", k, child.Params[k], child2.Params[k])
					}
				}
			},
		},
		{
			name: "different results with different seeds",
			a: makeTestStrategy("a", 0.5, 1, map[string]any{
				"x": 1,
				"y": 2,
				"z": 3,
				"w": 4,
				"v": 5,
				"u": 6,
				"q": 7,
				"r": 8,
				"s": 9,
				"t": 10,
			}, "prompt"),
			b: makeTestStrategy("b", 0.5, 1, map[string]any{
				"x": 11,
				"y": 22,
				"z": 33,
				"w": 44,
				"v": 55,
				"u": 66,
				"q": 77,
				"r": 88,
				"s": 99,
				"t": 100,
			}, "prompt"),
			seed:    1,
			wantErr: false,
			checkChild: func(t *testing.T, child *mutation.Strategy, a, b *mutation.Strategy) {
				c2, _ := NewCrossover(WithSeed(99999))
				child2, err := c2.Crossover(context.Background(), a, b)
				if err != nil {
					t.Fatalf("second crossover error: %v", err)
				}

				differentCount := 0
				for k := range child.Params {
					if child.Params[k] != child2.Params[k] {
						differentCount++
					}
				}
				if differentCount == 0 {
					t.Error("expected different results with different seeds, but got identical children")
				}
			},
		},
		{
			name:        "context cancellation support",
			a:           makeTestStrategy("a", 0.5, 1, map[string]any{"t": 1}, "p"),
			b:           makeTestStrategy("b", 0.5, 1, map[string]any{"t": 2}, "p"),
			wantErr:     true,
			errContains: "cancelled",
		},
		{
			name:    "empty params on both parents",
			a:       makeTestStrategy("a", 0.5, 1, map[string]any{}, "empty-prompt"),
			b:       makeTestStrategy("b", 0.5, 1, map[string]any{}, "empty-prompt"),
			seed:    42,
			wantErr: false,
			checkChild: func(t *testing.T, child *mutation.Strategy, a, b *mutation.Strategy) {
				if child == nil {
					t.Fatal("child should not be nil even with empty params")
				}
				if len(child.Params) != 0 {
					t.Errorf("child Params length = %d, want 0", len(child.Params))
				}
				if child.MutationDesc == "" {
					t.Error("MutationDesc should describe empty parameter case")
				}
			},
		},
		{
			name: "large number of params",
			a: func() *mutation.Strategy {
				params := make(map[string]any, 50)
				for i := 0; i < 50; i++ {
					params[fmtParamKey("key_a_%d", i)] = i
				}
				return makeTestStrategy("a", 0.5, 1, params, "large-prompt")
			}(),
			b: func() *mutation.Strategy {
				params := make(map[string]any, 50)
				for i := 0; i < 50; i++ {
					params[fmtParamKey("key_b_%d", i)] = i * 10
				}
				return makeTestStrategy("b", 0.5, 1, params, "large-prompt")
			}(),
			seed:    2024,
			wantErr: false,
			checkChild: func(t *testing.T, child *mutation.Strategy, a, b *mutation.Strategy) {
				if len(child.Params) != 100 {
					t.Errorf("child param count = %d, want 100 (union of 50+50)", len(child.Params))
				}
			},
		},
		{
			name:    "parentID format combines both IDs with multiplication sign",
			a:       makeTestStrategy("id-parent-A", 0.5, 1, map[string]any{"k": 1}, "p"),
			b:       makeTestStrategy("id-parent-B", 0.5, 1, map[string]any{"k": 2}, "p"),
			seed:    42,
			wantErr: false,
			checkChild: func(t *testing.T, child *mutation.Strategy, a, b *mutation.Strategy) {
				wantParentID := a.ID + "\u00d7" + b.ID
				if child.ParentID != wantParentID {
					t.Errorf("ParentID = %q, want %q", child.ParentID, wantParentID)
				}
			},
		},
		{
			name:    "prompt template from A when scores are tied",
			a:       makeTestStrategy("a", 0.5, 1, map[string]any{"k": 1}, "prompt_A_tie"),
			b:       makeTestStrategy("b", 0.5, 1, map[string]any{"k": 2}, "prompt_B_tie"),
			seed:    42,
			wantErr: false,
			checkChild: func(t *testing.T, child *mutation.Strategy, a, b *mutation.Strategy) {
				if child.PromptTemplate != a.PromptTemplate {
					t.Errorf("tied scores: PromptTemplate = %q, want %q (parent A wins on tie)", child.PromptTemplate, a.PromptTemplate)
				}
			},
		},
		{
			name:    "MutationDesc contains inheritance information",
			a:       makeTestStrategy("a", 0.5, 1, map[string]any{"alpha": 1, "beta": 2}, "p"),
			b:       makeTestStrategy("b", 0.5, 1, map[string]any{"gamma": 3, "delta": 4}, "p"),
			seed:    42,
			wantErr: false,
			checkChild: func(t *testing.T, child *mutation.Strategy, a, b *mutation.Strategy) {
				if child.MutationDesc == "" {
					t.Error("MutationDesc should not be empty")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c *Crossover
			var err error

			if tt.seed != 0 {
				c, err = NewCrossover(WithSeed(tt.seed))
			} else {
				c, err = NewCrossover()
			}
			if err != nil {
				t.Fatalf("NewCrossover error = %v", err)
			}

			ctx := context.Background()
			// Use cancelled context for cancellation tests.
			if tt.wantErr && tt.errContains == "cancelled" {
				cancelCtx, cancel := context.WithCancel(context.Background())
				cancel()
				ctx = cancelCtx
			}

			child, err := c.Crossover(ctx, tt.a, tt.b)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error = %q, want containing %q", err.Error(), tt.errContains)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.checkChild != nil {
				tt.checkChild(t, child, tt.a, tt.b)
			}
		})
	}
}

func TestCrossoverInterface(t *testing.T) {
	t.Run("satisfies interface", func(t *testing.T) {
		c, err := NewCrossover(WithSeed(42))
		if err != nil {
			t.Fatalf("NewCrossover error = %v", err)
		}

		var _ CrossoverInterface = c // Compile-time interface check.

		a := makeTestStrategy("a", 0.8, 1, map[string]any{"t": 0.7}, "pa")
		b := makeTestStrategy("b", 0.4, 1, map[string]any{"t": 0.3}, "pb")

		child, err := c.Crossover(context.Background(), a, b)
		if err != nil {
			t.Fatalf("Crossover error = %v", err)
		}
		if child == nil {
			t.Fatal("child should not be nil via interface")
		}
	})
}

// ── TwoPoint Crossover Tests ─────────────────

func TestTwoPointCrossover(t *testing.T) {
	t.Run("recombines with middle segment from parent B", func(t *testing.T) {
		c, err := NewCrossover(WithSeed(42), WithCrossoverType(CrossoverTwoPoint))
		if err != nil {
			t.Fatalf("NewCrossover error = %v", err)
		}

		a := makeTestStrategy("a", 0.8, 1, map[string]any{
			"p1": 1.0, "p2": 2.0, "p3": 3.0, "p4": 4.0, "p5": 5.0,
		}, "pa")
		b := makeTestStrategy("b", 0.6, 2, map[string]any{
			"p1": 10.0, "p2": 20.0, "p3": 30.0, "p4": 40.0, "p5": 50.0,
		}, "pb")

		child, err := c.Crossover(context.Background(), a, b)
		if err != nil {
			t.Fatalf("Crossover error = %v", err)
		}
		if child == nil {
			t.Fatal("child should not be nil")
		}
		if child.MutationDesc == "" {
			t.Error("MutationDesc should not be empty")
		}
		// Verify all 5 params are present.
		for _, k := range []string{"p1", "p2", "p3", "p4", "p5"} {
			if _, ok := child.Params[k]; !ok {
				t.Errorf("child missing param %s", k)
			}
		}
		// Verify at least one param from each parent (two-point guarantees middle from B).
		hasFromA := false
		hasFromB := false
		for k, v := range child.Params {
			if a.Params[k] == v {
				hasFromA = true
			}
			if b.Params[k] == v {
				hasFromB = true
			}
		}
		if !hasFromA {
			t.Error("child should have at least one param from parent A")
		}
		if !hasFromB {
			t.Error("child should have at least one param from parent B")
		}
	})

	t.Run("falls back to uniform for small param sets", func(t *testing.T) {
		c, err := NewCrossover(WithSeed(42), WithCrossoverType(CrossoverTwoPoint))
		if err != nil {
			t.Fatalf("NewCrossover error = %v", err)
		}

		a := makeTestStrategy("a", 0.5, 1, map[string]any{"x": 1.0}, "pa")
		b := makeTestStrategy("b", 0.5, 1, map[string]any{"x": 2.0}, "pb")

		child, err := c.Crossover(context.Background(), a, b)
		if err != nil {
			t.Fatalf("Crossover error = %v", err)
		}
		if child == nil {
			t.Fatal("child should not be nil")
		}
	})
}

// ── Segment Crossover Tests ─────────────────

func TestSegmentCrossover(t *testing.T) {
	t.Run("recombines with contiguous block from parent B", func(t *testing.T) {
		c, err := NewCrossover(WithSeed(42), WithCrossoverType(CrossoverSegment))
		if err != nil {
			t.Fatalf("NewCrossover error = %v", err)
		}

		a := makeTestStrategy("a", 0.8, 1, map[string]any{
			"p1": 1.0, "p2": 2.0, "p3": 3.0, "p4": 4.0,
		}, "pa")
		b := makeTestStrategy("b", 0.6, 2, map[string]any{
			"p1": 10.0, "p2": 20.0, "p3": 30.0, "p4": 40.0,
		}, "pb")

		child, err := c.Crossover(context.Background(), a, b)
		if err != nil {
			t.Fatalf("Crossover error = %v", err)
		}
		if child == nil {
			t.Fatal("child should not be nil")
		}
		// Verify all 4 params are present.
		for _, k := range []string{"p1", "p2", "p3", "p4"} {
			if _, ok := child.Params[k]; !ok {
				t.Errorf("child missing param %s", k)
			}
		}
		// With segment crossover, we expect params from both parents.
		hasFromA := false
		hasFromB := false
		for k, v := range child.Params {
			if a.Params[k] == v {
				hasFromA = true
			}
			if b.Params[k] == v {
				hasFromB = true
			}
		}
		if !hasFromA {
			t.Error("child should have at least one param from parent A")
		}
		if !hasFromB {
			t.Error("child should have at least one param from parent B")
		}
	})

	t.Run("falls back to uniform for single param", func(t *testing.T) {
		c, err := NewCrossover(WithSeed(42), WithCrossoverType(CrossoverSegment))
		if err != nil {
			t.Fatalf("NewCrossover error = %v", err)
		}

		a := makeTestStrategy("a", 0.5, 1, map[string]any{"x": 1.0}, "pa")
		b := makeTestStrategy("b", 0.5, 1, map[string]any{"x": 2.0}, "pb")

		child, err := c.Crossover(context.Background(), a, b)
		if err != nil {
			t.Fatalf("Crossover error = %v", err)
		}
		if child == nil {
			t.Fatal("child should not be nil")
		}
	})
}

// ── WithCrossoverType Tests ────────────────

func TestWithCrossoverType_Invalid(t *testing.T) {
	_, err := NewCrossover(WithCrossoverType(CrossoverType(99)))
	if err == nil {
		t.Fatal("expected error for invalid crossover type")
	}
}

func TestCollectParamKeys(t *testing.T) {
	tests := []struct {
		name string
		a    map[string]any
		b    map[string]any
		want int
	}{
		{
			name: "both empty",
			a:    map[string]any{},
			b:    map[string]any{},
			want: 0,
		},
		{
			name: "only A has keys",
			a:    map[string]any{"x": 1, "y": 2},
			b:    map[string]any{},
			want: 2,
		},
		{
			name: "only B has keys",
			a:    map[string]any{},
			b:    map[string]any{"m": 3, "n": 4, "o": 5},
			want: 3,
		},
		{
			name: "overlapping keys",
			a:    map[string]any{"shared": 1, "a_only": 2},
			b:    map[string]any{"shared": 10, "b_only": 20},
			want: 3,
		},
		{
			name: "no overlap",
			a:    map[string]any{"a1": 1, "a2": 2},
			b:    map[string]any{"b1": 3, "b2": 4},
			want: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys := collectParamKeys(tt.a, tt.b)
			if len(keys) != tt.want {
				t.Errorf("collectParamKeys returned %d keys, want %d", len(keys), tt.want)
			}
			// Verify sorted order.
			for i := 1; i < len(keys); i++ {
				if keys[i] <= keys[i-1] {
					t.Errorf("keys not sorted at index %d: %q <= %q", i, keys[i], keys[i-1])
				}
			}
		})
	}
}

func TestFormatParentIDs(t *testing.T) {
	result := formatParentIDs("aaa", "bbb")
	if result != "aaa\u00d7bbb" {
		t.Errorf("formatParentIDs = %q, want %q", result, "aaa\u00d7bbb")
	}
}

func TestMaxVersion(t *testing.T) {
	tests := []struct {
		a, b int
		want int
	}{
		{1, 2, 2},
		{5, 3, 5},
		{0, 0, 0},
		{-1, 5, 5},
		{100, 100, 100},
	}
	for _, tt := range tests {
		got := maxVersion(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("maxVersion(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestGenerateCrossoverPoints(t *testing.T) {
	t.Run("valid generation", func(t *testing.T) {
		rng := rand.New(rand.NewSource(42)) // #nosec G404
		points := generateCrossoverPoints(rng, 2, 10)
		if len(points) != 2 {
			t.Fatalf("got %d points, want 2", len(points))
		}
		for _, p := range points {
			if p < 1 || p >= 10 {
				t.Errorf("point %d out of range [1, 9]", p)
			}
		}
	})

	t.Run("points are unique and sorted", func(t *testing.T) {
		rng := rand.New(rand.NewSource(99)) // #nosec G404
		points := generateCrossoverPoints(rng, 5, 20)
		seen := make(map[int]bool)
		for i, p := range points {
			if seen[p] {
				t.Errorf("duplicate point at index %d: %d", i, p)
			}
			seen[p] = true
			if i > 0 && points[i] <= points[i-1] {
				t.Errorf("points not sorted at index %d", i)
			}
		}
	})

	t.Run("k exceeds max points", func(t *testing.T) {
		rng := rand.New(rand.NewSource(1)) // #nosec G404
		points := generateCrossoverPoints(rng, 100, 5)
		if len(points) != 4 { // n-1 = 4
			t.Errorf("got %d points, want 4 (max for n=5)", len(points))
		}
	})

	t.Run("n <= 1 returns nil", func(t *testing.T) {
		rng := rand.New(rand.NewSource(1)) // #nosec G404
		points := generateCrossoverPoints(rng, 3, 1)
		if points != nil {
			t.Errorf("expected nil for n=1, got %v", points)
		}
	})
}

// fmtParamKey is a test helper that formats a parameter key name.
func fmtParamKey(format string, args ...int) string {
	result := format
	for _, arg := range args {
		result = replaceFirstPlaceholder(result, arg)
	}
	return result
}

// replaceFirstPlaceholder replaces the first %d occurrence in s with the integer value.
func replaceFirstPlaceholder(s string, val int) string {
	for i := 0; i < len(s)-1; i++ {
		if s[i] == '%' && s[i+1] == 'd' {
			return s[:i] + strconv.Itoa(val) + s[i+2:]
		}
	}
	return s
}
