package main

import (
	"context"
	"testing"

	"github.com/Timwood0x10/ares/internal/fabric/agent"
	"github.com/Timwood0x10/ares/internal/storage/postgres/models"
)

// TestG1_FullLoopDistillToSpawnPrior verifies the G1 acceptance
// (aresos-agentos-plan G1: Memory Distill 挂到 agent 生命周期 — 蒸馏异步产出 →
// 经验仓库查询 → spawn 注入) end to end at the wiring layer: once distillation
// has asynchronously produced an experience for an agent (here: a repo query
// result, the same source the production loadExperiencePrior reads), a fresh
// spawn of the SAME capability agent loads that experience as its initial
// cognitive context — not a blank slate. The Distiller's own
// event→experience production is covered by internal/ares_memory; this test
// closes the write→read loop through the production wiring function.
func TestG1_FullLoopDistillToSpawnPrior(t *testing.T) {
	ctx := context.Background()

	// Distillation has produced an experience for the ffi-expert agent
	// (async, event-driven — the Distiller writes it to the repository).
	repo := &stubExpRepo{exps: []*models.Experience{
		{
			Type:        models.ExperienceTypeSuccess,
			Problem:     "FFI pointer safety",
			Solution:    "use checked accessors",
			Constraints: "never unsized ABI types",
			AgentID:     "ffi-expert",
		},
	}}

	// A fresh spawn of the same capability agent loads the prior (G1: 新 spawn
	// 的同 capability agent 能读到该经验先验).
	fab := agentfabric.NewFabric()
	if _, err := fab.Spawn(ctx, agentfabric.SpawnSpec{
		Identity:        "ffi-expert-2",
		Capabilities:    []string{"ffi-safety"},
		ExperiencePrior: loadExperiencePrior(ctx, repo, "ffi-expert"),
	}); err != nil {
		t.Fatalf("spawn: %v", err)
	}

	cs, err := fab.CognitiveState("ffi-expert-2")
	if err != nil {
		t.Fatalf("CognitiveState: %v", err)
	}
	if cs.Context == nil {
		t.Fatal("fresh spawn must start with the distilled experience as its cognitive context")
	}
	m, ok := cs.Context.(map[string]any)
	if !ok {
		t.Fatalf("context = %T, want map[string]any", cs.Context)
	}
	if m["solution"] != "use checked accessors" {
		t.Fatalf("prior solution = %v, want the distilled solution", m["solution"])
	}
}
