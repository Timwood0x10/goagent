package evolution

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestGetParamFloat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		params     map[string]any
		key        string
		expected   float64
		expectedOK bool
	}{
		{
			name:       "float64_value",
			params:     map[string]any{"top_k": 40.0},
			key:        "top_k",
			expected:   40.0,
			expectedOK: true,
		},
		{
			name:       "int_value",
			params:     map[string]any{"top_k": 40},
			key:        "top_k",
			expected:   40.0,
			expectedOK: true,
		},
		{
			name:       "int64_value",
			params:     map[string]any{"top_k": int64(40)},
			key:        "top_k",
			expected:   40.0,
			expectedOK: true,
		},
		{
			name:       "int32_value",
			params:     map[string]any{"top_k": int32(40)},
			key:        "top_k",
			expected:   40.0,
			expectedOK: true,
		},
		{
			name:       "float32_value",
			params:     map[string]any{"temperature": float32(0.7)},
			key:        "temperature",
			expected:   0.7,
			expectedOK: true,
		},
		{
			name:       "json_number_value",
			params:     map[string]any{"top_k": json.Number("40")},
			key:        "top_k",
			expected:   40.0,
			expectedOK: true,
		},
		{
			name:       "missing_key",
			params:     map[string]any{"temperature": 0.7},
			key:        "top_k",
			expected:   0.0,
			expectedOK: false,
		},
		{
			name:       "nil_params",
			params:     nil,
			key:        "top_k",
			expected:   0.0,
			expectedOK: false,
		},
		{
			name:       "string_value_not_found",
			params:     map[string]any{"top_k": "40"},
			key:        "top_k",
			expected:   0.0,
			expectedOK: false,
		},
		{
			name:       "invalid_json_number_not_found",
			params:     map[string]any{"top_k": json.Number("invalid")},
			key:        "top_k",
			expected:   0.0,
			expectedOK: false,
		},
		{
			name:       "zero_value_found",
			params:     map[string]any{"temperature": 0.0},
			key:        "temperature",
			expected:   0.0,
			expectedOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := getParamFloat(tt.params, tt.key)
			if ok != tt.expectedOK {
				t.Errorf("getParamFloat(%v, %q) ok = %v, want %v", tt.params, tt.key, ok, tt.expectedOK)
			}
			// Use approximate comparison for float32 values due to precision loss.
			if math.Abs(got-tt.expected) > 1e-6 {
				t.Errorf("getParamFloat(%v, %q) = %v, want %v", tt.params, tt.key, got, tt.expected)
			}
		})
	}
}

func TestDeterministicScore_NumericTypeHandling(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		strategy *Strategy
		expected float64
	}{
		{
			name: "top_k_as_float64",
			strategy: &Strategy{
				Params:         map[string]any{"temperature": 0.7, "top_k": 40.0},
				PromptTemplate: "helpful",
			},
			// Base: 50, temp: (1-0.7)*25=7.5, top_k penalty: (40-30)^2/10=10
			// Total: 50 + 7.5 - 10 = 47.5
			expected: 47.5,
		},
		{
			name: "top_k_as_int",
			strategy: &Strategy{
				Params:         map[string]any{"temperature": 0.7, "top_k": 40},
				PromptTemplate: "helpful",
			},
			// Should now correctly apply penalty for int type
			expected: 47.5,
		},
		{
			name: "top_k_as_int64",
			strategy: &Strategy{
				Params:         map[string]any{"temperature": 0.7, "top_k": int64(40)},
				PromptTemplate: "helpful",
			},
			expected: 47.5,
		},
		{
			name: "temperature_as_float32",
			strategy: &Strategy{
				Params:         map[string]any{"temperature": float32(0.7), "top_k": 40.0},
				PromptTemplate: "helpful",
			},
			// Float32(0.7) ~= 0.699999988079071, so (1-temp)*25 ~= 7.500000298023224
			// Use approximate comparison
			expected: 47.5,
		},
		{
			name: "optimal_top_k_30",
			strategy: &Strategy{
				Params:         map[string]any{"temperature": 0.0, "top_k": 30},
				PromptTemplate: "precise",
			},
			// Base: 50, temp: (1-0)*25=25, top_k penalty: 0, prompt: +15
			// Total: 50 + 25 + 0 + 15 = 90
			expected: 90.0,
		},
		{
			name: "high_top_k_penalty",
			strategy: &Strategy{
				Params:         map[string]any{"temperature": 0.0, "top_k": 80},
				PromptTemplate: "helpful",
			},
			// Base: 50, temp: +25, top_k penalty: (80-30)^2/10=2500/10=250
			// Total: 50 + 25 - 250 = -175 (clamped to min 5.0)
			expected: 5.0,
		},
		{
			name:     "nil_strategy",
			strategy: nil,
			expected: 50.0,
		},
		{
			name: "missing_temperature",
			strategy: &Strategy{
				Params:         map[string]any{"top_k": 30},
				PromptTemplate: "precise",
			},
			// Base: 50, temp: not found (skip), top_k penalty: 0, prompt: +15
			// Total: 50 + 0 + 0 + 15 = 65
			expected: 65.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := DeterministicScore(tt.strategy)
			// Use approximate comparison for float32 precision issues.
			if math.Abs(got-tt.expected) > 1e-6 {
				t.Errorf("DeterministicScore() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestExtractScoreFromText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		text     string
		expected float64
	}{
		{
			name:     "simple_json_score",
			text:     `{"score": 75}`,
			expected: 75.0,
		},
		{
			name:     "score_without_space",
			text:     `"score":85.5`,
			expected: 85.5,
		},
		{
			name:     "score_embedded_in_text",
			text:     `The result is "score": 92.0 out of 100`,
			expected: 92.0,
		},
		{
			name:     "score_at_upper_bound",
			text:     `"score": 100`,
			expected: 100.0,
		},
		{
			name:     "score_exceeds_max",
			text:     `"score": 101`,
			expected: 0,
		},
		{
			name:     "score_is_negative",
			text:     `"score": -5`,
			expected: 0,
		},
		{
			name:     "score_is_zero",
			text:     `"score": 0`,
			expected: 0,
		},
		{
			name:     "wrong_key",
			text:     `"other": 75`,
			expected: 0,
		},
		{
			name:     "no_quotes_around_score",
			text:     `score: 75`,
			expected: 0,
		},
		{
			name:     "empty_text",
			text:     "",
			expected: 0,
		},
		{
			name:     "non_numeric_value",
			text:     `"score": "high"`,
			expected: 0,
		},
		{
			name:     "first_valid_score_picked",
			text:     `"score": 0, "score": 80`,
			expected: 80.0,
		},
		{
			name:     "score_with_decimal_truncation",
			text:     `"score": 75.6789`,
			expected: 75.6789,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractScoreFromText(tt.text)
			if math.Abs(got-tt.expected) > 1e-6 {
				t.Errorf("extractScoreFromText(%q) = %v, want %v", tt.text, got, tt.expected)
			}
		})
	}
}

func TestFallbackScore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		text     string
		expected float64
	}{
		{
			name:     "excellent_keyword",
			text:     "The result is excellent",
			expected: 90,
		},
		{
			name:     "outstanding_max_score",
			text:     "outstanding performance",
			expected: 95,
		},
		{
			name:     "very_good_beats_good",
			text:     "This is very good work",
			expected: 80,
		},
		{
			name:     "good_keyword",
			text:     "good enough result",
			expected: 70,
		},
		{
			name:     "multiple_keywords_picks_highest",
			text:     "poor at first but decent overall",
			expected: 60,
		},
		{
			name:     "no_keywords_returns_default",
			text:     "no matching keywords here",
			expected: 50,
		},
		{
			name:     "case_insensitive_matching",
			text:     "This is EXCELLENT work",
			expected: 90,
		},
		{
			name:     "empty_string_default",
			text:     "",
			expected: 50,
		},
		{
			name:     "terrible_and_outstanding",
			text:     "terrible then outstanding",
			expected: 95,
		},
		{
			name:     "average_mid_range",
			text:     "average performance seen",
			expected: 50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := &LLMScorer{}
			got := s.fallbackScore(tt.text)
			if got != tt.expected {
				t.Errorf("fallbackScore(%q) = %v, want %v", tt.text, got, tt.expected)
			}
		})
	}
}

func TestBuildPrompt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		evalPrompt  string
		seed        int64
		strategy    *Strategy
		wantContain []string // substrings the result must contain
		wantNot     []string // substrings the result must NOT contain
	}{
		{
			name:       "basic_prompt_with_params",
			evalPrompt: "Evaluate: {strategy_json}",
			seed:       0,
			strategy: &Strategy{
				Name:           "test-strategy",
				PromptTemplate: "helpful",
				Params:         map[string]any{"temperature": 0.7, "top_k": 40},
			},
			wantContain: []string{"Evaluate:", "test-strategy", "helpful", "temperature", "top_k"},
			wantNot:     []string{"Scoring seed"},
		},
		{
			name:       "prompt_with_seed_appended",
			evalPrompt: "Rate: {strategy_json}",
			seed:       42,
			strategy: &Strategy{
				Name:           "seed-strategy",
				PromptTemplate: "precise",
				Params:         map[string]any{},
			},
			wantContain: []string{"Rate:", "seed-strategy", "precise", "Scoring seed: 42"},
		},
		{
			name:       "no_params_map",
			evalPrompt: "Score: {strategy_json}",
			seed:       0,
			strategy: &Strategy{
				Name:           "empty",
				PromptTemplate: "default",
			},
			wantContain: []string{"Score:", "empty", "default"},
			wantNot:     []string{"Scoring seed"},
		},
		{
			name:       "seed_zero_omits_seed_line",
			evalPrompt: "Go: {strategy_json}",
			seed:       0,
			strategy: &Strategy{
				Name:           "no-seed",
				PromptTemplate: "chat",
				Params:         map[string]any{"top_p": 0.9},
			},
			wantContain: []string{"Go:", "no-seed", "top_p"},
			wantNot:     []string{"Scoring seed"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &LLMScorer{
				evalPrompt: tt.evalPrompt,
				seed:       tt.seed,
			}
			got := s.buildPrompt(tt.strategy)
			for _, want := range tt.wantContain {
				if !strings.Contains(got, want) {
					t.Errorf("buildPrompt() missing %q in:\n%s", want, got)
				}
			}
			for _, notWant := range tt.wantNot {
				if strings.Contains(got, notWant) {
					t.Errorf("buildPrompt() unexpectedly contains %q in:\n%s", notWant, got)
				}
			}
		})
	}
}

func TestParseScore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		resp     string
		expected float64
	}{
		{
			name:     "direct_json_score",
			resp:     `{"score": 75}`,
			expected: 75.0,
		},
		{
			name:     "json_score_capped_at_100",
			resp:     `{"score": 150}`,
			expected: 100.0,
		},
		{
			name:     "json_score_with_decimal",
			resp:     `{"score": 83.5}`,
			expected: 83.5,
		},
		{
			name:     "json_score_exactly_100",
			resp:     `{"score": 100}`,
			expected: 100.0,
		},
		{
			name:     "zero_score_falls_to_extract",
			resp:     `{"score": 0, "other": "score": 80}`,
			expected: 80.0,
		},
		{
			name:     "no_json_falls_to_fallback",
			resp:     "the result is excellent overall",
			expected: 90,
		},
		{
			name:     "no_keyword_returns_50",
			resp:     "no useful information",
			expected: 50,
		},
		{
			name:     "extract_from_mixed_text",
			resp:     `Thinking: Let me evaluate... The "score": 88 seems right`,
			expected: 88.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := &LLMScorer{}
			got := s.parseScore(tt.resp)
			if math.Abs(got-tt.expected) > 1e-6 {
				t.Errorf("parseScore(%q) = %v, want %v", tt.resp, got, tt.expected)
			}
		})
	}
}
