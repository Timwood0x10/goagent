// Package report provides human-readable evolution report generation.
package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	truncpkg "github.com/Timwood0x10/ares/internal/truncate"
)

// Format renders the report as human-readable markdown text.
// It is suitable for display in consoles, log files, or operator dashboards.
//
// Returns:
//
//	string - the formatted markdown report.
func (r *Report) Format() string {
	var sb strings.Builder
	r.writeHeader(&sb)
	r.writeSummary(&sb)
	r.writeTopKnowledge(&sb)
	r.writeConflicts(&sb)
	r.writeTrends(&sb)
	r.writeRecommendations(&sb)
	return sb.String()
}

// String implements fmt.Stringer and is equivalent to Format().
func (r *Report) String() string {
	return r.Format()
}

// writeHeader writes the report title and metadata.
func (r *Report) writeHeader(sb *strings.Builder) {
	sb.WriteString("# ARES Evolution Report\n\n")
	fmt.Fprintf(sb, "- Generated: %s\n", r.GeneratedAt.Format(time.RFC3339))
	if r.TenantID != "" {
		fmt.Fprintf(sb, "- Tenant: %s\n", r.TenantID)
	}
	sb.WriteString("\n")
}

// writeSummary writes the aggregate statistics section.
func (r *Report) writeSummary(sb *strings.Builder) {
	sb.WriteString("## Summary\n\n")
	fmt.Fprintf(sb, "- Total knowledge items: %d\n", r.Summary.TotalItems)
	fmt.Fprintf(sb, "- Average score: %.3f\n", r.Summary.AverageScore)
	fmt.Fprintf(sb, "- Score range: [%.3f, %.3f]\n\n", r.Summary.MinScore, r.Summary.MaxScore)

	sb.WriteString("### By Category\n\n")
	if len(r.Summary.ByCategory) == 0 {
		sb.WriteString("_No items_\n\n")
	} else {
		for _, cat := range sortedCategories(r.Summary.ByCategory) {
			fmt.Fprintf(sb, "- %s: %d\n", cat, r.Summary.ByCategory[cat])
		}
		sb.WriteString("\n")
	}

	sb.WriteString("### By Confidence\n\n")
	fmt.Fprintf(sb, "- High [0.7, 1.0]: %d\n", r.Summary.ByConfidence[ConfidenceHigh])
	fmt.Fprintf(sb, "- Medium [0.4, 0.7): %d\n", r.Summary.ByConfidence[ConfidenceMedium])
	fmt.Fprintf(sb, "- Low [0.0, 0.4): %d\n\n", r.Summary.ByConfidence[ConfidenceLow])
}

// writeTopKnowledge writes the top knowledge items section.
func (r *Report) writeTopKnowledge(sb *strings.Builder) {
	sb.WriteString("## Top Knowledge Items\n\n")
	if len(r.TopKnowledge.Items) == 0 {
		sb.WriteString("_No knowledge items available_\n\n")
		return
	}
	for i, item := range r.TopKnowledge.Items {
		fmt.Fprintf(sb, "### %d. [%s] %s\n", i+1, item.Category, truncate(item.Problem, 80))
		fmt.Fprintf(sb, "- ID: %s\n", item.ID)
		fmt.Fprintf(sb, "- Score: %.3f\n", item.Score)
		if item.StrategyID != "" {
			fmt.Fprintf(sb, "- Strategy: %s\n", item.StrategyID)
		}
		if item.TaskType != "" {
			fmt.Fprintf(sb, "- Task type: %s\n", item.TaskType)
		}
		fmt.Fprintf(sb, "- Solution: %s\n", truncate(item.Solution, 120))
		if item.ConflictResolved {
			fmt.Fprintf(sb, "- Conflict resolved via: %s\n", item.ResolutionStrategy)
		}
		sb.WriteString("\n")
	}
}

// writeConflicts writes the conflict resolution section.
func (r *Report) writeConflicts(sb *strings.Builder) {
	sb.WriteString("## Conflict Resolutions\n\n")
	fmt.Fprintf(sb, "- Total conflicts resolved: %d\n", r.Conflicts.TotalConflicts)
	if len(r.Conflicts.ByStrategy) > 0 {
		sb.WriteString("- By resolution strategy:\n")
		for _, strategy := range sortedStrategies(r.Conflicts.ByStrategy) {
			fmt.Fprintf(sb, "  - %s: %d\n", strategy, r.Conflicts.ByStrategy[strategy])
		}
	}
	sb.WriteString("\n")
	if len(r.Conflicts.RecentResolutions) > 0 {
		sb.WriteString("### Recent Resolutions\n\n")
		for _, item := range r.Conflicts.RecentResolutions {
			fmt.Fprintf(sb, "- [%s] %s -> %s (score: %.3f, via: %s)\n",
				item.Category,
				truncate(item.Problem, 50),
				truncate(item.Solution, 50),
				item.Score,
				item.ResolutionStrategy)
		}
		sb.WriteString("\n")
	}
}

// writeTrends writes the evolution trends section.
func (r *Report) writeTrends(sb *strings.Builder) {
	sb.WriteString("## Evolution Trends\n\n")
	fmt.Fprintf(sb, "_Computed at %s_\n\n", r.Trends.GeneratedAt.Format(time.RFC3339))
	if len(r.Trends.Trends) == 0 {
		sb.WriteString("_No trends available_\n\n")
		return
	}
	sb.WriteString("| Category | Count | Average Score | Latest Item |\n")
	sb.WriteString("|----------|-------|---------------|-------------|\n")
	for _, trend := range r.Trends.Trends {
		latest := "-"
		if !trend.LatestCreatedAt.IsZero() {
			latest = trend.LatestCreatedAt.Format(time.RFC3339)
		}
		fmt.Fprintf(sb, "| %s | %d | %.3f | %s |\n", trend.Category, trend.Count, trend.AverageScore, latest)
	}
	sb.WriteString("\n")
}

// writeRecommendations writes the actionable recommendations section.
func (r *Report) writeRecommendations(sb *strings.Builder) {
	sb.WriteString("## Recommendations\n\n")
	if len(r.Recommendations.Recommendations) == 0 {
		sb.WriteString("_No recommendations_\n\n")
		return
	}
	for i, rec := range r.Recommendations.Recommendations {
		fmt.Fprintf(sb, "### %d. [%s] %s\n", i+1, strings.ToUpper(rec.Priority), rec.Title)
		fmt.Fprintf(sb, "- Rationale: %s\n", rec.Rationale)
		if rec.TargetStrategyID != "" {
			fmt.Fprintf(sb, "- Target strategy: %s\n", rec.TargetStrategyID)
		}
		if rec.TargetTaskType != "" {
			fmt.Fprintf(sb, "- Target task type: %s\n", rec.TargetTaskType)
		}
		if len(rec.RelatedItemIDs) > 0 {
			fmt.Fprintf(sb, "- Related items: %s\n", strings.Join(rec.RelatedItemIDs, ", "))
		}
		sb.WriteString("\n")
	}
}

// truncate shortens a string to maxLen characters, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if maxLen <= 3 {
		return truncpkg.Plain(s, maxLen)
	}
	return truncpkg.WithEllipsis(s, maxLen-3)
}

// sortedCategories returns category keys sorted alphabetically.
func sortedCategories(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sortedStrategies returns strategy keys sorted alphabetically.
func sortedStrategies(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
