package agentipc

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/Timwood0x10/ares/internal/feedback"
)

// taskIDKey is the canonical payload key carrying a task identifier across
// collaboration/handoff messages. cmd/ares mirrors this value on the receive
// side (its own constant) — change both together.
const taskIDKey = "task_id"

// defaultRequestTimeout is used when Request is called with timeout <= 0
// (prevents indefinite blocking on a missing timeout).
const defaultRequestTimeout = 30 * time.Second

// ErrHandlerPanic is returned to the caller when a registered handler panics
// during a Request. The panic is contained at the goroutine boundary
// (a goroutine must have a recover boundary or be guaranteed
// not to panic) so a buggy or third-party handler fails ONE request instead of
// terminating the process — a panic in a goroutine cannot be recovered by the
// caller's stack, so containment has to happen where the goroutine runs.
var ErrHandlerPanic = errors.New("agentipc: handler panicked")

// Send is the fire-and-forget primitive: deliver a message to a target agent
// without waiting for a reply. The target's handler is invoked synchronously
// in the caller's goroutine; a failed handler returns the error but does not
// block the sender (no reply channel is set up). Send does NOT pair with a
// reply — use Request for request/reply semantics.
//
// Args:
//   - ctx: passed to the handler.
//   - to: the target agent id.
//   - topic: the message subject.
//   - payload: the message body.
//
// Returns:
//   - error: ErrAgentNotRegistered / ErrNoHandler, or the handler error.
func (b *Bus) Send(ctx context.Context, from, to, topic string, payload any) error {
	// measure the delivery receipt. A fire-and-forget send has no
	// answer to judge, but "the agent I addressed does not exist" and "its
	// handler rejected my message" are still feedback about the initiator's
	// choice of collaborator — and Send is the primitive the production peer
	// bridge actually uses (cmd/ares/evolution_ipc.go routes every peer
	// message through it), so leaving it unobserved would mean the
	// collaboration channel records nothing in production.
	started := b.allocNow()
	// Start at "unobserved" and set a verdict only at a known exit. If the
	// handler panics, it unwinds through this defer with no verdict assigned —
	// and an unobserved record is discarded rather than scored. Initializing to
	// success instead would silently write a FALSE success for the one case
	// where the collaboration most clearly failed.
	outcome := feedback.OutcomeUnobserved
	defer func() {
		b.observeCollaboration(feedback.CollaborationOutcome{
			Initiator: from,
			Target:    to,
			Topic:     topic,
			Kind:      feedback.CollabSend,
			Outcome:   outcome,
			Latency:   b.allocNow().Sub(started),
		})
	}()

	b.mu.RLock()
	h, ok := b.handlers[to]
	b.mu.RUnlock()
	// resolve the trace once — continued from the caller when present,
	// else a fresh root. Every exit below (delivered or dead-lettered)
	// carries this same id.
	traceID := b.traceOrNew(ctx)
	if !ok {
		// the target does not exist — the message is undeliverable.
		b.deadLetters.Record(from, to, topic, payload, ErrAgentNotRegistered.Error(), traceID)
		outcome = feedback.OutcomeNotFound
		return ErrAgentNotRegistered
	}
	msg := &Message{
		ID:      b.allocID(),
		From:    from,
		To:      to,
		Topic:   topic,
		TraceID: traceID,
		Payload: payload,
		At:      b.allocNow(),
	}
	err := b.safeInvokeHandler(ContextWithTraceID(ctx, traceID), msg, h, from, to)
	if err != nil {
		b.deadLetters.Record(from, to, topic, payload, err.Error(), traceID)
		outcome = feedback.OutcomeFailure
		return err
	}
	outcome = feedback.OutcomeSuccess
	return nil
}

// safeInvokeHandler calls a handler with a recover boundary so a panicking
// handler cannot kill the process. Returns ErrHandlerPanic on recover.
func (b *Bus) safeInvokeHandler(ctx context.Context, msg *Message, h Handler, from, to string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			b.logHandlerPanic(msg, from, to, r)
			err = ErrHandlerPanic
		}
	}()
	_, err = h(ctx, msg)
	return err
}

// Request is the synchronous request/reply primitive: send a message to a
// target agent and wait for a reply within the timeout. The bus allocates a
// correlation id and registers a pending reply channel; the target's handler
// must call Reply with the same correlation id to complete the request. A
// timeout or context cancellation removes the pending entry and returns
// ErrTimeout.
//
// Args:
//   - ctx: cancellation propagates to the wait.
//   - from: the sender agent id.
//   - to: the target agent id.
//   - topic: the request subject.
//   - payload: the request body.
//   - timeout: how long to wait for a reply.
//
// Returns:
//   - *Message: the reply (nil on timeout/error).
//   - error: ErrAgentNotRegistered / ErrNoHandler / ErrTimeout.
func (b *Bus) Request(ctx context.Context, from, to, topic string, payload any, timeout time.Duration) (*Message, error) {
	// validate timeout — <=0 gets a sane default instead of blocking forever.
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	// derive a child context so the handler goroutine is cancelled when
	// the timeout fires or the caller cancels — the handler no longer leaks.
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// measure the collaboration receipt. started is captured before
	// the handler lookup so the latency covers what the initiator actually
	// waited, including an addressing failure. The verdict starts unobserved
	// and is set only at a known exit, so an unforeseen return path writes no
	// record rather than a fabricated success.
	started := b.allocNow()
	outcome := feedback.OutcomeUnobserved
	defer func() {
		b.observeCollaboration(feedback.CollaborationOutcome{
			Initiator: from,
			Target:    to,
			Topic:     topic,
			Kind:      feedback.CollabRequest,
			Outcome:   outcome,
			Latency:   b.allocNow().Sub(started),
		})
	}()

	b.mu.RLock()
	h, ok := b.handlers[to]
	b.mu.RUnlock()
	// resolve the trace once (see Send). The unregistered-target exit
	// below is a genuine delivery failure — record it like Send does
	// (previously this arm returned silently, the one coverage hole).
	traceID := b.traceOrNew(ctx)
	if !ok {
		outcome = feedback.OutcomeNotFound
		b.deadLetters.Record(from, to, topic, payload, ErrAgentNotRegistered.Error(), traceID)
		return nil, ErrAgentNotRegistered
	}
	corrID := b.allocID() + "-corr"
	replyCh := make(chan *Message, 1)
	b.mu.Lock()
	b.pending[corrID] = replyCh
	b.mu.Unlock()
	defer b.removePending(corrID)

	req := &Message{
		ID:            b.allocID(),
		From:          from,
		To:            to,
		Topic:         topic,
		CorrelationID: corrID,
		TraceID:       traceID,
		Payload:       payload,
		At:            b.allocNow(),
	}
	// Invoke the handler in a managed goroutine so the reply can be delivered
	// asynchronously via Reply. If the handler returns a reply directly, it is
	// stamped and delivered through the same reply channel. If the handler
	// returns an error, a nil reply is delivered so the caller's select wakes
	// up and the error is surfaced.
	//
	// The goroutine carries a recover boundary.
	// Handlers are foreign code — a registered agent handler, a collaboration
	// executor, a third-party plugin — and a panic inside a goroutine cannot be
	// recovered by the caller's stack, so without this the whole process dies
	// on one bad handler. Contained here, a panic fails exactly ONE request
	// (ErrHandlerPanic), which is the same blast radius as a returned error.
	go b.invokeHandler(reqCtx, h, req, corrID, from, to)

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	// single dead-letter exit — replyErr captures the failure reason
	// from whichever select arm fires, and exactly one Record happens below
	// (the two timeout arms race; without the flag a message could be
	// recorded twice).
	var replyErr error
	defer func() {
		if replyErr != nil {
			b.deadLetters.Record(from, to, topic, payload, replyErr.Error(), traceID)
		}
	}()
	select {
	case reply := <-replyCh:
		if reply == nil {
			// A nil reply signals a handler error — pull it from the stash.
			if err := b.popError(corrID); err != nil {
				replyErr = err
				outcome = feedback.OutcomeFailure
				return nil, err
			}
			replyErr = ErrTimeout
			outcome = feedback.OutcomeTimeout
			return nil, ErrTimeout
		}
		outcome = feedback.OutcomeSuccess
		return reply, nil
	case <-ctx.Done(): // Caller-side cancellation / deadline propagation is NOT a delivery
		// failure: the request may well have been delivered and handled. The
		// dead-letter queue is a bounded FIFO reserved for genuine delivery
		// failures, so recording cancellations here would evict them. Leave
		// replyErr nil.
		//
		// It is not a collaboration outcome either — the initiator
		// walked away, which says nothing about the target's quality. Scoring
		// it would punish whichever agent happened to be asked when the
		// caller's context expired.
		outcome = feedback.OutcomeUnobserved
		return nil, ctx.Err()
	case <-timer.C:
		replyErr = ErrTimeout
		outcome = feedback.OutcomeTimeout
		return nil, ErrTimeout
	}
}

// invokeHandler runs one request handler inside the managed goroutine spawned
// by Request, with the recover boundary.
//
// PANIC CONTAINMENT: the handler is foreign code and runs on
// its own goroutine, so a panic there is unrecoverable from the caller's stack
// and would take the process down. The recover converts it into
// ErrHandlerPanic delivered through the SAME sentinel-nil-reply path an ordinary
// handler error uses, so the waiting Request wakes up immediately instead of
// burning its full timeout — a panicking handler is never going to reply, and
// making the caller wait for the deadline would turn a fast failure into a slow
// one. The panic value is deliberately not embedded in the error: it may carry
// internal paths or request data, so it goes to the log with
// context keys instead.
//
// Args:
//   - ctx: the request-scoped context (already bounded by the timeout).
//   - h: the target's handler.
//   - req: the stamped request message.
//   - corrID: the correlation id the reply must carry.
//   - from: the initiator agent id (becomes the reply's To).
//   - to: the target agent id (becomes the reply's From).
func (b *Bus) invokeHandler(ctx context.Context, h Handler, req *Message, corrID, from, to string) {
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		b.logHandlerPanic(req, from, to, r)
		// Same wake-up protocol as a handler error: stash, then deliver the
		// sentinel nil reply.
		b.stashError(corrID, ErrHandlerPanic)
		_ = b.deliverReply(corrID, nil) // best-effort: an orphan reply is a documented no-op
	}()

	reply, err := h(ContextWithTraceID(ctx, req.TraceID), req)
	if err != nil {
		// Surface the error: deliver a sentinel nil reply so the caller
		// wakes up; the actual error is stashed on the pending entry.
		b.stashError(corrID, err)
		_ = b.deliverReply(corrID, nil) // best-effort: see deliverReply
		return
	}
	if reply != nil {
		// Copy the handler-returned reply and stamp it — never mutate the
		// caller's message (the handler may return a shared template
		// across concurrent requests, so in-place stamping would race).
		// the trace rides along — a reply belongs to its request's
		// causal chain by construction.
		stamped := *reply
		stamped.CorrelationID = corrID
		stamped.TraceID = req.TraceID
		stamped.To = from
		stamped.From = to
		_ = b.deliverReply(corrID, &stamped) // best-effort: see deliverReply
	}
	// If the handler returned nil with no error, it intends to reply
	// asynchronously later via Reply. The caller's select waits for the
	// timeout in that case.
}

// logHandlerPanic reports a contained handler panic through the injected
// logger. Library code must not print directly, so a bus
// without a logger stays silent rather than writing to stderr — the caller
// still learns about the failure through ErrHandlerPanic.
func (b *Bus) logHandlerPanic(req *Message, from, to string, panicValue any) {
	b.mu.RLock()
	logger := b.logger
	b.mu.RUnlock()
	if logger == nil {
		return
	}
	logger.Error("agentipc: handler panicked",
		"from", from,
		"to", to,
		"topic", req.Topic,
		"message_id", req.ID,
		"panic", fmt.Sprintf("%v", panicValue),
		"stack", string(debug.Stack()),
	)
}

// Reply delivers a reply to a pending request identified by the correlation
// id. It is called by the agent's handler when it has the answer (asynchronous
// reply — the handler may compute the reply later and call Reply separately).
// A reply to an unknown correlation id (already timed out or cancelled) is a
// no-op best-effort drop.
//
// Args:
//   - corrID: the correlation id from the original request.
//   - reply: the reply message (From/To/CorrelationID are stamped by the bus).
//
// Returns:
//   - error: ErrInvalidMessage when corrID is empty.
func (b *Bus) Reply(corrID string, reply *Message) error {
	if corrID == "" {
		return ErrInvalidMessage
	}
	if reply == nil {
		return ErrInvalidMessage
	}
	return b.deliverReply(corrID, reply)
}

// deliverReply pushes a reply to the pending channel. Best-effort: a full or
// absent channel means the request already completed (timeout/cancel).
func (b *Bus) deliverReply(corrID string, reply *Message) error {
	b.mu.Lock()
	ch, ok := b.pending[corrID]
	b.mu.Unlock()
	if !ok {
		return nil // best-effort drop — orphan reply for a completed request
	}
	select {
	case ch <- reply:
	default:
	}
	return nil
}

// removePending deletes a pending entry. Called via defer in Request.
func (b *Bus) removePending(corrID string) {
	b.mu.Lock()
	delete(b.pending, corrID)
	delete(b.pendingErr, corrID)
	b.mu.Unlock()
}

// stashError stores a handler error so the caller can surface it after the
// nil-reply sentinel wakes the select.
func (b *Bus) stashError(corrID string, err error) {
	b.mu.Lock()
	b.pendingErr[corrID] = err
	b.mu.Unlock()
}

// popError returns and clears a stashed handler error.
func (b *Bus) popError(corrID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	err := b.pendingErr[corrID]
	delete(b.pendingErr, corrID)
	return err
}

// Delegate forwards a request to another agent on the caller's behalf
// (IPC: Delegate). The delegating agent is the one making the call; the
// target sees the delegator as the From. The original requester's
// correlation id is preserved end-to-end so the reply can chain back. This is
// the primitive for "I can't handle this — let me ask someone who can".
//
// Args:
//   - ctx: cancellation.
//   - delegator: the agent delegating (forwards on behalf of).
//   - to: the final target.
//   - topic: the request subject.
//   - payload: the request body.
//   - timeout: reply wait timeout.
//
// Returns:
//   - *Message: the reply.
//   - error: ErrAgentNotRegistered / ErrTimeout.
func (b *Bus) Delegate(ctx context.Context, delegator, to, topic string, payload any, timeout time.Duration) (*Message, error) {
	return b.Request(ctx, delegator, to, topic, payload, timeout)
}

// Handoff transfers a task's ownership from one agent to another (IPC:
// Handoff). Unlike Send, Handoff carries a structured handoff payload
// (task id + context snapshot + artifacts) and the receiver acknowledges
// acceptance. The sender yields the task; the receiver takes it. This is the
// peer-to-peer task-transfer primitive — it does NOT go through the Scheduler.
//
// Args:
//   - ctx: cancellation.
//   - from: the yielding agent.
//   - to: the accepting agent.
//   - taskID: the task being handed off.
//   - contextSnapshot: the task context snapshot (selected projection).
//   - timeout: acceptance wait.
//
// Returns:
//   - *Message: the receiver's acceptance reply.
//   - error: ErrAgentNotRegistered / ErrTimeout.
func (b *Bus) Handoff(ctx context.Context, from, to, taskID string, contextSnapshot map[string]any, timeout time.Duration) (*Message, error) {
	payload := map[string]any{
		taskIDKey:   taskID,
		"context":   contextSnapshot,
		"artifacts": []any{},
	}
	return b.Request(ctx, from, to, "handoff-task", payload, timeout)
}

// Subscribe registers an agent's interest in a topic (IPC:
// Subscribe). Subscribers receive broadcast messages on that topic. A
// broadcast to a topic fans out to every subscriber's handler. This is the
// primitive for "I found X — anyone interested in X should know".
//
// Args:
//   - agentID: the subscribing agent.
//   - topic: the topic of interest.
//
// Returns:
//   - error: fmt.Errorf for an empty agent id.
func (b *Bus) Subscribe(agentID, topic string) error {
	if agentID == "" {
		return errors.New("agentipc: agent id required")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	// deduplicate — don't add the same agent to the same topic twice.
	for _, existing := range b.subscribers[topic] {
		if existing == agentID {
			return nil // already subscribed
		}
	}
	b.subscribers[topic] = append(b.subscribers[topic], agentID)
	return nil
}

// Unsubscribe removes an agent's subscription to a topic.
func (b *Bus) Unsubscribe(agentID, topic string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	subs := b.subscribers[topic]
	out := subs[:0]
	for _, s := range subs {
		if s != agentID {
			out = append(out, s)
		}
	}
	b.subscribers[topic] = out
}

// Broadcast sends a message to every subscriber of a topic (fire-and-forget
// fan-out). Each subscriber's handler is invoked; a handler error is collected
// but does not stop the fan-out. Returns the count of successful deliveries.
//
// Args:
//   - ctx: passed to each handler.
//   - from: the broadcasting agent.
//   - topic: the broadcast topic.
//   - payload: the message body.
//
// Returns:
//   - int: number of subscribers that received the message without error.
func (b *Bus) Broadcast(ctx context.Context, from, topic string, payload any) int {
	b.mu.RLock()
	subs := make([]string, len(b.subscribers[topic]))
	copy(subs, b.subscribers[topic])
	b.mu.RUnlock()

	// one trace for the whole fan-out — every delivery shares the
	// broadcast's causal id.
	traceID := b.traceOrNew(ctx)
	delivered := 0
	for _, subID := range subs {
		b.mu.RLock()
		h, ok := b.handlers[subID]
		b.mu.RUnlock()
		if !ok {
			continue
		}
		msg := &Message{
			ID:      b.allocID(),
			From:    from,
			To:      subID,
			Topic:   topic,
			TraceID: traceID,
			Payload: payload,
			At:      b.allocNow(),
		}
		if err := b.safeInvokeHandler(ContextWithTraceID(ctx, traceID), msg, h, from, subID); err == nil {
			delivered++
		} else {
			b.deadLetters.Record(from, subID, topic, payload, err.Error(), traceID)
		}
	}
	return delivered
}

// allocID generates a unique message id (thread-safe).
func (b *Bus) allocID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nextIDLocked()
}

// allocNow returns the current time (thread-safe via atomic-less read of the
// now closure; the closure is write-once at construction).
func (b *Bus) allocNow() time.Time {
	return b.now()
}
