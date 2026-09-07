// Package feedback carries the primitives of the three channels through which
// an agent can actually observe the outside world (closure plan Step Y): its
// own task outcomes, the tools it called, and the collaboration receipts other
// agents sent back. An agent cannot perceive the world any other way, so every
// evolution input must originate in one of these three channels — never in an
// external guess about how well a strategy is doing.
//
// The package holds DATA ONLY: no interfaces, no store, no logging, and no
// dependency beyond the standard library. That is deliberate. The producers
// (agentipc for collaboration receipts, the tool-binder decorator for tool
// calls) and the consumer (ares_evolution's ChannelFeedbackRecorder) both
// import these structs, and each side declares its own observer interface at
// its own consumption point. Putting the structs here — instead of in either
// producer or consumer — is what keeps the graph acyclic: the kernel IPC bus
// still knows nothing about the evolution layer.
//
// The task channel is NOT modelled here: task outcomes already reach the
// evolution layer through the event stream (ares_evolution.RuntimeObserver),
// so a second representation would be a competing source of truth.
package feedback

import "time"

// Outcome is what the observing side actually saw. It is deliberately coarse:
// these values feed a [0,1] fitness score, and a finer taxonomy would invite
// scoring rules nobody can audit.
type Outcome string

const (
	// OutcomeSuccess means the call completed and returned a usable result.
	OutcomeSuccess Outcome = "success"
	// OutcomeFailure means the call ran and failed (handler error, tool
	// error). The counterpart ran — it just did not deliver.
	OutcomeFailure Outcome = "failure"
	// OutcomeTimeout means no receipt arrived inside the deadline. Distinct
	// from failure: nothing is known about whether the work happened.
	OutcomeTimeout Outcome = "timeout"
	// OutcomeNotFound means the target did not exist (unregistered agent,
	// unknown tool). This is an addressing mistake by the CALLER, which is
	// exactly the kind of thing evolution should learn to stop doing.
	OutcomeNotFound Outcome = "not_found"
	// OutcomeUnobserved means the attempt produced no judgeable receipt —
	// the caller abandoned it (context cancelled / deadline from above), so
	// nothing was learned about the callee. It is the explicit "do not score
	// this" value: a producer sets it, and Observable() filters the record out
	// before it ever becomes evidence. Scoring an abandoned call as a failure
	// would punish whichever agent or tool happened to be in flight when the
	// caller walked away.
	OutcomeUnobserved Outcome = ""
)

// Observable reports whether the outcome carries a judgeable signal. A record
// whose outcome is not observable must not be turned into fitness evidence.
func (o Outcome) Observable() bool { return o != OutcomeUnobserved }

// Succeeded reports whether the outcome counts as a success for fitness
// scoring. Only OutcomeSuccess does; every other value — including a timeout,
// where the work may well have completed invisibly — scores 0, because an
// unobservable result is not a result the agent can build on.
func (o Outcome) Succeeded() bool { return o == OutcomeSuccess }

// Score maps the outcome onto the [0,1] fitness scale used by every evidence
// consumer (RuntimeFitnessAggregator rejects values outside that range).
func (o Outcome) Score() float64 {
	if o.Succeeded() {
		return 1
	}
	return 0
}

// CollaborationKind distinguishes WHAT was observed, because the two peer
// primitives answer different questions and a single outcome value would
// conflate them. Keeping the kind on the record lets an audit separate "the
// peer answered me" from "the peer accepted my message" instead of discovering
// later that a success rate mixed both.
type CollaborationKind string

const (
	// CollabRequest is a request/reply exchange: the receipt is the peer's
	// ANSWER, so the outcome reflects whether a usable reply came back in time.
	CollabRequest CollaborationKind = "request"
	// CollabSend is a fire-and-forget delivery: the receipt is only that the
	// target existed and its handler accepted the message. It says nothing
	// about the quality of any later answer — but "the agent I addressed does
	// not exist" and "its handler rejected my message" are still real feedback
	// about the initiator's choice of collaborator.
	CollabSend CollaborationKind = "send"
)

// CollaborationOutcome is one cross-agent collaboration receipt: agent A asked
// agent B for something and this is what came back. It answers the question
// evolution could not previously ask — "should I have asked THAT agent?"
type CollaborationOutcome struct {
	// Initiator is the agent that sent the request.
	Initiator string
	// Target is the agent the request was addressed to.
	Target string
	// Topic is the collaboration subject (e.g. "handoff-task").
	Topic string
	// Kind is which primitive produced the receipt.
	Kind CollaborationKind
	// Outcome is the observed receipt.
	Outcome Outcome
	// Latency is how long the initiator waited for the receipt.
	Latency time.Duration
}

// ToolCallOutcome is one tool invocation receipt. It answers the second
// question evolution could not ask — "was calling THAT tool worth it?"
type ToolCallOutcome struct {
	// Tool is the invoked tool name.
	Tool string
	// Caller is the agent identity that invoked it (from the kernel caller
	// context), empty when the call arrived without a stamped caller.
	Caller string
	// Outcome is the observed result.
	Outcome Outcome
	// Latency is the invocation wall time.
	Latency time.Duration
	// ToolStepID is the process-level attribution key: toolName#argShape.
	// It lets the evidence and the RuntimeFitnessAggregator distinguish "WHICH
	// way this strategy calls the tool" — two strategies that call the same tool
	// with different argument shapes no longer collapse into one undifferentiated
	// signal. When the observer cannot compute a shape, it is empty and the
	// recorder attributes at the tool (not tool-step) granularity.
	ToolStepID string
}
