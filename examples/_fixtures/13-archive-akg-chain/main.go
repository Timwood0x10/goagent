// Example 13 — Raw Conversation → AKG Knowledge Pipeline.
//
// Purpose:
//
//	Read original conversation records from .workbuddy/memory/, feed the
//	full content through the AKG (Agent Knowledge Graph) pipeline
//	(normalize → match → validate → summarize), and output entity-matched
//	and summarized knowledge objects with cross-record analysis.
//
// Learning objectives:
//   - Read and parse markdown conversation records from disk.
//   - Build a KnowledgePipeline with Normalizers, EntityMatchers,
//     Validators, and Summarizers.
//   - Process raw KnowledgeObjects through the pipeline stages.
//   - Analyze compression ratios and keyword mentions across records.
//
// Core APIs used:
//   - github.com/Timwood0x10/ares/internal/knowledge.NewKnowledgePipeline
//   - github.com/Timwood0x10/ares/internal/knowledge.KnowledgeObject
//   - github.com/Timwood0x10/ares/internal/knowledge.KnowledgePipeline.Process
//   - github.com/Timwood0x10/ares/internal/knowledge/pipeline.DefaultNormalizer
//   - github.com/Timwood0x10/ares/internal/knowledge/pipeline.DefaultEntityMatcher
//   - github.com/Timwood0x10/ares/internal/knowledge/pipeline.DefaultValidator
//   - github.com/Timwood0x10/ares/internal/knowledge/pipeline.DefaultSummarizer
//
// Run:
//
//	go run ./examples/13-archive-akg-chain
//
// Expected output:
//
//	read <N> conversation records
//
//	=================================================================
//	  AKG pipeline — normalized + summarized from raw conversation
//	=================================================================
//
//	■ <date>
//	  raw: <truncated content>
//	  normalized: <normalized text>
//	  AKG summary: <summarized text>
//	  confidence: 0.<XX>
//	...
//
//	=================================================================
//	  cross-record analysis
//	=================================================================
//	  total raw chars: <N>
//	  total AKG summary chars: <N>
//	  compression ratio: <X.X>%
//	  mentions of "DAG": <N> rounds
//	...
//	=================================================================
//
// Try modifying:
//   - MaxRawBytes in DefaultNormalizer to allow larger inputs.
//   - MatchThreshold in DefaultEntityMatcher to tune entity matching strictness.
//   - MaxSummaryLen in DefaultSummarizer to produce longer or shorter summaries.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/pipeline"
)

func main() {
	ctx := context.Background()

	// ── Step 1: Read original conversation records ──
	// Scan .workbuddy/memory/ for .md files (excluding MEMORY.md index).
	// Each file's first line "# <date>" becomes the record ID; the full
	// content is truncated to ~60 lines to avoid overloading the demo.
	home, _ := os.UserHomeDir()
	memDir := home + "/go/src/goagent/.workbuddy/memory"
	entries, err := os.ReadDir(memDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot read %s: %v\n", memDir, err)
		os.Exit(1)
	}

	type rawRecord struct {
		date    string
		content string
	}
	var records []rawRecord
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".md") || e.Name() == "MEMORY.md" {
			continue // skip non-markdown and the MEMORY.md index file
		}
		data, _ := os.ReadFile(filepath.Join(memDir, e.Name()))
		lines := strings.Split(string(data), "\n")
		date := strings.TrimPrefix(lines[0], "# ") // first line is "# <date>"
		// Take first ~60 lines of content (avoid overloading a demo)
		content := strings.Join(lines, "\n")
		contentLines := strings.Split(content, "\n")
		if len(contentLines) > 60 {
			content = strings.Join(contentLines[:60], "\n") + "\n..."
		}
		records = append(records, rawRecord{date: date, content: content})
	}
	// Sort by date so records are processed chronologically.
	sort.Slice(records, func(i, j int) bool { return records[i].date < records[j].date })
	fmt.Printf("read %d conversation records\n\n", len(records))

	// ── Step 2: Build the AKG pipeline ──
	// NewKnowledgePipeline wires four stage slices:
	//   - Normalizers: clean and truncate raw bytes (MaxRawBytes).
	//   - EntityMatchers: match entities with a similarity threshold.
	//   - Validators: validate the normalized output.
	//   - Summarizers: produce a concise summary (MaxSummaryLen).
	pipe := knowledge.NewKnowledgePipeline(
		[]knowledge.Normalizer{&pipeline.DefaultNormalizer{MaxRawBytes: 20480}},
		[]knowledge.EntityMatcher{&pipeline.DefaultEntityMatcher{MatchThreshold: 0.5}},
		[]knowledge.Validator{&pipeline.DefaultValidator{}},
		[]knowledge.Summarizer{&pipeline.DefaultSummarizer{MaxSummaryLen: 300}},
	)

	// ── Step 3: Process each record through the pipeline ──
	// For each raw record, build a KnowledgeObject with the record's date
	// as ID, then call Process which runs normalize → match → validate →
	// summarize and returns a processed KnowledgeObject.
	var results []*knowledge.KnowledgeObject
	for i, rec := range records {
		obj := &knowledge.KnowledgeObject{
			ID:   rec.date,
			Raw:  []byte(rec.content),
			Tags: []string{"conversation"},
		}
		processed, err := pipe.Process(ctx, obj) // full pipeline execution
		if err != nil {
			fmt.Printf("  %s: pipeline error: %v\n", rec.date, err)
			continue
		}
		results = append(results, processed)
		_ = i
	}

	// ── Step 4: Show per-record pipeline results ──
	// For each processed KnowledgeObject, print the truncated raw content,
	// normalized text, AKG summary, and confidence score.
	fmt.Println(strings.Repeat("=", 65))
	fmt.Println("  AKG pipeline — normalized + summarized from raw conversation")
	fmt.Println(strings.Repeat("=", 65))

	for _, ko := range results {
		fmt.Printf("\n■ %s\n", ko.ID)

		r := ko.Raw
		if len(r) > 300 {
			r = r[:300]
		}
		fmt.Printf("  raw: %s\n", truncate(string(r), 150))

		if ko.Normalized != "" {
			fmt.Printf("  normalized: %s\n", truncate(ko.Normalized, 150))
		}
		if ko.Summary != "" && ko.Summary != string(ko.Raw) {
			fmt.Printf("  AKG summary: %s\n", truncate(ko.Summary, 250))
		}
		if ko.Confidence > 0 {
			fmt.Printf("  confidence: %.2f\n", ko.Confidence)
		}
	}

	// ── Step 5: Cross-record analysis ──
	// Aggregate total raw chars vs total AKG summary chars to compute a
	// compression ratio, then count keyword mentions across all summaries.
	fmt.Printf("\n%s\n", strings.Repeat("=", 65))
	fmt.Println("  cross-record analysis")
	fmt.Println(strings.Repeat("=", 65))
	totalChars := 0
	totalAKG := 0
	for _, ko := range results {
		totalChars += len(ko.Raw)
		totalAKG += len(ko.Summary)
	}
	fmt.Printf("  total raw chars: %d\n", totalChars)
	fmt.Printf("  total AKG summary chars: %d\n", totalAKG)
	if totalChars > 0 {
		fmt.Printf("  compression ratio: %.1f%%\n", 100.0-float64(totalAKG)/float64(totalChars)*100.0)
	}
	fmt.Println()
	// Count keyword/focus area mentions across all AKG summaries.
	wordCount := make(map[string]int)
	for _, ko := range results {
		for _, w := range []string{"review", "DAG", "AKG", "bug", "test", "evolution", "archive", "distill"} {
			if strings.Contains(ko.Summary, w) {
				wordCount[w]++
			}
		}
	}
	for _, w := range []string{"DAG", "review", "test", "evolution", "archive", "distill", "AKG", "bug"} {
		fmt.Printf("  mentions of %q: %d rounds\n", w, wordCount[w])
	}
	fmt.Println(strings.Repeat("=", 65))
}

// truncate shortens s to max runes, appending "..." if truncated.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "..."
	}
	return s
}
