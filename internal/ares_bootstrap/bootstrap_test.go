package ares_bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/ares_config"
	ares_memory "github.com/Timwood0x10/ares/internal/runtime/memory"
)

func TestProvideRuntime(t *testing.T) {
	rt, err := ProvideRuntime(nil)
	require.NoError(t, err)
	require.NotNil(t, rt)
}

func TestProvideMemory_DefaultConfig(t *testing.T) {
	mem, err := ProvideMemory(nil)
	require.NoError(t, err)
	require.NotNil(t, mem)
}

func TestProvideMemory_CustomConfig(t *testing.T) {
	cfg := ares_memory.DefaultMemoryConfig()
	mem, err := ProvideMemory(cfg)
	require.NoError(t, err)
	require.NotNil(t, mem)
}

func TestBootstrap_WithMinimalConfig(t *testing.T) {
	ctx := context.Background()
	cfg := &ares_config.Config{
		LLM: ares_config.LLMConfig{
			Provider: "mock",
			Model:    "mock-model",
			APIKey:   "test-key",
			BaseURL:  "http://localhost:9999",
		},
		Memory: ares_config.MemoryConfig{Enabled: boolPtr(true)},
	}
	comp, err := Bootstrap(ctx, cfg, nil)
	require.NoError(t, err)
	require.NotNil(t, comp)
	assert.NotNil(t, comp.EventStore)
	assert.NotNil(t, comp.Runtime)
	assert.NotNil(t, comp.Memory)
	assert.NotNil(t, comp.LLM)
	assert.NotNil(t, comp.Dashboard)
}

func TestBootstrap_WithDeps(t *testing.T) {
	ctx := context.Background()
	cfg := &ares_config.Config{
		Memory: ares_config.MemoryConfig{Enabled: boolPtr(true)},
	}
	comp, err := Bootstrap(ctx, cfg, &BootstrapDeps{
		LLMClient: &mockLLMClient{},
	})
	require.NoError(t, err)
	require.NotNil(t, comp)
	assert.NotNil(t, comp.LLM)
}

// boolPtr returns a pointer to a bool literal for *bool config fields.
func boolPtr(b bool) *bool { return &b }

// mockLLMClient is a minimal mock for eval.LLMClient.
type mockLLMClient struct{}

func (m *mockLLMClient) Generate(ctx context.Context, prompt string) (string, error) {
	return "mock response", nil
}

func (m *mockLLMClient) Close() error {
	return nil
}
