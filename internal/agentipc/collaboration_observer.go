// collaboration_observer.go makes the bus measure collaboration receipts.
//
// Before this file the Bus recorded delivery FAILURES into the dead-letter
// store and nothing else. Nobody counted how often a collaboration succeeded,
// how long the initiator waited, or whether the target even existed — so the
// evolution loop could not learn "asking agent X about topic Y does not work".
// Collaboration is one of the three channels through which an agent perceives
// the outside world; leaving it unmeasured left evolution structurally blind to
// a third of its own inputs.
//
// The observer interface is declared HERE, at the consumer
// (interfaces at the consumer), and the bus never imports the evolution layer:
// ares_evolution.ChannelFeedbackRecorder satisfies it structurally. The bus
// stays a kernel primitive that does not know it is being scored.
package agentipc

import "github.com/Timwood0x10/ares/internal/feedback"

// CollaborationObserver receives one record per collaboration attempt that
// carries an observable receipt. Implementations MUST NOT block: the observer is
// called on the initiator's path, immediately after the attempt resolves, so any
// latency here is latency the collaborating agent pays.
//
// Two primitives are observed, and the record's Kind says which:
//
//   - Request (and Delegate / Handoff, which are built on it) — the receipt is
//     the peer's ANSWER, so the outcome reflects whether a usable reply arrived
//     in time.
//   - Send — fire-and-forget, so the receipt is only that the target existed
//     and its handler accepted the message. This is the primitive the production
//     peer bridge actually uses (cmd/ares/evolution_ipc.go routes every peer
//     message through Send), so observing only Request would leave the
//     collaboration channel empty in production while looking wired.
//
// The two are kept distinguishable rather than merged: "the peer answered me"
// and "the peer accepted my message" are different signals, and a success rate
// that silently mixed them would not be auditable.
//
// Broadcast is deliberately NOT observed: a topic fan-out has no single target
// to attribute the outcome to, and scoring the aggregate delivery count would
// blame the initiator for subscribers it never chose.
type CollaborationObserver interface {
	// OnCollaboration reports one collaboration receipt.
	OnCollaboration(out feedback.CollaborationOutcome)
}

// WithCollaborationObserver attaches the collaboration observer. Nil disables
// observation (the pre-Step-Y behavior). Safe to call before or after handlers
// are registered; the observer is read under the bus lock on each request.
//
// Args:
//   - obs: the non-blocking observer; nil clears it.
//
// Returns:
//   - *Bus: the receiver, for chaining.
func (b *Bus) WithCollaborationObserver(obs CollaborationObserver) *Bus {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.collabObserver = obs
	return b
}

// observeCollaboration emits one receipt when an observer is attached. It reads
// the observer under the read lock and calls it OUTSIDE the lock: the observer
// is foreign code (the evolution recorder), and holding the bus lock across it
// would let a slow implementation stall every other agent's messaging.
//
// A non-observable outcome (the caller abandoned the request) is dropped here
// rather than at the producer, so every call site gets the same rule.
func (b *Bus) observeCollaboration(out feedback.CollaborationOutcome) {
	if !out.Outcome.Observable() {
		return
	}
	b.mu.RLock()
	obs := b.collabObserver
	b.mu.RUnlock()
	if obs == nil {
		return
	}
	obs.OnCollaboration(out)
}
