// Package scoring provides cost-controlled scoring infrastructure for the
// evolution system, including strategy hashing, score caching, and tiered
// scorer pipelines.
package scoring

import (
	"fmt"
	"hash/fnv"
	"sort"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/mutation"
)

// ErrNilStrategy is returned when a nil strategy is passed to StrategyHash.
// Errors are defined in errors.go.

// StrategyHash computes a stable 64-bit hash for a strategy.
// Two strategies with the same params, prompt template, tool config,
// and model config will produce the same hash regardless of creation time or ID.
//
// Hash components (order matters for stability):
//   - Sorted params (key-value pairs, values formatted with %v). This covers
//     the tool whitelist (Params["tools"]) and model selection
//     (Params["model"]) — they are ordinary Params entries and need no
//     separate handling.
//   - PromptTemplate
//
// Metadata fields that are excluded from the hash:
//   - Score, ID, ParentID, Version, CreatedAt, MutationDesc,
//     Name, StrategyMutationType — these are metadata, not identity.
//
// Args:
//
//	s - the strategy to hash (must not be nil).
//
// Returns:
//
//	uint64 - the computed hash value.
//	error - non-nil if s is nil.
func StrategyHash(s *mutation.Strategy) (uint64, error) {
	if s == nil {
		return 0, ErrNilStrategy
	}

	if s.HashCached() {
		return s.HashValue(), nil
	}

	h := fnv.New64a()

	// Hash sorted params for order-independence.
	// Allocation is bounded by the HashCached() fast path above — only cache
	// misses reach this point, so the sorted key slice is allocated at most once
	// per unique strategy.
	keys := make([]string, 0, len(s.Params))
	for k := range s.Params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		v := s.Params[k]
		_, _ = fmt.Fprintf(h, "%s=%v|", k, v)
	}

	// Hash prompt template.
	_, _ = fmt.Fprintf(h, "prompt=%s|", s.PromptTemplate)

	hash := h.Sum64()
	s.SetHash(hash)
	return hash, nil
}
