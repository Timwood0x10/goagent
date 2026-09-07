// Package mcp is the official MCP (Model Context Protocol) adapter for ARES.
//
// It exposes ARES agents/tools over the MCP wire format so MCP-compatible
// clients (Claude Desktop, IDE plugins, …) can call into ARES.
//
// The adapter binds internal/ares_mcp.MCPClient's public surface (ListTools,
// CallTool, ServerCapabilities) to the compat/protocol.ProtocolAdapter
// interface. Each Serve call decodes one inbound JSON-RPC 2.0 message,
// dispatches it through a thin JSON-RPC layer (initialize/tools/list/
// tools/call/ping), and returns the encoded response. Notifications (no
// `id`) return nil.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Timwood0x10/ares/compat/protocol"
	aresmcp "github.com/Timwood0x10/ares/internal/runtime/protocol/mcp"
)

// Adapter satisfies compat/protocol.ProtocolAdapter for MCP.
//
// It wraps an ares_mcp.MCPClient that has been pre-connected to a remote
// MCP server by the bootstrap layer. The adapter does NOT own the client's
// lifecycle — bootstrap is responsible for Connect/Close on the underlying
// client. Serve is a synchronous per-request dispatch.
type Adapter struct {
	client *aresmcp.MCPClient
}

// New constructs an Adapter from a raw config map.
//
// Recognized keys:
//
//	client *aresmcp.MCPClient — REQUIRED. A pre-connected MCPClient.
func New(config map[string]any) (*Adapter, error) {
	client, _ := config["client"].(*aresmcp.MCPClient)
	if client == nil {
		return nil, fmt.Errorf("compat/protocol/mcp: client is required")
	}
	return &Adapter{client: client}, nil
}

// Serve handles a single inbound MCP JSON-RPC request and returns the encoded response.
//
// raw is a JSON-RPC 2.0 message (request or notification). Supported methods:
//   - initialize       — returns server info + capabilities
//   - tools/list        — returns the list of tools discovered on the remote server
//   - tools/call        — calls a named tool with the given arguments
//   - ping              — health probe
//
// For notifications nil is returned with no error. Malformed JSON returns an
// error rather than a JSON-RPC error envelope.
func (a *Adapter) Serve(ctx context.Context, raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("compat/protocol/mcp: empty request")
	}
	var msg aresmcp.JSONRPCMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil, fmt.Errorf("compat/protocol/mcp: decode: %w", err)
	}

	// Notifications (no id) are accepted but produce no response.
	if !aresmcp.IsRequest(&msg) {
		return nil, nil
	}

	resp := a.dispatch(ctx, &msg)
	if resp == nil {
		return nil, nil
	}
	out, err := json.Marshal(resp)
	if err != nil {
		return nil, fmt.Errorf("compat/protocol/mcp: encode: %w", err)
	}
	return out, nil
}

// dispatch routes a JSON-RPC request to the appropriate handler, returning
// a JSON-RPC response. Unknown methods yield a method-not-found error envelope.
func (a *Adapter) dispatch(ctx context.Context, msg *aresmcp.JSONRPCMessage) *aresmcp.JSONRPCMessage {
	id := *msg.ID
	switch msg.Method {
	case aresmcp.MethodInitialize:
		return a.handleInitialize(id)
	case aresmcp.MethodToolsList:
		return a.handleToolsList(ctx, id)
	case aresmcp.MethodToolsCall:
		return a.handleToolsCall(ctx, id, msg.Params)
	case aresmcp.MethodPing:
		return a.handlePing(id)
	default:
		resp, _ := aresmcp.NewErrorResponse(id, aresmcp.MethodNotFound,
			fmt.Sprintf("method not found: %s", msg.Method), nil)
		return resp
	}
}

// handleInitialize returns the server implementation info and capabilities.
func (a *Adapter) handleInitialize(id int64) *aresmcp.JSONRPCMessage {
	result := struct {
		ProtocolVersion string                      `json:"protocolVersion"`
		Capabilities    *aresmcp.ServerCapabilities `json:"capabilities"`
	}{
		ProtocolVersion: aresmcp.ProtocolVersion,
		Capabilities:    a.client.ServerCapabilities(),
	}
	resp, err := aresmcp.NewResponse(id, result)
	if err != nil {
		resp, _ = aresmcp.NewErrorResponse(id, aresmcp.InternalError, "encode initialize result", nil)
	}
	return resp
}

// handleToolsList returns the list of tools discovered on the remote MCP server.
func (a *Adapter) handleToolsList(ctx context.Context, id int64) *aresmcp.JSONRPCMessage {
	tools, err := a.client.ListTools(ctx)
	if err != nil {
		resp, _ := aresmcp.NewErrorResponse(id, aresmcp.InternalError,
			fmt.Sprintf("list tools: %v", err), nil)
		return resp
	}
	result := struct {
		Tools []aresmcp.MCPToolDef `json:"tools"`
	}{Tools: tools}
	resp, err := aresmcp.NewResponse(id, result)
	if err != nil {
		resp, _ = aresmcp.NewErrorResponse(id, aresmcp.InternalError, "encode tools list", nil)
	}
	return resp
}

// handleToolsCall dispatches a tools/call request to the named remote tool.
func (a *Adapter) handleToolsCall(ctx context.Context, id int64, params json.RawMessage) *aresmcp.JSONRPCMessage {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	// Some MCP clients omit `params` for argument-less tools; treat an absent
	// or null params as an empty object instead of InvalidParams.
	if len(params) == 0 {
		params = []byte("{}")
	}
	if err := json.Unmarshal(params, &p); err != nil {
		resp, _ := aresmcp.NewErrorResponse(id, aresmcp.InvalidParams,
			fmt.Sprintf("invalid params: %v", err), nil)
		return resp
	}
	if p.Arguments == nil {
		p.Arguments = map[string]any{}
	}
	result, err := a.client.CallTool(ctx, p.Name, p.Arguments)
	if err != nil {
		resp, _ := aresmcp.NewErrorResponse(id, aresmcp.InternalError,
			fmt.Sprintf("call tool %s: %v", p.Name, err), nil)
		return resp
	}
	resp, err := aresmcp.NewResponse(id, result)
	if err != nil {
		resp, _ = aresmcp.NewErrorResponse(id, aresmcp.InternalError, "encode tool result", nil)
	}
	return resp
}

// handlePing responds to a ping health probe with an empty result.
func (a *Adapter) handlePing(id int64) *aresmcp.JSONRPCMessage {
	resp, err := aresmcp.NewResponse(id, struct{}{})
	if err != nil {
		resp, _ = aresmcp.NewErrorResponse(id, aresmcp.InternalError, "encode ping", nil)
	}
	return resp
}

// Name returns the canonical protocol name.
func (*Adapter) Name() string { return "mcp" }

// ContentType returns the MIME type this adapter produces.
func (*Adapter) ContentType() string { return "application/json" }

// Compile-time interface assertion.
var _ protocol.ProtocolAdapter = (*Adapter)(nil)
