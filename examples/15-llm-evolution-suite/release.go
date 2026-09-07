// Scenario: release — candidate release closed loop (real LLM).
//
// Purpose:
//
//	This is the deepest tier of the LLM evolution suite: it runs the FULL
//	candidate release closed loop — failure evidence → verify (gates 1/2/3)
//	→ Release → release-time gate-3 confirm → SetStable → Promote — against a
//	real LLM. It demonstrates that even a candidate which slipped past verify
//	is caught by the release-time regression check before reaching stable.
//
// Learning objectives:
//   - How the SAME gate-3 check is wired into both the verify stage and the
//     release stage (CandidatePipeline.WithReleaseRegressionCheck).
//   - How a release builds a RuntimePatch, submits it to the coordinator,
//     deploys via the deployment pipeline, and promotes to stable on success.
//   - How rollback is carried (RuntimePatch.Rollback restores the old stable
//     instructions).
//
// Core APIs (with package paths):
//   - evolution.NewCandidatePipelineWithOptions + WithReleaseRegressionCheck
//     (internal/evolution)
//   - coordinator.NewEvolutionCoordinator (internal/evolution/coordinator)
//   - patch.NewRegistry (internal/evolution/patch)
//   - (*CandidatePipeline).Release
//
// Run:
//
//	go run ./examples/15-llm-evolution-suite release
//
// Expected output (three scenarios):
//
//  1. bad candidate REJECTED at verify
//  2. manually-verified bad candidate REJECTED at RELEASE (release gate-3)
//  3. good candidate verified and PROMOTED to stable
//
// Note: this scenario makes real API calls; WithRegressionRuns(2) keeps the
// call count low for rate-limited providers.
package main

import (
	"context"
	"log"

	"github.com/Timwood0x10/ares/internal/agents"
	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/llm"
	"github.com/Timwood0x10/ares/internal/runtime/evolution"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/coordinator"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

// runReleaseClosedLoop runs the FULL candidate release closed loop against a
// real LLM:
//
//	failure evidence → CandidateVerifier.Verify (gates 1/2/3)
//	  → CandidatePipelineWithOptions(WithReleaseRegressionCheck).Release
//	  → release-time gate-3 confirm → SetStable → Promote
//
// It wires the SAME gate-3 regression check into both the verify stage and the
// release stage, and exercises three scenarios:
//
//  1. a regressing candidate is REJECTED at verify (gate 3);
//  2. a manually-verified regressing candidate is REJECTED at RELEASE (the
//     release-time gate-3, the subject of this demo);
//  3. a good candidate passes verify and is promoted to stable by Release.
func runReleaseClosedLoop(ctx context.Context, client *llm.Client) {
	// ── Step 1: Seed a profile store with the stable (good) instructions ──
	// The stable region holds the verified behavior baseline; the candidate
	// region is the writable workspace during evolution.
	profileStore := evolution.NewProfileStore()
	stable := &agents.AgentProfile{Role: "coder", Instructions: goodStrategy}
	if err := profileStore.Update(stable); err != nil {
		log.Fatalf("update stable profile: %v", err)
	}
	if err := profileStore.SetStable("coder", stable); err != nil {
		log.Fatalf("set stable profile: %v", err)
	}

	// ── Step 2: Seed failure evidence + candidate store ──
	// The evidence store feeds gate 2; the candidate store tracks submitted
	// candidates and their lifecycle status.
	evStore := evidence.NewMemoryStore()
	seedEvidence(ctx, evStore, "coder", []string{"ev-bad"})
	candStore := evolution.NewCandidateStore()

	// ── Step 3: Wire the ONE gate-3 check into both verify and release ──
	// LoadRegressionGate3 builds the check from configs/ares.local.yaml; the
	// same function is injected into the verifier (verify stage) and into the
	// pipeline (release-time confirm), so a candidate cannot reach stable
	// without passing the regression check at BOTH stages.
	gate3, err := evolution.LoadRegressionGate3(profileStore, configPath, preservedCases,
		evolution.WithRegressionRuns(2),
	)
	if err != nil {
		log.Fatalf("load gate3: %v", err)
	}
	verifier := evolution.NewCandidateVerifierWithOptions(
		evolution.WithEvidenceStore(evStore),
		evolution.WithRegressionCheck(gate3),
	)
	registry := patch.NewRegistry()
	coord := coordinator.NewEvolutionCoordinator(coordinator.DefaultPolicy(), registry)
	pipeline := evolution.NewCandidatePipelineWithOptions(
		candStore, profileStore, registry, coord, nil,
		evolution.WithReleaseRegressionCheck(gate3),
	)
	log.Printf("wired gate-3 regression check into verify + release")

	// ── Step 4 (Scenario 1): regressing candidate rejected at VERIFY ──
	// The bad candidate fails gate 3 during verification: its reason carries
	// the preserved-suite regression (avg drop, p-value).
	log.Printf("── Scenario 1: bad candidate rejected at verify ──")
	bad := evolution.NewCandidate(evolution.CandidateInstruction, "coder",
		badStrategy, "off-by-one refactor", []string{"ev-bad"})
	badResult := verifier.Verify(bad)
	log.Printf("verify: success=%v reason=%q status=%s", badResult.Success, badResult.Reason, bad.Status)
	if badResult.Success {
		log.Fatalf("BUG: regressing candidate passed verify")
	}
	log.Printf("scenario 1 OK: regressing candidate rejected at verify")

	// ── Step 5 (Scenario 2): manually-verified bad candidate rejected at RELEASE ──
	// We bypass verify on purpose (bad2.Verify()) to show the release-time
	// gate-3 is an independent backstop: even a "verified" regressing
	// candidate is rejected by Release before any promotion happens.
	log.Printf("── Scenario 2: manually-verified bad candidate rejected at RELEASE ──")
	bad2 := evolution.NewCandidate(evolution.CandidateInstruction, "coder",
		badStrategy, "off-by-one refactor", []string{"ev-bad"})
	bad2.Verify() // bypass verify on purpose to exercise the release-time gate-3
	candStore.Submit(bad2)
	released, err := pipeline.Release(ctx, bad2.ID)
	log.Printf("release: released=%v err=%v status=%s reason=%q",
		released, err, bad2.Status, bad2.RejectionReason)
	if released {
		log.Fatalf("BUG: release-time gate-3 did not reject the regressing candidate")
	}
	if bad2.Status != evolution.StatusRejected {
		log.Fatalf("BUG: expected rejected status, got %s", bad2.Status)
	}
	log.Printf("scenario 2 OK: release-time gate-3 rejected the regressing candidate")

	// ── Step 6 (Scenario 3): good candidate passes verify and is promoted ──
	// The good candidate reuses the stable instruction text; if it passes
	// verify, Release builds a RuntimePatch (with the old stable text as
	// Rollback), evaluates it via the coordinator, and promotes to stable on
	// success. Grading is stochastic, so a miss is logged, not fatal.
	log.Printf("── Scenario 3: good candidate verified + released ──")
	good := evolution.NewCandidate(evolution.CandidateInstruction, "coder",
		goodStrategy, "clarify instructions", []string{"ev-bad"})
	goodResult := verifier.Verify(good)
	log.Printf("verify: success=%v reason=%q status=%s", goodResult.Success, goodResult.Reason, good.Status)
	if !goodResult.Success {
		log.Printf("note: good candidate did not pass verify (stochastic grading); skipping release")
	} else {
		candStore.Submit(good)
		releasedGood, errGood := pipeline.Release(ctx, good.ID)
		log.Printf("release: released=%v err=%v status=%s", releasedGood, errGood, good.Status)
		if !releasedGood {
			log.Printf("note: good candidate release did not promote (stochastic grading); not a hard failure")
		} else {
			log.Printf("scenario 3 OK: good candidate promoted to stable")
		}
	}

	// ── Step 7: Report the final stable instructions ──
	// After all scenarios, the stable region should still hold the verified
	// good instructions (or the original stable text if release was skipped).
	finalStable := profileStore.GetStable("coder")
	log.Printf("final stable instructions: %q", finalStable.Instructions)
	log.Printf("=== release closed-loop done ===")
}
