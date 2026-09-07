package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Timwood0x10/ares/internal/logger"
)

// fileArchiveReader reads round_N.json files written by fileArchiveWriter.
// All methods are read-only and safe for concurrent use; the directory is
// accessed on each call so newly-written rounds are immediately visible.
type fileArchiveReader struct {
	dir string
	log *slog.Logger
}

// NewFileArchiveReader creates a reader for dir. Validates dir is non-empty
// (ErrEmptyDir). Does NOT require the dir to exist: Read/List/Search/Recall
// gracefully handle a missing or empty directory so a recall CLI can print a
// friendly "no archive" message before any round has been written.
//
// Args:
//   - dir: directory path for round_N.json files. Must be non-empty.
//
// Returns:
//   - *fileArchiveReader: a ready-to-use reader.
//   - error: ErrEmptyDir when dir is empty.
func NewFileArchiveReader(dir string) (*fileArchiveReader, error) {
	if dir == "" {
		return nil, fmt.Errorf("new archive reader: %w", ErrEmptyDir)
	}
	return &fileArchiveReader{
		dir: dir,
		log: logger.Module("archive.reader"),
	}, nil
}

// Read returns the record for the given round number.
//
// Round files may live either flat in the archive root (legacy single-stream
// layout) or under a per-stream subdirectory (REVIEW #50). Read searches the
// root first, then any subdirectory, returning the first match. When multiple
// streams share the same round number, use Search/Recall (which carry stream
// context in each record) instead.
//
// Returns:
//   - ErrInvalidRound (wrapped) when round <= 0.
//   - ErrRoundNotFound (wrapped) when the round file does not exist.
//   - a wrapped error on JSON unmarshal failure (no panic).
//   - ctx.Err() (wrapped) when the context is cancelled.
func (r *fileArchiveReader) Read(ctx context.Context, round int) (*RoundRecord, error) {
	if round <= 0 {
		return nil, fmt.Errorf("round %d: %w", round, ErrInvalidRound)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("read round %d: %w", round, err)
	}

	// Search the flat root first (legacy), then each stream subdirectory.
	name := fmt.Sprintf("round_%d.json", round)
	candidates := []string{filepath.Join(r.dir, name)}
	if entries, err := os.ReadDir(r.dir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				candidates = append(candidates, filepath.Join(r.dir, e.Name(), name))
			}
		}
	}

	for _, path := range candidates {
		data, err := os.ReadFile(path) //nolint:gosec // path is built from a validated integer round number + os.ReadDir entry names, not user input
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read round %d: %w", round, err)
		}
		var record RoundRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return nil, fmt.Errorf("unmarshal round %d: %w", round, err)
		}
		return &record, nil
	}
	return nil, fmt.Errorf("round %d: %w", round, ErrRoundNotFound)
}

// List returns all archived round numbers sorted ascending, across both the
// flat root (legacy) and every per-stream subdirectory (REVIEW #50). Round
// numbers that appear in multiple streams are de-duplicated.
//
// Corrupt filenames (e.g. "round_abc.json") are skipped with a debug log
// entry, never returned as errors. A missing or empty directory yields a
// nil slice and a nil error. Temporary files (round_N.json.tmp) are excluded
// by the glob pattern, so an in-flight write is never listed.
func (r *fileArchiveReader) List(ctx context.Context) ([]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("list rounds: %w", err)
	}

	// Glob the flat root plus one level of stream subdirectories.
	globs := []string{filepath.Join(r.dir, "round_*.json")}
	if entries, err := os.ReadDir(r.dir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				globs = append(globs, filepath.Join(r.dir, e.Name(), "round_*.json"))
			}
		}
	}

	// Use a nil slice so an empty/missing archive yields (nil, nil) rather
	// than ([]int{}, nil) — recall CLIs rely on this to print a friendly
	// "no archive yet" message.
	var rounds []int
	seen := make(map[int]bool)
	for _, g := range globs {
		matches, err := filepath.Glob(g)
		if err != nil {
			return nil, fmt.Errorf("list rounds: %w", err)
		}
		for _, m := range matches {
			n, ok := parseRoundFromName(m)
			if !ok {
				r.log.Debug("list: skipping unparseable filename", "file", m)
				continue
			}
			if seen[n] {
				continue
			}
			seen[n] = true
			rounds = append(rounds, n)
		}
	}
	sort.Ints(rounds)
	return rounds, nil
}

// Search returns records whose Summary, Decisions, Files[].Path,
// Files[].Summary, or Refs values contain the query (case-insensitive
// substring match). Results are sorted by round descending.
//
// Search scans every round file across the flat root and all per-stream
// subdirectories (REVIEW #50), so records from different streams that share a
// round number are all considered. A single corrupt round file is skipped
// (logged) rather than failing the whole search.
//
// Returns:
//   - ErrEmptyQuery (wrapped) when the query is empty or whitespace-only.
//   - a wrapped error on context failure.
func (r *fileArchiveReader) Search(ctx context.Context, query string) ([]RoundRecord, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, fmt.Errorf("search: %w", ErrEmptyQuery)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	needle := strings.ToLower(q)

	// Glob the flat root plus one level of stream subdirectories.
	globs := []string{filepath.Join(r.dir, "round_*.json")}
	if entries, err := os.ReadDir(r.dir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				globs = append(globs, filepath.Join(r.dir, e.Name(), "round_*.json"))
			}
		}
	}

	var matches []RoundRecord
	for _, g := range globs {
		files, err := filepath.Glob(g)
		if err != nil {
			return nil, fmt.Errorf("search: %w", err)
		}
		for _, path := range files {
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("search: %w", err)
			}
			data, err := os.ReadFile(path) //nolint:gosec // path comes from Glob over the archive dir, not user input
			if err != nil {
				r.log.Debug("search: skipping unreadable round file", "file", path, "error", err)
				continue
			}
			var rec RoundRecord
			if err := json.Unmarshal(data, &rec); err != nil {
				// A single corrupt round file must not fail the whole search
				// (REVIEW: reader.go:137). Skip it and continue.
				r.log.Debug("search: skipping corrupt round file", "file", path, "error", err)
				continue
			}
			if recordMatches(&rec, needle) {
				matches = append(matches, rec)
			}
		}
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Round > matches[j].Round
	})
	return matches, nil
}

// Recall returns a human-readable, multi-round conclusion string for the
// query. When no rounds match, it returns a friendly "no matches" message
// and a nil error.
//
// Each matching round is rendered as:
//
//	Round <N>: <Summary>
//	  Files: <comma-joined file paths>
//	  Verdict: vet=<GoVet or ""> lint=<GoLint or ""> test=<GoTest or "">
//	  Decisions: <joined decisions>
//	---
//
// The Files and Decisions lines are omitted when empty; the Verdict line is
// always present. Blocks are joined with newlines.
func (r *fileArchiveReader) Recall(ctx context.Context, query string) (string, error) {
	matches, err := r.Search(ctx, query)
	if err != nil {
		return "", fmt.Errorf("recall: %w", err)
	}
	if len(matches) == 0 {
		return fmt.Sprintf("no matching rounds found for query: %s", query), nil
	}

	var sb strings.Builder
	for i, m := range matches {
		if i > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(formatRecallBlock(m))
	}
	return sb.String(), nil
}

// recordMatches reports whether the record contains the lowercased needle in
// any of its searchable text fields: Summary, Decisions entries, file paths
// and file summaries, and Refs values.
func recordMatches(rec *RoundRecord, needle string) bool {
	if rec == nil {
		return false
	}
	if strings.Contains(strings.ToLower(rec.Summary), needle) {
		return true
	}
	for _, d := range rec.Decisions {
		if strings.Contains(strings.ToLower(d), needle) {
			return true
		}
	}
	for _, f := range rec.Files {
		if strings.Contains(strings.ToLower(f.Path), needle) {
			return true
		}
		if strings.Contains(strings.ToLower(f.Summary), needle) {
			return true
		}
	}
	for _, v := range rec.Refs {
		if strings.Contains(strings.ToLower(v), needle) {
			return true
		}
	}
	return false
}

// formatRecallBlock renders a single RoundRecord as the recall format. The
// Files line is omitted when no files were touched; the Decisions line is
// omitted when there are no decisions. The Verdict line is always present,
// with empty fields rendered as empty (e.g. "vet=" when GoVet is "").
func formatRecallBlock(m RoundRecord) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Round %d: %s\n", m.Round, m.Summary)

	if len(m.Files) > 0 {
		paths := make([]string, 0, len(m.Files))
		for _, f := range m.Files {
			paths = append(paths, f.Path)
		}
		fmt.Fprintf(&sb, "  Files: %s\n", strings.Join(paths, ", "))
	}

	fmt.Fprintf(&sb, "  Verdict: vet=%s lint=%s test=%s\n",
		m.Verdict.GoVet, m.Verdict.GoLint, m.Verdict.GoTest)

	if len(m.Decisions) > 0 {
		fmt.Fprintf(&sb, "  Decisions: %s\n", strings.Join(m.Decisions, "; "))
	}

	sb.WriteString("---")
	return sb.String()
}
