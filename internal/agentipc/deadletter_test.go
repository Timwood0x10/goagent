package agentipc

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestDeadLetter_RecordsUndeliverableSend verifies GAP-3: a send to an
// unregistered agent is recorded in the dead-letter store.
func TestDeadLetter_RecordsUnreachable(t *testing.T) {
	b := NewBus()
	if err := b.Send(context.Background(), "a", "ghost", "t", "p"); !errors.Is(err, ErrAgentNotRegistered) {
		t.Fatalf("want ErrAgentNotRegistered, got %v", err)
	}
	if got := b.DeadLetters().Count(); got != 1 {
		t.Fatalf("dead letters = %d, want 1", got)
	}
	dl := b.DeadLetters().Snapshot()[0]
	if dl.To != "ghost" || dl.Topic != "t" || dl.Reason != ErrAgentNotRegistered.Error() {
		t.Fatalf("dead letter fields mismatch: %+v", dl)
	}
}

// TestDeadLetter_RecordsTimeout verifies a timed-out Request lands in the
// dead-letter store with the timeout reason.
func TestDeadLetter_RecordsTimeout(t *testing.T) {
	b := NewBus()
	_ = b.Register("slow", func(ctx context.Context, m *Message) (*Message, error) {
		time.Sleep(50 * time.Millisecond)
		return nil, nil // async reply never arrives
	})
	_, err := b.Request(context.Background(), "caller", "slow", "t", "p", 5*time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	if got := b.DeadLetters().Count(); got != 1 {
		t.Fatalf("dead letters = %d, want 1", got)
	}
}

// TestDeadLetter_CapacityEviction verifies the ring bound: oldest entries are
// evicted when capacity is exceeded (N8: unbounded-growth guard).
func TestDeadLetter_CapacityEviction(t *testing.T) {
	s := NewDeadLetterStore(3)
	for i := 0; i < 5; i++ {
		s.Record("a", "b", "t", i, "boom", "trace-9")
	}
	if got := s.Count(); got != 3 {
		t.Fatalf("count = %d, want 3 (ring bound)", got)
	}
	snap := s.Snapshot()
	if snap[0].ID != 3 {
		t.Fatalf("oldest retained id = %d, want 3 (1 and 2 evicted)", snap[0].ID)
	}
	if snap[0].TraceID != "trace-9" {
		t.Fatalf("trace id = %q, want it preserved on the record", snap[0].TraceID)
	}
}
