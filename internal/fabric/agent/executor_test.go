package agentfabric

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/agents/base"
	"github.com/Timwood0x10/ares/internal/agents/sub"
	"github.com/Timwood0x10/ares/internal/core/models"
)

// stubCognition is a deterministic Cognition for the A1 contract test: it
// records the task it ran and returns a fixed outcome, so the test asserts
// the wiring (spawn → ExecuteStep) without depending on the LLM stack.
type stubCognition struct {
	ran bool
	out *StepOutcome
	err error
}

func (c *stubCognition) ExecuteStep(_ context.Context, task *models.Task) (*StepOutcome, error) {
	c.ran = true
	return c.out, c.err
}

// TestSpawnedAgentExecutesQuantum is the A1 acceptance contract test: an
// agent spawned with a CognitionFactory is directly executable — one
// ExecuteStep call runs one quantum and produces an outcome (the plan's
// "spawn → 喂 task → 跑一个 quantum → 有结果/checkpoint").
func TestSpawnedAgentExecutesQuantum(t *testing.T) {
	f := NewFabric()
	out := &StepOutcome{Done: true, Result: models.NewTaskResult("t1", "code")}
	cog := &stubCognition{out: out}

	a, err := f.Spawn(context.Background(), SpawnSpec{
		Identity: "worker-1", Capabilities: []string{"code"},
		CognitionFactory: func(capabilities []string) Cognition { return cog },
	})
	if err != nil {
		t.Fatalf("Spawn with CognitionFactory: %v", err)
	}

	// The spawned agent is executable immediately (no extra wiring).
	res, err := a.ExecuteStep(context.Background(), models.NewTask("t1", "code", nil))
	if err != nil {
		t.Fatalf("ExecuteStep: %v", err)
	}
	if !cog.ran {
		t.Fatal("CognitionFactory-produced Cognition must have been invoked")
	}
	if !res.Done || res.Result == nil || res.Result.TaskID != "t1" {
		t.Fatalf("quantum outcome must be Done with result, got Done=%v Result=%+v", res.Done, res.Result)
	}
	// The factory received the declared capabilities (spawn 时的能力传入).
	if a.cognition == nil {
		t.Fatal("Agent must hold the injected Cognition")
	}
}

// TestSpawnRejectsNilCognition locks the N10 contract: a NON-nil
// CognitionFactory that returns nil is a programming error that must surface
// at spawn time instead of silently producing a permanently non-executable
// agent.
func TestSpawnRejectsNilCognition(t *testing.T) {
	f := NewFabric()
	_, err := f.Spawn(context.Background(), SpawnSpec{
		Identity: "broken", Capabilities: []string{"code"},
		CognitionFactory: func([]string) Cognition { return nil },
	})
	require.Error(t, err, "a nil-returning CognitionFactory must fail the spawn")
	assert.ErrorIs(t, err, ErrInvalidSpawnSpec)
	assert.Contains(t, err.Error(), "nil")

	// The failed spawn must leave the fabric untouched: the id is still free
	// and no agent (managed or otherwise) exists under it.
	_, getErr := f.Get("broken")
	assert.ErrorIs(t, getErr, ErrAgentNotFound, "failed spawn must not register the agent")
}

// TestSpawnedAgentYieldCarriesCheckpoint verifies the yield path of the A1
// contract: a quantum that does not complete returns Done=false with a
// resumable checkpoint — the scheduler's yield→resume protocol depends on it.
func TestSpawnedAgentYieldCarriesCheckpoint(t *testing.T) {
	f := NewFabric()
	ckpt := map[string]any{"round": 1}
	cog := &stubCognition{out: &StepOutcome{Done: false, Checkpoint: ckpt}}

	a, err := f.Spawn(context.Background(), SpawnSpec{
		Identity: "worker-2", Capabilities: []string{"code"},
		CognitionFactory: func(capabilities []string) Cognition { return cog },
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	res, err := a.ExecuteStep(context.Background(), models.NewTask("t2", "code", nil))
	if err != nil {
		t.Fatalf("ExecuteStep: %v", err)
	}
	if res.Done {
		t.Fatal("yield quantum must have Done=false")
	}
	if res.Checkpoint == nil {
		t.Fatal("yield quantum must carry a resumable checkpoint")
	}
}

// TestSpawnedAgentWithoutCognitionNotExecutable verifies the negative case:
// an agent spawned without a CognitionFactory is managed but not executable —
// ExecuteStep fails with ErrAgentNotExecutable instead of panicking or
// silently returning an empty success.
func TestSpawnedAgentWithoutCognitionNotExecutable(t *testing.T) {
	f := NewFabric()
	a, err := f.Spawn(context.Background(), SpawnSpec{
		Identity: "bare-1", Capabilities: []string{"code"},
	})
	if err != nil {
		t.Fatalf("Spawn without factory: %v", err)
	}
	if _, err := a.ExecuteStep(context.Background(), models.NewTask("t3", "code", nil)); err != ErrAgentNotExecutable {
		t.Fatalf("ExecuteStep without cognition must fail with ErrAgentNotExecutable, got %v", err)
	}
}

// fakeSubAgent is a minimal sub.Agent for the migration test. It implements
// the full sub.Agent interface (base.Agent + Execute/ExecuteStep) so the
// SubAgentCognition adapter can be exercised against a deterministic outcome.
type fakeSubAgent struct {
	id    string
	typ   models.AgentType
	out   *sub.StepOutcome
	err   error
	calls int
}

func (a *fakeSubAgent) ID() string                  { return a.id }
func (a *fakeSubAgent) Type() models.AgentType      { return a.typ }
func (a *fakeSubAgent) Status() models.AgentStatus  { return models.AgentStatusReady }
func (a *fakeSubAgent) Start(context.Context) error { return nil }
func (a *fakeSubAgent) Stop(context.Context) error  { return nil }
func (a *fakeSubAgent) Process(context.Context, any) (any, error) {
	return nil, nil
}
func (a *fakeSubAgent) ProcessStream(context.Context, any) (<-chan base.AgentEvent, error) {
	return nil, nil
}
func (a *fakeSubAgent) Execute(context.Context, *models.Task) (*models.TaskResult, error) {
	return nil, nil
}
func (a *fakeSubAgent) ExecuteStep(_ context.Context, _ *models.Task) (*sub.StepOutcome, error) {
	a.calls++
	return a.out, a.err
}

// TestSubAgentCognitionSemanticsParity is the migration test: the default
// agentfabric Cognition (SubAgentCognition) delegates to the production sub
// executor and preserves the StepOutcome semantics (Done/Checkpoint/Result)
// exactly — a resumed outcome from the wrapped sub agent surfaces unchanged.
func TestSubAgentCognitionSemanticsParity(t *testing.T) {
	subOut := &sub.StepOutcome{
		Done:       false,
		Checkpoint: map[string]any{"round": 2},
		Result:     nil,
	}
	inner := &fakeSubAgent{id: "sub-1", typ: "code", out: subOut}
	cog := NewSubAgentCognition(inner)

	res, err := cog.ExecuteStep(context.Background(), models.NewTask("t4", "code", nil))
	if err != nil {
		t.Fatalf("ExecuteStep: %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("adapter must delegate to the wrapped sub agent, got %d calls", inner.calls)
	}
	if res.Done != subOut.Done || res.Checkpoint == nil || res.Result != nil {
		t.Fatalf("outcome must match sub semantics, got Done=%v Checkpoint=%v Result=%v",
			res.Done, res.Checkpoint, res.Result)
	}
	if ck, ok := res.Checkpoint.(map[string]any); !ok || ck["round"] != 2 {
		t.Fatalf("checkpoint payload must pass through unchanged, got %v", res.Checkpoint)
	}

	// Completed outcome with a result also passes through.
	done := &sub.StepOutcome{Done: true, Result: models.NewTaskResult("t4", "code")}
	cog2 := NewSubAgentCognition(&fakeSubAgent{id: "sub-2", typ: "code", out: done})
	res2, err := cog2.ExecuteStep(context.Background(), models.NewTask("t4", "code", nil))
	if err != nil {
		t.Fatalf("ExecuteStep (done): %v", err)
	}
	if !res2.Done || res2.Result == nil || res2.Result.TaskID != "t4" {
		t.Fatalf("completed outcome must carry the result, got %+v", res2)
	}
}
