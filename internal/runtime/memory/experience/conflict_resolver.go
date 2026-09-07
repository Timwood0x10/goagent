// Package experience provides experience conflict resolution service.
package experience

import (
	"context"
	"fmt"
	"log/slog"
	"math"
)

// ConflictResolver provides lazy conflict resolution for experiences.
// This implements the principle: Store Simply, Retrieve Smartly.
type ConflictResolver struct {
	logger *slog.Logger
	// Problem similarity threshold for conflict detection
	problemSimilarityThreshold float64
}

// NewConflictResolver creates a new ConflictResolver instance.
// Args:
// Returns new ConflictResolver instance with default threshold.
func NewConflictResolver() *ConflictResolver {
	return &ConflictResolver{
		logger:                     slog.Default(),
		problemSimilarityThreshold: 0.9, // 90% similarity threshold
	}
}

// DetectConflictGroups groups experiences by problem similarity.
// This uses simple clustering O(K²) which is efficient for K=20.
// Args:
// ctx - operation context.
// experiences - experiences to group.
// Returns list of conflict groups.
func (c *ConflictResolver) DetectConflictGroups(ctx context.Context, experiences []*Experience) [][]*Experience {
	if len(experiences) <= 1 {
		return [][]*Experience{experiences}
	}

	groups := make([][]*Experience, 0)
	used := make(map[string]bool)

	// Simple clustering: O(K²)
	// For K=20, this is 400 comparisons which is < 0.1ms
	for i, exp1 := range experiences {
		if used[exp1.ID] {
			continue
		}

		// Create new group with this experience
		group := []*Experience{exp1}
		used[exp1.ID] = true

		// Find all experiences with similar problem.
		// Add to group if similar to any existing member (single-link clustering).
		for j := i + 1; j < len(experiences); j++ {
			exp2 := experiences[j]
			if used[exp2.ID] {
				continue
			}

			// Check similarity against all group members.
			// Add to group if similar to any existing member.
			shouldAdd := false
			for _, groupMember := range group {
				similarity := c.cosineSimilarity(exp2.Embedding, groupMember.Embedding)
				if similarity > c.problemSimilarityThreshold {
					shouldAdd = true
					break
				}
			}

			if shouldAdd {
				group = append(group, exp2)
				used[exp2.ID] = true
			}
		}

		groups = append(groups, group)
	}

	c.logger.Debug("Conflict groups detected",
		"total_experiences", len(experiences),
		"total_groups", len(groups),
		"similarity_threshold", c.problemSimilarityThreshold,
	)

	return groups
}

// Resolve resolves conflicts by selecting the best experience from each group.
// This uses the final score from ranking to determine the best experience.
// Args:
// ctx - operation context.
// rankedExperiences - ranked experiences with scores.
// Returns resolved experiences (one per conflict group).
func (c *ConflictResolver) Resolve(ctx context.Context, rankedExperiences []*RankedExperience) []*Experience {
	if len(rankedExperiences) <= 1 {
		return c.extractExperiences(rankedExperiences)
	}

	// Group experiences by problem similarity
	experiences := c.extractExperiences(rankedExperiences)
	groups := c.DetectConflictGroups(ctx, experiences)

	// Select best experience from each group
	resolved := make([]*Experience, 0, len(groups))

	for _, group := range groups {
		if len(group) == 0 {
			continue
		}

		// Find the experience with highest score in this group
		best := c.findBestInGroup(group, rankedExperiences)
		resolved = append(resolved, best)

		// Mark other experiences in group as conflict resolved
		c.markConflictResolved(group, best.ID, rankedExperiences)
	}

	c.logger.Debug("Conflicts resolved",
		"input_count", len(rankedExperiences),
		"group_count", len(groups),
		"output_count", len(resolved),
	)

	return resolved
}

// findBestInGroup finds the experience with highest score in a group.
// Args:
// group - group of experiences.
// rankedExperiences - all ranked experiences with scores.
// Returns the best experience in the group.
func (c *ConflictResolver) findBestInGroup(group []*Experience, rankedExperiences []*RankedExperience) *Experience {
	best := group[0]
	bestScore := c.getScore(best, rankedExperiences)

	for _, exp := range group[1:] {
		score := c.getScore(exp, rankedExperiences)
		if score > bestScore {
			best = exp
			bestScore = score
		}
	}

	return best
}

// getScore retrieves the score for an experience.
// Args:
// exp - experience to get score for.
// rankedExperiences - all ranked experiences with scores.
// Returns the experience score or 0 if not found.
func (c *ConflictResolver) getScore(exp *Experience, rankedExperiences []*RankedExperience) float64 {
	for _, ranked := range rankedExperiences {
		if ranked.Experience.ID == exp.ID {
			return ranked.FinalScore
		}
	}
	return 0.0
}

// markConflictResolved marks experiences as conflict resolved.
// Args:
// group - group of experiences.
// winnerID - ID of the winning experience.
// rankedExperiences - all ranked experiences to update.
func (c *ConflictResolver) markConflictResolved(group []*Experience, winnerID string, rankedExperiences []*RankedExperience) {
	for _, ranked := range rankedExperiences {
		for _, exp := range group {
			if ranked.Experience.ID == exp.ID {
				ranked.ConflictChecked = true
				ranked.ConflictResolved = (exp.ID == winnerID)
				break
			}
		}
	}
}

// extractExperiences extracts experiences from ranked experiences.
// Args:
// rankedExperiences - ranked experiences with scores.
// Returns list of experiences.
func (c *ConflictResolver) extractExperiences(rankedExperiences []*RankedExperience) []*Experience {
	experiences := make([]*Experience, len(rankedExperiences))
	for i, ranked := range rankedExperiences {
		experiences[i] = ranked.Experience
	}
	return experiences
}

// cosineSimilarity calculates cosine similarity between two vectors.
// Args:
// vec1 - first vector.
// vec2 - second vector.
// Returns cosine similarity (0.0 to 1.0).
func (c *ConflictResolver) cosineSimilarity(vec1, vec2 []float64) float64 {
	if len(vec1) != len(vec2) {
		slog.Warn("conflict resolver: vector dimension mismatch",
			"len1", len(vec1), "len2", len(vec2))
		return 0.0
	}

	var dotProduct, norm1, norm2 float64

	for i := 0; i < len(vec1); i++ {
		dotProduct += vec1[i] * vec2[i]
		norm1 += vec1[i] * vec1[i]
		norm2 += vec2[i] * vec2[i]
	}

	if norm1 == 0 || norm2 == 0 {
		return 0.0
	}

	return dotProduct / (math.Sqrt(norm1) * math.Sqrt(norm2))
}

// Configure updates the conflict resolver configuration.
// Args:
// problemSimilarityThreshold - similarity threshold for conflict detection.
// Returns error if threshold is invalid.
func (c *ConflictResolver) Configure(problemSimilarityThreshold float64) error {
	if problemSimilarityThreshold <= 0 || problemSimilarityThreshold > 1.0 {
		return fmt.Errorf("problem similarity threshold must be between 0 and 1, got %f", problemSimilarityThreshold)
	}

	c.problemSimilarityThreshold = problemSimilarityThreshold

	c.logger.Info("Conflict resolver configured",
		"similarity_threshold", c.problemSimilarityThreshold,
	)

	return nil
}
