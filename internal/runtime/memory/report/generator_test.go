// Package report provides human-readable evolution report generation.
package report

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeKnowledgeSource is a deterministic in-memory KnowledgeSource for tests.
type fakeKnowledgeSource struct {
	mu       sync.Mutex
	items    []KnowledgeItem
	listErr  error
	listCall int
}

func newFakeKnowledgeSource(items []KnowledgeItem) *fakeKnowledgeSource {
	return &fakeKnowledgeSource{items: items}
}

func (f *fakeKnowledgeSource) ListKnowledge(ctx context.Context, tenantID string) ([]KnowledgeItem, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCall++
	if f.listErr != nil {
		return nil, f.listErr
	}
	// Return a copy to avoid callers mutating internal state.
	out := make([]KnowledgeItem, len(f.items))
	copy(out, f.items)
	return out, nil
}

// sampleItems returns a deterministic set of KnowledgeItem values for tests.
// The set covers multiple categories, confidence levels, and conflict states.
func sampleItems() []KnowledgeItem {
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	return []KnowledgeItem{
		{
			ID:         "k-1",
			Category:   "fact",
			Problem:    "service-a returning 503 errors",
			Solution:   "increase rate limit to 1000 rps",
			Score:      0.92,
			Source:     "conv-1",
			StrategyID: "strategy-alpha",
			TaskType:   "incident_response",
			CreatedAt:  base.Add(2 * time.Hour),
		},
		{
			ID:                 "k-2",
			Category:           "solution",
			Problem:            "memory leak in worker pool",
			Solution:           "add finalizer to release connections on shutdown",
			Score:              0.81,
			Source:             "conv-2",
			StrategyID:         "strategy-beta",
			TaskType:           "bug_fix",
			ConflictResolved:   true,
			ResolutionStrategy: "replace",
			CreatedAt:          base.Add(1 * time.Hour),
		},
		{
			ID:        "k-3",
			Category:  "preference",
			Problem:   "users prefer dark mode",
			Solution:  "default theme = dark",
			Score:     0.45,
			Source:    "conv-3",
			TaskType:  "ux_tuning",
			CreatedAt: base.Add(3 * time.Hour),
		},
		{
			ID:        "k-4",
			Category:  "rule",
			Problem:   "low-confidence rule captured",
			Solution:  "needs more evidence before promotion",
			Score:     0.20,
			Source:    "conv-4",
			TaskType:  "exploration",
			CreatedAt: base,
		},
		{
			ID:        "k-5",
			Category:  "fact",
			Problem:   "another low-confidence fact",
			Solution:  "observe more executions",
			Score:     0.15,
			Source:    "conv-5",
			TaskType:  "exploration",
			CreatedAt: base.Add(4 * time.Hour),
		},
		{
			ID:        "k-7",
			Category:  "fact",
			Problem:   "third low-confidence fact for collect rule",
			Solution:  "needs more data",
			Score:     0.10,
			Source:    "conv-7",
			TaskType:  "exploration",
			CreatedAt: base.Add(6 * time.Hour),
		},
		{
			ID:                 "k-6",
			Category:           "solution",
			Problem:            "conflicting solution a",
			Solution:           "approach a",
			Score:              0.70,
			Source:             "conv-6",
			ConflictResolved:   true,
			ResolutionStrategy: "version",
			CreatedAt:          base.Add(5 * time.Hour),
		},
	}
}

func TestNewReportGenerator_NilSource(t *testing.T) {
	g, err := NewReportGenerator(nil, DefaultReportConfig())
	require.Error(t, err)
	assert.Nil(t, g)
	assert.ErrorIs(t, err, ErrInvalidConfig)
}

func TestNewReportGenerator_InvalidTopN(t *testing.T) {
	src := newFakeKnowledgeSource(nil)
	g, err := NewReportGenerator(src, &ReportConfig{TopN: -1})
	require.Error(t, err)
	assert.Nil(t, g)
	assert.ErrorIs(t, err, ErrInvalidConfig)
}

func TestNewReportGenerator_InvalidMinScore(t *testing.T) {
	src := newFakeKnowledgeSource(nil)
	g, err := NewReportGenerator(src, &ReportConfig{TopN: 5, MinScore: 1.5})
	require.Error(t, err)
	assert.Nil(t, g)
	assert.ErrorIs(t, err, ErrInvalidConfig)
}

func TestNewReportGenerator_NilConfig_UsesDefaults(t *testing.T) {
	src := newFakeKnowledgeSource(sampleItems())
	g, err := NewReportGenerator(src, nil)
	require.NoError(t, err)
	require.NotNil(t, g)
	assert.Equal(t, 10, g.config.TopN)
}

func TestDefaultReportGenerator_Generate_Summary(t *testing.T) {
	src := newFakeKnowledgeSource(sampleItems())
	g, err := NewReportGenerator(src, DefaultReportConfig())
	require.NoError(t, err)

	rpt, err := g.Generate(context.Background(), "tenant-1")
	require.NoError(t, err)
	require.NotNil(t, rpt)

	assert.Equal(t, "tenant-1", rpt.TenantID)
	assert.Equal(t, 7, rpt.Summary.TotalItems)

	// Category distribution.
	assert.Equal(t, 3, rpt.Summary.ByCategory["fact"])
	assert.Equal(t, 2, rpt.Summary.ByCategory["solution"])
	assert.Equal(t, 1, rpt.Summary.ByCategory["preference"])
	assert.Equal(t, 1, rpt.Summary.ByCategory["rule"])

	// Confidence buckets: high [0.7,1.0]=3 (0.92,0.81,0.70), medium [0.4,0.7)=1 (0.45), low [0,0.4)=3 (0.20,0.15,0.10).
	assert.Equal(t, 3, rpt.Summary.ByConfidence[ConfidenceHigh])
	assert.Equal(t, 1, rpt.Summary.ByConfidence[ConfidenceMedium])
	assert.Equal(t, 3, rpt.Summary.ByConfidence[ConfidenceLow])

	// Aggregate stats: sum = 0.92+0.81+0.45+0.20+0.15+0.70+0.10 = 3.33; /7 = 0.4757.
	assert.InDelta(t, 0.476, rpt.Summary.AverageScore, 0.001)
	assert.InDelta(t, 0.92, rpt.Summary.MaxScore, 0.001)
	assert.InDelta(t, 0.10, rpt.Summary.MinScore, 0.001)
}

func TestDefaultReportGenerator_Generate_TopKnowledge_SortedByScore(t *testing.T) {
	src := newFakeKnowledgeSource(sampleItems())
	cfg := &ReportConfig{TopN: 3, IncludeConflicts: true, IncludeTrends: true, IncludeRecommendations: true}
	g, err := NewReportGenerator(src, cfg)
	require.NoError(t, err)

	rpt, err := g.Generate(context.Background(), "")
	require.NoError(t, err)

	require.Len(t, rpt.TopKnowledge.Items, 3)
	// Items must be sorted by score descending.
	assert.Equal(t, "k-1", rpt.TopKnowledge.Items[0].ID)
	assert.Equal(t, 0.92, rpt.TopKnowledge.Items[0].Score)
	assert.Equal(t, "k-2", rpt.TopKnowledge.Items[1].ID)
	assert.Equal(t, "k-6", rpt.TopKnowledge.Items[2].ID)
}

func TestDefaultReportGenerator_Generate_TopKnowledge_TiebreakByID(t *testing.T) {
	items := []KnowledgeItem{
		{ID: "b", Category: "fact", Score: 0.5, Problem: "p-b", Solution: "s-b"},
		{ID: "a", Category: "fact", Score: 0.5, Problem: "p-a", Solution: "s-a"},
		{ID: "c", Category: "fact", Score: 0.5, Problem: "p-c", Solution: "s-c"},
	}
	src := newFakeKnowledgeSource(items)
	cfg := &ReportConfig{TopN: 3, IncludeConflicts: false, IncludeTrends: false, IncludeRecommendations: false}
	g, err := NewReportGenerator(src, cfg)
	require.NoError(t, err)

	rpt, err := g.Generate(context.Background(), "")
	require.NoError(t, err)

	require.Len(t, rpt.TopKnowledge.Items, 3)
	assert.Equal(t, "a", rpt.TopKnowledge.Items[0].ID)
	assert.Equal(t, "b", rpt.TopKnowledge.Items[1].ID)
	assert.Equal(t, "c", rpt.TopKnowledge.Items[2].ID)
}

func TestDefaultReportGenerator_Generate_MinScoreFilter(t *testing.T) {
	src := newFakeKnowledgeSource(sampleItems())
	cfg := &ReportConfig{TopN: 10, MinScore: 0.4, IncludeConflicts: false, IncludeTrends: false, IncludeRecommendations: false}
	g, err := NewReportGenerator(src, cfg)
	require.NoError(t, err)

	rpt, err := g.Generate(context.Background(), "")
	require.NoError(t, err)

	// Items below 0.4 are filtered out: 0.20 and 0.15 removed.
	assert.Equal(t, 4, rpt.Summary.TotalItems)
	for _, item := range rpt.TopKnowledge.Items {
		assert.GreaterOrEqual(t, item.Score, 0.4)
	}
}

func TestDefaultReportGenerator_Generate_ConflictSection(t *testing.T) {
	src := newFakeKnowledgeSource(sampleItems())
	g, err := NewReportGenerator(src, DefaultReportConfig())
	require.NoError(t, err)

	rpt, err := g.Generate(context.Background(), "")
	require.NoError(t, err)

	assert.Equal(t, 2, rpt.Conflicts.TotalConflicts)
	assert.Equal(t, 1, rpt.Conflicts.ByStrategy["replace"])
	assert.Equal(t, 1, rpt.Conflicts.ByStrategy["version"])

	// Recent resolutions sorted by CreatedAt descending.
	require.Len(t, rpt.Conflicts.RecentResolutions, 2)
	assert.Equal(t, "k-6", rpt.Conflicts.RecentResolutions[0].ID)
	assert.Equal(t, "k-2", rpt.Conflicts.RecentResolutions[1].ID)
}

func TestDefaultReportGenerator_Generate_TrendsSection(t *testing.T) {
	src := newFakeKnowledgeSource(sampleItems())
	g, err := NewReportGenerator(src, DefaultReportConfig())
	require.NoError(t, err)

	rpt, err := g.Generate(context.Background(), "")
	require.NoError(t, err)

	// Three categories should appear; fact has 3 items (most), then solution (2), then rule/preference (1).
	require.NotEmpty(t, rpt.Trends.Trends)
	assert.Equal(t, "fact", rpt.Trends.Trends[0].Category)
	assert.Equal(t, 3, rpt.Trends.Trends[0].Count)
	assert.InDelta(t, 0.39, rpt.Trends.Trends[0].AverageScore, 0.001) // (0.92 + 0.15 + 0.10) / 3
}

func TestDefaultReportGenerator_Generate_Recommendations(t *testing.T) {
	src := newFakeKnowledgeSource(sampleItems())
	g, err := NewReportGenerator(src, DefaultReportConfig())
	require.NoError(t, err)

	rpt, err := g.Generate(context.Background(), "")
	require.NoError(t, err)

	// High-confidence strategy items -> deploy recommendations.
	var deployIDs []string
	for _, rec := range rpt.Recommendations.Recommendations {
		if rec.Priority == "high" {
			deployIDs = append(deployIDs, rec.ID)
		}
	}
	assert.Contains(t, deployIDs, "deploy-strategy-alpha")
	assert.Contains(t, deployIDs, "deploy-strategy-beta")

	// "fact" category has 2 items, 1 low-conf -> ratio 0.5, meets threshold.
	hasCollectFact := false
	for _, rec := range rpt.Recommendations.Recommendations {
		if rec.ID == "collect-fact" {
			hasCollectFact = true
		}
	}
	assert.True(t, hasCollectFact)

	// "solution" has 2 conflict resolutions -> review recommendation.
	hasReviewSolution := false
	for _, rec := range rpt.Recommendations.Recommendations {
		if rec.ID == "review-conflicts-solution" {
			hasReviewSolution = true
		}
	}
	assert.True(t, hasReviewSolution)

	// Recommendations sorted by priority: high, medium, low.
	for i := 1; i < len(rpt.Recommendations.Recommendations); i++ {
		prev := priorityRank(rpt.Recommendations.Recommendations[i-1].Priority)
		curr := priorityRank(rpt.Recommendations.Recommendations[i].Priority)
		assert.LessOrEqual(t, prev, curr, "recommendations should be sorted by priority")
	}
}

func priorityRank(p string) int {
	switch p {
	case "high":
		return 0
	case "medium":
		return 1
	case "low":
		return 2
	default:
		return 99
	}
}

func TestDefaultReportGenerator_Generate_EmptySource(t *testing.T) {
	src := newFakeKnowledgeSource(nil)
	g, err := NewReportGenerator(src, DefaultReportConfig())
	require.NoError(t, err)

	rpt, err := g.Generate(context.Background(), "t")
	require.NoError(t, err)
	require.NotNil(t, rpt)

	assert.Equal(t, 0, rpt.Summary.TotalItems)
	assert.Empty(t, rpt.TopKnowledge.Items)
	assert.Equal(t, 0, rpt.Conflicts.TotalConflicts)
	assert.Empty(t, rpt.Conflicts.ByStrategy)
	assert.Empty(t, rpt.Trends.Trends)
	assert.Empty(t, rpt.Recommendations.Recommendations)
}

func TestDefaultReportGenerator_Generate_SourceError(t *testing.T) {
	src := newFakeKnowledgeSource(nil)
	src.listErr = errors.New("database offline")
	g, err := NewReportGenerator(src, DefaultReportConfig())
	require.NoError(t, err)

	rpt, err := g.Generate(context.Background(), "")
	require.Error(t, err)
	assert.Nil(t, rpt)
	assert.Contains(t, err.Error(), "list knowledge")
}

func TestDefaultReportGenerator_Generate_CancelledContext(t *testing.T) {
	src := newFakeKnowledgeSource(sampleItems())
	g, err := NewReportGenerator(src, DefaultReportConfig())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rpt, err := g.Generate(ctx, "")
	require.Error(t, err)
	assert.Nil(t, rpt)
	assert.Contains(t, err.Error(), "report generate")
}

func TestDefaultReportGenerator_Generate_DisabledSections(t *testing.T) {
	src := newFakeKnowledgeSource(sampleItems())
	cfg := &ReportConfig{
		TopN:                   5,
		IncludeConflicts:       false,
		IncludeTrends:          false,
		IncludeRecommendations: false,
	}
	g, err := NewReportGenerator(src, cfg)
	require.NoError(t, err)

	rpt, err := g.Generate(context.Background(), "")
	require.NoError(t, err)

	// Conflicts disabled: zero values.
	assert.Equal(t, 0, rpt.Conflicts.TotalConflicts)
	assert.Empty(t, rpt.Conflicts.ByStrategy)
	// Trends disabled: empty list (not nil) for stable JSON output.
	assert.Empty(t, rpt.Trends.Trends)
	// Recommendations disabled.
	assert.Equal(t, 0, rpt.Recommendations.Total)
	assert.Empty(t, rpt.Recommendations.Recommendations)

	// TopKnowledge still populated.
	assert.NotEmpty(t, rpt.TopKnowledge.Items)
}

func TestReport_Format_HasExpectedSections(t *testing.T) {
	src := newFakeKnowledgeSource(sampleItems())
	g, err := NewReportGenerator(src, DefaultReportConfig())
	require.NoError(t, err)

	rpt, err := g.Generate(context.Background(), "tenant-1")
	require.NoError(t, err)

	out := rpt.Format()
	assert.NotEmpty(t, out)
	assert.Contains(t, out, "ARES Evolution Report")
	assert.Contains(t, out, "Tenant: tenant-1")
	assert.Contains(t, out, "## Summary")
	assert.Contains(t, out, "By Category")
	assert.Contains(t, out, "By Confidence")
	assert.Contains(t, out, "## Top Knowledge Items")
	assert.Contains(t, out, "## Conflict Resolutions")
	assert.Contains(t, out, "## Evolution Trends")
	assert.Contains(t, out, "## Recommendations")
}

func TestReport_String_EqualsFormat(t *testing.T) {
	src := newFakeKnowledgeSource(sampleItems())
	g, err := NewReportGenerator(src, DefaultReportConfig())
	require.NoError(t, err)

	rpt, err := g.Generate(context.Background(), "")
	require.NoError(t, err)

	assert.Equal(t, rpt.Format(), rpt.String())
}

func TestReport_Format_EmptyReport(t *testing.T) {
	rpt := &Report{
		GeneratedAt: time.Now(),
		TenantID:    "t",
	}
	out := rpt.Format()
	assert.Contains(t, out, "Total knowledge items: 0")
	assert.Contains(t, out, "_No knowledge items available_")
	assert.Contains(t, out, "_No recommendations_")
}

func TestReport_Format_TruncatesLongText(t *testing.T) {
	longProblem := ""
	for i := 0; i < 200; i++ {
		longProblem += "x"
	}
	src := newFakeKnowledgeSource([]KnowledgeItem{
		{ID: "k", Category: "fact", Problem: longProblem, Solution: "s", Score: 0.5},
	})
	cfg := &ReportConfig{TopN: 1, IncludeConflicts: false, IncludeTrends: false, IncludeRecommendations: false}
	g, err := NewReportGenerator(src, cfg)
	require.NoError(t, err)

	rpt, err := g.Generate(context.Background(), "")
	require.NoError(t, err)

	out := rpt.Format()
	assert.Contains(t, out, "...")
	// 80-char max + "..." but the truncate fn caps at maxLen-3 chars + "..."
	assert.LessOrEqual(t, len(out), 4096)
}

func TestReport_ConcurrentGenerate(t *testing.T) {
	src := newFakeKnowledgeSource(sampleItems())
	g, err := NewReportGenerator(src, DefaultReportConfig())
	require.NoError(t, err)

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rpt, err := g.Generate(context.Background(), "t")
			if err != nil {
				errs <- err
				return
			}
			if rpt == nil {
				errs <- errors.New("nil report")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent generate failed: %v", err)
	}
}
