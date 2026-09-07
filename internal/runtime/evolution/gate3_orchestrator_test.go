package evolution

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ares_config "github.com/Timwood0x10/ares/internal/ares_config"
)

// stubGate3LLM simulates the LLM in the scorer's two-step protocol for both
// single-case and batch calls: execution prompts are answered with a strategy
// marker, grading prompts with a score derived from the marker. It lets tests
// exercise the full gate-3 wiring without any real API call.
type stubGate3LLM struct{}

func (stubGate3LLM) Generate(_ context.Context, prompt string) (string, error) {
	switch {
	case isBatchExecPromptStr(prompt):
		// One output line per numbered task, mirroring splitOutputLines.
		marker := "exec-bad"
		if strings.Contains(prompt, goodStrategy) {
			marker = "exec-good"
		}
		return repeatLines(marker, numberedItemCount(prompt)), nil
	case isBatchEvalPromptStr(prompt):
		// One score line per (task, output) pair, mirroring parseBatchScores.
		score := "0.2"
		if strings.Contains(prompt, "exec-good") {
			score = "0.9"
		}
		return repeatLines(score, numberedItemCount(prompt)), nil
	case isExecPromptStr(prompt):
		if strings.Contains(prompt, goodStrategy) {
			return "exec-good", nil
		}
		return "exec-bad", nil
	default:
		if strings.Contains(prompt, "exec-good") {
			return "0.9", nil
		}
		return "0.2", nil
	}
}

// isExecPromptStr mirrors the scorer's single-case execution-prompt marker.
func isExecPromptStr(prompt string) bool {
	return strings.Contains(prompt, "Produce your final output")
}

// isBatchExecPromptStr mirrors the scorer's batch execution-prompt marker.
func isBatchExecPromptStr(prompt string) bool {
	return strings.Contains(prompt, "EACH numbered task")
}

// isBatchEvalPromptStr mirrors the scorer's batch grading-prompt marker.
func isBatchEvalPromptStr(prompt string) bool {
	return strings.Contains(prompt, "one numeric score per line")
}

// numberedItemCount counts the "Task N:" items embedded in a batch prompt so
// the stub emits exactly as many lines as the scorer expects.
func numberedItemCount(prompt string) int {
	n := 0
	for ln := range strings.Lines(prompt) {
		if strings.HasPrefix(strings.TrimSpace(ln), "Task ") {
			n++
		}
	}
	if n == 0 {
		return 1
	}
	return n
}

// repeatLines returns marker repeated n times, one per line.
func repeatLines(marker string, n int) string {
	lines := make([]string, n)
	for i := range n {
		lines[i] = marker
	}
	return strings.Join(lines, "\n")
}

const (
	goodStrategy = "good coder strategy"
	badStrategy  = "bad coder strategy"
)

// newGate3Fixture builds a profile store whose stable instructions are the
// good strategy, plus a preserved case suite.
func newGate3Fixture(t *testing.T) *ProfileStore {
	t.Helper()
	store := newRegressionProfileStore(t, goodStrategy)
	return store
}

func TestBuildRegressionGate3_RejectsRegression(t *testing.T) {
	store := newGate3Fixture(t)
	check, err := BuildRegressionGate3(store, stubGate3LLM{}, []any{"case-1", "case-2"})
	if err != nil {
		t.Fatalf("BuildRegressionGate3 returned error: %v", err)
	}

	// A bad new strategy (scored 0.2) must regress against the good baseline.
	// Assert the genuine regression marker ("avg dropped") rather than the
	// substring "regression", which the wrapper error ("run preserved-case
	// regression") would match even when no regression was detected.
	c := NewCandidate(CandidateInstruction, "coder", badStrategy, "change", []string{"ev-1"})
	err = check(c)
	if err == nil || !strings.Contains(err.Error(), "avg dropped") {
		t.Fatalf("check should reject a regressing candidate, got %v", err)
	}
}

func TestBuildRegressionGate3_PassesNoRegression(t *testing.T) {
	store := newGate3Fixture(t)
	check, err := BuildRegressionGate3(store, stubGate3LLM{}, []any{"case-1", "case-2"})
	if err != nil {
		t.Fatalf("BuildRegressionGate3 returned error: %v", err)
	}

	// A new strategy equal to the good baseline must pass.
	c := NewCandidate(CandidateInstruction, "coder", goodStrategy, "tweak", []string{"ev-1"})
	err = check(c)
	if err != nil {
		t.Fatalf("check should pass a non-regressing candidate, got %v", err)
	}
}

func TestBuildRegressionGate3_NilClient(t *testing.T) {
	_, err := BuildRegressionGate3(newGate3Fixture(t), nil, []any{"case-1"})
	if err == nil {
		t.Fatal("BuildRegressionGate3 with nil client should error")
	}
}

func TestBuildRegressionGate3_NilProfileStore(t *testing.T) {
	_, err := BuildRegressionGate3(nil, stubGate3LLM{}, []any{"case-1"})
	if err == nil {
		t.Fatal("BuildRegressionGate3 with nil profile store should error")
	}
}

func TestLoadRegressionGate3_MissingConfig(t *testing.T) {
	_, err := LoadRegressionGate3(newGate3Fixture(t), "no/such/file.yaml", []any{"case-1"})
	if err == nil {
		t.Fatal("LoadRegressionGate3 with a missing config should error")
	}
}

// writeTempConfig writes an ares YAML into a temp file and returns its path.
func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ares.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return path
}

func TestLoadRegressionGate3_OllamaNoKey(t *testing.T) {
	// Ollama needs no API key: loading must succeed (assembly only, no calls).
	path := writeTempConfig(t, `
llm:
  provider: ollama
  model: "gemma4:e4b"
  base_url: "http://localhost:11434"
`)
	check, err := LoadRegressionGate3(newGate3Fixture(t), path, []any{"case-1"})
	if err != nil {
		t.Fatalf("LoadRegressionGate3 for ollama without a key should succeed, got %v", err)
	}
	if check == nil {
		t.Fatal("LoadRegressionGate3 returned a nil check for ollama")
	}
}

func TestLoadRegressionGate3_OpenAINoKey(t *testing.T) {
	// OpenAI without an api key must be rejected by IsEnabled.
	path := writeTempConfig(t, `
llm:
  provider: openai
  model: "deepseek-v4-flash"
  base_url: "https://token.sensenova.cn/v1"
`)
	_, err := LoadRegressionGate3(newGate3Fixture(t), path, []any{"case-1"})
	if err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("LoadRegressionGate3 for openai without a key should report not-enabled, got %v", err)
	}
}

func TestLoadRegressionGate3_FallbackChain(t *testing.T) {
	// A primary provider with credentials plus a keyless ollama fallback must
	// build a FailoverClient successfully (assembly only, no calls).
	path := writeTempConfig(t, `
llm:
  provider: openai
  model: "agnes-2.5-flash"
  api_key: "sk-test"
  base_url: "https://apihub.agnes-ai.com/v1"
  fallbacks:
    - provider: ollama
      model: "gemma4:e4b"
      base_url: "http://localhost:11434"
`)
	check, err := LoadRegressionGate3(newGate3Fixture(t), path, []any{"case-1"})
	if err != nil {
		t.Fatalf("LoadRegressionGate3 with a fallback chain should succeed, got %v", err)
	}
	if check == nil {
		t.Fatal("LoadRegressionGate3 returned a nil check for a fallback chain")
	}
}

func TestLoadRegressionGate3_AllFallbacksDisabled(t *testing.T) {
	// Both the primary and every fallback lack a key: nothing is usable, so
	// loading must report not-enabled.
	path := writeTempConfig(t, `
llm:
  provider: openai
  model: "agnes-2.5-flash"
  base_url: "https://apihub.agnes-ai.com/v1"
  fallbacks:
    - provider: openai
      model: "deepseek-v4-flash"
      base_url: "https://token.sensenova.cn/v1"
`)
	_, err := LoadRegressionGate3(newGate3Fixture(t), path, []any{"case-1"})
	if err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("LoadRegressionGate3 with all-disabled providers should report not-enabled, got %v", err)
	}
}

func TestLoadRegressionGate3_FallbackOnlyEnabled(t *testing.T) {
	// Primary without a key but an enabled ollama fallback: the chain is still
	// usable, so loading must succeed (failover would switch to the fallback).
	path := writeTempConfig(t, `
llm:
  provider: openai
  model: "agnes-2.5-flash"
  base_url: "https://apihub.agnes-ai.com/v1"
  fallbacks:
    - provider: ollama
      model: "gemma4:e4b"
      base_url: "http://localhost:11434"
`)
	check, err := LoadRegressionGate3(newGate3Fixture(t), path, []any{"case-1"})
	if err != nil {
		t.Fatalf("LoadRegressionGate3 with only a fallback enabled should succeed, got %v", err)
	}
	if check == nil {
		t.Fatal("LoadRegressionGate3 returned a nil check when only the fallback is enabled")
	}
}

func TestToLLMConfigs_PrimaryFirst(t *testing.T) {
	primary := ares_config.LLMConfig{
		Provider: "openai",
		Model:    "agnes-2.5-flash",
		APIKey:   "sk-primary",
		BaseURL:  "https://apihub.agnes-ai.com/v1",
		Fallbacks: []ares_config.LLMConfig{
			{Provider: "ollama", Model: "gemma4:e4b", BaseURL: "http://localhost:11434"},
			{Provider: "openai", Model: "deepseek-v4-flash", APIKey: "sk-fb", BaseURL: "https://token.sensenova.cn/v1"},
		},
	}
	configs := toLLMConfigs(primary)
	if len(configs) != 3 {
		t.Fatalf("toLLMConfigs returned %d configs, want 3", len(configs))
	}
	if configs[0].Model != "agnes-2.5-flash" {
		t.Errorf("configs[0].Model = %q, want primary model", configs[0].Model)
	}
	if configs[1].Provider != "ollama" {
		t.Errorf("configs[1].Provider = %q, want ollama fallback", configs[1].Provider)
	}
	if configs[2].Model != "deepseek-v4-flash" {
		t.Errorf("configs[2].Model = %q, want second fallback model", configs[2].Model)
	}
	if configs[0].APIKey != "sk-primary" || configs[2].APIKey != "sk-fb" {
		t.Error("api keys were not carried through the conversion")
	}
}
