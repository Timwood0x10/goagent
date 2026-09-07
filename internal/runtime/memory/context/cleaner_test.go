package context

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	truncpkg "github.com/Timwood0x10/ares/internal/truncate"
)

// Test helpers.

func testMsg(role, content string) Message {
	return Message{Role: role, Content: content, Time: time.Now()}
}

func testMsgWithTool(role, content string, toolCalls []ToolCall) Message {
	return Message{Role: role, Content: content, Time: time.Now(), ToolCalls: toolCalls}
}

// ──────────────────────────── Empty / nil ────────────────────────────

func TestCleaner_EmptyInput(t *testing.T) {
	cl := NewContextCleaner()

	t.Run("nil slice", func(t *testing.T) {
		result := cl.Clean(nil)
		if result != nil {
			t.Errorf("expected nil, got %v", result)
		}
	})

	t.Run("empty slice", func(t *testing.T) {
		in := []Message{}
		result := cl.Clean(in)
		if len(result) != 0 {
			t.Errorf("expected empty, got %d", len(result))
		}
	})
}

// ──────────────────────────── Role routing ────────────────────────────

func TestCleaner_UserRole(t *testing.T) {
	cl := NewContextCleaner()
	opts := DefaultCleanOptions()

	t.Run("within limit", func(t *testing.T) {
		msg := testMsg(RoleUser, "short")
		result := cl.Clean([]Message{msg})
		if result[0].Content != "short" {
			t.Errorf("expected 'short', got %q", result[0].Content)
		}
	})

	t.Run("exceeds limit", func(t *testing.T) {
		long := longString(opts.MaxUserLen+50, 'あ')
		msg := testMsg(RoleUser, long)
		result := cl.Clean([]Message{msg})
		if utf8.RuneCountInString(result[0].Content) > opts.MaxUserLen+3 {
			t.Errorf("content too long: %d runes", utf8.RuneCountInString(result[0].Content))
		}
		if !strings.HasSuffix(result[0].Content, "...") {
			t.Errorf("expected truncated content ending with '...', got %q", result[0].Content)
		}
	})
}

func TestCleaner_AssistantPureReasoning(t *testing.T) {
	cl := NewContextCleaner()
	opts := DefaultCleanOptions()

	t.Run("within limit, no code blocks", func(t *testing.T) {
		msg := testMsg(RoleAssistant, "Sure, here is my analysis.")
		result := cl.Clean([]Message{msg}, opts)
		if result[0].Content != "Sure, here is my analysis." {
			t.Errorf("content changed unexpectedly: %q", result[0].Content)
		}
	})

	t.Run("exceeds limit with code blocks", func(t *testing.T) {
		content := "Let me implement that.\n```go\npackage main\nfunc main() {}\n```\n" +
			longString(opts.MaxAssistantLen+50, 'b')
		msg := testMsg(RoleAssistant, content)
		result := cl.Clean([]Message{msg}, opts)
		// Code block should be compressed.
		if contains(result[0].Content, "```") {
			t.Errorf("code block not compressed: %q", result[0].Content)
		}
		if utf8.RuneCountInString(result[0].Content) > opts.MaxAssistantLen+3 {
			t.Errorf("content too long: %d runes", utf8.RuneCountInString(result[0].Content))
		}
	})
}

func TestCleaner_AssistantWithToolCalls(t *testing.T) {
	cl := NewContextCleaner()

	t.Run("tool-invoking assistant gets gist", func(t *testing.T) {
		content := "I need to search for the weather. First, let me check the API docs." +
			"\n\nHere is the full response with lots of additional text that should be stripped."
		msg := testMsgWithTool(RoleAssistant, content, []ToolCall{
			{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "get_weather"}},
		})
		result := cl.Clean([]Message{msg})
		// Should be aggressively compressed: gist only.
		gist := result[0].Content
		if len(gist) >= len(content) {
			t.Errorf("tool content not compressed: %d vs %d", len(gist), len(content))
		}
	})
}

func TestCleaner_ToolCallAndResultRoles(t *testing.T) {
	cl := NewContextCleaner()

	t.Run("tool_call role", func(t *testing.T) {
		msg := testMsg(RoleToolCall, "search(\"weather\") returned a very long result with lots of detail...")
		result := cl.Clean([]Message{msg})
		if len(result[0].Content) >= len(msg.Content) {
			t.Errorf("tool_call content not compressed")
		}
	})

	t.Run("tool_result role", func(t *testing.T) {
		msg := testMsg(RoleToolResult, "Here is the detailed weather data: temperature=25, humidity=60, ... (long)")
		result := cl.Clean([]Message{msg})
		if len(result[0].Content) >= len(msg.Content) {
			t.Errorf("tool_result content not compressed")
		}
	})

	t.Run("tool_call preserves ToolCallID", func(t *testing.T) {
		msg := Message{Role: RoleToolCall, Content: "search result", ToolCallID: "tc_001"}
		result := cl.Clean([]Message{msg})
		if result[0].ToolCallID != "tc_001" {
			t.Errorf("ToolCallID not preserved: got %q", result[0].ToolCallID)
		}
	})
}

func TestCleaner_SystemRole(t *testing.T) {
	cl := NewContextCleaner()
	opts := DefaultCleanOptions()

	t.Run("within limit", func(t *testing.T) {
		msg := testMsg(RoleSystem, "You are a helpful assistant.")
		result := cl.Clean([]Message{msg}, opts)
		if result[0].Content != "You are a helpful assistant." {
			t.Errorf("content changed unexpectedly: %q", result[0].Content)
		}
	})

	t.Run("exceeds limit", func(t *testing.T) {
		long := longString(opts.MaxSystemLen+100, 'c')
		msg := testMsg(RoleSystem, long)
		result := cl.Clean([]Message{msg}, opts)
		if utf8.RuneCountInString(result[0].Content) > opts.MaxSystemLen+3 {
			t.Errorf("content too long: %d runes", utf8.RuneCountInString(result[0].Content))
		}
	})
}

func TestCleaner_DefaultRole(t *testing.T) {
	cl := NewContextCleaner()

	t.Run("unknown role falls back to assistant limit", func(t *testing.T) {
		msg := testMsg("custom_role", longString(300, 'd'))
		result := cl.Clean([]Message{msg})
		opts := DefaultCleanOptions()
		if utf8.RuneCountInString(result[0].Content) > opts.MaxAssistantLen+3 {
			t.Errorf("unknown role content too long: %d runes",
				utf8.RuneCountInString(result[0].Content))
		}
	})
}

// ──────────────────────────── Mixed sequences ────────────────────────────

func TestCleaner_MixedSequence(t *testing.T) {
	cl := NewContextCleaner()

	msgs := []Message{
		testMsg(RoleSystem, "You are a coding assistant."), // within MaxSystemLen=500
		testMsg(RoleUser, "Write a fibonacci function."),   // within MaxUserLen=200
		testMsgWithTool(RoleAssistant, "Let me write the fibonacci code for you. This is a recursive implementation that will calculate the nth fibonacci number.", []ToolCall{
			{ID: "call_1", Function: ToolCallFunction{Name: "write_code"}},
		}),
		testMsg(RoleToolResult, "Code written successfully. Output: fibonacci(10)=55. This was generated by the code execution engine."),
		testMsg(RoleAssistant, "Here is the fibonacci function:\n\n```python\ndef fib(n):\n    return n if n <= 1 else fib(n-1) + fib(n-2)\n```\n\nThis is a recursive implementation."),
	}

	result := cl.Clean(msgs)
	if len(result) != len(msgs) {
		t.Fatalf("expected %d messages, got %d", len(msgs), len(result))
	}

	// Assistant with tool calls should be compressed.
	if len(result[2].Content) >= len(msgs[2].Content) {
		t.Errorf("tool-assistant at idx 2 not compressed")
	}
	// Tool result should be compressed.
	if len(result[3].Content) >= len(msgs[3].Content) {
		t.Errorf("tool_result at idx 3 not compressed")
	}
	// Pure reasoning assistant should keep code block compressed.
	if contains(result[4].Content, "```") {
		t.Errorf("code block not compressed in reasoning assistant at idx 4")
	}
}

// ──────────────────────────── Code block compression ────────────────────────────

func TestCleaner_CompressCodeBlocks(t *testing.T) {
	cl := NewContextCleaner()

	t.Run("go code block", func(t *testing.T) {
		content := "Here is the implementation:\n```go\npackage main\nimport \"fmt\"\nfunc main() {}\n```"
		compressed := cl.compressCodeBlocks(content)
		if contains(compressed, "```") {
			t.Errorf("code block should be compressed, got: %q", compressed)
		}
		if !contains(compressed, "[go]") {
			t.Errorf("compressed block should include language hint, got: %q", compressed)
		}
	})

	t.Run("json code block", func(t *testing.T) {
		content := "```json\n{\"key\": \"value\"}\n```"
		compressed := cl.compressCodeBlocks(content)
		if !contains(compressed, "[json]") {
			t.Errorf("compressed block should include json hint, got: %q", compressed)
		}
	})

	t.Run("code block without language", func(t *testing.T) {
		content := "```\nplain code\n```"
		compressed := cl.compressCodeBlocks(content)
		if contains(compressed, "```") {
			t.Errorf("code block without lang should still be compressed, got: %q", compressed)
		}
	})
}

// ──────────────────────────── extractGist ────────────────────────────

func TestExtractGist(t *testing.T) {
	t.Run("first sentence extracted", func(t *testing.T) {
		content := "The result is 42. This is the second sentence. And a third."
		gist := extractGist(content, 200)
		if gist != "The result is 42." {
			t.Errorf("expected 'The result is 42.', got %q", gist)
		}
	})

	t.Run("empty content after removing code blocks", func(t *testing.T) {
		content := "```python\ncode only\n```"
		gist := extractGist(content, 200)
		if gist == "" {
			t.Errorf("expected fallback, got empty")
		}
	})

	t.Run("fallback to truncate when no sentence boundary", func(t *testing.T) {
		content := longString(100, 'x')
		gist := extractGist(content, 20)
		if len(gist) == 0 || len(gist) > 23 {
			t.Errorf("expected truncation with ..., got %q (len %d)", gist, len(gist))
		}
	})

	t.Run("gist within limit returns full first sentence", func(t *testing.T) {
		content := "Short."
		gist := extractGist(content, 200)
		if gist != "Short." {
			t.Errorf("expected 'Short.', got %q", gist)
		}
	})
}

// ──────────────────────────── Truncation ────────────────────────────

func TestTruncateContent(t *testing.T) {
	t.Run("empty string", func(t *testing.T) {
		if got := truncpkg.WithEllipsis("", 10); got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("within limit", func(t *testing.T) {
		if got := truncpkg.WithEllipsis("hello", 10); got != "hello" {
			t.Errorf("expected 'hello', got %q", got)
		}
	})

	t.Run("exceeds limit", func(t *testing.T) {
		if got := truncpkg.WithEllipsis("hello world", 5); got != "he..." {
			t.Errorf("expected 'he...', got %q", got)
		}
	})

	t.Run("zero maxLen", func(t *testing.T) {
		if got := truncpkg.WithEllipsis("hello", 0); got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("negative maxLen", func(t *testing.T) {
		if got := truncpkg.WithEllipsis("hello", -1); got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("multi-byte UTF-8", func(t *testing.T) {
		// 5 runes in the CJK sample. maxLen=3 equals ellipsis length, so the string
		// is truncated to 3 runes without an ellipsis suffix.
		s := "日本語です"
		got := truncpkg.WithEllipsis(s, 3)
		if got != "日本語" {
			t.Errorf("expected '日本語', got %q", got)
		}
		if utf8.RuneCountInString(got) != 3 {
			t.Errorf("expected 3 runes, got %d", utf8.RuneCountInString(got))
		}
	})
}

// ──────────────────────────── UTF-8 boundary ────────────────────────────

func TestCleaner_UTF8TruncationBoundary(t *testing.T) {
	cl := NewContextCleaner()
	opts := CleanOptions{
		MaxUserLen:      10,
		MaxAssistantLen: 10,
		MaxToolLen:      10,
		MaxSystemLen:    10,
	}

	long := longString(15, '界') // 15 Chinese characters, each 3 bytes
	msg := testMsg(RoleUser, long)
	result := cl.Clean([]Message{msg}, opts)

	// Should not have broken multi-byte sequence.
	if !utf8.ValidString(result[0].Content) {
		t.Errorf("invalid UTF-8 after truncation: %q", result[0].Content)
	}
	runes := utf8.RuneCountInString(result[0].Content)
	if runes > 13 { // 10 runes + "..."
		t.Errorf("expected ≤13 runes, got %d: %q", runes, result[0].Content)
	}
}

// ──────────────────────────── Custom options ────────────────────────────

func TestCleaner_CustomOptions(t *testing.T) {
	cl := NewContextCleaner()

	opts := CleanOptions{
		MaxUserLen:      5,
		MaxAssistantLen: 5,
		MaxToolLen:      3,
		MaxSystemLen:    5,
	}

	msgs := []Message{
		testMsg(RoleUser, "hello world this is long"),
		testMsg(RoleAssistant, "here is a detailed response for you"),
		testMsgWithTool(RoleAssistant, "very long tool assistant content here", []ToolCall{{}}),
		testMsg(RoleSystem, "very long system prompt here"),
	}

	result := cl.Clean(msgs, opts)

	tests := []struct {
		idx  int
		max  int
		name string
	}{
		{0, opts.MaxUserLen, "user"},
		{1, opts.MaxAssistantLen, "assistant"},
		{3, opts.MaxSystemLen, "system"},
	}
	for _, tt := range tests {
		runes := utf8.RuneCountInString(result[tt.idx].Content)
		if runes > tt.max+3 {
			t.Errorf("%s: expected ≤%d runes, got %d: %q", tt.name, tt.max+3, runes, result[tt.idx].Content)
		}
	}

	// Tool assistant should use MaxToolLen or less (gist).
	if len(result[2].Content) > 30 {
		t.Errorf("tool assistant not compressed: %q (len %d)", result[2].Content, len(result[2].Content))
	}
}

// ──────────────────────────── Stats tracking ────────────────────────────

func TestCleaner_Stats(t *testing.T) {
	cl := NewContextCleaner()

	// Initially zero.
	stats := cl.Stats()
	if stats.ToolCalls != 0 || stats.LLMCalls != 0 || stats.BytesSaved != 0 {
		t.Errorf("expected zero stats, got %+v", stats)
	}

	// Clean once — use content long enough to exceed default truncation limits.
	msgs := []Message{
		testMsg(RoleUser, longString(300, 'a')),                                      // MaxUserLen=200 → 100 bytes saved
		testMsg(RoleAssistant, longString(300, 'b')),                                 // MaxAssistantLen=150 → 150 bytes saved
		testMsgWithTool(RoleAssistant, longString(100, 'c'), []ToolCall{{ID: "c1"}}), // MaxToolLen=50 → 50 bytes saved
		testMsg(RoleToolCall, longString(100, 'd')),                                  // MaxToolLen=50 → 50 bytes saved
		testMsg(RoleToolResult, longString(100, 'e')),                                // 50 bytes saved
	}
	cl.Clean(msgs)

	stats = cl.Stats()
	if stats.HistoryIn != 5 {
		t.Errorf("expected HistoryIn=5, got %d", stats.HistoryIn)
	}
	if stats.HistoryOut != 5 {
		t.Errorf("expected HistoryOut=5, got %d", stats.HistoryOut)
	}
	// Tool-related: assistant with ToolCalls (1) + tool_call (1) + tool_result (1) = 3
	if stats.ToolCalls != 3 {
		t.Errorf("expected ToolCalls=3, got %d", stats.ToolCalls)
	}
	// Pure reasoning assistant: 1
	if stats.LLMCalls != 1 {
		t.Errorf("expected LLMCalls=1, got %d", stats.LLMCalls)
	}
	if stats.BytesSaved <= 0 {
		t.Errorf("expected BytesSaved > 0, got %d", stats.BytesSaved)
	}
}

func TestCleaner_StatsMultipleCalls(t *testing.T) {
	cl := NewContextCleaner()

	cl.Clean([]Message{testMsg(RoleUser, "hello")})
	cl.Clean([]Message{testMsg(RoleToolCall, "tool data")})
	cl.Clean([]Message{
		testMsg(RoleAssistant, "reasoning"),
		testMsg(RoleAssistant, "more reasoning"),
	})

	stats := cl.Stats()
	if stats.HistoryIn != 4 {
		t.Errorf("expected HistoryIn=4, got %d", stats.HistoryIn)
	}
	if stats.ToolCalls != 1 {
		t.Errorf("expected ToolCalls=1, got %d", stats.ToolCalls)
	}
	if stats.LLMCalls != 2 {
		t.Errorf("expected LLMCalls=2, got %d", stats.LLMCalls)
	}
}

func TestCleaner_StatsReset(t *testing.T) {
	cl := NewContextCleaner()
	cl.Clean([]Message{testMsg(RoleUser, "hello")})
	cl.ResetStats()

	stats := cl.Stats()
	if stats.HistoryIn != 0 || stats.BytesSaved != 0 {
		t.Errorf("expected zero after reset, got %+v", stats)
	}
}

// ──────────────────────────── Fields preserved ────────────────────────────

func TestCleaner_FieldsPreserved(t *testing.T) {
	cl := NewContextCleaner()

	now := time.Now()
	msgs := []Message{
		{
			Role:       RoleToolCall,
			Content:    "search result",
			Time:       now,
			TurnID:     "turn_1",
			ToolCallID: "tc_001",
		},
		{
			Role:    RoleAssistant,
			Content: "analysis",
			Time:    now,
			TurnID:  "turn_1",
			ToolCalls: []ToolCall{
				{ID: "call_1", Type: "function", Function: ToolCallFunction{Name: "search", Arguments: `{"q":"test"}`}},
			},
		},
	}

	result := cl.Clean(msgs)

	// ToolCallID preserved.
	if result[0].ToolCallID != "tc_001" {
		t.Errorf("ToolCallID not preserved: got %q", result[0].ToolCallID)
	}
	// TurnID preserved.
	if result[0].TurnID != "turn_1" {
		t.Errorf("TurnID not preserved: got %q", result[0].TurnID)
	}
	if result[1].TurnID != "turn_1" {
		t.Errorf("TurnID not preserved for assistant: got %q", result[1].TurnID)
	}
	// Time preserved.
	if !result[0].Time.Equal(now) {
		t.Errorf("Time not preserved: got %v", result[0].Time)
	}
	// ToolCalls preserved for assistant.
	if len(result[1].ToolCalls) != 1 {
		t.Fatalf("expected 1 ToolCall, got %d", len(result[1].ToolCalls))
	}
	if result[1].ToolCalls[0].ID != "call_1" {
		t.Errorf("ToolCall.ID not preserved: got %q", result[1].ToolCalls[0].ID)
	}
	if result[1].ToolCalls[0].Function.Name != "search" {
		t.Errorf("ToolCall.Function.Name not preserved: got %q", result[1].ToolCalls[0].Function.Name)
	}
}

// ──────────────────────────── Thread safety ────────────────────────────

func TestCleaner_ConcurrentAccess(t *testing.T) {
	cl := NewContextCleaner()

	var wg sync.WaitGroup
	n := 50

	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			msgs := []Message{
				{Role: RoleUser, Content: "msg " + strconv.Itoa(idx)},
				{Role: RoleAssistant, Content: "response " + strconv.Itoa(idx)},
				{Role: RoleToolCall, Content: "tool " + strconv.Itoa(idx)},
			}
			result := cl.Clean(msgs)
			if len(result) != 3 {
				t.Errorf("expected 3 results, got %d", len(result))
			}
		}(i)
	}

	wg.Wait()

	stats := cl.Stats()
	if stats.HistoryIn != n*3 {
		t.Errorf("expected HistoryIn=%d, got %d", n*3, stats.HistoryIn)
	}
	if stats.ToolCalls != int64(n) {
		t.Errorf("expected ToolCalls=%d, got %d", n, stats.ToolCalls)
	}
}

// ──────────────────────────── Default options ────────────────────────────

func TestDefaultCleanOptions(t *testing.T) {
	opts := DefaultCleanOptions()
	if opts.MaxUserLen <= 0 {
		t.Errorf("MaxUserLen should be positive, got %d", opts.MaxUserLen)
	}
	if opts.MaxAssistantLen <= 0 {
		t.Errorf("MaxAssistantLen should be positive, got %d", opts.MaxAssistantLen)
	}
	if opts.MaxToolLen <= 0 {
		t.Errorf("MaxToolLen should be positive, got %d", opts.MaxToolLen)
	}
	if opts.MaxSystemLen <= 0 {
		t.Errorf("MaxSystemLen should be positive, got %d", opts.MaxSystemLen)
	}
}

// ──────────────────────────── Helpers ────────────────────────────

// longString builds a string of n runes with character ch.
func longString(n int, ch rune) string {
	runes := make([]rune, n)
	for i := range runes {
		runes[i] = ch
	}
	return string(runes)
}

// ──────────────────────────── turn grouping ────────────────────────────

func TestGroupIntoTurns(t *testing.T) {
	t.Run("empty input", func(t *testing.T) {
		turns := groupIntoTurns(nil)
		if len(turns) != 0 {
			t.Errorf("expected 0 turns, got %d", len(turns))
		}
	})

	t.Run("single user message", func(t *testing.T) {
		msgs := []Message{testMsg(RoleUser, "hello")}
		turns := groupIntoTurns(msgs)
		if len(turns) != 1 {
			t.Fatalf("expected 1 turn, got %d", len(turns))
		}
		if len(turns[0]) != 1 {
			t.Errorf("expected 1 message in turn, got %d", len(turns[0]))
		}
	})

	t.Run("system then user", func(t *testing.T) {
		msgs := []Message{
			testMsg(RoleSystem, "You are a bot."),
			testMsg(RoleUser, "hello"),
		}
		turns := groupIntoTurns(msgs)
		if len(turns) != 1 {
			t.Fatalf("expected 1 turn, got %d", len(turns))
		}
		// System message grouped with first user.
		if len(turns[0]) != 2 {
			t.Errorf("expected 2 messages in turn, got %d", len(turns[0]))
		}
	})

	t.Run("two turns with tool calls", func(t *testing.T) {
		msgs := []Message{
			testMsg(RoleUser, "turn 1 question"),
			testMsgWithTool(RoleAssistant, "let me search", []ToolCall{{ID: "c1"}}),
			testMsg(RoleToolResult, "file content"),
			testMsg(RoleAssistant, "here is the answer"),
			testMsg(RoleUser, "turn 2 question"),
			testMsg(RoleAssistant, "answer to turn 2"),
		}
		turns := groupIntoTurns(msgs)
		if len(turns) != 2 {
			t.Fatalf("expected 2 turns, got %d", len(turns))
		}
		if len(turns[0]) != 4 {
			t.Errorf("expected 4 messages in turn 0, got %d", len(turns[0]))
		}
		if len(turns[1]) != 2 {
			t.Errorf("expected 2 messages in turn 1, got %d", len(turns[1]))
		}
	})
}

// ──────────────────────────── CleanWithTurns ────────────────────────────

func TestCleaner_CleanWithTurns_Empty(t *testing.T) {
	cl := NewContextCleaner()

	t.Run("nil slice", func(t *testing.T) {
		result := cl.CleanWithTurns(nil)
		if result != nil {
			t.Errorf("expected nil, got %v", result)
		}
	})

	t.Run("empty slice", func(t *testing.T) {
		result := cl.CleanWithTurns([]Message{})
		if len(result) != 0 {
			t.Errorf("expected empty, got %d", len(result))
		}
	})
}

func TestCleaner_CleanWithTurns_SummarizesCompletedToolResults(t *testing.T) {
	cl := NewContextCleaner()

	// Two turns: first completed, second active.
	msgs := []Message{
		testMsg(RoleUser, "first question"),
		testMsgWithTool(RoleAssistant, longString(100, 's'),
			[]ToolCall{{ID: "c1", Function: ToolCallFunction{Name: "search"}}}),
		testMsg(RoleToolResult, "very long tool result data that should be dropped"),
		testMsg(RoleAssistant, "final answer for turn 1"),
		testMsg(RoleUser, "second question"),
		testMsg(RoleAssistant, "active answer"),
	}

	result := cl.CleanWithTurns(msgs)

	// Turn 1 (completed): tool_call dropped, tool_result summarized.
	// Turn 2 (active): all messages preserved.
	expectedLen := 6
	if len(result) != expectedLen {
		t.Fatalf("expected %d messages, got %d", expectedLen, len(result))
	}

	// Turn 1: user, assistant(with toolcalls), tool_result(summarized), assistant(answer).
	if result[0].Role != RoleUser {
		t.Errorf("expected result[0] role user, got %s", result[0].Role)
	}
	if result[1].Role != RoleAssistant {
		t.Errorf("expected result[1] role assistant, got %s", result[1].Role)
	}
	// Assistant with toolcalls in completed turn: content compressed.
	if len(result[1].Content) >= len(msgs[1].Content) {
		t.Errorf("completed turn assistant with toolcalls not compressed")
	}
	if len(result[1].ToolCalls) != 1 {
		t.Errorf("expected 1 ToolCall preserved, got %d", len(result[1].ToolCalls))
	}
	// Tool result should be summarized (shorter than original).
	if result[2].Role != RoleToolResult {
		t.Errorf("expected result[2] role tool_result, got %s", result[2].Role)
	}
	if len(result[2].Content) == 0 {
		t.Errorf("tool result summary should not be empty")
	}
	if result[3].Role != RoleAssistant {
		t.Errorf("expected result[3] role assistant, got %s", result[3].Role)
	}

	// Turn 2 (active): all original types preserved.
	if result[4].Role != RoleUser {
		t.Errorf("expected result[4] role user, got %s", result[4].Role)
	}
	if result[5].Role != RoleAssistant {
		t.Errorf("expected result[5] role assistant, got %s", result[5].Role)
	}
}

func TestCleaner_CleanWithTurns_SingleTurn(t *testing.T) {
	cl := NewContextCleaner()

	// Single active turn: everything preserved.
	msgs := []Message{
		testMsg(RoleSystem, "system prompt"),
		testMsg(RoleUser, "hello"),
		testMsgWithTool(RoleAssistant, "let me check", []ToolCall{{ID: "c1"}}),
		testMsg(RoleToolResult, "tool output"),
		testMsg(RoleAssistant, "answer"),
	}

	result := cl.CleanWithTurns(msgs)

	if len(result) != len(msgs) {
		t.Fatalf("expected %d messages, got %d", len(msgs), len(result))
	}
	// ToolCallID and ToolCalls preserved.
	if len(result[2].ToolCalls) != 1 {
		t.Errorf("expected 1 ToolCall preserved, got %d", len(result[2].ToolCalls))
	}
}

func TestCleaner_CleanWithTurns_ThreeTurns(t *testing.T) {
	cl := NewContextCleaner()

	// Three turns: first two completed, third active.
	msgs := []Message{
		testMsg(RoleUser, "q1"),
		testMsg(RoleToolCall, "tool call detail 1"),
		testMsg(RoleAssistant, "a1"),
		testMsg(RoleUser, "q2"),
		testMsg(RoleToolResult, "tool result data 2"),
		testMsg(RoleAssistant, "a2"),
		testMsg(RoleUser, "q3"),
		testMsg(RoleAssistant, "a3"),
	}

	result := cl.CleanWithTurns(msgs)

	// Completed turn 1: tool_call compressed (not dropped, per causal compression design).
	// Completed turn 2: tool_result summarized (kept).
	// Active turn 3: all preserved.
	if len(result) != 8 {
		t.Fatalf("expected 8 messages (all preserved, tool_call compressed not dropped), got %d", len(result))
	}

	// Message roles in order: user, tool_call(compressed), assistant, user, tool_result(summarized), assistant, user, assistant.
	roles := []string{RoleUser, RoleToolCall, RoleAssistant, RoleUser, RoleToolResult, RoleAssistant, RoleUser, RoleAssistant}
	for i, role := range roles {
		if result[i].Role != role {
			t.Errorf("result[%d] expected role %s, got %s", i, role, result[i].Role)
		}
	}
}

// ──────────────────────────── Turn grouping by turn_id ────────────────────────────

func TestGroupIntoTurns_ByTurnID(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Content: "q1", TurnID: "turn_1"},
		{Role: RoleAssistant, Content: "a1", TurnID: "turn_1"},
		{Role: RoleUser, Content: "q2", TurnID: "turn_2"},
		{Role: RoleAssistant, Content: "a2", TurnID: "turn_2"},
		{Role: RoleUser, Content: "q3", TurnID: "turn_3"},
		{Role: RoleAssistant, Content: "a3", TurnID: "turn_3"},
	}

	turns := groupIntoTurns(msgs)
	if len(turns) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(turns))
	}
	if len(turns[0]) != 2 {
		t.Errorf("expected 2 msgs in turn 0, got %d", len(turns[0]))
	}
	if len(turns[1]) != 2 {
		t.Errorf("expected 2 msgs in turn 1, got %d", len(turns[1]))
	}
	if len(turns[2]) != 2 {
		t.Errorf("expected 2 msgs in turn 2, got %d", len(turns[2]))
	}
}

// ──────────────────────────── Turn grouping by structural linkage ────────────────────────────

func TestGroupIntoTurns_ByStructuralLinkage(t *testing.T) {
	// No turn_id, no user boundary — should fall back to structural linkage.
	msgs := []Message{
		{Role: RoleAssistant, Content: "let me search", ToolCalls: []ToolCall{{ID: "call_1"}}},
		{Role: RoleToolResult, Content: "result", ToolCallID: "call_1"},
		{Role: RoleAssistant, Content: "here is the answer"},
	}

	turns := groupIntoTurns(msgs)
	if len(turns) == 0 {
		t.Fatal("expected at least 1 turn")
	}
	// Should group all in one turn (no user boundaries).
	if len(turns[0]) != 3 {
		t.Errorf("expected 3 msgs in first turn, got %d", len(turns[0]))
	}
}

// ──────────────────────────── Turn-ID based CleanWithTurns ────────────────────────────

func TestCleaner_CleanWithTurns_ByTurnID(t *testing.T) {
	cl := NewContextCleaner()

	msgs := []Message{
		{Role: RoleUser, Content: "q1", TurnID: "t1"},
		{Role: RoleAssistant, Content: "thinking 1", TurnID: "t1"},
		{Role: RoleToolCall, Content: "tool detail 1", TurnID: "t1"},
		{Role: RoleToolResult, Content: "tool result 1", TurnID: "t1"},
		{Role: RoleAssistant, Content: "a1", TurnID: "t1"},
		{Role: RoleUser, Content: "q2", TurnID: "t2"},
		{Role: RoleAssistant, Content: "a2", TurnID: "t2"},
	}

	result := cl.CleanWithTurns(msgs)

	// Completed turn (t1): tool_call compressed (not dropped), tool_result summarized.
	// Active turn (t2): all preserved.
	expectedLen := 7 // all preserved, tool_call compressed not dropped
	if len(result) != expectedLen {
		t.Fatalf("expected %d messages, got %d", expectedLen, len(result))
	}
	if result[0].Role != RoleUser {
		t.Errorf("expected result[0] role user, got %s", result[0].Role)
	}
	if result[1].Role != RoleAssistant {
		t.Errorf("expected result[1] role assistant, got %s", result[1].Role)
	}
	// Tool call compressed (kept, not dropped).
	if result[2].Role != RoleToolCall {
		t.Errorf("expected result[2] role tool_call, got %s", result[2].Role)
	}
	// Tool result summarized (kept).
	if result[3].Role != RoleToolResult {
		t.Errorf("expected result[3] role tool_result, got %s", result[3].Role)
	}
	if result[4].Role != RoleAssistant {
		t.Errorf("expected result[4] role assistant, got %s", result[4].Role)
	}
	// Active turn preserved.
	if result[5].Role != RoleUser {
		t.Errorf("expected result[5] role user, got %s", result[5].Role)
	}
	if result[6].Role != RoleAssistant {
		t.Errorf("expected result[6] role assistant, got %s", result[6].Role)
	}
}

// ──────────────────────────── Conservative mode ────────────────────────────

func TestCleaner_CleanWithTurns_ConservativeMode(t *testing.T) {
	cl := NewContextCleaner()

	opts := DefaultCleanOptions()
	opts.Mode = CleaningModeConservative

	msgs := []Message{
		testMsg(RoleUser, "q1"),
		testMsg(RoleToolCall, longString(200, 'x')),
		testMsg(RoleToolResult, longString(200, 'y')),
		testMsg(RoleAssistant, "a1"),
		testMsg(RoleUser, "q2"),
		testMsg(RoleAssistant, "a2"),
	}

	result := cl.CleanWithTurns(msgs, opts)

	// Conservative: tool messages kept but compressed.
	if len(result) != 6 {
		t.Fatalf("expected 6 messages (all kept but compressed), got %d", len(result))
	}
	// Tool messages should be compressed (shorter than original).
	if len(result[1].Content) >= len(msgs[1].Content) {
		t.Errorf("tool_call not compressed in conservative mode")
	}
	if len(result[2].Content) >= len(msgs[2].Content) {
		t.Errorf("tool_result not compressed in conservative mode")
	}
}

// ──────────────────────────── SummarizeToolResult ────────────────────────────

func TestSummarizeToolResult(t *testing.T) {
	t.Run("non-tool message returns content as-is", func(t *testing.T) {
		msg := Message{Role: RoleUser, Content: "hello"}
		result := SummarizeToolResult(msg)
		if result != "hello" {
			t.Errorf("expected 'hello', got %q", result)
		}
	})

	t.Run("tool result gets gist-extracted", func(t *testing.T) {
		msg := Message{Role: RoleToolResult,
			Content: "The file /etc/config/app.yml contains the database connection settings. " +
				"The host is localhost, port is 5432, and the timeout is 30 seconds. " +
				"This is additional detail that should be stripped."}
		result := SummarizeToolResult(msg)
		if len(result) >= len(msg.Content) {
			t.Errorf("tool result not summarized: %d vs %d", len(result), len(msg.Content))
		}
		if result == "" {
			t.Errorf("summary should not be empty")
		}
	})

	t.Run("empty content", func(t *testing.T) {
		msg := Message{Role: RoleToolResult, Content: ""}
		result := SummarizeToolResult(msg)
		if result != "" {
			t.Errorf("expected empty, got %q", result)
		}
	})
}

// ──────────────────────────── SummarizeToolResultWithCall ────────────────────────────

func TestSummarizeToolResultWithCall_FileRead(t *testing.T) {
	msg := Message{Role: RoleToolResult, Content: "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"}
	result := SummarizeToolResultWithCall(msg, "file_tools", `{"operation":"read","path":"main.go"}`)
	if !strings.Contains(result, "main.go") {
		t.Errorf("expected file path in summary, got %q", result)
	}
	if len(result) >= len(msg.Content) {
		t.Errorf("expected summarized content, got %d vs %d bytes", len(result), len(msg.Content))
	}
}

func TestSummarizeToolResultWithCall_FileWrite(t *testing.T) {
	msg := Message{Role: RoleToolResult, Content: "wrote 1024 bytes"}
	result := SummarizeToolResultWithCall(msg, "file_tools", `{"operation":"write","path":"/etc/config.yml"}`)
	if !strings.Contains(result, "/etc/config.yml") {
		t.Errorf("expected file path in summary, got %q", result)
	}
}

func TestSummarizeToolResultWithCall_CodeRunner(t *testing.T) {
	msg := Message{Role: RoleToolResult, Content: "Hello World\nexit code: 0\n"}
	result := SummarizeToolResultWithCall(msg, "code_runner", `{"command":"echo hello","language":"bash"}`)
	if !strings.Contains(result, "exit=0") {
		t.Errorf("expected exit code in summary, got %q", result)
	}
	if !strings.Contains(result, "Hello World") {
		t.Errorf("expected output preview in summary, got %q", result)
	}
}

func TestSummarizeToolResultWithCall_HTTPRequest(t *testing.T) {
	msg := Message{Role: RoleToolResult, Content: "Status: 200 OK\n{\"id\": 1, \"name\": \"test\"}\n"}
	result := SummarizeToolResultWithCall(msg, "http_request", `{"url":"https://api.example.com/resource","method":"GET"}`)
	if !strings.Contains(result, "200") {
		t.Errorf("expected status code in summary, got %q", result)
	}
	if !strings.Contains(result, "api.example.com") {
		t.Errorf("expected URL in summary, got %q", result)
	}
}

func TestSummarizeToolResultWithCall_UnknownTool(t *testing.T) {
	msg := Message{Role: RoleToolResult,
		Content: "The calculation result is 42. " +
			"This is additional detail that should be stripped out."}
	result := SummarizeToolResultWithCall(msg, "calculator", `{}`)
	// Falls back to text-level gist extraction.
	if len(result) >= len(msg.Content) {
		t.Errorf("expected summarized content, got %d vs %d bytes", len(result), len(msg.Content))
	}
	if result == "" {
		t.Errorf("summary should not be empty")
	}
}

func TestSummarizeToolResultWithCall_NonToolMessage(t *testing.T) {
	msg := Message{Role: RoleUser, Content: "hello"}
	result := SummarizeToolResultWithCall(msg, "", "")
	if result != "hello" {
		t.Errorf("expected 'hello', got %q", result)
	}
}

// ──────────────────────────── Extended stats ────────────────────────────

func TestCleaner_ExtendedStats(t *testing.T) {
	cl := NewContextCleaner()

	msgs := []Message{
		testMsg(RoleUser, "q1"),
		testMsg(RoleToolCall, "tool call data"),
		testMsg(RoleToolResult, "tool result data"),
		testMsg(RoleAssistant, "a1"),
		testMsg(RoleUser, "q2"),
		testMsg(RoleAssistant, "a2"),
	}

	cl.CleanWithTurns(msgs)

	stats := cl.Stats()
	// Tool calls are now compressed (not dropped), so DroppedToolMessages should be 0.
	if stats.DroppedToolMessages != 0 {
		t.Errorf("expected DroppedToolMessages == 0 (tool_call compressed, not dropped), got %d", stats.DroppedToolMessages)
	}
	if stats.SummarizedToolMessages < 1 {
		t.Errorf("expected SummarizedToolMessages >= 1 (tool_call + tool_result summarized), got %d", stats.SummarizedToolMessages)
	}
	if stats.TurnsProcessed < 1 {
		t.Errorf("expected TurnsProcessed >= 1, got %d", stats.TurnsProcessed)
	}
	if stats.ActivePreservedMessages < 1 {
		t.Errorf("expected ActivePreservedMessages >= 1, got %d", stats.ActivePreservedMessages)
	}
}

// ──────────────────────────── CleanOptions defaults ────────────────────────────

func TestCleanOptions_PolicyDefaults(t *testing.T) {
	opts := DefaultCleanOptions()
	if opts.KeepRawToolDetails != true {
		t.Errorf("expected KeepRawToolDetails=true, got %v", opts.KeepRawToolDetails)
	}
	if opts.Mode != CleaningModeDefault {
		t.Errorf("expected Mode=CleaningModeDefault, got %v", opts.Mode)
	}
	if opts.MaxRawToolResultLength <= 0 {
		t.Errorf("expected MaxRawToolResultLength > 0, got %d", opts.MaxRawToolResultLength)
	}
}

func TestCleaner_CleanWithTurns_StatsTracked(t *testing.T) {
	cl := NewContextCleaner()

	msgs := []Message{
		testMsg(RoleUser, "hello"),
		testMsg(RoleToolResult, "tool data that should be summarized with significant length for bytes saved"),
		testMsg(RoleAssistant, "done"),
		testMsg(RoleUser, "follow-up"),
		testMsg(RoleAssistant, "ok"),
	}

	cl.CleanWithTurns(msgs)

	stats := cl.Stats()
	if stats.SummarizedToolMessages < 1 {
		t.Errorf("expected SummarizedToolMessages >= 1, got %d", stats.SummarizedToolMessages)
	}
	if stats.TurnsProcessed < 1 {
		t.Errorf("expected TurnsProcessed >= 1, got %d", stats.TurnsProcessed)
	}
	if stats.ActivePreservedMessages < 1 {
		t.Errorf("expected ActivePreservedMessages >= 1, got %d", stats.ActivePreservedMessages)
	}
}
