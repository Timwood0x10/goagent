package sdk

import (
	"strings"
	"testing"

	llmcore "github.com/Timwood0x10/ares/internal/llmcore"
)

// TestProviderKeyHint covers the early-misconfiguration warning returned at
// Runtime construction.
//
// Bug scenario: a newcomer calling NewRuntime(WithOpenAI("gpt-4o-mini"))
// without an API key got NO feedback until the first Run failed with a
// provider-side 401 — far from the mistake. The constructor now warns
// immediately via providerKeyHint.
//
// Contract:
//   - Hosted providers (openai / anthropic / openrouter) with an empty key
//     yield an actionable hint naming the env var AND the WithAPIKey option.
//   - Ollama never hints: it is local and typically runs without a key.
//   - Any provider with a non-empty key yields no hint.
func TestProviderKeyHint(t *testing.T) {
	cases := []struct {
		name       string
		provider   llmcore.LLMProvider
		apiKey     string
		wantSubstr string // empty wantSubstr means "no hint expected"
	}{
		{
			name:       "openai without key names env var and option",
			provider:   llmcore.LLMProviderOpenAI,
			apiKey:     "",
			wantSubstr: "OPENAI_API_KEY",
		},
		{
			name:       "anthropic without key names its env var",
			provider:   llmcore.LLMProviderAnthropic,
			apiKey:     "",
			wantSubstr: "ANTHROPIC_API_KEY",
		},
		{
			name:       "openrouter without key names its env var",
			provider:   llmcore.LLMProviderOpenRouter,
			apiKey:     "",
			wantSubstr: "OPENROUTER_API_KEY",
		},
		{
			name:       "ollama without key is legitimate",
			provider:   llmcore.LLMProviderOllama,
			apiKey:     "",
			wantSubstr: "",
		},
		{
			name:       "openai with key stays silent",
			provider:   llmcore.LLMProviderOpenAI,
			apiKey:     "sk-test",
			wantSubstr: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := providerKeyHint(tc.provider, tc.apiKey)
			if tc.wantSubstr == "" {
				if got != "" {
					t.Fatalf("providerKeyHint(%s, %q) = %q, want no hint", tc.provider, tc.apiKey, got)
				}
				return
			}
			if !strings.Contains(got, tc.wantSubstr) {
				t.Fatalf("providerKeyHint(%s, %q) = %q, want it to mention %q", tc.provider, tc.apiKey, got, tc.wantSubstr)
			}
			if !strings.Contains(got, "WithAPIKey") {
				t.Fatalf("hint %q must point at the WithAPIKey option", got)
			}
		})
	}
}
