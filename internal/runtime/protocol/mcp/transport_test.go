package ares_mcp

import (
	"context"
	"runtime"
	"testing"
	"time"
)

func TestNewStdioTransport(t *testing.T) {
	tr := NewStdioTransport(StdioConfig{Command: "echo"})
	if tr == nil {
		t.Fatal("NewStdioTransport returned nil")
	}
}

func TestStdioTransportStartEmptyCommand(t *testing.T) {
	tr := NewStdioTransport(StdioConfig{})
	err := tr.Start(context.Background())
	if err == nil {
		t.Fatal("expected error for empty command, got nil")
	}
}

func TestStdioTransportDoubleStart(t *testing.T) {
	// Use a command that stays alive so we can test double-start.
	tr := NewStdioTransport(StdioConfig{Command: "cat"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := tr.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	defer func() { _ = tr.Close() }()

	err := tr.Start(ctx)
	if err == nil {
		t.Fatal("expected error for double start, got nil")
	}
}

func TestStdioTransportCloseUnstarted(t *testing.T) {
	tr := NewStdioTransport(StdioConfig{Command: "echo"})
	// Close on an unstarted transport should be a no-op.
	if err := tr.Close(); err != nil {
		t.Errorf("Close on unstarted: %v", err)
	}
}

func TestStdioTransportSendBeforeStart(t *testing.T) {
	tr := NewStdioTransport(StdioConfig{Command: "echo"})
	msg := &JSONRPCMessage{JSONRPC: JSONRPCVersion, Method: "test"}
	err := tr.Send(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error for send before start, got nil")
	}
}

func TestStdioTransportReceiveBeforeStart(t *testing.T) {
	tr := NewStdioTransport(StdioConfig{Command: "echo"})
	_, err := tr.Receive(context.Background())
	if err == nil {
		t.Fatal("expected error for receive before start, got nil")
	}
}

func TestStdioTransportStartWithInvalidCommand(t *testing.T) {
	tr := NewStdioTransport(StdioConfig{Command: "/nonexistent/binary/that/does/not/exist"})
	err := tr.Start(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid command, got nil")
	}
}

func TestNewSSETransport(t *testing.T) {
	tr := NewSSETransport(SSEConfig{URL: "http://localhost:0"})
	if tr == nil {
		t.Fatal("NewSSETransport returned nil")
	}
}

func TestSSETransportStartEmptyURL(t *testing.T) {
	tr := NewSSETransport(SSEConfig{})
	err := tr.Start(context.Background())
	if err == nil {
		t.Fatal("expected error for empty URL, got nil")
	}
}

func TestSSETransportDoubleStart(t *testing.T) {
	// Start with a URL that won't be connected to (we just test the guard).
	tr := NewSSETransport(SSEConfig{URL: "http://localhost:0"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := tr.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	defer func() { _ = tr.Close() }()

	err := tr.Start(ctx)
	if err == nil {
		t.Fatal("expected error for double start, got nil")
	}
}

func TestSSETransportCloseUnstarted(t *testing.T) {
	tr := NewSSETransport(SSEConfig{URL: "http://localhost:0"})
	if err := tr.Close(); err != nil {
		t.Errorf("Close on unstarted: %v", err)
	}
}

func TestSSETransportSendBeforeStart(t *testing.T) {
	tr := NewSSETransport(SSEConfig{URL: "http://localhost:0"})
	msg := &JSONRPCMessage{JSONRPC: JSONRPCVersion, Method: "test"}
	err := tr.Send(context.Background(), msg)
	if err == nil {
		t.Fatal("expected error for send before start, got nil")
	}
}

func TestSSETransportDefaultTimeout(t *testing.T) {
	tr := NewSSETransport(SSEConfig{URL: "http://localhost:0"})
	// SSE connections are long-lived: http.Client.Timeout must be zero so the
	// stream is not killed. A custom Transport with DialContext provides the
	// connection-establishment timeout instead.
	if tr.httpClient.Timeout != 0 {
		t.Error("expected no client timeout for SSE (long-lived stream)")
	}
	if tr.httpClient.Transport == nil {
		t.Error("expected transport with dial timeout to be set")
	}
}

func TestSSETransportCustomTimeout(t *testing.T) {
	tr := NewSSETransport(SSEConfig{URL: "http://localhost:0", Timeout: 5000000000}) // 5s
	// The timeout is applied via Transport.DialContext, not Client.Timeout.
	if tr.httpClient.Timeout != 0 {
		t.Error("expected no client timeout for SSE (long-lived stream)")
	}
	if tr.httpClient.Transport == nil {
		t.Error("expected transport with dial timeout to be set")
	}
}

func TestNewTransportFromConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  TransportConfig
		wantErr bool
	}{
		{
			name: "valid stdio",
			config: TransportConfig{
				Type: "stdio",
				Stdio: &StdioConfig{
					Command: "echo",
				},
			},
			wantErr: false,
		},
		{
			name: "valid sse",
			config: TransportConfig{
				Type: "sse",
				SSE: &SSEConfig{
					URL: "http://localhost:0",
				},
			},
			wantErr: false,
		},
		{
			name: "stdio without config",
			config: TransportConfig{
				Type: "stdio",
			},
			wantErr: true,
		},
		{
			name: "sse without config",
			config: TransportConfig{
				Type: "sse",
			},
			wantErr: true,
		},
		{
			name: "unsupported type",
			config: TransportConfig{
				Type: "websocket",
			},
			wantErr: true,
		},
		{
			name:    "empty type",
			config:  TransportConfig{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, err := NewTransportFromConfig(tt.config)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr && tr == nil {
				t.Error("expected non-nil transport")
			}
		})
	}
}

func TestNewTransportFromConfigReturnsCorrectType(t *testing.T) {
	tr, err := NewTransportFromConfig(TransportConfig{
		Type:  "stdio",
		Stdio: &StdioConfig{Command: "echo"},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if _, ok := tr.(*StdioTransport); !ok {
		t.Errorf("expected *StdioTransport, got %T", tr)
	}

	tr, err = NewTransportFromConfig(TransportConfig{
		Type: "sse",
		SSE:  &SSEConfig{URL: "http://localhost:0"},
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if _, ok := tr.(*SSETransport); !ok {
		t.Errorf("expected *SSETransport, got %T", tr)
	}
}

func TestStdioTransportReceiveContextCancelNoLeak(t *testing.T) {
	// Use "cat" which reads stdin and echoes to stdout — it will block waiting for input,
	// so Receive() will block in t.stdout.Scan() until we cancel the context.
	tr := NewStdioTransport(StdioConfig{Command: "cat"})
	ctx, cancel := context.WithCancel(context.Background())

	if err := tr.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = tr.Close() }()

	before := runtime.NumGoroutine()

	// Cancel context after a short delay to trigger the ctx.Done() path in Receive().
	time.AfterFunc(50*time.Millisecond, cancel)
	_, err := tr.Receive(ctx)
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}

	// Give goroutines a moment to exit.
	time.Sleep(100 * time.Millisecond)

	after := runtime.NumGoroutine()
	diff := after - before
	if diff > 2 {
		t.Fatalf("possible goroutine leak: goroutines increased by %d (before=%d, after=%d)", diff, before, after)
	}
}
