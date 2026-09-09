package llmcore

// CleaningMode controls how aggressively the context cleaner strips tool data.
type CleaningMode int

const (
	CleaningModeDefault CleaningMode = iota
	CleaningModeConservative
	CleaningModeAggressive
)

// CleanOptions configures content truncation limits and policy controls for context cleaning.
type CleanOptions struct {
	MaxUserLen      int
	MaxAssistantLen int
	MaxToolLen      int
	MaxSystemLen    int

	MaxRawToolResultLength int
	MaxSummarizedTurns     int
	KeepRawToolDetails     bool
	Mode                   CleaningMode
}

// DefaultCleanOptions returns sensible defaults for content truncation.
func DefaultCleanOptions() CleanOptions {
	return CleanOptions{
		MaxUserLen:             200,
		MaxAssistantLen:        150,
		MaxToolLen:             50,
		MaxSystemLen:           500,
		MaxRawToolResultLength: 2000,
		MaxSummarizedTurns:     0,
		KeepRawToolDetails:     true,
		Mode:                   CleaningModeDefault,
	}
}

// CleanerStats holds context cleaning statistics.
type CleanerStats struct {
	LLMCalls                int64 `json:"llm_calls"`
	ToolCalls               int64 `json:"tool_calls"`
	BytesSaved              int64 `json:"bytes_saved"`
	HistoryIn               int   `json:"history_in"`
	HistoryOut              int   `json:"history_out"`
	DroppedToolMessages     int64 `json:"dropped_tool_messages"`
	SummarizedToolMessages  int64 `json:"summarized_tool_messages"`
	ActivePreservedMessages int64 `json:"active_preserved_messages"`
	TurnsProcessed          int64 `json:"turns_processed"`
}

// ContextCleaner defines the interface for cleaning conversation context before LLM calls.
// Implementations apply differential compression based on message role and turn structure.
type ContextCleaner interface {
	// Clean processes a message slice, applying role-aware truncation.
	// Returns a new slice with compressed content; original slice is not modified.
	Clean(messages []Message, opts ...CleanOptions) []Message

	// CleanWithTurns performs turn-aware context cleaning.
	// Messages are grouped into turns; completed turns are summarized, active turn preserved.
	CleanWithTurns(messages []Message, opts ...CleanOptions) []Message

	// Stats returns a snapshot of current cleaning statistics.
	Stats() CleanerStats

	// ResetStats resets all statistics counters to zero.
	ResetStats()
}
