package flight

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// DiagnosticCategory classifies the root cause of a failure.
type DiagnosticCategory string

const (
	DiagToolTimeout      DiagnosticCategory = "tool_timeout"
	DiagLLMError         DiagnosticCategory = "llm_error"
	DiagParseError       DiagnosticCategory = "parse_error"
	DiagMemoryError      DiagnosticCategory = "memory_error"
	DiagNetworkError     DiagnosticCategory = "network_error"
	DiagConfigError      DiagnosticCategory = "config_error"
	DiagConcurrencyError DiagnosticCategory = "concurrency_error"
	DiagUnknown          DiagnosticCategory = "unknown"
)

// DiagnosticRecord records a single failure with root cause analysis.
type DiagnosticRecord struct {
	ID         string             `json:"id"`
	AgentID    string             `json:"agent_id"`
	TaskID     string             `json:"task_id"`
	Category   DiagnosticCategory `json:"category"`
	RootCause  string             `json:"rootcause"`
	Suggestion string             `json:"suggestion"`
	Timestamp  time.Time          `json:"timestamp"`
	Duration   time.Duration      `json:"duration"`
	Context    map[string]any     `json:"context,omitempty"`
}

// CategoryDistribution shows the percentage breakdown of failure categories.
type CategoryDistribution struct {
	Categories  map[DiagnosticCategory]int     `json:"categories"`
	Percentages map[DiagnosticCategory]float64 `json:"percentages"`
	Total       int                            `json:"total"`
}

// DiagnosticsEngine analyzes failures and provides root cause classification.
type DiagnosticsEngine struct {
	records []DiagnosticRecord
	mu      sync.RWMutex
	cap     int
}

// maxDiagnosticRecords is the ring cap for diagnostic records.
const maxDiagnosticRecords = 200

// NewDiagnosticsEngine creates an empty diagnostics engine.
func NewDiagnosticsEngine() *DiagnosticsEngine {
	return &DiagnosticsEngine{
		records: make([]DiagnosticRecord, 0, 32),
		cap:     maxDiagnosticRecords,
	}
}

// Record adds a diagnostic record.
func (e *DiagnosticsEngine) Record(r DiagnosticRecord) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.records = append(e.records, r)
	// P1-2: ring cap — drop the oldest record when the cap is exceeded.
	if e.cap > 0 && len(e.records) > e.cap {
		e.records = e.records[len(e.records)-e.cap:]
	}
}

// All returns all diagnostic records.
func (e *DiagnosticsEngine) All() []DiagnosticRecord {
	e.mu.RLock()
	defer e.mu.RUnlock()
	result := make([]DiagnosticRecord, len(e.records))
	copy(result, e.records)
	return result
}

// FilterByAgent returns diagnostics for a specific agent.
func (e *DiagnosticsEngine) FilterByAgent(agentID string) []DiagnosticRecord {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var result []DiagnosticRecord
	for _, r := range e.records {
		if r.AgentID == agentID {
			result = append(result, r)
		}
	}
	return result
}

// FilterByCategory returns diagnostics of a specific category.
func (e *DiagnosticsEngine) FilterByCategory(cat DiagnosticCategory) []DiagnosticRecord {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var result []DiagnosticRecord
	for _, r := range e.records {
		if r.Category == cat {
			result = append(result, r)
		}
	}
	return result
}

// Distribution computes the category distribution.
func (e *DiagnosticsEngine) Distribution() CategoryDistribution {
	e.mu.RLock()
	defer e.mu.RUnlock()

	dist := CategoryDistribution{
		Categories:  make(map[DiagnosticCategory]int),
		Percentages: make(map[DiagnosticCategory]float64),
		Total:       len(e.records),
	}

	for _, r := range e.records {
		dist.Categories[r.Category]++
	}

	if dist.Total > 0 {
		for cat, count := range dist.Categories {
			dist.Percentages[cat] = float64(count) / float64(dist.Total) * 100
		}
	}

	return dist
}

// Len returns the number of records.
func (e *DiagnosticsEngine) Len() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return len(e.records)
}

// ClassifyError automatically categorizes an error message.
func ClassifyError(errMsg string) DiagnosticCategory {
	switch {
	case contains(errMsg, "timeout") || contains(errMsg, "deadline exceeded"):
		return DiagToolTimeout
	case contains(errMsg, "llm") || contains(errMsg, "openai") || contains(errMsg, "ollama") || contains(errMsg, "generate"):
		return DiagLLMError
	case contains(errMsg, "parse") || contains(errMsg, "unmarshal") || contains(errMsg, "json"):
		return DiagParseError
	case contains(errMsg, "memory") || contains(errMsg, "session") || contains(errMsg, "distill"):
		return DiagMemoryError
	case contains(errMsg, "connection") || contains(errMsg, "network") || contains(errMsg, "dial"):
		return DiagNetworkError
	case contains(errMsg, "config") || contains(errMsg, "yaml") || contains(errMsg, "env"):
		return DiagConfigError
	default:
		return DiagUnknown
	}
}

// SuggestFix returns actionable suggestions based on the diagnostic category.
func SuggestFix(cat DiagnosticCategory) []string {
	switch cat {
	case DiagToolTimeout:
		return []string{
			"Increase tool timeout in config",
			"Add retry with exponential backoff",
			"Check if the tool server is responsive",
		}
	case DiagLLMError:
		return []string{
			"Check LLM provider status (Ollama/OpenAI)",
			"Verify API key and base URL",
			"Try a different model",
			"Reduce input token count",
		}
	case DiagParseError:
		return []string{
			"Improve prompt to enforce JSON output",
			"Add output parser with error recovery",
			"Use structured output mode if available",
		}
	case DiagMemoryError:
		return []string{
			"Check database connectivity",
			"Verify session ID is valid",
			"Check memory manager configuration",
		}
	case DiagNetworkError:
		return []string{
			"Check network connectivity",
			"Verify target URL is reachable",
			"Check firewall/proxy settings",
		}
	case DiagConfigError:
		return []string{
			"Validate config file syntax",
			"Check required fields are set",
			"Verify environment variables",
		}
	case DiagConcurrencyError:
		return []string{
			"Check for race conditions with -race flag",
			"Verify mutex usage on shared state",
			"Ensure goroutines have deterministic exit",
		}
	default:
		return []string{
			"Check logs for detailed error message",
			"Enable debug logging for more context",
		}
	}
}

// AutoDiagnose creates a DiagnosticRecord from an error with automatic classification.
func AutoDiagnose(agentID, taskID string, err error, duration time.Duration) DiagnosticRecord {
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	cat := ClassifyError(errMsg)
	suggestions := SuggestFix(cat)

	suggestion := ""
	if len(suggestions) > 0 {
		suggestion = suggestions[0]
	}

	return DiagnosticRecord{
		ID:         fmt.Sprintf("diag-%d", time.Now().UnixNano()),
		AgentID:    agentID,
		TaskID:     taskID,
		Category:   cat,
		RootCause:  errMsg,
		Suggestion: suggestion,
		Timestamp:  time.Now(),
		Duration:   duration,
	}
}

// contains checks if s contains substr (case-insensitive via lowercase).
func contains(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
