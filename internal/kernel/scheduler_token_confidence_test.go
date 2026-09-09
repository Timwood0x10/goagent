package kernel

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/agents/sub"
	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/core/models"
	taskfabric "github.com/Timwood0x10/ares/internal/fabric/task"
)

// The M4 cost channel's scheduler half: buildQuantumStep accumulates each
// quantum's reported token usage (StepOutcome result metadata) into the
// checkpoint envelope, so a multi-quantum task carries the SESSION TOTAL,
// and the terminal task.completed event stamps it as payload keys for the
// RuntimeObserver's cost penalty.

// usageExecutor runs a TWO-quanta task: the first quantum yields with a
// token-usage-stamped result, the second completes with one (mirroring the
// planner's metadata contract across a yield→resume cycle).
type usageExecutor struct {
	id     string
	typ    models.AgentType
	calls  int
	inTok  []int
	outTok []int
}

func (e *usageExecutor) ID() string             { return e.id }
func (e *usageExecutor) Type() models.AgentType { return e.typ }

func (e *usageExecutor) ExecuteStep(_ context.Context, task *models.Task) (*sub.StepOutcome, error) {
	i := e.calls
	if i >= len(e.inTok) {
		i = len(e.inTok) - 1
	}
	e.calls++
	done := i > 0 // every quantum but the last yields
	res := models.NewTaskResult(task.TaskID, task.AgentType)
	res.SetSuccess(nil, "usage quantum")
	if e.inTok[i] > 0 || e.outTok[i] > 0 {
		res.Metadata = map[string]any{
			"input_tokens":  e.inTok[i],
			"output_tokens": e.outTok[i],
		}
	}
	return &sub.StepOutcome{Done: done, Result: res}, nil
}

func TestQuantumAccumulatesTokenUsage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := ares_events.NewMemoryEventStore()
	fabric := taskfabric.NewFabric().WithEventStore(store)
	tracker := NewLoadTracker()
	exec := &usageExecutor{
		id:     "coder-u",
		typ:    models.AgentType("code"),
		inTok:  []int{100, 50},
		outTok: []int{40, 10},
	}
	sched := New(fabric, map[string]CapabilityExecutor{"coder-u": exec}, tracker)
	sched.PollInterval = 10 * time.Millisecond
	go sched.Run(ctx)

	require.NoError(t, fabric.Create(&taskfabric.Task{
		ID:          "t-acc",
		Capability:  "code",
		RetryPolicy: taskfabric.RetryPolicy{MaxRetries: 1},
	}))

	// Wait for the two-quantum task (yield → resume → complete).
	deadline := time.Now().Add(3 * time.Second)
	var tk *taskfabric.Task
	for time.Now().Before(deadline) {
		tk, err := fabric.Task("t-acc")
		if err == nil && tk.State == taskfabric.StateCompleted {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	tk, err := fabric.Task("t-acc")
	require.NoError(t, err)
	require.Equal(t, taskfabric.StateCompleted, tk.State, "task must reach COMPLETED")

	// The completed task's envelope carries the SUM of both quanta.
	dc, err := taskfabric.DecodeCheckpoint(tk.Checkpoint)
	require.NoError(t, err)
	assert.Equal(t, 150, dc.InputTokens, "input tokens must accumulate across quanta")
	assert.Equal(t, 50, dc.OutputTokens, "output tokens must accumulate across quanta")

	// The terminal event carries the same totals as payload keys.
	evs, err := store.Read(ctx, "t-acc", ares_events.ReadOptions{})
	require.NoError(t, err)
	var terminal *ares_events.Event
	for _, ev := range evs {
		if ev.Type == ares_events.EventTaskCompleted {
			terminal = ev
		}
	}
	require.NotNil(t, terminal)
	assert.Equal(t, 150, terminal.Payload["input_tokens"])
	assert.Equal(t, 50, terminal.Payload["output_tokens"])
	assert.Equal(t, 200, terminal.Payload["total_tokens"])
}

func TestQuantumWithoutUsageKeepsEnvelopeClean(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fabric := taskfabric.NewFabric()
	tracker := NewLoadTracker()
	exec := &usageExecutor{
		id:     "coder-clean",
		typ:    models.AgentType("code"),
		inTok:  []int{0},
		outTok: []int{0},
	}
	sched := New(fabric, map[string]CapabilityExecutor{"coder-clean": exec}, tracker)

	require.NoError(t, fabric.Create(&taskfabric.Task{ID: "t-clean", Capability: "code"}))
	require.NoError(t, sched.executeUnbound(ctx, "t-clean"))

	tk, err := fabric.Task("t-clean")
	require.NoError(t, err)
	dc, err := taskfabric.DecodeCheckpoint(tk.Checkpoint)
	require.NoError(t, err)
	assert.Equal(t, 0, dc.InputTokens)
	assert.Equal(t, 0, dc.OutputTokens)
}

// The done-path re-wrap must carry SessionID: CompleteWithCheckpoint stores
// the new envelope BEFORE recordLocked reads the session scope off it, so a
// dropped field here strips session_id from every task.completed event
// payload (the contract RUNTIME.md documents: session_id rides on every
// persisted event).
func TestDonePathEnvelopePreservesSessionID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := ares_events.NewMemoryEventStore()
	fabric := taskfabric.NewFabric().WithEventStore(store)
	tracker := NewLoadTracker()
	exec := &probingExecutor{id: "coder-sess", typ: models.AgentType("code")}
	sched := New(fabric, map[string]CapabilityExecutor{"coder-sess": exec}, tracker)

	// Submit the task WITH a session-scoped envelope (how session traffic
	// arrives: planprojection stamps SessionID at Create).
	require.NoError(t, fabric.Create(&taskfabric.Task{
		ID:         "t-sess",
		Capability: "code",
		Checkpoint: taskfabric.EncodeCheckpoint(taskfabric.DecodedCheckpoint{
			SessionID: "sess-42",
		}),
	}))
	require.NoError(t, sched.executeUnbound(ctx, "t-sess"))

	// The COMPLETED task's envelope keeps the session scope.
	tk, err := fabric.Task("t-sess")
	require.NoError(t, err)
	require.Equal(t, taskfabric.StateCompleted, tk.State)
	dc, err := taskfabric.DecodeCheckpoint(tk.Checkpoint)
	require.NoError(t, err)
	assert.Equal(t, "sess-42", dc.SessionID, "done-path re-wrap must not drop SessionID")

	// The terminal task.completed event carries the session_id payload key.
	evs, err := store.Read(ctx, "t-sess", ares_events.ReadOptions{})
	require.NoError(t, err)
	var terminal *ares_events.Event
	for _, ev := range evs {
		if ev.Type == ares_events.EventTaskCompleted {
			terminal = ev
		}
	}
	require.NotNil(t, terminal)
	assert.Equal(t, "sess-42", terminal.Payload["session_id"],
		"the terminal event must stamp session_id (read from the envelope recordLocked is about to persist)")
}

// The M4.4 read side: a history-less candidate's confidence yields to the
// wired experience prior; a measured tracker value (or the neutral default
// with no prior) is untouched.

// staticPrior is a fixed ConfidenceSource.
type staticPrior struct{ v float64 }

func (p staticPrior) Confidence(string) float64 { return p.v }

// probingExecutor records the confidence its scheduling decision used.
type probingExecutor struct {
	id  string
	typ models.AgentType
}

func (e *probingExecutor) ID() string             { return e.id }
func (e *probingExecutor) Type() models.AgentType { return e.typ }

func (e *probingExecutor) ExecuteStep(_ context.Context, task *models.Task) (*sub.StepOutcome, error) {
	res := models.NewTaskResult(task.TaskID, task.AgentType)
	res.SetSuccess(nil, "probe done")
	return &sub.StepOutcome{Done: true, Result: res}, nil
}

func TestUnmeasuredCandidateYieldsToPrior(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fabric := taskfabric.NewFabric().WithConfidenceSource(staticPrior{0.5})
	tracker := NewLoadTracker()
	exec := &probingExecutor{id: "rust-prior", typ: models.AgentType("rust")}
	sched := New(fabric, map[string]CapabilityExecutor{"rust-prior": exec}, tracker)

	require.NoError(t, fabric.Create(&taskfabric.Task{ID: "t-prior", Capability: "rust"}))
	require.NoError(t, sched.executeUnbound(ctx, "t-prior"))

	// The decision the recorder captured must reflect the FILLED prior
	// (0.5), not the neutral 1.0 — the read-side de-masking.
	d := sched.DecisionsSnapshot()
	require.NotEmpty(t, d)
	var cand *CandidateScore
	for i := range d[0].Candidates {
		if d[0].Candidates[i].AgentID == "rust-prior" {
			cand = &d[0].Candidates[i]
		}
	}
	require.NotNil(t, cand, "candidate missing from decision")
	assert.InDelta(t, 0.5, cand.Confidence, 1e-9)
	assert.InDelta(t, 0.5, cand.Score, 1e-9)
}

func TestMeasuredTrackerValueWinsOverPrior(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fabric := taskfabric.NewFabric().WithConfidenceSource(staticPrior{0.9})
	tracker := NewLoadTracker()
	// Measured history: 1 success in 2 attempts → 0.5, which must WIN over
	// the 0.9 prior (live feedback outranks stale priors).
	tracker.End("rust-meas", true)
	tracker.End("rust-meas", false)
	exec := &probingExecutor{id: "rust-meas", typ: models.AgentType("rust")}
	sched := New(fabric, map[string]CapabilityExecutor{"rust-meas": exec}, tracker)

	require.NoError(t, fabric.Create(&taskfabric.Task{ID: "t-meas", Capability: "rust"}))
	require.NoError(t, sched.executeUnbound(ctx, "t-meas"))

	d := sched.DecisionsSnapshot()
	require.NotEmpty(t, d)
	var cand *CandidateScore
	for i := range d[0].Candidates {
		if d[0].Candidates[i].AgentID == "rust-meas" {
			cand = &d[0].Candidates[i]
		}
	}
	require.NotNil(t, cand)
	assert.InDelta(t, 0.5, cand.Confidence, 1e-9, "measured 0.5 must override the 0.9 prior")
}

func TestNeutralStandsWhenNoPriorWired(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// No ConfidenceSource: the neutral 1.0 default is unchanged (the
	// historical behavior — wiring nothing changes nothing).
	fabric := taskfabric.NewFabric()
	tracker := NewLoadTracker()
	exec := &probingExecutor{id: "rust-neutral", typ: models.AgentType("rust")}
	sched := New(fabric, map[string]CapabilityExecutor{"rust-neutral": exec}, tracker)

	require.NoError(t, fabric.Create(&taskfabric.Task{ID: "t-neutral", Capability: "rust"}))
	require.NoError(t, sched.executeUnbound(ctx, "t-neutral"))

	d := sched.DecisionsSnapshot()
	require.NotEmpty(t, d)
	var cand *CandidateScore
	for i := range d[0].Candidates {
		if d[0].Candidates[i].AgentID == "rust-neutral" {
			cand = &d[0].Candidates[i]
		}
	}
	require.NotNil(t, cand)
	assert.InDelta(t, 1.0, cand.Confidence, 1e-9)
}

// ConfidenceForMeasured: the tracker's own verdict contract.
func TestConfidenceForMeasured(t *testing.T) {
	tracker := NewLoadTracker()

	v, measured := tracker.ConfidenceForMeasured("a", "code")
	assert.Equal(t, 1.0, v)
	assert.False(t, measured, "no history, no override → neutral, unmeasured")

	tracker.SetAgentConfidence("a", 0.7)
	v, measured = tracker.ConfidenceForMeasured("a", "code")
	assert.Equal(t, 0.7, v)
	assert.True(t, measured, "override → measured")

	tracker.SetAgentConfidence("b", -1) // clear
	tracker.End("b", true)
	v, measured = tracker.ConfidenceForMeasured("b", "code")
	assert.Equal(t, 1.0, v)
	assert.True(t, measured, "history → measured (1/1)")
}
