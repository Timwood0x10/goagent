package sdk

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Timwood0x10/ares/internal/agentsyscall"
)

// TestSDKPlanLoopLifetimeWired is the serve/SDK parity acceptance: the
// SDK path's create_plan must accept the `loop` option — before this the
// shared schema advertised a parameter that always failed with "plan loop
// requires a kernel loop lifetime". The kernel is built with the runtime's
// lifetime ctx (cancelled in Close), so SDK plan loops cannot outlive the
// Runtime — the same lifecycle the serve path gets.
//
// The probe calls CreatePlan with a minimal loop spec and asserts the error
// is NOT the missing-lifetime sentinel: a no-executor environment may fail
// later (scheduling has no candidate), but the lifetime check happens first
// in startPlanLoop, so reaching any other error proves the ctx was injected.
func TestSDKPlanLoopLifetimeWired(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()
	rt.ensureScheduler()

	if rt.syscallKernel == nil {
		t.Fatal("T4: syscallKernel must be wired by ensureScheduler")
	}

	_, err := rt.syscallKernel.CreatePlan(context.Background(), agentsyscall.CreatePlanArgs{
		Steps: []agentsyscall.PlanStepArgs{{ID: "s1", Capability: "analysis"}},
		Loop:  &agentsyscall.PlanLoopArgs{MaxRounds: 1, Until: "all_succeeded"},
	})
	if err != nil && strings.Contains(err.Error(), "requires a kernel loop lifetime") {
		t.Fatalf("T4: SDK kernel must carry the runtime loop lifetime, got: %v", err)
	}

	// Control: the same call with the loop lifetime cancelled must be
	// rejected — proving the injected ctx actually governs the loop.
	rt.cancel()
	_, err = rt.syscallKernel.CreatePlan(context.Background(), agentsyscall.CreatePlanArgs{
		Steps: []agentsyscall.PlanStepArgs{{ID: "s2", Capability: "analysis"}},
		Loop:  &agentsyscall.PlanLoopArgs{MaxRounds: 1},
	})
	if err == nil {
		t.Fatal("T4: a cancelled runtime lifetime must reject new plan loops")
	}
	if strings.Contains(err.Error(), "requires a kernel loop lifetime") {
		t.Fatalf("T4: cancellation must surface as ctx error, not missing lifetime: %v", err)
	}
}

// TestSDKPlanLoopControlSurface is the other half of the parity acceptance: plan
// loops must be OBSERVABLE and STOPPABLE from the SDK, not just startable.
// Without exported accessors a `loop` plan would be a goroutine the embedding
// program can neither list nor cancel before Close.
func TestSDKPlanLoopControlSurface(t *testing.T) {
	rt := NewRuntime(WithOllama("llama3.2"), WithTrace(false))
	defer rt.Close()

	// Before the first Submit the kernel is unwired: the accessors must be
	// safe no-ops rather than nil-panics.
	if loops := rt.LivePlanLoops(); len(loops) != 0 {
		t.Fatalf("unwired runtime must report no live loops, got %v", loops)
	}
	if err := rt.StopPlanLoop("nope"); !errors.Is(err, agentsyscall.ErrPlanLoopNotFound) {
		t.Fatalf("unwired StopPlanLoop = %v, want ErrPlanLoopNotFound", err)
	}

	rt.ensureScheduler()
	// Wired but idle: still no loops, and an unknown ID is reported cleanly.
	if loops := rt.LivePlanLoops(); len(loops) != 0 {
		t.Fatalf("idle runtime must report no live loops, got %v", loops)
	}
	if err := rt.StopPlanLoop("unknown-plan"); !errors.Is(err, agentsyscall.ErrPlanLoopNotFound) {
		t.Fatalf("StopPlanLoop(unknown) = %v, want ErrPlanLoopNotFound", err)
	}
}
