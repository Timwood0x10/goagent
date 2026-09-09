// Package mcpclient provides the MCP (Model Context Protocol) client used
// by the sdk and cmd layers (M5 internalization of the former api/mcp).
//
// Usage:
//
//	client, err := mcpclient.ConnectStdio(ctx, "my-server", "codegraph", []string{"serve", "--mcp"})
//	tools, _ := client.ListTools(ctx)
//	result, _ := client.CallTool(ctx, "tool_name", map[string]any{"key": "value"})
package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// transport abstracts MCP transport (stdio, SSE, etc.).
type transport interface {
	roundTrip(ctx context.Context, req jsonrpcRequest) (*jsonrpcResponse, error)
	notify(ctx context.Context, notif jsonrpcNotification) error
	close() error
}

// Client connects to an MCP server and provides tool access.
type Client struct {
	name      string
	transport transport
	mu        sync.Mutex
	idCounter int
	tools     []ToolInfo
	connected bool
}

// ToolInfo describes a tool exposed by an MCP server.
type ToolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// CallResult is the result of calling an MCP tool.
type CallResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"is_error"`
}

// ContentBlock represents a content block in a tool result.
type ContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

func (c *Client) initialize(ctx context.Context) error {
	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      c.nextID(),
		Method:  "initialize",
		Params: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "ares-mcp-client",
				"version": "1.0.0",
			},
		},
	}

	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return err
	}
	if resp.Error != nil {
		return fmt.Errorf("initialize error: %s", resp.Error.Message)
	}

	notif := jsonrpcNotification{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	_ = c.sendNotification(ctx, notif) //nolint: errcheck

	return nil
}

// ListTools returns all tools exposed by the MCP server.
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      c.nextID(),
		Method:  "tools/list",
	}

	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("list tools error: %s", resp.Error.Message)
	}

	var result struct {
		Tools []ToolInfo `json:"tools"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("unmarshal tools: %w", err)
	}

	c.tools = result.Tools
	return c.tools, nil
}

// CallTool invokes a tool on the MCP server.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (*CallResult, error) {
	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      c.nextID(),
		Method:  "tools/call",
		Params: map[string]any{
			"name":      name,
			"arguments": args,
		},
	}

	resp, err := c.sendRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return &CallResult{IsError: true}, fmt.Errorf("call tool error: %s", resp.Error.Message)
	}

	var result CallResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("unmarshal result: %w", err)
	}

	return &result, nil
}

// Name returns the server name.
func (c *Client) Name() string {
	return c.name
}

// Close closes the MCP connection.
func (c *Client) Close() error {
	return c.transport.close()
}

// sendRequest sends a JSON-RPC request through the transport.
func (c *Client) sendRequest(ctx context.Context, req jsonrpcRequest) (*jsonrpcResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transport.roundTrip(ctx, req)
}

func (c *Client) sendNotification(ctx context.Context, notif jsonrpcNotification) error {
	// Notifications never receive a response. Write directly on the transport
	// instead of roundTrip, which for stdio waits up to 30s for a reply that
	// never arrives (and then closes stdin, killing the connection).
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.transport.notify(ctx, notif)
}

func (c *Client) nextID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.idCounter
	c.idCounter++
	return id
}

// jsonrpcRequest is a JSON-RPC 2.0 request.
type jsonrpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type jsonrpcNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// jsonrpcResponse is a JSON-RPC 2.0 response.
type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ServerConfig holds MCP server connection configuration.
type ServerConfig struct {
	Name    string   `json:"name"`
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	URL     string   `json:"url,omitempty"`
}

// ConnectFromConfig connects to an MCP server from a ServerConfig.
func ConnectFromConfig(ctx context.Context, cfg ServerConfig) (*Client, error) {
	if cfg.Command != "" {
		return ConnectStdio(ctx, cfg.Name, cfg.Command, cfg.Args)
	}
	if cfg.URL != "" {
		return ConnectSSE(ctx, cfg.Name, cfg.URL)
	}
	return nil, fmt.Errorf("either command or url is required")
}

// DiscoverServers scans ~/.claude.json for MCP server definitions.
func DiscoverServers(projectDir string) []ServerConfig {
	home, _ := os.UserHomeDir()
	var servers []ServerConfig
	seen := make(map[string]bool)

	if home != "" {
		servers = append(servers, scanClaudeConfig(home+"/.claude.json", seen)...)
	}
	if projectDir != "" {
		servers = append(servers, scanClaudeConfig(projectDir+"/.claude/settings.json", seen)...)
	}
	return servers
}

func scanClaudeConfig(path string, seen map[string]bool) []ServerConfig {
	data, err := os.ReadFile(path) // #nosec G304 - path is constructed from known base dirs
	if err != nil {
		return nil
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil
	}
	var servers []ServerConfig
	for name, sc := range cfg.MCPServers {
		if seen[name] {
			continue
		}
		seen[name] = true
		servers = append(servers, ServerConfig{
			Name:    name,
			Command: sc.Command,
			Args:    sc.Args,
		})
	}
	return servers
}
