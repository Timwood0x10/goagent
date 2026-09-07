// Package distillation provides memory distillation functionality for agent experience extraction.
package distillation

import (
	"context"
	"math"

	"github.com/Timwood0x10/ares/internal/errors"
)

// ErrNoConflict is a sentinel error returned by DetectConflict when no conflicting memory is found.
// ErrNoConflict This allows callers to distinguish "no conflict" from "no error" when both the value and err would otherwise be nil.
var ErrNoConflict = errors.New("no conflict detected")

// ConflictResolver detects and resolves memory conflicts.
type ConflictResolver struct {
	repo              ExperienceRepository
	conflictThreshold float64
	searchLimit       int
}

// NewConflictResolver creates a new ConflictResolver.
func NewConflictResolver(repo ExperienceRepository) *ConflictResolver {
	return &ConflictResolver{
		repo:              repo,
		conflictThreshold: 0.85,
		searchLimit:       5,
	}
}

// NewConflictResolverWithConfig creates a new ConflictResolver with custom configuration.
func NewConflictResolverWithConfig(repo ExperienceRepository, conflictThreshold float64, searchLimit int) *ConflictResolver {
	return &ConflictResolver{
		repo:              repo,
		conflictThreshold: conflictThreshold,
		searchLimit:       searchLimit,
	}
}

// ResolveConflict determines the resolution strategy for a conflict.
//
// DetectConflict has already established that the two memories are
// near-duplicates (similarity > threshold), so exactly one of them should
// survive; keeping both would pollute retrieval with redundant entries.
// The survivor is chosen by confidence:
//   - new memory strictly more confident: ReplaceOld
//   - old memory strictly more confident: KeepOld (the incoming low-confidence
//     duplicate is discarded rather than overwriting a better fact)
//   - equal confidence: ReplaceOld, preferring the more recent observation
//
// Args:
//
//	newMemory - the new memory being added.
//	oldMemory - the conflicting existing memory.
//
// Returns:
//
//	ResolutionStrategy - the strategy to resolve the conflict.
func (r *ConflictResolver) ResolveConflict(newMemory *Experience, oldMemory *Experience) ResolutionStrategy {
	if oldMemory == nil {
		return ReplaceOld
	}
	if newMemory == nil {
		return KeepOld
	}

	if oldMemory.Confidence > newMemory.Confidence {
		return KeepOld
	}

	// Strictly higher, or a tie broken in favour of the newer observation.
	return ReplaceOld
}

// DetectConflict detects conflicts with existing memories.
// It searches for similar experiences using vector similarity and checks
// if any existing memory exceeds the conflict threshold.
//
// Args:
//
//	ctx - operation context.
//	vector - the embedding vector to search for similar memories.
//	tenantID - tenant ID for multi-tenancy.
//
// Returns:
//
//	*Experience - the conflicting memory, or nil if no conflict.
//	error - any error encountered.
func (r *ConflictResolver) DetectConflict(ctx context.Context, vector []float64, tenantID string) (*Experience, error) {
	if r.repo == nil {
		return nil, ErrNoConflict
	}

	if len(vector) == 0 {
		return nil, ErrNoConflict
	}

	similar, err := r.repo.SearchByVector(ctx, vector, tenantID, r.searchLimit)
	if err != nil {
		return nil, errors.Wrap(err, "failed to search for similar memories")
	}

	if len(similar) == 0 {
		return nil, ErrNoConflict
	}

	for i := range similar {
		if len(similar[i].Vector) == 0 {
			continue
		}
		similarity := r.cosineSimilarity(vector, similar[i].Vector)
		if similarity > r.conflictThreshold {
			return &similar[i], nil
		}
	}

	return nil, ErrNoConflict
}

// DetectConflictByExperience detects conflicts using an Experience struct.
// This is a convenience method that extracts the vector from the Experience
// and calls DetectConflict. It provides backward compatibility for callers
// that prefer to work with Experience structs.
//
// Args:
//
//	ctx - operation context.
//	exp - the experience to check for conflicts (must have a non-empty Vector field).
//	tenantID - tenant ID for multi-tenancy.
//
// Returns:
//
//	*Experience - the conflicting memory, or nil if no conflict.
//	error - any error encountered.
func (r *ConflictResolver) DetectConflictByExperience(ctx context.Context, exp *Experience, tenantID string) (*Experience, error) {
	if exp == nil {
		return nil, ErrNoConflict
	}
	if len(exp.Vector) == 0 {
		return nil, ErrNoConflict
	}
	return r.DetectConflict(ctx, exp.Vector, tenantID)
}

// cosineSimilarity calculates the cosine similarity between two vectors.
//
// Args:
//
//	v1 - first vector.
//	v2 - second vector.
//
// Returns:
//
//	float64 - similarity score between 0 and 1.
func (r *ConflictResolver) cosineSimilarity(v1, v2 []float64) float64 {
	if len(v1) != len(v2) || len(v1) == 0 {
		return 0.0
	}

	dotProduct := 0.0
	norm1 := 0.0
	norm2 := 0.0

	for i := range v1 {
		dotProduct += v1[i] * v2[i]
		norm1 += v1[i] * v1[i]
		norm2 += v2[i] * v2[i]
	}

	if norm1 == 0 || norm2 == 0 {
		return 0.0
	}

	// Optimization: Use single sqrt instead of two
	// math.Sqrt(norm1) * math.Sqrt(norm2) == math.Sqrt(norm1 * norm2)
	result := dotProduct / math.Sqrt(norm1*norm2)

	// Guard against NaN/Inf from degenerate vectors.
	if math.IsNaN(result) || math.IsInf(result, 0) {
		return 0.0
	}
	return result
}
