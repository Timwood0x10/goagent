package context

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Timwood0x10/ares/api/core"
	truncpkg "github.com/Timwood0x10/ares/internal/truncate"
)

// CleanerStats, CleaningMode, CleanOptions are now defined in api/core.
// Re-export for backward compatibility within the package.
type (
	CleanerStats = core.CleanerStats
	CleaningMode = core.CleaningMode
	CleanOptions = core.CleanOptions
)

const (
	CleaningModeDefault      = core.CleaningModeDefault
	CleaningModeConservative = core.CleaningModeConservative
	CleaningModeAggressive   = core.CleaningModeAggressive
)

// DefaultCleanOptions delegates to the canonical definition in api/core.
func DefaultCleanOptions() CleanOptions { return core.DefaultCleanOptions() }

// ContextCleaner intelligently cleans conversation context before LLM calls.
// It applies differential compression based on message role:
//   - tool_call / tool_result → aggressively compressed to first sentence
//   - assistant with ToolCalls → treated as tool-like content
//   - pure assistant reasoning → code blocks compressed, content truncated
//   - user / system → straightforward truncation
//
// Statistics on tool calls and bytes saved are tracked internally.
type ContextCleaner struct {
	mu          sync.Mutex
	stats       CleanerStats
	codePattern *regexp.Regexp
}

// NewContextCleaner creates a new ContextCleaner.
func NewContextCleaner() *ContextCleaner {
	return &ContextCleaner{
		codePattern: regexp.MustCompile("```[^`]*```"),
	}
}

// Clean processes a message slice, applying role-aware truncation.
// Returns a new slice with compressed content; original slice is not modified.
func (c *ContextCleaner) Clean(messages []Message, opts ...CleanOptions) []Message {
	options := DefaultCleanOptions()
	if len(opts) > 0 {
		options = opts[0]
	}

	if len(messages) == 0 {
		return messages
	}

	result := make([]Message, len(messages))
	var saved int64
	var toolCount, llmCount int64

	for i, msg := range messages {
		// Preserve all fields including tool call metadata and turn linkage.
		result[i] = Message{
			Role:       msg.Role,
			Content:    msg.Content,
			Time:       msg.Time,
			TurnID:     msg.TurnID,
			ToolCallID: msg.ToolCallID,
			ToolCalls:  msg.ToolCalls,
		}
		origLen := len(msg.Content)

		switch msg.Role {
		case RoleUser:
			result[i].Content = truncpkg.WithEllipsis(msg.Content, options.MaxUserLen)

		case RoleAssistant:
			if len(msg.ToolCalls) > 0 {
				// Assistant message that invokes tools → aggressive strip.
				toolCount++
				result[i].Content = extractGist(msg.Content, options.MaxToolLen)
			} else {
				// Pure reasoning → keep more context.
				llmCount++
				cleaned := c.compressCodeBlocks(msg.Content)
				result[i].Content = truncpkg.WithEllipsis(cleaned, options.MaxAssistantLen)
			}

		case RoleSystem:
			result[i].Content = truncpkg.WithEllipsis(msg.Content, options.MaxSystemLen)

		case RoleToolCall, RoleToolResult:
			toolCount++
			result[i].Content = extractGist(msg.Content, options.MaxToolLen)

		default:
			result[i].Content = truncpkg.WithEllipsis(msg.Content, options.MaxAssistantLen)
		}

		saved += int64(origLen - len(result[i].Content))
	}

	c.mu.Lock()
	c.stats.BytesSaved += saved
	c.stats.HistoryIn += len(messages)
	c.stats.HistoryOut += len(result)
	c.stats.ToolCalls += toolCount
	c.stats.LLMCalls += llmCount
	c.mu.Unlock()

	return result
}

// compressCodeBlocks replaces long code blocks with a short summary.
func (c *ContextCleaner) compressCodeBlocks(content string) string {
	return c.codePattern.ReplaceAllStringFunc(content, func(match string) string {
		lines := strings.SplitN(match, "\n", 3)
		langHint := ""
		if len(lines) > 0 {
			langHint = strings.TrimPrefix(lines[0], "```")
			if langHint != "" {
				langHint = " [" + langHint + "]"
			}
		}
		return "<code block" + langHint + ">"
	})
}

// extractGist extracts the first meaningful sentence from content.
// The sentence-ending delimiter (. ! ?) is included in the result.
func extractGist(content string, maxLen int) string {
	noCode := codeBlockRE.ReplaceAllString(content, "")
	trimmed := strings.TrimSpace(noCode)
	if trimmed == "" {
		trimmed = strings.TrimSpace(content)
	}

	// Find first sentence boundary including the delimiter.
	loc := sentenceRE.FindStringIndex(trimmed)
	if loc != nil {
		gist := strings.TrimSpace(trimmed[:loc[1]])
		if gist != "" && utf8.RuneCountInString(gist) <= maxLen {
			return gist
		}
	}

	return truncpkg.WithEllipsis(trimmed, maxLen)
}

// CleanWithTurns performs turn-aware context cleaning.
// Messages are grouped into turns by turn_id (primary), user-message boundary
// (fallback), or structural linkage via tool_call_id (last resort).
// For completed turns (all but the last), tool call/result messages are
// dropped or summarized. For the active (last) turn, all tool-call
// protocol fields are preserved as-is so the provider can continue.
func (c *ContextCleaner) CleanWithTurns(messages []Message, opts ...CleanOptions) []Message {
	options := DefaultCleanOptions()
	if len(opts) > 0 {
		options = opts[0]
	}

	if len(messages) == 0 {
		return messages
	}

	// Group into turns using turn_id first, then structural fallback.
	turns := groupIntoTurns(messages)

	// Apply MaxSummarizedTurns limit from the end.
	if options.MaxSummarizedTurns > 0 && len(turns) > options.MaxSummarizedTurns {
		turns = turns[len(turns)-options.MaxSummarizedTurns:]
	}

	// Process each turn.
	var result []Message
	c.mu.Lock()
	c.stats.TurnsProcessed += int64(len(turns))
	c.mu.Unlock()

	for turnIdx, turn := range turns {
		isActive := turnIdx == len(turns)-1

		if isActive {
			// Active turn: preserve all protocol fields as-is so the provider
			// can continue the tool loop. Only trim user/assistant text content.
			for _, msg := range turn {
				// Tool protocol messages: preserve content verbatim.
				if msg.Role == RoleToolCall || msg.Role == RoleToolResult ||
					(msg.Role == RoleAssistant && len(msg.ToolCalls) > 0) {
					result = append(result, msg)
					c.mu.Lock()
					c.stats.ActivePreservedMessages++
					c.mu.Unlock()
					continue
				}
				// User/pure-assistant/system: apply normal cleaning.
				cleaned := c.Clean([]Message{msg}, options)
				if len(cleaned) > 0 {
					result = append(result, cleaned[0])
					c.mu.Lock()
					c.stats.ActivePreservedMessages++
					c.mu.Unlock()
				}
			}
		} else {
			// Completed turn: summarize tool results, apply policy for tool calls.
			toolCallMap := buildToolCallMap(turn)

			for _, msg := range turn {
				switch msg.Role {
				case RoleToolResult:
					// Tool result: type-specific summarization (upgrade from text-level).
					tcInfo, hasMatch := toolCallMap[msg.ToolCallID]
					var summary string
					if hasMatch {
						summary = SummarizeToolResultWithCall(msg, tcInfo.name, tcInfo.args)
					} else {
						summary = extractGist(msg.Content, options.MaxToolLen)
					}
					saved := int64(len(msg.Content) - len(summary))
					summarizedMsg := msg
					summarizedMsg.Content = summary
					result = append(result, summarizedMsg)
					c.mu.Lock()
					c.stats.BytesSaved += saved
					c.stats.SummarizedToolMessages++
					c.mu.Unlock()

				case RoleToolCall:
					// Always keep tool call causality, compress the arguments.
					// The event kind, tool name, and tool_call_id are preserved
					// so the causal chain (intent → action → observation → decision)
					// remains intact for prompt building and distillation.
					tcSummary := summarizeToolCall(msg)
					saved := int64(len(msg.Content) - len(tcSummary))
					summarizedMsg := msg
					summarizedMsg.Content = tcSummary
					result = append(result, summarizedMsg)
					c.mu.Lock()
					c.stats.BytesSaved += saved
					c.stats.SummarizedToolMessages++
					c.mu.Unlock()

				default:
					// Keep user, assistant, system messages with normal cleaning.
					cleaned := c.Clean([]Message{msg}, options)
					if len(cleaned) > 0 {
						result = append(result, cleaned[0])
					}
				}
			}
		}
	}

	return result
}

// SummarizeToolResult generates a compact summary of a tool result message.
// Uses text-level gist extraction as a fallback when tool call info is unavailable.
func SummarizeToolResult(msg Message) string {
	if msg.Role != RoleToolResult || len(msg.Content) == 0 {
		return msg.Content
	}
	summary := extractGist(msg.Content, 200)
	if summary != "" {
		return summary
	}
	return truncpkg.WithEllipsis(msg.Content, 200)
}

// SummarizeToolResultWithCall generates a type-aware tool result summary.
// It parses the result content based on tool type (file_tools, code_runner, etc.)
// and returns a compact structured summary instead of raw output.
func SummarizeToolResultWithCall(msg Message, toolName string, argumentsJSON string) string {
	if msg.Role != RoleToolResult || msg.Content == "" {
		return msg.Content
	}
	args := parseToolArgs(argumentsJSON)

	switch {
	case toolName == "file_tools":
		return summarizeFileToolResult(msg, args)
	case toolName == "code_runner":
		return summarizeCodeRunnerResult(msg, args)
	case toolName == "http_request":
		return summarizeHTTPRequestResult(msg, args)
	case toolName == "web_scraper":
		return summarizeWebScraperResult(msg, args)
	case toolName == "knowledge_search" || toolName == "memory_search" || toolName == "distilled_memory_search":
		return summarizeSearchResult(msg, args)
	case strings.HasPrefix(toolName, "file_"):
		return summarizeFileToolResult(msg, args)
	default:
		return SummarizeToolResult(msg)
	}
}

// summarizeFileToolResult extracts compact info from file_tool results.
func summarizeFileToolResult(msg Message, args map[string]interface{}) string {
	op, _ := args["operation"].(string)
	path, _ := args["path"].(string)
	content := msg.Content

	if strings.HasPrefix(content, "Error:") || strings.HasPrefix(content, "error:") {
		return fmt.Sprintf("File %s (%s): %s", path, op, truncpkg.WithEllipsis(content, 150))
	}
	switch op {
	case "read":
		lines := strings.SplitN(content, "\n", 3)
		preview := ""
		if len(lines) > 0 {
			preview = strings.TrimSpace(lines[0])
		}
		if preview == "" && len(lines) > 1 {
			preview = strings.TrimSpace(lines[1])
		}
		if utf8.RuneCountInString(preview) > 100 {
			preview = truncpkg.WithEllipsis(preview, 100)
		}
		if preview != "" {
			return fmt.Sprintf("Read %s: %s", path, preview)
		}
		return fmt.Sprintf("Read %s (%d bytes)", path, len(content))
	case "write":
		return fmt.Sprintf("Wrote %s (%d bytes)", path, len(content))
	case "list":
		return fmt.Sprintf("Listed %s (%d entries)", path, len(strings.Split(content, "\n")))
	default:
		return SummarizeToolResult(msg)
	}
}

// summarizeCodeRunnerResult extracts compact info from code_runner results.
func summarizeCodeRunnerResult(msg Message, args map[string]interface{}) string {
	command, _ := args["command"].(string)
	lang, _ := args["language"].(string)
	content := msg.Content

	lines := strings.Split(content, "\n")
	exitCode := "?"
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "exit code:") || strings.HasPrefix(t, "Exit code:") {
			exitCode = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(t, "exit code:"), "Exit code:"))
		}
	}
	var preview string
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t != "" && !strings.HasPrefix(t, "exit code:") && !strings.HasPrefix(t, "Exit code:") {
			preview = truncpkg.WithEllipsis(t, 100)
			break
		}
	}
	cmdSummary := command
	if lang != "" {
		cmdSummary = fmt.Sprintf("%s (%s)", command, lang)
	}
	if preview != "" {
		return fmt.Sprintf("Exec `%s` exit=%s: %s", cmdSummary, exitCode, preview)
	}
	return fmt.Sprintf("Exec `%s` exit=%s (%d bytes)", cmdSummary, exitCode, len(content))
}

// summarizeHTTPRequestResult extracts compact info from http_request results.
func summarizeHTTPRequestResult(msg Message, args map[string]interface{}) string {
	url, _ := args["url"].(string)
	if url == "" {
		url, _ = args["URL"].(string)
	}
	method, _ := args["method"].(string)
	if method == "" {
		method = "GET"
	}
	content := msg.Content

	statusCode := ""
	lines := strings.SplitN(content, "\n", 5)
	for _, line := range lines {
		if strings.Contains(line, "Status:") || strings.Contains(line, "status:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				statusCode = strings.TrimSpace(parts[1])
			}
			break
		}
	}
	var preview string
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t != "" && !strings.HasPrefix(t, "Status:") && !strings.HasPrefix(t, "status:") {
			preview = truncpkg.WithEllipsis(t, 100)
			break
		}
	}
	if statusCode != "" && preview != "" {
		return fmt.Sprintf("%s %s → %s: %s", method, url, statusCode, preview)
	}
	if statusCode != "" {
		return fmt.Sprintf("%s %s → %s (%d bytes)", method, url, statusCode, len(content))
	}
	return fmt.Sprintf("%s %s (%d bytes)", method, url, len(content))
}

// summarizeWebScraperResult extracts compact info from web_scraper results.
func summarizeWebScraperResult(msg Message, args map[string]interface{}) string {
	url, _ := args["url"].(string)
	if url == "" {
		url, _ = args["URL"].(string)
	}
	content := msg.Content
	preview := truncpkg.WithEllipsis(content, 120)
	return fmt.Sprintf("Scraped %s: %s", url, preview)
}

// summarizeSearchResult extracts compact info from knowledge/memory search results.
func summarizeSearchResult(msg Message, args map[string]interface{}) string {
	query, _ := args["query"].(string)
	content := msg.Content
	lines := strings.SplitN(content, "\n", 4)
	count := len(lines)
	if count > 3 {
		count = len(strings.Split(content, "\n"))
	}
	preview := truncpkg.WithEllipsis(content, 120)
	return fmt.Sprintf("Search %q (%d results): %s", query, count, preview)
}

// toolCallInfo holds the name and arguments of a tool call for correlation.
type toolCallInfo struct {
	name string
	args string
}

// summarizeToolCall produces a compact causal summary of a tool call message.
// Includes tool_call_id, tool name, and argument keys for causal tracing.
// Full argument values are not included to avoid bloating the summary.
func summarizeToolCall(msg Message) string {
	if msg.Role != RoleToolCall {
		return extractGist(msg.Content, 100)
	}
	if len(msg.ToolCalls) > 0 {
		var parts []string
		for _, tc := range msg.ToolCalls {
			// Build a compact summary: {id} call {name}({key1}, {key2}, ...)
			argKeys := extractArgKeys(tc.Function.Arguments)
			if tc.ID != "" {
				parts = append(parts, fmt.Sprintf("%s: call %s(%s)", tc.ID, tc.Function.Name, argKeys))
			} else {
				parts = append(parts, fmt.Sprintf("call %s(%s)", tc.Function.Name, argKeys))
			}
		}
		return strings.Join(parts, "; ")
	}
	return extractGist(msg.Content, 100)
}

// extractArgKeys extracts top-level JSON keys from a tool call arguments string.
func extractArgKeys(args string) string {
	if args == "" || args == "{}" {
		return ""
	}
	// Parse as simple JSON object to get keys.
	dec := json.NewDecoder(strings.NewReader(args))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		// Not a JSON object; return empty.
		return ""
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		key, ok := tok.(string)
		if !ok {
			continue
		}
		keys = append(keys, key)
		// Skip the value token.
		tok, err = dec.Token()
		if err != nil {
			break
		}
		// If the value is a string, include a brief preview.
		if str, ok := tok.(string); ok && len(str) <= 20 {
			keys[len(keys)-1] = fmt.Sprintf("%s=%q", key, str)
		}
	}
	return strings.Join(keys, ", ")
}

func buildToolCallMap(turn []Message) map[string]toolCallInfo {
	m := make(map[string]toolCallInfo)
	for _, msg := range turn {
		if len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				m[tc.ID] = toolCallInfo{
					name: tc.Function.Name,
					args: tc.Function.Arguments,
				}
			}
		}
	}
	return m
}

// parseToolArgs parses a JSON-encoded tool arguments string.
func parseToolArgs(argsJSON string) map[string]interface{} {
	if argsJSON == "" {
		return nil
	}
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return nil
	}
	return args
}

// groupIntoTurns splits messages into turns using three strategies in order:
//  1. By explicit turn_id field (most reliable).
//  2. By user-message boundary (current default).
//  3. By structural linkage via tool_call_id (fallback when neither turn_id nor user boundaries exist).
//
// Leading system messages are grouped with the first turn.
func groupIntoTurns(messages []Message) [][]Message {
	if len(messages) == 0 {
		return nil
	}

	// Strategy 1: Group by explicit turn_id.
	if turns := groupByTurnID(messages); turns != nil {
		return turns
	}

	// Strategy 2: Group by user-message boundary.
	if turns := groupByUserBoundary(messages); len(turns) > 0 {
		return turns
	}

	// Strategy 3: Fallback — structural linkage via tool_call_id.
	return groupByStructuralLinkage(messages)
}

// groupByTurnID groups messages by their TurnID field.
// Returns nil if no messages have TurnID set.
func groupByTurnID(messages []Message) [][]Message {
	hasTurnID := false
	for _, m := range messages {
		if m.TurnID != "" {
			hasTurnID = true
			break
		}
	}
	if !hasTurnID {
		return nil
	}

	var turns [][]Message
	var current []Message
	var currentTurnID string
	var turnIDAssigned bool

	for _, msg := range messages {
		if msg.TurnID != "" {
			if !turnIDAssigned {
				currentTurnID = msg.TurnID
				turnIDAssigned = true
			} else if msg.TurnID != currentTurnID && len(current) > 0 {
				turns = append(turns, current)
				current = nil
				currentTurnID = msg.TurnID
			}
		}
		current = append(current, msg)
	}
	if len(current) > 0 {
		turns = append(turns, current)
	}
	return turns
}

// groupByUserBoundary splits messages into turns at each user message boundary.
// Leading system messages are grouped with the first turn.
func groupByUserBoundary(messages []Message) [][]Message {
	var turns [][]Message
	current := make([]Message, 0, len(messages))

	for _, msg := range messages {
		if msg.Role == RoleUser && len(current) > 0 && hasUserIn(current) {
			turns = append(turns, current)
			current = make([]Message, 0, len(messages))
		}
		current = append(current, msg)
	}
	if len(current) > 0 {
		turns = append(turns, current)
	}
	return turns
}

// groupByStructuralLinkage groups messages using tool_call_id relationships.
// An assistant message with ToolCalls followed by a tool result referencing
// those calls forms a structural unit.
func groupByStructuralLinkage(messages []Message) [][]Message {
	if len(messages) == 0 {
		return nil
	}

	var turns [][]Message
	var current []Message
	pendingToolCallIDs := make(map[string]bool)

	for _, msg := range messages {
		current = append(current, msg)

		// Track tool call IDs from assistant messages.
		if msg.Role == RoleAssistant && len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				pendingToolCallIDs[tc.ID] = true
			}
		}

		// Check if this tool result completes a pending tool call.
		if msg.Role == RoleToolResult {
			delete(pendingToolCallIDs, msg.ToolCallID)
		}

		// If no pending tool calls and we have a user message, end the turn.
		if msg.Role == RoleUser && len(pendingToolCallIDs) == 0 && len(current) > 1 {
			// Only split if we already have a completed turn before.
			turns = append(turns, current)
			current = nil
		}
	}

	if len(current) > 0 {
		turns = append(turns, current)
	}
	return turns
}

// hasUserIn reports whether any message in the slice has role RoleUser.
func hasUserIn(msgs []Message) bool {
	for _, m := range msgs {
		if m.Role == RoleUser {
			return true
		}
	}
	return false
}

// Stats returns a snapshot of current cleaning statistics.
func (c *ContextCleaner) Stats() CleanerStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

// ResetStats resets all statistics counters to zero.
func (c *ContextCleaner) ResetStats() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stats = CleanerStats{}
}

// Compiled regexes used across methods.
var (
	codeBlockRE = regexp.MustCompile("```[^`]*```")
	sentenceRE  = regexp.MustCompile(`[.!?\n]+\s*`)
)
