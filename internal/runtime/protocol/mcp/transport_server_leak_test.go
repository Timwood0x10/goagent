package ares_mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestSSEDisconnectNoGoroutineLeak pins the SSE handler leak fix (DEEP_CODE_REVIEW
// 1.7): the handler's deferred cleanup used to drain msgCh AFTER removing the
// session from t.sessions, but Close() — the only place that closes msgCh —
// looks sessions up by ID, so the drain could never observe a close and every
// disconnected client leaked one goroutine forever.
//
// The regression signal: run the SSE GET handler through httptest with real
// request contexts, disconnect the client, and wait for the handler goroutine
// to exit. We assert the goroutine count returns to baseline (after the
// server-side handler and its test client fully unwind), using retries to
// absorb scheduling jitter instead of a single racy snapshot.
func TestSSEDisconnectNoGoroutineLeak(t *testing.T) {
	tr := NewSSEServerTransport("127.0.0.1:0")
	require.NoError(t, tr.Start(context.Background()))
	t.Cleanup(func() { _ = tr.Close() })

	// Baseline after transport Start (its errgroup + http server are up).
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	// Drive N clients through the SSE connect path with REAL request
	// contexts, then cancel the request context to simulate client
	// disconnect. Each handler goroutine must exit.
	const clients = 5
	for i := 0; i < clients; i++ {
		req := httptest.NewRequest("GET", "/mcp", nil)
		ctx, cancel := context.WithCancel(req.Context())
		req = req.WithContext(ctx)
		w := newFlushRecorder()

		done := make(chan struct{})
		go func() {
			defer close(done)
			tr.handleSSEConnect(w, req)
		}()

		// Wait until the session is registered (handler is live).
		require.Eventually(t, func() bool {
			tr.sessionsMu.Lock()
			defer tr.sessionsMu.Unlock()
			return len(tr.sessions) == 1
		}, 2*time.Second, 5*time.Millisecond, "session must register")

		// Client disconnects.
		cancel()
		select {
		case <-done:
			// handler returned — good.
		case <-time.After(2 * time.Second):
			t.Fatal("SSE handler did not return after client disconnect")
		}
		// Session must be removed by the deferred cleanup.
		require.Eventually(t, func() bool {
			tr.sessionsMu.Lock()
			defer tr.sessionsMu.Unlock()
			return len(tr.sessions) == 0
		}, 2*time.Second, 5*time.Millisecond, "session must be deregistered")
	}

	// The leak: under the bug, each disconnected client left a goroutine
	// blocked in `for range msgCh` inside the DEFERRED cleanup — wait for
	// pending goroutines to unwind, then compare counts with retries.
	// NOTE: NumGoroutine polling boundary — we poll for up to 2s; the leak
	// under test NEVER resolves, so a stable count is conclusive.
	leakFree := false
	var last int
	for i := 0; i < 40 && !leakFree; i++ {
		time.Sleep(50 * time.Millisecond)
		last = runtime.NumGoroutine()
		// Allow small scheduling noise but not one goroutine per client.
		if last <= baseline+1 {
			leakFree = true
		}
	}
	if !leakFree {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		stacks := string(buf[:n])
		t.Fatalf("goroutine leak after %d SSE client disconnects: baseline=%d now=%d\nstacks containing msgCh:\n%s",
			clients, baseline, last,
			extractDrainStacks(stacks))
	}
}

// extractDrainStacks filters full goroutine stacks down to ones parked on the
// leaked drain loop, keeping the failure output readable.
func extractDrainStacks(stacks string) string {
	var b strings.Builder
	for _, chunk := range strings.Split(stacks, "\n\n") {
		if strings.Contains(chunk, "msgCh") || strings.Contains(chunk, "handleSSEConnect") {
			b.WriteString(chunk)
			b.WriteString("\n\n")
		}
	}
	if b.Len() == 0 {
		return "(no handleSSEConnect frames found in dump)"
	}
	return b.String()
}

// flushRecorder is an http.ResponseWriter with Flush, mirroring the real SSE
// streaming writer.
type flushRecorder struct {
	header  http.Header
	written []byte
	flushed int
}

func newFlushRecorder() *flushRecorder {
	return &flushRecorder{header: http.Header{}}
}

func (f *flushRecorder) Header() http.Header { return f.header }

func (f *flushRecorder) Write(p []byte) (int, error) {
	f.written = append(f.written, p...)
	return len(p), nil
}

func (f *flushRecorder) WriteHeader(int) {}

func (f *flushRecorder) Flush() { f.flushed++ }

var _ http.ResponseWriter = (*flushRecorder)(nil)
var _ http.Flusher = (*flushRecorder)(nil)
