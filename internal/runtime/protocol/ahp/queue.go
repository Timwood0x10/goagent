package ahp

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Timwood0x10/ares/internal/errors"
)

// MessageQueue represents an in-memory message queue for agent communication.
type MessageQueue struct {
	messages     chan *AHPMessage
	agentID      string
	opts         *QueueOptions
	backupBuffer []*AHPMessage
	backupMu     sync.Mutex
	closed       atomic.Bool
	closeOnce    sync.Once
}

// QueueOptions holds the configuration options for the message queue.
type QueueOptions struct {
	MaxSize    int
	MaxWorkers int
	Timeout    time.Duration
}

// DefaultQueueOptions returns the default queue options.
func DefaultQueueOptions() *QueueOptions {
	return &QueueOptions{
		MaxSize:    1000,
		MaxWorkers: 10,
		Timeout:    30 * time.Second,
	}
}

// NewMessageQueue creates a new MessageQueue.
func NewMessageQueue(agentID string, opts *QueueOptions) *MessageQueue {
	if opts == nil {
		opts = DefaultQueueOptions()
	}
	return &MessageQueue{
		messages:     make(chan *AHPMessage, opts.MaxSize),
		agentID:      agentID,
		opts:         opts,
		backupBuffer: make([]*AHPMessage, 0),
	}
}

// Enqueue adds a message to the queue.
// A recover guard protects against the race where Close() runs between
// the closed check and the channel send.
func (q *MessageQueue) Enqueue(ctx context.Context, msg *AHPMessage) (retErr error) {
	if q.closed.Load() {
		return errors.ErrQueueClosed
	}

	// Guard against send-on-closed-channel panic when Close() races.
	defer func() {
		if r := recover(); r != nil {
			retErr = errors.ErrQueueClosed
		}
	}()

	// Non-blocking send: the default branch makes this non-blocking,
	// so ctx.Done() would never be selected even if present.
	select {
	case q.messages <- msg:
		return nil
	default:
		return errors.ErrQueueFull
	}
}

// Dequeue removes and returns a message from the queue.
// Messages in the backup buffer are prioritized over the main queue.
func (q *MessageQueue) Dequeue(ctx context.Context) (*AHPMessage, error) {
	q.backupMu.Lock()
	if len(q.backupBuffer) > 0 {
		msg := q.backupBuffer[0]
		q.backupBuffer = q.backupBuffer[1:]
		q.backupMu.Unlock()
		return msg, nil
	}
	q.backupMu.Unlock()

	select {
	case msg, ok := <-q.messages:
		if !ok {
			return nil, errors.ErrQueueClosed
		}
		return msg, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// DequeueWithTimeout removes and returns a message with timeout.
// Messages in the backup buffer are prioritized over the main queue.
func (q *MessageQueue) DequeueWithTimeout(timeout time.Duration) (*AHPMessage, error) {
	q.backupMu.Lock()
	if len(q.backupBuffer) > 0 {
		msg := q.backupBuffer[0]
		q.backupBuffer = q.backupBuffer[1:]
		q.backupMu.Unlock()
		return msg, nil
	}
	q.backupMu.Unlock()

	qTimer := time.NewTimer(timeout)
	select {
	case msg, ok := <-q.messages:
		if !ok {
			qTimer.Stop()
			return nil, errors.ErrQueueClosed
		}
		qTimer.Stop()
		return msg, nil
	case <-qTimer.C:
		return nil, errors.ErrQueueEmpty
	}
}

// Peek returns the first message without removing it.
// Returns nil if the queue is empty or closed.
//
// This method holds backupMu across the entire operation so that
// the read-from-channel and write-back sequence is atomic with respect
// to concurrent Dequeue / Peek callers.
//
// Returns:
//   - (*AHPMessage, nil): successfully peeked message
//   - (nil, nil): queue is empty
//   - (nil, ErrQueueClosed): queue has been closed
func (q *MessageQueue) Peek() (*AHPMessage, error) {
	if q.closed.Load() {
		return nil, errors.ErrQueueClosed
	}

	q.backupMu.Lock()
	defer q.backupMu.Unlock()

	if len(q.backupBuffer) > 0 {
		return q.backupBuffer[0], nil
	}

	select {
	case msg, ok := <-q.messages:
		if !ok {
			return nil, errors.ErrQueueClosed
		}
		// Try to put the message back immediately.
		select {
		case q.messages <- msg:
			return msg, nil
		default:
			// Channel full; store in backup buffer.
			q.backupBuffer = append(q.backupBuffer, msg)
			return msg, nil
		}
	default:
		return nil, errors.ErrQueueEmpty
	}
}

// Size returns the current number of messages in the queue (including backup buffer).
func (q *MessageQueue) Size() int {
	q.backupMu.Lock()
	backupSize := len(q.backupBuffer)
	q.backupMu.Unlock()
	return len(q.messages) + backupSize
}

// Capacity returns the maximum capacity of the queue.
func (q *MessageQueue) Capacity() int {
	return q.opts.MaxSize
}

// IsEmpty checks if the queue is empty (including backup buffer).
func (q *MessageQueue) IsEmpty() bool {
	q.backupMu.Lock()
	empty := len(q.messages) == 0 && len(q.backupBuffer) == 0
	q.backupMu.Unlock()
	return empty
}

// IsFull checks if the queue is full.
func (q *MessageQueue) IsFull() bool {
	return len(q.messages) >= q.opts.MaxSize
}

// Available returns the number of available slots (accounting for backup buffer).
func (q *MessageQueue) Available() int {
	q.backupMu.Lock()
	backupSize := len(q.backupBuffer)
	q.backupMu.Unlock()
	return q.opts.MaxSize - len(q.messages) - backupSize
}

// AgentID returns the agent ID associated with this queue.
func (q *MessageQueue) AgentID() string {
	return q.agentID
}

// Close closes the queue and drains remaining messages.
func (q *MessageQueue) Close() {
	q.closeOnce.Do(func() {
		q.closed.Store(true)
		close(q.messages)
	})
}

// QueueRegistry manages multiple message queues for different agents.
type QueueRegistry struct {
	mu          sync.RWMutex
	queues      map[string]*MessageQueue
	defaultOpts *QueueOptions
}

// NewQueueRegistry creates a new QueueRegistry.
func NewQueueRegistry(opts *QueueOptions) *QueueRegistry {
	if opts == nil {
		opts = DefaultQueueOptions()
	}
	return &QueueRegistry{
		queues:      make(map[string]*MessageQueue),
		defaultOpts: opts,
	}
}

// GetOrCreate returns an existing queue or creates a new one.
func (r *QueueRegistry) GetOrCreate(agentID string) *MessageQueue {
	r.mu.Lock()
	defer r.mu.Unlock()

	if q, ok := r.queues[agentID]; ok {
		return q
	}

	q := NewMessageQueue(agentID, r.defaultOpts)
	r.queues[agentID] = q
	return q
}

// Get returns a queue by agent ID.
func (r *QueueRegistry) Get(agentID string) (*MessageQueue, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	q, ok := r.queues[agentID]
	return q, ok
}

// Delete removes a queue by agent ID.
func (r *QueueRegistry) Delete(agentID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if q, ok := r.queues[agentID]; ok {
		q.Close()
		delete(r.queues, agentID)
	}
}

// ListAgents returns all registered agent IDs.
func (r *QueueRegistry) ListAgents() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	agents := make([]string, 0, len(r.queues))
	for agentID := range r.queues {
		agents = append(agents, agentID)
	}
	return agents
}

// Size returns the total number of messages across all queues.
func (r *QueueRegistry) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	total := 0
	for _, q := range r.queues {
		total += q.Size()
	}
	return total
}
