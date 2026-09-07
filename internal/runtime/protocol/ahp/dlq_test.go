package ahp

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestMessage creates a minimal AHPMessage for testing.
func newTestMessage(sessionID string) *AHPMessage {
	return &AHPMessage{
		MessageID: "msg-1",
		AgentID:   "agent-1",
		SessionID: sessionID,
	}
}

func TestDLQProcessor_AutoRetry(t *testing.T) {
	dlq := NewDLQ(100)
	processor := NewDLQProcessor(dlq)

	msg := newTestMessage("sess-1")
	dlq.Add(msg, errors.New("transient failure"), "test-reason")

	var attempts atomic.Int32
	processor.RegisterHandler("test-reason", func(_ context.Context, _ *DLQEntry) error {
		attempts.Add(1)
		// Fail the first attempt, succeed on the second.
		if attempts.Load() < 2 {
			return errors.New("still failing")
		}
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		processor.StartAutoRetry(ctx, 20*time.Millisecond)
		close(done)
	}()

	// Wait long enough for at least two ticks.
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	assert.GreaterOrEqual(t, attempts.Load(), int32(2), "handler should have been called at least twice")
	assert.Equal(t, 0, dlq.Size(), "entry should be removed after successful retry")
}

func TestDLQProcessor_MaxRetries(t *testing.T) {
	dlq := NewDLQ(100)
	processor := NewDLQProcessor(dlq)

	msg := newTestMessage("sess-2")
	entry := &DLQEntry{
		Message:    msg,
		Error:      errors.New("permanent failure"),
		Reason:     "test-reason",
		Timestamp:  time.Now(),
		Retries:    0,
		MaxRetries: 2,
	}

	// Manually add the entry to the DLQ.
	dlq.mu.Lock()
	dlq.messages = append(dlq.messages, entry)
	dlq.mu.Unlock()

	var attempts atomic.Int32
	processor.RegisterHandler("test-reason", func(_ context.Context, _ *DLQEntry) error {
		attempts.Add(1)
		return errors.New("always fails")
	})

	ctx := context.Background()

	// First two calls should process the entry.
	require.NoError(t, processor.Process(ctx))
	require.NoError(t, processor.Process(ctx))
	assert.Equal(t, int32(2), attempts.Load(), "entry should have been processed twice")
	assert.Equal(t, 1, dlq.Size(), "entry should still be in DLQ")

	// Third call should skip the entry (retries >= maxRetries).
	require.NoError(t, processor.Process(ctx))
	assert.Equal(t, int32(2), attempts.Load(), "entry should not be processed again after max retries")
	assert.Equal(t, 1, dlq.Size(), "entry should remain in DLQ")
}

// TestDLQProcessor_AddWithMaxRetries verifies the public AddWithMaxRetries
// API actually enforces the retry budget. Previously Add() never set
// MaxRetries, so the retry-exhaustion logic was only reachable by reaching into
// the queue internals — any entry added through the public API was retried
// indefinitely.
func TestDLQProcessor_AddWithMaxRetries(t *testing.T) {
	dlq := NewDLQ(100)
	processor := NewDLQProcessor(dlq)

	msg := newTestMessage("sess-3")
	dlq.AddWithMaxRetries(msg, errors.New("boom"), "bounded-reason", 2)

	var attempts atomic.Int32
	processor.RegisterHandler("bounded-reason", func(_ context.Context, _ *DLQEntry) error {
		attempts.Add(1)
		return errors.New("always fails")
	})

	ctx := context.Background()
	require.NoError(t, processor.Process(ctx))
	require.NoError(t, processor.Process(ctx))
	assert.Equal(t, int32(2), attempts.Load(), "entry processed up to its budget")
	assert.Equal(t, 1, dlq.Size(), "entry should still be in DLQ")

	require.NoError(t, processor.Process(ctx))
	assert.Equal(t, int32(2), attempts.Load(), "entry must not be processed after budget exhausted")
}

// TestDLQProcessor_AddBoundedStillUnlimited verifies Add() (no budget) keeps
// the documented unlimited-retry behaviour.
func TestDLQProcessor_AddBoundedStillUnlimited(t *testing.T) {
	dlq := NewDLQ(100)
	processor := NewDLQProcessor(dlq)

	dlq.Add(newTestMessage("sess-4"), errors.New("boom"), "unbounded-reason")

	var attempts atomic.Int32
	processor.RegisterHandler("unbounded-reason", func(_ context.Context, _ *DLQEntry) error {
		attempts.Add(1)
		return errors.New("always fails")
	})

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		require.NoError(t, processor.Process(ctx))
	}
	assert.Equal(t, int32(3), attempts.Load(), "Add() entries should retry without a cap")
	assert.Equal(t, 1, dlq.Size(), "entry should remain in DLQ")
}

func TestDLQEntry_MaxRetries_Unlimited(t *testing.T) {
	dlq := NewDLQ(100)
	processor := NewDLQProcessor(dlq)

	msg := newTestMessage("sess-3")
	entry := &DLQEntry{
		Message:    msg,
		Error:      errors.New("failure"),
		Reason:     "test-reason",
		Timestamp:  time.Now(),
		Retries:    0,
		MaxRetries: 0, // Zero means unlimited.
	}

	dlq.mu.Lock()
	dlq.messages = append(dlq.messages, entry)
	dlq.mu.Unlock()

	var attempts atomic.Int32
	processor.RegisterHandler("test-reason", func(_ context.Context, _ *DLQEntry) error {
		attempts.Add(1)
		return errors.New("always fails")
	})

	ctx := context.Background()

	// Process many times; entry should never be skipped.
	for i := 0; i < 10; i++ {
		require.NoError(t, processor.Process(ctx))
	}

	assert.Equal(t, int32(10), attempts.Load(), "entry with MaxRetries=0 should be retried indefinitely")
	assert.Equal(t, 1, dlq.Size(), "entry should remain in DLQ")
}
