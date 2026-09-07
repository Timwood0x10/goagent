// channel_feedback.go closes N-11 (closure plan Step Y.2/Y.3): the OBSERVE
// stage for the two perception channels evolution was blind to — cross-agent
// collaboration receipts and tool-call outcomes.
//
// Before this file the evolution loop only saw the task channel (RuntimeObserver
// on task events). A strategy could keep asking an agent that never answers, or
// keep calling a tool that always errors, and the fitness signal would not move
// — evolution had no way to know either happened. ChannelFeedbackRecorder gives
// those two channels the same shape as the task channel: normalized [0,1]
// KindFitness evidence, attributed to the active strategy.
//
// SOURCE ISOLATION: collaboration and tool-call records go to their OWN
// evidence sources ("collaboration" / "tool_call"), never to "strategy" or
// "strategy_shadow". Both of those are load-bearing for other verdicts — the
// rollback window and deployment staging read "strategy", the shadow A/B pair
// reads "strategy_shadow" — and folding a different measurement standard into
// them would corrupt those verdicts. The aggregator picks the new sources up as
// separately weighted dimensions (see RuntimeFitnessAggregator.Window), with
// weight 0 by default so an operator who has not opted in sees no change.
//
// WRITE PATH: the observers are called from hot paths (the IPC bus's reply
// wait, every tool invocation), so they only enqueue onto a bounded channel and
// return. A single drain goroutine performs the store write. A full queue drops
// the record and counts the drop — losing a fitness sample is acceptable, adding
// store latency to an agent's tool call is not.
package evolution

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/feedback"
)

// collaborationEvidenceSource is the dedicated source for cross-agent
// collaboration receipts (Step Y.2).
const collaborationEvidenceSource = "collaboration"

// toolCallEvidenceSource is the dedicated source for tool-call outcomes
// (Step Y.3).
const toolCallEvidenceSource = "tool_call"

// channelQueueSize bounds the in-flight record queue. Sized for a burst of
// tool calls across the peer population between two drain iterations; beyond
// that, dropping is the correct trade (see the WRITE PATH note).
const channelQueueSize = 256

// channelRecord is one queued fitness observation from a perception channel.
type channelRecord struct {
	// source is the evidence source ("collaboration" / "tool_call").
	source string
	// idPrefix disambiguates evidence IDs per channel.
	idPrefix string
	// score is the normalized [0,1] fitness value.
	score float64
	// payload holds the channel-specific detail merged into the evidence.
	payload map[string]any
	// at is when the observation was made.
	at time.Time
}

// ChannelFeedbackChannels declares which perception channels are armed. A
// disarmed channel records nothing even if a producer is attached to it: the
// switch is enforced HERE, at the recorder, so arming is not a property of how
// carefully each wiring site was written.
type ChannelFeedbackChannels struct {
	// Collaboration arms cross-agent collaboration receipts (Step Y.2).
	Collaboration bool
	// ToolCalls arms tool invocation outcomes (Step Y.3).
	ToolCalls bool
}

// Any reports whether at least one channel is armed.
func (c ChannelFeedbackChannels) Any() bool { return c.Collaboration || c.ToolCalls }

// ChannelFeedbackRecorder converts collaboration and tool-call observations
// into strategy-attributed fitness evidence. It satisfies the observer
// interfaces declared by its producers (agentipc.CollaborationObserver and
// sub.ToolCallObserver) STRUCTURALLY — neither producer imports the evolution
// layer, so the kernel IPC bus and the tool binder stay unaware that their
// outcomes are being scored (the same one-way relationship RuntimeObserver has
// with agent code).
type ChannelFeedbackRecorder struct {
	// store receives the fitness evidence.
	store evidence.Store
	// activeID resolves the strategy the observation is attributed to. An
	// unattributable record is dropped rather than written under a placeholder
	// ID: the aggregator scopes these sources by strategy, so a record with a
	// wrong ID would be counted toward a strategy that did not earn it.
	activeID func() string
	// channels is the arm state, fixed at construction.
	channels ChannelFeedbackChannels
	// queue carries records to the drain goroutine.
	queue chan channelRecord
	// mu guards cancel/done/dropped.
	mu sync.Mutex
	// cancel stops the drain goroutine.
	cancel context.CancelFunc
	// done closes when the drain goroutine has exited.
	done chan struct{}
	// dropped counts records discarded because the queue was full or the
	// record could not be attributed.
	dropped int
}

// NewChannelFeedbackRecorder creates the recorder. Missing dependencies are
// rejected loudly rather than degraded: a recorder that silently writes nowhere
// (nil store), cannot attribute anything (nil resolver), or has no armed
// channel would look wired while recording nothing — precisely the failure mode
// this plan exists to remove.
//
// Args:
//
//	store    - the shared evidence store (must be non-nil).
//	activeID - resolves the currently active strategy ID (must be non-nil);
//	           an empty return drops the record as unattributable.
//	channels - which channels are armed (at least one must be).
//
// Returns:
//
//	*ChannelFeedbackRecorder - the recorder, not yet started.
//	error                    - non-nil when a dependency is missing.
func NewChannelFeedbackRecorder(store evidence.Store, activeID func() string, channels ChannelFeedbackChannels) (*ChannelFeedbackRecorder, error) {
	if store == nil {
		return nil, fmt.Errorf("channel feedback recorder requires an evidence store")
	}
	if activeID == nil {
		return nil, fmt.Errorf("channel feedback recorder requires an active-strategy resolver")
	}
	if !channels.Any() {
		return nil, fmt.Errorf("channel feedback recorder requires at least one armed channel")
	}
	return &ChannelFeedbackRecorder{
		store:    store,
		activeID: activeID,
		channels: channels,
		queue:    make(chan channelRecord, channelQueueSize),
	}, nil
}

// CollaborationArmed reports whether the collaboration channel is armed. The
// wiring layer reads it to decide whether attaching the bus observer is
// meaningful (an unarmed attach is inert but misleading in a wiring log).
func (r *ChannelFeedbackRecorder) CollaborationArmed() bool {
	return r != nil && r.channels.Collaboration
}

// ToolCallsArmed reports whether the tool-call channel is armed.
func (r *ChannelFeedbackRecorder) ToolCallsArmed() bool {
	return r != nil && r.channels.ToolCalls
}

// Start launches the drain goroutine. Idempotent: a second call is a no-op.
// The goroutine exits when ctx is cancelled or Stop is called.
func (r *ChannelFeedbackRecorder) Start(ctx context.Context) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.cancel != nil {
		r.mu.Unlock()
		return
	}
	drainCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	r.cancel = cancel
	r.done = done
	r.mu.Unlock()

	go func() {
		// A background goroutine must not take the process down on a bug
		// (code_rules §4.2) — recover, log, exit cleanly.
		defer func() {
			if rec := recover(); rec != nil {
				log.ErrorContext(context.Background(), "channel feedback drain panicked",
					"error", fmt.Errorf("panic: %v", rec))
			}
			close(done)
		}()
		for {
			select {
			case rec := <-r.queue:
				r.write(drainCtx, rec)
			case <-drainCtx.Done():
				// Flush what is already queued: these are completed
				// observations, and shutdown is not a reason to lose them.
				for {
					select {
					case rec := <-r.queue:
						r.write(context.WithoutCancel(drainCtx), rec)
					default:
						return
					}
				}
			}
		}
	}()
}

// Stop cancels the drain goroutine and waits for it to exit.
func (r *ChannelFeedbackRecorder) Stop() {
	if r == nil {
		return
	}
	r.mu.Lock()
	cancel := r.cancel
	done := r.done
	r.cancel = nil
	r.done = nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// Dropped reports how many observations were discarded (queue full, or no
// attributable strategy). Observability surface: a climbing count means the
// channel feedback is lossy and the verdict rests on a thinner sample than the
// evidence count suggests.
func (r *ChannelFeedbackRecorder) Dropped() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.dropped
}

// OnCollaboration implements agentipc.CollaborationObserver: one cross-agent
// collaboration receipt (Step Y.2). Non-blocking by contract — it runs on the
// bus's request path.
//
// Args:
//
//	out - the observed receipt (initiator, target, topic, outcome, latency).
func (r *ChannelFeedbackRecorder) OnCollaboration(out feedback.CollaborationOutcome) {
	if r == nil || !r.channels.Collaboration {
		return
	}
	r.enqueue(out.Outcome, channelRecord{
		source:   collaborationEvidenceSource,
		idPrefix: "collab",
		score:    out.Outcome.Score(),
		payload: map[string]any{
			"initiator": out.Initiator,
			"target":    out.Target,
			"topic":     out.Topic,
			// kind separates a request/reply answer from a fire-and-forget
			// delivery acceptance. Both are collaboration feedback, but they
			// measure different things, so an audit (or a future per-kind
			// weighting) must be able to tell them apart instead of finding a
			// success rate that silently blended the two.
			"kind":    string(out.Kind),
			"outcome": string(out.Outcome),
			// Latency is carried for audit only. It is deliberately NOT folded
			// into the score: there is no calibrated latency budget for a
			// collaboration receipt, and inventing one here would make the
			// fitness number unauditable (same reasoning as the aggregator's
			// deferred cost/latency penalty term).
			"latency_ms": out.Latency.Milliseconds(),
		},
	})
}

// OnToolCall implements sub.ToolCallObserver: one tool invocation outcome
// (Step Y.3). Non-blocking by contract — it runs inside every tool call.
//
// Args:
//
//	out - the observed invocation (tool, caller, outcome, latency).
func (r *ChannelFeedbackRecorder) OnToolCall(out feedback.ToolCallOutcome) {
	if r == nil || !r.channels.ToolCalls {
		return
	}
	r.enqueue(out.Outcome, channelRecord{
		source:   toolCallEvidenceSource,
		idPrefix: "toolcall",
		score:    out.Outcome.Score(),
		payload: map[string]any{
			"tool":    out.Tool,
			"caller":  out.Caller,
			"outcome": string(out.Outcome),
			// Process-level attribution (Y1 C3): toolStepID = tool#argShape.
			// Carried verbatim so the aggregator / projection layer can scope by
			// (strategyID, toolStepID) instead of the coarse per-strategy bucket —
			// two strategies calling the same tool with different shapes no longer
			// blend into one signal.
			"tool_step_id": out.ToolStepID,
			// Audit-only, same reasoning as the collaboration latency.
			"latency_ms": out.Latency.Milliseconds(),
		},
	})
}

// enqueue stamps the record and hands it to the drain goroutine. A full queue
// drops it and counts the drop. A non-observable outcome is discarded without
// counting a drop: it was never a measurement, so it is not a loss.
func (r *ChannelFeedbackRecorder) enqueue(outcome feedback.Outcome, rec channelRecord) {
	if !outcome.Observable() {
		return
	}
	rec.at = time.Now()
	select {
	case r.queue <- rec:
	default:
		r.countDrop()
	}
}

// countDrop increments the drop counter.
func (r *ChannelFeedbackRecorder) countDrop() {
	r.mu.Lock()
	r.dropped++
	r.mu.Unlock()
}

// write appends one record as KindFitness evidence attributed to the active
// strategy. Best-effort on the store error, but a refused append is counted as
// a drop so Dropped() remains a faithful lower bound on lost evidence — a lost
// fitness sample must never escalate into a failure of the path that produced
// it (same non-escalation guarantee as RuntimeObserver.writeEvidence).
func (r *ChannelFeedbackRecorder) write(ctx context.Context, rec channelRecord) {
	strategyID := r.activeID()
	if strategyID == "" {
		// Unattributable: the aggregator scopes these sources by strategy, so
		// writing it would either be ignored or, worse, mis-credited.
		r.countDrop()
		return
	}
	payload := make(map[string]any, len(rec.payload)+3)
	for k, v := range rec.payload {
		payload[k] = v
	}
	payload["value"] = rec.score
	payload["success"] = rec.score > 0
	payload[evidenceKeyStrategyID] = strategyID
	raw, err := json.Marshal(payload)
	if err != nil {
		r.countDrop()
		return
	}
	if err := r.store.Append(ctx, evidence.Evidence{
		// Full-date format with fractional seconds: the PG store deduplicates
		// on id via ON CONFLICT DO NOTHING, so a coarser timestamp would
		// silently drop records (same reasoning as the observer's IDs).
		ID:        rec.idPrefix + "_" + strategyID + "_" + rec.at.UTC().Format("20060102150405.000000"),
		Source:    rec.source,
		Kind:      evidence.KindFitness,
		Payload:   raw,
		Timestamp: rec.at,
	}); err != nil {
		// A sample the store refused is a lost sample: count it so Dropped() is
		// a faithful lower bound on how much fitness evidence never reached the
		// judgment path. The error itself is not propagated — a failed append
		// must never escalate into a failure of the drain goroutine's lifetime.
		r.countDrop()
	}
}
