package archive

import "context"

// ArchiveWriter persists RoundRecords as independent round_N.json files.
// Implementations must be safe for concurrent use.
//
// The file-based implementation (NewFileArchiveWriter) writes atomically and
// rotates old rounds when MaxRounds is exceeded. Archive writes are best
// effort with respect to compaction: a write failure is logged but never
// blocks the compaction core.
type ArchiveWriter interface {
	// RecordRound writes round_N.json atomically. The record must Validate.
	// When MaxRounds is exceeded, the oldest rounds are deleted (rotation).
	// Returns a wrapped error on validation, marshalling, or I/O failure.
	RecordRound(ctx context.Context, record RoundRecord) error
	// Flush waits for any pending writes to complete. It is a no-op when the
	// implementation writes synchronously; it exists so callers can force a
	// durable flush before compaction discards the raw events.
	Flush(ctx context.Context) error
}

// ArchiveReader queries persisted round archives.
//
// The file-based implementation (NewFileArchiveReader) reads from the same
// directory written by ArchiveWriter. Read/List/Search/Recall are read-only
// and safe for concurrent use.
type ArchiveReader interface {
	// Read returns the record for the given round number.
	// Returns ErrInvalidRound when round <= 0 and ErrRoundNotFound when the
	// file does not exist.
	Read(ctx context.Context, round int) (*RoundRecord, error)
	// List returns all archived round numbers sorted ascending.
	// Corrupt filenames are skipped (logged), never returned as errors.
	List(ctx context.Context) ([]int, error)
	// Search returns records whose Summary/Decisions/Files/Refs contain the
	// query (case-insensitive). Results are sorted by round descending.
	// Returns ErrEmptyQuery when the query is empty or whitespace-only.
	Search(ctx context.Context, query string) ([]RoundRecord, error)
	// Recall returns a human-readable, multi-round conclusion string for the
	// query. When no rounds match, it returns a friendly "no matches" message
	// and a nil error.
	Recall(ctx context.Context, query string) (string, error)
}
