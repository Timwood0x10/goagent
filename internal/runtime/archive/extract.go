package archive

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Timwood0x10/ares/internal/ares_events"
)

// Compiled regexes used by the sub-extractors.
var (
	// reLintIssues matches "N issues" or "N issue" in linter output.
	reLintIssues = regexp.MustCompile(`(\d+)\s+issues?`)
	// reDiffStat matches git diff --stat summary lines like "+10 -3".
	reDiffStat = regexp.MustCompile(`\+(\d+) -(\d+)`)
)

// BuildRoundRecord is the extraction orchestrator. It validates inputs,
// protects caller-supplied identifiers, runs all sub-extractors over the
// event stream, and returns a validated RoundRecord ready for archival.
//
// The record's Refs field merges two identifier sources:
//  1. ProtectIdentifiers(refs) — caller-supplied, validated against role
//     patterns (P3 verbatim guarantee).
//  2. ExtractIdentifiersFromEvents(events) — identifiers scanned from tool
//     outputs and task/result text.
//
// Caller-supplied refs take precedence: an extracted identifier is only added
// when no caller-supplied value exists for the same role.
//
// Args:
//   - ctx: timeout/cancellation context. Cancelled ctx yields a wrapped
//     ctx.Err().
//   - round: 1-based round number. Must be > 0.
//   - action: one of "plan"|"implement"|"fix"|"review".
//   - events: the round's events to summarise. Must be non-empty.
//   - refs: caller-supplied identifier map (may be nil).
//
// Returns:
//   - *RoundRecord: validated record ready for RecordRound.
//   - error: wrapped ErrInvalidRound, ErrInvalidAction, ErrNoEvents,
//     ErrInvalidIdentifier, or ctx.Err() on failure.
func BuildRoundRecord(
	ctx context.Context,
	round int,
	action string,
	events []*ares_events.Event,
	refs map[string]string,
) (*RoundRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("build round record: context: %w", err)
	}
	if round <= 0 {
		return nil, fmt.Errorf("build round record: round %d: %w", round, ErrInvalidRound)
	}
	if !allowedActions[action] {
		return nil, fmt.Errorf("build round record: action %q: %w", action, ErrInvalidAction)
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("build round record: %w", ErrNoEvents)
	}

	protected, err := ProtectIdentifiers(refs)
	if err != nil {
		return nil, fmt.Errorf("build round record: %w", err)
	}

	verdict := extractVerdict(events)
	extracted := ExtractIdentifiersFromEvents(events)

	record := &RoundRecord{
		Round:     round,
		Action:    action,
		Summary:   extractSummary(events, verdict),
		Files:     extractFileChanges(events),
		Verdict:   verdict,
		TODOs:     extractTODOs(events),
		Decisions: extractDecisions(events),
		Refs:      mergeRefs(protected, extracted),
	}

	if err := record.Validate(); err != nil {
		return nil, fmt.Errorf("build round record: validate: %w", err)
	}
	return record, nil
}

// extractVerdict scans EventToolCallCompleted events and maps each tool's
// output to the appropriate Verdict field. Only the tool name determines
// which field is set — a "PASS" substring in a file path will NOT set
// GoTest, because the tool name must contain "test" for GoTest to be
// considered.
//
// Tool-name to verdict mapping:
//   - "code_runner" or contains "vet"  → GoVet (exit code 0 → "pass", else "fail")
//   - contains "lint" or "golangci"    → GoLint ("N issues" or "pass")
//   - contains "test" and not "vet"    → GoTest ("pass"|"fail"|"skip")
//   - output contains "DATA RACE"      → RaceDetector "fail"
//   - contains "example"               → Examples ("pass"|"fail")
//
// Returns an empty Verdict when no tool events are present.
func extractVerdict(events []*ares_events.Event) Verdict {
	var v Verdict
	for _, ev := range events {
		if ev == nil || ev.Type != ares_events.EventToolCallCompleted {
			continue
		}
		name := extractToolName(ev)
		output := extractToolOutput(ev)
		lowerName := strings.ToLower(name)

		// code_runner may carry any tool invocation (including `go vet`); its
		// exit code is attributed to GoVet per the documented contract and is
		// guarded by tests (TestExtractVerdict/go vet via code_runner). The
		// "vet" name check covers dedicated vet tools.
		if name == toolCodeRunner || strings.Contains(lowerName, "vet") {
			if code, ok := parseExitCode(output); ok {
				if code == 0 {
					v.GoVet = verdictPass
				} else {
					v.GoVet = verdictFail
				}
			}
		}
		if strings.Contains(lowerName, "lint") || strings.Contains(lowerName, "golangci") {
			v.GoLint = parseLintResult(output)
		}
		if strings.Contains(lowerName, "test") && !strings.Contains(lowerName, "vet") {
			v.GoTest = parseTestResult(output)
		}
		if strings.Contains(output, "DATA RACE") {
			v.RaceDetector = verdictFail
		} else if strings.Contains(lowerName, "race") {
			if strings.Contains(output, "PASS") || strings.Contains(output, "ok") {
				v.RaceDetector = verdictPass
			}
		}
		if strings.Contains(lowerName, "example") {
			v.Examples = parseExamplesResult(output)
		}
	}
	return v
}

// parseExitCode extracts the numeric exit code from a tool output, reusing
// the "exit code:" / "Exit code:" prefix pattern from
// internal/ares_memory/context/cleaner.go (summarizeCodeRunnerResult).
// Handles zero-padded values like "00" (parses to 0).
//
// Returns:
//   - int: the parsed exit code.
//   - bool: true when an exit code line was found and parsed.
func parseExitCode(output string) (int, bool) {
	for _, line := range strings.Split(output, "\n") {
		t := strings.TrimSpace(line)
		var numStr string
		switch {
		case strings.HasPrefix(t, "exit code:"):
			numStr = strings.TrimSpace(strings.TrimPrefix(t, "exit code:"))
		case strings.HasPrefix(t, "Exit code:"):
			numStr = strings.TrimSpace(strings.TrimPrefix(t, "Exit code:"))
		default:
			continue
		}
		if n, err := strconv.Atoi(numStr); err == nil {
			return n, true
		}
	}
	return 0, false
}

// parseLintResult extracts the issue count from linter output.
// "0 issues" or no issues found → "pass"; "N issues" (N>0) → "N issues".
func parseLintResult(output string) string {
	m := reLintIssues.FindStringSubmatch(output)
	if m == nil {
		return verdictPass
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n == 0 {
		return verdictPass
	}
	return fmt.Sprintf("%d issues", n)
}

// parseTestResult maps Go test output tokens to a verdict string.
// "FAIL" → "fail"; "no test files"/"--skip" → "skip"; "PASS"/"ok" → "pass".
// Returns "" when no recognised token is found.
func parseTestResult(output string) string {
	if strings.Contains(output, "FAIL") {
		return verdictFail
	}
	lower := strings.ToLower(output)
	if strings.Contains(lower, "no test files") || strings.Contains(lower, "--skip") {
		return verdictSkip
	}
	if strings.Contains(output, "PASS") || strings.Contains(output, "ok") {
		return verdictPass
	}
	return ""
}

// parseExamplesResult maps Go example output to a verdict string.
// "FAIL" → "fail"; "PASS"/"ok" → "pass"; otherwise "".
func parseExamplesResult(output string) string {
	if strings.Contains(output, "FAIL") {
		return verdictFail
	}
	if strings.Contains(output, "PASS") || strings.Contains(output, "ok") {
		return verdictPass
	}
	return ""
}

// extractFileChanges scans EventToolCallCompleted events for file_tools
// invocations and builds a FileChange entry per touched file. When the tool
// output looks like a git diff stat ("+N -M"), the additions are summed into
// LinesAdded.
//
// Returns nil when no file tool events are present.
func extractFileChanges(events []*ares_events.Event) []FileChange {
	var changes []FileChange
	for _, ev := range events {
		if ev == nil || ev.Type != ares_events.EventToolCallCompleted {
			continue
		}
		name := extractToolName(ev)
		if name != toolFileTools && !strings.HasPrefix(name, "file_") {
			continue
		}
		path := extractFilePath(ev)
		if path == "" {
			continue
		}
		output := extractToolOutput(ev)
		op := extractOperation(ev)
		changes = append(changes, FileChange{
			Path:       path,
			LinesAdded: parseDiffAdditions(output),
			Summary:    summarizeFileChange(path, op),
		})
	}
	return changes
}

// extractFilePath resolves the file path from a file_tools event payload,
// checking (in order) the "args" map, the "path" key, and the "input" JSON.
func extractFilePath(ev *ares_events.Event) string {
	args := extractToolArgs(ev)
	if args != nil {
		if p, ok := args["path"].(string); ok && p != "" {
			return p
		}
	}
	if p, ok := ev.Payload["path"].(string); ok && p != "" {
		return p
	}
	return ""
}

// extractOperation resolves the operation from a file_tools event payload.
func extractOperation(ev *ares_events.Event) string {
	args := extractToolArgs(ev)
	if args != nil {
		if op, ok := args["operation"].(string); ok {
			return op
		}
	}
	if op, ok := ev.Payload["operation"].(string); ok {
		return op
	}
	return ""
}

// extractToolArgs parses tool arguments from the event payload. Handles both
// the "args" map shape and the "input" JSON string shape.
func extractToolArgs(ev *ares_events.Event) map[string]any {
	if ev == nil || ev.Payload == nil {
		return nil
	}
	if args, ok := ev.Payload["args"].(map[string]any); ok {
		return args
	}
	// Shape 2: args as a JSON string (B17 — agentloop emitter may stringify).
	if argsStr, ok := ev.Payload["args"].(string); ok && argsStr != "" {
		var args map[string]any
		if err := json.Unmarshal([]byte(argsStr), &args); err == nil {
			return args
		}
	}
	// Shape 3: input as a JSON string.
	if input, ok := ev.Payload["input"].(string); ok && input != "" {
		var args map[string]any
		if err := json.Unmarshal([]byte(input), &args); err == nil {
			return args
		}
	}
	return nil
}

// parseDiffAdditions sums the "+" counts from git diff stat lines like
// "+10 -3". Returns 0 when no diff stat is found.
func parseDiffAdditions(output string) int {
	if output == "" {
		return 0
	}
	total := 0
	for _, m := range reDiffStat.FindAllStringSubmatch(output, -1) {
		if len(m) >= 2 {
			if n, err := strconv.Atoi(m[1]); err == nil {
				total += n
			}
		}
	}
	return total
}

// summarizeFileChange builds a one-line description of a file operation.
func summarizeFileChange(path, op string) string {
	switch op {
	case opWrite:
		return fmt.Sprintf("Wrote %s", path)
	case "read":
		return fmt.Sprintf("Read %s", path)
	case "list":
		return fmt.Sprintf("Listed %s", path)
	default:
		if op != "" {
			return fmt.Sprintf("%s %s", op, path)
		}
		return path
	}
}

// extractDecisions scans EventLLMCall and EventMessageAdded payloads for P0
// architecture-decision sentences. The heuristic matches lines containing
// "decide", "chose", "will use", "architecture", or "adopt"
// (case-insensitive). Results are deduplicated, trimmed, capped at ~200 chars
// each and ~10 entries total.
func extractDecisions(events []*ares_events.Event) []string {
	var decisions []string
	seen := make(map[string]bool)
	for _, ev := range events {
		if ev == nil {
			continue
		}
		if ev.Type != ares_events.EventLLMCall && ev.Type != ares_events.EventMessageAdded {
			continue
		}
		text := extractPayloadText(ev)
		if text == "" {
			continue
		}
		for _, line := range strings.Split(text, "\n") {
			lower := strings.ToLower(line)
			if !containsAnySubstr(lower, "decide", "chose", "will use", "architecture", "adopt") {
				continue
			}
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || seen[trimmed] {
				continue
			}
			trimmed = truncateString(trimmed, 200)
			seen[trimmed] = true
			decisions = append(decisions, trimmed)
			if len(decisions) >= 10 {
				return decisions
			}
		}
	}
	return decisions
}

// extractSummary builds a one-line summary from the task payload
// (EventKeyTask) and the supplied verdict. The verdict is passed in (rather
// than recomputed) because BuildRoundRecord already computes it for the
// record's Verdict field. Capped at ~160 chars.
func extractSummary(events []*ares_events.Event, verdict Verdict) string {
	var task string
	for _, ev := range events {
		if ev == nil {
			continue
		}
		if t, ok := ev.Payload[ares_events.EventKeyTask].(string); ok && t != "" {
			task = t
			break
		}
	}
	var parts []string
	if task != "" {
		parts = append(parts, task)
	}
	vParts := formatVerdictParts(verdict)
	if len(vParts) > 0 {
		parts = append(parts, "verdict: "+strings.Join(vParts, ", "))
	}
	summary := strings.Join(parts, " | ")
	return truncateString(summary, 160)
}

// formatVerdictParts converts non-empty verdict fields into "key=value" pairs.
func formatVerdictParts(v Verdict) []string {
	var parts []string
	if v.GoVet != "" {
		parts = append(parts, "vet="+v.GoVet)
	}
	if v.GoLint != "" {
		parts = append(parts, "lint="+v.GoLint)
	}
	if v.GoTest != "" {
		parts = append(parts, "test="+v.GoTest)
	}
	if v.RaceDetector != "" {
		parts = append(parts, "race="+v.RaceDetector)
	}
	if v.Examples != "" {
		parts = append(parts, "examples="+v.Examples)
	}
	return parts
}

// extractTODOs scans all events for P5 notes: lines containing "TODO",
// "FIXME", "roll back", or "rollback". Results are deduplicated, trimmed,
// capped at ~200 chars each and ~10 entries total.
func extractTODOs(events []*ares_events.Event) []string {
	var todos []string
	seen := make(map[string]bool)
	for _, ev := range events {
		if ev == nil {
			continue
		}
		text := extractToolOutput(ev)
		if text == "" {
			text = extractPayloadText(ev)
		}
		if text == "" {
			continue
		}
		for _, line := range strings.Split(text, "\n") {
			lower := strings.ToLower(line)
			if !containsAnySubstr(lower, "todo", "fixme", "roll back", "rollback") {
				continue
			}
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || seen[trimmed] {
				continue
			}
			trimmed = truncateString(trimmed, 200)
			seen[trimmed] = true
			todos = append(todos, trimmed)
			if len(todos) >= 10 {
				return todos
			}
		}
	}
	return todos
}

// extractToolOutput retrieves the output text from a tool-call event payload.
// Handles both payload shapes: "output" (callback_bridge) and "result"
// (EventKeyResult). Also checks "error" as a fallback. Returns "" when none
// are present.
func extractToolOutput(ev *ares_events.Event) string {
	if ev == nil || ev.Payload == nil {
		return ""
	}
	if s, ok := ev.Payload["output"].(string); ok && s != "" {
		return s
	}
	if s, ok := ev.Payload[ares_events.EventKeyResult].(string); ok && s != "" {
		return s
	}
	if s, ok := ev.Payload["error"].(string); ok && s != "" {
		return s
	}
	return ""
}

// extractToolName retrieves the tool name from a tool-call event payload.
// Checks "tool_name", "tool", and "function" keys (in that order). Returns
// "" when none are present.
func extractToolName(ev *ares_events.Event) string {
	if ev == nil || ev.Payload == nil {
		return ""
	}
	if s, ok := ev.Payload["tool_name"].(string); ok {
		return s
	}
	if s, ok := ev.Payload["tool"].(string); ok {
		return s
	}
	if s, ok := ev.Payload["function"].(string); ok {
		return s
	}
	return ""
}

// ExtractIdentifiersFromEvents concatenates all tool outputs and task/result
// texts from the event stream, then scans the combined text with
// ExtractIdentifiers. This captures identifiers that appear in tool output
// but were not explicitly supplied by the caller.
//
// Returns a non-nil map with all six roles populated (possibly empty).
func ExtractIdentifiersFromEvents(events []*ares_events.Event) map[string][]string {
	var sb strings.Builder
	for _, ev := range events {
		if ev == nil {
			continue
		}
		if ev.Type == ares_events.EventToolCallCompleted {
			sb.WriteString(extractToolOutput(ev))
			sb.WriteString("\n")
		}
		if t, ok := ev.Payload[ares_events.EventKeyTask].(string); ok && t != "" {
			sb.WriteString(t)
			sb.WriteString("\n")
		}
		if r, ok := ev.Payload[ares_events.EventKeyResult].(string); ok && r != "" {
			sb.WriteString(r)
			sb.WriteString("\n")
		}
	}
	return ExtractIdentifiers(sb.String())
}

// mergeRefs combines caller-supplied (protected) identifiers with extracted
// identifiers. Protected values take precedence: an extracted identifier is
// only added when no protected value exists for the same role. Extracted
// values within a role are joined with ", ".
//
// Returns a non-nil map.
func mergeRefs(protected map[string]string, extracted map[string][]string) map[string]string {
	merged := make(map[string]string)
	for k, v := range protected {
		merged[k] = v
	}
	for role, vals := range extracted {
		if len(vals) == 0 {
			continue
		}
		if _, exists := merged[role]; !exists {
			merged[role] = strings.Join(vals, ", ")
		}
	}
	return merged
}

// extractPayloadText gathers all text fields from an event payload for
// scanning. Checks "content", "input", "prompt", EventKeyResult, and
// EventKeyTask keys.
func extractPayloadText(ev *ares_events.Event) string {
	if ev == nil || ev.Payload == nil {
		return ""
	}
	var parts []string
	for _, key := range []string{keyContent, "input", "prompt", ares_events.EventKeyResult, ares_events.EventKeyTask} {
		if s, ok := ev.Payload[key].(string); ok && s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}

// containsAnySubstr reports whether s contains any of the given substrings.
func containsAnySubstr(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// truncateString caps s at maxLen runes, appending "..." when truncation
// occurs. When maxLen <= 3, returns "..." for any string exceeding maxLen.
func truncateString(s string, maxLen int) string {
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return "..."
	}
	runes := []rune(s)
	return string(runes[:maxLen-3]) + "..."
}
