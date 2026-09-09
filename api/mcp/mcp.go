// Package mcp is the DEPRECATED public alias of internal/mcpclient (M5).
// New code MUST import internal/mcpclient; this package exists only for
// external consumers and is scheduled for removal.
package mcp

import (
	"context"

	"github.com/Timwood0x10/ares/internal/mcpclient"
)

// Client connects to an MCP server and provides tool access.
type Client = mcpclient.Client

// ToolInfo describes a tool exposed by an MCP server.
type ToolInfo = mcpclient.ToolInfo

// CallResult is the result of calling an MCP tool.
type CallResult = mcpclient.CallResult

// ContentBlock represents a content block in a tool result.
type ContentBlock = mcpclient.ContentBlock

// ServerConfig holds MCP server connection configuration.
type ServerConfig = mcpclient.ServerConfig

// ConnectFromConfig connects to an MCP server from a ServerConfig.
func ConnectFromConfig(ctx context.Context, cfg ServerConfig) (*Client, error) {
	return mcpclient.ConnectFromConfig(ctx, cfg)
}

// ConnectSSE connects to an MCP server via SSE transport.
func ConnectSSE(ctx context.Context, name, url string) (*Client, error) {
	return mcpclient.ConnectSSE(ctx, name, url)
}

// ConnectStdio connects to an MCP server via stdio transport.
func ConnectStdio(ctx context.Context, name, command string, args []string) (*Client, error) {
	return mcpclient.ConnectStdio(ctx, name, command, args)
}

// DiscoverServers scans ~/.claude.json for MCP server definitions.
func DiscoverServers(projectDir string) []ServerConfig {
	return mcpclient.DiscoverServers(projectDir)
}
