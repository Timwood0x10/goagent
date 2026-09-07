// Package mcp provides MCP (Model Context Protocol) server implementation for ares.
package ares_mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// --- Handler types ---

// ToolHandler handles a tool invocation request from an MCP client.
//
// Args:
//   - ctx: context for cancellation and timeout
//   - args: tool arguments as a map
//
// Returns:
//   - *ToolCallResult: the tool execution result
//   - error: non-nil if the tool fails
type ToolHandler func(ctx context.Context, args map[string]any) (*ToolCallResult, error)

// ResourceHandler handles a resource read request from an MCP client.
//
// Args:
//   - ctx: context for cancellation and timeout
//   - uri: the resource URI to read
//
// Returns:
//   - *ReadResourceResult: the resource content
//   - error: non-nil if reading fails
type ResourceHandler func(ctx context.Context, uri string) (*ReadResourceResult, error)

// PromptHandler handles a prompt get request from an MCP client.
//
// Args:
//   - ctx: context for cancellation and timeout
//   - args: prompt arguments as a string map
//
// Returns:
//   - *GetPromptResult: the prompt messages
//   - error: non-nil if prompt generation fails
type PromptHandler func(ctx context.Context, args map[string]string) (*GetPromptResult, error)

// --- Registered types ---

// registeredTool holds a tool's handler, input schema, and description.
type registeredTool struct {
	handler     ToolHandler
	schema      json.RawMessage // inputSchema JSON
	description string
}

// registeredResource holds a resource's handler and URI.
type registeredResource struct {
	handler     ResourceHandler
	uri         string
	name        string
	description string
	mimeType    string
}

// registeredResourceTemplate holds a resource template's handler and URI pattern.
type registeredResourceTemplate struct {
	handler     ResourceHandler
	uriTemplate string
	description string
	mimeType    string
}

// registeredPrompt holds a prompt's handler, argument definitions, and description.
type registeredPrompt struct {
	handler     PromptHandler
	args        []PromptArgument // argument schema
	description string
}

// --- Errors ---

// ErrDuplicateRegistration indicates a tool/resource/prompt with the same name already exists.
// Errors are defined in errors.go.

// ErrEmptyName indicates that a name parameter was empty.
// Errors are defined in errors.go.

// --- MCPServer ---

// MCPServer hosts MCP tools, resources, and prompts for external clients.
// It implements the MCP protocol server side, handling JSON-RPC requests
// over a pluggable transport layer.
type MCPServer struct {
	info              Implementation
	capabilities      ServerCapabilities
	tools             map[string]*registeredTool
	resources         map[string]*registeredResource
	resourceTemplates []*registeredResourceTemplate
	prompts           map[string]*registeredPrompt
	transport         ServerTransport
	mu                sync.RWMutex
	serveCtx          context.Context
	handlerTimeout    time.Duration
}

// handlerCtx returns the server lifecycle context for handler timeouts,
// falling back to context.Background() if Serve has not been called yet.
func (s *MCPServer) handlerCtx() context.Context {
	if s.serveCtx != nil {
		return s.serveCtx
	}
	return context.Background()
}

// defaultHandlerTimeout is the timeout applied to individual tool/resource/prompt handlers.
const defaultHandlerTimeout = 30 * time.Second

// NewMCPServer creates a new MCPServer instance.
//
// Args:
//   - info: server identification (name and version)
//   - transport: the transport layer for communication
//
// Returns:
//   - *MCPServer: the new server instance
func NewMCPServer(info Implementation, transport ServerTransport) *MCPServer {
	return &MCPServer{
		info:           info,
		transport:      transport,
		tools:          make(map[string]*registeredTool),
		resources:      make(map[string]*registeredResource),
		prompts:        make(map[string]*registeredPrompt),
		handlerTimeout: defaultHandlerTimeout,
	}
}

// WithHandlerTimeout returns a functional option that sets the handler timeout.
// This must be called before Serve().
func (s *MCPServer) WithHandlerTimeout(timeout time.Duration) *MCPServer {
	s.handlerTimeout = timeout
	return s
}

// RegisterTool registers a tool handler with the server.
//
// Args:
//   - name: unique tool identifier (must not be empty)
//   - description: human-readable description of the tool
//   - inputSchema: JSON Schema describing the tool's input parameters
//   - handler: function to invoke when the tool is called
//
// Returns:
//   - error: non-nil if name is empty or tool is already registered
func (s *MCPServer) RegisterTool(name string, description string, inputSchema json.RawMessage, handler ToolHandler) error {
	if name == "" {
		return ErrEmptyName
	}
	if handler == nil {
		return errors.New("handler must not be nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.tools[name]; exists {
		return fmt.Errorf("%w: tool %q", ErrDuplicateRegistration, name)
	}

	s.tools[name] = &registeredTool{
		handler:     handler,
		schema:      inputSchema,
		description: description,
	}

	// Update capabilities to indicate tools are available.
	s.capabilities.Tools = &ToolServerCapabilities{}

	return nil
}

// RegisterResource registers a static resource with the server.
//
// Args:
//   - uri: unique resource URI (must not be empty)
//   - description: human-readable description
//   - mimeType: MIME type of the resource content
//   - handler: function to invoke when the resource is read
//
// Returns:
//   - error: non-nil if uri is empty or resource is already registered
func (s *MCPServer) RegisterResource(uri string, description string, mimeType string, handler ResourceHandler) error {
	if uri == "" {
		return fmt.Errorf("%w: resource uri", ErrEmptyName)
	}
	if handler == nil {
		return errors.New("handler must not be nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.resources[uri]; exists {
		return fmt.Errorf("%w: resource %q", ErrDuplicateRegistration, uri)
	}

	s.resources[uri] = &registeredResource{
		handler:     handler,
		uri:         uri,
		name:        uri,
		description: description,
		mimeType:    mimeType,
	}

	s.capabilities.Resources = &ResourceServerCapabilities{}
	return nil
}

// ResourceTemplate registers a resource URI template with the server.
//
// Args:
//   - uriTemplate: URI template pattern (e.g., "weather://{city}")
//   - description: human-readable description
//   - mimeType: MIME type of the resource content
//   - handler: function to invoke when a matching resource is read
//
// Returns:
//   - error: non-nil if uriTemplate is empty or template is already registered
func (s *MCPServer) ResourceTemplate(uriTemplate string, description string, mimeType string, handler ResourceHandler) error {
	if uriTemplate == "" {
		return fmt.Errorf("%w: resource template", ErrEmptyName)
	}
	if handler == nil {
		return errors.New("handler must not be nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, t := range s.resourceTemplates {
		if t.uriTemplate == uriTemplate {
			return fmt.Errorf("%w: template %q", ErrDuplicateRegistration, uriTemplate)
		}
	}

	s.resourceTemplates = append(s.resourceTemplates, &registeredResourceTemplate{
		handler:     handler,
		uriTemplate: uriTemplate,
		description: description,
		mimeType:    mimeType,
	})

	s.capabilities.Resources = &ResourceServerCapabilities{}
	return nil
}

// RegisterPrompt registers a prompt handler with the server.
//
// Args:
//   - name: unique prompt identifier (must not be empty)
//   - description: human-readable description of the prompt
//   - args: list of accepted arguments with their schemas
//   - handler: function to invoke when the prompt is requested
//
// Returns:
//   - error: non-nil if name is empty or prompt is already registered
func (s *MCPServer) RegisterPrompt(name string, description string, args []PromptArgument, handler PromptHandler) error {
	if name == "" {
		return fmt.Errorf("%w: prompt", ErrEmptyName)
	}
	if handler == nil {
		return errors.New("handler must not be nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.prompts[name]; exists {
		return fmt.Errorf("%w: prompt %q", ErrDuplicateRegistration, name)
	}

	s.prompts[name] = &registeredPrompt{
		handler:     handler,
		args:        args,
		description: description,
	}

	s.capabilities.Prompts = &PromptServerCapabilities{}
	return nil
}

// Serve starts the MCP server main loop.
// It starts the transport, then reads requests in a loop, dispatches
// each request to the appropriate handler, and sends back responses.
// The loop continues until the context is cancelled or a fatal error occurs.
//
// Args:
//   - ctx: context for cancellation and lifecycle management
//
// Returns:
//   - error: non-nil if the server fails to start or encounters a fatal error
func (s *MCPServer) Serve(ctx context.Context) error {
	if s.transport == nil {
		return errors.New("transport is required")
	}

	s.serveCtx = ctx

	if err := s.transport.Start(ctx); err != nil {
		return fmt.Errorf("start transport: %w", err)
	}

	log.Info("mcp-server: serving",
		"name", s.info.Name,
		"version", s.info.Version,
	)

	eg, ctx := errgroup.WithContext(ctx)

	eg.Go(func() error {
		return s.serveLoop(ctx)
	})

	if err := eg.Wait(); err != nil {
		if ctx.Err() != nil {
			log.Info("mcp-server: shutdown complete")
			return nil
		}
		return err
	}

	return nil
}

// serveLoop is the main request dispatch loop.
func (s *MCPServer) serveLoop(ctx context.Context) error {
	for {
		msg, err := s.transport.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil // graceful shutdown
			}
			return fmt.Errorf("accept request: %w", err)
		}

		resp := s.dispatch(msg)
		if resp != nil {
			if sendErr := s.transport.Send(ctx, resp); sendErr != nil {
				log.Warn("mcp-server: failed to send response",
					"method", msg.Method,
					"error", sendErr,
				)
			}
		}
	}
}

// dispatch routes an incoming message to the appropriate handler.
func (s *MCPServer) dispatch(msg *JSONRPCMessage) *JSONRPCMessage {
	switch {
	case IsRequest(msg):
		return s.handleRequest(msg)
	case IsNotification(msg):
		s.handleNotification(msg)
		return nil
	default:
		if msg.ID != nil {
			resp, _ := NewErrorResponse(*msg.ID, InvalidRequest, "invalid request", nil)
			return resp
		}
		return nil
	}
}

// handleRequest processes a JSON-RPC request and returns a response.
func (s *MCPServer) handleRequest(msg *JSONRPCMessage) *JSONRPCMessage {
	switch msg.Method {
	case MethodInitialize:
		return s.handleInitialize(msg)
	case MethodToolsList:
		return s.handleToolsList(msg)
	case MethodToolsCall:
		return s.handleToolsCall(msg)
	case MethodResourcesList:
		return s.handleResourcesList(msg)
	case MethodResourcesRead:
		return s.handleResourcesRead(msg)
	case MethodPromptsList:
		return s.handlePromptsList(msg)
	case MethodPromptsGet:
		return s.handlePromptsGet(msg)
	case MethodPing:
		return s.handlePing(msg)
	default:
		resp, _ := NewErrorResponse(*msg.ID, MethodNotFound,
			fmt.Sprintf("method not found: %s", msg.Method), nil)
		return resp
	}
}

// handleNotification processes a JSON-RPC notification (no response).
func (s *MCPServer) handleNotification(msg *JSONRPCMessage) {
	switch msg.Method {
	case NotificationInitialized:
		log.Debug("mcp-server: received initialized notification")
	default:
		log.Debug("mcp-server: unknown notification", "method", msg.Method)
	}
}

// handleInitialize processes the initialize request.
func (s *MCPServer) handleInitialize(msg *JSONRPCMessage) *JSONRPCMessage {
	result := InitializeResult{
		ProtocolVersion: ProtocolVersion,
		ServerInfo:      s.info,
		Capabilities:    s.getCapabilities(),
	}

	resp, err := NewResponse(*msg.ID, result)
	if err != nil {
		resp, _ = NewErrorResponse(*msg.ID, InternalError, "failed to create response", nil)
	}

	return resp
}

// handleToolsList returns the list of registered tools.
func (s *MCPServer) handleToolsList(msg *JSONRPCMessage) *JSONRPCMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tools := make([]MCPToolDef, 0, len(s.tools))
	for name, t := range s.tools {
		tools = append(tools, MCPToolDef{
			Name:        name,
			Description: t.description,
			InputSchema: t.schema,
		})
	}

	result := ToolsListResult{Tools: tools}
	resp, err := NewResponse(*msg.ID, result)
	if err != nil {
		resp, _ = NewErrorResponse(*msg.ID, InternalError, "failed to create response", nil)
	}

	return resp
}

// handleToolsCall invokes a registered tool handler.
func (s *MCPServer) handleToolsCall(msg *JSONRPCMessage) *JSONRPCMessage {
	var params ToolCallParams
	if err := DecodeParams(msg, &params); err != nil {
		resp, _ := NewErrorResponse(*msg.ID, InvalidParams, "invalid params: "+err.Error(), nil)
		return resp
	}

	s.mu.RLock()
	tool, ok := s.tools[params.Name]
	s.mu.RUnlock()

	if !ok {
		resp, _ := NewErrorResponse(*msg.ID, MethodNotFound,
			fmt.Sprintf("tool not found: %s", params.Name), nil)
		return resp
	}

	ctx, cancel := context.WithTimeout(s.handlerCtx(), s.handlerTimeout)
	defer cancel()

	result, err := tool.handler(ctx, params.Arguments)
	if err != nil {
		errResp, _ := NewErrorResponse(*msg.ID, InternalError, err.Error(), nil)
		return errResp
	}

	if result == nil {
		result = &ToolCallResult{Content: []ContentBlock{}}
	}

	resp, err := NewResponse(*msg.ID, result)
	if err != nil {
		resp, _ = NewErrorResponse(*msg.ID, InternalError, "failed to create response", nil)
	}

	return resp
}

// handleResourcesList returns the list of registered resources.
func (s *MCPServer) handleResourcesList(msg *JSONRPCMessage) *JSONRPCMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]ResourceItem, 0, len(s.resources))
	for _, r := range s.resources {
		items = append(items, ResourceItem{
			URI:         r.uri,
			Name:        r.name,
			Description: r.description,
			MimeType:    r.mimeType,
		})
	}

	result := ListResourcesResult{Resources: items}
	resp, err := NewResponse(*msg.ID, result)
	if err != nil {
		resp, _ = NewErrorResponse(*msg.ID, InternalError, "failed to create response", nil)
	}

	return resp
}

// matchURITemplate attempts to match a URI against a template pattern.
// Templates use {param} style placeholders (e.g., "file://{path}").
// A placeholder that is the last template part matches all remaining URI
// segments (greedy); otherwise each placeholder matches exactly one segment.
func matchURITemplate(uri string, tmpl string) bool {
	// Decode URL-encoded characters for more flexible matching. On decode
	// failure (invalid percent-encoding), fall back to the raw input so a
	// malformed URI does not silently mismatch.
	decodedURI := uri
	if d, err := url.PathUnescape(uri); err == nil {
		decodedURI = d
	}
	decodedTmpl := tmpl
	if d, err := url.PathUnescape(tmpl); err == nil {
		decodedTmpl = d
	}

	tmplParts := splitTemplate(decodedTmpl)
	uriParts := splitURI(decodedURI)

	// Quick check: if no placeholders, both must have same number of parts.
	hasPlaceholder := false
	for _, p := range tmplParts {
		if isPlaceholder(p) {
			hasPlaceholder = true
			break
		}
	}
	if !hasPlaceholder {
		if len(tmplParts) != len(uriParts) {
			return false
		}
		for i := range tmplParts {
			if tmplParts[i] != uriParts[i] {
				return false
			}
		}
		return true
	}

	// With placeholders: iterate through template parts.
	ti := 0
	ui := 0
	for ti < len(tmplParts) && ui < len(uriParts) {
		if isPlaceholder(tmplParts[ti]) {
			// If this is the last template part, it greedily matches all remaining.
			if ti == len(tmplParts)-1 {
				return true
			}
			// Otherwise consume one URI segment.
			ti++
			ui++
		} else {
			if tmplParts[ti] != uriParts[ui] {
				return false
			}
			ti++
			ui++
		}
	}

	// Both must be fully consumed (unless last part was greedy placeholder).
	return ti == len(tmplParts) && ui == len(uriParts)
}

// splitTemplate splits a URI template into parts, preserving placeholders.
func splitTemplate(tmpl string) []string {
	return splitPath(tmpl)
}

// splitURI splits a URI into path parts.
func splitURI(uri string) []string {
	return splitPath(uri)
}

// splitPath splits a path-like string by '/', filtering empty parts.
func splitPath(path string) []string {
	var parts []string
	start := 0
	for i := 0; i <= len(path); i++ {
		if i == len(path) || path[i] == '/' {
			if start < i {
				parts = append(parts, path[start:i])
			}
			start = i + 1
		}
	}
	return parts
}

// isPlaceholder checks if a template part is a {param} placeholder.
func isPlaceholder(part string) bool {
	return len(part) >= 2 && part[0] == '{' && part[len(part)-1] == '}'
}

// findMatchingTemplate searches resourceTemplates for one that matches the given URI.
// Returns the matched template or nil if none match.
func (s *MCPServer) findMatchingTemplate(uri string) *registeredResourceTemplate {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, t := range s.resourceTemplates {
		if matchURITemplate(uri, t.uriTemplate) {
			return t
		}
	}
	return nil
}

// handleResourcesRead reads a resource by URI.
func (s *MCPServer) handleResourcesRead(msg *JSONRPCMessage) *JSONRPCMessage {
	type readParams struct {
		URI string `json:"uri"`
	}

	var params readParams
	if err := DecodeParams(msg, &params); err != nil {
		resp, _ := NewErrorResponse(*msg.ID, InvalidParams, "invalid params: "+err.Error(), nil)
		return resp
	}

	// First, try exact match against registered resources.
	s.mu.RLock()
	resource, ok := s.resources[params.URI]
	s.mu.RUnlock()

	var handler ResourceHandler

	if ok {
		handler = resource.handler
	} else {
		// Then, try matching against resource templates.
		template := s.findMatchingTemplate(params.URI)
		if template == nil {
			resp, _ := NewErrorResponse(*msg.ID, MethodNotFound,
				fmt.Sprintf("resource not found: %s", params.URI), nil)
			return resp
		}
		handler = template.handler
	}

	ctx, cancel := context.WithTimeout(s.handlerCtx(), s.handlerTimeout)
	defer cancel()

	result, err := handler(ctx, params.URI)
	if err != nil {
		errResp, _ := NewErrorResponse(*msg.ID, InternalError, err.Error(), nil)
		return errResp
	}

	if result == nil {
		result = &ReadResourceResult{Contents: []ResourceContent{}}
	}

	resp, err := NewResponse(*msg.ID, result)
	if err != nil {
		resp, _ = NewErrorResponse(*msg.ID, InternalError, "failed to create response", nil)
	}

	return resp
}

// handlePromptsList returns the list of registered prompts.
func (s *MCPServer) handlePromptsList(msg *JSONRPCMessage) *JSONRPCMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items := make([]PromptItem, 0, len(s.prompts))
	for name, p := range s.prompts {
		items = append(items, PromptItem{
			Name:        name,
			Description: p.description,
		})
	}

	result := ListPromptsResult{Prompts: items}
	resp, err := NewResponse(*msg.ID, result)
	if err != nil {
		resp, _ = NewErrorResponse(*msg.ID, InternalError, "failed to create response", nil)
	}

	return resp
}

// handlePromptsGet returns a prompt by name with its messages.
func (s *MCPServer) handlePromptsGet(msg *JSONRPCMessage) *JSONRPCMessage {
	type getPromptParams struct {
		Name      string            `json:"name"`
		Arguments map[string]string `json:"arguments,omitempty"`
	}

	var params getPromptParams
	if err := DecodeParams(msg, &params); err != nil {
		resp, _ := NewErrorResponse(*msg.ID, InvalidParams, "invalid params: "+err.Error(), nil)
		return resp
	}

	s.mu.RLock()
	prompt, ok := s.prompts[params.Name]
	s.mu.RUnlock()

	if !ok {
		resp, _ := NewErrorResponse(*msg.ID, MethodNotFound,
			fmt.Sprintf("prompt not found: %s", params.Name), nil)
		return resp
	}

	ctx, cancel := context.WithTimeout(s.handlerCtx(), s.handlerTimeout)
	defer cancel()

	result, err := prompt.handler(ctx, params.Arguments)
	if err != nil {
		errResp, _ := NewErrorResponse(*msg.ID, InternalError, err.Error(), nil)
		return errResp
	}

	if result == nil {
		result = &GetPromptResult{Messages: []PromptMessage{}}
	}

	resp, err := NewResponse(*msg.ID, result)
	if err != nil {
		resp, _ = NewErrorResponse(*msg.ID, InternalError, "failed to create response", nil)
	}

	return resp
}

// handlePing responds to ping with an empty result.
func (s *MCPServer) handlePing(msg *JSONRPCMessage) *JSONRPCMessage {
	resp, err := NewResponse(*msg.ID, PingResult{})
	if err != nil {
		resp, _ = NewErrorResponse(*msg.ID, InternalError, "failed to create response", nil)
	}

	return resp
}

// getCapabilities returns a snapshot of current server capabilities.
func (s *MCPServer) getCapabilities() ServerCapabilities {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.capabilities
}

// ToolCount returns the number of registered tools.
//
// Returns:
//   - int: the count of registered tools
func (s *MCPServer) ToolCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.tools)
}

// ResourceCount returns the number of registered resources.
//
// Returns:
//   - int: the count of registered resources
func (s *MCPServer) ResourceCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.resources)
}

// PromptCount returns the number of registered prompts.
//
// Returns:
//   - int: the count of registered prompts
func (s *MCPServer) PromptCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.prompts)
}
