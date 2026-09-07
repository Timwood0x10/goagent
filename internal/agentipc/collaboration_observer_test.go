package agentipc

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/Timwood0x10/ares/internal/feedback"
)

// recordingObserver captures collaboration receipts for assertions.
type recordingObserver struct {
	got []feedback.CollaborationOutcome
}

func (o *recordingObserver) OnCollaboration(out feedback.CollaborationOutcome) {
	o.got = append(o.got, out)
}

// TestRequest_ObservesCollaborationOutcomes is the collaboration-receipts acceptance: every
// collaboration attempt with an observable receipt produces exactly one record,
// and the outcome distinguishes the four cases evolution needs to tell apart
// (answered / handler failed / no reply in time / target does not exist).
// Before this, the bus recorded only failures into the dead-letter store, so
// success rate was unknowable.
func TestRequest_ObservesCollaborationOutcomes(t *testing.T) {
	handlerErr := errors.New("handler blew up")
	tests := []struct {
		name    string
		target  string
		handler Handler
		timeout time.Duration
		want    feedback.Outcome
	}{
		{
			name:   "reply received",
			target: "responder",
			handler: func(_ context.Context, msg *Message) (*Message, error) {
				return &Message{Topic: msg.Topic, Payload: "answer"}, nil
			},
			timeout: time.Second,
			want:    feedback.OutcomeSuccess,
		},
		{
			name:    "handler error",
			target:  "responder",
			handler: func(context.Context, *Message) (*Message, error) { return nil, handlerErr },
			timeout: time.Second,
			want:    feedback.OutcomeFailure,
		},
		{
			name:   "no reply before deadline",
			target: "responder",
			// Returning (nil, nil) means "I will reply later via Reply" — the
			// request then times out, which is a real collaboration outcome.
			handler: func(context.Context, *Message) (*Message, error) { return nil, nil },
			timeout: 30 * time.Millisecond,
			want:    feedback.OutcomeTimeout,
		},
		{
			name:    "target not registered",
			target:  "ghost",
			handler: nil,
			timeout: time.Second,
			want:    feedback.OutcomeNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obs := &recordingObserver{}
			bus := NewBus().WithCollaborationObserver(obs)
			if tc.handler != nil {
				if err := bus.Register("responder", tc.handler); err != nil {
					t.Fatalf("register: %v", err)
				}
			}

			_, _ = bus.Request(context.Background(), "asker", tc.target, "verify", "payload", tc.timeout)

			if len(obs.got) != 1 {
				t.Fatalf("want exactly 1 collaboration record, got %d", len(obs.got))
			}
			rec := obs.got[0]
			if rec.Outcome != tc.want {
				t.Errorf("outcome = %q, want %q", rec.Outcome, tc.want)
			}
			if rec.Kind != feedback.CollabRequest {
				t.Errorf("kind = %q, want %q", rec.Kind, feedback.CollabRequest)
			}
			if rec.Initiator != "asker" || rec.Target != tc.target || rec.Topic != "verify" {
				t.Errorf("attribution = (%s → %s, %s), want (asker → %s, verify)",
					rec.Initiator, rec.Target, rec.Topic, tc.target)
			}
			// Latency must be a real measurement, not a zero placeholder: the
			// success case rides the fast path, so only require non-negative
			// there, but a timeout must have waited at least its deadline.
			if rec.Latency < 0 {
				t.Errorf("latency = %v, want >= 0", rec.Latency)
			}
			if tc.want == feedback.OutcomeTimeout && rec.Latency < tc.timeout {
				t.Errorf("timeout latency = %v, want >= %v", rec.Latency, tc.timeout)
			}
		})
	}
}

// TestRequest_CallerCancellationIsNotScored locks the deliberate exclusion: a
// caller that abandons the request teaches nothing about the target, so it must
// NOT become a collaboration record. Scoring it would let an unrelated upstream
// deadline degrade whichever agent happened to be asked.
func TestRequest_CallerCancellationIsNotScored(t *testing.T) {
	obs := &recordingObserver{}
	bus := NewBus().WithCollaborationObserver(obs)
	blocked := make(chan struct{})
	defer close(blocked)
	if err := bus.Register("slow", func(ctx context.Context, _ *Message) (*Message, error) {
		select {
		case <-blocked:
		case <-ctx.Done():
		}
		return nil, nil
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	if _, err := bus.Request(ctx, "asker", "slow", "verify", nil, time.Minute); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}

	if len(obs.got) != 0 {
		t.Fatalf("caller cancellation must produce no collaboration record, got %+v", obs.got)
	}
}

// TestSend_ObservesDeliveryReceipts is the production-path half of the
// collaboration channel.
// Send — not Request — is what the peer bridge actually uses
// (cmd/ares/evolution_ipc.go routes every peer message through it), so without
// this the collaboration channel would record nothing in a real deployment while
// every unit test passed. A fire-and-forget send has no answer to judge, but it
// does tell the initiator whether the agent it addressed exists and accepted the
// message.
func TestSend_ObservesDeliveryReceipts(t *testing.T) {
	handlerErr := errors.New("handler rejected")
	tests := []struct {
		name    string
		target  string
		handler Handler
		want    feedback.Outcome
	}{
		{
			name:    "accepted",
			target:  "peer",
			handler: func(context.Context, *Message) (*Message, error) { return nil, nil },
			want:    feedback.OutcomeSuccess,
		},
		{
			name:    "handler rejected",
			target:  "peer",
			handler: func(context.Context, *Message) (*Message, error) { return nil, handlerErr },
			want:    feedback.OutcomeFailure,
		},
		{
			name:    "target not registered",
			target:  "ghost",
			handler: nil,
			want:    feedback.OutcomeNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obs := &recordingObserver{}
			bus := NewBus().WithCollaborationObserver(obs)
			if tc.handler != nil {
				if err := bus.Register("peer", tc.handler); err != nil {
					t.Fatalf("register: %v", err)
				}
			}

			_ = bus.Send(context.Background(), "sender", tc.target, "peer", "payload")

			if len(obs.got) != 1 {
				t.Fatalf("want exactly 1 collaboration record, got %d", len(obs.got))
			}
			rec := obs.got[0]
			if rec.Outcome != tc.want {
				t.Errorf("outcome = %q, want %q", rec.Outcome, tc.want)
			}
			// The kind must mark this as a delivery receipt, not an answer: a
			// consumer that cannot tell them apart would report "collaboration
			// success rate" over two different measurements.
			if rec.Kind != feedback.CollabSend {
				t.Errorf("kind = %q, want %q", rec.Kind, feedback.CollabSend)
			}
			if rec.Initiator != "sender" || rec.Target != tc.target {
				t.Errorf("attribution = (%s → %s), want (sender → %s)",
					rec.Initiator, rec.Target, tc.target)
			}
		})
	}
}

// TestSend_PanickingHandlerWritesNoFalseSuccess locks the interaction between
// the two fixes in this change. Send invokes the handler SYNCHRONOUSLY, so a
// panic there propagates to the caller (that is Send's contract — it is the
// caller's own goroutine, and the caller can recover). What must not happen is
// the observer recording a success on the way out: the deferred record runs
// during the panic unwind, and if the outcome had been initialized to success,
// the single clearest collaboration failure would be written as a win.
func TestSend_PanickingHandlerWritesNoFalseSuccess(t *testing.T) {
	obs := &recordingObserver{}
	bus := NewBus().WithCollaborationObserver(obs)
	if err := bus.Register("panicky", func(context.Context, *Message) (*Message, error) {
		panic("handler exploded")
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("Send must propagate a synchronous handler panic to its caller")
			}
		}()
		_ = bus.Send(context.Background(), "sender", "panicky", "peer", nil)
	}()

	for _, rec := range obs.got {
		if rec.Outcome == feedback.OutcomeSuccess {
			t.Fatalf("a panicking handler was recorded as a collaboration success: %+v", rec)
		}
	}
}

// TestRequest_HandlerPanicIsContained locks the panic-containment contract.
// Before the fix, a panic in a registered handler ran on the bus's
// own goroutine with no recover boundary, so one buggy or third-party handler
// terminated the entire process. The test asserts all three properties of
// containment: the process survives, the caller gets ErrHandlerPanic, and other
// agents keep working afterwards.
func TestRequest_HandlerPanicIsContained(t *testing.T) {
	bus := NewBus()
	if err := bus.Register("panicky", func(context.Context, *Message) (*Message, error) {
		panic("handler exploded")
	}); err != nil {
		t.Fatalf("register panicky: %v", err)
	}
	if err := bus.Register("healthy", func(_ context.Context, msg *Message) (*Message, error) {
		return &Message{Topic: msg.Topic, Payload: "fine"}, nil
	}); err != nil {
		t.Fatalf("register healthy: %v", err)
	}

	// If containment is missing, this call takes the test binary down.
	reply, err := bus.Request(context.Background(), "asker", "panicky", "verify", nil, time.Second)
	if !errors.Is(err, ErrHandlerPanic) {
		t.Fatalf("err = %v, want ErrHandlerPanic", err)
	}
	if reply != nil {
		t.Errorf("reply = %+v, want nil", reply)
	}

	// The bus must still serve other agents: a contained panic fails ONE
	// request, which is the same blast radius as a returned error.
	reply, err = bus.Request(context.Background(), "asker", "healthy", "verify", nil, time.Second)
	if err != nil {
		t.Fatalf("healthy request after panic: %v", err)
	}
	if reply == nil || reply.Payload != "fine" {
		t.Errorf("reply = %+v, want payload fine", reply)
	}
}

// TestRequest_HandlerPanicDoesNotBurnTheTimeout locks the wake-up path chosen
// for the fix. A panicking handler will never call Reply, so if the recover only
// logged, the caller would block for the full timeout. Routing the panic through
// the same sentinel-nil-reply protocol as a handler error turns a slow failure
// into a fast one — asserted here with a deliberately long timeout that the call
// must NOT wait for.
func TestRequest_HandlerPanicDoesNotBurnTheTimeout(t *testing.T) {
	bus := NewBus()
	if err := bus.Register("panicky", func(context.Context, *Message) (*Message, error) {
		panic("handler exploded")
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	const generousTimeout = 10 * time.Second
	started := time.Now()
	_, err := bus.Request(context.Background(), "asker", "panicky", "verify", nil, generousTimeout)
	elapsed := time.Since(started)

	if !errors.Is(err, ErrHandlerPanic) {
		t.Fatalf("err = %v, want ErrHandlerPanic", err)
	}
	if elapsed >= generousTimeout {
		t.Errorf("waited %v for a panicking handler; the caller must wake immediately", elapsed)
	}
}

// TestRequest_HandlerPanicIsObservedAsFailure ties panic containment to the
// collaboration channel: a panicking peer is the strongest possible evidence that asking it
// was a bad choice, so the collaboration record must exist and must not be a
// success.
func TestRequest_HandlerPanicIsObservedAsFailure(t *testing.T) {
	obs := &recordingObserver{}
	bus := NewBus().WithCollaborationObserver(obs)
	if err := bus.Register("panicky", func(context.Context, *Message) (*Message, error) {
		panic("handler exploded")
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	if _, err := bus.Request(context.Background(), "asker", "panicky", "verify", nil, time.Second); !errors.Is(err, ErrHandlerPanic) {
		t.Fatalf("want ErrHandlerPanic, got %v", err)
	}

	if len(obs.got) != 1 {
		t.Fatalf("want 1 collaboration record, got %d", len(obs.got))
	}
	if got := obs.got[0].Outcome; got != feedback.OutcomeFailure {
		t.Errorf("outcome = %q, want failure", got)
	}
}

// TestRequest_HandlerPanicWithLoggerIsReported locks the observability half:
// containment must not be silent. The bus prints nothing on its own,
// so the panic reaches operators only through the injected
// logger — and a fix that swallowed the panic entirely would leave the failure
// undiagnosable.
func TestRequest_HandlerPanicWithLoggerIsReported(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))
	bus := NewBus().WithLogger(logger)
	if err := bus.Register("panicky", func(context.Context, *Message) (*Message, error) {
		panic("handler exploded")
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	if _, err := bus.Request(context.Background(), "asker", "panicky", "verify", nil, time.Second); !errors.Is(err, ErrHandlerPanic) {
		t.Fatalf("want ErrHandlerPanic, got %v", err)
	}

	logged := buf.String()
	if !strings.Contains(logged, "handler panicked") {
		t.Errorf("log missing the panic report: %q", logged)
	}
	// Context keys, not a concatenated message: an operator
	// needs to know WHICH collaboration died.
	for _, key := range []string{"from=asker", "to=panicky", "topic=verify"} {
		if !strings.Contains(logged, key) {
			t.Errorf("log missing context %q: %q", key, logged)
		}
	}
}

// TestRequest_NoObserverIsUnchanged locks the default: without an observer the
// bus behaves exactly as before the channel existed (this is what keeps `make gate`
// unchanged when the channel is not armed).
func TestRequest_NoObserverIsUnchanged(t *testing.T) {
	bus := NewBus()
	if err := bus.Register("responder", func(_ context.Context, msg *Message) (*Message, error) {
		return &Message{Topic: msg.Topic, Payload: "ok"}, nil
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	reply, err := bus.Request(context.Background(), "asker", "responder", "verify", nil, time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if reply == nil || reply.Payload != "ok" {
		t.Fatalf("reply = %+v, want payload ok", reply)
	}
}

// TestHandoffAndDelegate_AreObserved locks that the collaboration primitives
// built on Request inherit the measurement. Handoff and Delegate are how task
// ownership actually moves between agents, so a measurement that covered only
// raw Request would miss the collaboration that matters most.
func TestHandoffAndDelegate_AreObserved(t *testing.T) {
	obs := &recordingObserver{}
	bus := NewBus().WithCollaborationObserver(obs)
	if err := bus.Register("worker", func(_ context.Context, msg *Message) (*Message, error) {
		return &Message{Topic: msg.Topic, Payload: "accepted"}, nil
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	if _, err := bus.Handoff(context.Background(), "lead", "worker", "task-1", nil, time.Second); err != nil {
		t.Fatalf("handoff: %v", err)
	}
	if _, err := bus.Delegate(context.Background(), "lead", "worker", "help", nil, time.Second); err != nil {
		t.Fatalf("delegate: %v", err)
	}

	if len(obs.got) != 2 {
		t.Fatalf("want 2 records (handoff + delegate), got %d", len(obs.got))
	}
	if obs.got[0].Topic != "handoff-task" {
		t.Errorf("handoff topic = %q, want handoff-task", obs.got[0].Topic)
	}
	if obs.got[1].Topic != "help" {
		t.Errorf("delegate topic = %q, want help", obs.got[1].Topic)
	}
	for i, rec := range obs.got {
		if rec.Outcome != feedback.OutcomeSuccess {
			t.Errorf("record %d outcome = %q, want success", i, rec.Outcome)
		}
	}
}
