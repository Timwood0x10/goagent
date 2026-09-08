package builtin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

func TestNewEmbeddingTool(t *testing.T) {
	tool := NewEmbeddingTool("")
	require.NotNil(t, tool)
	require.Equal(t, "embedding", tool.Name())
	require.Equal(t, core.CategoryExternal, tool.Category())
}

func TestEmbeddingTool_UnknownAction(t *testing.T) {
	tool := NewEmbeddingTool("")
	ctx := context.Background()

	result, err := tool.Execute(ctx, map[string]interface{}{
		"action": "invalid",
	})
	require.NoError(t, err)
	require.False(t, result.Success)
}

func TestEmbeddingTool_EmbedMissingText(t *testing.T) {
	tool := NewEmbeddingTool("")
	ctx := context.Background()

	// embed action without text should return error
	result, err := tool.Execute(ctx, map[string]interface{}{
		"action": "embed",
	})
	require.NoError(t, err)
	require.False(t, result.Success)
}

func TestEmbeddingTool_BatchMissingTexts(t *testing.T) {
	tool := NewEmbeddingTool("")
	ctx := context.Background()

	// embed_batch action without texts should return error
	result, err := tool.Execute(ctx, map[string]interface{}{
		"action": "embed_batch",
	})
	require.NoError(t, err)
	require.False(t, result.Success)
}

func TestEmbeddingTool_Parameters(t *testing.T) {
	tool := NewEmbeddingTool("http://test:8000")
	params := tool.Parameters()
	require.NotNil(t, params)
	require.Contains(t, params.Properties, "action")
	require.Contains(t, params.Properties, "text")
	require.Contains(t, params.Properties, "texts")
	require.Contains(t, params.Properties, "prefix")
	require.Contains(t, params.Required, "action")
}

// TestEmbeddingTool_BatchTextsElementTypes locks the strict element type
// contract of the 'texts' parameter: a non-string element is rejected with
// an error result naming the offending index (never silently zero-folded to
// ""), while an all-strings batch passes validation and proceeds to the
// HTTP call (which fails against the unreachable test endpoint — the call
// attempt itself proves validation passed).
func TestEmbeddingTool_BatchTextsElementTypes(t *testing.T) {
	// Point at a closed port so the request fails fast instead of hanging.
	unreachable := "http://127.0.0.1:1"

	tests := []struct {
		name    string
		texts   []interface{}
		wantErr string
	}{
		{
			name:    "all strings pass validation and reach the HTTP call",
			texts:   []interface{}{"alpha", "beta"},
			wantErr: "", // connection error, not a validation error
		},
		{
			name:    "mixed types reject naming the first non-string index",
			texts:   []interface{}{"alpha", 42, "beta"},
			wantErr: "'texts'[1] must be a string, got int",
		},
		{
			name:    "bool element rejects",
			texts:   []interface{}{true},
			wantErr: "'texts'[0] must be a string, got bool",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := NewEmbeddingTool(unreachable)
			result, err := tool.Execute(context.Background(), map[string]interface{}{
				"action": "embed_batch",
				"texts":  tt.texts,
			})
			require.NoError(t, err, "tool errors ride in the Result, not the error return")
			require.NotNil(t, result)
			if tt.wantErr == "" {
				// Validation passed: the failure is the (expected) unreachable
				// service error, which is NOT the element-type message.
				require.False(t, result.Success)
				require.NotContains(t, result.Error, "must be a string")
				return
			}
			require.False(t, result.Success)
			require.Contains(t, result.Error, tt.wantErr)
		})
	}
}
