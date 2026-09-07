package evaluation

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ToMarkdown formats the report as a Markdown table.
func (r *Report) ToMarkdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Evaluation Report: %s\n\n", r.Name)
	fmt.Fprintf(&b, "**Date:** %s\n\n", r.Date)
	b.WriteString("| Metric | Value |\n|---|---|\n")
	fmt.Fprintf(&b, "| Runs | %d |\n", r.Runs)
	fmt.Fprintf(&b, "| Passed | %d |\n", r.Passed)
	fmt.Fprintf(&b, "| Failed | %d |\n", r.Failed)
	fmt.Fprintf(&b, "| Pass Rate | %.1f%% |\n", r.PassRate)
	fmt.Fprintf(&b, "| Avg Score | %.3f |\n", r.AvgScore)
	fmt.Fprintf(&b, "| Avg Latency | %v |\n", r.AvgLatency)
	fmt.Fprintf(&b, "| Max Latency | %v |\n", r.MaxLatency)
	fmt.Fprintf(&b, "| Total Tokens | %d |\n\n", r.TotalTokens)

	if len(r.Results) > 0 {
		b.WriteString("### Results\n\n")
		b.WriteString("| Task | Success | Score | Latency | Tokens | Tools |\n|---|---|---|---|---|---|\n")
		for _, m := range r.Results {
			status := "❌"
			if m.Success {
				status = "✅"
			}
			fmt.Fprintf(&b, "| %s | %s | %.2f | %v | %d | %d |\n",
				m.Task, status, m.Score, m.Latency, m.TokenCount, m.ToolCalls)
		}
	}
	return b.String()
}

// ToJSON formats the report as indented JSON.
func (r *Report) ToJSON() (string, error) {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal report: %w", err)
	}
	return string(data), nil
}
