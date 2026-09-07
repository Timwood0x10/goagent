package ares_mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/Timwood0x10/ares/internal/tools/resources/base"
	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

const (
	contentTypeText = "text"
)

// MCPTool wraps an MCP tool definition as a ares Tool.
type MCPTool struct {
	*base.BaseTool
	client     *MCPClient
	serverName string
	toolDef    *MCPToolDef
}

// NewMCPTool creates an MCPTool from an MCP tool definition.
func NewMCPTool(client *MCPClient, def *MCPToolDef) (*MCPTool, error) {
	if client == nil {
		return nil, errors.New("client is required")
	}
	if def == nil {
		return nil, errors.New("tool definition is required")
	}

	schema, err := ConvertJSONSchema(def.InputSchema)
	if err != nil {
		return nil, fmt.Errorf("convert schema for %s: %w", def.Name, err)
	}

	name := fmt.Sprintf("mcp.%s.%s", client.ServerName(), def.Name)

	bt := base.NewBaseToolWithCapabilities(
		name,
		def.Description,
		core.CategoryExternal,
		[]core.Capability{core.CapabilityExternal},
		schema,
	)

	return &MCPTool{
		BaseTool:   bt,
		client:     client,
		serverName: client.ServerName(),
		toolDef:    def,
	}, nil
}

// Execute delegates the tool call to the MCP server.
func (t *MCPTool) Execute(ctx context.Context, params map[string]interface{}) (core.Result, error) {
	result, err := t.client.CallTool(ctx, t.toolDef.Name, params)
	if err != nil {
		return core.NewErrorResult(err.Error()), nil
	}

	if result.IsError {
		text := extractText(result.Content)
		return core.NewErrorResult(text), nil
	}

	text := extractText(result.Content)
	return core.NewResult(true, map[string]interface{}{
		"content": text,
		"blocks":  result.Content,
	}), nil
}

// ServerName returns the MCP server name this tool belongs to.
func (t *MCPTool) ServerName() string {
	return t.serverName
}

// Close is a no-op. The underlying client connection lifecycle is owned by
// MCPManager — closing one MCPTool must not cut the shared connection for
// other tools on the same server (P0-4). Safe to call multiple times.
func (t *MCPTool) Close() error {
	return nil
}

// MCPTName returns the original tool name on the MCP server (without namespace).
func (t *MCPTool) MCPTName() string {
	return t.toolDef.Name
}

// extractText concatenates text from all text-type content blocks.
func extractText(blocks []ContentBlock) string {
	if len(blocks) == 0 {
		return ""
	}

	totalLen := 0
	for _, b := range blocks {
		if b.Type == contentTypeText {
			totalLen += len(b.Text)
		}
	}

	buf := make([]byte, 0, totalLen)
	for _, b := range blocks {
		if b.Type == contentTypeText {
			buf = append(buf, b.Text...)
		}
	}

	return string(buf)
}

// Ensure MCPTool implements core.Tool at compile time.
var _ core.Tool = (*MCPTool)(nil)
