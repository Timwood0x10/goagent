// Scenario: gate3 — candidate gate-3 end-to-end (real LLM).
//
// Purpose:
//
//	This scenario is the deep tier of the LLM evolution suite: it runs the
//	FULL candidate verification path with the gate-3 regression check wired
//	in, against a real LLM. A deliberately bad candidate must be REJECTED at
//	gate 3 and a good candidate must pass — proving the whole
//	evidence → candidate → verify pipeline works end to end.
//
// Learning objectives:
//   - How LoadRegressionGate3 builds a gate-3 regression check from a YAML
//     config (configs/ares.local.yaml) via a real LLM client.
//   - How CandidateVerifier runs three gates: static check, evidence replay,
//     and the LLM regression check.
//   - Why a regressing candidate's verdict carries the regression reason
//     (avg drop, win rate, p-value).
//
// Core APIs (with package paths):
//   - evolution.LoadRegressionGate3 (internal/evolution)
//   - evolution.NewCandidateVerifierWithOptions + WithRegressionCheck
//   - evolution.NewCandidate / CandidateVerifier.Verify
//   - evolution.WithRegressionRuns (internal/evolution)
//
// Run:
//
//	go run ./examples/15-llm-evolution-suite gate3
//
// Expected output:
//
//	Bad candidate (a+b+1): success=false status=rejected
//	  reason: regression: preserved-suite avg dropped ... (p=...)
//	Good candidate (add numbers): success=true status=verified
//
// Note: this scenario makes real API calls; WithRegressionRuns(2) keeps the
// call count low for rate-limited providers.
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/Timwood0x10/ares/internal/agents"
	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/llm"
	"github.com/Timwood0x10/ares/internal/runtime/evolution"
)

// runGate3E2E runs the FULL candidate gate-3 end-to-end against a real LLM:
// it loads the LLM client via LoadRegressionGate3, injects the regression
// check into a CandidateVerifier (WithRegressionCheck), and verifies a
// deliberately bad candidate — which must be REJECTED at gate 3 — and a good
// candidate, which must pass.
func runGate3E2E(ctx context.Context, client *llm.Client) {
	// ── Step 1: Seed a profile store with the stable (good) instructions ──
	// The stable instructions are the preserved behavior baseline: gate 3
	// compares every candidate against them over the preserved case suite.
	// SetStable places the profile in the read-only stable region.
	profileStore := evolution.NewProfileStore()
	stable := &agents.AgentProfile{Role: "coder", Instructions: goodStrategy}
	if err := profileStore.Update(stable); err != nil {
		log.Fatalf("update profile: %v", err)
	}
	if err := profileStore.SetStable("coder", stable); err != nil {
		log.Fatalf("set stable profile: %v", err)
	}

	// ── Step 2: Seed failure evidence for gate 2 (evidence replay) ──
	// Gate 2 requires every EvidenceIDs reference to exist in the evidence
	// store as KindDimensionEval; seedEvidence writes such records so the
	// candidate's evidence chain is real.
	evStore := evidence.NewMemoryStore()
	seedEvidence(ctx, evStore, "coder", []string{"ev-bad"})

	// ── Step 3: Build the gate-3 regression check from the real LLM config ──
	// LoadRegressionGate3 reads configs/ares.local.yaml, builds the LLM
	// client (with a lenient gate-3 circuit breaker), wires it into an
	// LLMArenaScorer + CandidateRegressionChecker, and returns a check
	// function injectable into the verifier.
	check, err := evolution.LoadRegressionGate3(profileStore, configPath, preservedCases,
		evolution.WithRegressionRuns(2),
	)
	if err != nil {
		log.Fatalf("load gate3: %v", err)
	}
	log.Printf("gate-3 regression check built (provider from %s)", configPath)

	// ── Step 4: Wire the check into a CandidateVerifier ──
	// WithEvidenceStore satisfies gate 2; WithRegressionCheck injects the
	// LLM regression check as gate 3. The verifier then runs all three gates
	// per candidate.
	verifier := evolution.NewCandidateVerifierWithOptions(
		evolution.WithEvidenceStore(evStore),
		evolution.WithRegressionCheck(check),
	)
	log.Printf("candidate verifier wired with gate-3 regression check")

	// ── Step 5: A deliberately bad candidate must be rejected at gate 3 ──
	// badStrategy ("a+b+1") is a harmless-but-wrong instruction: gate 3
	// scores it against the preserved cases, detects a significant drop, and
	// rejects the candidate with the regression reason attached.
	bad := evolution.NewCandidate(evolution.CandidateInstruction, "coder",
		badStrategy, "off-by-one refactor", []string{"ev-bad"})
	badResult := verifier.Verify(bad)
	fmt.Println("── Bad candidate (a+b+1) ──")
	fmt.Printf("  success: %v\n", badResult.Success)
	fmt.Printf("  reason:  %s\n", badResult.Reason)
	fmt.Printf("  status:  %s\n", bad.Status)
	if badResult.Success {
		log.Fatalf("BUG: a regressing candidate passed gate 3")
	}
	log.Printf("bad candidate correctly REJECTED at gate 3")

	// ── Step 6: A good candidate (keeps good behavior) should pass gate 3 ──
	// The good candidate reuses the stable instruction text, so gate 3
	// should measure no regression and mark it verified. Grading is
	// stochastic, so a miss is logged as a note, not a hard failure.
	good := evolution.NewCandidate(evolution.CandidateInstruction, "coder",
		goodStrategy, "clarify instructions", []string{"ev-bad"})
	goodResult := verifier.Verify(good)
	fmt.Println("── Good candidate (add numbers) ──")
	fmt.Printf("  success: %v\n", goodResult.Success)
	fmt.Printf("  reason:  %s\n", goodResult.Reason)
	fmt.Printf("  status:  %s\n", good.Status)
	if !goodResult.Success {
		log.Printf("note: good candidate did not pass gate 3 (stochastic model grading); not a hard failure")
	} else {
		log.Printf("good candidate passed gate 3")
	}
	log.Printf("gate-3 e2e done")
}
