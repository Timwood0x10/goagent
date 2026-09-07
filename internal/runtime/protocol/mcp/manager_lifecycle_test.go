package ares_mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

var lifecycleTestTools = []MCPToolDef{{
	Name:        "mock_tool",
	Description: "a tool",
	InputSchema: json.RawMessage(`{"type":"object"}`),
}}

// TestStopDoesNotDeadlockWithInFlightNotification pins the shutdown-deadlock
// fix: Stop must complete even when a tools/listChanged notification handler
// is concurrently re-entering the manager through RefreshTools. Under the old
// lock discipline (Close under m.mu while the notification goroutine waits
// for m.mu), this test hung forever.
func TestStopDoesNotDeadlockWithInFlightNotification(t *testing.T) {
	tr := newTestServer(lifecycleTestTools, nil)
	m := newTestManager(t, &MCPManagerConfig{}, core.NewRegistry())

	sc := &MCPServerConfig{
		Name:      "mock",
		Enabled:   true,
		AutoStart: false,
		Timeout:   2 * time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := m.connectWithTransport(ctx, "mock", sc, tr); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Queue a tools/listChanged notification so the receive loop spawns the
	// onChange → RefreshTools goroutine racing with Stop. The refresh's
	// tools/list is delayed 300ms so it is guaranteed INSIDE RefreshTools
	// (holding/blocking on m.mu) while Stop tears down.
	tr.mu.Lock()
	origFn := tr.respFn
	delayed := false
	tr.respFn = func(msg *JSONRPCMessage) *JSONRPCMessage {
		if msg.Method == MethodToolsList && !delayed {
			delayed = true
			time.Sleep(300 * time.Millisecond)
		}
		return origFn(msg)
	}
	tr.mu.Unlock()
	var notifID int64 = 99
	tr.respCh <- &JSONRPCMessage{JSONRPC: JSONRPCVersion, Method: NotificationToolsListChanged, ID: &notifID}

	done := make(chan error, 1)
	go func() { done <- m.Stop(ctx) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Stop returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Stop deadlocked with an in-flight notification handler")
	}
}

// TestConnectTwiceReplacesAndClosesStaleClient pins the double-connect fix:
// connecting twice must CLOSE the first client instead of orphaning its
// process and receive loop.
func TestConnectTwiceReplacesAndClosesStaleClient(t *testing.T) {
	first := newTestServer(lifecycleTestTools, nil)
	m := newTestManager(t, &MCPManagerConfig{}, core.NewRegistry())
	sc := &MCPServerConfig{Name: "mock", Enabled: true, Timeout: 2 * time.Second}
	ctx := context.Background()

	if err := m.connectWithTransport(ctx, "mock", sc, first); err != nil {
		t.Fatalf("first connect: %v", err)
	}

	second := newTestServer(lifecycleTestTools, nil)
	if err := m.connectWithTransport(ctx, "mock", sc, second); err != nil {
		t.Fatalf("second connect: %v", err)
	}

	// The stale client must be closed (transport closed + loops drained).
	deadline := time.Now().Add(3 * time.Second)
	closed := false
	for !closed && time.Now().Before(deadline) {
		first.mu.Lock()
		closed = first.closed
		first.mu.Unlock()
		if !closed {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !closed {
		t.Fatal("stale client was not closed after reconnect (leaked stdio process)")
	}

	if got := len(m.clients); got != 1 {
		t.Fatalf("want exactly one managed client, got %d", got)
	}
}

// TestRefreshToolsFailureRestoresPreviousTools pins refresh atomicity: a
// failed ListTools during refresh must NOT leave the registry empty — the
// previous tool set is restored.
func TestRefreshToolsFailureRestoresPreviousTools(t *testing.T) {
	tr := newTestServer(lifecycleTestTools, nil)
	m := newTestManager(t, &MCPManagerConfig{}, core.NewRegistry())
	sc := &MCPServerConfig{Name: "mock", Enabled: true, Timeout: 3 * time.Second}
	ctx := context.Background()

	if err := m.connectWithTransport(ctx, "mock", sc, tr); err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, ok := m.registry.Get("mcp.mock.mock_tool"); !ok {
		t.Fatalf("tool must be registered after connect; have %v", m.registry.List())
	}

	// Arm the failure: the NEXT tools/list gets no response. The call then
	// fails via ctx timeout, exercising the restore path.
	tr.mu.Lock()
	origFn := tr.respFn
	armed := false
	tr.respFn = func(msg *JSONRPCMessage) *JSONRPCMessage {
		if msg.Method == MethodToolsList && !armed {
			armed = true
			return nil // swallow once → ListTools times out
		}
		return origFn(msg)
	}
	tr.mu.Unlock()

	err := m.RefreshTools(ctx, "mock")
	if err == nil {
		t.Fatal("refresh must report the discovery failure")
	}

	if _, ok := m.registry.Get("mcp.mock.mock_tool"); !ok {
		t.Fatalf("previous tools must be restored after failed refresh; have %v", m.registry.List())
	}
}
