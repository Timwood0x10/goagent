package agentfabric

import (
	"context"
	"testing"

	"github.com/Timwood0x10/ares/internal/core/models"
)

// TestSpawnLoadsExperiencePrior verifies the G1 contract
// (aresos-agentos-plan G1: Memory Distill 挂到 agent 生命周期): a spawned
// agent loads the distilled prior experience as its initial cognitive context.
// The prior is readable via the standard CognitiveState path, so a fresh agent
// of the same capability starts with reusable experience instead of a blank
// slate.
func TestSpawnLoadsExperiencePrior(t *testing.T) {
	ctx := context.Background()
	f := NewFabric()

	prior := map[string]any{
		"capability": "ffi-safety",
		"solution":   "prefer checked accessors over raw FFI pointers",
		"constraint": "never cross the ABI boundary with unsized types",
	}
	if _, err := f.Spawn(ctx, SpawnSpec{
		Identity:        "expert-ffi",
		Capabilities:    []string{"ffi-safety"},
		ExperiencePrior: prior,
	}); err != nil {
		t.Fatalf("spawn with prior: %v", err)
	}

	cs, err := f.CognitiveState("expert-ffi")
	if err != nil {
		t.Fatalf("CognitiveState: %v", err)
	}
	if cs.SchemaVersion != CognitiveStateSchemaVersion {
		t.Fatalf("prior-carrying state must be versioned, got %d", cs.SchemaVersion)
	}
	if cs.Context == nil {
		t.Fatal("spawned agent must start with the distilled experience prior as Context")
	}
	got, ok := cs.Context.(map[string]any)
	if !ok {
		t.Fatalf("Context must carry the prior, got %T", cs.Context)
	}
	if got["capability"] != "ffi-safety" || got["solution"] == "" {
		t.Fatalf("Context prior content mismatch: %v", got)
	}
}

// TestSpawnWithoutPriorStartsBlank verifies the zero-value contract
// code_rules: an ExperiencePrior of nil leaves the agent with an
// empty cognitive state — the pre-G1 behavior is unchanged.
func TestSpawnWithoutPriorStartsBlank(t *testing.T) {
	ctx := context.Background()
	f := NewFabric()
	if _, err := f.Spawn(ctx, SpawnSpec{
		Identity:     "blank",
		Capabilities: []string{"code"},
	}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	cs, err := f.CognitiveState("blank")
	if err != nil {
		t.Fatalf("CognitiveState: %v", err)
	}
	if cs.Context != nil {
		t.Fatalf("agent without prior must start blank, got Context=%v", cs.Context)
	}
}

// TestSpawnPriorCoexistsWithCognition verifies the G1 prior does not interfere
// with the A1 execution body: an agent that carries both a prior and an
// injected Cognition can run a quantum — Memory Distill feeds the execution
// loop (as initial context), it does not replace it.
func TestSpawnPriorCoexistsWithCognition(t *testing.T) {
	ctx := context.Background()
	f := NewFabric()
	if _, err := f.Spawn(ctx, SpawnSpec{
		Identity:        "expert-code",
		Capabilities:    []string{"code"},
		ExperiencePrior: "prior: reuse the verified pattern",
		CognitionFactory: func([]string) Cognition {
			return &completingCognition{}
		},
	}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	a, err := f.Get("expert-code")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	out, err := a.ExecuteStep(ctx, models.NewTask("t-g1", models.AgentType("code"), nil))
	if err != nil {
		t.Fatalf("ExecuteStep: %v", err)
	}
	if !out.Done {
		t.Fatal("cognition must complete the quantum")
	}
	// The prior is still readable after the quantum ran.
	cs, err := f.CognitiveState("expert-code")
	if err != nil {
		t.Fatalf("CognitiveState: %v", err)
	}
	if cs.Context == nil {
		t.Fatal("prior must survive the executed quantum")
	}
}

// completingCognition completes every task in one quantum.
type completingCognition struct{}

func (c *completingCognition) ExecuteStep(_ context.Context, task *models.Task) (*StepOutcome, error) {
	res := models.NewTaskResult(task.TaskID, task.AgentType)
	res.SetSuccess(nil, "completed")
	return &StepOutcome{Done: true, Result: res}, nil
}
