package agentipc

import (
	"context"
	"fmt"
)

// Trace propagation across the IPC boundary.
//
// Every message carries a TraceID that identifies one causal chain:
// a Send/Request starts a trace (or continues the caller's, see below),
// replies inherit it, and nested calls made by a handler inherit it
// automatically because the handler's context carries it. Following one
// TraceID through logs, dead letters, and collaboration receipts gives the
// full cross-agent story that CorrelationID alone cannot (correlation pairs
// a single request with its reply; a trace spans the whole delegation
// tree).
//
// Rules:
//   - Send/Request/Broadcast stamp the message with the caller's trace when
//     the context carries one (ContextWithTraceID), else mint a fresh id.
//   - Handlers receive a context carrying the request's TraceID, so any
//     Send/Request/Broadcast they make continues the same trace with no
//     extra code.
//   - Direct-return replies are stamped by the bus (invokeHandler); async
//     replies via Reply must copy req.TraceID — the bus cannot know which
//     pending request an out-of-band reply belongs to beyond corrID, and it
//     deliberately does not rewrite handler-built payloads. Contract,
//     enforced by TestReplyPreservesTraceID.
//   - Trace IDs are bus-local unique ("trace-<seq>"); the bus is
//     in-process (messages are never serialized onto a wire here), so no
//     global uniqueness scheme is needed.

// traceContextKey is the context key carrying a TraceID. Unexported: only
// the helpers below may read/write it, so a forged string key from another
// package can never collide with it.
type traceContextKey struct{}

// ContextWithTraceID returns a context carrying the given trace id for
// downstream Send/Request/Broadcast calls to continue.
func ContextWithTraceID(ctx context.Context, traceID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, traceContextKey{}, traceID)
}

// TraceIDFromContext returns the trace id carried by ctx, or "" when the
// caller runs outside any trace (Send/Request mint a fresh one then).
func TraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(traceContextKey{}).(string)
	return id
}

// allocTraceID mints a bus-local unique trace id (thread-safe).
func (b *Bus) allocTraceID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.traceSeq++
	return fmt.Sprintf("trace-%d", b.traceSeq)
}

// traceOrNew resolves the trace for an outgoing message: continue the
// caller's trace when present, else mint a fresh root trace.
func (b *Bus) traceOrNew(ctx context.Context) string {
	if id := TraceIDFromContext(ctx); id != "" {
		return id
	}
	return b.allocTraceID()
}
