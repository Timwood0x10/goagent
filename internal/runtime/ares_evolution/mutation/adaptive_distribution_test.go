package mutation

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestNewAdaptiveDistribution_NilMutator verifies that nil mutator is rejected.
func TestNewAdaptiveDistribution_NilMutator(t *testing.T) {
	_, err := NewAdaptiveDistribution(nil, DefaultAdaptiveDistributionConfig())
	if err == nil {
		t.Fatal("expected error for nil mutator")
	}
}

// TestNewAdaptiveDistribution_InvalidConfig verifies that invalid configuration
// values are rejected.
func TestNewAdaptiveDistribution_InvalidConfig(t *testing.T) {
	m, _ := NewMutator(WithSeed(42))

	tests := []struct {
		name string
		cfg  AdaptiveDistributionConfig
	}{
		{
			name: "negative param min",
			cfg:  modifyConfig(func(c *AdaptiveDistributionConfig) { c.MinParamProb = -0.1 }),
		},
		{
			name: "param min > max",
			cfg:  modifyConfig(func(c *AdaptiveDistributionConfig) { c.MinParamProb = 0.8; c.MaxParamProb = 0.5 }),
		},
		{
			name: "negative exploration floor",
			cfg:  modifyConfig(func(c *AdaptiveDistributionConfig) { c.ExplorationFloor = -0.1 }),
		},
		{
			name: "exploration floor > 1",
			cfg:  modifyConfig(func(c *AdaptiveDistributionConfig) { c.ExplorationFloor = 1.5 }),
		},
		{
			name: "zero learning rate",
			cfg:  modifyConfig(func(c *AdaptiveDistributionConfig) { c.LearningRate = 0 }),
		},
		{
			name: "learning rate > 1",
			cfg:  modifyConfig(func(c *AdaptiveDistributionConfig) { c.LearningRate = 1.5 }),
		},
		{
			name: "negative prompt min",
			cfg:  modifyConfig(func(c *AdaptiveDistributionConfig) { c.MinPromptProb = -0.05 }),
		},
		{
			name: "negative tool min",
			cfg:  modifyConfig(func(c *AdaptiveDistributionConfig) { c.MinToolProb = -0.05 }),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewAdaptiveDistribution(m, tt.cfg)
			if err == nil {
				t.Errorf("expected error for config: %s", tt.name)
			}
		})
	}
}

// modifyConfig returns a copy of DefaultAdaptiveDistributionConfig with the
// given modifier applied.
func modifyConfig(mod func(c *AdaptiveDistributionConfig)) AdaptiveDistributionConfig {
	cfg := DefaultAdaptiveDistributionConfig()
	cfg.Enabled = true
	mod(&cfg)
	return cfg
}

// TestAdaptiveDistribution_DefaultProbabilities verifies the initial
// probability distribution matches expected defaults.
func TestAdaptiveDistribution_DefaultProbabilities(t *testing.T) {
	m, _ := NewMutator(WithSeed(42))
	ad, err := NewAdaptiveDistribution(m, DefaultAdaptiveDistributionConfig())
	if err != nil {
		t.Fatalf("NewAdaptiveDistribution failed: %v", err)
	}

	paramProb, promptProb, toolProb := ad.CurrentProbabilities()
	if paramProb != 0.70 {
		t.Errorf("expected paramProb=0.70, got %f", paramProb)
	}
	if promptProb != 0.15 {
		t.Errorf("expected promptProb=0.15, got %f", promptProb)
	}
	if toolProb != 0.15 {
		t.Errorf("expected toolProb=0.15, got %f", toolProb)
	}
}

// TestAdaptiveDistribution_Mutate verifies that Mutate produces valid children
// through the adaptive distribution.
func TestAdaptiveDistribution_Mutate(t *testing.T) {
	m, err := NewMutator(
		WithSeed(42),
		WithPromptPool([]string{"prompt-a", "prompt-b", "prompt-c"}),
		WithToolPool([]string{"tool-a", "tool-b"}),
	)
	if err != nil {
		t.Fatalf("NewMutator failed: %v", err)
	}

	ad, err := NewAdaptiveDistribution(m, DefaultAdaptiveDistributionConfig())
	if err != nil {
		t.Fatalf("NewAdaptiveDistribution failed: %v", err)
	}

	parent := &Strategy{
		ID:             "test-parent",
		Version:        1,
		Params:         map[string]any{"temperature": 0.5, "tools": "tool-a"},
		PromptTemplate: "prompt-a",
		CreatedAt:      time.Now(),
	}

	children, err := ad.Mutate(context.Background(), parent, 10)
	if err != nil {
		t.Fatalf("AdaptiveDistribution.Mutate failed: %v", err)
	}

	if len(children) != 10 {
		t.Errorf("expected 10 children, got %d", len(children))
	}

	for _, child := range children {
		if child.ParentID != parent.ID {
			t.Errorf("child %s: wrong ParentID", child.ID)
		}
		if child.Version != parent.Version+1 {
			t.Errorf("child %s: wrong Version", child.ID)
		}
		if child.Score != -1 {
			t.Errorf("child %s: expected Score=-1, got %f", child.ID, child.Score)
		}
	}
}

// TestAdaptiveDistribution_RecordOutcome_SuccessGainsWeight verifies that
// repeatedly successful mutation types gain weight within configured bounds.
func TestAdaptiveDistribution_RecordOutcome_SuccessGainsWeight(t *testing.T) {
	cfg := DefaultAdaptiveDistributionConfig()
	cfg.Enabled = true
	cfg.LearningRate = 0.2 // Faster adaptation for test.

	m, _ := NewMutator(WithSeed(42))
	ad, _ := NewAdaptiveDistribution(m, cfg)

	// Record many successful prompt mutations (positive score delta, won).
	for i := 0; i < 20; i++ {
		ad.RecordOutcome(MutationPrompt, 15.0, 0.0, true)
	}

	_, promptProb, toolProb := ad.CurrentProbabilities()

	// Prompt should have gained weight over default 0.15.
	if promptProb <= 0.15 {
		t.Errorf("expected promptProb > 0.15 after successful mutations, got %f", promptProb)
	}

	// Record many unsuccessful tool mutations (high cost, not won).
	for i := 0; i < 20; i++ {
		ad.RecordOutcome(MutationTool, -5.0, 10.0, false)
	}

	_, _, toolProb2 := ad.CurrentProbabilities()

	// Prompt probability should be even higher now, tool should be lower.
	if toolProb2 >= toolProb {
		t.Errorf("expected toolProb to decrease after failures, was %f, now %f", toolProb, toolProb2)
	}

	// Both should be within configured bounds.
	_, promptProb3, toolProb3 := ad.CurrentProbabilities()
	if promptProb3 < cfg.MinPromptProb || promptProb3 > cfg.MaxPromptProb {
		t.Errorf("promptProb %f outside bounds [%f, %f]",
			promptProb3, cfg.MinPromptProb, cfg.MaxPromptProb)
	}
	if toolProb3 < cfg.MinToolProb || toolProb3 > cfg.MaxToolProb {
		t.Errorf("toolProb %f outside bounds [%f, %f]",
			toolProb3, cfg.MinToolProb, cfg.MaxToolProb)
	}
}

// TestAdaptiveDistribution_RecordOutcome_FailuresReduceWeight verifies that
// repeated failures reduce weight without dropping below exploration floor.
func TestAdaptiveDistribution_RecordOutcome_FailuresReduceWeight(t *testing.T) {
	cfg := DefaultAdaptiveDistributionConfig()
	cfg.Enabled = true
	cfg.LearningRate = 0.3
	cfg.ExplorationFloor = 0.05

	m, _ := NewMutator(WithSeed(42))
	ad, _ := NewAdaptiveDistribution(m, cfg)

	// Record many failed prompt mutations (negative score delta, not won).
	for i := 0; i < 30; i++ {
		ad.RecordOutcome(MutationPrompt, -10.0, 5.0, false)
	}

	_, promptProb, _ := ad.CurrentProbabilities()

	// Prompt probability should be reduced but not below exploration floor.
	if promptProb >= 0.15 {
		t.Errorf("expected promptProb < 0.15 after failures, got %f", promptProb)
	}
	if promptProb < cfg.ExplorationFloor {
		t.Errorf("promptProb %f dropped below exploration floor %f",
			promptProb, cfg.ExplorationFloor)
	}
}

// TestAdaptiveDistribution_NotEnabled_NoOp verifies that RecordOutcome is a
// no-op when adaptive distribution is not enabled.
func TestAdaptiveDistribution_NotEnabled_NoOp(t *testing.T) {
	m, _ := NewMutator(WithSeed(42))
	cfg := DefaultAdaptiveDistributionConfig()
	cfg.Enabled = false
	ad, err := NewAdaptiveDistribution(m, cfg)
	if err != nil {
		t.Fatalf("NewAdaptiveDistribution failed: %v", err)
	}

	// Record outcomes (should be no-op since not enabled).
	for i := 0; i < 10; i++ {
		ad.RecordOutcome(MutationPrompt, 10.0, 0.0, true)
	}

	paramProb, promptProb, toolProb := ad.CurrentProbabilities()

	// Probabilities should remain at defaults.
	if paramProb != 0.70 || promptProb != 0.15 || toolProb != 0.15 {
		t.Errorf("probabilities changed when disabled: param=%f, prompt=%f, tool=%f",
			paramProb, promptProb, toolProb)
	}

	outcomes := ad.Outcomes()
	// Outcomes should be empty since disabled.
	for _, o := range outcomes {
		if o.Attempts > 0 {
			t.Error("outcomes recorded when disabled")
		}
	}
}

// TestAdaptiveDistribution_Outcomes verifies that RecordOutcome correctly
// tracks attempt counts, win counts, and running averages.
func TestAdaptiveDistribution_Outcomes(t *testing.T) {
	cfg := DefaultAdaptiveDistributionConfig()
	cfg.Enabled = true

	m, _ := NewMutator(WithSeed(42))
	ad, _ := NewAdaptiveDistribution(m, cfg)

	// Record 5 parameter mutations: 3 wins, 2 losses, varying score deltas.
	ad.RecordOutcome(MutationParameter, 5.0, 0.0, true)
	ad.RecordOutcome(MutationParameter, 3.0, 0.0, true)
	ad.RecordOutcome(MutationParameter, 10.0, 0.0, true)
	ad.RecordOutcome(MutationParameter, -2.0, 0.0, false)
	ad.RecordOutcome(MutationParameter, -1.0, 0.0, false)

	outcomes := ad.Outcomes()
	po, ok := outcomes[MutationParameter]
	if !ok {
		t.Fatal("missing parameter outcome")
	}

	if po.Attempts != 5 {
		t.Errorf("expected 5 attempts, got %d", po.Attempts)
	}
	if po.Wins != 3 {
		t.Errorf("expected 3 wins, got %d", po.Wins)
	}

	// AvgScoreDelta = (5+3+10-2-1) / 5 = 15/5 = 3.0.
	expectedAvg := (5.0 + 3.0 + 10.0 - 2.0 - 1.0) / 5.0
	if po.AvgScoreDelta != expectedAvg {
		t.Errorf("expected AvgScoreDelta=%f, got %f", expectedAvg, po.AvgScoreDelta)
	}
}

// TestAdaptiveDistribution_Report verifies that the Report method produces
// a non-empty string with expected content.
func TestAdaptiveDistribution_Report(t *testing.T) {
	cfg := DefaultAdaptiveDistributionConfig()
	cfg.Enabled = true

	m, _ := NewMutator(WithSeed(42))
	ad, _ := NewAdaptiveDistribution(m, cfg)

	// Record some outcomes.
	ad.RecordOutcome(MutationParameter, 5.0, 0.0, true)
	ad.RecordOutcome(MutationPrompt, 10.0, 0.0, true)

	report := ad.Report()
	if report == "" {
		t.Fatal("expected non-empty report")
	}

	// Report should contain key information.
	expected := []string{"Adaptive Mutation Distribution", "parameter", "prompt", "tool", "attempts"}
	for _, s := range expected {
		if !contains(report, s) {
			t.Errorf("report missing expected content: %q", s)
		}
	}
}

// TestAdaptiveDistribution_ContextCancellation verifies that Mutate respects
// context cancellation.
func TestAdaptiveDistribution_ContextCancellation(t *testing.T) {
	m, _ := NewMutator(WithSeed(42))
	ad, _ := NewAdaptiveDistribution(m, DefaultAdaptiveDistributionConfig())

	parent := &Strategy{
		ID:        "cancel-test",
		Version:   1,
		Params:    map[string]any{"temperature": 0.5},
		CreatedAt: time.Now(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ad.Mutate(ctx, parent, 100)
	if err == nil {
		t.Error("expected error from cancelled context")
	}
}

// TestAdaptiveDistribution_NilParent verifies Mutate rejects nil parent.
func TestAdaptiveDistribution_NilParent(t *testing.T) {
	m, _ := NewMutator(WithSeed(42))
	ad, _ := NewAdaptiveDistribution(m, DefaultAdaptiveDistributionConfig())

	_, err := ad.Mutate(context.Background(), nil, 3)
	if err != ErrNilParent {
		t.Errorf("expected ErrNilParent, got: %v", err)
	}
}

// TestAdaptiveDistribution_ZeroN verifies Mutate rejects n <= 0.
func TestAdaptiveDistribution_ZeroN(t *testing.T) {
	m, _ := NewMutator(WithSeed(42))
	ad, _ := NewAdaptiveDistribution(m, DefaultAdaptiveDistributionConfig())

	parent := &Strategy{ID: "test", Params: map[string]any{}}
	_, err := ad.Mutate(context.Background(), parent, 0)
	if err != ErrInvalidCount {
		t.Errorf("expected ErrInvalidCount, got: %v", err)
	}

	_, err = ad.Mutate(context.Background(), parent, -1)
	if err != ErrInvalidCount {
		t.Errorf("expected ErrInvalidCount for negative n, got: %v", err)
	}
}

// helper to check if a string contains a substring.
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// --- Additional coverage tests ---

// TestAdaptiveDistribution_ConcurrentRecordOutcome verifies that concurrent
// RecordOutcome calls are safe and do not race.
func TestAdaptiveDistribution_ConcurrentRecordOutcome(t *testing.T) {
	cfg := DefaultAdaptiveDistributionConfig()
	cfg.Enabled = true
	cfg.LearningRate = 0.1

	m, _ := NewMutator(WithSeed(42))
	ad, _ := NewAdaptiveDistribution(m, cfg)

	var wg sync.WaitGroup
	mutationTypes := []MutationType{MutationParameter, MutationPrompt, MutationTool}

	// Launch concurrent goroutines recording outcomes for different types.
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			mt := mutationTypes[id%len(mutationTypes)]
			won := id%2 == 0
			ad.RecordOutcome(mt, float64(id%10-5), float64(id%3), won)
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success - no deadlock or data race (run with -race to verify).
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent RecordOutcome timed out")
	}

	// Verify all outcomes were recorded.
	outcomes := ad.Outcomes()
	totalAttempts := 0
	for _, o := range outcomes {
		totalAttempts += o.Attempts
	}
	if totalAttempts != 30 {
		t.Errorf("expected 30 total attempts, got %d", totalAttempts)
	}
}

// TestAdaptiveDistribution_MinAttemptsBeforeAdjust verifies that probability
// adjustments do not begin until enough data has been collected.
func TestAdaptiveDistribution_MinAttemptsBeforeAdjust(t *testing.T) {
	cfg := DefaultAdaptiveDistributionConfig()
	cfg.Enabled = true
	cfg.MinAttemptsBeforeAdjust = 10
	cfg.LearningRate = 0.5 // High rate to make any adjustment obvious.

	m, _ := NewMutator(WithSeed(42))
	ad, _ := NewAdaptiveDistribution(m, cfg)

	// Record fewer outcomes than MinAttemptsBeforeAdjust.
	for i := 0; i < 8; i++ {
		ad.RecordOutcome(MutationPrompt, 100.0, 0.0, true)
	}

	paramProb, promptProb, toolProb := ad.CurrentProbabilities()

	// Probabilities should remain at defaults because MinAttemptsBeforeAdjust
	// has not been reached (8 < 10).
	if paramProb != 0.70 {
		t.Errorf("expected paramProb=0.70 (no adjustment yet), got %f", paramProb)
	}
	if promptProb != 0.15 {
		t.Errorf("expected promptProb=0.15 (no adjustment yet), got %f", promptProb)
	}
	if toolProb != 0.15 {
		t.Errorf("expected toolProb=0.15 (no adjustment yet), got %f", toolProb)
	}

	// Cross the threshold by adding more outcomes.
	for i := 0; i < 5; i++ {
		ad.RecordOutcome(MutationPrompt, 100.0, 0.0, true)
	}

	_, promptProbAfter, _ := ad.CurrentProbabilities()

	// Now prompt probability should have increased.
	if promptProbAfter <= 0.15 {
		t.Errorf("expected promptProb > 0.15 after crossing MinAttemptsBeforeAdjust, got %f", promptProbAfter)
	}
}

// TestAdaptiveDistribution_ProbabilitiesStayBoundedAfterExtremeOutcomes
// verifies that even extreme feedback (all wins or all losses for one type)
// does not push probabilities outside configured bounds.
func TestAdaptiveDistribution_ProbabilitiesStayBoundedAfterExtremeOutcomes(t *testing.T) {
	cfg := DefaultAdaptiveDistributionConfig()
	cfg.Enabled = true
	cfg.LearningRate = 0.5
	cfg.ExplorationFloor = 0.03

	m, _ := NewMutator(WithSeed(42))
	ad, _ := NewAdaptiveDistribution(m, cfg)

	// Extreme: all prompt mutations succeed, all tool mutations fail.
	for i := 0; i < 100; i++ {
		ad.RecordOutcome(MutationPrompt, 50.0, 0.0, true)
		ad.RecordOutcome(MutationTool, -50.0, 20.0, false)
		ad.RecordOutcome(MutationParameter, 0.0, 0.0, false)
	}

	paramProb, promptProb, toolProb := ad.CurrentProbabilities()

	// All probabilities must remain within configured bounds.
	if paramProb < cfg.MinParamProb || paramProb > cfg.MaxParamProb {
		t.Errorf("paramProb %f outside [%f, %f]", paramProb, cfg.MinParamProb, cfg.MaxParamProb)
	}
	if promptProb < cfg.MinPromptProb || promptProb > cfg.MaxPromptProb {
		t.Errorf("promptProb %f outside [%f, %f]", promptProb, cfg.MinPromptProb, cfg.MaxPromptProb)
	}
	if toolProb < cfg.MinToolProb || toolProb > cfg.MaxToolProb {
		t.Errorf("toolProb %f outside [%f, %f]", toolProb, cfg.MinToolProb, cfg.MaxToolProb)
	}

	// All probabilities must be at least the exploration floor.
	if paramProb < cfg.ExplorationFloor {
		t.Errorf("paramProb %f below exploration floor %f", paramProb, cfg.ExplorationFloor)
	}
	if promptProb < cfg.ExplorationFloor {
		t.Errorf("promptProb %f below exploration floor %f", promptProb, cfg.ExplorationFloor)
	}
	if toolProb < cfg.ExplorationFloor {
		t.Errorf("toolProb %f below exploration floor %f", toolProb, cfg.ExplorationFloor)
	}
}

// TestAdaptiveDistribution_ExplorationFloorPreventsZero verifies that the
// exploration floor prevents any mutation type probability from reaching zero
// even after sustained failures.
func TestAdaptiveDistribution_ExplorationFloorPreventsZero(t *testing.T) {
	cfg := DefaultAdaptiveDistributionConfig()
	cfg.Enabled = true
	cfg.LearningRate = 0.5
	cfg.ExplorationFloor = 0.05

	m, _ := NewMutator(WithSeed(42))
	ad, _ := NewAdaptiveDistribution(m, cfg)

	// Record 200 failed tool mutations - should drive toolProb toward floor.
	for i := 0; i < 200; i++ {
		ad.RecordOutcome(MutationTool, -20.0, 15.0, false)
	}

	_, _, toolProb := ad.CurrentProbabilities()

	if toolProb < cfg.ExplorationFloor {
		t.Errorf("toolProb %f dropped below exploration floor %f after sustained failures",
			toolProb, cfg.ExplorationFloor)
	}
	if toolProb == 0 {
		t.Error("toolProb must never be zero; exploration floor should prevent this")
	}
}

// TestAdaptiveDistribution_ReportContainsAllMutationTypes verifies that the
// report includes information for all three mutation types and the key fields.
func TestAdaptiveDistribution_ReportContainsAllMutationTypes(t *testing.T) {
	cfg := DefaultAdaptiveDistributionConfig()
	cfg.Enabled = true
	cfg.ExplorationFloor = 0.03

	m, _ := NewMutator(WithSeed(42))
	ad, _ := NewAdaptiveDistribution(m, cfg)

	// Record outcomes for all three types.
	ad.RecordOutcome(MutationParameter, 5.0, 0.0, true)
	ad.RecordOutcome(MutationParameter, -2.0, 0.0, false)
	ad.RecordOutcome(MutationPrompt, 10.0, 0.0, true)
	ad.RecordOutcome(MutationTool, -3.0, 5.0, false)

	report := ad.Report()

	// Verify report structure.
	required := []string{
		"Adaptive Mutation Distribution",
		"Probabilities:",
		"Bounds:",
		"Exploration floor:",
		"Learning rate:",
		"Outcomes:",
		"parameter",
		"prompt",
		"tool",
		"attempts",
		"wins",
		"win_rate",
		"avg_score_delta",
		"avg_cost_delta",
	}
	for _, s := range required {
		if !contains(report, s) {
			t.Errorf("report missing %q", s)
		}
	}

	// Prompt and tool should have recorded attempt data (not "no attempts yet").
	if !contains(report, "prompt") {
		t.Error("report missing prompt section")
	}
}

// TestAdaptiveDistribution_OutcomeRunningAverage verifies that the running
// average computation is correct over a series of outcomes.
func TestAdaptiveDistribution_OutcomeRunningAverage(t *testing.T) {
	cfg := DefaultAdaptiveDistributionConfig()
	cfg.Enabled = true

	m, _ := NewMutator(WithSeed(42))
	ad, _ := NewAdaptiveDistribution(m, cfg)

	// Record outcomes with known values to verify running average.
	values := []float64{2.0, 4.0, 6.0, 8.0, 10.0}
	for _, v := range values {
		ad.RecordOutcome(MutationParameter, v, 0.0, true)
	}

	outcomes := ad.Outcomes()
	po := outcomes[MutationParameter]

	// AvgScoreDelta = (2+4+6+8+10) / 5 = 6.0
	expectedAvg := 6.0
	if po.AvgScoreDelta != expectedAvg {
		t.Errorf("expected AvgScoreDelta=%f, got %f", expectedAvg, po.AvgScoreDelta)
	}

	// AvgCostDelta should be 0.0 since all cost deltas were 0.
	if po.AvgCostDelta != 0.0 {
		t.Errorf("expected AvgCostDelta=0.0, got %f", po.AvgCostDelta)
	}
}

// TestAdaptiveDistribution_DisabledRecordOutcomeNoOutcomesRecorded verifies
// that when disabled, Outcomes() returns zero attempts for all types.
func TestAdaptiveDistribution_DisabledRecordOutcomeNoOutcomesRecorded(t *testing.T) {
	m, _ := NewMutator(WithSeed(42))
	cfg := DefaultAdaptiveDistributionConfig()
	cfg.Enabled = false
	ad, _ := NewAdaptiveDistribution(m, cfg)

	// Attempt to record outcomes.
	ad.RecordOutcome(MutationParameter, 10.0, 0.0, true)
	ad.RecordOutcome(MutationPrompt, 5.0, 0.0, true)
	ad.RecordOutcome(MutationTool, -3.0, 2.0, false)

	outcomes := ad.Outcomes()
	for mt, o := range outcomes {
		if o.Attempts != 0 {
			t.Errorf("mutation type %v: expected 0 attempts when disabled, got %d", mt, o.Attempts)
		}
		if o.Wins != 0 {
			t.Errorf("mutation type %v: expected 0 wins when disabled, got %d", mt, o.Wins)
		}
	}
}
