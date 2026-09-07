package archive

import (
	"errors"
)

// Sentinel errors for the archive package. Callers may use errors.Is to
// classify failures (e.g. distinguish a missing round from a corrupt file).
var (
	// ErrInvalidRound indicates a round number that is not positive.
	ErrInvalidRound = errors.New("invalid round: must be > 0")
	// ErrInvalidAction indicates an action outside the allowed set.
	ErrInvalidAction = errors.New("invalid action: must be one of plan|implement|fix|review")
	// ErrInvalidIdentifier indicates a caller-supplied identifier that does not
	// match its declared protection pattern (e.g. a truncated commit hash).
	ErrInvalidIdentifier = errors.New("invalid identifier: does not match expected pattern")
	// ErrRoundNotFound indicates the requested round archive file does not exist.
	ErrRoundNotFound = errors.New("round not found")
	// ErrEmptyQuery indicates a search/recall query that is empty or whitespace-only.
	ErrEmptyQuery = errors.New("empty query")
	// ErrEmptyDir indicates an archive directory path that is empty.
	ErrEmptyDir = errors.New("archive directory must be non-empty")
	// ErrNoEvents indicates BuildRoundRecord was called with no events to summarize.
	ErrNoEvents = errors.New("no events to archive")
)
