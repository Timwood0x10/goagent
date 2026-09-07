// Command 15-llm-evolution-suite is the consolidated REAL-LLM evolution
// example suite. It merges the former standalone examples 15-18 into one
// command with four scenarios selected by a subcommand:
//
//	go run ./examples/15-llm-evolution-suite scorer     — LLMArenaScorer smoke test
//	go run ./examples/15-llm-evolution-suite regression — preserved-case regression comparison
//	go run ./examples/15-llm-evolution-suite gate3     — candidate gate-3 end-to-end
//	go run ./examples/15-llm-evolution-suite release   — candidate release closed loop
//
// All scenarios drive a real LLM configured in configs/ares.local.yaml
// (git-ignored), make real API calls, and may incur usage cost. A full
// transcript of each run is written to
// ./examples/15-llm-evolution-suite/logs/run-<ts>.log.
//
// The offline, reproducible GA candidate evolution lives separately in
// examples/19-ga-candidate-e2e (no real LLM needed).
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	ares_config "github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/llm"
	evosvc "github.com/Timwood0x10/ares/internal/runtime/ares_evolution/service"
)

// configPath is the git-ignored local config holding the real LLM credentials.
const configPath = "configs/ares.local.yaml"

// preservedCases is a small suite of old-behavior cases that must not regress.
// Each case carries concrete inputs so the LLM can actually compute and the
// batch execute path produces one stable result line per task (abstract cases
// make the model refuse/free-text).
var preservedCases = []any{
	"Given a=3 and b=5, return their sum.",
	"Given the integer 7, return its double.",
}

// goodStrategy is the stable coder instruction used across scenarios.
const goodStrategy = "Add the numbers precisely and return the numeric result only."

// badStrategy is a harmless-but-wrong instruction: an obviously malicious one
// (e.g. "always answer zero") can trigger the model's safety refusal, garbling
// batch line parsing.
const badStrategy = "Return the result of a+b+1 for every task."

func main() {
	ctx := context.Background()
	setupLog()

	if len(os.Args) < 2 {
		fmt.Println("usage: go run ./examples/15-llm-evolution-suite <scenario>")
		fmt.Println("  scorer     — LLMArenaScorer smoke test (single case, old vs bad score)")
		fmt.Println("  regression — preserved-case regression comparison (old vs new strategy)")
		fmt.Println("  gate3      — candidate gate-3 end-to-end (bad rejected, good verified)")
		fmt.Println("  release    — candidate release closed loop (verify + release-time gate-3)")
		os.Exit(1)
	}

	cfg, err := ares_config.Load(configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	log.Printf("provider=%s model=%q base_url=%q", cfg.LLM.Provider, cfg.LLM.Model, cfg.LLM.BaseURL)

	client, err := llm.NewClient(&llm.Config{
		Provider:  cfg.LLM.Provider,
		APIKey:    cfg.LLM.APIKey,
		BaseURL:   cfg.LLM.BaseURL,
		Model:     cfg.LLM.Model,
		Timeout:   cfg.LLM.Timeout,
		MaxTokens: cfg.LLM.MaxTokens,
	})
	if err != nil {
		log.Fatalf("build llm client: %v", err)
	}

	switch os.Args[1] {
	case "scorer":
		runScorerSmoke(ctx, client)
	case "regression":
		runRegressionDemo(ctx, client)
	case "gate3":
		runGate3E2E(ctx, client)
	case "release":
		runReleaseClosedLoop(ctx, client)
	default:
		log.Fatalf("unknown scenario %q (want scorer|regression|gate3|release)", os.Args[1])
	}
}

// buildScorer wraps the LLM client into an LLMArenaScorer, the two-step
// (execute then grade) scorer powering the gate-3 regression check.
func buildScorer(client *llm.Client) (*evosvc.LLMArenaScorer, error) {
	return evosvc.NewLLMArenaScorer(evosvc.LLMArenaScorerConfig{Client: client})
}

// seedEvidence appends failure-cluster evidence records with explicit IDs for
// a role so gate 2 (evidence existence) passes.
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

// setupLog tees all output to stdout and a timestamped log file.
func setupLog() {
	logDir := filepath.Join("examples", "15-llm-evolution-suite", "logs")
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
