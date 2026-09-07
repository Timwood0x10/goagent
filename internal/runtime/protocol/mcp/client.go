package ares_mcp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"
)

// Transport types
const (
	TransportTypeStdio = "stdio"
	TransportTypeSSE   = "sse"
)

// Default timeout for MCP client operations when no config timeout is specified.
const defaultClientTimeout = 30 * time.Second

// MCPClientConfig holds configuration for creating an MCPClient.
type MCPClientConfig struct {
	ServerName string
	Transport  TransportConfig
	Timeout    time.Duration
	OnChange   func()
}

// TransportConfig selects and configures a transport.
type TransportConfig struct {
	Type  string       `yaml:"type" json:"type"`
	Stdio *StdioConfig `yaml:"stdio,omitempty" json:"stdio,omitempty"`
	SSE   *SSEConfig   `yaml:"sse,omitempty" json:"sse,omitempty"`
}

// MCPClient manages a connection to a single MCP server.
type MCPClient struct {
	transport  Transport
	serverName string
	serverCaps *ServerCapabilities
	tools      map[string]*MCPToolDef
	mu         sync.RWMutex
	nextID     IDGenerator
	pending    map[int64]chan *JSONRPCMessage
	pendingMu  sync.Mutex
	onChange   func()
	timeout    time.Duration
	ctx        context.Context
	cancel     context.CancelFunc
	eg         errgroup.Group
	connected  atomic.Bool

	// notifySlots bounds concurrently running notification handlers (#27):
	// each notification spawned an unbounded goroutine, so a malicious or
	// buggy server flooding notifications allocated goroutines without limit
	// (each may also issue a ListTools round-trip). Full slots drop the
	// notification — notifications are advisory by JSON-RPC semantics.
	notifySlots chan struct{}
}

// maxConcurrentNotifications is the notification handler concurrency cap.
const maxConcurrentNotifications = 8

// NewMCPClient creates a new MCPClient with the given config.
func NewMCPClient(config MCPClientConfig) *MCPClient {
	timeout := config.Timeout
	if timeout == 0 {
		timeout = defaultClientTimeout
	}

	return &MCPClient{
		serverName:  config.ServerName,
		tools:       make(map[string]*MCPToolDef),
		pending:     make(map[int64]chan *JSONRPCMessage),
		onChange:    config.OnChange,
		timeout:     timeout,
		notifySlots: make(chan struct{}, maxConcurrentNotifications),
	}
}

// Connect starts the transport and performs the MCP initialize handshake.
// The client's lifetime is bound to ctx: cancelling ctx (or calling Close)
// tears down the connection.
func (c *MCPClient) Connect(ctx context.Context, transport Transport) error {
	return c.ConnectWithLifetime(ctx, ctx, transport)
}

// ConnectWithLifetime starts the transport and performs the MCP initialize
// handshake with separate handshake and lifetime scopes (#26): the handshake
// steps are bounded by ctx, while the client and its subprocess live as long
// as lifetimeCtx. This lets a factory bound only the initial connect — a
// short-lived handshake context must not cascade-cancel the client's own
// context, or the returned tool dies the moment Connect returns.
func (c *MCPClient) ConnectWithLifetime(ctx, lifetimeCtx context.Context, transport Transport) error {
	if transport == nil {
		return errors.New("transport is required")
	}
	if lifetimeCtx == nil {
		lifetimeCtx = ctx
	}

	c.transport = transport
	c.ctx, c.cancel = context.WithCancel(lifetimeCtx)

	// The subprocess must be bound to the LIFETIME context (it owns the
	// process); binding it to the handshake ctx would kill the child as soon
	// as the handshake scope ends (#26).
	if err := c.transport.Start(lifetimeCtx); err != nil {
		return fmt.Errorf("start transport: %w", err)
	}

	// Start receiving messages in background.
	c.eg.Go(func() error {
		return c.receiveLoop()
	})

	// Perform initialize handshake against the bounded handshake ctx.
	if err := c.initialize(ctx); err != nil {
		if closeErr := c.Close(); closeErr != nil {
			log.Warn("mcp: close after init failure", "error", closeErr)
		}
		return fmt.Errorf("initialize: %w", err)
	}

	c.connected.Store(true)

	// Discover initial tools against the bounded handshake ctx.
	if _, err := c.ListTools(ctx); err != nil {
		if closeErr := c.Close(); closeErr != nil {
			log.Warn("mcp: close after list tools failure", "error", closeErr)
		}
		return fmt.Errorf("list tools: %w", err)
	}

	return nil
}

// initialize performs the MCP initialize handshake.
func (c *MCPClient) initialize(ctx context.Context) error {
	params := InitializeParams{
		ProtocolVersion: ProtocolVersion,
		ClientInfo: Implementation{
			Name:    ClientName,
			Version: ClientVersion,
		},
		Capabilities: ClientCapabilities{
			Tools: &ToolClientCapabilities{
				ListChanged: true,
			},
		},
	}

	var result InitializeResult
	if err := c.call(ctx, MethodInitialize, params, &result); err != nil {
		return fmt.Errorf("initialize call: %w", err)
	}

	c.mu.Lock()
	c.serverCaps = &result.Capabilities
	c.mu.Unlock()

	// Send initialized notification.
	notif, err := NewNotification(NotificationInitialized, nil)
	if err != nil {
		return fmt.Errorf("create initialized notification: %w", err)
	}

	if err := c.transport.Send(ctx, notif); err != nil {
		return fmt.Errorf("send initialized: %w", err)
	}

	return nil
}

// ListTools requests the list of tools from the server.
func (c *MCPClient) ListTools(ctx context.Context) ([]MCPToolDef, error) {
	var result ToolsListResult
	if err := c.call(ctx, MethodToolsList, nil, &result); err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}

	c.mu.Lock()
	c.tools = make(map[string]*MCPToolDef, len(result.Tools))
	for i := range result.Tools {
		c.tools[result.Tools[i].Name] = &result.Tools[i]
	}
	c.mu.Unlock()

	return result.Tools, nil
}

// CallTool invokes a tool on the server.
func (c *MCPClient) CallTool(ctx context.Context, name string, args map[string]any) (*ToolCallResult, error) {
	params := ToolCallParams{
		Name:      name,
		Arguments: args,
	}

	var result ToolCallResult
	if err := c.call(ctx, MethodToolsCall, params, &result); err != nil {
		return nil, fmt.Errorf("call tool %s: %w", name, err)
	}

	return &result, nil
}

// ServerName returns the configured server name.
func (c *MCPClient) ServerName() string {
	return c.serverName
}

// ServerCapabilities returns the server's declared capabilities.
func (c *MCPClient) ServerCapabilities() *ServerCapabilities {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.serverCaps
}

// GetTool returns a tool definition by name.
func (c *MCPClient) GetTool(name string) (*MCPToolDef, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	t, ok := c.tools[name]
	return t, ok
}

// ToolCount returns the number of discovered tools.
func (c *MCPClient) ToolCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.tools)
}

// IsConnected returns true if the client is connected.
func (c *MCPClient) IsConnected() bool {
	return c.connected.Load()
}

// Close shuts down the client and transport.
func (c *MCPClient) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	c.connected.Store(false)

	if c.transport != nil {
		if err := c.transport.Close(); err != nil {
			log.Warn("mcp: transport close error", "server", c.serverName, "error", err)
		}
	}

	if err := c.eg.Wait(); err != nil && c.ctx.Err() == nil {
		log.Error("mcp: receive loop error", "server", c.serverName, "error", err)
	}

	// Close all pending channels.
	c.pendingMu.Lock()
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.pendingMu.Unlock()

	return nil
}

// call sends a request and waits for the correlated response.
func (c *MCPClient) call(ctx context.Context, method string, params interface{}, result interface{}) error {
	if !c.connected.Load() && method != MethodInitialize {
		return errors.New("not connected")
	}

	id := c.nextID.Next()

	msg, err := NewRequest(id, method, params)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	// Register pending channel before sending.
	ch := make(chan *JSONRPCMessage, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	if err := c.transport.Send(ctx, msg); err != nil {
		return fmt.Errorf("send: %w", err)
	}

	// Check parent context before applying timeout.
	if err := ctx.Err(); err != nil {
		return err
	}

	// Wait for response with timeout.
	callCtx, callCancel := context.WithTimeout(ctx, c.timeout)
	defer callCancel()

	select {
	case <-callCtx.Done():
		return fmt.Errorf("timeout waiting for response to %s: %w", method, callCtx.Err())
	case resp, ok := <-ch:
		if !ok {
			return fmt.Errorf("connection closed waiting for %s", method)
		}
		if resp.Error != nil {
			return resp.Error
		}
		if result != nil && len(resp.Result) > 0 {
			if err := DecodeResult(resp, result); err != nil {
				return fmt.Errorf("decode result: %w", err)
			}
		}
		return nil
	}
}

// receiveLoop reads messages from the transport and dispatches them.
func (c *MCPClient) receiveLoop() error {
	for {
		msg, err := c.transport.Receive(c.ctx)
		if err != nil {
			if c.ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("receive: %w", err)
		}

		switch {
		case IsResponse(msg) || IsError(msg):
			c.dispatchResponse(msg)
		case IsNotification(msg):
			// Run handleNotification in its own goroutine so receiveLoop can
			// continue processing responses. handleNotification may issue a
			// ListTools request whose response arrives on this loop; blocking
			// here would deadlock.
			//
			// Concurrency is capped via notifySlots (#27): try to take a slot
			// without blocking. When all handlers are busy, drop this
			// notification instead of queueing unboundedly — a flood must
			// cost the server its updates, not our memory.
			msg := msg
			select {
			case c.notifySlots <- struct{}{}:
				c.eg.Go(func() error {
					defer func() { <-c.notifySlots }()
					c.handleNotification(msg)
					return nil
				})
			default:
				log.Warn("mcp: notification dropped, handler slots full",
					"server", c.serverName, "method", msg.Method)
			}
		}
	}
}

// dispatchResponse routes a response to its pending channel.
func (c *MCPClient) dispatchResponse(msg *JSONRPCMessage) {
	if msg.ID == nil {
		return
	}

	// Hold pendingMu during the send to prevent a race with Close() which
	// closes pending channels under the same lock. If Close() runs first it
	// deletes the entry, so we won't send on a closed channel.
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	ch, ok := c.pending[*msg.ID]
	if !ok {
		return
	}
	// Remove from pending map; we now own the response delivery.
	delete(c.pending, *msg.ID)
	select {
	case ch <- msg:
	default:
		// Channel full means caller already stopped waiting (timeout/cancel).
		// Discard the stale response.
	}
}

// handleNotification processes server notifications.
func (c *MCPClient) handleNotification(msg *JSONRPCMessage) {
	if msg.Method == NotificationToolsListChanged {
		// Re-fetch tools list. Use a fresh context derived from the client
		// context so the request is cancelled when the client shuts down.
		ctx, cancel := context.WithTimeout(c.ctx, c.timeout)
		defer cancel()

		if _, err := c.ListTools(ctx); err == nil && c.onChange != nil {
			c.onChange()
		}
	}
}

// NewTransportFromConfig creates a Transport from a TransportConfig.
func NewTransportFromConfig(config TransportConfig) (Transport, error) {
	switch config.Type {
	case TransportTypeStdio:
		if config.Stdio == nil {
			return nil, errors.New("stdio config is required")
		}
		return NewStdioTransport(*config.Stdio), nil
	case TransportTypeSSE:
		if config.SSE == nil {
			return nil, errors.New("sse config is required")
		}
		return NewSSETransport(*config.SSE), nil
	default:
		return nil, fmt.Errorf("unsupported transport type: %s", config.Type)
	}
}
