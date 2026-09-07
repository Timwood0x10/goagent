package ares_mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// mockTransport implements Transport for testing.
// It uses a response function that generates responses for each request.
type mockTransport struct {
	mu      sync.Mutex
	started bool
	closed  bool
	respFn  func(msg *JSONRPCMessage) *JSONRPCMessage
	respCh  chan *JSONRPCMessage
	sent    []*JSONRPCMessage
	sendErr error
	recvErr error
}

// newMockServer creates a mock transport that responds to each request.
func newMockServer(handler func(msg *JSONRPCMessage) *JSONRPCMessage) *mockTransport {
	return &mockTransport{
		respFn: handler,
		respCh: make(chan *JSONRPCMessage, 16),
	}
}

func (t *mockTransport) Start(_ context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.started {
		return fmt.Errorf("already started")
	}
	t.started = true
	return nil
}

func (t *mockTransport) Send(_ context.Context, msg *JSONRPCMessage) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sendErr != nil {
		return t.sendErr
	}
	t.sent = append(t.sent, msg)

	// If we have a response function, generate and queue the response.
	if t.respFn != nil {
		resp := t.respFn(msg)
		if resp != nil {
			t.respCh <- resp
		}
	}

	return nil
}

func (t *mockTransport) Receive(_ context.Context) (*JSONRPCMessage, error) {
	if t.recvErr != nil {
		return nil, t.recvErr
	}
	msg, ok := <-t.respCh
	if !ok {
		return nil, fmt.Errorf("channel closed")
	}
	return msg, nil
}

func (t *mockTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	close(t.respCh)
	return nil
}

func (t *mockTransport) Sent() []*JSONRPCMessage {
	t.mu.Lock()
	defer t.mu.Unlock()
	result := make([]*JSONRPCMessage, len(t.sent))
	copy(result, t.sent)
	return result
}

func buildSimpleToolDef(name, desc string) MCPToolDef {
	return MCPToolDef{
		Name:        name,
		Description: desc,
		InputSchema: json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`),
	}
}
