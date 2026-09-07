package main

// W-L1 falsifiable acceptance: the kernel loop clock must actually drive the
// round-end actions through the PluginBus capability discovery — not just
// tick. A grep for NewLoopPlugin cannot distinguish "beat wired" from
// "capability actions reachable", so every test here asserts effects that
// only happen when the whole chain works: Register(before Start) →
// PluginBus.Start hands the plugin its bus → OnRoundEnd discovers capability
// plugins → their callbacks fire with the ROUND execution identity.

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/runtime"
)

// roundFlushSpy is a fake CapCheckpoint plugin implementing Flusher. It
// records every Flush's executionID — the concrete, observable effect of
// LoopPlugin.OnRoundEnd's checkpoint branch.
type roundFlushSpy struct {
	mu    sync.Mutex
	calls []string
	// afterSteps counts bus step passthroughs, proving the hook keeps
	// serving the bus after the loop budget is exhausted.
	afterSteps int
}

func (s *roundFlushSpy) Name() string { return "round-flush-spy" }
func (s *roundFlushSpy) Capabilities() []runtime.Capability {
	return []runtime.Capability{runtime.CapCheckpoint}
}
func (s *roundFlushSpy) Start(context.Context, runtime.EventBus) error { return nil }
func (s *roundFlushSpy) Stop(context.Context) error                    { return nil }

// Flush implements runtime.Flusher.
func (s *roundFlushSpy) Flush(_ context.Context, executionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, executionID)
	return nil
}

// AfterStep implements runtime.WorkflowHook (auto-registered by the bus).
func (s *roundFlushSpy) AfterStep(_ context.Context, _ string, _ *runtime.StepResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.afterSteps++
	return nil
}

// BeforeStep implements runtime.WorkflowHook.
func (s *roundFlushSpy) BeforeStep(context.Context, string, *runtime.Step) error {
	return nil
}

func (s *roundFlushSpy) flushes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *roundFlushSpy) stepCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.afterSteps
}

// startSpyBus assembles the production wiring path with the spy registered:
// Register BEFORE Start (the load-bearing order — Register after Start is a
// guaranteed silent no-op via ErrBusAlreadyStarted, and the plugin would
// never receive its EventBus reference).
func startSpyBus(ctx context.Context, loopCfg kernelLoopConfig) (*pluginBusHook, *roundFlushSpy, error) {
	bus := runtime.NewPluginBus()
	spy := &roundFlushSpy{}
	if err := bus.Register(spy); err != nil {
		return nil, nil, err
	}
	loop := runtime.NewLoopPlugin("kernel-loop", runtime.LoopConfig{
		MaxIterations: loopCfg.LoopMaxIterations,
	})
	if err := bus.Register(loop); err != nil {
		return nil, nil, err
	}
	if err := bus.Start(ctx); err != nil {
		return nil, nil, err
	}
	return newPluginBusHook(bus, loop, loopCfg), spy, nil
}

// TestLoopClock_FlushesRoundExecutionIDs is the falsifiable closure proof:
// with a CapCheckpoint plugin on the bus, every round boundary must flush
// with the round's own execution identity (kernel-round-N), not a task id.
func TestLoopClock_FlushesRoundExecutionIDs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hook, spy, err := startSpyBus(ctx, kernelLoopConfig{LoopRoundQuanta: 2})
	if err != nil {
		t.Fatalf("startSpyBus: %v", err)
	}

	// 4 quanta at 2 quanta/round → rounds 1 and 2 close.
	for i := 0; i < 4; i++ {
		hook.AfterQuantum(ctx, fmt.Sprintf("task-%d", i), "agent", nil)
	}

	got := spy.flushes()
	if len(got) != 2 {
		t.Fatalf("expected 2 round flushes, got %d (%v)", len(got), got)
	}
	want := []string{"kernel-round-1", "kernel-round-2"}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("flush[%d] = %q, want %q", i, got[i], w)
		}
	}
}

// TestLoopClock_MaxIterationsSettlesFinalRound locks the settle-then-gate
// order: with MaxIterations=1 the FIRST boundary must still settle round 1
// (OnRoundEnd → Flush), and only THEN stop advancing. Asking
// ShouldExecuteRound before settling would swallow the final round's
// end-of-round bookkeeping — the exact bug the review flagged.
func TestLoopClock_MaxIterationsSettlesFinalRound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hook, spy, err := startSpyBus(ctx, kernelLoopConfig{LoopMaxIterations: 1, LoopRoundQuanta: 1})
	if err != nil {
		t.Fatalf("startSpyBus: %v", err)
	}

	// Round 1 settles at the first quantum; rounds 2+ never process.
	for i := 0; i < 5; i++ {
		hook.AfterQuantum(ctx, fmt.Sprintf("task-%d", i), "agent", nil)
	}

	got := spy.flushes()
	if len(got) != 1 || got[0] != "kernel-round-1" {
		t.Fatalf("expected exactly the final round-1 flush, got %v", got)
	}
}

// TestLoopClock_ConcurrentBoundariesNoSkipNoDouble proves the AddInt64
// return-value contract: under concurrent AfterQuantum calls every boundary
// maps to exactly one goroutine — rounds are a permutation of 1..N with no
// duplicate and no skipped boundary (Add-then-Load would double-fire or lose
// rounds).
func TestLoopClock_ConcurrentBoundariesNoSkipNoDouble(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const quanta = 200
	hook, spy, err := startSpyBus(ctx, kernelLoopConfig{LoopRoundQuanta: 1})
	if err != nil {
		t.Fatalf("startSpyBus: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < quanta; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			hook.AfterQuantum(ctx, fmt.Sprintf("task-%d", n), "agent", nil)
		}(i)
	}
	wg.Wait()

	got := spy.flushes()
	if len(got) != quanta {
		t.Fatalf("expected %d flushes under concurrency, got %d (lost or double-fired boundaries)", quanta, len(got))
	}
	seen := make(map[string]bool, quanta)
	for _, id := range got {
		if seen[id] {
			t.Fatalf("duplicate round boundary %q (double-fire)", id)
		}
		seen[id] = true
	}
	for i := 1; i <= quanta; i++ {
		id := fmt.Sprintf("kernel-round-%d", i)
		if !seen[id] {
			t.Fatalf("missing round boundary %q (skipped quantum)", id)
		}
	}
}

// TestLoopClock_BudgetExhaustedKeepsServingBus proves the round clock is
// observational: once the budget latches, the hook still projects every
// quantum onto the bus (AfterStep passthrough keeps counting) — the
// scheduler's task flow is never gated by the evolution clock.
func TestLoopClock_BudgetExhaustedKeepsServingBus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hook, spy, err := startSpyBus(ctx, kernelLoopConfig{LoopMaxIterations: 1, LoopRoundQuanta: 1})
	if err != nil {
		t.Fatalf("startSpyBus: %v", err)
	}

	for i := 0; i < 8; i++ {
		hook.AfterQuantum(ctx, fmt.Sprintf("task-%d", i), "agent", nil)
	}

	if got := spy.stepCount(); got != 8 {
		t.Fatalf("bus AfterStep passthrough must survive the loop budget: got %d, want 8", got)
	}
	if len(spy.flushes()) != 1 {
		t.Fatalf("flushes after budget: %v", spy.flushes())
	}
}

// TestLoopClock_BudgetHoldsUnderConcurrency closes the matrix hole between
// TestLoopClock_MaxIterationsSettlesFinalRound (budget, serial) and
// TestLoopClock_ConcurrentBoundariesNoSkipNoDouble (concurrent, no budget):
// budget × concurrency. A read-then-set stop flag lets every concurrent
// boundary caller observe "not stopped" before any latches it, over-settling
// rounds past the budget (observed: max_iterations=1 settling 3 rounds).
// Deriving the round from each caller's own unique quantum count fixes it.
// Repeated attempts because the interleaving is probabilistic.
func TestLoopClock_BudgetHoldsUnderConcurrency(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const (
		attempts   = 100
		goroutines = 64
		maxRounds  = 2
	)
	for attempt := 0; attempt < attempts; attempt++ {
		hook, spy, err := startSpyBus(ctx, kernelLoopConfig{
			LoopMaxIterations: maxRounds,
			LoopRoundQuanta:   1,
		})
		if err != nil {
			t.Fatalf("startSpyBus: %v", err)
		}
		var wg sync.WaitGroup
		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				hook.AfterQuantum(ctx, fmt.Sprintf("task-%d", n), "agent", nil)
			}(i)
		}
		wg.Wait()

		got := spy.flushes()
		if len(got) > maxRounds {
			t.Fatalf("attempt %d: round budget overrun — settled %d rounds (%v), want <= %d",
				attempt, len(got), got, maxRounds)
		}
		// The budget must be reached, not merely respected: with far more
		// quanta than the budget, exactly maxRounds rounds must settle.
		if len(got) != maxRounds {
			t.Fatalf("attempt %d: settled %d rounds (%v), want exactly %d",
				attempt, len(got), got, maxRounds)
		}
		// Settled rounds are exactly the first maxRounds identities — an
		// over-budget round must never settle even if it wins the race.
		seen := make(map[string]bool, len(got))
		for _, id := range got {
			if seen[id] {
				t.Fatalf("attempt %d: duplicate round %q", attempt, id)
			}
			seen[id] = true
		}
		for i := 1; i <= maxRounds; i++ {
			id := fmt.Sprintf("kernel-round-%d", i)
			if !seen[id] {
				t.Fatalf("attempt %d: missing in-budget round %q (got %v)", attempt, id, got)
			}
		}
	}
}

// TestNewPluginBusHook_NormalizesRoundQuanta locks the invariant inside the
// type: a zero/negative LoopRoundQuanta (a caller that skipped withDefaults —
// e.g. an adopt path constructing kernelLoopConfig{}) must still beat once
// per quantum rather than silently never ticking.
func TestNewPluginBusHook_NormalizesRoundQuanta(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for _, quanta := range []int{0, -5} {
		hook, spy, err := startSpyBus(ctx, kernelLoopConfig{LoopRoundQuanta: quanta})
		if err != nil {
			t.Fatalf("startSpyBus(quanta=%d): %v", quanta, err)
		}
		for i := 0; i < 3; i++ {
			hook.AfterQuantum(ctx, fmt.Sprintf("task-%d", i), "agent", nil)
		}
		if got := spy.flushes(); len(got) != 3 {
			t.Fatalf("LoopRoundQuanta=%d must normalize to 1: got %d flushes (%v)", quanta, len(got), got)
		}
	}
}

// TestParseKernelLoopConfig_LoopKnobs covers the config regression contract:
// unset knobs fall back to the zero-value-safe defaults (quanta 1, unlimited
// rounds); explicit values pass through.
func TestParseKernelLoopConfig_LoopKnobs(t *testing.T) {
	cfg := parseKernelLoopConfig(&ares_config.Config{})
	if cfg.LoopRoundQuanta != 1 {
		t.Fatalf("default LoopRoundQuanta = %d, want 1", cfg.LoopRoundQuanta)
	}
	if cfg.LoopMaxIterations != 0 {
		t.Fatalf("default LoopMaxIterations = %d, want 0 (unlimited)", cfg.LoopMaxIterations)
	}

	withKnobs := &ares_config.Config{}
	withKnobs.Kernel.LoopRoundQuanta = 3
	withKnobs.Kernel.LoopMaxIterations = 7
	parsed := parseKernelLoopConfig(withKnobs)
	if parsed.LoopRoundQuanta != 3 || parsed.LoopMaxIterations != 7 {
		t.Fatalf("explicit knobs not honored: quanta=%d max=%d", parsed.LoopRoundQuanta, parsed.LoopMaxIterations)
	}
}
