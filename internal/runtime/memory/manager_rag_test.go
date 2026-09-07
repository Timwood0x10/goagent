// Package memory - RAG configuration validation tests for MemoryConfig.
package memory

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// validBaseMemoryConfig returns a MemoryConfig seeded with values that pass
// every non-RAG validate() check. RAG-specific fields are left zero so each
// table-driven case can set them independently.
func validBaseMemoryConfig() *MemoryConfig {
	cfg := DefaultMemoryConfig()
	cfg.EnableRAG = false
	cfg.RAGTopK = 0
	cfg.RAGMinScore = 0
	return cfg
}

// TestMemoryConfigValidate_RAG covers the RAG-specific branches of
// MemoryConfig.validate(): when EnableRAG is true, RAGTopK must be positive
// and RAGMinScore must be non-negative. When EnableRAG is false, zero/negative
// RAG fields are tolerated (defaults are applied lazily at retrieval time).
func TestMemoryConfigValidate_RAG(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(*MemoryConfig)
		wantErr   bool
		wantIsErr error // sentinel that should be matched via errors.Is
	}{
		{
			name: "rag_disabled_zero_topk_ok",
			mutate: func(c *MemoryConfig) {
				c.EnableRAG = false
				c.RAGTopK = 0
				c.RAGMinScore = 0
			},
			wantErr: false,
		},
		{
			name: "rag_disabled_negative_topk_ok",
			mutate: func(c *MemoryConfig) {
				c.EnableRAG = false
				c.RAGTopK = -3
				c.RAGMinScore = -0.1
			},
			wantErr: false,
		},
		{
			name: "rag_enabled_valid_topk_minscore_ok",
			mutate: func(c *MemoryConfig) {
				c.EnableRAG = true
				c.RAGTopK = 5
				c.RAGMinScore = 0.4
			},
			wantErr: false,
		},
		{
			name: "rag_enabled_zero_topk_invalid",
			mutate: func(c *MemoryConfig) {
				c.EnableRAG = true
				c.RAGTopK = 0
				c.RAGMinScore = 0.4
			},
			wantErr:   true,
			wantIsErr: ErrInvalidRAGConfig,
		},
		{
			name: "rag_enabled_negative_topk_invalid",
			mutate: func(c *MemoryConfig) {
				c.EnableRAG = true
				c.RAGTopK = -1
				c.RAGMinScore = 0.4
			},
			wantErr:   true,
			wantIsErr: ErrInvalidRAGConfig,
		},
		{
			name: "rag_enabled_zero_minscore_ok",
			mutate: func(c *MemoryConfig) {
				c.EnableRAG = true
				c.RAGTopK = 5
				c.RAGMinScore = 0
			},
			wantErr: false,
		},
		{
			name: "rag_enabled_negative_minscore_invalid",
			mutate: func(c *MemoryConfig) {
				c.EnableRAG = true
				c.RAGTopK = 5
				c.RAGMinScore = -0.01
			},
			wantErr:   true,
			wantIsErr: ErrInvalidRAGConfig,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validBaseMemoryConfig()
			tc.mutate(cfg)
			err := cfg.validate()
			if tc.wantErr {
				require.Error(t, err, "expected validate() to fail for %s", tc.name)
				if tc.wantIsErr != nil {
					require.True(t, errors.Is(err, tc.wantIsErr),
						"expected error to wrap %v, got %v", tc.wantIsErr, err)
				}
				return
			}
			require.NoError(t, err, "expected validate() to pass for %s", tc.name)
		})
	}
}

// TestDefaultMemoryConfig_RAGDefaults verifies DefaultMemoryConfig seeds RAG
// fields with the documented opt-in defaults: EnableRAG=false, RAGTopK=5,
// RAGMinScore=0.4. The defaults must also pass validate().
func TestDefaultMemoryConfig_RAGDefaults(t *testing.T) {
	cfg := DefaultMemoryConfig()
	require.False(t, cfg.EnableRAG, "EnableRAG must default to false (opt-in)")
	require.Equal(t, 5, cfg.RAGTopK, "RAGTopK must default to 5")
	require.Equal(t, 0.4, cfg.RAGMinScore, "RAGMinScore must default to 0.4")
	// Flipping EnableRAG to true should still validate since defaults are seeded.
	cfg.EnableRAG = true
	require.NoError(t, cfg.validate(), "default RAG config must validate when enabled")
}
