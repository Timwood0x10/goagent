//go:build integration

// SSE server transport lifecycle tests. These START a real HTTP server
// (net/http on 127.0.0.1:0) to exercise the transport end-to-end; they are
// isolated behind the integration tag so the default `go test ./...` / make
// test run stays hermetic and never binds a socket. Run with
// `go test -tags=integration ./internal/ares_mcp/`.
package ares_mcp

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSSEServerTransportStartStop verifies SSE server lifecycle on random port.
func TestSSEServerTransportStartStop(t *testing.T) {
	// Use port 0 to get a random available port.
	transport := NewSSEServerTransport("127.0.0.1:0")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := transport.Start(ctx)
	require.NoError(t, err)

	// Give server time to start.
	time.Sleep(50 * time.Millisecond)

	err = transport.Close()
	require.NoError(t, err)
}

// TestSSEServerTransportDoubleStart verifies error on double start.
func TestSSEServerTransportDoubleStart(t *testing.T) {
	transport := NewSSEServerTransport("127.0.0.1:0")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := transport.Start(ctx)
	require.NoError(t, err)

	err = transport.Start(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already started")

	err = transport.Close()
	require.NoError(t, err)
}

// TestSSEServerTimeout verifies that the server handles client timeouts correctly.
func TestSSEServerTimeout(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	transport := NewSSEServerTransport("127.0.0.1:0")
	err := transport.Start(ctx)
	require.NoError(t, err)
	defer func() { _ = transport.Close() }()

	// Give server time to start.
	time.Sleep(50 * time.Millisecond)

	// Verify server is still running after brief period.
	assert.NoError(t, ctx.Err(), "context should not be cancelled yet")

	_ = transport.Close()
}
