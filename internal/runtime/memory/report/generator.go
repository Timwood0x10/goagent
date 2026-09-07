// Package report provides human-readable evolution report generation
// from distilled knowledge items.
package report

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// ReportGenerator converts distilled knowledge items into structured reports.
// Implementations must be safe for concurrent use.
type ReportGenerator interface {
	// Generate produces a structured Report from the knowledge source.
	//
	// Args:
	//   ctx - timeout and cancellation context.
	//   tenantID - tenant scope for the report (may be empty for global).
	//
	// Returns:
	//   *Report - the structured report.
	//   error - wrapped error if generation fails.
	Generate(ctx context.Context, tenantID string) (*Report, error)
}

// DefaultReportGenerator is the canonical ReportGenerator implementation.
// It pulls knowledge items from an injected KnowledgeSource and assembles
// them into a structured Report. All I/O is context-bound.
type DefaultReportGenerator struct {
	source KnowledgeSource
	config *ReportConfig
}

// NewReportGenerator creates a new DefaultReportGenerator.
//
// Args:
//
//	source - the knowledge source to read from (must not be nil).
//	config - report configuration (nil uses defaults).
//
// Returns:
//
//	*DefaultReportGenerator - the configured generator.
func NewReportGenerator(source KnowledgeSource, config *ReportConfig) (*DefaultReportGenerator, error) {
	if source == nil {
		return nil, fmt.Errorf("report generator: %w", ErrInvalidConfig)
	}
	if config == nil {
		config = DefaultReportConfig()
	}
	if config.TopN < 0 {
		return nil, fmt.Errorf("report generator: top_n must be >= 0: %w", ErrInvalidConfig)
	}
	if config.MinScore < 0 || config.MinScore > 1 {
		return nil, fmt.Errorf("report generator: min_score must be in [0,1]: %w", ErrInvalidConfig)
	}
	return &DefaultReportGenerator{
		source: source,
		config: config,
	}, nil
}

// Generate produces a structured Report from the knowledge source.
// It applies the configured filters, sorts items by score, and assembles
// all sections (summary, top knowledge, conflicts, trends, recommendations).
//
// Args:
//
//	ctx - timeout and cancellation context.
//	tenantID - tenant scope for the report (may be empty for global).
//
// Returns:
//
//	*Report - the structured report.
//	error - wrapped error if the source fails or context is cancelled.
func (g *DefaultReportGenerator) Generate(ctx context.Context, tenantID string) (*Report, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("report generate: %w", err)
	}

	items, err := g.source.ListKnowledge(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("report generate: list knowledge: %w", err)
	}

	filtered := g.applyFilters(items)
	sortByScoreDesc(filtered)

	summary := buildSummary(filtered)
	topKnowledge := buildTopKnowledge(filtered, g.config.TopN)
	conflicts := buildConflictSection(filtered, g.config.IncludeConflicts)
	trends := buildTrendSection(filtered, g.config.IncludeTrends)
	recommendations := RecommendationSection{}
	if g.config.IncludeRecommendations {
		recommendations = buildRecommendations(filtered)
	}

	return &Report{
		GeneratedAt:     time.Now(),
		TenantID:        tenantID,
		Summary:         summary,
		TopKnowledge:    topKnowledge,
		Conflicts:       conflicts,
		Trends:          trends,
		Recommendations: recommendations,
	}, nil
}

// applyFilters removes items below the configured minimum score.
// Returns a new slice; the input slice is not modified.
func (g *DefaultReportGenerator) applyFilters(items []KnowledgeItem) []KnowledgeItem {
	if g.config.MinScore <= 0 {
		out := make([]KnowledgeItem, len(items))
		copy(out, items)
		return out
	}
	out := make([]KnowledgeItem, 0, len(items))
	for _, item := range items {
		if item.Score >= g.config.MinScore {
			out = append(out, item)
		}
	}
	return out
}

// sortByScoreDesc sorts items by score descending, then by ID for stable ordering.
// The slice is sorted in place.
func sortByScoreDesc(items []KnowledgeItem) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		return items[i].ID < items[j].ID
	})
}

// buildSummary computes aggregate statistics over the item set.
func buildSummary(items []KnowledgeItem) SummarySection {
	byCategory := make(map[string]int)
	byConfidence := map[ConfidenceBucket]int{
		ConfidenceLow:    0,
		ConfidenceMedium: 0,
		ConfidenceHigh:   0,
	}

	var total float64
	var maxScore, minScore float64
	if len(items) > 0 {
		maxScore = items[0].Score
		minScore = items[0].Score
	}

	for _, item := range items {
		byCategory[item.Category]++
		byConfidence[bucketFor(item.Score)]++
		total += item.Score
		if item.Score > maxScore {
			maxScore = item.Score
		}
		if item.Score < minScore {
			minScore = item.Score
		}
	}

	avg := 0.0
	if len(items) > 0 {
		avg = total / float64(len(items))
	}

	return SummarySection{
		TotalItems:   len(items),
		ByCategory:   byCategory,
		ByConfidence: byConfidence,
		AverageScore: avg,
		MaxScore:     maxScore,
		MinScore:     minScore,
	}
}

// bucketFor maps a confidence score to a ConfidenceBucket.
func bucketFor(score float64) ConfidenceBucket {
	switch {
	case score < 0.4:
		return ConfidenceLow
	case score < 0.7:
		return ConfidenceMedium
	default:
		return ConfidenceHigh
	}
}

// buildTopKnowledge selects the top-N items after sorting.
func buildTopKnowledge(items []KnowledgeItem, limit int) TopKnowledgeSection {
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}
	top := make([]KnowledgeItem, limit)
	copy(top, items[:limit])
	return TopKnowledgeSection{
		Items: top,
		Limit: limit,
	}
}

// buildConflictSection aggregates conflict resolution statistics.
func buildConflictSection(items []KnowledgeItem, include bool) ConflictResolutionSection {
	section := ConflictResolutionSection{
		ByStrategy: make(map[string]int),
	}
	if !include {
		return section
	}

	var resolved []KnowledgeItem
	for _, item := range items {
		if !item.ConflictResolved {
			continue
		}
		section.TotalConflicts++
		strategy := item.ResolutionStrategy
		if strategy == "" {
			strategy = "unknown"
		}
		section.ByStrategy[strategy]++

		// Collect for recent resolutions (preserve original order).
		resolved = append(resolved, item)
	}

	// Most recent first by CreatedAt.
	sort.SliceStable(resolved, func(i, j int) bool {
		return resolved[i].CreatedAt.After(resolved[j].CreatedAt)
	})
	limit := len(resolved)
	if limit > 5 {
		limit = 5
	}
	section.RecentResolutions = resolved[:limit]
	return section
}

// buildTrendSection computes per-category evolution trends.
func buildTrendSection(items []KnowledgeItem, include bool) TrendSection {
	if !include {
		return TrendSection{
			Trends:      []EvolutionTrend{},
			GeneratedAt: time.Now(),
		}
	}

	type agg struct {
		count      int
		totalScore float64
		latest     time.Time
	}
	byCat := make(map[string]*agg)
	for _, item := range items {
		a, ok := byCat[item.Category]
		if !ok {
			a = &agg{}
			byCat[item.Category] = a
		}
		a.count++
		a.totalScore += item.Score
		if item.CreatedAt.After(a.latest) {
			a.latest = item.CreatedAt
		}
	}

	trends := make([]EvolutionTrend, 0, len(byCat))
	for cat, a := range byCat {
		avg := 0.0
		if a.count > 0 {
			avg = a.totalScore / float64(a.count)
		}
		trends = append(trends, EvolutionTrend{
			Category:        cat,
			Count:           a.count,
			AverageScore:    avg,
			LatestCreatedAt: a.latest,
		})
	}
	// Sort by count descending, then category for stable output.
	sort.SliceStable(trends, func(i, j int) bool {
		if trends[i].Count != trends[j].Count {
			return trends[i].Count > trends[j].Count
		}
		return trends[i].Category < trends[j].Category
	})

	return TrendSection{
		Trends:      trends,
		GeneratedAt: time.Now(),
	}
}

// buildRecommendations generates actionable recommendations from the knowledge corpus.
// Current rules:
//   - High-confidence items tied to a strategy -> "deploy" recommendation.
//   - Category with many low-confidence items -> "collect more evidence" recommendation.
//   - Conflict-heavy categories -> "review conflicts" recommendation.
func buildRecommendations(items []KnowledgeItem) RecommendationSection {
	deployRecs := buildDeployRecommendations(items)
	evidenceRecs := buildCollectEvidenceRecommendations(items)
	conflictRecs := buildConflictReviewRecommendations(items)

	recs := make([]Recommendation, 0, len(deployRecs)+len(evidenceRecs)+len(conflictRecs))
	recs = append(recs, deployRecs...)
	recs = append(recs, evidenceRecs...)
	recs = append(recs, conflictRecs...)

	priorityRank := map[string]int{string(ConfidenceHigh): 0, string(ConfidenceMedium): 1, string(ConfidenceLow): 2}
	sort.SliceStable(recs, func(i, j int) bool {
		ri, ok := priorityRank[recs[i].Priority]
		if !ok {
			ri = 99
		}
		rj, ok := priorityRank[recs[j].Priority]
		if !ok {
			rj = 99
		}
		if ri != rj {
			return ri < rj
		}
		return recs[i].ID < recs[j].ID
	})

	return RecommendationSection{
		Recommendations: recs,
		Total:           len(recs),
	}
}

// buildDeployRecommendations suggests deploying high-confidence knowledge to strategies.
func buildDeployRecommendations(items []KnowledgeItem) []Recommendation {
	var recs []Recommendation
	seen := make(map[string]bool)
	for _, item := range items {
		if item.Score < 0.7 || item.StrategyID == "" {
			continue
		}
		if seen[item.StrategyID] {
			continue
		}
		seen[item.StrategyID] = true
		recs = append(recs, Recommendation{
			ID:               fmt.Sprintf("deploy-%s", item.StrategyID),
			Title:            fmt.Sprintf("Deploy validated knowledge to strategy %s", item.StrategyID),
			Rationale:        fmt.Sprintf("Knowledge item %q has confidence %.2f; consider promoting it as the strategy's default behavior.", item.ID, item.Score),
			Priority:         string(ConfidenceHigh),
			TargetStrategyID: item.StrategyID,
			TargetTaskType:   item.TaskType,
			RelatedItemIDs:   []string{item.ID},
		})
	}
	return recs
}

// buildCollectEvidenceRecommendations flags categories dominated by low-confidence items.
func buildCollectEvidenceRecommendations(items []KnowledgeItem) []Recommendation {
	var recs []Recommendation
	type agg struct {
		total   int
		lowConf int
	}
	byCat := make(map[string]*agg)
	for _, item := range items {
		a, ok := byCat[item.Category]
		if !ok {
			a = &agg{}
			byCat[item.Category] = a
		}
		a.total++
		if item.Score < 0.4 {
			a.lowConf++
		}
	}
	for cat, a := range byCat {
		if a.total < 3 {
			continue
		}
		if float64(a.lowConf)/float64(a.total) < 0.5 {
			continue
		}
		recs = append(recs, Recommendation{
			ID:             fmt.Sprintf("collect-%s", cat),
			Title:          fmt.Sprintf("Collect more evidence for category %q", cat),
			Rationale:      fmt.Sprintf("%d of %d items in %q are low-confidence; gather more execution data before relying on them.", a.lowConf, a.total, cat),
			Priority:       "medium",
			TargetTaskType: cat,
		})
	}
	return recs
}

// buildConflictReviewRecommendations flags categories with many conflict resolutions.
func buildConflictReviewRecommendations(items []KnowledgeItem) []Recommendation {
	var recs []Recommendation
	conflictByCat := make(map[string]int)
	for _, item := range items {
		if item.ConflictResolved {
			conflictByCat[item.Category]++
		}
	}
	for cat, count := range conflictByCat {
		if count < 2 {
			continue
		}
		recs = append(recs, Recommendation{
			ID:             fmt.Sprintf("review-conflicts-%s", cat),
			Title:          fmt.Sprintf("Review conflict-heavy category %q", cat),
			Rationale:      fmt.Sprintf("%d conflict resolutions in %q; consider manual review or raising the conflict threshold.", count, cat),
			Priority:       "low",
			TargetTaskType: cat,
		})
	}
	return recs
}
