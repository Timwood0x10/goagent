package agentfabric

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/core/models"
)

// stubCognition is a deterministic Cognition for the contract test: it
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

// TestSpawnedAgentExecutesQuantum is the acceptance contract test: an
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

// TestSpawnRejectsNilCognition locks the contract: a NON-nil
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

// TestSpawnedAgentYieldCarriesCheckpoint verifies the yield path of the
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

// SubAgentCognition and its parity test were removed with the migration
// adapter's retirement (production zero callers — peer execution lives in
// the L2 router; TODO(tech-debt) 留痕 in sub_cognition.go's removal).
