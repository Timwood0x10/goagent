package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Test constants matching values used in production.
const (
	jsonrpcVersion          = "2.0"
	methodToolsList         = "tools/list"
	methodToolsCall         = "tools/call"
	methodInitialize        = "initialize"
	methodNotificationsInit = "notifications/initialized"
	protocolVersion         = "2024-11-05"
	mcpClientName           = "ares-mcp-client"
)

// mockTransport implements the transport interface for testing.
type mockTransport struct {
	roundTripFn func(ctx context.Context, req jsonrpcRequest) (*jsonrpcResponse, error)
	notifyFn    func(ctx context.Context, notif jsonrpcNotification) error
	closeCalled bool
}

func (m *mockTransport) roundTrip(ctx context.Context, req jsonrpcRequest) (*jsonrpcResponse, error) {
	return m.roundTripFn(ctx, req)
}

func (m *mockTransport) notify(ctx context.Context, notif jsonrpcNotification) error {
	if m.notifyFn != nil {
		return m.notifyFn(ctx, notif)
	}
	// Preserve existing tests that only configure roundTripFn by routing
	// notifications through it.
	req := jsonrpcRequest{JSONRPC: notif.JSONRPC, Method: notif.Method, Params: notif.Params}
	_, err := m.roundTrip(ctx, req)
	return err
}

func (m *mockTransport) close() error {
	m.closeCalled = true
	return nil
}

func TestJSONRPCRequestSerialization(t *testing.T) {
	tests := []struct {
		name   string
		req    jsonrpcRequest
		expect string
	}{
		{
			name:   "basic request",
			req:    jsonrpcRequest{JSONRPC: jsonrpcVersion, ID: 1, Method: methodToolsList},
			expect: `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		},
		{
			name:   "request with params",
			req:    jsonrpcRequest{JSONRPC: jsonrpcVersion, ID: 2, Method: methodToolsCall, Params: map[string]any{"name": "test-tool"}},
			expect: `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"test-tool"}}`,
		},
		{
			name:   "initialize request",
			req:    jsonrpcRequest{JSONRPC: jsonrpcVersion, ID: 1, Method: methodInitialize, Params: map[string]any{"protocolVersion": protocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": mcpClientName, "version": "1.0.0"}}},
			expect: `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"capabilities":{},"clientInfo":{"name":"ares-mcp-client","version":"1.0.0"},"protocolVersion":"2024-11-05"}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.req)
			if err != nil {
				t.Fatalf("marshal error: %v", err)
			}
			if string(data) != tc.expect {
				t.Errorf("got %s, want %s", string(data), tc.expect)
			}

			var decoded jsonrpcRequest
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("unmarshal error: %v", err)
			}
			if decoded.JSONRPC != tc.req.JSONRPC || decoded.ID != tc.req.ID || decoded.Method != tc.req.Method {
				t.Errorf("roundtrip mismatch: %+v", decoded)
			}
		})
	}
}

func TestJSONRPCResponseSerialization(t *testing.T) {
	tests := []struct {
		name   string
		resp   jsonrpcResponse
		expect string
	}{
		{
			name:   "success response",
			resp:   jsonrpcResponse{JSONRPC: jsonrpcVersion, ID: 1, Result: json.RawMessage(`{"tools":[]}`)},
			expect: `{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`,
		},
		{
			name:   "error response",
			resp:   jsonrpcResponse{JSONRPC: jsonrpcVersion, ID: 1, Error: &jsonrpcError{Code: -32601, Message: "Method not found"}},
			expect: `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.resp)
			if err != nil {
				t.Fatalf("marshal error: %v", err)
			}
			if string(data) != tc.expect {
				t.Errorf("got %s, want %s", string(data), tc.expect)
			}

			var decoded jsonrpcResponse
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("unmarshal error: %v", err)
			}
			if decoded.JSONRPC != tc.resp.JSONRPC || decoded.ID != tc.resp.ID {
				t.Errorf("roundtrip mismatch: %+v", decoded)
			}
		})
	}
}

func TestJSONRPCNotificationSerialization(t *testing.T) {
	n := jsonrpcNotification{JSONRPC: jsonrpcVersion, Method: methodNotificationsInit}
	data, err := json.Marshal(n)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	// Notifications must not have an "id" field.
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if _, ok := raw["id"]; ok {
		t.Error("notification must not include id field")
	}
}

func TestJSONRPCErrorStruct(t *testing.T) {
	e := jsonrpcError{Code: -32700, Message: "Parse error"}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	expect := `{"code":-32700,"message":"Parse error"}`
	if string(data) != expect {
		t.Errorf("got %s, want %s", string(data), expect)
	}
}

func TestToolInfoSerialization(t *testing.T) {
	tests := []struct {
		name   string
		tool   ToolInfo
		expect string
	}{
		{
			name:   "full tool",
			tool:   ToolInfo{Name: "web_search", Description: "Search the web"},
			expect: `{"name":"web_search","description":"Search the web"}`,
		},
		{
			name:   "tool without description",
			tool:   ToolInfo{Name: "calculator"},
			expect: `{"name":"calculator"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.tool)
			if err != nil {
				t.Fatalf("marshal error: %v", err)
			}
			if string(data) != tc.expect {
				t.Errorf("got %s, want %s", string(data), tc.expect)
			}
			var decoded ToolInfo
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("unmarshal error: %v", err)
			}
			if decoded.Name != tc.tool.Name || decoded.Description != tc.tool.Description {
				t.Errorf("roundtrip mismatch: %+v", decoded)
			}
		})
	}
}

func TestCallResultSerialization(t *testing.T) {
	tests := []struct {
		name   string
		result CallResult
		expect string
	}{
		{
			name:   "success result",
			result: CallResult{Content: []ContentBlock{{Type: "text", Text: "Hello"}}},
			expect: `{"content":[{"type":"text","text":"Hello"}],"is_error":false}`,
		},
		{
			name:   "error result",
			result: CallResult{Content: []ContentBlock{}, IsError: true},
			expect: `{"content":[],"is_error":true}`,
		},
		{
			name:   "resource result with mime type",
			result: CallResult{Content: []ContentBlock{{Type: "resource", Text: "data", MimeType: "text/plain"}}},
			expect: `{"content":[{"type":"resource","text":"data","mimeType":"text/plain"}],"is_error":false}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.result)
			if err != nil {
				t.Fatalf("marshal error: %v", err)
			}
			if string(data) != tc.expect {
				t.Errorf("got %s, want %s", string(data), tc.expect)
			}
			var decoded CallResult
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("unmarshal error: %v", err)
			}
			if len(decoded.Content) != len(tc.result.Content) || decoded.IsError != tc.result.IsError {
				t.Errorf("roundtrip mismatch: %+v", decoded)
			}
		})
	}
}

func TestServerConfigJSON(t *testing.T) {
	tests := []struct {
		name   string
		cfg    ServerConfig
		expect string
	}{
		{
			name:   "stdio config",
			cfg:    ServerConfig{Name: "codegraph", Command: "npx", Args: []string{"codegraph", "serve", "--mcp"}},
			expect: `{"name":"codegraph","command":"npx","args":["codegraph","serve","--mcp"]}`,
		},
		{
			name:   "sse config",
			cfg:    ServerConfig{Name: "remote", URL: "http://localhost:8080/sse"},
			expect: `{"name":"remote","url":"http://localhost:8080/sse"}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.cfg)
			if err != nil {
				t.Fatalf("marshal error: %v", err)
			}
			if string(data) != tc.expect {
				t.Errorf("got %s, want %s", string(data), tc.expect)
			}
			var decoded ServerConfig
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("unmarshal error: %v", err)
			}
			if decoded.Name != tc.cfg.Name || decoded.Command != tc.cfg.Command {
				t.Errorf("roundtrip mismatch: %+v", decoded)
			}
		})
	}
}

func TestConnectFromConfig_EmptyConfig(t *testing.T) {
	cfg := ServerConfig{Name: "empty"}
	_, err := ConnectFromConfig(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error for empty config")
	}
	if err.Error() != "either command or url is required" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestConnectFromConfig_SSEWithoutURL(t *testing.T) {
	cfg := ServerConfig{Name: "sse", URL: ""}
	_, err := ConnectFromConfig(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error for empty url")
	}
}

func TestClient_NewAndName(t *testing.T) {
	c := &Client{
		name:      "test-server",
		transport: &mockTransport{},
		idCounter: 1,
	}
	if got := c.Name(); got != "test-server" {
		t.Errorf("Name() = %s, want %s", got, "test-server")
	}
}

func TestClient_NextID(t *testing.T) {
	c := &Client{
		name:      "test",
		transport: &mockTransport{},
		idCounter: 0,
	}
	ids := []int{c.nextID(), c.nextID(), c.nextID()}
	expected := []int{0, 1, 2}
	for i, id := range ids {
		if id != expected[i] {
			t.Errorf("nextID()[%d] = %d, want %d", i, id, expected[i])
		}
	}
}

func TestClient_ListTools(t *testing.T) {
	mock := &mockTransport{
		roundTripFn: func(_ context.Context, req jsonrpcRequest) (*jsonrpcResponse, error) {
			if req.Method != "tools/list" {
				t.Errorf("method = %s, want tools/list", req.Method)
			}
			return &jsonrpcResponse{
				JSONRPC: jsonrpcVersion,
				ID:      req.ID,
				Result:  json.RawMessage(`{"tools":[{"name":"web_search","description":"Search the web"},{"name":"calculator"}]}`),
			}, nil
		},
	}
	c := &Client{name: "test", transport: mock, idCounter: 1}
	tools, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools error: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("got %d tools, want 2", len(tools))
	}
	if tools[0].Name != "web_search" || tools[0].Description != "Search the web" {
		t.Errorf("tool[0] = %+v", tools[0])
	}
	if tools[1].Name != "calculator" || tools[1].Description != "" {
		t.Errorf("tool[1] = %+v", tools[1])
	}
}

func TestClient_ListTools_TransportError(t *testing.T) {
	mock := &mockTransport{
		roundTripFn: func(_ context.Context, req jsonrpcRequest) (*jsonrpcResponse, error) {
			return nil, errors.New("connection refused")
		},
	}
	c := &Client{name: "test", transport: mock, idCounter: 1}
	_, err := c.ListTools(context.Background())
	if err == nil {
		t.Fatal("expected error from transport")
	}
}

func TestClient_ListTools_RemoteError(t *testing.T) {
	mock := &mockTransport{
		roundTripFn: func(_ context.Context, req jsonrpcRequest) (*jsonrpcResponse, error) {
			return &jsonrpcResponse{
				JSONRPC: jsonrpcVersion,
				ID:      req.ID,
				Error:   &jsonrpcError{Code: -32601, Message: "Method not found"},
			}, nil
		},
	}
	c := &Client{name: "test", transport: mock, idCounter: 1}
	_, err := c.ListTools(context.Background())
	if err == nil {
		t.Fatal("expected JSON-RPC error")
	}
}

func TestClient_ListTools_BadResult(t *testing.T) {
	mock := &mockTransport{
		roundTripFn: func(_ context.Context, req jsonrpcRequest) (*jsonrpcResponse, error) {
			return &jsonrpcResponse{
				JSONRPC: jsonrpcVersion,
				ID:      req.ID,
				Result:  json.RawMessage(`invalid json`),
			}, nil
		},
	}
	c := &Client{name: "test", transport: mock, idCounter: 1}
	_, err := c.ListTools(context.Background())
	if err == nil {
		t.Fatal("expected unmarshal error")
	}
}

func TestClient_CallTool_Success(t *testing.T) {
	mock := &mockTransport{
		roundTripFn: func(_ context.Context, req jsonrpcRequest) (*jsonrpcResponse, error) {
			if req.Method != "tools/call" {
				t.Errorf("method = %s, want tools/call", req.Method)
			}
			return &jsonrpcResponse{
				JSONRPC: jsonrpcVersion,
				ID:      req.ID,
				Result:  json.RawMessage(`{"content":[{"type":"text","text":"result data"}],"is_error":false}`),
			}, nil
		},
	}
	c := &Client{name: "test", transport: mock, idCounter: 1}
	result, err := c.CallTool(context.Background(), "web_search", map[string]any{"query": "test"})
	if err != nil {
		t.Fatalf("CallTool error: %v", err)
	}
	if result.IsError {
		t.Error("expected success, got error")
	}
	if len(result.Content) != 1 || result.Content[0].Text != "result data" {
		t.Errorf("unexpected content: %+v", result.Content)
	}
}

func TestClient_CallTool_ErrorMessage(t *testing.T) {
	mock := &mockTransport{
		roundTripFn: func(_ context.Context, req jsonrpcRequest) (*jsonrpcResponse, error) {
			return &jsonrpcResponse{
				JSONRPC: jsonrpcVersion,
				ID:      req.ID,
				Error:   &jsonrpcError{Code: -32603, Message: "Internal error"},
			}, nil
		},
	}
	c := &Client{name: "test", transport: mock, idCounter: 1}
	result, err := c.CallTool(context.Background(), "fail", nil)
	if err == nil {
		t.Fatal("expected JSON-RPC error")
	}
	if result == nil || !result.IsError {
		t.Error("expected IsError=true in result")
	}
}

func TestClient_CallTool_TransportError(t *testing.T) {
	mock := &mockTransport{
		roundTripFn: func(_ context.Context, req jsonrpcRequest) (*jsonrpcResponse, error) {
			return nil, errors.New("timeout")
		},
	}
	c := &Client{name: "test", transport: mock, idCounter: 1}
	_, err := c.CallTool(context.Background(), "web_search", nil)
	if err == nil {
		t.Fatal("expected transport error")
	}
}

func TestClient_Close(t *testing.T) {
	mock := &mockTransport{}
	c := &Client{name: "test", transport: mock}
	if err := c.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}
	if !mock.closeCalled {
		t.Error("transport.close() was not called")
	}
}

func TestClient_CallTool_BadResult(t *testing.T) {
	mock := &mockTransport{
		roundTripFn: func(_ context.Context, req jsonrpcRequest) (*jsonrpcResponse, error) {
			return &jsonrpcResponse{
				JSONRPC: jsonrpcVersion,
				ID:      req.ID,
				Result:  json.RawMessage(`not valid json`),
			}, nil
		},
	}
	c := &Client{name: "test", transport: mock, idCounter: 1}
	_, err := c.CallTool(context.Background(), "web_search", nil)
	if err == nil {
		t.Fatal("expected error for bad result JSON")
	}
}

func TestClient_InitializeRequest(t *testing.T) {
	var requests []jsonrpcRequest
	mock := &mockTransport{
		roundTripFn: func(_ context.Context, req jsonrpcRequest) (*jsonrpcResponse, error) {
			requests = append(requests, req)
			return &jsonrpcResponse{
				JSONRPC: jsonrpcVersion,
				ID:      req.ID,
				Result:  json.RawMessage(`{}`),
			}, nil
		},
	}
	c := &Client{name: "test", transport: mock, idCounter: 1}
	if err := c.initialize(context.Background()); err != nil {
		t.Fatalf("initialize error: %v", err)
	}

	if len(requests) < 2 {
		t.Fatalf("expected 2 requests, got %d", len(requests))
	}

	// First request should be "initialize".
	initReq := requests[0]
	if initReq.Method != "initialize" {
		t.Errorf("method = %s, want initialize", initReq.Method)
	}
	if initReq.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %s, want 2.0", initReq.JSONRPC)
	}
	params, ok := initReq.Params.(map[string]any)
	if !ok {
		t.Fatalf("params type = %T", initReq.Params)
	}
	if params["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocolVersion = %v", params["protocolVersion"])
	}
	clientInfo, ok := params["clientInfo"].(map[string]any)
	if !ok {
		t.Fatalf("clientInfo type = %T", params["clientInfo"])
	}
	if clientInfo["name"] != "ares-mcp-client" {
		t.Errorf("clientInfo.name = %v", clientInfo["name"])
	}

	// Second request should be the initialized notification.
	notifReq := requests[1]
	if notifReq.Method != "notifications/initialized" {
		t.Errorf("second method = %s, want notifications/initialized", notifReq.Method)
	}
}

func TestClient_InitializeError(t *testing.T) {
	mock := &mockTransport{
		roundTripFn: func(_ context.Context, req jsonrpcRequest) (*jsonrpcResponse, error) {
			return &jsonrpcResponse{
				JSONRPC: jsonrpcVersion,
				ID:      req.ID,
				Error:   &jsonrpcError{Code: -32000, Message: "Unsupported version"},
			}, nil
		},
	}
	c := &Client{name: "test", transport: mock, idCounter: 1}
	err := c.initialize(context.Background())
	if err == nil {
		t.Fatal("expected initialize error")
	}
}

func TestClient_SendNotification(t *testing.T) {
	var capturedReq jsonrpcRequest
	mock := &mockTransport{
		roundTripFn: func(_ context.Context, req jsonrpcRequest) (*jsonrpcResponse, error) {
			capturedReq = req
			return &jsonrpcResponse{}, nil
		},
	}
	c := &Client{name: "test", transport: mock}
	err := c.sendNotification(context.Background(), jsonrpcNotification{
		JSONRPC: jsonrpcVersion,
		Method:  "notifications/initialized",
	})
	if err != nil {
		t.Fatalf("sendNotification error: %v", err)
	}
	if capturedReq.Method != "notifications/initialized" {
		t.Errorf("method = %s", capturedReq.Method)
	}
	if capturedReq.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %s", capturedReq.JSONRPC)
	}
}

func TestDiscoverServers_NoHomeDir(t *testing.T) {
	// Temporarily override HOME to a non-existent directory.
	t.Setenv("HOME", "/nonexistent/home")

	servers := DiscoverServers("")
	if len(servers) != 0 {
		t.Errorf("got %d servers, want 0", len(servers))
	}
}

func TestDiscoverServers_ProjectDirOnly(t *testing.T) {
	// Temporarily override HOME to a non-existent directory to avoid noise.
	t.Setenv("HOME", "/nonexistent/home")

	dir := t.TempDir()
	settingsDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(settingsDir, 0750); err != nil {
		t.Fatal(err)
	}
	config := `{
		"mcpServers": {
			"web-search": {
				"command": "uvx",
				"args": ["web-search-mcp"]
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}

	servers := DiscoverServers(dir)
	if len(servers) != 1 {
		t.Fatalf("got %d servers, want 1", len(servers))
	}
	if servers[0].Name != "web-search" {
		t.Errorf("name = %s, want web-search", servers[0].Name)
	}
	if servers[0].Command != "uvx" {
		t.Errorf("command = %s, want uvx", servers[0].Command)
	}
	if len(servers[0].Args) != 1 || servers[0].Args[0] != "web-search-mcp" {
		t.Errorf("args = %v", servers[0].Args)
	}
}

func TestDiscoverServers_GlobalAndProject(t *testing.T) {
	dir := t.TempDir()
	homeDir := filepath.Join(dir, "home")
	projectDir := filepath.Join(dir, "project")
	if err := os.MkdirAll(homeDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}

	// Global config (~/.claude.json)
	globalCfg := `{
		"mcpServers": {
			"codegraph": {
				"command": "npx",
				"args": ["codegraph"]
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(homeDir, ".claude.json"), []byte(globalCfg), 0600); err != nil {
		t.Fatal(err)
	}

	// Project config (.claude/settings.json) — should be merged
	projectCfg := `{
		"mcpServers": {
			"web-search": {
				"command": "uvx",
				"args": ["web-search-mcp"]
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(projectDir, ".claude/settings.json"), []byte(projectCfg), 0600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", homeDir)

	servers := DiscoverServers(projectDir)
	if len(servers) != 2 {
		t.Fatalf("got %d servers, want 2", len(servers))
	}
	names := make(map[string]bool)
	for _, s := range servers {
		names[s.Name] = true
	}
	if !names["codegraph"] {
		t.Error("missing codegraph server")
	}
	if !names["web-search"] {
		t.Error("missing web-search server")
	}
}

func TestDiscoverServers_Deduplication(t *testing.T) {
	// Project server with same name as global — global should win (scanned second).
	dir := t.TempDir()
	homeDir := filepath.Join(dir, "home")
	projectDir := filepath.Join(dir, "project")
	if err := os.MkdirAll(homeDir, 0750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, ".claude"), 0755); err != nil {
		t.Fatal(err)
	}

	globalCfg := `{
		"mcpServers": {
			"my-tool": {
				"command": "node",
				"args": ["global.js"]
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(homeDir, ".claude.json"), []byte(globalCfg), 0600); err != nil {
		t.Fatal(err)
	}

	projectCfg := `{
		"mcpServers": {
			"my-tool": {
				"command": "python",
				"args": ["project.py"]
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(projectDir, ".claude/settings.json"), []byte(projectCfg), 0600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", homeDir)

	servers := DiscoverServers(projectDir)
	if len(servers) != 1 {
		t.Fatalf("got %d servers, want 1 (deduplicated)", len(servers))
	}
	// Global (first scanned) wins because seen map blocks the project duplicate.
	if servers[0].Command != "node" {
		t.Errorf("expected global command 'node', got %s", servers[0].Command)
	}
}

func TestScanClaudeConfig_MissingFile(t *testing.T) {
	servers := scanClaudeConfig("/nonexistent/path.json", make(map[string]bool))
	if servers != nil {
		t.Errorf("expected nil, got %v", servers)
	}
}

func TestScanClaudeConfig_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte(`not json`), 0600); err != nil {
		t.Fatal(err)
	}
	servers := scanClaudeConfig(path, make(map[string]bool))
	if servers != nil {
		t.Errorf("expected nil, got %v", servers)
	}
}

func TestScanClaudeConfig_DeduplicatesViaSeen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dup.json")
	cfg := `{
		"mcpServers": {
			"dupe": {"command": "cmd", "args": ["a"]}
		}
	}`
	if err := os.WriteFile(path, []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}

	seen := map[string]bool{"dupe": true}
	servers := scanClaudeConfig(path, seen)
	if len(servers) != 0 {
		t.Errorf("expected 0 servers (already seen), got %d", len(servers))
	}
}

func TestContentBlockJSON(t *testing.T) {
	tests := []struct {
		name string
		cb   ContentBlock
	}{
		{name: "text", cb: ContentBlock{Type: "text", Text: "hello"}},
		{name: "resource", cb: ContentBlock{Type: "resource", Text: "data", MimeType: "text/plain"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.cb)
			if err != nil {
				t.Fatal(err)
			}
			var decoded ContentBlock
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Type != tc.cb.Type || decoded.Text != tc.cb.Text || decoded.MimeType != tc.cb.MimeType {
				t.Errorf("roundtrip mismatch: %+v", decoded)
			}
		})
	}
}

func TestTransportInterfaceContract(t *testing.T) {
	// Verify that both stdioTransport and sseTransport implement the transport interface.
	var tr transport
	tr = &stdioTransport{}
	_ = tr
	tr = &sseTransport{}
	_ = tr
	_ = tr // ensure both types compile as transports
}

func TestClient_MethodCallsUseNextID(t *testing.T) {
	var ids []int
	mock := &mockTransport{
		roundTripFn: func(_ context.Context, req jsonrpcRequest) (*jsonrpcResponse, error) {
			ids = append(ids, req.ID)
			return &jsonrpcResponse{
				JSONRPC: jsonrpcVersion,
				ID:      req.ID,
				Result:  json.RawMessage(`{"tools":[]}`),
			}, nil
		},
	}
	c := &Client{name: "test", transport: mock, idCounter: 1}

	if _, err := c.ListTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CallTool(context.Background(), "x", nil); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 IDs, got %d", len(ids))
	}
	if ids[0] != 1 || ids[1] != 2 {
		t.Errorf("expected IDs [1,2], got %v", ids)
	}
}
