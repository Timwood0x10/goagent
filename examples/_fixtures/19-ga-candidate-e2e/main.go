// Command 19-ga-candidate-e2e runs a REAL multi-generation GA evolution loop
// end to end and turns the evolved champion into a candidate:
//
//	failure evidence cluster → base strategy (stable instructions)
//	  → Population (init) → N generations of {ScoreAgents → Evolve (mutation +
//	  crossover + tournament selection + elitism)} → BestStrategy
//	  → Candidate (GA champion diff) → CandidateVerifier.Verify (gates 1/2)
//
// This is the "real GA" counterpart of the deterministic single-shot mutation
// demo: it shows the full Darwinian loop — population, fitness scoring,
// selection, crossover, mutation, and elite survival — with complete per-
// generation logs (best/avg/worst fitness, population size, diversity).
//
// It is fully offline and reproducible: the mutator, crossover, and population
// are all seeded (seed 42) and fitness is a deterministic heuristic over the
// strategy's prompt template and parameters, so no real LLM is needed. Gate 3
// (LLM regression) is intentionally not attached — see examples/17 and 18 for
// the real-LLM gate-3 path.
//
// Learning objectives:
//   - How to drive a multi-generation GA loop: Population init → per-
//     generation {ScoreAgents → Evolve} → BestStrategy.
//   - How the GA's OWN output (fitness plateau) drives a diagnosis and a fix
//     (the naive-vs-fixed fitness feedback loop at the end of the run).
//   - How the evolved champion becomes a Candidate and flows through the
//     standard CandidateVerifier gates 1/2.
//
// Core APIs (with package paths):
//   - ares_genome.NewPopulation (internal/ares_evolution/genome)
//   - (*Population).ScoreAgents / (*Population).Evolve / (*Population).Stats
//   - (*Population).BestStrategy / (*Population).BestEverScore
//   - mutation.NewMutator (internal/ares_evolution/mutation)
//   - ares_genome.NewCrossover (internal/ares_evolution/genome)
//   - evolution.NewCandidate / CandidateVerifier (internal/evolution)
//
// Run from the repo root:
//
//	go run ./examples/19-ga-candidate-e2e
//
// Expected output:
//
//	population initialized → 6 generations of best/avg/worst fitness
//	champion: prompt=... bestScore=0.950
//	FEEDBACK LOOP CLOSED: best 0.850 -> 0.950
//	candidate: diff=... status=verified
//	reproducibility OK: same-seed GA run converged to the same champion
//
// A full transcript is written to
// ./examples/19-ga-candidate-e2e/logs/run-<ts>.log.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Timwood0x10/ares/internal/agents"
	"github.com/Timwood0x10/ares/internal/evidence"
	ares_genome "github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
	"github.com/Timwood0x10/ares/internal/runtime/evolution"
)

// promptPool is the instruction pool the GA mutator draws from when mutating a
// strategy's prompt template.
var promptPool = []string{
	"Solve the task step by step and verify the result before answering.",
	"Write clean, well-commented code and double-check edge cases.",
	"Explain your reasoning briefly, then give the final answer.",
	"Re-read the task, extract the exact inputs, and return the numeric result only.",
}

// gaParams is the initial parameter vector the GA mutates (temperature,
// max_steps, top_k). Fitness rewards sane values so the population evolves
// toward them.
var gaParams = map[string]any{
	"temperature": 0.7,
	"max_steps":   10,
	"top_k":       50,
}

// seedEvidence appends failure-cluster evidence records with explicit IDs for
// a role so gate 2 (evidence existence) and the GenerateGA cluster gate pass.
func seedEvidence(ctx context.Context, store evidence.Store, role string, ids []string) {
	for _, id := range ids {
		rec := evidence.NewEvidence("result_verifier", evidence.KindDimensionEval,
			map[string]any{"verdict": "fail"},
			evidence.WithMetadata("role", role),
		)
		rec.ID = id
		if err := store.Append(ctx, rec); err != nil {
			log.Fatalf("seed evidence %s: %v", id, err)
		}
	}
}

// paramFloat reads a strategy parameter that may be stored as either an int
// (initial gaParams literal) or a float64 (mutated value). A naive
// `.(float64)` assertion silently skips int values — the fitness reward is
// then never applied, which the first run's log revealed (best stuck at 0.850
// although max_steps=10 is in the rewarded range). Type-switching fixes the
// feedback loop: the reward now actually influences selection.
func paramFloat(params map[string]any, key string) (float64, bool) {
	v, ok := params[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	default:
		return 0, false
	}
}

// fitnessScoreNaive is the FIRST version of the fitness function, kept to
// demonstrate the feedback loop: it asserts params with a naive `.(float64)`,
// so int-valued parameters (max_steps=10 in gaParams) silently skip their
// reward. Running this version first lets the algorithm's OWN output (best
// stuck at 0.850 despite max_steps being in the rewarded range) drive the
// diagnosis and the fix below.
func fitnessScoreNaive(s *mutation.Strategy) float64 {
	score := 0.5 // baseline

	prompt := s.PromptTemplate
	for _, kw := range []string{
		"step by step", "verify", "edge cases", "numeric result", "re-read",
	} {
		if strings.Contains(strings.ToLower(prompt), kw) {
			score += 0.1
		}
	}
	if strings.Contains(prompt, "Add the numbers precisely") {
		score -= 0.3
	}

	// Naive assertion: int params (e.g. max_steps: 10) fail this and their
	// reward is silently dropped — the fitness signal is incomplete.
	if t, ok := s.Params["temperature"].(float64); ok {
		if t >= 0.4 && t <= 0.9 {
			score += 0.15
		} else {
			score -= 0.15
		}
	}
	if steps, ok := s.Params["max_steps"].(float64); ok {
		if steps >= 5 && steps <= 20 {
			score += 0.1
		} else {
			score -= 0.1
		}
	}
	return score
}

// runEvolution creates a seeded population and runs 6 generations with the
// given fitness scorer, returning the champion and its best score. Sharing one
// helper keeps the feedback-loop demonstration (naive run vs fixed run) free of
// copy-paste.
func runEvolution(
	ctx context.Context,
	base *mutation.Strategy,
	scorer func(*mutation.Strategy) float64,
) (*mutation.Strategy, float64) {
	mutator, err := mutation.NewMutator(
		mutation.WithPromptPool(promptPool),
		mutation.WithSeed(42),
	)
	if err != nil {
		log.Fatalf("build mutator: %v", err)
	}
	crosser, err := ares_genome.NewCrossover(ares_genome.WithSeed(42))
	if err != nil {
		log.Fatalf("build crossover: %v", err)
	}
	population, err := ares_genome.NewPopulation(ctx, base, mutator,
		ares_genome.WithPopulationSize(16),
		ares_genome.WithEliteCount(3),
		ares_genome.WithMutationRate(0.25),
		ares_genome.WithSurvivalRate(0.6),
		ares_genome.WithTournamentSelection(3),
		ares_genome.WithPopulationSeed(42),
	)
	if err != nil {
		log.Fatalf("create population: %v", err)
	}
	log.Printf("population initialized: size=%d generation=%d", population.Size, population.Generation)

	for gen := 1; gen <= 6; gen++ {
		// Score every agent BEFORE evolving — Evolve rejects unevaluated
		// (score=-1) agents.
		population.ScoreAgents(scorer)
		if err := population.Evolve(ctx, mutator, crosser); err != nil {
			log.Fatalf("evolve generation %d: %v", gen, err)
		}
		stats := population.Stats()
		best := population.BestStrategy()
		bestPrompt := ""
		if best != nil {
			bestPrompt = best.PromptTemplate
			if len(bestPrompt) > 60 {
				bestPrompt = bestPrompt[:60] + "..."
			}
		}
		log.Printf("Gen %d: best=%.3f avg=%.3f worst=%.3f size=%d bestPrompt=%q",
			stats.Generation, stats.BestScore, stats.AvgScore, stats.WorstScore,
			stats.Size, bestPrompt)
	}
	return population.BestStrategy(), population.BestEverScore()
}

// fitnessScore is the FIXED fitness function. It reads parameters through
// paramFloat (int/float64 type switch), so every reward — including max_steps —
// actually influences selection. The fix is driven by the naive run's feedback
// below: the algorithm told us its fitness signal was incomplete, we adjusted
// the scorer, and the re-run must show the champion climbing to the full score.
func fitnessScore(s *mutation.Strategy) float64 {
	score := 0.5 // baseline

	prompt := s.PromptTemplate
	for _, kw := range []string{
		"step by step", "verify", "edge cases", "numeric result", "re-read",
	} {
		if strings.Contains(strings.ToLower(prompt), kw) {
			score += 0.1
		}
	}
	// A prompt identical to the raw seed template is a non-mutation: penalize.
	if strings.Contains(prompt, "Add the numbers precisely") {
		score -= 0.3
	}

	if t, ok := paramFloat(s.Params, "temperature"); ok {
		if t >= 0.4 && t <= 0.9 {
			score += 0.15
		} else {
			score -= 0.15
		}
	}
	if steps, ok := paramFloat(s.Params, "max_steps"); ok {
		if steps >= 5 && steps <= 20 {
			score += 0.1
		} else {
			score -= 0.1
		}
	}
	return score
}

// fitnessBreakdown prints the per-term composition of the fitness score for a
// champion, so the feedback loop is auditable: it shows exactly which rewards
// were (or were not) applied, which is how the naive run's missing max_steps
// reward was spotted.
func fitnessBreakdown(s *mutation.Strategy) {
	prompt := s.PromptTemplate
	score := 0.5
	terms := "baseline=0.500"
	for _, kw := range []string{
		"step by step", "verify", "edge cases", "numeric result", "re-read",
	} {
		if strings.Contains(strings.ToLower(prompt), kw) {
			score += 0.1
			terms += fmt.Sprintf(" +0.100[keyword:%s]", kw)
		}
	}
	if strings.Contains(prompt, "Add the numbers precisely") {
		score -= 0.3
		terms += " -0.300[seed-template]"
	}
	if t, ok := paramFloat(s.Params, "temperature"); ok {
		if t >= 0.4 && t <= 0.9 {
			score += 0.15
			terms += " +0.150[temperature]"
		} else {
			score -= 0.15
			terms += " -0.150[temperature]"
		}
	}
	if steps, ok := paramFloat(s.Params, "max_steps"); ok {
		if steps >= 5 && steps <= 20 {
			score += 0.1
			terms += " +0.100[max_steps]"
		} else {
			score -= 0.1
			terms += " -0.100[max_steps]"
		}
	} else {
		terms += " [max_steps reward NOT applied: param missing or wrong type]"
	}
	log.Printf("  fitness breakdown: total=%.3f <-%s", score, terms)
}

func main() {
	ctx := context.Background()
	setupLog()
	log.Printf("=== REAL GA evolution closed-loop demo (offline, reproducible) ===")

	// ── 1. Seed a profile store with the stable (good) instructions. ──
	profileStore := evolution.NewProfileStore()
	stable := &agents.AgentProfile{
		Role:         "coder",
		Instructions: "Add the numbers precisely and return the numeric result only.",
	}
	if err := profileStore.Update(stable); err != nil {
		log.Fatalf("update profile: %v", err)
	}
	if err := profileStore.SetStable("coder", stable); err != nil {
		log.Fatalf("set stable profile: %v", err)
	}

	// ── 2. Seed a failure cluster (>= MinFailureClusterSize = 2). ──
	evStore := evidence.NewMemoryStore()
	seedEvidence(ctx, evStore, "coder", []string{"ev-1", "ev-2"})

	// ── 3. Base strategy: the stable instructions + parameter vector. ──
	base := &mutation.Strategy{
		PromptTemplate: stable.Instructions,
		Params:         gaParams,
	}

	// ── 4. PHASE A — run GA with the NAIVE fitness function. ──
	// This is the feedback loop's first observation: the algorithm runs, but
	// its output tells us something is off — best fitness plateaus below the
	// theoretical maximum because the int-valued max_steps param silently
	// misses its reward (naive `.(float64)` assertion).
	log.Printf("═══ PHASE A: GA with NAIVE fitness (feedback source) ═══")
	naiveChampion, naiveBest := runEvolution(ctx, base, fitnessScoreNaive)
	if naiveChampion == nil {
		log.Fatalf("BUG: no champion after naive evolution")
	}
	log.Printf("naive champion: prompt=%q params=%v best=%.3f",
		naiveChampion.PromptTemplate, naiveChampion.Params, naiveBest)
	fitnessBreakdown(naiveChampion)

	// ── 5. FEEDBACK — read what the algorithm told us. ──
	// The breakdown shows the max_steps reward is NOT applied: gaParams stores
	// max_steps as an int, which the naive assertion drops. The fitness signal
	// is incomplete, so the search has no gradient toward better max_steps.
	log.Printf("FEEDBACK: max_steps reward missing in naive fitness "+
		"(int param skipped by `.(float64)`); best plateaus at %.3f", naiveBest)

	// ── 6. ADJUST — fix the scorer (paramFloat type switch). ──
	log.Printf("ADJUST: switch to paramFloat-aware fitness (int/float64 both rewarded)")

	// ── 7. PHASE B — re-run GA with the FIXED fitness function. ──
	// Same seed, same operators, only the scorer changed: the champion must
	// now climb to the full score, proving the adjustment fixed the signal.
	log.Printf("═══ PHASE B: GA with FIXED fitness (adjustment verified) ═══")
	champion, bestScore := runEvolution(ctx, base, fitnessScore)
	if champion == nil {
		log.Fatalf("BUG: no champion after fixed evolution")
	}
	log.Printf("champion: prompt=%q params=%v bestScore=%.3f",
		champion.PromptTemplate, champion.Params, bestScore)
	fitnessBreakdown(champion)

	// ── 8. Verify the adjustment actually moved the result. ──
	if bestScore <= naiveBest {
		log.Fatalf("BUG: fixed fitness did not improve the champion (naive=%.3f fixed=%.3f)",
			naiveBest, bestScore)
	}
	log.Printf("FEEDBACK LOOP CLOSED: best %.3f -> %.3f (max_steps reward now counts)",
		naiveBest, bestScore)

	// ── 9. Turn the evolved champion into a candidate. ──
	verifier := evolution.NewCandidateVerifierWithOptions(
		evolution.WithEvidenceStore(evStore),
	)
	candidate := evolution.NewCandidate(
		evolution.CandidateInstruction, "coder",
		champion.PromptTemplate,
		fmt.Sprintf("GA evolution champion (gen %d)", 6),
		[]string{"ev-1", "ev-2"},
	)
	result := verifier.Verify(candidate)
	log.Printf("candidate: diff=%q status=%s verifySuccess=%v reason=%q",
		candidate.Diff, candidate.Status, result.Success, result.Reason)

	// ── 10. Reproducibility: a fresh same-seed run must converge identically. ──
	champion2, _ := runEvolution(ctx, base, fitnessScore)
	if champion2 == nil || champion2.PromptTemplate != champion.PromptTemplate {
		log.Fatalf("BUG: repeat GA run diverged (non-deterministic): %q vs %q",
			champion2.PromptTemplate, champion.PromptTemplate)
	}
	log.Printf("reproducibility OK: same-seed GA run converged to the same champion")

	fmt.Println("── REAL GA evolution closed-loop demo done ──")
	log.Printf("gate-3 note: LLM regression gate not attached; see examples/17,18 for the real-LLM path")
}

// setupLog tees all output to stdout and a timestamped log file.
func setupLog() {
	logDir := filepath.Join("examples", "19-ga-candidate-e2e", "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		log.Fatalf("create log dir: %v", err)
	}
	name := filepath.Join(logDir, fmt.Sprintf("run-%s.log", time.Now().Format("20060102-150405")))
	f, err := os.Create(name)
	if err != nil {
		log.Fatalf("create log file: %v", err)
	}
	multi := io.MultiWriter(os.Stdout, f)
	log.SetOutput(multi)
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.Printf("log file: %s", name)
}
