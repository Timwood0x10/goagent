package ares_mcp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// procKillTransport wraps a mockTransport and emulates the stdio subprocess
// lifetime semantics of StdioTransport.Start: the "process" is bound to the
// context passed to Start (exec.CommandContext(lifetimeCtx, ...) in the real
// transport), and once that context is cancelled the process dies and Receive
// fails permanently — Receive parks on the process ctx so cancellation wakes
// it, exactly like a killed child's stdout scanner returning EOF.
type procKillTransport struct {
	*mockTransport
	mu      sync.Mutex
	procCtx context.Context
}

func (p *procKillTransport) Start(ctx context.Context) error {
	p.mu.Lock()
	p.procCtx = ctx
	p.mu.Unlock()
	return p.mockTransport.Start(ctx)
}

func (p *procKillTransport) Receive(_ context.Context) (*JSONRPCMessage, error) {
	p.mu.Lock()
	procCtx := p.procCtx
	p.mu.Unlock()
	if procCtx == nil {
		return p.mockTransport.Receive(context.Background())
	}
	select {
	case <-procCtx.Done():
		return nil, errors.New("subprocess killed: " + procCtx.Err().Error())
	case msg, ok := <-p.respCh:
		if !ok {
			return nil, errors.New("channel closed")
		}
		return msg, nil
	}
}

// TestConnectWithTransportSurvivesCallerCancel pins the skill_activate fix
// (DEEP_CODE_REVIEW 1.6): the managed client's LIFETIME must not be bound to
// the caller's context. connectWithTransport used client.Connect(ctx, ...),
// which bound both the handshake AND the subprocess to the caller ctx — when
// skill_activate returned and the dispatcher cancelled the request context,
// every lazily-connected MCP server was killed. The fix mirrors factory.go's
// ConnectWithLifetime pattern: the handshake is bounded by the caller ctx,
// the subprocess lives on a background lifetime context.
func TestConnectWithTransportSurvivesCallerCancel(t *testing.T) {
	tr := &procKillTransport{mockTransport: newTestServer(lifecycleTestTools, nil)}
	m := newTestManager(t, &MCPManagerConfig{}, core.NewRegistry())
	sc := &MCPServerConfig{Name: "mock", Enabled: true, Timeout: 2 * time.Second}

	ctx, cancel := context.WithCancel(context.Background())
	if err := m.connectWithTransport(ctx, "mock", sc, tr); err != nil {
		cancel()
		t.Fatalf("connect: %v", err)
	}

	// Simulate skill_activate returning and the dispatcher cancelling the
	// request context. Under the bug this kills the subprocess (its ctx was
	// the caller's); under the fix only the handshake scope ends.
	cancel()

	// Give a (buggy) ctx-cancellation cascade a moment to propagate through
	// the receive loop before asserting liveness.
	time.Sleep(100 * time.Millisecond)

	client, ok := m.GetClient("mock")
	if !ok {
		t.Fatal("managed client disappeared after caller ctx cancel")
	}
	if !client.IsConnected() {
		t.Fatal("managed client was disconnected by caller ctx cancel " +
			"(lifetime bound to the caller context, not background)")
	}
	// The connection must remain USABLE: a full request/response round-trip
	// on a fresh context, like a later tool call would do. Under the bug the
	// subprocess is dead, the receive loop has exited, and this times out.
	rtCtx, rtCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer rtCancel()
	if _, err := client.ListTools(rtCtx); err != nil {
		t.Fatalf("connection unusable after caller ctx cancel (ListTools): %v", err)
	}

	// The manager's Stop path must still close the client and its transport:
	// the lifetime context is manager-closeable via client.Close (cancel +
	// transport.Close), so no ctx-escape hatch leaks the subprocess either.
	if err := m.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if client.IsConnected() {
		t.Fatal("Stop must close the managed client (lifetime close wiring)")
	}
	if !tr.closed {
		t.Fatal("Stop must close the client's transport (subprocess reaped)")
	}
}
