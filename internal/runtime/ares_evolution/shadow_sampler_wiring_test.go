package evolution

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// TestWiring_ShadowSampler_WiredInBootstrapShape locks the sampler wiring
// against
// the SHAPE bootstrap actually uses: EnableDreamCycle=false but
// EnableScheduler=true. NewWiredEvolutionSystem builds a DreamCycle whenever
// EITHER flag is set (needDreamCycle = EnableDreamCycle || EnableScheduler), so
// a `system.DreamCycle == nil` guard would silently skip the sampler in every
// production config — the exact path the sampler exists to fix.
func TestWiring_ShadowSampler_WiredInBootstrapShape(t *testing.T) {
	defer discardLogs()()
	base := &mutation.Strategy{
		ID:     "bootstrap-root",
		Params: map[string]any{"temperature": 0.7},
	}
	cfg := DefaultSystemConfig()
	cfg.PopulationSize = 4
	// Bootstrap's exact shape (bootstrap_steps.go): DreamCycle off, scheduler on.
	cfg.EnableDreamCycle = false
	cfg.EnableScheduler = true
	cfg.EventStore = newMockCallbackRegistrarForTest()
	cfg.StrategyStore = newMockStrategyStore()
	cfg.RollbackPolicyConfig = RollbackPolicyConfig{Enabled: true}
	cfg.ShadowEvalConfig = ShadowEvaluationConfig{Enabled: true, MinSamples: 3, MinWinRate: 0.55}

	system, err := NewWiredEvolutionSystem(base, cfg)
	if err != nil {
		t.Fatalf("NewWiredEvolutionSystem failed: %v", err)
	}
	defer Shutdown(system)

	if system.Lifecycle == nil {
		t.Fatal("expected non-nil Lifecycle")
	}
	if system.ShadowEvaluator == nil {
		t.Fatal("expected non-nil ShadowEvaluator")
	}
	if system.Lifecycle.sampler == nil {
		t.Fatal("P0-9 regression: shadow sampler must be wired when DreamCycle " +
			"does not feed comparisons (EnableDreamCycle=false)")
	}
}

// TestWiring_ShadowSampler_NotWiredWhenDreamCycleFeeds locks the exclusivity:
// when DreamCycle IS the feeder it owns StartShadow/RecordResult, and wiring the
// sampler too would reset its accumulated comparisons on every Submit.
func TestWiring_ShadowSampler_NotWiredWhenDreamCycleFeeds(t *testing.T) {
	defer discardLogs()()
	base := &mutation.Strategy{
		ID:     "bootstrap-root",
		Params: map[string]any{"temperature": 0.7},
	}
	cfg := DefaultSystemConfig()
	cfg.PopulationSize = 4
	cfg.EnableDreamCycle = true
	cfg.EnableScheduler = true
	cfg.EventStore = newMockCallbackRegistrarForTest()
	cfg.StrategyStore = newMockStrategyStore()
	cfg.RollbackPolicyConfig = RollbackPolicyConfig{Enabled: true}
	cfg.ShadowEvalConfig = ShadowEvaluationConfig{Enabled: true, MinSamples: 3, MinWinRate: 0.55}

	system, err := NewWiredEvolutionSystem(base, cfg)
	if err != nil {
		t.Fatalf("NewWiredEvolutionSystem failed: %v", err)
	}
	defer Shutdown(system)

	if system.Lifecycle == nil {
		t.Fatal("expected non-nil Lifecycle")
	}
	if system.Lifecycle.sampler != nil {
		t.Fatal("sampler must NOT be wired when DreamCycle is the shadow feeder " +
			"(exactly one feeder owns StartShadow/RecordResult)")
	}
}

// TestWiring_ShadowSampler_BudgetGated locks review finding #1: when an LLM
// scorer is wired, the shadow path MUST draw from the same per-generation
// budget as population scoring. Before this fix buildShadowEvaluator wired the
// raw cfg.Scorer, so every Submit's Prime ran minSamples×2 LLM calls with zero
// accounting against MaxLLMCallsPerGeneration. With the tiered scorer wired as
// the shadow scorer, Prime's Evaluate calls go through TieredScorer.Score,
// which calls TryRecordLLMCall before each LLM score and falls back to the
// heuristic once the budget is exhausted. The assertion below is that the LLM
// scorer is never invoked more times than the budget allows.
func TestWiring_ShadowSampler_BudgetGated(t *testing.T) {
	defer discardLogs()()
	base := &mutation.Strategy{
		ID:     "bootstrap-root",
		Params: map[string]any{"temperature": 0.7},
	}
	cfg := DefaultSystemConfig()
	cfg.PopulationSize = 4
	cfg.EnableDreamCycle = false
	cfg.EnableScheduler = true
	cfg.EventStore = newMockCallbackRegistrarForTest()
	cfg.StrategyStore = newMockStrategyStore()
	cfg.RollbackPolicyConfig = RollbackPolicyConfig{Enabled: true}
	cfg.ShadowEvalConfig = ShadowEvaluationConfig{Enabled: true, MinSamples: 3, MinWinRate: 0.55}
	// Wire an LLM scorer with a tight budget: 4 LLM calls across the whole
	// generation, shared by population scoring AND shadow Prime.
	var llmCalls atomic.Int64
	cfg.Scorer = genome.ScorerFunc(func(*mutation.Strategy) float64 {
		llmCalls.Add(1)
		return 0.8
	})
	cfg.HeuristicScorer = genome.ScorerFunc(func(*mutation.Strategy) float64 { return 0.5 })
	cfg.MaxLLMCallsPerGeneration = 4

	system, err := NewWiredEvolutionSystem(base, cfg)
	if err != nil {
		t.Fatalf("NewWiredEvolutionSystem failed: %v", err)
	}
	defer Shutdown(system)

	if system.ShadowEvaluator == nil {
		t.Fatal("expected non-nil ShadowEvaluator")
	}
	if !system.ShadowEvaluator.HasIndependentScorer() {
		t.Fatal("with an LLM scorer wired, the shadow scorer must be budget-gated (tiered), not absent")
	}

	// Prime requests MinSamples×2 = 6 scorer evaluations; at most 4 may hit
	// the LLM. The rest fall back to the heuristic so the gate still gets a
	// full comparison window.
	sampler := NewShadowSampler(system.ShadowEvaluator, cfg.ShadowEvalConfig.MinSamples)
	sampler.Prime(context.Background(), &mutation.Strategy{ID: "cand"}, &mutation.Strategy{ID: "active"})

	got := llmCalls.Load()
	if got > int64(cfg.MaxLLMCallsPerGeneration) {
		t.Fatalf("shadow Prime exceeded the shared LLM budget: %d calls, budget %d",
			got, cfg.MaxLLMCallsPerGeneration)
	}
	if n := len(system.ShadowEvaluator.Results()); n != cfg.ShadowEvalConfig.MinSamples {
		t.Fatalf("expected a full comparison window (%d), got %d — the heuristic fallback must keep the gate able to judge",
			cfg.ShadowEvalConfig.MinSamples, n)
	}
	// The tiered scorer caches per generation, so every comparison after the
	// first is a cache hit returning an identical score. The evaluator must
	// report that, otherwise the window looks like independent evidence.
	if !system.ShadowEvaluator.IsDeterministicScorer() {
		t.Fatal("a cache-backed tiered scorer must be reported as deterministic")
	}
}
