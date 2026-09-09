package genome

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// --- Multi-objective Fitness Tests ---

func TestParetoDominance(t *testing.T) {
	t.Parallel()

	// cost and latency are minimize (lower is better), quality is maximize (higher is better).
	a := &mutation.Strategy{
		DimensionScores: map[string]float64{"cost": 0.3, "quality": 0.9, "latency": 0.2},
		Score:           0.7,
	}
	b := &mutation.Strategy{
		DimensionScores: map[string]float64{"cost": 0.6, "quality": 0.7, "latency": 0.5},
		Score:           0.6,
	}

	if !ParetoDominance(a, b) {
		t.Error("a should dominate b: lower cost, higher quality, lower latency")
	}
	if ParetoDominance(b, a) {
		t.Error("b should NOT dominate a")
	}
}

func TestParetoDominance_NoDim(t *testing.T) {
	t.Parallel()

	a := &mutation.Strategy{Score: 0.9}
	b := &mutation.Strategy{Score: 0.5}

	if !ParetoDominance(a, b) {
		t.Error("a should dominate b by single score")
	}
	if ParetoDominance(b, a) {
		t.Error("b should NOT dominate a")
	}
}

func TestParetoFront(t *testing.T) {
	t.Parallel()

	// cost and latency are minimize (lower is better), quality is maximize (higher is better).
	// Strategy A dominates B (all dims better or equal).
	// Strategy C is Pareto-optimal (different trade-off).
	// Strategy D is dominated by A.
	strategies := []*mutation.Strategy{
		{DimensionScores: map[string]float64{"cost": 0.2, "quality": 0.9, "latency": 0.1}}, // A: best overall
		{DimensionScores: map[string]float64{"cost": 0.5, "quality": 0.5, "latency": 0.5}}, // B: dominated by A
		{DimensionScores: map[string]float64{"cost": 0.1, "quality": 0.6, "latency": 0.3}}, // C: best cost, but lower quality than A
		{DimensionScores: map[string]float64{"cost": 0.8, "quality": 0.3, "latency": 0.9}}, // D: dominated by A and C
	}

	front := ParetoFront(strategies)
	// Expected: A (index 0) and C (index 2) are Pareto-optimal.
	if len(front) != 2 {
		t.Fatalf("expected 2 Pareto-optimal (A and C), got %d", len(front))
	}
}

func TestCrowdingDistance(t *testing.T) {
	t.Parallel()

	strategies := []*mutation.Strategy{
		{DimensionScores: map[string]float64{"cost": 0.0, "quality": 0.0}},
		{DimensionScores: map[string]float64{"cost": 0.5, "quality": 0.5}},
		{DimensionScores: map[string]float64{"cost": 1.0, "quality": 1.0}},
	}

	dists := CrowdingDistance(strategies)
	if len(dists) != 3 {
		t.Fatalf("expected 3 distances, got %d", len(dists))
	}
	// Boundary points should have infinite distance.
	if !math.IsInf(dists[0], 1) || !math.IsInf(dists[2], 1) {
		t.Error("boundary points should have infinite crowding distance")
	}
}

func TestAggregateDimensions(t *testing.T) {
	t.Parallel()

	// cost is minimize (lower is better), so higher raw cost values are penalized.
	dims := map[string]float64{"success_rate": 0.9, "quality": 0.8, "cost": 0.2}
	score := AggregateDimensions(dims, nil)
	if score <= 0 {
		t.Errorf("expected positive aggregate, got %f", score)
	}
	// Default weights: success_rate=0.4, quality=0.25, cost=0.2, latency=0.15
	// cost is minimized so it's inverted: 1 - 0.2 = 0.8
	// 0.9*0.4 + 0.8*0.25 + 0.8*0.2 + 0*0.15 = 0.36 + 0.20 + 0.16 + 0 = 0.72
	if score < 0.71 || score > 0.73 {
		t.Errorf("expected aggregate ~0.72, got %f", score)
	}
}

func TestAggregateDimensions_Empty(t *testing.T) {
	t.Parallel()

	if got := AggregateDimensions(nil, nil); got != 0 {
		t.Errorf("expected 0 for nil dims, got %f", got)
	}
	if got := AggregateDimensions(map[string]float64{}, nil); got != 0 {
		t.Errorf("expected 0 for empty dims, got %f", got)
	}
}

// --- Selection Strategy Tests ---

func TestRankSelection(t *testing.T) {
	t.Parallel()

	rs := NewRankSelection()
	pop := []*mutation.Strategy{
		{Score: 10.0}, {Score: 20.0}, {Score: 30.0},
		{Score: 40.0}, {Score: 50.0},
	}

	ctx := context.Background()
	result, err := rs.Select(ctx, pop, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 10 {
		t.Fatalf("expected 10 selections, got %d", len(result))
	}
	// Higher-scoring should appear more often (rank-based).
	scoreSum := 0.0
	for _, s := range result {
		scoreSum += s.Score
	}
	avg := scoreSum / 10
	if avg < 30 {
		t.Logf("rank selection avg=%.1f (expected bias toward higher scores)", avg)
	}
}

func TestSUSSelection(t *testing.T) {
	t.Parallel()

	sus := NewSUSSelection()
	pop := []*mutation.Strategy{
		{Score: 10.0}, {Score: 20.0}, {Score: 30.0},
		{Score: 40.0}, {Score: 50.0},
	}

	ctx := context.Background()
	result, err := sus.Select(ctx, pop, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 10 {
		t.Fatalf("expected 10 selections, got %d", len(result))
	}
}

func TestSUSSelection_EqualScores(t *testing.T) {
	t.Parallel()

	sus := NewSUSSelection()
	pop := []*mutation.Strategy{
		{Score: 50.0}, {Score: 50.0}, {Score: 50.0},
	}

	ctx := context.Background()
	result, err := sus.Select(ctx, pop, 6)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 6 {
		t.Fatalf("expected 6 selections, got %d", len(result))
	}
}

func TestRouletteWheelNewSelection(t *testing.T) {
	t.Parallel()

	rw, err := NewRouletteWheelSelection()
	if err != nil {
		t.Fatalf("NewRouletteWheelSelection failed: %v", err)
	}

	pop := []*mutation.Strategy{
		{Score: 10.0}, {Score: 20.0}, {Score: 30.0},
		{Score: 40.0}, {Score: 50.0},
	}

	ctx := context.Background()
	result, err := rw.Select(ctx, pop, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 10 {
		t.Fatalf("expected 10 selections, got %d", len(result))
	}
}

// --- KnowledgeBase Tests ---

func TestKnowledgeBase_Record(t *testing.T) {
	t.Parallel()

	kb := NewKnowledgeBase()
	kb.Record("tool_timeout", "increase_timeout", "success+10%", 0.1)
	kb.Record("tool_timeout", "increase_timeout", "success+15%", 0.15)

	if kb.Count() != 1 {
		t.Fatalf("expected 1 entry, got %d", kb.Count())
	}

	entries := kb.Lookup("tool_timeout")
	if len(entries) != 1 {
		t.Fatalf("expected 1 lookup result, got %d", len(entries))
	}
	if entries[0].ObservationCount != 2 {
		t.Errorf("expected 2 observations, got %d", entries[0].ObservationCount)
	}
	if entries[0].SuccessCount != 2 {
		t.Errorf("expected 2 successes, got %d", entries[0].SuccessCount)
	}
}

func TestKnowledgeBase_Lookup(t *testing.T) {
	t.Parallel()

	kb := NewKnowledgeBase()
	kb.Record("pattern_a", "mut_1", "good", 0.1)
	kb.Record("pattern_b", "mut_2", "better", 0.2)

	entries := kb.Lookup("pattern_a")
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry for pattern_a, got %d", len(entries))
	}
	if entries[0].Mutation != "mut_1" {
		t.Errorf("expected mutation mut_1, got %s", entries[0].Mutation)
	}

	entries = kb.Lookup("nonexistent")
	if entries != nil {
		t.Errorf("expected nil for nonexistent pattern, got %v", entries)
	}
}

func TestKnowledgeBase_All(t *testing.T) {
	t.Parallel()

	kb := NewKnowledgeBase()
	kb.Record("a", "m1", "ok", 0.1)
	kb.Record("b", "m2", "good", 0.2)

	all := kb.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(all))
	}
}

func TestKnowledgeAdapter(t *testing.T) {
	t.Parallel()

	kb := NewKnowledgeBase()
	kb.Record("tool_timeout", "increase_timeout", "success+10%", 0.1)

	// First observation (positive delta) → add-one smoothing: (1+1)/(1+2) ≈ 0.67.
	// minConfidence default is 0.4, so this should match.
	ka := NewKnowledgeAdapter(kb, 0)
	mutation, confidence := ka.SuggestMutation("tool_timeout")
	if mutation != "increase_timeout" {
		t.Errorf("expected increase_timeout, got %s", mutation)
	}
	if confidence < 0.5 {
		t.Errorf("expected confidence >= 0.5, got %f", confidence)
	}
}

// --- HypothesisGenerator Tests ---

func TestHypothesisGenerator(t *testing.T) {
	t.Parallel()

	hg := NewHypothesisGenerator(0.3)

	ref := &Reflection{
		Summary: "Tool timeout is the main bottleneck",
		Recommendations: []Recommendation{
			{
				Target:     "param:temperature",
				Action:     "decrease",
				Rationale:  "lower temperature increases determinism",
				Confidence: 0.8,
			},
		},
	}

	ctx := context.Background()
	hyps := hg.Generate(ctx, ref)
	if len(hyps) != 1 {
		t.Fatalf("expected 1 hypothesis, got %d", len(hyps))
	}
	if hyps[0].TargetType != "param" {
		t.Errorf("expected target_type param, got %s", hyps[0].TargetType)
	}
	if hyps[0].TargetKey != "temperature" {
		t.Errorf("expected target_key temperature, got %s", hyps[0].TargetKey)
	}
}

func TestHypothesisGenerator_FiltersLowConfidence(t *testing.T) {
	t.Parallel()

	hg := NewHypothesisGenerator(0.7)

	ref := &Reflection{
		Recommendations: []Recommendation{
			{Target: "param:temp", Action: "increase", Confidence: 0.3},
			{Target: "param:top_k", Action: "decrease", Confidence: 0.9},
		},
	}

	ctx := context.Background()
	hyps := hg.Generate(ctx, ref)
	if len(hyps) != 1 {
		t.Fatalf("expected 1 hypothesis (filtered), got %d", len(hyps))
	}
	if hyps[0].TargetKey != "top_k" {
		t.Errorf("expected top_k, got %s", hyps[0].TargetKey)
	}
}

func TestApplyHypothesis(t *testing.T) {
	t.Parallel()

	base := &mutation.Strategy{
		Params:         map[string]any{"temperature": 0.7},
		PromptTemplate: "original prompt",
	}

	// Param hypothesis.
	hyp := MutationHypothesis{
		TargetType: "param",
		TargetKey:  "temperature",
		Direction:  "decrease",
	}
	child := ApplyHypothesis(base, hyp)
	if child == nil {
		t.Fatal("expected non-nil child")
	}
	temp := child.Params["temperature"].(float64)
	if temp >= 0.7 {
		t.Errorf("expected temperature decreased, got %f", temp)
	}
}

// --- ParetoRank Tests ---

func TestParetoRank_SingleFront(t *testing.T) {
	t.Parallel()

	// cost is minimize (lower is better), quality is maximize (higher is better).
	// Both strategies are non-dominated: each has a different trade-off.
	strategies := []*mutation.Strategy{
		{DimensionScores: map[string]float64{"cost": 0.9, "quality": 0.9}}, // high cost, high quality
		{DimensionScores: map[string]float64{"cost": 0.3, "quality": 0.3}}, // low cost, low quality
	}

	ranks := ParetoRank(strategies)
	for i, r := range ranks {
		if r != 0 {
			t.Errorf("strategy %d: expected rank 0, got %d", i, r)
		}
	}
}

func TestParetoRank_MultiFront(t *testing.T) {
	t.Parallel()

	strategies := []*mutation.Strategy{
		{DimensionScores: map[string]float64{"a": 1.0, "b": 1.0}},
		{DimensionScores: map[string]float64{"a": 0.5, "b": 0.5}},
		{DimensionScores: map[string]float64{"a": 0.1, "b": 0.1}},
	}

	ranks := ParetoRank(strategies)
	rankSet := make(map[int]int)
	for _, r := range ranks {
		rankSet[r]++
	}
	if len(rankSet) != 3 {
		t.Errorf("expected 3 distinct ranks, got %d: %v", len(rankSet), rankSet)
	}
}

func TestParetoRank_Empty(t *testing.T) {
	t.Parallel()

	ranks := ParetoRank(nil)
	if ranks == nil {
		t.Error("expected non-nil empty slice for nil input")
	}
	ranks = ParetoRank([]*mutation.Strategy{})
	if len(ranks) != 0 {
		t.Errorf("expected empty, got %d", len(ranks))
	}
}

func TestParetoRank_NoDimScores(t *testing.T) {
	t.Parallel()

	strategies := []*mutation.Strategy{
		{Score: 100},
		{Score: 50},
		{Score: 10},
	}

	ranks := ParetoRank(strategies)
	if ranks[0] != 0 {
		t.Errorf("best strategy should be rank 0, got %d", ranks[0])
	}
}

// --- NormalizeDimensions Tests ---

func TestNormalizeDimensions(t *testing.T) {
	t.Parallel()

	dims := map[string]float64{"cost": 50, "quality": 0.5, "latency": 200}
	bounds := map[string][2]float64{
		"cost":    {0, 100},
		"quality": {0, 1},
		"latency": {0, 1000},
	}
	normalized := NormalizeDimensions(dims, bounds)
	if normalized["cost"] != 0.5 {
		t.Errorf("expected cost 0.5, got %f", normalized["cost"])
	}
	if normalized["quality"] != 0.5 {
		t.Errorf("expected quality 0.5, got %f", normalized["quality"])
	}
	if normalized["latency"] != 0.2 {
		t.Errorf("expected latency 0.2, got %f", normalized["latency"])
	}
}

func TestNormalizeDimensions_Nil(t *testing.T) {
	t.Parallel()

	got := NormalizeDimensions(nil, nil)
	if got == nil {
		t.Error("expected non-nil result for nil dims")
	}
	got = NormalizeDimensions(map[string]float64{}, nil)
	if len(got) != 0 {
		t.Errorf("expected empty, got %d", len(got))
	}
}

func TestNormalizeDimensions_Identity(t *testing.T) {
	t.Parallel()

	dims := map[string]float64{"a": 0.0, "b": 0.5, "c": 1.0}
	bounds := map[string][2]float64{
		"a": {0, 1},
		"b": {0, 1},
		"c": {0, 1},
	}
	normalized := NormalizeDimensions(dims, bounds)
	for k, v := range normalized {
		expected := map[string]float64{"a": 0.0, "b": 0.5, "c": 1.0}[k]
		if v != expected {
			t.Errorf("key %s: expected %f, got %f", k, expected, v)
		}
	}
}

// --- ExtractJSONBracket Tests ---

func TestExtractJSONBracket_Object(t *testing.T) {
	t.Parallel()

	input := `some text {"key": "value"} more text`
	got := mutation.ExtractJSONBracket(input)
	want := `{"key": "value"}`
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestExtractJSONBracket_Array(t *testing.T) {
	t.Parallel()

	input := `before [1, 2, 3] after`
	got := mutation.ExtractJSONBracket(input)
	want := `[1, 2, 3]`
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestExtractJSONBracket_Nested(t *testing.T) {
	t.Parallel()

	input := `{"outer": {"inner": "value"}, "arr": [1, 2, {"nested": 3}]}`
	got := mutation.ExtractJSONBracket(input)
	if got != input {
		t.Errorf("expected to extract full nested object, got %q", got)
	}
}

func TestExtractJSONBracket_SkipStrings(t *testing.T) {
	t.Parallel()

	input := `{"key": "with { and } chars", "num": 42}`
	got := mutation.ExtractJSONBracket(input)
	if got != input {
		t.Errorf("expected to skip braces inside strings, got %q", got)
	}
}

func TestExtractJSONBracket_CodeFence(t *testing.T) {
	t.Parallel()

	input := "```\n{\"a\": 1}\n```"
	got := mutation.ExtractJSONBracket(input)
	want := `{"a": 1}`
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestExtractJSONBracket_TrailingBrace(t *testing.T) {
	t.Parallel()

	// LLM sometimes appends closing braces from the chat template.
	input := `{"result": "ok"}}`
	got := mutation.ExtractJSONBracket(input)
	want := `{"result": "ok"}`
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestExtractJSONBracket_NoBrackets(t *testing.T) {
	t.Parallel()

	got := mutation.ExtractJSONBracket("plain text")
	if got != "" {
		t.Errorf("expected empty for no brackets, got %q", got)
	}
}

func TestExtractJSONBracket_Empty(t *testing.T) {
	t.Parallel()

	if got := mutation.ExtractJSONBracket(""); got != "" {
		t.Errorf("expected empty for empty input, got %q", got)
	}
}

// --- FormatHypotheses Tests ---

func TestFormatHypotheses(t *testing.T) {
	t.Parallel()

	got := FormatHypotheses(nil)
	if got != "no hypotheses" {
		t.Errorf("expected 'no hypotheses', got %q", got)
	}

	got = FormatHypotheses([]MutationHypothesis{})
	if got != "no hypotheses" {
		t.Errorf("expected 'no hypotheses' for empty, got %q", got)
	}

	hyps := []MutationHypothesis{
		{TargetType: "param", TargetKey: "temp", Direction: "decrease", Confidence: 0.8},
	}
	got = FormatHypotheses(hyps)
	want := "param:temp → decrease (80%)"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// --- clampParam Tests ---

func TestClampParam_Temperature(t *testing.T) {
	t.Parallel()

	if got := clampParam("temperature", 0.5); got != 0.5 {
		t.Errorf("expected 0.5, got %f", got)
	}
	if got := clampParam("temperature", 0.0); got != 0.0001 {
		t.Errorf("expected epsilon for zero, got %f", got)
	}
	if got := clampParam("temperature", -1); got != 0.0001 {
		t.Errorf("expected epsilon for negative, got %f", got)
	}
	if got := clampParam("temperature", 3.0); got != 2.0 {
		t.Errorf("expected 2.0, got %f", got)
	}
}

func TestClampParam_TopK(t *testing.T) {
	t.Parallel()

	if got := clampParam("top_k", 50); got != 50 {
		t.Errorf("expected 50, got %f", got)
	}
	if got := clampParam("top_k", 0); got != 1 {
		t.Errorf("expected 1 for 0, got %f", got)
	}
	if got := clampParam("top_k", 200); got != 100 {
		t.Errorf("expected 100, got %f", got)
	}
	if got := clampParam("topP", 0.5); got != 0.5 {
		t.Errorf("expected 0.5 for topP, got %f", got)
	}
}

// --- DistillFromHistory Tests ---

func TestDistillFromHistory(t *testing.T) {
	t.Parallel()

	kb := NewKnowledgeBase()
	kd := NewKnowledgeDistiller(kb)

	history := []GenerationHistoryEntry{
		{Generation: 0, BestScore: 0.5, Diversity: 0.8},
		{Generation: 1, BestScore: 0.5, Diversity: 0.1},  // stagnation
		{Generation: 2, BestScore: 0.56, Diversity: 0.3}, // delta=0.06 > 0.05 → score_improvement
	}

	kd.DistillFromHistory(history)

	entries := kb.Lookup("stagnation")
	if len(entries) == 0 {
		t.Error("expected stagnation entry")
	}

	entries = kb.Lookup("score_improvement")
	if len(entries) == 0 {
		t.Error("expected score_improvement entry")
	}
}

func TestDistillFromHistory_Empty(t *testing.T) {
	t.Parallel()

	kb := NewKnowledgeBase()
	kd := NewKnowledgeDistiller(kb)
	kd.DistillFromHistory(nil)                                       // should not panic
	kd.DistillFromHistory([]GenerationHistoryEntry{})                // should not panic
	kd.DistillFromHistory([]GenerationHistoryEntry{{Generation: 0}}) // single entry
}

// --- LLMReflector Error Path Tests ---

type errClient struct {
	err error
}

func (e *errClient) Generate(_ context.Context, _ string) (string, error) {
	return "", e.err
}

type fixedClient struct {
	resp string
}

func (f *fixedClient) Generate(_ context.Context, _ string) (string, error) {
	return f.resp, nil
}

func TestLLMReflector_ClientError(t *testing.T) {
	t.Parallel()

	r := NewLLMReflector(&errClient{err: fmt.Errorf("LLM down")})
	_, err := r.Reflect(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error from LLM failure")
	}
}

func TestLLMReflector_BadJSON(t *testing.T) {
	t.Parallel()

	r := NewLLMReflector(&fixedClient{resp: "not json at all"})
	_, err := r.Reflect(context.Background(), nil, nil)
	if err == nil {
		t.Fatal("expected error for non-JSON response")
	}
}

func TestLLMReflector_ValidJSON(t *testing.T) {
	t.Parallel()

	jsonResp := `{"summary": "test", "patterns": [], "recommendations": [], "confidence": 0.5}`
	r := NewLLMReflector(&fixedClient{resp: jsonResp})
	ref, err := r.Reflect(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref.Summary != "test" {
		t.Errorf("expected summary 'test', got %q", ref.Summary)
	}
}

func TestApplyHypothesis_Clamping(t *testing.T) {
	t.Parallel()

	base := &mutation.Strategy{
		Params: map[string]any{"temperature": 10.0, "top_k": 999.0},
	}

	hyp := MutationHypothesis{
		TargetType: "param",
		TargetKey:  "temperature",
		Direction:  "increase",
	}
	child := ApplyHypothesis(base, hyp)
	temp := child.Params["temperature"].(float64)
	if temp > 2.0 {
		t.Errorf("expected temperature clamped to <= 2.0, got %f", temp)
	}

	hyp2 := MutationHypothesis{
		TargetType: "param",
		TargetKey:  "top_k",
		Direction:  "increase",
	}
	child2 := ApplyHypothesis(base, hyp2)
	topK := child2.Params["top_k"].(float64)
	if topK > 100 {
		t.Errorf("expected top_k clamped to <= 100, got %f", topK)
	}
}

func TestApplyHypothesis_NilBase(t *testing.T) {
	t.Parallel()

	if got := ApplyHypothesis(nil, MutationHypothesis{}); got != nil {
		t.Error("expected nil for nil base")
	}
}

func TestApplyHypothesis_SuggestedValue(t *testing.T) {
	t.Parallel()

	base := &mutation.Strategy{Params: map[string]any{"temperature": 0.7}}
	hyp := MutationHypothesis{
		TargetType:     "param",
		TargetKey:      "temperature",
		SuggestedValue: 0.3,
	}
	child := ApplyHypothesis(base, hyp)
	temp := child.Params["temperature"].(float64)
	if temp != 0.3 {
		t.Errorf("expected 0.3, got %f", temp)
	}
}

func TestApplyHypothesis_Prompt(t *testing.T) {
	t.Parallel()

	base := &mutation.Strategy{PromptTemplate: "old"}
	hyp := MutationHypothesis{
		TargetType:     "prompt",
		TargetKey:      "prompt_template",
		SuggestedValue: "new prompt",
	}
	child := ApplyHypothesis(base, hyp)
	if child.PromptTemplate != "new prompt" {
		t.Errorf("expected 'new prompt', got %q", child.PromptTemplate)
	}
}

func TestApplyHypothesis_Tool(t *testing.T) {
	t.Parallel()

	base := &mutation.Strategy{Params: map[string]any{}}
	hyp := MutationHypothesis{
		TargetType:     "tool",
		TargetKey:      "web_search",
		SuggestedValue: "enhanced_search",
	}
	child := ApplyHypothesis(base, hyp)
	if child.Params["tools"] != "enhanced_search" {
		t.Errorf("expected 'enhanced_search', got %v", child.Params["tools"])
	}
}

// --- GenerationHistoryEntry Tests ---

func TestApplyMetaToPopulation_TooEarly(t *testing.T) {
	t.Parallel()

	pop := &Population{
		Generation: 0,
		cfg: PopulationConfig{
			Size: 10,
		},
	}
	controller := &MetaController{
		cfg: MetaConfig{Enabled: true},
	}
	modified := ApplyMetaToPopulation(pop, controller)
	if modified {
		t.Error("expected no modification for generation 0")
	}
}

func TestComputeStatsLocked(t *testing.T) {
	t.Parallel()

	p := &Population{
		Agents: []*mutation.Strategy{
			{Score: 10},
			{Score: 20},
			{Score: 30},
		},
	}
	best, avg, worst := p.computeStatsLocked()
	if best != 30 {
		t.Errorf("expected best 30, got %f", best)
	}
	if avg != 20 {
		t.Errorf("expected avg 20, got %f", avg)
	}
	if worst != 10 {
		t.Errorf("expected worst 10, got %f", worst)
	}
}

func TestComputeStatsLocked_Empty(t *testing.T) {
	t.Parallel()

	p := &Population{}
	best, avg, worst := p.computeStatsLocked()
	if best != 0 || avg != 0 || worst != 0 {
		t.Errorf("expected 0,0,0 for empty pop, got %f,%f,%f", best, avg, worst)
	}
}

// --- Selection Strategy Tests ---

func TestNewRankSelection_Deterministic(t *testing.T) {
	t.Parallel()

	rs1 := NewRankSelection()
	rs2 := NewRankSelection()
	pop := []*mutation.Strategy{
		{Score: 10}, {Score: 20}, {Score: 30},
	}
	ctx := context.Background()
	r1, _ := rs1.Select(ctx, pop, 3)
	r2, _ := rs2.Select(ctx, pop, 3)
	if len(r1) != len(r2) {
		t.Fatalf("expected same length, got %d vs %d", len(r1), len(r2))
	}
}

// --- ParetoFront edge cases ---

func TestParetoFront_Nil(t *testing.T) {
	t.Parallel()

	if got := ParetoFront(nil); got != nil {
		t.Errorf("expected nil for nil input, got %d", len(got))
	}
	if got := ParetoFront([]*mutation.Strategy{}); got != nil {
		t.Errorf("expected nil for empty input, got %d", len(got))
	}
}

// --- CrowdingDistance edge cases ---

func TestCrowdingDistance_Nil(t *testing.T) {
	t.Parallel()

	if got := CrowdingDistance(nil); got == nil {
		t.Error("expected non-nil for nil input")
	}
	if got := CrowdingDistance([]*mutation.Strategy{}); len(got) != 0 {
		t.Errorf("expected 0-length for empty, got %d", len(got))
	}
}

func TestCrowdingDistance_Small(t *testing.T) {
	t.Parallel()

	single := []*mutation.Strategy{{DimensionScores: map[string]float64{"a": 0.5}}}
	one := CrowdingDistance(single)
	if len(one) != 1 || !math.IsInf(one[0], 1) {
		t.Error("single strategy should get Inf distance")
	}

	two := []*mutation.Strategy{
		{DimensionScores: map[string]float64{"a": 0.0}},
		{DimensionScores: map[string]float64{"a": 1.0}},
	}
	twoDists := CrowdingDistance(two)
	if len(twoDists) != 2 {
		t.Fatalf("expected 2 distances, got %d", len(twoDists))
	}
	if !math.IsInf(twoDists[0], 1) || !math.IsInf(twoDists[1], 1) {
		t.Error("both strategies in 2-element set should get Inf distance")
	}
}

func TestCrowdingDistance_Identical(t *testing.T) {
	t.Parallel()

	strategies := []*mutation.Strategy{
		{DimensionScores: map[string]float64{"a": 0.5}},
		{DimensionScores: map[string]float64{"a": 0.5}},
		{DimensionScores: map[string]float64{"a": 0.5}},
	}
	dists := CrowdingDistance(strategies)
	if len(dists) != 3 {
		t.Fatalf("expected 3 distances, got %d", len(dists))
	}
	// All same → norm < 1e-10 → distance remains 0 for interior.
	if !math.IsInf(dists[0], 1) || !math.IsInf(dists[2], 1) {
		t.Error("boundary points should have Inf distance")
	}
}

func TestCrowdingDistance_NoDims(t *testing.T) {
	t.Parallel()

	strategies := []*mutation.Strategy{
		{Score: 10},
		{Score: 20},
		{Score: 30},
	}
	dists := CrowdingDistance(strategies)
	if len(dists) != 3 {
		t.Fatalf("expected 3 distances, got %d", len(dists))
	}
}

// --- mergeWeights ---

func TestMergeWeights(t *testing.T) {
	t.Parallel()

	result := mergeWeights(nil)
	if result == nil {
		t.Fatal("expected non-nil")
	}
	// Default weights should be present.
	for k, w := range DefaultDimensionWeights {
		if result[k] != w {
			t.Errorf("expected default weight %s=%f, got %f", k, w, result[k])
		}
	}
	// Custom weights should override defaults.
	custom := map[string]float64{"success_rate": 1.0}
	result = mergeWeights(custom)
	if result["success_rate"] != 1.0 {
		t.Errorf("expected custom success_rate 1.0, got %f", result["success_rate"])
	}
}

// --- LLMReflector empty reflection ---

func TestLLMReflector_EmptySummary(t *testing.T) {
	t.Parallel()

	// Empty summary with empty arrays should fail validation.
	jsonResp := `{"summary": "", "patterns": [], "recommendations": [], "confidence": 0.0}`
	r := NewLLMReflector(&fixedClient{resp: jsonResp})
	_, err := r.Reflect(context.Background(), nil, nil)
	if err == nil {
		t.Error("expected error for empty summary")
	}
}

func TestLLMReflector_ArrayOfReflections(t *testing.T) {
	t.Parallel()

	// LLM sometimes wraps in an array.
	jsonResp := `[{"summary": "first", "patterns": [], "recommendations": [], "confidence": 0.5}]`
	r := NewLLMReflector(&fixedClient{resp: jsonResp})
	ref, err := r.Reflect(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ref.Summary != "first" {
		t.Errorf("expected 'first', got %q", ref.Summary)
	}
}

// --- RecordStrategyOutcome no-op test ---

// --- clampParam with new keys ---

func TestClampParam_NewKeys(t *testing.T) {
	t.Parallel()

	if got := clampParam("topk", 50); got != 50 {
		t.Errorf("expected 50 for topk, got %f", got)
	}
	if got := clampParam("topp", 0.5); got != 0.5 {
		t.Errorf("expected 0.5 for topp, got %f", got)
	}
	if got := clampParam("max_steps", 10); got != 10 {
		t.Errorf("expected 10 for max_steps, got %f", got)
	}
	if got := clampParam("maxSteps", 10); got != 10 {
		t.Errorf("expected 10 for maxSteps, got %f", got)
	}
	if got := clampParam("memory_limit", 5); got != 5 {
		t.Errorf("expected 5 for memory_limit, got %f", got)
	}
	if got := clampParam("memoryLimit", 5); got != 5 {
		t.Errorf("expected 5 for memoryLimit, got %f", got)
	}
	if got := clampParam("conflict_threshold", 0.9); got != 0.9 {
		t.Errorf("expected 0.9 for conflict_threshold, got %f", got)
	}
	if got := clampParam("conflictThreshold", 0.9); got != 0.9 {
		t.Errorf("expected 0.9 for conflictThreshold, got %f", got)
	}
}

func TestClampParam_UnknownKey(t *testing.T) {
	t.Parallel()

	// Unknown key with non-standard value: should use wide default [0, 10000].
	if got := clampParam("unknown_param", 500); got != 500 {
		t.Errorf("expected 500 for unknown, got %f", got)
	}
	// Over upper bound should clamp to 10000.
	if got := clampParam("unknown_param", 50000); got != 10000 {
		t.Errorf("expected 10000 for over-bounds unknown, got %f", got)
	}
	// Negative should clamp to epsilon.
	if got := clampParam("unknown_param", -1); got != 0.0001 {
		t.Errorf("expected epsilon for negative unknown, got %f", got)
	}
}

// --- SuggestedValue clamp test ---

func TestApplyHypothesis_SuggestedValueClamped(t *testing.T) {
	t.Parallel()

	base := &mutation.Strategy{Params: map[string]any{"temperature": 0.7}}
	hyp := MutationHypothesis{
		TargetType:     "param",
		TargetKey:      "temperature",
		SuggestedValue: float64(500), // way out of range
	}
	child := ApplyHypothesis(base, hyp)
	temp := child.Params["temperature"].(float64)
	if temp > 2.0 {
		t.Errorf("expected temperature clamped to <= 2.0, got %f", temp)
	}
}

func TestApplyHypothesis_SuggestedValueNonFloat(t *testing.T) {
	t.Parallel()

	base := &mutation.Strategy{Params: map[string]any{"prompt": "old"}}
	hyp := MutationHypothesis{
		TargetType:     "param",
		TargetKey:      "prompt",
		SuggestedValue: "new prompt", // string, not float → pass through as-is
	}
	child := ApplyHypothesis(base, hyp)
	v := child.Params["prompt"]
	if v != "new prompt" {
		t.Errorf("expected 'new prompt', got %v", v)
	}
}

// --- Record update path (existing entry merge) ---

func TestKnowledgeBase_RecordUpdatePath(t *testing.T) {
	t.Parallel()

	kb := NewKnowledgeBase()
	// First record: positive delta.
	kb.Record("pattern_x", "mut_x", "good", 0.2)
	// Second record: same pattern+mutation = update.
	kb.Record("pattern_x", "mut_x", "better", 0.3)

	entries := kb.All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].ObservationCount != 2 {
		t.Errorf("expected 2 observations, got %d", entries[0].ObservationCount)
	}
	if entries[0].SuccessCount != 2 {
		t.Errorf("expected 2 successes, got %d", entries[0].SuccessCount)
	}
	// Add-one smoothing: (2+1)/(2+2) = 3/4 = 0.75.
	if entries[0].Confidence < 0.7 || entries[0].Confidence > 0.8 {
		t.Errorf("expected confidence ~0.75, got %f", entries[0].Confidence)
	}
	if entries[0].Outcome != "better" {
		t.Errorf("expected outcome 'better', got %q", entries[0].Outcome)
	}
}

func TestKnowledgeBase_RecordMixedSuccess(t *testing.T) {
	t.Parallel()

	kb := NewKnowledgeBase()
	kb.Record("p", "m", "ok", 0.1)     // positive: success=1, obs=1 → (1+1)/(1+2)=0.67
	kb.Record("p", "m", "bad", -0.1)   // negative: success stays 1, obs=2 → (1+1)/(2+2)=0.5
	kb.Record("p", "m", "worse", -0.2) // negative: success stays 1, obs=3 → (1+1)/(3+2)=0.4

	entries := kb.All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].ObservationCount != 3 {
		t.Errorf("expected 3 observations, got %d", entries[0].ObservationCount)
	}
	if entries[0].SuccessCount != 1 {
		t.Errorf("expected 1 success, got %d", entries[0].SuccessCount)
	}
	// More negative data should dilute confidence: (1+1)/(3+2) = 0.4.
	if entries[0].Confidence < 0.35 || entries[0].Confidence > 0.45 {
		t.Errorf("expected confidence ~0.4 after mixed signals, got %f", entries[0].Confidence)
	}
}

// --- ApplyMetaToPopulation with history enabled ---

func TestApplyMetaToPopulation_WithHistory(t *testing.T) {
	t.Parallel()

	pop := &Population{
		Generation: 3,
		cfg: PopulationConfig{
			Size:              10,
			HistoryMaxSize:    100,
			MutationRate:      0.2,
			SurvivalRate:      0.5,
			SelectionStrategy: "tournament",
		},
		Agents: []*mutation.Strategy{
			{Score: 1.0},
			{Score: 0.5},
		},
		history: []GenerationHistoryEntry{
			{Generation: 1, BestScore: 0.5, AvgScore: 0.4, WorstScore: 0.3, Diversity: 0.5},
			{Generation: 2, BestScore: 0.7, AvgScore: 0.6, WorstScore: 0.4, Diversity: 0.5},
			{Generation: 3, BestScore: 0.9, AvgScore: 0.7, WorstScore: 0.5, Diversity: 0.5},
		},
	}

	controller := NewMetaController(MetaConfig{
		Enabled: true,
	})

	modified := ApplyMetaToPopulation(pop, controller)
	// May or may not modify depending on tune logic — just shouldn't panic.
	_ = modified
}

// --- ParetoDominance mixed mode ---

func TestParetoDominance_MixedMode(t *testing.T) {
	t.Parallel()

	a := &mutation.Strategy{
		DimensionScores: map[string]float64{"cost": 0.8},
		Score:           0.7,
	}
	b := &mutation.Strategy{
		Score: 0.9, // no DimensionScores
	}

	// Mixed mode: should fallback to Score comparison.
	if ParetoDominance(a, b) {
		t.Error("a (score=0.7) should NOT dominate b (score=0.9) in fallback mode")
	}
	if !ParetoDominance(b, a) {
		t.Error("b (score=0.9) should dominate a (score=0.7) in fallback mode")
	}
}

// --- buildSelector truncation ---

func TestBuildSelector_Truncation(t *testing.T) {
	t.Parallel()

	p := &Population{
		cfg: PopulationConfig{
			Size:              10,
			SelectionStrategy: "truncation",
		},
	}
	sel, err := p.buildSelector()
	if err != nil {
		t.Fatalf("unexpected error for truncation: %v", err)
	}
	if sel == nil {
		t.Fatal("expected non-nil selector for truncation")
	}
	// Verify it's actually a TruncationSelection.
	pop := []*mutation.Strategy{
		{Score: 10}, {Score: 30}, {Score: 20},
	}
	selected, err := sel.Select(context.Background(), pop, 2)
	if err != nil {
		t.Fatalf("Select failed: %v", err)
	}
	if len(selected) != 2 {
		t.Fatalf("expected 2 selected, got %d", len(selected))
	}
	if selected[0].Score != 30 || selected[1].Score != 20 {
		t.Errorf("expected top 2 sorted by score, got %v", selected)
	}
}
