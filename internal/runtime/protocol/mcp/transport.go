package ares_mcp

import "context"

// Transport defines the interface for MCP JSON-RPC message transport.
// Implementations handle the low-level communication with MCP servers.
type Transport interface {
	// Start initializes the transport connection.
	Start(ctx context.Context) error
	// Send sends a JSON-RPC message to the server.
	Send(ctx context.Context, msg *JSONRPCMessage) error
	// Receive reads the next JSON-RPC message from the server.
	Receive(ctx context.Context) (*JSONRPCMessage, error)
	// Close shuts down the transport and releases resources.
	Close() error
}
