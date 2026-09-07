// package integration provides end-to-end integration tests for the AHP ares_protocol.
package ares_integration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/runtime/protocol/ahp"
)

// TestProtocolSendMessageReceiveMessage verifies the full round-trip:
// SendMessage enqueues a message, ReceiveMessage dequeues it.
func TestProtocolSendMessageReceiveMessage(t *testing.T) {
	proto := ahp.NewProtocol(ahp.DefaultProtocolConfig())
	defer proto.Close()

	ctx := context.Background()
	msg := ahp.NewTaskMessage("leader", "worker-1", "task-1", "session-1", map[string]any{
		"content": "hello",
	})

	require.NoError(t, proto.SendMessage(ctx, msg))

	received, err := proto.ReceiveMessage(ctx, "worker-1")
	require.NoError(t, err)
	require.NotNil(t, received)
	assert.Equal(t, msg.MessageID, received.MessageID)
	assert.Equal(t, ahp.AHPMethodTask, received.Method)
	assert.Equal(t, "leader", received.AgentID)
	assert.Equal(t, "worker-1", received.TargetAgent)
	assert.Equal(t, "task-1", received.TaskID)
}

// TestProtocolDLQMessageGoesToDLQ verifies that when the target queue is full,
// the message is routed to the dead letter queue.
func TestProtocolDLQMessageGoesToDLQ(t *testing.T) {
	config := &ahp.ProtocolConfig{
		QueueSize:       1,
		HeartbeatConfig: ahp.DefaultHeartbeatConfig(),
		EnableDLQ:       true,
		DLQSize:         100,
	}
	proto := ahp.NewProtocol(config)
	defer proto.Close()

	ctx := context.Background()

	// Fill the queue to capacity.
	msg1 := ahp.NewTaskMessage("leader", "agent-full", "task-1", "session-1", nil)
	require.NoError(t, proto.SendMessage(ctx, msg1))

	// Second message should fail and go to DLQ.
	msg2 := ahp.NewTaskMessage("leader", "agent-full", "task-2", "session-1", nil)
	err := proto.SendMessage(ctx, msg2)
	require.Error(t, err)

	dlq := proto.GetDLQ()
	require.NotNil(t, dlq)
	assert.GreaterOrEqual(t, dlq.Size(), 1, "expected at least one entry in DLQ")
}

// TestProtocolDLQProcessorRetries verifies that the DLQProcessor can process
// entries and track stats.
func TestProtocolDLQProcessorRetries(t *testing.T) {
	dlq := ahp.NewDLQ(100)
	processor := ahp.NewDLQProcessor(dlq)

	// Register a handler that always succeeds.
	var handled int
	var mu sync.Mutex
	processor.RegisterHandler("queue_full", func(_ context.Context, _ *ahp.DLQEntry) error {
		mu.Lock()
		handled++
		mu.Unlock()
		return nil
	})

	// Add entries to the DLQ.
	for i := 0; i < 3; i++ {
		msg := ahp.NewTaskMessage("leader", "agent", "task-"+string(rune('a'+i)), "session-1", nil)
		dlq.Add(msg, assert.AnError, "queue_full")
	}

	require.NoError(t, processor.Process(context.Background()))

	mu.Lock()
	assert.Equal(t, 3, handled, "expected all 3 entries to be handled")
	mu.Unlock()

	processed, failed := processor.Stats()
	assert.Equal(t, 3, processed)
	assert.Equal(t, 0, failed)
}

// TestProtocolHeartbeatMonitorRegisterRecordTimeout verifies the full heartbeat
// lifecycle: register -> record -> timeout detection -> callback fires.
func TestProtocolHeartbeatMonitorRegisterRecordTimeout(t *testing.T) {
	config := &ahp.HeartbeatConfig{
		Interval:  50 * time.Millisecond,
		Timeout:   100 * time.Millisecond,
		MaxMissed: 1,
	}
	monitor := ahp.NewHeartbeatMonitor(config)

	var timedOut []string
	var mu sync.Mutex
	monitor.RegisterCallback(func(agentID string) {
		mu.Lock()
		timedOut = append(timedOut, agentID)
		mu.Unlock()
	})

	// Register agent via heartbeat.
	monitor.RecordHeartbeat("agent-1")
	status, ok := monitor.GetStatus("agent-1")
	require.True(t, ok)
	assert.Equal(t, "ready", string(status))

	// Wait for timeout.
	time.Sleep(200 * time.Millisecond)
	offline := monitor.CheckTimeouts()
	assert.Contains(t, offline, "agent-1")

	mu.Lock()
	assert.Contains(t, timedOut, "agent-1")
	mu.Unlock()
}

// TestProtocolHeartbeatSenderStartStop verifies that HeartbeatSender starts,
// sends heartbeats to the queue, and stops cleanly.
func TestProtocolHeartbeatSenderStartStop(t *testing.T) {
	queue := ahp.NewMessageQueue("hb-agent", &ahp.QueueOptions{MaxSize: 100})
	sender := ahp.NewHeartbeatSender("hb-agent", 50*time.Millisecond, queue)

	require.NoError(t, sender.Validate())

	ctx := context.Background()
	sender.Start(ctx)

	// Wait for at least one heartbeat to be sent.
	time.Sleep(150 * time.Millisecond)

	sender.Stop()

	// The queue should contain at least one heartbeat message.
	assert.False(t, queue.IsEmpty(), "expected heartbeat messages in queue")
}

// TestProtocolQueueConcurrentEnqueueDequeue verifies that the MessageQueue
// handles concurrent producers and consumers without data races.
func TestProtocolQueueConcurrentEnqueueDequeue(t *testing.T) {
	queue := ahp.NewMessageQueue("concurrent-agent", &ahp.QueueOptions{MaxSize: 500})

	ctx := context.Background()
	const numProducers = 10
	const msgsPerProducer = 20
	totalMessages := numProducers * msgsPerProducer

	var wg sync.WaitGroup
	var enqueued int64
	var mu sync.Mutex

	// Producers: enqueue messages concurrently.
	for i := 0; i < numProducers; i++ {
		wg.Add(1)
		go func(producerID int) {
			defer wg.Done()
			for j := 0; j < msgsPerProducer; j++ {
				msg := ahp.NewTaskMessage("leader", "concurrent-agent", "task", "session", nil)
				if err := queue.Enqueue(ctx, msg); err == nil {
					mu.Lock()
					enqueued++
					mu.Unlock()
				}
			}
		}(i)
	}

	wg.Wait()

	// Drain the queue and count received messages.
	received := 0
	for !queue.IsEmpty() {
		_, err := queue.DequeueWithTimeout(10 * time.Millisecond)
		if err != nil {
			break
		}
		received++
	}

	mu.Lock()
	assert.Equal(t, int64(totalMessages), enqueued, "all messages should be enqueued")
	mu.Unlock()
	assert.Equal(t, totalMessages, received, "all messages should be dequeued")
}

// TestProtocolCloseCleansUpQueues verifies that Protocol.Close() removes all
// registered agent queues.
func TestProtocolCloseCleansUpQueues(t *testing.T) {
	proto := ahp.NewProtocol(ahp.DefaultProtocolConfig())

	ctx := context.Background()

	// Send messages to create queues for multiple agents.
	for _, agentID := range []string{"agent-a", "agent-b", "agent-c"} {
		msg := ahp.NewTaskMessage("leader", agentID, "task-1", "session-1", nil)
		require.NoError(t, proto.SendMessage(ctx, msg))
	}

	stats := proto.Stats()
	assert.Equal(t, 3, stats.TotalQueues, "expected 3 queues before close")

	proto.Close()

	stats = proto.Stats()
	assert.Equal(t, 0, stats.TotalQueues, "expected 0 queues after close")
}

// TestProtocolCodecRoundTrip verifies that the JSON codec can encode and
// decode an AHPMessage preserving all fields.
func TestProtocolCodecRoundTrip(t *testing.T) {
	proto := ahp.NewProtocol(ahp.DefaultProtocolConfig())
	defer proto.Close()

	original := ahp.NewTaskMessage("agent-src", "agent-dst", "task-42", "session-xyz", map[string]any{
		"key1": "value1",
		"key2": float64(42),
	})

	encoded, err := proto.EncodeMessage(original)
	require.NoError(t, err)
	require.NotEmpty(t, encoded)

	decoded, err := proto.DecodeMessage(encoded)
	require.NoError(t, err)
	require.NotNil(t, decoded)

	assert.Equal(t, original.MessageID, decoded.MessageID)
	assert.Equal(t, original.Method, decoded.Method)
	assert.Equal(t, original.AgentID, decoded.AgentID)
	assert.Equal(t, original.TargetAgent, decoded.TargetAgent)
	assert.Equal(t, original.TaskID, decoded.TaskID)
	assert.Equal(t, original.SessionID, decoded.SessionID)
	assert.Equal(t, "value1", decoded.Payload["key1"])
}

// TestProtocolSendMessageNilMessage verifies that sending a nil message
// returns an error.
func TestProtocolSendMessageNilMessage(t *testing.T) {
	proto := ahp.NewProtocol(ahp.DefaultProtocolConfig())
	defer proto.Close()

	err := proto.SendMessage(context.Background(), nil)
	require.Error(t, err, "expected error when sending nil message")
}

// TestProtocolReceiveFromNonExistentQueue verifies that receiving from a queue
// that was never created returns an error.
func TestProtocolReceiveFromNonExistentQueue(t *testing.T) {
	proto := ahp.NewProtocol(ahp.DefaultProtocolConfig())
	defer proto.Close()

	_, err := proto.ReceiveMessage(context.Background(), "non-existent-agent")
	require.Error(t, err, "expected error when receiving from non-existent queue")
}
