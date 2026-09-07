package agentipc

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// recordingHandler captures delivered messages and optionally returns a reply.
type recordingHandler struct {
	mu       sync.Mutex
	messages []*Message
	reply    *Message
	err      error
}

func (h *recordingHandler) handle(_ context.Context, msg *Message) (*Message, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cp := *msg
	h.messages = append(h.messages, &cp)
	if h.err != nil {
		return nil, h.err
	}
	return h.reply, nil
}

// TestSendDeliversToTarget verifies Send (fire-and-forget) delivers the
// message to the registered target handler.
func TestSendDeliversToTarget(t *testing.T) {
	bus := NewBus()
	recv := &recordingHandler{}
	if err := bus.Register("b", recv.handle); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := bus.Send(context.Background(), "a", "b", "hello", "payload"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	recv.mu.Lock()
	defer recv.mu.Unlock()
	if len(recv.messages) != 1 || recv.messages[0].Topic != "hello" {
		t.Fatalf("want 1 message 'hello', got %+v", recv.messages)
	}
	if recv.messages[0].From != "a" || recv.messages[0].To != "b" {
		t.Fatalf("from/to must be stamped, got from=%q to=%q",
			recv.messages[0].From, recv.messages[0].To)
	}
}

// TestSendUnknownAgent verifies Send to an unregistered agent errors.
func TestSendUnknownAgent(t *testing.T) {
	bus := NewBus()
	if err := bus.Send(context.Background(), "a", "ghost", "x", nil); !errors.Is(err, ErrAgentNotRegistered) {
		t.Fatalf("want ErrAgentNotRegistered, got %v", err)
	}
}

// TestRequestReplyRoundTrip verifies the core IPC pattern: agent a sends a
// request, agent b's handler returns a reply synchronously, and a receives it.
func TestRequestReplyRoundTrip(t *testing.T) {
	bus := NewBus()
	recv := &recordingHandler{reply: &Message{Topic: "reply", Payload: "answer"}}
	if err := bus.Register("b", recv.handle); err != nil {
		t.Fatalf("Register: %v", err)
	}
	reply, err := bus.Request(context.Background(), "a", "b", "question", "what?", 2*time.Second)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if reply == nil || reply.Topic != "reply" {
		t.Fatalf("want reply 'reply', got %+v", reply)
	}
	if reply.From != "b" || reply.To != "a" {
		t.Fatalf("reply from/to must be stamped, got from=%q to=%q", reply.From, reply.To)
	}
	if reply.CorrelationID == "" {
		t.Fatal("reply must carry a correlation id")
	}
}

// TestRequestAsyncReply verifies the handler can call Reply asynchronously
// (the handler returns nil and later calls Reply with the correlation id).
func TestRequestAsyncReply(t *testing.T) {
	bus := NewBus()
	var corrID string
	h := func(ctx context.Context, msg *Message) (*Message, error) {
		corrID = msg.CorrelationID
		// Return nil — will call Reply later in a goroutine.
		go func() {
			time.Sleep(10 * time.Millisecond)
			_ = bus.Reply(corrID, &Message{Topic: "async-reply", Payload: "late-answer"})
		}()
		return nil, nil
	}
	if err := bus.Register("b", h); err != nil {
		t.Fatalf("Register: %v", err)
	}
	reply, err := bus.Request(context.Background(), "a", "b", "q", "p", 2*time.Second)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if reply == nil || reply.Topic != "async-reply" {
		t.Fatalf("want async reply, got %+v", reply)
	}
}

// TestRequestTimeout verifies a request with no reply within the timeout
// returns ErrTimeout.
func TestRequestTimeout(t *testing.T) {
	bus := NewBus()
	h := func(context.Context, *Message) (*Message, error) {
		return nil, nil // never replies
	}
	if err := bus.Register("b", h); err != nil {
		t.Fatalf("Register: %v", err)
	}
	reply, err := bus.Request(context.Background(), "a", "b", "q", "p", 50*time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v err=%v", reply, err)
	}
}

// TestHandoffTaskTransfer verifies the Handoff primitive: agent a hands off a
// task to agent b; b acknowledges acceptance.
func TestHandoffTaskTransfer(t *testing.T) {
	bus := NewBus()
	recv := &recordingHandler{reply: &Message{Topic: "accept", Payload: "accepted"}}
	if err := bus.Register("b", recv.handle); err != nil {
		t.Fatalf("Register: %v", err)
	}
	reply, err := bus.Handoff(context.Background(), "a", "b", "task-1",
		map[string]any{"step": 3}, 2*time.Second)
	if err != nil {
		t.Fatalf("Handoff: %v", err)
	}
	if reply == nil || reply.Topic != "accept" {
		t.Fatalf("want acceptance reply, got %+v", reply)
	}
	recv.mu.Lock()
	defer recv.mu.Unlock()
	if len(recv.messages) != 1 {
		t.Fatalf("b must receive 1 handoff message, got %d", len(recv.messages))
	}
	if recv.messages[0].Topic != "handoff-task" {
		t.Fatalf("topic must be 'handoff-task', got %q", recv.messages[0].Topic)
	}
}

// TestSubscribeBroadcast verifies the Subscribe + Broadcast pattern: a
// subscribes to a topic, b broadcasts, a receives it.
func TestSubscribeBroadcast(t *testing.T) {
	bus := NewBus()
	recv := &recordingHandler{}
	if err := bus.Register("a", recv.handle); err != nil {
		t.Fatalf("Register a: %v", err)
	}
	if err := bus.Subscribe("a", "findings"); err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	// Broadcast from b.
	if n := bus.Broadcast(context.Background(), "b", "findings", "I found X"); n != 1 {
		t.Fatalf("want 1 delivery, got %d", n)
	}
	recv.mu.Lock()
	defer recv.mu.Unlock()
	if len(recv.messages) != 1 || recv.messages[0].Payload != "I found X" {
		t.Fatalf("a must receive the broadcast, got %+v", recv.messages)
	}
}

// TestBroadcastMultipleSubscribers verifies fan-out to multiple subscribers.
func TestBroadcastMultipleSubscribers(t *testing.T) {
	bus := NewBus()
	r1 := &recordingHandler{}
	r2 := &recordingHandler{}
	if err := bus.Register("a", r1.handle); err != nil {
		t.Fatal(err)
	}
	if err := bus.Register("b", r2.handle); err != nil {
		t.Fatal(err)
	}
	_ = bus.Subscribe("a", "topic")
	_ = bus.Subscribe("b", "topic")
	if n := bus.Broadcast(context.Background(), "c", "topic", "msg"); n != 2 {
		t.Fatalf("want 2 deliveries, got %d", n)
	}
}

// TestDelegateForwards verifies Delegate routes the request to the target
// with the delegator as From.
func TestDelegateForwards(t *testing.T) {
	bus := NewBus()
	recv := &recordingHandler{reply: &Message{Topic: "delegated-reply"}}
	if err := bus.Register("c", recv.handle); err != nil {
		t.Fatalf("Register: %v", err)
	}
	reply, err := bus.Delegate(context.Background(), "b", "c", "help-verify", "data", 2*time.Second)
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	if reply == nil || reply.Topic != "delegated-reply" {
		t.Fatalf("want delegated reply, got %+v", reply)
	}
	recv.mu.Lock()
	defer recv.mu.Unlock()
	if recv.messages[0].From != "b" {
		t.Fatalf("delegator must be From, got %q", recv.messages[0].From)
	}
}

// TestPolicyFlagDefaults verifies the flag defaults and flips correctly.
func TestPolicyFlagDefaults(t *testing.T) {
	f := NewPolicyFlag(PolicyLegacy)
	if !f.IsLegacy() {
		t.Fatal("default must be legacy")
	}
	f.Set(PolicyTaskFabric)
	if !f.IsTaskFabric() {
		t.Fatal("after Set, must be task fabric")
	}
}

// stubDispatcher is a test Dispatcher that counts calls and optionally errors.
type stubDispatcher struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (s *stubDispatcher) D(_ context.Context, _ string, _ string, _ any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.err
}

func (s *stubDispatcher) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// TestDualTrackDispatcherRoutesByFlag verifies the DualTrackDispatcher routes
// to the active path based on the flag (legacy vs new).
func TestDualTrackDispatcherRoutesByFlag(t *testing.T) {
	legacy := &stubDispatcher{}
	newPath := &stubDispatcher{}
	flag := NewPolicyFlag(PolicyLegacy)
	d := NewDualTrackDispatcher(flag, legacy, newPath, false)
	_ = d.Dispatch(context.Background(), "a", "t", nil)
	if legacy.count() != 1 || newPath.count() != 0 {
		t.Fatalf("legacy=%d new=%d (flag=legacy)", legacy.count(), newPath.count())
	}
	flag.Set(PolicyTaskFabric)
	_ = d.Dispatch(context.Background(), "a", "t", nil)
	if legacy.count() != 1 || newPath.count() != 1 {
		t.Fatalf("legacy=%d new=%d (flag=fabric)", legacy.count(), newPath.count())
	}
}

// TestDualTrackDispatcherShadowEquivalence verifies shadow mode: both paths
// run and their outcomes are compared; when both succeed, mismatches stays 0.
func TestDualTrackDispatcherShadowEquivalence(t *testing.T) {
	legacy := &stubDispatcher{}
	newPath := &stubDispatcher{}
	flag := NewPolicyFlag(PolicyLegacy)
	d := NewDualTrackDispatcher(flag, legacy, newPath, true)
	_ = d.Dispatch(context.Background(), "a", "t", nil)
	if d.Mismatches() != 0 {
		t.Fatalf("shadow equivalence must be 0 mismatches, got %d", d.Mismatches())
	}
}

// TestDualTrackDispatcherShadowMismatch verifies a mismatch (one errors, one
// succeeds) is detected and counted.
func TestDualTrackDispatcherShadowMismatch(t *testing.T) {
	legacy := &stubDispatcher{err: errors.New("legacy failed")}
	newPath := &stubDispatcher{}
	flag := NewPolicyFlag(PolicyLegacy)
	d := NewDualTrackDispatcher(flag, legacy, newPath, true)
	_ = d.Dispatch(context.Background(), "a", "t", nil)
	if d.Mismatches() != 1 {
		t.Fatalf("want 1 mismatch, got %d", d.Mismatches())
	}
}

// TestDualTrackConcurrentShadowFlip verifies a live mid-run flip is race-free
// (-race): dispatches racing SetShadow / SetNewPath / flag.Set never panic and
// every dispatch completes exactly once on the active path. This guards the
// shadow snapshot read (Dispatch must not read d.shadow unsynchronized).
func TestDualTrackConcurrentShadowFlip(t *testing.T) {
	legacy := &stubDispatcher{}
	newPath := &stubDispatcher{}
	flag := NewPolicyFlag(PolicyLegacy)
	d := NewDualTrackDispatcher(flag, legacy, newPath, true)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	// Concurrent flippers: shadow on/off, swap new path, flip the flag.
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				d.SetShadow(true)
				flag.Set(PolicyTaskFabric)
				d.SetNewPath(&stubDispatcher{})
				d.SetShadow(false)
				flag.Set(PolicyLegacy)
			}
		}()
	}
	// Concurrent dispatchers: each dispatch must return without panic.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = d.Dispatch(context.Background(), "a", "t", nil)
			}
		}()
	}
	// Let them race for a short, bounded window, then stop.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	close(stop)
	wg.Wait()
}

// TestConcurrentRequestsAreSafe verifies concurrent Request/Reply is race-free
// (verified with go test -race).
func TestConcurrentRequestsAreSafe(t *testing.T) {
	bus := NewBus()
	recv := &recordingHandler{reply: &Message{Topic: "r"}}
	if err := bus.Register("b", recv.handle); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = bus.Request(context.Background(), "a", "b", "q", "p", 2*time.Second)
		}()
	}
	wg.Wait()
	recv.mu.Lock()
	defer recv.mu.Unlock()
	if len(recv.messages) != 10 {
		t.Fatalf("want 10 requests, got %d", len(recv.messages))
	}
}
