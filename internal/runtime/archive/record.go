package archive

import (
	"fmt"
)

// allowedActions is the set of round actions recognised by the archive.
// Order is stable so validation errors list them deterministically.
var allowedActions = map[string]bool{
	actionPlan:      true,
	actionImplement: true,
	actionFix:       true,
	actionReview:    true,
}

// RoundRecord is the independent per-round archive entry.
//
// Mirrors git-log-per-commit (not git-squash): rounds are never merged, so a
// later round can reference "round N's conclusion" rather than a compacted
// fragment. JSON tags match plan/context_compression_strategy.md section 3.1.
type RoundRecord struct {
	// Round is the 1-based round number. Must be > 0.
	//
	// Round numbers are assigned PER STREAM (each event stream restarts at 1),
	// so a bare round number is only unique within its StreamID. The archive
	// writer persists each stream's rounds under a per-stream subdirectory to
	// keep them from colliding on disk (see fileArchiveWriter.streamDir).
	Round int `json:"round"`
	// StreamID identifies the event stream this round belongs to. It scopes
	// the round number so that, e.g., stream A's round 1 and stream B's round 1
	// are stored independently instead of overwriting one another. Empty means
	// the round is unscoped (legacy / single-stream) and is stored flat in the
	// archive root. The value is sanitised before use as a directory name.
	StreamID string `json:"stream_id,omitempty"`
	// Action classifies the round: "plan" | "implement" | "fix" | "review".
	Action string `json:"action"`
	// Summary is a one-line description of what the round accomplished.
	Summary string `json:"summary"`
	// Files is the P1 structured file-change list for the round.
	Files []FileChange `json:"files"`
	// Verdict is the P2 verification state (conclusion only; raw output discarded).
	Verdict Verdict `json:"verdict"`
	// TODOs carries P5 todo / rollback notes, preserved verbatim.
	TODOs []string `json:"todos,omitempty"`
	// Decisions records P0 architecture decisions, preserved verbatim.
	Decisions []string `json:"decisions,omitempty"`
	// Refs holds P3 identifiers keyed by role (e.g. "commit" -> hash).
	// Values are preserved verbatim and must never be truncated.
	Refs map[string]string `json:"refs,omitempty"`
}

// FileChange is a P1 structured file-change entry.
type FileChange struct {
	// Path is the repository-relative file path.
	Path string `json:"path"`
	// LinesAdded is the number of lines added in this change.
	LinesAdded int `json:"lines_added"`
	// Summary is a one-line description of the change to this file.
	Summary string `json:"summary"`
}

// Verdict is the P2 verification state for a round.
//
// Each field uses "pass" | "fail" (and "skip" for GoTest when tests were
// explicitly skipped). An empty string means the corresponding check was not
// observed in the round. GoLint may hold "N issues" (e.g. "3 issues") or
// "pass" when zero issues were reported.
type Verdict struct {
	GoVet        string `json:"go_vet"`        // "pass" | "fail" | ""
	GoLint       string `json:"go_lint"`       // "N issues" | "pass" | ""
	GoTest       string `json:"go_test"`       // "pass" | "fail" | "skip" | ""
	RaceDetector string `json:"race_detector"` // "pass" | "fail" | ""
	Examples     string `json:"examples"`      // "pass" | "fail" | ""
}

// Validate checks the round record for required fields and valid ranges.
// It enforces the invariants that let later rounds trust an archive entry:
// a positive round number and a recognised action. An empty Summary is allowed
// (some rounds genuinely have nothing to summarise) but is not encouraged.
//
// Returns:
//   - ErrInvalidRound when Round <= 0.
//   - ErrInvalidAction when Action is not in the allowed set.
//   - nil when the record is valid.
func (r *RoundRecord) Validate() error {
	if r == nil {
		return ErrInvalidRound
	}
	if r.Round <= 0 {
		return fmt.Errorf("round %d: %w", r.Round, ErrInvalidRound)
	}
	if !allowedActions[r.Action] {
		return fmt.Errorf("action %q: %w", r.Action, ErrInvalidAction)
	}
	return nil
}
