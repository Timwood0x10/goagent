package agentsyscall

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestKernelSeedIDSeq locks the grow-only contract of the post-restore
// counter seed: the sequence must jump to (at least) the restored max so the
// fresh boot cannot re-mint an ID family value the previous boot already
// wrote into the fabric, and a lower min must never move it backwards.
func TestKernelSeedIDSeq(t *testing.T) {
	k := NewKernel(nil, nil, nil, nil)
	k.idSeq.Store(3)

	k.SeedIDSeq(10)
	require.Equal(t, int64(10), k.idSeq.Load(),
		"seed must advance the sequence to the restored max")

	k.SeedIDSeq(4)
	require.Equal(t, int64(10), k.idSeq.Load(),
		"seed is grow-only: a lower min must be a no-op")
}
