package mutation

import (
	"context"
	"testing"
)

func TestExtractJSONBracket(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "whitespace only",
			input: "  \n\t  ",
			want:  "",
		},
		{
			name:  "pure JSON array",
			input: `[{"problem":"p","solution":"s"}]`,
			want:  `[{"problem":"p","solution":"s"}]`,
		},
		{
			name:  "JSON array with leading whitespace",
			input: `  [{"problem":"p","solution":"s"}]  `,
			want:  `[{"problem":"p","solution":"s"}]`,
		},
		{
			name:  "text before and after JSON",
			input: "Here you go:\n[{\"problem\":\"p\",\"solution\":\"s\"}]\nDone",
			want:  `[{"problem":"p","solution":"s"}]`,
		},
		{
			name:  "code fence with json tag",
			input: "```json\n[{\"problem\":\"p\",\"solution\":\"s\"}]\n```",
			want:  `[{"problem":"p","solution":"s"}]`,
		},
		{
			name:  "code fence without language tag",
			input: "```\n[{\"problem\":\"p\",\"solution\":\"s\"}]\n```",
			want:  `[{"problem":"p","solution":"s"}]`,
		},
		{
			name:  "text before code fence",
			input: "Here you go:\n```json\n[{\"problem\":\"p\",\"solution\":\"s\"}]\n```\nDone",
			want:  `[{"problem":"p","solution":"s"}]`,
		},
		{
			name:  "multiple JSON objects in array",
			input: `[{"problem":"p1"},{"problem":"p2"}]`,
			want:  `[{"problem":"p1"},{"problem":"p2"}]`,
		},
		{
			name:  "no JSON in response returns empty",
			input: "I don't know what to suggest.",
			want:  "",
		},
		{
			name:  "brackets but not valid JSON content",
			input: "Some [text] here",
			want:  "[text]",
		},
		{
			name:  "unclosed code fence returns inner content",
			input: "```\nsome text without closing fence",
			want:  "some text without closing fence",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := ExtractJSONBracket(tt.input)
			if got != tt.want {
				t.Errorf("ExtractJSONBracket() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseResponse(t *testing.T) {
	t.Parallel()

	provider, err := NewLLMHintProvider(&mockLLMClient{}, nil)
	if err != nil {
		t.Fatalf("NewLLMHintProvider failed: %v", err)
	}

	t.Run("pure JSON array", func(t *testing.T) {
		t.Parallel()

		resp := `[{"problem":"p","solution":"s","confidence":0.8}]`
		hints, err := provider.parseResponse(resp, 10)
		if err != nil {
			t.Fatalf("parseResponse failed: %v", err)
		}
		if len(hints) != 1 {
			t.Fatalf("expected 1 hint, got %d", len(hints))
		}
		if hints[0].Problem != "p" {
			t.Errorf("expected problem 'p', got %q", hints[0].Problem)
		}
		if hints[0].Solution != "s" {
			t.Errorf("expected solution 's', got %q", hints[0].Solution)
		}
	})

	t.Run("text wrapped JSON", func(t *testing.T) {
		t.Parallel()

		resp := "Here you go:\n[{\"problem\":\"p\",\"solution\":\"s\",\"confidence\":0.8}]\nDone"
		hints, err := provider.parseResponse(resp, 10)
		if err != nil {
			t.Fatalf("parseResponse failed: %v", err)
		}
		if len(hints) != 1 {
			t.Fatalf("expected 1 hint, got %d", len(hints))
		}
		if hints[0].Problem != "p" {
			t.Errorf("expected problem 'p', got %q", hints[0].Problem)
		}
	})

	t.Run("code fence with json tag", func(t *testing.T) {
		t.Parallel()

		resp := "Here you go:\n```json\n[{\"problem\":\"p\",\"solution\":\"s\",\"confidence\":0.8}]\n```\nDone"
		hints, err := provider.parseResponse(resp, 10)
		if err != nil {
			t.Fatalf("parseResponse failed: %v", err)
		}
		if len(hints) != 1 {
			t.Fatalf("expected 1 hint, got %d", len(hints))
		}
		if hints[0].Problem != "p" {
			t.Errorf("expected problem 'p', got %q", hints[0].Problem)
		}
	})

	t.Run("code fence without language tag", func(t *testing.T) {
		t.Parallel()

		resp := "```\n[{\"problem\":\"p\",\"solution\":\"s\",\"confidence\":0.8}]\n```"
		hints, err := provider.parseResponse(resp, 10)
		if err != nil {
			t.Fatalf("parseResponse failed: %v", err)
		}
		if len(hints) != 1 {
			t.Fatalf("expected 1 hint, got %d", len(hints))
		}
	})

	t.Run("empty response", func(t *testing.T) {
		t.Parallel()

		_, err := provider.parseResponse("", 10)
		if err == nil {
			t.Error("expected error for empty response")
		}
	})

	t.Run("invalid JSON", func(t *testing.T) {
		t.Parallel()

		_, err := provider.parseResponse("not json at all", 10)
		if err == nil {
			t.Error("expected error for invalid JSON")
		}
	})

	t.Run("respects limit", func(t *testing.T) {
		t.Parallel()

		resp := `[{"problem":"p1"},{"problem":"p2"},{"problem":"p3"}]`
		hints, err := provider.parseResponse(resp, 2)
		if err != nil {
			t.Fatalf("parseResponse failed: %v", err)
		}
		if len(hints) != 2 {
			t.Fatalf("expected 2 hints, got %d", len(hints))
		}
	})

	t.Run("multiple hints parsed correctly", func(t *testing.T) {
		t.Parallel()

		resp := `[
			{"problem":"p1","solution":"s1","confidence":0.7},
			{"problem":"p2","solution":"s2","confidence":0.8}
		]`
		hints, err := provider.parseResponse(resp, 10)
		if err != nil {
			t.Fatalf("parseResponse failed: %v", err)
		}
		if len(hints) != 2 {
			t.Fatalf("expected 2 hints, got %d", len(hints))
		}
		if hints[0].Problem != "p1" || hints[1].Problem != "p2" {
			t.Errorf("unexpected problems: %q, %q", hints[0].Problem, hints[1].Problem)
		}
	})

	t.Run("empty JSON array", func(t *testing.T) {
		t.Parallel()

		hints, err := provider.parseResponse("[]", 10)
		if err != nil {
			t.Fatalf("parseResponse failed: %v", err)
		}
		if len(hints) != 0 {
			t.Errorf("expected 0 hints, got %d", len(hints))
		}
	})

	t.Run("default confidence for missing confidence", func(t *testing.T) {
		t.Parallel()

		resp := `[{"problem":"p","solution":"s"}]`
		hints, err := provider.parseResponse(resp, 10)
		if err != nil {
			t.Fatalf("parseResponse failed: %v", err)
		}
		if len(hints) != 1 {
			t.Fatalf("expected 1 hint, got %d", len(hints))
		}
		if hints[0].Confidence != 0.6 {
			t.Errorf("expected default confidence 0.6, got %f", hints[0].Confidence)
		}
	})
}

// mockLLMClient is a no-op LLM client for testing the hint provider.
type mockLLMClient struct{}

func (m *mockLLMClient) Generate(ctx context.Context, prompt string) (string, error) {
	return "", nil
}
