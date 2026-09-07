package agentipc

import (
	"context"
	"testing"
	"time"
)

// echoHandler replies synchronously, simulating a peer responding to a
// request.
func echoHandler(ctx context.Context, msg *Message) (*Message, error) {
	return &Message{ID: msg.ID, From: msg.To, To: msg.From, Topic: "reply", Payload: msg.Payload}, nil
}

// BenchmarkBus_Send measures the fire-and-forget peer send.
func BenchmarkBus_Send(b *testing.B) {
	bus := NewBus()
	_ = bus.Register("b", echoHandler)
	ctx := context.Background()
	msg := &Message{From: "a", To: "b", Topic: "t", Payload: "p"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = bus.Send(ctx, "a", "b", "t", msg)
	}
}

// BenchmarkBus_RequestReply measures the synchronous request/reply round
// trip with correlation-id matching.
func BenchmarkBus_RequestReply(b *testing.B) {
	bus := NewBus()
	_ = bus.Register("b", echoHandler)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = bus.Request(ctx, "a", "b", "t", "payload", time.Second)
	}
}

// BenchmarkBus_Broadcast measures fan-out to N subscribers.
func BenchmarkBus_Broadcast(b *testing.B) {
	bus := NewBus()
	for i := 0; i < 10; i++ {
		_ = bus.Register("sub", echoHandler)
		_ = bus.Subscribe("sub", "topic")
	}
	ctx := context.Background()
	msg := &Message{From: "a", To: "sub", Topic: "topic"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = bus.Broadcast(ctx, "a", "topic", msg)
	}
}

// BenchmarkDualTrackDispatch measures the kernel dispatch overhead
// (flag read + active path + optional shadow comparison).
func BenchmarkDualTrackDispatch(b *testing.B) {
	flag := NewPolicyFlag(PolicyTaskFabric)
	legacy := &stubDispatcher{}
	newPath := &stubDispatcher{}
	d := NewDualTrackDispatcher(flag, legacy, newPath, false) // shadow off (live path)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = d.Dispatch(ctx, "a", "t", nil)
	}
}
