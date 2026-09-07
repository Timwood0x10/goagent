package agentipc

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestTraceFreshRoot mints a trace on Send and carries it into the handler
// context (A-3): the handler's downstream calls continue it with no code.
func TestTraceFreshRoot(t *testing.T) {
	bus := NewBus()
	var handlerTrace string
	var handlerMsgTrace string
	if err := bus.Register("b", func(ctx context.Context, msg *Message) (*Message, error) {
		handlerTrace = TraceIDFromContext(ctx)
		handlerMsgTrace = msg.TraceID
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := bus.Send(context.Background(), "a", "b", "ping", nil); err != nil {
		t.Fatal(err)
	}
	if handlerMsgTrace == "" {
		t.Fatal("sent message carries no trace")
	}
	if handlerTrace != handlerMsgTrace {
		t.Fatalf("handler ctx trace %q != message trace %q", handlerTrace, handlerMsgTrace)
	}
}

// TestTraceContinuedFromContext locks continuation: a caller inside a trace
// extends it instead of minting a new root.
func TestTraceContinuedFromContext(t *testing.T) {
	bus := NewBus()
	var got string
	if err := bus.Register("b", func(_ context.Context, msg *Message) (*Message, error) {
		got = msg.TraceID
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	ctx := ContextWithTraceID(context.Background(), "trace-external-1")
	if err := bus.Send(ctx, "a", "b", "ping", nil); err != nil {
		t.Fatal(err)
	}
	if got != "trace-external-1" {
		t.Fatalf("trace = %q, want the caller's trace continued", got)
	}
}

// TestTraceSpansNestedCalls locks end-to-end propagation: a handler's nested
// Request inherits the inbound trace, and the direct-return reply carries it
// back to the original caller.
func TestTraceSpansNestedCalls(t *testing.T) {
	bus := NewBus()
	var nestedTrace string
	if err := bus.Register("mid", func(ctx context.Context, msg *Message) (*Message, error) {
		reply, err := bus.Request(ctx, "mid", "leaf", "work", nil, 2*time.Second)
		if err != nil {
			return nil, err
		}
		nestedTrace = reply.TraceID
		return &Message{Payload: "done"}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var leafTrace, leafCtxTrace string
	if err := bus.Register("leaf", func(ctx context.Context, msg *Message) (*Message, error) {
		leafTrace = msg.TraceID
		leafCtxTrace = TraceIDFromContext(ctx)
		return &Message{Payload: "leaf-done"}, nil
	}); err != nil {
		t.Fatal(err)
	}

	reply, err := bus.Request(context.Background(), "root", "mid", "start", nil, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	rootTrace := reply.TraceID
	if rootTrace == "" {
		t.Fatal("reply carries no trace")
	}
	if leafTrace != rootTrace || leafCtxTrace != rootTrace || nestedTrace != rootTrace {
		t.Fatalf("trace forked: root=%q leaf=%q leafCtx=%q nested=%q",
			rootTrace, leafTrace, leafCtxTrace, nestedTrace)
	}
}

// TestTraceRecordedOnUndeliverable locks the GAP-3 hole closure: a Request
// to an unregistered agent is dead-lettered WITH its trace (Send already
// did; Request returned silently before A-3).
func TestTraceRecordedOnUndeliverable(t *testing.T) {
	bus := NewBus()
	ctx := ContextWithTraceID(context.Background(), "trace-doomed-7")
	if _, err := bus.Request(ctx, "a", "ghost", "ping", nil, time.Second); err == nil {
		t.Fatal("want error for unregistered target")
	}
	letters := bus.DeadLetters().Snapshot()
	if len(letters) != 1 {
		t.Fatalf("want 1 dead letter, got %d", len(letters))
	}
	if letters[0].TraceID != "trace-doomed-7" {
		t.Fatalf("dead letter trace = %q, want the request trace", letters[0].TraceID)
	}
}

// TestTraceSharedAcrossBroadcastFanout locks one trace per fan-out: every
// delivery shares the broadcast's id, and a rejecting subscriber is recorded
// without stopping the fan-out.
func TestTraceSharedAcrossBroadcastFanout(t *testing.T) {
	bus := NewBus()
	var got []string
	okFn := func(_ context.Context, msg *Message) (*Message, error) {
		got = append(got, msg.TraceID)
		return nil, nil
	}
	if err := bus.Register("s1", okFn); err != nil {
		t.Fatal(err)
	}
	if err := bus.Register("s2", okFn); err != nil {
		t.Fatal(err)
	}
	if err := bus.Register("s3", func(context.Context, *Message) (*Message, error) {
		return nil, errors.New("reject")
	}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"s1", "s2", "s3"} {
		if err := bus.Subscribe(s, "news"); err != nil {
			t.Fatal(err)
		}
	}
	if n := bus.Broadcast(context.Background(), "a", "news", nil); n != 2 {
		t.Fatalf("delivered = %d, want 2 (rejector excluded, fan-out continues)", n)
	}
	if len(got) != 2 || got[0] != got[1] || got[0] == "" {
		t.Fatalf("fan-out deliveries must share one fresh trace, got %q", got)
	}
	letters := bus.DeadLetters().Snapshot()
	if len(letters) != 1 || letters[0].TraceID != got[0] {
		t.Fatalf("rejector must be recorded under the fan-out trace: %+v", letters)
	}
}

// TestReplyPreservesTraceIDAsync documents the async-Reply contract: the bus
// stamps direct returns, but an out-of-band Reply carries whatever the
// handler built — handlers copy req.TraceID to stay in the chain.
func TestReplyPreservesTraceIDAsync(t *testing.T) {
	bus := NewBus()
	if err := bus.Register("svc", func(_ context.Context, msg *Message) (*Message, error) {
		// Async style: reply later through Reply, carrying the trace over
		// explicitly (the documented contract).
		go func() {
			_ = bus.Reply(msg.CorrelationID, &Message{TraceID: msg.TraceID, Payload: "async-done"})
		}()
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	reply, err := bus.Request(context.Background(), "a", "svc", "work", nil, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if reply.TraceID == "" {
		t.Fatal("async reply lost the trace — handler must copy req.TraceID")
	}
}
