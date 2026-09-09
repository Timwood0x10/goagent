package llmcore

import (
	"testing"
)

func TestDefaultCleanOptions_Values(t *testing.T) {
	opts := DefaultCleanOptions()
	if opts.MaxUserLen <= 0 {
		t.Errorf("expected MaxUserLen > 0, got %d", opts.MaxUserLen)
	}
	if opts.MaxAssistantLen <= 0 {
		t.Errorf("expected MaxAssistantLen > 0, got %d", opts.MaxAssistantLen)
	}
	if opts.MaxToolLen <= 0 {
		t.Errorf("expected MaxToolLen > 0, got %d", opts.MaxToolLen)
	}
	if opts.Mode != CleaningModeDefault {
		t.Errorf("expected Mode=CleaningModeDefault, got %v", opts.Mode)
	}
	if !opts.KeepRawToolDetails {
		t.Errorf("expected KeepRawToolDetails=true")
	}
}

func TestCleaningMode_Values(t *testing.T) {
	if CleaningModeDefault != 0 {
		t.Errorf("expected CleaningModeDefault=0, got %d", CleaningModeDefault)
	}
	if CleaningModeConservative != 1 {
		t.Errorf("expected CleaningModeConservative=1, got %d", CleaningModeConservative)
	}
	if CleaningModeAggressive != 2 {
		t.Errorf("expected CleaningModeAggressive=2, got %d", CleaningModeAggressive)
	}
}

func TestCleanerStats_Zero(t *testing.T) {
	var s CleanerStats
	if s.LLMCalls != 0 || s.ToolCalls != 0 || s.BytesSaved != 0 {
		t.Error("expected zero-value CleanerStats")
	}
}
