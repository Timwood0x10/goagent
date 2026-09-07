package archive

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Potential bug scenarios covered by this file:
//  1. List returning unsorted — a non-sorted list would break recall
//     ordering and rotation selection. Tested by TestList_Sorted, which
//     writes rounds out of order and asserts ascending output.
//  2. Search matching inside a .tmp file — a partially-written record
//     could be matched and returned as a search hit. The glob pattern
//     "round_*.json" excludes .tmp files, so an in-flight write is never
//     read. Tested by TestList_ExcludesTmp (the .tmp is never listed, so
//     Search can never read it).
//  3. Read on corrupt JSON panicking — json.Unmarshal returns an error
//     rather than panicking on malformed input, and the reader wraps it.
//     Tested by TestRead_CorruptJSON, which asserts a non-nil wrapped
//     error and no panic.

func TestNewFileArchiveReader_InvalidDir(t *testing.T) {
	r, err := NewFileArchiveReader("")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEmptyDir)
	assert.Nil(t, r)
}

func TestNewFileArchiveReader_DoesNotRequireDirToExist(t *testing.T) {
	// A missing directory must not fail construction so a recall CLI can
	// print a friendly "no archive yet" message instead of erroring.
	r, err := NewFileArchiveReader(filepath.Join(t.TempDir(), "missing"))
	require.NoError(t, err)
	require.NotNil(t, r)
}

func TestRead_Existing(t *testing.T) {
	dir := t.TempDir()
	w, err := NewFileArchiveWriter(dir, 0)
	require.NoError(t, err)
	r, err := NewFileArchiveReader(dir)
	require.NoError(t, err)

	rec := RoundRecord{
		Round:     7,
		Action:    actionFix,
		Summary:   "fix the bug",
		Files:     []FileChange{{Path: "main.go", LinesAdded: 3, Summary: "patch"}},
		Verdict:   Verdict{GoVet: verdictPass, GoLint: "1 issues", GoTest: verdictPass},
		TODOs:     []string{"remove the workaround"},
		Decisions: []string{"chose option B"},
		Refs:      map[string]string{roleCommit: "abc1234"},
	}
	require.NoError(t, w.RecordRound(context.Background(), rec))

	got, err := r.Read(context.Background(), 7)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, rec, *got, "round-trip must preserve every field exactly")
}

func TestRead_Missing(t *testing.T) {
	dir := t.TempDir()
	r, err := NewFileArchiveReader(dir)
	require.NoError(t, err)

	_, err = r.Read(context.Background(), 99)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRoundNotFound)
}

func TestRead_InvalidRound(t *testing.T) {
	dir := t.TempDir()
	r, err := NewFileArchiveReader(dir)
	require.NoError(t, err)

	_, err = r.Read(context.Background(), 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRound)

	_, err = r.Read(context.Background(), -3)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidRound)
}

func TestRead_CorruptJSON(t *testing.T) {
	// Bug scenario 3: corrupt JSON must yield a wrapped error, never a panic.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(roundPath(dir, 1), []byte("{not valid json"), 0o644))

	r, err := NewFileArchiveReader(dir)
	require.NoError(t, err)

	var got *RoundRecord
	assert.NotPanics(t, func() {
		got, err = r.Read(context.Background(), 1)
	})
	require.Error(t, err, "corrupt JSON must return an error")
	assert.Nil(t, got, "no record must be returned on unmarshal failure")
	assert.Contains(t, err.Error(), "unmarshal round 1")
}

func TestRead_MissingDirectory(t *testing.T) {
	// Reading from a directory that does not exist must surface
	// ErrRoundNotFound, not a raw os error, so the CLI can print a clean
	// "no such round" message.
	r, err := NewFileArchiveReader(filepath.Join(t.TempDir(), "missing"))
	require.NoError(t, err)

	_, err = r.Read(context.Background(), 1)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRoundNotFound)
}

func TestList_Sorted(t *testing.T) {
	dir := t.TempDir()
	w, err := NewFileArchiveWriter(dir, 0)
	require.NoError(t, err)
	r, err := NewFileArchiveReader(dir)
	require.NoError(t, err)

	// Write out of order to prove List sorts the result.
	for _, n := range []int{3, 1, 2} {
		require.NoError(t, w.RecordRound(context.Background(),
			RoundRecord{Round: n, Action: actionImplement, Summary: "round"}))
	}

	got, err := r.List(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2, 3}, got)
}

func TestList_Empty(t *testing.T) {
	dir := t.TempDir()
	r, err := NewFileArchiveReader(dir)
	require.NoError(t, err)

	got, err := r.List(context.Background())
	require.NoError(t, err)
	assert.Nil(t, got, "an empty archive must return nil, nil")
}

func TestList_MissingDirectory(t *testing.T) {
	r, err := NewFileArchiveReader(filepath.Join(t.TempDir(), "missing"))
	require.NoError(t, err)

	got, err := r.List(context.Background())
	require.NoError(t, err)
	assert.Nil(t, got, "a missing archive dir must return nil, nil")
}

func TestList_CorruptFilename(t *testing.T) {
	// Bug scenario (defensive): a manually-placed "round_abc.json" file is
	// skipped, not crashed; valid rounds are still returned.
	dir := t.TempDir()
	w, err := NewFileArchiveWriter(dir, 0)
	require.NoError(t, err)
	r, err := NewFileArchiveReader(dir)
	require.NoError(t, err)

	require.NoError(t, w.RecordRound(context.Background(),
		RoundRecord{Round: 2, Action: actionImplement, Summary: "valid"}))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "round_abc.json"), []byte("{}"), 0o644))

	got, err := r.List(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []int{2}, got, "only the valid round must be returned")
}

func TestList_ExcludesTmp(t *testing.T) {
	// Bug scenario 2: a leftover .tmp file must never appear in List, so
	// Search cannot match a partial write.
	dir := t.TempDir()
	w, err := NewFileArchiveWriter(dir, 0)
	require.NoError(t, err)
	r, err := NewFileArchiveReader(dir)
	require.NoError(t, err)

	require.NoError(t, w.RecordRound(context.Background(),
		RoundRecord{Round: 1, Action: actionImplement, Summary: "real"}))
	// Manually place a .tmp file that should be ignored by List.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "round_2.json.tmp"),
		[]byte(`{"round":2,"action":"implement","summary":"tmp leak"}`), 0o644))

	got, err := r.List(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []int{1}, got, ".tmp files must be excluded from List")
}

func TestSearch_Substring(t *testing.T) {
	dir := t.TempDir()
	w, err := NewFileArchiveWriter(dir, 0)
	require.NoError(t, err)
	r, err := NewFileArchiveReader(dir)
	require.NoError(t, err)

	require.NoError(t, w.RecordRound(context.Background(), RoundRecord{
		Round:   1,
		Action:  actionReview,
		Summary: "HITL review of the PR",
	}))

	tests := []struct {
		name      string
		query     string
		wantMatch bool
	}{
		{"exact case matches", "HITL", true},
		{"lowercase query matches summary case-insensitively", "hitl", true},
		{"mixed case matches", "HiTl", true},
		{"non-matching query returns no hits", "nonexistent", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.Search(context.Background(), tt.query)
			require.NoError(t, err)
			if tt.wantMatch {
				require.Len(t, got, 1, "query %q must match round 1", tt.query)
				assert.Equal(t, 1, got[0].Round)
			} else {
				assert.Empty(t, got, "query %q must not match any round", tt.query)
			}
		})
	}
}

func TestSearch_MatchesAllFields(t *testing.T) {
	dir := t.TempDir()
	w, err := NewFileArchiveWriter(dir, 0)
	require.NoError(t, err)
	r, err := NewFileArchiveReader(dir)
	require.NoError(t, err)

	require.NoError(t, w.RecordRound(context.Background(), RoundRecord{
		Round:   1,
		Action:  actionImplement,
		Summary: "no keyword here",
		Files:   []FileChange{{Path: "unique_path.go", Summary: "touched"}},
	}))
	require.NoError(t, w.RecordRound(context.Background(), RoundRecord{
		Round:     2,
		Action:    actionImplement,
		Summary:   "also no keyword",
		Decisions: []string{"decided to use libx"},
	}))
	require.NoError(t, w.RecordRound(context.Background(), RoundRecord{
		Round:   3,
		Action:  actionImplement,
		Summary: "again no keyword",
		Refs:    map[string]string{roleCommit: "deadbeef99"},
	}))

	tests := []struct {
		name      string
		query     string
		wantRound int
	}{
		{"matches file path", "unique_path", 1},
		{"matches file summary", "touched", 1},
		{"matches decision", "libx", 2},
		{"matches ref value", "deadbeef99", 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.Search(context.Background(), tt.query)
			require.NoError(t, err)
			require.Len(t, got, 1, "query %q must match exactly one round", tt.query)
			assert.Equal(t, tt.wantRound, got[0].Round)
		})
	}
}

func TestSearch_EmptyQuery(t *testing.T) {
	dir := t.TempDir()
	r, err := NewFileArchiveReader(dir)
	require.NoError(t, err)

	tests := []string{"", "   ", "\t\n"}
	for _, q := range tests {
		got, err := r.Search(context.Background(), q)
		require.Error(t, err, "query %q must be rejected", q)
		assert.ErrorIs(t, err, ErrEmptyQuery)
		assert.Nil(t, got)
	}
}

func TestSearch_SortedDescending(t *testing.T) {
	dir := t.TempDir()
	w, err := NewFileArchiveWriter(dir, 0)
	require.NoError(t, err)
	r, err := NewFileArchiveReader(dir)
	require.NoError(t, err)

	// Three rounds that all match the query "feature".
	for _, n := range []int{1, 2, 3} {
		require.NoError(t, w.RecordRound(context.Background(), RoundRecord{
			Round:   n,
			Action:  actionImplement,
			Summary: "feature work",
		}))
	}

	got, err := r.Search(context.Background(), "feature")
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, []int{3, 2, 1}, []int{got[0].Round, got[1].Round, got[2].Round},
		"matches must be sorted by round descending")
}

func TestSearch_CancelledContext(t *testing.T) {
	dir := t.TempDir()
	r, err := NewFileArchiveReader(dir)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = r.Search(ctx, "anything")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestRecall_NoMatches(t *testing.T) {
	dir := t.TempDir()
	w, err := NewFileArchiveWriter(dir, 0)
	require.NoError(t, err)
	r, err := NewFileArchiveReader(dir)
	require.NoError(t, err)

	require.NoError(t, w.RecordRound(context.Background(), RoundRecord{
		Round:   1,
		Action:  actionImplement,
		Summary: "unrelated content",
	}))

	got, err := r.Recall(context.Background(), "xyz")
	require.NoError(t, err, "no matches must yield a friendly message, not an error")
	assert.Equal(t, "no matching rounds found for query: xyz", got)
}

func TestRecall_Formatted(t *testing.T) {
	dir := t.TempDir()
	w, err := NewFileArchiveWriter(dir, 0)
	require.NoError(t, err)
	r, err := NewFileArchiveReader(dir)
	require.NoError(t, err)

	// Two matching rounds: round 5 with Files + Decisions, round 3 with
	// neither. Both match the query "feature".
	require.NoError(t, w.RecordRound(context.Background(), RoundRecord{
		Round:     5,
		Action:    actionImplement,
		Summary:   "implement feature A",
		Files:     []FileChange{{Path: "a.go", LinesAdded: 10, Summary: "added"}},
		Verdict:   Verdict{GoVet: verdictPass, GoLint: verdictPass, GoTest: verdictPass},
		Decisions: []string{"chose feature A"},
	}))
	require.NoError(t, w.RecordRound(context.Background(), RoundRecord{
		Round:   3,
		Action:  actionImplement,
		Summary: "implement feature B",
		Verdict: Verdict{},
	}))

	got, err := r.Recall(context.Background(), "feature")
	require.NoError(t, err)

	expected := "Round 5: implement feature A\n" +
		"  Files: a.go\n" +
		"  Verdict: vet=pass lint=pass test=pass\n" +
		"  Decisions: chose feature A\n" +
		"---\n" +
		"Round 3: implement feature B\n" +
		"  Verdict: vet= lint= test=\n" +
		"---"
	assert.Equal(t, expected, got)
}

func TestRecall_EmptyQuery(t *testing.T) {
	dir := t.TempDir()
	r, err := NewFileArchiveReader(dir)
	require.NoError(t, err)

	_, err = r.Recall(context.Background(), "   ")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEmptyQuery)
}

func TestRecall_MissingDirectory(t *testing.T) {
	// Recall on a never-written archive must return the friendly "no
	// matches" message, not an error, so the recall CLI prints cleanly.
	r, err := NewFileArchiveReader(filepath.Join(t.TempDir(), "missing"))
	require.NoError(t, err)

	got, err := r.Recall(context.Background(), "anything")
	require.NoError(t, err)
	assert.Equal(t, "no matching rounds found for query: anything", got)
}
