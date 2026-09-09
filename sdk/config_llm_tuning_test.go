package sdk

import (
	"os"
	"testing"
)

// TestLLMTuningFieldsBridged pins the ToOptions fix (DEEP_CODE_REVIEW 1.8):
// llm.temperature and llm.max_tokens were validated and then silently
// dropped, so users got the hardcoded defaults 0.7/2048 regardless of
// ares.yaml. Non-zero values must reach llmCfg; zero values must leave the
// defaults untouched.
func TestLLMTuningFieldsBridged(t *testing.T) {
	content := `llm:
  provider: openai
  model: test-model
  api_key: test-key
  temperature: 0.3
  max_tokens: 512
`
	path := t.TempDir() + "/ares.yaml"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := LoadConfigFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.LLM.Temperature != 0.3 {
		t.Fatalf("yaml parse: want temperature 0.3, got %v", cfg.LLM.Temperature)
	}
	if cfg.LLM.MaxTokens != 512 {
		t.Fatalf("yaml parse: want max_tokens 512, got %d", cfg.LLM.MaxTokens)
	}

	opts, err := cfg.ToOptions()
	if err != nil {
		t.Fatalf("toOptions: %v", err)
	}
	c := defaultConfig()
	for _, o := range opts {
		if err := o(c); err != nil {
			t.Fatalf("apply option: %v", err)
		}
	}
	if c.llmCfg.Temperature != 0.3 {
		t.Fatalf("bridge: want temperature 0.3 in llmCfg, got %v", c.llmCfg.Temperature)
	}
	if c.llmCfg.MaxTokens != 512 {
		t.Fatalf("bridge: want max_tokens 512 in llmCfg, got %d", c.llmCfg.MaxTokens)
	}
}

// TestLLMTuningZeroKeepsDefaults pins the zero-value semantics: unset fields
// fall back to the component defaults (0.7/2048), not zero.
func TestLLMTuningZeroKeepsDefaults(t *testing.T) {
	content := `llm:
  provider: openai
  model: test-model
  api_key: test-key
`
	path := t.TempDir() + "/ares.yaml"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cfg, err := LoadConfigFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	opts, err := cfg.ToOptions()
	if err != nil {
		t.Fatalf("toOptions: %v", err)
	}
	c := defaultConfig()
	for _, o := range opts {
		if err := o(c); err != nil {
			t.Fatalf("apply option: %v", err)
		}
	}
	if c.llmCfg.Temperature != 0.7 {
		t.Fatalf("default temperature: want 0.7, got %v", c.llmCfg.Temperature)
	}
	if c.llmCfg.MaxTokens != 2048 {
		t.Fatalf("default max_tokens: want 2048, got %d", c.llmCfg.MaxTokens)
	}
}
