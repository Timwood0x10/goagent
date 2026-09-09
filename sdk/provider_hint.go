package sdk

import (
	"fmt"

	llmcore "github.com/Timwood0x10/ares/internal/llmcore"
)

// providerKeyHint returns an actionable warning for a runtime that declares a
// hosted LLM provider without an API key, or "" when the configuration is
// plausible.
//
// Why a warning and not an error: some deployments reach hosted providers
// through gateways that authenticate by network position instead of a key,
// so rejecting construction would break legitimate setups. Surfacing the gap
// at construction time — next to the option call that caused it — is what
// fixes the newcomer experience; before this check the misconfiguration only
// surfaced as a provider-side 401 on the first Run, far from its cause.
func providerKeyHint(provider llmcore.LLMProvider, apiKey string) string {
	if apiKey != "" {
		return ""
	}
	envVar := map[llmcore.LLMProvider]string{
		llmcore.LLMProviderOpenAI:     "OPENAI_API_KEY",
		llmcore.LLMProviderAnthropic:  "ANTHROPIC_API_KEY",
		llmcore.LLMProviderOpenRouter: "OPENROUTER_API_KEY",
	}[provider]
	if envVar == "" {
		// Ollama and unknown providers legitimately run without a key.
		return ""
	}
	return fmt.Sprintf("%s selected but no API key set: pass sdk.WithAPIKey(...) or export %s; calls will fail with 401 until then", provider, envVar)
}
