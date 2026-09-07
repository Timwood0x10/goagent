package genome

import (
	"testing"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// This file contains stricter tests for preservePromptDiversityLocked that
// complement the existing tests in population_guard_test.go. Each test
// asserts a specific behavioral contract rather than just "not nil" so a
// regression in the guard's policy fails the test immediately.

// newGuardTestElites builds an elite slice where every strategy shares the
// same prompt template — the trigger condition for the diversity guard.
func newGuardTestElites(template string, scores ...float64) []*mutation.Strategy {
	elites := make([]*mutation.Strategy, len(scores))
	for i, s := range scores {
		elites[i] = &mutation.Strategy{
			ID:             "e" + itoa(i),
			Score:          s,
			PromptTemplate: template,
			Params:         map[string]any{"temp": 0.5},
		}
	}
	return elites
}

// itoa is a tiny strconv.Itoa alternative to keep this test file dependency-free.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// TestPreservePromptDiversity_ConfigDisabled_NoOp verifies that when
// DisablePromptDiversityGuard=true, the guard returns the elite slice
// unchanged — same length, same templates, same scores, same MutationDesc
// values. No diversity seed should be injected.
func TestPreservePromptDiversity_ConfigDisabled_NoOp(t *testing.T) {
	elites := newGuardTestElites("template1", 100.0, 90.0, 80.0)
	population := []*mutation.Strategy{
		{ID: "ea", Score: 100, PromptTemplate: "template1"},
		{ID: "eb", Score: 90, PromptTemplate: "template1"},
		{ID: "ec", Score: 80, PromptTemplate: "template1"},
		{ID: "alt", Score: 50, PromptTemplate: "template2"},
	}

	pop := &Population{
		cfg: PopulationConfig{DisablePromptDiversityGuard: true},
	}

	result := pop.preservePromptDiversityLocked(elites, population)

	if len(result) != len(elites) {
		t.Fatalf("guard disabled: got %d elites, want %d (unchanged)", len(result), len(elites))
	}
	for i, e := range result {
		if e.ID != elites[i].ID {
			t.Errorf("index %d: ID changed from %q to %q (guard disabled should be no-op)",
				i, elites[i].ID, e.ID)
		}
		if e.PromptTemplate != elites[i].PromptTemplate {
			t.Errorf("index %d: template changed from %q to %q",
				i, elites[i].PromptTemplate, e.PromptTemplate)
		}
		if e.Score != elites[i].Score {
			t.Errorf("index %d: score changed from %.1f to %.1f",
				i, elites[i].Score, e.Score)
		}
		if e.MutationDesc != elites[i].MutationDesc {
			t.Errorf("index %d: MutationDesc changed from %q to %q",
				i, elites[i].MutationDesc, e.MutationDesc)
		}
	}

	// Negative assertion: no diversity seed should have been injected.
	for i, e := range result {
		if e.MutationDesc == "prompt_diversity_seed" {
			t.Errorf("index %d: diversity seed injected despite guard disabled", i)
		}
	}
}

// TestPreservePromptDiversity_CloneIsolation_DeepCopy verifies the retained
// seed is a deep copy: mutating the seed's Params, Score, PromptTemplate,
// ParentID, and MutationDesc must NOT affect the original population member.
// This catches shallow-clone bugs that would silently corrupt population state.
func TestPreservePromptDiversity_CloneIsolation_DeepCopy(t *testing.T) {
	elites := newGuardTestElites("template1", 100.0)
	original := &mutation.Strategy{
		ID:             "alt",
		Score:          50.0,
		PromptTemplate: "template2",
		ParentID:       "root-alt",
		Params: map[string]any{
			"temp":   0.7,
			"count":  3,
			"nested": map[string]any{"deep": "value"},
		},
	}
	population := []*mutation.Strategy{
		{ID: "ea", Score: 100, PromptTemplate: "template1"},
		original,
	}

	pop := &Population{Generation: 5}
	result := pop.preservePromptDiversityLocked(elites, population)
	if len(result) != 2 {
		t.Fatalf("expected 2 elites with diversity seed, got %d", len(result))
	}

	seed := result[1]
	if seed.MutationDesc != "prompt_diversity_seed" {
		t.Fatalf("expected prompt_diversity_seed marker, got %q", seed.MutationDesc)
	}

	// Snapshot the original's Params to detect any mutation.
	origTemp := original.Params["temp"]
	origCount := original.Params["count"]
	origNested, _ := original.Params["nested"].(map[string]any)

	// Mutate the seed in every mutable field.
	seed.Score = 999.0
	seed.PromptTemplate = "mutated_template"
	seed.ParentID = "mutated_parent"
	seed.MutationDesc = "mutated_desc"
	seed.Params["temp"] = 0.001
	seed.Params["count"] = 999
	seed.Params["new_key"] = "new_value"
	if nested, ok := seed.Params["nested"].(map[string]any); ok {
		nested["deep"] = "mutated_value"
	}

	// Original must be unchanged.
	if original.Score != 50.0 {
		t.Errorf("original Score corrupted: got %.1f, want 50.0", original.Score)
	}
	if original.PromptTemplate != "template2" {
		t.Errorf("original PromptTemplate corrupted: got %q, want template2", original.PromptTemplate)
	}
	if original.ParentID != "root-alt" {
		t.Errorf("original ParentID corrupted: got %q, want root-alt", original.ParentID)
	}
	if original.MutationDesc != "" {
		t.Errorf("original MutationDesc corrupted: got %q, want empty", original.MutationDesc)
	}
	if original.Params["temp"] != origTemp {
		t.Errorf("original Params[temp] corrupted: got %v, want %v", original.Params["temp"], origTemp)
	}
	if original.Params["count"] != origCount {
		t.Errorf("original Params[count] corrupted: got %v, want %v", original.Params["count"], origCount)
	}
	// New key must NOT appear on the original.
	if _, exists := original.Params["new_key"]; exists {
		t.Errorf("original Params[new_key] should not exist (deep copy broken)")
	}
	// Nested mutation must NOT propagate.
	if changed, _ := origNested["deep"].(string); changed == "mutated_value" {
		t.Errorf("original Params[nested].deep corrupted — clone shares nested map (deep copy broken)")
	}
}

// TestPreservePromptDiversity_MaxAgeExpiry verifies the contract between
// AgentMaxAge eviction (in Population.Evolve) and the prompt diversity guard.
// The guard itself does NOT filter by age — it should still inject a viable
// seed even if the seed's GenerationCreated would make it eligible for
// age-based eviction in the next evolve cycle. The age filter is a separate
// concern that runs BEFORE the guard in Evolve.
//
// Concretely: an alternative with GenerationCreated far in the past (age would
// exceed AgentMaxAge) is still accepted by the guard because the guard only
// checks score (>= floor) and template difference.
func TestPreservePromptDiversity_MaxAgeExpiry(t *testing.T) {
	elites := newGuardTestElites("template1", 100.0, 90.0)

	// Alternative has GenerationCreated=1; current generation is 100, so
	// age = 99 (would be evicted by AgentMaxAge=10 in the population-level filter).
	// The guard should STILL accept this alternative as a diversity seed.
	agedAlternative := &mutation.Strategy{
		ID:                "aged-alt",
		Score:             50.0,
		PromptTemplate:    "template2",
		GenerationCreated: 1,
	}
	population := []*mutation.Strategy{
		{ID: "ea", Score: 100, PromptTemplate: "template1"},
		{ID: "eb", Score: 90, PromptTemplate: "template1"},
		agedAlternative,
	}

	pop := &Population{
		Generation: 100,
		cfg:        PopulationConfig{AgentMaxAge: 10}, // would evict age > 10
	}
	result := pop.preservePromptDiversityLocked(elites, population)
	if len(result) != 3 {
		t.Fatalf("expected guard to still inject aged alternative (3 elites), got %d", len(result))
	}

	seed := result[2]
	if seed.MutationDesc != "prompt_diversity_seed" {
		t.Fatalf("expected prompt_diversity_seed marker, got %q", seed.MutationDesc)
	}
	if seed.ID != "aged-alt" {
		t.Errorf("expected seed ID 'aged-alt', got %q", seed.ID)
	}
	if seed.PromptTemplate != "template2" {
		t.Errorf("expected seed template 'template2', got %q", seed.PromptTemplate)
	}
	if seed.Score != 50.0 {
		t.Errorf("expected seed score 50.0, got %.1f", seed.Score)
	}

	// The guard bumps GenerationCreated to current+1 — verify this contract
	// so the seed is not immediately eligible for eviction next cycle.
	if seed.GenerationCreated != 101 {
		t.Errorf("expected seed GenerationCreated=101 (population.Generation+1), got %d",
			seed.GenerationCreated)
	}
}

// TestPreservePromptDiversity_SeedReplacesWeakestElite verifies the
// replacement path: when the elite set is already at the population size cap,
// the diversity seed REPLACES the weakest (lowest-score) elite rather than
// being appended (which would be truncated by the caller). The strongest
// elite MUST be preserved.
func TestPreservePromptDiversity_SeedReplacesWeakestElite(t *testing.T) {
	elites := []*mutation.Strategy{
		{ID: "strong", Score: 100.0, PromptTemplate: "template1"},
		{ID: "mid", Score: 80.0, PromptTemplate: "template1"},
		{ID: "weak", Score: 60.0, PromptTemplate: "template1"},
	}
	population := []*mutation.Strategy{
		{ID: "strong", Score: 100.0, PromptTemplate: "template1"},
		{ID: "mid", Score: 80.0, PromptTemplate: "template1"},
		{ID: "weak", Score: 60.0, PromptTemplate: "template1"},
		{ID: "alt", Score: 50.0, PromptTemplate: "template2"},
	}

	pop := &Population{
		Size:       3,
		Generation: 7,
		cfg:        PopulationConfig{EliteCount: 3},
	}
	result := pop.preservePromptDiversityLocked(elites, population)

	// Should still be 3 elites (replaced weakest, not appended).
	if len(result) != 3 {
		t.Fatalf("expected 3 elites (replaced weakest), got %d", len(result))
	}

	// Locate the seed and verify replacement semantics.
	var seed *mutation.Strategy
	strongSurvived := false
	midSurvived := false
	weakEvicted := true
	for _, e := range result {
		if e.MutationDesc == "prompt_diversity_seed" {
			seed = e
		}
		switch e.ID {
		case "strong":
			strongSurvived = true
		case "mid":
			midSurvived = true
		case "weak":
			// "weak" might still appear if it was the seed source, but the
			// seed's ID is "alt" (the alternative), not "weak".
			if e.MutationDesc != "prompt_diversity_seed" {
				weakEvicted = false
			}
		}
	}

	if seed == nil {
		t.Fatal("diversity seed not present — expected to replace weakest elite")
	}
	if !strongSurvived {
		t.Error("strongest elite (score 100) was evicted — only the weakest should be replaced")
	}
	if !midSurvived {
		t.Error("mid elite (score 80) was evicted — only the weakest should be replaced")
	}
	if !weakEvicted {
		t.Error("weakest elite (score 60) was not evicted — expected to be replaced by seed")
	}

	// Verify the seed has the alternative template and correct score.
	if seed.PromptTemplate != "template2" {
		t.Errorf("seed template: got %q, want template2", seed.PromptTemplate)
	}
	if seed.Score != 50.0 {
		t.Errorf("seed score: got %.1f, want 50.0", seed.Score)
	}
	if seed.ID != "alt" {
		t.Errorf("seed ID: got %q, want 'alt'", seed.ID)
	}
}

// TestPreservePromptDiversity_ScoreFloorBoundary verifies the effective
// acceptance boundary for the diversity seed. The guard combines two
// checks: IsScoreEvaluated (score >= 0) AND score >= promptDiversityScoreFloor
// (-0.5). Because IsScoreEvaluated rejects all negative scores, the floor
// constant is effectively redundant — the practical boundary is score >= 0.
// This test pins the current contract so a future change to either check
// is caught.
func TestPreservePromptDiversity_ScoreFloorBoundary(t *testing.T) {
	tests := []struct {
		name         string
		altScore     float64
		expectInject bool
	}{
		// Accepted: positive scores pass both checks.
		{"positive score", 1.0, true},
		{"score zero (boundary)", 0.0, true},
		// Rejected: negative scores fail IsScoreEvaluated even if above floor.
		{"score just below zero (-0.4)", -0.4, false},
		{"score at exact floor (-0.5)", -0.5, false},
		{"score well below floor (-2.0)", -2.0, false},
		{"unevaluated sentinel (-1.0)", -1.0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			elites := newGuardTestElites("template1", 100.0)
			population := []*mutation.Strategy{
				{ID: "ea", Score: 100, PromptTemplate: "template1"},
				{ID: "alt", Score: tc.altScore, PromptTemplate: "template2"},
			}
			pop := &Population{}
			result := pop.preservePromptDiversityLocked(elites, population)

			injected := false
			for _, e := range result {
				if e.MutationDesc == "prompt_diversity_seed" {
					injected = true
				}
			}
			if injected != tc.expectInject {
				t.Errorf("alt score %.2f: injected=%v, want %v",
					tc.altScore, injected, tc.expectInject)
			}
		})
	}
}

// TestDiversityReport_PromptTemplateDistribution_Precise verifies the
// DiversityReport.PromptTemplateDistribution map contains the correct
// counts for a population with multiple templates. This strengthens the
// existing test in adaptive_test.go by asserting exact counts for all
// templates and the total agent count.
func TestDiversityReport_PromptTemplateDistribution_Precise(t *testing.T) {
	agents := []*mutation.Strategy{
		{ID: "a1", PromptTemplate: "reasoner", Score: 100.0},
		{ID: "a2", PromptTemplate: "reasoner", Score: 90.0},
		{ID: "a3", PromptTemplate: "reasoner", Score: 80.0},
		{ID: "b1", PromptTemplate: "planner", Score: 70.0},
		{ID: "b2", PromptTemplate: "planner", Score: 60.0},
		{ID: "c1", PromptTemplate: "coder", Score: 50.0},
		{ID: "d1", PromptTemplate: "", Score: 40.0}, // empty template
	}
	pop := &Population{Agents: agents, cfg: PopulationConfig{}}

	report := pop.measureDiversityReportLocked()

	if report.PromptTemplateDistribution == nil {
		t.Fatal("PromptTemplateDistribution should not be nil")
	}

	// Verify each template count exactly.
	expectedCounts := map[string]int{
		"reasoner": 3,
		"planner":  2,
		"coder":    1,
		"":         1,
	}
	if len(report.PromptTemplateDistribution) != len(expectedCounts) {
		t.Errorf("distribution size: got %d, want %d",
			len(report.PromptTemplateDistribution), len(expectedCounts))
	}
	for template, want := range expectedCounts {
		got := report.PromptTemplateDistribution[template]
		if got != want {
			t.Errorf("template %q: got count %d, want %d", template, got, want)
		}
	}

	// Sanity: sum of all counts must equal total agent count.
	total := 0
	for _, c := range report.PromptTemplateDistribution {
		total += c
	}
	if total != len(agents) {
		t.Errorf("distribution sum: got %d, want %d (total agents)", total, len(agents))
	}
}

// TestDiversityReport_PromptTemplateDistribution_UpdatesAfterMutation
// verifies that the distribution reflects the CURRENT state of the
// population — re-running measureDiversityReportLocked after modifying
// agent templates must reflect the new counts. This catches bugs where
// the distribution is cached or computed from stale data.
func TestDiversityReport_PromptTemplateDistribution_UpdatesAfterMutation(t *testing.T) {
	agents := []*mutation.Strategy{
		{ID: "a1", PromptTemplate: "t1", Score: 100.0},
		{ID: "a2", PromptTemplate: "t1", Score: 90.0},
		{ID: "a3", PromptTemplate: "t2", Score: 80.0},
	}
	pop := &Population{Agents: agents, cfg: PopulationConfig{}}

	report1 := pop.measureDiversityReportLocked()
	if report1.PromptTemplateDistribution["t1"] != 2 || report1.PromptTemplateDistribution["t2"] != 1 {
		t.Fatalf("initial distribution: t1=%d, t2=%d (want 2, 1)",
			report1.PromptTemplateDistribution["t1"], report1.PromptTemplateDistribution["t2"])
	}

	// Mutate one agent's template and re-measure.
	pop.Agents[0].PromptTemplate = "t2"
	report2 := pop.measureDiversityReportLocked()
	if report2.PromptTemplateDistribution["t1"] != 1 || report2.PromptTemplateDistribution["t2"] != 2 {
		t.Errorf("post-mutation distribution: t1=%d, t2=%d (want 1, 2) — distribution is stale",
			report2.PromptTemplateDistribution["t1"], report2.PromptTemplateDistribution["t2"])
	}
}

// TestPreservePromptDiversity_AlreadyDiverse_NoOp verifies that when the
// elite set already contains multiple templates, the guard does nothing —
// no seed injected, elites returned unchanged. This is a negative test
// that catches a regression where the guard always injects.
func TestPreservePromptDiversity_AlreadyDiverse_NoOp(t *testing.T) {
	elites := []*mutation.Strategy{
		{ID: "e1", Score: 100.0, PromptTemplate: "t1"},
		{ID: "e2", Score: 90.0, PromptTemplate: "t2"},
	}
	population := []*mutation.Strategy{
		{ID: "e1", Score: 100.0, PromptTemplate: "t1"},
		{ID: "e2", Score: 90.0, PromptTemplate: "t2"},
		{ID: "alt", Score: 50.0, PromptTemplate: "t3"},
	}

	pop := &Population{}
	result := pop.preservePromptDiversityLocked(elites, population)

	if len(result) != 2 {
		t.Fatalf("already-diverse elites: got %d, want 2 (no injection)", len(result))
	}
	for i, e := range result {
		if e.MutationDesc == "prompt_diversity_seed" {
			t.Errorf("index %d: seed injected despite elites already diverse", i)
		}
		if e.ID != elites[i].ID {
			t.Errorf("index %d: ID changed from %q to %q (no-op expected)",
				i, elites[i].ID, e.ID)
		}
	}
}

// TestPreservePromptDiversity_NoAlternative_NoOp verifies the guard does
// nothing when the population contains only the dominant template (no
// viable alternative with a different PromptTemplate exists).
func TestPreservePromptDiversity_NoAlternative_NoOp(t *testing.T) {
	elites := newGuardTestElites("only_template", 100.0, 90.0)
	population := []*mutation.Strategy{
		{ID: "e1", Score: 100.0, PromptTemplate: "only_template"},
		{ID: "e2", Score: 90.0, PromptTemplate: "only_template"},
		{ID: "e3", Score: 80.0, PromptTemplate: "only_template"},
	}

	pop := &Population{}
	result := pop.preservePromptDiversityLocked(elites, population)

	if len(result) != len(elites) {
		t.Fatalf("no alternative: got %d elites, want %d (unchanged)", len(result), len(elites))
	}
	for i, e := range result {
		if e.MutationDesc == "prompt_diversity_seed" {
			t.Errorf("index %d: seed injected despite no alternative template in population", i)
		}
	}
}

// TestPreservePromptDiversity_EarlyReturnConditions verifies the early-return
// guards: empty elites and population not larger than elites both short-circuit.
func TestPreservePromptDiversity_EarlyReturnConditions(t *testing.T) {
	t.Run("empty elites returns empty", func(t *testing.T) {
		pop := &Population{}
		alt := &mutation.Strategy{ID: "alt", PromptTemplate: "t2", Score: 50.0}
		result := pop.preservePromptDiversityLocked(
			[]*mutation.Strategy{},
			[]*mutation.Strategy{alt},
		)
		if len(result) != 0 {
			t.Errorf("expected empty result, got %d", len(result))
		}
	})

	t.Run("population not larger than elites returns unchanged", func(t *testing.T) {
		elites := newGuardTestElites("t1", 100.0, 90.0)
		// population has 2 agents, elites has 2 — population <= len(elites).
		population := []*mutation.Strategy{
			{ID: "e1", Score: 100.0, PromptTemplate: "t1"},
			{ID: "e2", Score: 90.0, PromptTemplate: "t1"},
		}
		pop := &Population{}
		result := pop.preservePromptDiversityLocked(elites, population)
		if len(result) != 2 {
			t.Fatalf("expected 2 elites (early return), got %d", len(result))
		}
		for i, e := range result {
			if e.MutationDesc == "prompt_diversity_seed" {
				t.Errorf("index %d: seed injected despite early-return condition", i)
			}
		}
	})
}

// TestPreservePromptDiversity_SeedInheritsAlternativeMetadata verifies the
// injected seed carries the alternative's phenotype metadata (PromptTemplate,
// Score, Params, ParentID) so it can be tracked through subsequent evolution
// cycles. This strengthens the existing test which only checks MutationDesc.
func TestPreservePromptDiversity_SeedInheritsAlternativeMetadata(t *testing.T) {
	elites := newGuardTestElites("template1", 100.0)
	alternative := &mutation.Strategy{
		ID:             "alt-agent",
		Score:          42.5,
		PromptTemplate: "template2",
		ParentID:       "lineage-X",
		Params: map[string]any{
			"temp":      0.8,
			"max_turns": 5,
		},
		StrategyMutationType: mutation.MutationParameter,
	}
	population := []*mutation.Strategy{
		{ID: "ea", Score: 100.0, PromptTemplate: "template1"},
		alternative,
	}

	pop := &Population{Generation: 3}
	result := pop.preservePromptDiversityLocked(elites, population)
	if len(result) != 2 {
		t.Fatalf("expected 2 elites, got %d", len(result))
	}

	seed := result[1]
	if seed.MutationDesc != "prompt_diversity_seed" {
		t.Fatalf("expected prompt_diversity_seed marker, got %q", seed.MutationDesc)
	}

	// Phenotype metadata must be inherited from the alternative.
	if seed.ID != "alt-agent" {
		t.Errorf("seed ID: got %q, want alt-agent", seed.ID)
	}
	if seed.Score != 42.5 {
		t.Errorf("seed Score: got %.2f, want 42.5", seed.Score)
	}
	if seed.PromptTemplate != "template2" {
		t.Errorf("seed PromptTemplate: got %q, want template2", seed.PromptTemplate)
	}
	if seed.ParentID != "lineage-X" {
		t.Errorf("seed ParentID: got %q, want lineage-X", seed.ParentID)
	}
	if seed.StrategyMutationType != mutation.MutationParameter {
		t.Errorf("seed StrategyMutationType: got %v, want MutationParameter", seed.StrategyMutationType)
	}

	// Params must be copied (deep), and present.
	if seed.Params == nil {
		t.Fatal("seed Params is nil — expected deep copy of alternative Params")
	}
	if seed.Params["temp"] != 0.8 {
		t.Errorf("seed Params[temp]: got %v, want 0.8", seed.Params["temp"])
	}
	if seed.Params["max_turns"] != 5 {
		t.Errorf("seed Params[max_turns]: got %v, want 5", seed.Params["max_turns"])
	}

	// GenerationCreated must be bumped to population.Generation + 1.
	if seed.GenerationCreated != 4 {
		t.Errorf("seed GenerationCreated: got %d, want 4 (Generation+1)", seed.GenerationCreated)
	}
}
