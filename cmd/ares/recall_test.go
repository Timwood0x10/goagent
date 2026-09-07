// Package main — recall CLI integration tests.
//
// Tests the recall command's query and round subcommands end-to-end against
// a temp archive directory populated via NewFileArchiveWriter. The config is
// a minimal YAML that only sets memory.archive.dir; all other fields fall
// back to setDefaults so validation passes.
package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/runtime/archive"
)

// Potential bug scenarios tested below:
//  1. recall round on a non-existent round must NOT error — it prints a
//     friendly "not found" message. Covered by TestRecall_RoundNotFound.
//  2. recall query on a missing archive directory must print a friendly
//     message, not an error. Covered by TestRecall_QueryMissingDir.
//  3. recall round with a non-integer argument must return an error.
//     Covered by TestRecall_RoundInvalidArg.

// writeTestConfig writes a minimal YAML config that points the archive dir
// at the given path. Returns the config file path.
func writeTestConfig(t *testing.T, archiveDir string) string {
	t.Helper()
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "test-config.yaml")
	// The YAML only needs memory.archive.dir — setDefaults fills server,
	// llm, agents, etc. with valid defaults so Validate passes.
	yamlContent := "memory:\n  archive:\n    dir: " + archiveDir + "\n"
	require.NoError(t, os.WriteFile(cfgPath, []byte(yamlContent), 0o644))
	return cfgPath
}

// writeTestRound writes a single round record to the given archive dir.
func writeTestRound(t *testing.T, archiveDir string, rec archive.RoundRecord) {
	t.Helper()
	w, err := archive.NewFileArchiveWriter(archiveDir, 0)
	require.NoError(t, err)
	require.NoError(t, w.RecordRound(context.Background(), rec))
}

// withRecallConfig sets recallConfigPath for the duration of the test and
// restores the original value afterward.
func withRecallConfig(t *testing.T, cfgPath string) {
	t.Helper()
	original := recallConfigPath
	recallConfigPath = cfgPath
	t.Cleanup(func() { recallConfigPath = original })
}

func TestRecall_Round_PrintsJSON(t *testing.T) {
	archiveDir := t.TempDir()
	writeTestRound(t, archiveDir, archive.RoundRecord{
		Round:     1,
		Action:    "implement",
		Summary:   "implemented the feature",
		Decisions: []string{"chose option B"},
		Refs:      map[string]string{"commit": "abc1234"},
	})
	withRecallConfig(t, writeTestConfig(t, archiveDir))

	// runRecallRound prints JSON to stdout; we only verify it succeeds.
	err := runRecallRound("1")
	require.NoError(t, err)
}

func TestRecall_RoundNotFound(t *testing.T) {
	archiveDir := t.TempDir()
	// Write round 1 so the directory exists, but ask for round 99.
	writeTestRound(t, archiveDir, archive.RoundRecord{
		Round:   1,
		Action:  "implement",
		Summary: "exists",
	})
	withRecallConfig(t, writeTestConfig(t, archiveDir))

	// A missing round must NOT return an error — it prints a friendly message.
	err := runRecallRound("99")
	require.NoError(t, err)
}

func TestRecall_RoundInvalidArg(t *testing.T) {
	archiveDir := t.TempDir()
	withRecallConfig(t, writeTestConfig(t, archiveDir))

	err := runRecallRound("not-a-number")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid round number")

	err = runRecallRound("0")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be positive")

	err = runRecallRound("-5")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be positive")
}

func TestRecall_Query_MatchesExistingRound(t *testing.T) {
	archiveDir := t.TempDir()
	writeTestRound(t, archiveDir, archive.RoundRecord{
		Round:   1,
		Action:  "fix",
		Summary: "fix the broken test suite",
		Files:   []archive.FileChange{{Path: "main.go", LinesAdded: 3}},
	})
	withRecallConfig(t, writeTestConfig(t, archiveDir))

	// Query for a keyword in the summary — must succeed.
	err := runRecallQuery("test")
	require.NoError(t, err)
}

func TestRecall_Query_NoMatches(t *testing.T) {
	archiveDir := t.TempDir()
	writeTestRound(t, archiveDir, archive.RoundRecord{
		Round:   1,
		Action:  "implement",
		Summary: "unrelated content",
	})
	withRecallConfig(t, writeTestConfig(t, archiveDir))

	// A query that matches nothing must still succeed (prints "no matches").
	err := runRecallQuery("zzz-nonexistent")
	require.NoError(t, err)
}

func TestRecall_QueryMissingDir(t *testing.T) {
	// Point the archive dir at a path that does not exist.
	archiveDir := filepath.Join(t.TempDir(), "missing")
	withRecallConfig(t, writeTestConfig(t, archiveDir))

	// Must NOT error — prints a friendly "no archive directory" message.
	err := runRecallQuery("anything")
	require.NoError(t, err)
}

func TestRecall_ArchiveDisabled(t *testing.T) {
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "disabled.yaml")
	yamlContent := "memory:\n  archive:\n    enabled: false\n"
	require.NoError(t, os.WriteFile(cfgPath, []byte(yamlContent), 0o644))
	withRecallConfig(t, cfgPath)

	err := runRecallQuery("test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "archive is disabled")

	err = runRecallRound("1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "archive is disabled")
}

func TestRecall_EndToEnd_RecallReturnsArchivedContent(t *testing.T) {
	// Full end-to-end: write a round with known content, then read it back
	// via the archive reader (the same reader the recall command uses).
	// This verifies the round-trip integrity that recall depends on.
	archiveDir := t.TempDir()
	original := archive.RoundRecord{
		Round:     42,
		Action:    "review",
		Summary:   "reviewed the PR changes",
		Files:     []archive.FileChange{{Path: "auth.go", LinesAdded: 10, Summary: "added JWT"}},
		Verdict:   archive.Verdict{GoVet: "pass", GoLint: "pass", GoTest: "pass"},
		Decisions: []string{"approved the approach"},
		Refs:      map[string]string{"commit": "deadbeef"},
	}
	writeTestRound(t, archiveDir, original)

	reader, err := archive.NewFileArchiveReader(archiveDir)
	require.NoError(t, err)

	// Read back the specific round.
	got, err := reader.Read(context.Background(), 42)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, original, *got, "round-trip must preserve every field")

	// Recall (search) must find it by a keyword in the summary.
	out, err := reader.Recall(context.Background(), "PR")
	require.NoError(t, err)
	assert.Contains(t, out, "Round 42")
	assert.Contains(t, out, "reviewed the PR changes")
	assert.True(t, strings.Contains(out, "auth.go"), "recall output must list changed files")
}
