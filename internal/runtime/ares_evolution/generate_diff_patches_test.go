package evolution

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/runtime/evolution/diff"
	evogenome "github.com/Timwood0x10/ares/internal/runtime/evolution/genome"
	"github.com/Timwood0x10/ares/internal/runtime/evolution/patch"
)

// stubGenome is a test double for evogenome.Genome.
// It returns a fixed snapshot every call and a fixed list of children
// from Mutate. Counters record observed calls.
type stubGenome struct {
	name        string
	snapshot    any                // returned by every Snapshot call
	mutateErr   error              // when non-nil, Mutate returns this
	children    []evogenome.Genome // returned by Mutate (overrides default)
	snapCalls   int
	mutateCalls int
}

func (s *stubGenome) Name() string { return s.name }

func (s *stubGenome) Snapshot(ctx context.Context) (any, error) {
	s.snapCalls++
	return s.snapshot, nil
}

func (s *stubGenome) Mutate(ctx context.Context, n int) ([]evogenome.Genome, error) {
	s.mutateCalls++
	if s.mutateErr != nil {
		return nil, s.mutateErr
	}
	if s.children != nil {
		return s.children, nil
	}
	// Default: n child stub genomes, each with a distinct snapshot value.
	children := make([]evogenome.Genome, n)
	for i := range children {
		children[i] = &stubGenome{
			name:     s.name + "-child",
			snapshot: struct{ gen int }{gen: i + 100},
		}
	}
	return children, nil
}

// stubDiffer is a test double for diff.Differ.
type stubDiffer struct {
	name      string
	patchOut  []patch.RuntimePatch
	diffErr   error
	diffCalls int
}

func (d *stubDiffer) Name() string { return d.name }

func (d *stubDiffer) Diff(ctx context.Context, old, new any) ([]patch.RuntimePatch, error) {
	d.diffCalls++
	if d.diffErr != nil {
		return nil, d.diffErr
	}
	return d.patchOut, nil
}

// makeChildStub returns a stubGenome whose Snapshot returns the given value.
func makeChildStub(name string, snap any) *stubGenome {
	return &stubGenome{name: name, snapshot: snap}
}

// TestGenerateDiffPatches_CallsMutate verifies that generateDiffPatches
// actually invokes Mutate on each registered genome. Pre-fix this test
// would fail because the old implementation never called Mutate.
func TestGenerateDiffPatches_CallsMutate(t *testing.T) {
	ctx := context.Background()

	// Parent genome with a real snapshot value.
	g := &stubGenome{
		name:     "workflow",
		snapshot: struct{ gen int }{gen: 1},
	}
	// Differ returns one patch per Diff call.
	d := &stubDiffer{
		name:     "workflow",
		patchOut: []patch.RuntimePatch{{Target: "p1"}},
	}

	genomeReg := evogenome.NewRegistry()
	require.NoError(t, genomeReg.Register(g))
	diffReg := diff.NewRegistry()
	require.NoError(t, diffReg.Register(d))

	patches, err := generateDiffPatches(ctx, genomeReg, diffReg, 3, "strategy-1")
	require.NoError(t, err)

	// Parent Snapshot called once; each of 3 children Snapshot called once.
	assert.Equal(t, 1, g.snapCalls, "parent Snapshot should be called once")
	// Mutate called once on the parent.
	assert.Equal(t, 1, g.mutateCalls, "Mutate should be called once on parent")
	// 3 children → 3 Diff calls.
	assert.Equal(t, 3, d.diffCalls, "Diff should be called once per candidate")
	// 3 children × 1 patch each = 3 patches.
	assert.Len(t, patches, 3, "should produce one patch per mutated child")
}

// TestGenerateDiffPatches_ZeroChildrenReturnsError verifies the guard
// against invalid nChildren.
func TestGenerateDiffPatches_ZeroChildrenReturnsError(t *testing.T) {
	_, err := generateDiffPatches(context.Background(), evogenome.NewRegistry(), diff.NewRegistry(), 0, "strategy-1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nChildren must be > 0")
}

// TestGenerateDiffPatches_NegativeChildrenReturnsError verifies negative
// nChildren is also rejected.
func TestGenerateDiffPatches_NegativeChildrenReturnsError(t *testing.T) {
	_, err := generateDiffPatches(context.Background(), evogenome.NewRegistry(), diff.NewRegistry(), -1, "strategy-1")
	require.Error(t, err)
}

// TestGenerateDiffPatches_NilParentSnapshotSkipsGenome verifies that a
// genome whose parent Snapshot returns nil is silently skipped (no
// Mutate call, no Diff call).
func TestGenerateDiffPatches_NilParentSnapshotSkipsGenome(t *testing.T) {
	ctx := context.Background()

	// snapshot == nil → parent snap is nil → skip.
	g := &stubGenome{name: "workflow", snapshot: nil}
	d := &stubDiffer{name: "workflow"}

	genomeReg := evogenome.NewRegistry()
	require.NoError(t, genomeReg.Register(g))
	diffReg := diff.NewRegistry()
	require.NoError(t, diffReg.Register(d))

	_, err := generateDiffPatches(ctx, genomeReg, diffReg, 2, "strategy-1")
	require.NoError(t, err)
	assert.Equal(t, 0, g.mutateCalls, "Mutate should NOT be called when parent snapshot is nil")
	assert.Equal(t, 0, d.diffCalls, "Diff should NOT be called when parent snapshot is nil")
}

// TestGenerateDiffPatches_MutateErrorSkipsGenome verifies that a Mutate
// error causes the genome to be skipped but does NOT fail the whole call.
func TestGenerateDiffPatches_MutateErrorSkipsGenome(t *testing.T) {
	ctx := context.Background()

	g := &stubGenome{
		name:      "workflow",
		snapshot:  struct{ gen int }{gen: 1},
		mutateErr: errors.New("mutate boom"),
	}
	d := &stubDiffer{name: "workflow"}

	genomeReg := evogenome.NewRegistry()
	require.NoError(t, genomeReg.Register(g))
	diffReg := diff.NewRegistry()
	require.NoError(t, diffReg.Register(d))

	patches, err := generateDiffPatches(ctx, genomeReg, diffReg, 2, "strategy-1")
	require.NoError(t, err, "mutate error should be logged and skipped, not returned")
	assert.Empty(t, patches)
	assert.Equal(t, 0, d.diffCalls, "Diff should not be called when Mutate failed")
}

// TestGenerateDiffPatches_DiffErrorSkipsCandidate verifies that a Diff
// error for one candidate does not prevent other candidates from being diffed.
func TestGenerateDiffPatches_DiffErrorSkipsCandidate(t *testing.T) {
	ctx := context.Background()

	g := &stubGenome{
		name:     "workflow",
		snapshot: struct{ gen int }{gen: 1},
	}
	d := &stubDiffer{
		name:     "workflow",
		diffErr:  errors.New("diff boom"),
		patchOut: []patch.RuntimePatch{{Target: "should-not-reach"}},
	}

	genomeReg := evogenome.NewRegistry()
	require.NoError(t, genomeReg.Register(g))
	diffReg := diff.NewRegistry()
	require.NoError(t, diffReg.Register(d))

	patches, err := generateDiffPatches(ctx, genomeReg, diffReg, 2, "strategy-1")
	require.NoError(t, err)
	assert.Empty(t, patches, "all diffs failed, so no patches")
}

// TestGenerateDiffPatches_EmptyRegistriesReturnEmpty verifies the
// degenerate case of empty registries does not panic or error.
func TestGenerateDiffPatches_EmptyRegistriesReturnEmpty(t *testing.T) {
	patches, err := generateDiffPatches(
		context.Background(),
		evogenome.NewRegistry(),
		diff.NewRegistry(),
		3,
		"strategy-1",
	)
	require.NoError(t, err)
	assert.Empty(t, patches)
}

// TestGenerateDiffPatches_EmptyStrategyIDReturnsError verifies the
// attribution precondition: generateDiffPatches must fail fast instead of
// emitting unattributable patches, since an empty strategy cannot be A/B
// compared.
func TestGenerateDiffPatches_EmptyStrategyIDReturnsError(t *testing.T) {
	_, err := generateDiffPatches(
		context.Background(),
		evogenome.NewRegistry(),
		diff.NewRegistry(),
		3,
		"",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "strategyID must not be empty")
}

// TestGenerateDiffPatches_StampsStrategyID verifies every produced patch
// carries the supplied strategy ID so the coordinator can attribute outcomes.
func TestGenerateDiffPatches_StampsStrategyID(t *testing.T) {
	ctx := context.Background()

	g := &stubGenome{name: "workflow", snapshot: struct{ gen int }{gen: 1}}
	d := &stubDiffer{name: "workflow", patchOut: []patch.RuntimePatch{{Target: "p1"}, {Target: "p2"}}}

	genomeReg := evogenome.NewRegistry()
	require.NoError(t, genomeReg.Register(g))
	diffReg := diff.NewRegistry()
	require.NoError(t, diffReg.Register(d))

	patches, err := generateDiffPatches(ctx, genomeReg, diffReg, 2, "strategy-abc")
	require.NoError(t, err)
	assert.Len(t, patches, 4, "2 children × 2 patches each = 4")
	for i, p := range patches {
		assert.Equal(t, "strategy-abc", p.StrategyID, "patch %d must carry the strategy ID", i)
	}
}

// TestGenerateDiffPatches_MultipleGenomes verifies that multiple registered
// genomes each get Mutate + Diff, and patches are aggregated.
func TestGenerateDiffPatches_MultipleGenomes(t *testing.T) {
	ctx := context.Background()

	g1 := &stubGenome{name: "workflow", snapshot: struct{ gen int }{gen: 1}}
	g2 := &stubGenome{name: "scheduler", snapshot: struct{ gen int }{gen: 1}}
	d1 := &stubDiffer{name: "workflow", patchOut: []patch.RuntimePatch{{Target: "w1"}}}
	d2 := &stubDiffer{name: "scheduler", patchOut: []patch.RuntimePatch{{Target: "s1"}, {Target: "s2"}}}

	genomeReg := evogenome.NewRegistry()
	require.NoError(t, genomeReg.Register(g1))
	require.NoError(t, genomeReg.Register(g2))
	diffReg := diff.NewRegistry()
	require.NoError(t, diffReg.Register(d1))
	require.NoError(t, diffReg.Register(d2))

	patches, err := generateDiffPatches(ctx, genomeReg, diffReg, 2, "strategy-1")
	require.NoError(t, err)
	// g1: 2 children × 1 patch = 2; g2: 2 children × 2 patches = 4. Total 6.
	assert.Len(t, patches, 6, "patches from all genomes should be aggregated")
	assert.Equal(t, 1, g1.mutateCalls)
	assert.Equal(t, 1, g2.mutateCalls)
}

// keep imports used
var _ = makeChildStub
