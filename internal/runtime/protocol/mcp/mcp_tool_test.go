package ares_mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

func TestNewMCPTool(t *testing.T) {
	toolDef := buildSimpleToolDef("search", "Search the web")
	server := newTestServer([]MCPToolDef{toolDef}, nil)

	client := NewMCPClient(MCPClientConfig{ServerName: "myserver"})
	ctx := context.Background()

	if err := client.Connect(ctx, server); err != nil {
		t.Fatalf("Connect error: %v", err)
	}
	defer func() { _ = client.Close() }()

	mcpTool, err := NewMCPTool(client, &toolDef)
	if err != nil {
		t.Fatalf("NewMCPTool error: %v", err)
	}

	// Verify namespaced name.
	if mcpTool.Name() != "mcp.myserver.search" {
		t.Errorf("Name = %s, want mcp.myserver.search", mcpTool.Name())
	}

	// Verify category.
	if mcpTool.Category() != core.CategoryExternal {
		t.Errorf("Category = %s, want %s", mcpTool.Category(), core.CategoryExternal)
	}

	// Verify capabilities.
	caps := mcpTool.Capabilities()
	if len(caps) != 1 || caps[0] != core.CapabilityExternal {
		t.Errorf("Capabilities = %v, want [%s]", caps, core.CapabilityExternal)
	}

	// Verify description.
	if mcpTool.Description() != "Search the web" {
		t.Errorf("Description = %s, want 'Search the web'", mcpTool.Description())
	}

	// Verify server name.
	if mcpTool.ServerName() != "myserver" {
		t.Errorf("ServerName = %s, want myserver", mcpTool.ServerName())
	}

	// Verify MCP tool name.
	if mcpTool.MCPTName() != "search" {
		t.Errorf("MCPTName = %s, want search", mcpTool.MCPTName())
	}

	// Verify parameters.
	params := mcpTool.Parameters()
	if params == nil {
		t.Fatal("Parameters should not be nil")
	}
	if _, ok := params.Properties["input"]; !ok {
		t.Error("expected 'input' parameter")
	}
}

func TestNewMCPToolNilClient(t *testing.T) {
	_, err := NewMCPTool(nil, &MCPToolDef{Name: "test"})
	if err == nil {
		t.Error("expected error for nil client")
	}
}

func TestNewMCPToolNilDef(t *testing.T) {
	server := newTestServer(nil, nil)
	client := NewMCPClient(MCPClientConfig{ServerName: "test"})
	_ = client.Connect(context.Background(), server)
	defer func() { _ = client.Close() }()

	_, err := NewMCPTool(client, nil)
	if err == nil {
		t.Error("expected error for nil def")
	}
}

func TestMCPToolExecute(t *testing.T) {
	toolDef := buildSimpleToolDef("echo", "Echo tool")
	callResult := &ToolCallResult{
		Content: []ContentBlock{{Type: "text", Text: "result text"}},
	}
	server := newTestServer([]MCPToolDef{toolDef}, callResult)

	client := NewMCPClient(MCPClientConfig{ServerName: "test"})
	ctx := context.Background()

	if err := client.Connect(ctx, server); err != nil {
		t.Fatalf("Connect error: %v", err)
	}
	defer func() { _ = client.Close() }()

	mcpTool, err := NewMCPTool(client, &toolDef)
	if err != nil {
		t.Fatalf("NewMCPTool error: %v", err)
	}

	result, err := mcpTool.Execute(ctx, map[string]interface{}{"input": "hello"})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if !result.Success {
		t.Error("expected success")
	}
}

func TestMCPToolExecuteError(t *testing.T) {
	server := newMockServer(func(msg *JSONRPCMessage) *JSONRPCMessage {
		if msg.Method == MethodInitialize {
			result := InitializeResult{
				ProtocolVersion: ProtocolVersion,
				ServerInfo:      Implementation{Name: "mock", Version: "1.0"},
				Capabilities:    ServerCapabilities{Tools: &ToolServerCapabilities{}},
			}
			data, _ := json.Marshal(result)
			return &JSONRPCMessage{JSONRPC: JSONRPCVersion, ID: msg.ID, Result: data}
		}
		if msg.Method == MethodToolsList {
			return &JSONRPCMessage{JSONRPC: JSONRPCVersion, ID: msg.ID, Result: []byte(`{"tools":[]}`)}
		}
		return &JSONRPCMessage{
			JSONRPC: JSONRPCVersion,
			ID:      msg.ID,
			Error:   &JSONRPCError{Code: -1, Message: "tool failed"},
		}
	})

	client := NewMCPClient(MCPClientConfig{ServerName: "test"})
	ctx := context.Background()

	if err := client.Connect(ctx, server); err != nil {
		t.Fatalf("Connect error: %v", err)
	}
	defer func() { _ = client.Close() }()

	toolDef := buildSimpleToolDef("fail", "Failing tool")
	mcpTool, _ := NewMCPTool(client, &toolDef)
	result, err := mcpTool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if result.Success {
		t.Error("expected non-success result for server error")
	}
}

func TestMCPToolExecuteIsError(t *testing.T) {
	toolDef := buildSimpleToolDef("fail", "Failing tool")
	callResult := &ToolCallResult{
		Content: []ContentBlock{{Type: "text", Text: "something went wrong"}},
		IsError: true,
	}
	server := newTestServer([]MCPToolDef{toolDef}, callResult)

	client := NewMCPClient(MCPClientConfig{ServerName: "test"})
	ctx := context.Background()

	if err := client.Connect(ctx, server); err != nil {
		t.Fatalf("Connect error: %v", err)
	}
	defer func() { _ = client.Close() }()

	mcpTool, _ := NewMCPTool(client, &toolDef)
	res, err := mcpTool.Execute(ctx, nil)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if res.Success {
		t.Error("expected non-success for error result")
	}
}

func TestExtractText(t *testing.T) {
	tests := []struct {
		name   string
		blocks []ContentBlock
		want   string
	}{
		{"empty", nil, ""},
		{"single text", []ContentBlock{{Type: "text", Text: "hello"}}, "hello"},
		{"multiple text", []ContentBlock{
			{Type: "text", Text: "hello "},
			{Type: "text", Text: "world"},
		}, "hello world"},
		{"mixed types", []ContentBlock{
			{Type: "text", Text: "text"},
			{Type: "image", Data: "base64data"},
		}, "text"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractText(tt.blocks)
			if got != tt.want {
				t.Errorf("extractText = %q, want %q", got, tt.want)
			}
		})
	}
}
