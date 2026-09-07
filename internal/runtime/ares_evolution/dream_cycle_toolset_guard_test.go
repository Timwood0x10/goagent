package evolution

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// recordingTester records every candidate that actually reached the arena, so a
// test can assert not just "the bad candidate did not win" but the stronger
// "the bad candidate never consumed an arena run".
type recordingTester struct {
	mu   sync.Mutex
	seen []string
	// winRate is returned for every candidate that does reach the arena.
	winRate float64
}

func (r *recordingTester) Run(ctx context.Context, cfg RegressionConfig) (*RegressionResult, error) {
	r.mu.Lock()
	r.seen = append(r.seen, cfg.Candidate.ID)
	r.mu.Unlock()
	return &RegressionResult{
		CandidateScore: 0.9,
		BaselineScore:  0.5,
		WinRate:        r.winRate,
		TotalTasks:     10,
	}, nil
}

func (r *recordingTester) sawCandidate(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.seen {
		if s == id {
			return true
		}
	}
	return false
}

func (r *recordingTester) runCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seen)
}

// newToolGuardDreamCycle builds a DreamCycle whose only interesting wiring is
// the tool-set guardrail, with both arena stages configured to pass anything
// they are handed. Any rejection observed in these tests is therefore the C6
// guard, not the win-rate screen.
func newToolGuardDreamCycle(t *testing.T, tester TesterInterface, guardOpts ...GuardrailOption) *DreamCycle {
	t.Helper()
	guards, err := NewEvolutionGuardrails(guardOpts...)
	if err != nil {
		t.Fatalf("NewEvolutionGuardrails: %v", err)
	}
	dc, err := NewDreamCycle(NewEvolutionScheduler(nil, nil), &mockMutator{}, tester, nil,
		WithDreamCycleConfig(DreamCycleConfig{
			Enabled:         true,
			MinWinRate:      0.55,
			QuickRejectRuns: 1,
			TaskSampleSize:  10,
		}),
		WithDreamCycleGuardrails(guards),
	)
	if err != nil {
		t.Fatalf("NewDreamCycle: %v", err)
	}
	return dc
}

func strategyWithTools(id, tools string) Strategy {
	return Strategy{ID: id, Name: id, Version: 2, Params: map[string]any{"tools": tools}}
}

// TestFindWinner_ToolSetGuardRejectsBeforeArena is the C6 wiring assertion: a
// candidate whose evolved Params["tools"] exceeds the guardrail bound must be
// dropped in the selection path, BEFORE it is arena-tested. Without the wiring
// the guard was dead code — ValidateToolSet existed but nothing called it, so an
// over-budget mutation was silently arena-tested and could win.
func TestFindWinner_ToolSetGuardRejectsBeforeArena(t *testing.T) {
	defer discardLogs()()

	tester := &recordingTester{winRate: 0.9}
	dc := newToolGuardDreamCycle(t, tester, WithMaxToolsEnabled(2))

	candidates := []Strategy{
		strategyWithTools("over-budget", "a,b,c"),
		strategyWithTools("within-budget", "a,b"),
	}

	winner, err := dc.findWinner(context.Background(), candidates, Strategy{ID: "baseline"}, "task")
	if err != nil {
		t.Fatalf("findWinner: %v", err)
	}
	if winner == nil {
		t.Fatal("expected the within-budget candidate to win")
	}
	if winner.strategy.ID != "within-budget" {
		t.Errorf("winner = %q, want within-budget", winner.strategy.ID)
	}
	if tester.sawCandidate("over-budget") {
		t.Error("over-budget candidate reached the arena; the guard must reject before testing")
	}
	if !tester.sawCandidate("within-budget") {
		t.Error("within-budget candidate must still be arena-tested")
	}
}

// TestFindWinner_AllCandidatesJailedByToolSetGuard asserts the all-rejected path
// reports "no winner" (ErrAllCandidatesRejected, which Run treats as a normal
// no-op cycle) rather than falling through to the arena with an empty slice.
func TestFindWinner_AllCandidatesJailedByToolSetGuard(t *testing.T) {
	defer discardLogs()()

	tester := &recordingTester{winRate: 0.9}
	dc := newToolGuardDreamCycle(t, tester, WithMaxToolsEnabled(1))

	candidates := []Strategy{
		strategyWithTools("bad-1", "a,b"),
		strategyWithTools("bad-2", "a,b,c"),
	}

	winner, err := dc.findWinner(context.Background(), candidates, Strategy{ID: "baseline"}, "task")
	if !errors.Is(err, ErrAllCandidatesRejected) {
		t.Fatalf("err = %v, want ErrAllCandidatesRejected", err)
	}
	if winner != nil {
		t.Errorf("winner = %+v, want nil", winner)
	}
	if tester.runCount() != 0 {
		t.Errorf("arena ran %d times, want 0 (every candidate was jailed)", tester.runCount())
	}
}

// TestFindWinner_ToolSetGuardCountsSameNamesAsExecutor locks the guard and the
// executors onto ONE parser. A mutated whitelist like "a,,b," (mutation does
// emit trailing/duplicate separators) enables exactly 2 tools as far as the
// executors are concerned; if the guard counted raw comma fields it would see 4
// and jail a candidate the runtime considers within budget.
func TestFindWinner_ToolSetGuardCountsSameNamesAsExecutor(t *testing.T) {
	defer discardLogs()()

	tester := &recordingTester{winRate: 0.9}
	dc := newToolGuardDreamCycle(t, tester, WithMaxToolsEnabled(2))

	winner, err := dc.findWinner(context.Background(),
		[]Strategy{strategyWithTools("sloppy", " a , , b , ")},
		Strategy{ID: "baseline"}, "task")
	if err != nil {
		t.Fatalf("findWinner: %v", err)
	}
	if winner == nil || winner.strategy.ID != "sloppy" {
		t.Fatal("a whitelist that enables 2 real tools must pass a bound of 2")
	}
}

// TestFindWinner_NoToolsWhitelistNotRejectedByDefault keeps the default
// behavior zero-value usable: a strategy with no Params["tools"] (all tools
// allowed) must not be treated as "enables zero tools" unless the deployment
// opted into WithRequireAnyTool.
func TestFindWinner_NoToolsWhitelistNotRejectedByDefault(t *testing.T) {
	defer discardLogs()()

	tester := &recordingTester{winRate: 0.9}
	dc := newToolGuardDreamCycle(t, tester, WithMaxToolsEnabled(2))

	winner, err := dc.findWinner(context.Background(),
		[]Strategy{{ID: "no-tools-key", Version: 2}},
		Strategy{ID: "baseline"}, "task")
	if err != nil {
		t.Fatalf("findWinner: %v", err)
	}
	if winner == nil || winner.strategy.ID != "no-tools-key" {
		t.Fatal("a strategy without a tool whitelist must not be jailed by default")
	}
}

// TestFindWinner_RequireAnyToolJailsEmptyWhitelist covers the opt-in inverse:
// when the deployment demands every strategy advertise at least one tool, a
// candidate with no whitelist is rejected at selection time instead of relying
// on the runtime zero-intersection fallback.
func TestFindWinner_RequireAnyToolJailsEmptyWhitelist(t *testing.T) {
	defer discardLogs()()

	tester := &recordingTester{winRate: 0.9}
	dc := newToolGuardDreamCycle(t, tester, WithRequireAnyTool(true))

	_, err := dc.findWinner(context.Background(),
		[]Strategy{{ID: "toolless", Version: 2}},
		Strategy{ID: "baseline"}, "task")
	if !errors.Is(err, ErrAllCandidatesRejected) {
		t.Fatalf("err = %v, want ErrAllCandidatesRejected", err)
	}
	if tester.runCount() != 0 {
		t.Errorf("arena ran %d times, want 0", tester.runCount())
	}
}
