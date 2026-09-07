package ares_mcp

import (
	"context"
	"encoding/json"
	"testing"
)

// newTestServer creates a mock server that handles initialize, tools/list, and tools/call.
func newTestServer(tools []MCPToolDef, callResult *ToolCallResult) *mockTransport {
	return newMockServer(func(msg *JSONRPCMessage) *JSONRPCMessage {
		if msg == nil || msg.ID == nil {
			return nil
		}

		switch msg.Method {
		case MethodInitialize:
			result := InitializeResult{
				ProtocolVersion: ProtocolVersion,
				ServerInfo:      Implementation{Name: "mock-server", Version: "1.0.0"},
				Capabilities:    ServerCapabilities{Tools: &ToolServerCapabilities{ListChanged: true}},
			}
			data, _ := json.Marshal(result)
			return &JSONRPCMessage{JSONRPC: JSONRPCVersion, ID: msg.ID, Result: data}

		case MethodToolsList:
			result := ToolsListResult{Tools: tools}
			data, _ := json.Marshal(result)
			return &JSONRPCMessage{JSONRPC: JSONRPCVersion, ID: msg.ID, Result: data}

		case MethodToolsCall:
			if callResult != nil {
				data, _ := json.Marshal(callResult)
				return &JSONRPCMessage{JSONRPC: JSONRPCVersion, ID: msg.ID, Result: data}
			}
			return &JSONRPCMessage{JSONRPC: JSONRPCVersion, ID: msg.ID, Result: []byte(`{"content":[]}`)}

		case MethodPing:
			return &JSONRPCMessage{JSONRPC: JSONRPCVersion, ID: msg.ID, Result: []byte(`{}`)}

		default:
			return &JSONRPCMessage{
				JSONRPC: JSONRPCVersion,
				ID:      msg.ID,
				Error:   &JSONRPCError{Code: MethodNotFound, Message: "method not found: " + msg.Method},
			}
		}
	})
}

func TestMCPClientConnect(t *testing.T) {
	toolDef := buildSimpleToolDef("test_tool", "A test tool")
	server := newTestServer([]MCPToolDef{toolDef}, nil)

	client := NewMCPClient(MCPClientConfig{ServerName: "test-server"})

	ctx := context.Background()
	if err := client.Connect(ctx, server); err != nil {
		t.Fatalf("Connect error: %v", err)
	}
	defer func() { _ = client.Close() }()

	if !client.IsConnected() {
		t.Error("expected client to be connected")
	}
	if client.ServerName() != "test-server" {
		t.Errorf("ServerName = %s, want test-server", client.ServerName())
	}
	if client.ToolCount() != 1 {
		t.Errorf("ToolCount = %d, want 1", client.ToolCount())
	}
	if client.ServerCapabilities() == nil {
		t.Error("ServerCapabilities should not be nil")
	}
}

func TestMCPClientConnectNilTransport(t *testing.T) {
	client := NewMCPClient(MCPClientConfig{ServerName: "test"})
	err := client.Connect(context.Background(), nil)
	if err == nil {
		t.Error("expected error for nil transport")
	}
}

func TestMCPClientListTools(t *testing.T) {
	tools := []MCPToolDef{
		buildSimpleToolDef("tool1", "First tool"),
		buildSimpleToolDef("tool2", "Second tool"),
	}

	server := newTestServer(tools, nil)
	client := NewMCPClient(MCPClientConfig{ServerName: "test"})
	ctx := context.Background()

	if err := client.Connect(ctx, server); err != nil {
		t.Fatalf("Connect error: %v", err)
	}
	defer func() { _ = client.Close() }()

	if client.ToolCount() != 2 {
		t.Errorf("ToolCount = %d, want 2", client.ToolCount())
	}

	def, ok := client.GetTool("tool1")
	if !ok {
		t.Fatal("expected tool1 to exist")
	}
	if def.Description != "First tool" {
		t.Errorf("Description = %s, want 'First tool'", def.Description)
	}
}

func TestMCPClientCallTool(t *testing.T) {
	toolDef := buildSimpleToolDef("echo", "Echo tool")
	callResult := &ToolCallResult{
		Content: []ContentBlock{{Type: "text", Text: "hello world"}},
	}
	server := newTestServer([]MCPToolDef{toolDef}, callResult)

	client := NewMCPClient(MCPClientConfig{ServerName: "test"})
	ctx := context.Background()

	if err := client.Connect(ctx, server); err != nil {
		t.Fatalf("Connect error: %v", err)
	}
	defer func() { _ = client.Close() }()

	result, err := client.CallTool(ctx, "echo", map[string]any{"input": "hello"})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}

	if result.IsError {
		t.Error("expected non-error result")
	}
	if len(result.Content) != 1 {
		t.Fatalf("Content count = %d, want 1", len(result.Content))
	}
	if result.Content[0].Text != "hello world" {
		t.Errorf("Content[0].Text = %s, want 'hello world'", result.Content[0].Text)
	}
}

func TestMCPClientCallToolError(t *testing.T) {
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
			Error:   &JSONRPCError{Code: -32601, Message: "method not found"},
		}
	})

	client := NewMCPClient(MCPClientConfig{ServerName: "test"})
	ctx := context.Background()

	if err := client.Connect(ctx, server); err != nil {
		t.Fatalf("Connect error: %v", err)
	}
	defer func() { _ = client.Close() }()

	_, err := client.CallTool(ctx, "broken", nil)
	if err == nil {
		t.Error("expected error for broken tool call")
	}
}

func TestMCPClientNotConnected(t *testing.T) {
	client := NewMCPClient(MCPClientConfig{ServerName: "test"})
	_, err := client.CallTool(context.Background(), "test", nil)
	if err == nil {
		t.Error("expected error when not connected")
	}
}

func TestMCPClientClose(t *testing.T) {
	toolDef := buildSimpleToolDef("test", "test")
	server := newTestServer([]MCPToolDef{toolDef}, nil)

	client := NewMCPClient(MCPClientConfig{ServerName: "test"})
	ctx := context.Background()

	if err := client.Connect(ctx, server); err != nil {
		t.Fatalf("Connect error: %v", err)
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	if client.IsConnected() {
		t.Error("expected client to be disconnected after close")
	}
}

func TestNewTransportFromConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  TransportConfig
		wantErr bool
	}{
		{
			name: "stdio",
			config: TransportConfig{
				Type:  "stdio",
				Stdio: &StdioConfig{Command: "echo"},
			},
		},
		{
			name: "sse",
			config: TransportConfig{
				Type: "sse",
				SSE:  &SSEConfig{URL: "http://localhost:8080"},
			},
		},
		{
			name:    "unsupported type",
			config:  TransportConfig{Type: "unknown"},
			wantErr: true,
		},
		{
			name:    "stdio without config",
			config:  TransportConfig{Type: "stdio"},
			wantErr: true,
		},
		{
			name:    "sse without config",
			config:  TransportConfig{Type: "sse"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewTransportFromConfig(tt.config)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}
