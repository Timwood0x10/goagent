package ares_mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

// TestNewMCPServer verifies server creation and initial state.
func TestNewMCPServer(t *testing.T) {
	info := Implementation{Name: "test-server", Version: "1.0.0"}
	transport := newPipeServerTransport()
	server := NewMCPServer(info, transport)

	assert.NotNil(t, server)
	assert.Equal(t, "test-server", server.info.Name)
	assert.Equal(t, "1.0.0", server.info.Version)
	assert.Equal(t, 0, server.ToolCount())
	assert.Equal(t, 0, server.ResourceCount())
	assert.Equal(t, 0, server.PromptCount())
}

// TestRegisterTool verifies tool registration and duplicate detection.
func TestRegisterTool(t *testing.T) {
	transport := newPipeServerTransport()
	server := NewMCPServer(Implementation{Name: "test", Version: "1.0.0"}, transport)

	schema := json.RawMessage(`{"type": "object"}`)
	handler := func(ctx context.Context, args map[string]any) (*ToolCallResult, error) {
		return &ToolCallResult{
			Content: []ContentBlock{{Type: "text", Text: "ok"}},
		}, nil
	}

	// Successful registration.
	err := server.RegisterTool("test_tool", "A test tool", schema, handler)
	require.NoError(t, err)
	assert.Equal(t, 1, server.ToolCount())

	// Duplicate registration should fail.
	err = server.RegisterTool("test_tool", "Duplicate", schema, handler)
	assert.ErrorIs(t, err, ErrDuplicateRegistration)

	// Empty name should fail.
	err = server.RegisterTool("", "Empty", schema, handler)
	assert.ErrorIs(t, err, ErrEmptyName)

	// Nil handler should fail.
	err = server.RegisterTool("nil_handler", "Nil", schema, nil)
	assert.Error(t, err)
}

// TestRegisterResource verifies resource registration.
func TestRegisterResource(t *testing.T) {
	transport := newPipeServerTransport()
	server := NewMCPServer(Implementation{Name: "test", Version: "1.0.0"}, transport)

	handler := func(ctx context.Context, uri string) (*ReadResourceResult, error) {
		return &ReadResourceResult{
			Contents: []ResourceContent{
				{URI: uri, MimeType: "text/plain", Text: "hello"},
			},
		}, nil
	}

	// Successful registration.
	err := server.RegisterResource("test://resource", "Test resource", "text/plain", handler)
	require.NoError(t, err)
	assert.Equal(t, 1, server.ResourceCount())

	// Duplicate registration should fail.
	err = server.RegisterResource("test://resource", "Duplicate", "text/plain", handler)
	assert.ErrorIs(t, err, ErrDuplicateRegistration)

	// Empty URI should fail.
	err = server.RegisterResource("", "Empty", "text/plain", handler)
	assert.Error(t, err)
}

// TestResourceTemplate verifies resource template registration.
func TestResourceTemplate(t *testing.T) {
	transport := newPipeServerTransport()
	server := NewMCPServer(Implementation{Name: "test", Version: "1.0.0"}, transport)

	handler := func(ctx context.Context, uri string) (*ReadResourceResult, error) {
		return nil, errors.New("not found")
	}

	err := server.ResourceTemplate("weather://{city}", "Weather data", "application/json", handler)
	require.NoError(t, err)
	assert.Equal(t, 1, len(server.resourceTemplates))

	// Duplicate template should fail.
	err = server.ResourceTemplate("weather://{city}", "Duplicate", "application/json", handler)
	assert.ErrorIs(t, err, ErrDuplicateRegistration)
}

// TestRegisterPrompt verifies prompt registration.
func TestRegisterPrompt(t *testing.T) {
	transport := newPipeServerTransport()
	server := NewMCPServer(Implementation{Name: "test", Version: "1.0.0"}, transport)

	handler := func(ctx context.Context, args map[string]string) (*GetPromptResult, error) {
		return &GetPromptResult{
			Messages: []PromptMessage{{Role: "user", Content: "hello"}},
		}, nil
	}

	args := []PromptArgument{
		{Name: "topic", Description: "Topic to summarize", Required: true},
	}

	// Successful registration.
	err := server.RegisterPrompt("summarize", "Summarize a topic", args, handler)
	require.NoError(t, err)
	assert.Equal(t, 1, server.PromptCount())

	// Duplicate registration should fail.
	err = server.RegisterPrompt("summarize", "Duplicate", args, handler)
	assert.ErrorIs(t, err, ErrDuplicateRegistration)

	// Empty name should fail.
	err = server.RegisterPrompt("", "Empty", args, handler)
	assert.Error(t, err)
}

// TestHandleInitialize verifies initialize response format.
func TestHandleInitialize(t *testing.T) {
	transport := newPipeServerTransport()
	server := NewMCPServer(
		Implementation{Name: "init-test", Version: "2.0.0"},
		transport,
	)

	msg, _ := NewRequest(1, MethodInitialize, InitializeParams{
		ProtocolVersion: ProtocolVersion,
		ClientInfo:      Implementation{Name: "client", Version: "1.0.0"},
	})

	resp := server.handleInitialize(msg)
	require.NotNil(t, resp)
	assert.Nil(t, resp.Error)

	var result InitializeResult
	err := DecodeResult(resp, &result)
	require.NoError(t, err)
	assert.Equal(t, ProtocolVersion, result.ProtocolVersion)
	assert.Equal(t, "init-test", result.ServerInfo.Name)
	assert.Equal(t, "2.0.0", result.ServerInfo.Version)
}

// TestHandleToolsList verifies tools list response includes registered tools.
func TestHandleToolsList(t *testing.T) {
	transport := newPipeServerTransport()
	server := NewMCPServer(Implementation{Name: "test", Version: "1.0.0"}, transport)

	schema := json.RawMessage(`{"type": "object"}`)
	handler := func(ctx context.Context, args map[string]any) (*ToolCallResult, error) {
		return &ToolCallResult{Content: []ContentBlock{}}, nil
	}

	_ = server.RegisterTool("tool1", "First tool", schema, handler)
	_ = server.RegisterTool("tool2", "Second tool", schema, handler)

	msg, _ := NewRequest(2, MethodToolsList, nil)
	resp := server.handleToolsList(msg)
	require.NotNil(t, resp)
	assert.Nil(t, resp.Error)

	var result ToolsListResult
	err := DecodeResult(resp, &result)
	require.NoError(t, err)
	assert.Len(t, result.Tools, 2)

	names := make(map[string]bool)
	for _, t := range result.Tools {
		names[t.Name] = true
	}
	assert.True(t, names["tool1"])
	assert.True(t, names["tool2"])
}

// TestHandleToolsCall verifies tool dispatch and execution.
func TestHandleToolsCall(t *testing.T) {
	transport := newPipeServerTransport()
	server := NewMCPServer(Implementation{Name: "test", Version: "1.0.0"}, transport)

	schema := json.RawMessage(`{"type": "object"}`)
	handler := func(ctx context.Context, args map[string]any) (*ToolCallResult, error) {
		msg, _ := args["message"].(string)
		return &ToolCallResult{
			Content: []ContentBlock{{Type: "text", Text: "echo: " + msg}},
		}, nil
	}

	_ = server.RegisterTool("echo", "Echo tool", schema, handler)

	params := ToolCallParams{
		Name:      "echo",
		Arguments: map[string]any{"message": "hello"},
	}
	msg, _ := NewRequest(3, MethodToolsCall, params)
	resp := server.handleToolsCall(msg)
	require.NotNil(t, resp)
	assert.Nil(t, resp.Error)

	var result ToolCallResult
	err := DecodeResult(resp, &result)
	require.NoError(t, err)
	assert.Len(t, result.Content, 1)
	assert.Equal(t, "echo: hello", result.Content[0].Text)
}

// TestHandleToolsCallUnknownTool verifies error for unknown tools.
func TestHandleToolsCallUnknownTool(t *testing.T) {
	transport := newPipeServerTransport()
	server := NewMCPServer(Implementation{Name: "test", Version: "1.0.0"}, transport)

	params := ToolCallParams{
		Name:      "nonexistent",
		Arguments: map[string]any{},
	}
	msg, _ := NewRequest(4, MethodToolsCall, params)
	resp := server.handleToolsCall(msg)
	require.NotNil(t, resp)
	assert.NotNil(t, resp.Error)
	assert.Equal(t, MethodNotFound, resp.Error.Code)
}

// TestHandlePing verifies ping returns empty result.
func TestHandlePing(t *testing.T) {
	transport := newPipeServerTransport()
	server := NewMCPServer(Implementation{Name: "test", Version: "1.0.0"}, transport)

	msg, _ := NewRequest(5, MethodPing, nil)
	resp := server.handlePing(msg)
	require.NotNil(t, resp)
	assert.Nil(t, resp.Error)

	var result PingResult
	err := DecodeResult(resp, &result)
	require.NoError(t, err)
}

// TestHandleUnknownMethod verifies MethodNotFound for unknown methods.
func TestHandleUnknownMethod(t *testing.T) {
	transport := newPipeServerTransport()
	server := NewMCPServer(Implementation{Name: "test", Version: "1.0.0"}, transport)

	msg, _ := NewRequest(6, "unknown/method", nil)
	resp := server.handleRequest(msg)
	require.NotNil(t, resp)
	assert.NotNil(t, resp.Error)
	assert.Equal(t, MethodNotFound, resp.Error.Code)
}

// TestHandleResourcesList verifies resources list response.
func TestHandleResourcesList(t *testing.T) {
	transport := newPipeServerTransport()
	server := NewMCPServer(Implementation{Name: "test", Version: "1.0.0"}, transport)

	handler := func(ctx context.Context, uri string) (*ReadResourceResult, error) {
		return nil, errors.New("not found")
	}
	_ = server.RegisterResource("test://res", "Test Resource", "text/plain", handler)

	msg, _ := NewRequest(7, MethodResourcesList, nil)
	resp := server.handleResourcesList(msg)
	require.NotNil(t, resp)
	assert.Nil(t, resp.Error)

	var result ListResourcesResult
	err := DecodeResult(resp, &result)
	require.NoError(t, err)
	assert.Len(t, result.Resources, 1)
	assert.Equal(t, "test://res", result.Resources[0].URI)
}

// TestHandleResourcesRead verifies resource read dispatch.
func TestHandleResourcesRead(t *testing.T) {
	transport := newPipeServerTransport()
	server := NewMCPServer(Implementation{Name: "test", Version: "1.0.0"}, transport)

	handler := func(ctx context.Context, uri string) (*ReadResourceResult, error) {
		return &ReadResourceResult{
			Contents: []ResourceContent{
				{URI: uri, Text: "resource content"},
			},
		}, nil
	}
	_ = server.RegisterResource("test://data", "Data resource", "text/plain", handler)

	type readParams struct {
		URI string `json:"uri"`
	}
	params := readParams{URI: "test://data"}
	msg, _ := NewRequest(8, MethodResourcesRead, params)
	resp := server.handleResourcesRead(msg)
	require.NotNil(t, resp)
	assert.Nil(t, resp.Error)

	var result ReadResourceResult
	err := DecodeResult(resp, &result)
	require.NoError(t, err)
	assert.Len(t, result.Contents, 1)
	assert.Equal(t, "resource content", result.Contents[0].Text)
}

// TestHandlePromptsList verifies prompts list response.
func TestHandlePromptsList(t *testing.T) {
	transport := newPipeServerTransport()
	server := NewMCPServer(Implementation{Name: "test", Version: "1.0.0"}, transport)

	handler := func(ctx context.Context, args map[string]string) (*GetPromptResult, error) {
		return &GetPromptResult{Messages: []PromptMessage{}}, nil
	}
	_ = server.RegisterPrompt("summarize", "Summarize topic", nil, handler)

	msg, _ := NewRequest(9, MethodPromptsList, nil)
	resp := server.handlePromptsList(msg)
	require.NotNil(t, resp)
	assert.Nil(t, resp.Error)

	var result ListPromptsResult
	err := DecodeResult(resp, &result)
	require.NoError(t, err)
	assert.Len(t, result.Prompts, 1)
	assert.Equal(t, "summarize", result.Prompts[0].Name)
}

// TestHandlePromptsGet verifies prompt get dispatch.
func TestHandlePromptsGet(t *testing.T) {
	transport := newPipeServerTransport()
	server := NewMCPServer(Implementation{Name: "test", Version: "1.0.0"}, transport)

	handler := func(ctx context.Context, args map[string]string) (*GetPromptResult, error) {
		topic := args["topic"]
		return &GetPromptResult{
			Description: "Summary of " + topic,
			Messages: []PromptMessage{
				{Role: "user", Content: "Summarize: " + topic},
			},
		}, nil
	}
	_ = server.RegisterPrompt("summarize", "Summarize",
		[]PromptArgument{{Name: "topic", Required: true}}, handler)

	type getPromptParams struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments,omitempty"`
	}
	params := getPromptParams{
		Name:      "summarize",
		Arguments: map[string]string{"topic": "AI"},
	}
	msg, _ := NewRequest(10, MethodPromptsGet, params)
	resp := server.handlePromptsGet(msg)
	require.NotNil(t, resp)
	assert.Nil(t, resp.Error)

	var result GetPromptResult
	err := DecodeResult(resp, &result)
	require.NoError(t, err)
	assert.Equal(t, "Summary of AI", result.Description)
	assert.Len(t, result.Messages, 1)
	assert.Contains(t, result.Messages[0].Content, "AI")
}

// TestServeWithTransport verifies the full serve loop via pipe transport.
func TestServeWithTransport(t *testing.T) {
	pipe := newPipeServerTransport()
	server := NewMCPServer(Implementation{Name: "pipe-test", Version: "1.0.0"}, pipe)

	schema := json.RawMessage(`{"type": "object"}`)
	handler := func(ctx context.Context, args map[string]any) (*ToolCallResult, error) {
		return &ToolCallResult{
			Content: []ContentBlock{{Type: "text", Text: "pong"}},
		}, nil
	}
	_ = server.RegisterTool("ping_tool", "Ping tool", schema, handler)

	ctx, cancel := context.WithCancel(context.Background())

	// Start server in background (use errgroup as required).
	eg, serveCtx := errgroup.WithContext(ctx)
	eg.Go(func() error { return server.Serve(serveCtx) })

	// Give server time to start.
	time.Sleep(10 * time.Millisecond)

	// Send initialize request.
	initMsg, _ := NewRequest(1, MethodInitialize, InitializeParams{
		ProtocolVersion: ProtocolVersion,
		ClientInfo:      Implementation{Name: "test-client", Version: "1.0.0"},
	})
	pipe.requestCh <- initMsg

	// Read response from response channel.
	select {
	case resp := <-pipe.responseCh:
		require.NotNil(t, resp)
		assert.Nil(t, resp.Error)
		var result InitializeResult
		err := DecodeResult(resp, &result)
		require.NoError(t, err)
		assert.Equal(t, "pipe-test", result.ServerInfo.Name)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for initialize response")
	}

	// Send ping request.
	pingMsg, _ := NewRequest(2, MethodPing, nil)
	pipe.requestCh <- pingMsg

	select {
	case resp := <-pipe.responseCh:
		require.NotNil(t, resp)
		assert.Nil(t, resp.Error)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for ping response")
	}

	// Shutdown.
	cancel()

	// Wait for server to stop with timeout.
	done := make(chan struct{})
	go func() {
		_ = eg.Wait()
		close(done)
	}()
	select {
	case <-done:
		// Server stopped gracefully.
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for server shutdown")
	}
}

// TestServeNilTransport verifies error when transport is nil.
func TestServeNilTransport(t *testing.T) {
	server := NewMCPServer(Implementation{Name: "test", Version: "1.0.0"}, nil)
	err := server.Serve(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "transport is required")
}

// TestNotificationHandling verifies notifications don't produce responses.
func TestNotificationHandling(t *testing.T) {
	transport := newPipeServerTransport()
	server := NewMCPServer(Implementation{Name: "test", Version: "1.0.0"}, transport)

	notif, _ := NewNotification(NotificationInitialized, nil)
	resp := server.dispatch(notif)
	assert.Nil(t, resp, "notifications should not produce responses")
}

// TestMatchURITemplate verifies URI template matching logic.
func TestMatchURITemplate(t *testing.T) {
	tests := []struct {
		name     string
		uri      string
		template string
		want     bool
	}{
		{
			name:     "simple placeholder match",
			uri:      "file:///path/to/file.txt",
			template: "file://{path}",
			want:     true,
		},
		{
			name:     "weather city template",
			uri:      "weather://London",
			template: "weather://{city}",
			want:     true,
		},
		{
			name:     "multi-segment template",
			uri:      "db://users/123",
			template: "db://{collection}/{id}",
			want:     true,
		},
		{
			name:     "no placeholder exact match",
			uri:      "config://settings",
			template: "config://settings",
			want:     true,
		},
		{
			name:     "segment count mismatch with no greedy placeholder",
			uri:      "file:///path/to/file.txt",
			template: "file://{a}/fixed",
			want:     false,
		},
		{
			name:     "literal segment mismatch",
			uri:      "weather://London",
			template: "stock://{symbol}",
			want:     false,
		},
		{
			name:     "empty uri and template",
			uri:      "",
			template: "",
			want:     true,
		},
		{
			name:     "scheme mismatch with placeholder",
			uri:      "ftp://server/file",
			template: "http://{host}/{path}",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchURITemplate(tt.uri, tt.template)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestHandleResourcesReadWithTemplate verifies resource read dispatch via URI templates.
func TestHandleResourcesReadWithTemplate(t *testing.T) {
	transport := newPipeServerTransport()
	server := NewMCPServer(Implementation{Name: "test", Version: "1.0.0"}, transport)

	calledWith := ""
	handler := func(ctx context.Context, uri string) (*ReadResourceResult, error) {
		calledWith = uri
		return &ReadResourceResult{
			Contents: []ResourceContent{
				{URI: uri, Text: "template content for " + uri},
			},
		}, nil
	}

	_ = server.ResourceTemplate("file://{path}", "File Access", "text/plain", handler)

	type readParams struct {
		URI string `json:"uri"`
	}

	// Match via template.
	params := readParams{URI: "file:///etc/hostname"}
	msg, _ := NewRequest(100, MethodResourcesRead, params)
	resp := server.handleResourcesRead(msg)
	require.NotNil(t, resp)
	assert.Nil(t, resp.Error)

	var result ReadResourceResult
	err := DecodeResult(resp, &result)
	require.NoError(t, err)
	assert.Len(t, result.Contents, 1)
	assert.Contains(t, result.Contents[0].Text, "file:///etc/hostname")
	assert.Equal(t, "file:///etc/hostname", calledWith)

	// Non-matching URI should return error.
	params2 := readParams{URI: "other://something"}
	msg2, _ := NewRequest(101, MethodResourcesRead, params2)
	resp2 := server.handleResourcesRead(msg2)
	require.NotNil(t, resp2)
	assert.NotNil(t, resp2.Error)
	assert.Equal(t, MethodNotFound, resp2.Error.Code)
}

// TestHandleResourcesReadExactMatchTakesPriority verifies that exact resource
// matches take priority over template matches for the same URI.
func TestHandleResourcesReadExactMatchTakesPriority(t *testing.T) {
	transport := newPipeServerTransport()
	server := NewMCPServer(Implementation{Name: "test", Version: "1.0.0"}, transport)

	exactHandler := func(ctx context.Context, uri string) (*ReadResourceResult, error) {
		return &ReadResourceResult{
			Contents: []ResourceContent{{URI: uri, Text: "exact match"}},
		}, nil
	}
	templateHandler := func(ctx context.Context, uri string) (*ReadResourceResult, error) {
		return &ReadResourceResult{
			Contents: []ResourceContent{{URI: uri, Text: "template match"}},
		}, nil
	}

	_ = server.RegisterResource("config://settings", "Settings", "text/plain", exactHandler)
	_ = server.ResourceTemplate("config://{path}", "Config template", "text/plain", templateHandler)

	type readParams struct {
		URI string `json:"uri"`
	}
	params := readParams{URI: "config://settings"}
	msg, _ := NewRequest(102, MethodResourcesRead, params)
	resp := server.handleResourcesRead(msg)
	require.NotNil(t, resp)
	assert.Nil(t, resp.Error)

	var result ReadResourceResult
	err := DecodeResult(resp, &result)
	require.NoError(t, err)
	assert.Equal(t, "exact match", result.Contents[0].Text)
}
