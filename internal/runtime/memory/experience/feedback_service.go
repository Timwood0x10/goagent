// Package experience provides bandit feedback loop service for experience reinforcement.
package experience

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Timwood0x10/ares/internal/storage/postgres/repositories"
)

// FeedbackService provides bandit feedback loop for experience reinforcement.
// It closes the feedback loop by updating experience metrics based on task outcomes.
type FeedbackService struct {
	experienceRepo repositories.ExperienceRepositoryInterface
	logger         *slog.Logger
}

// NewFeedbackService creates a new FeedbackService instance.
//
// Args:
//
//	experienceRepo - repository for experience data access.
//
// Returns:
//
//	*FeedbackService - the configured FeedbackService instance.
func NewFeedbackService(experienceRepo repositories.ExperienceRepositoryInterface) *FeedbackService {
	return &FeedbackService{
		experienceRepo: experienceRepo,
		logger:         slog.Default(),
	}
}

// RecordSuccess records positive feedback for an used experience.
// This increments the usage count when a task succeeds with an experience.
//
// Args:
//
//	ctx - operation context.
//	experienceID - ID of the experience that was used.
//
// Returns:
//
//	error - nil on success, or error if the update fails.
func (s *FeedbackService) RecordSuccess(ctx context.Context, tenantID, experienceID string) error {
	if experienceID == "" {
		return nil
	}

	if err := s.experienceRepo.IncrementUsageCount(ctx, tenantID, experienceID); err != nil {
		s.logger.Error("Failed to increment usage count",
			"experience_id", experienceID,
			"error", err,
		)
		return fmt.Errorf("record success feedback: %w", err)
	}

	s.logger.Debug("Experience usage count incremented",
		"experience_id", experienceID,
	)

	return nil
}

// RecordFailure records negative feedback for an used experience.
// This decrements the rank (score) when a task fails with an experience.
//
// Args:
//
//	ctx - operation context.
//	experienceID - ID of the experience that was used.
//
// Returns:
//
//	error - nil on success, or error if the update fails.
func (s *FeedbackService) RecordFailure(ctx context.Context, tenantID, experienceID string) error {
	if experienceID == "" {
		return nil
	}

	if err := s.experienceRepo.DecrementRank(ctx, tenantID, experienceID); err != nil {
		s.logger.Error("Failed to decrement rank",
			"experience_id", experienceID,
			"error", err,
		)
		return fmt.Errorf("record failure feedback: %w", err)
	}

	s.logger.Debug("Experience rank decremented",
		"experience_id", experienceID,
	)

	return nil
}

// RecordFeedback records feedback based on task result.
// This is a convenience method that calls RecordSuccess or RecordFailure based on success flag.
//
// Args:
//
//	ctx - operation context.
//	experienceID - ID of the experience that was used.
//	success - whether the task was successful.
//
// Returns:
//
//	error - nil on success, or error if the update fails.
func (s *FeedbackService) RecordFeedback(ctx context.Context, tenantID, experienceID string, success bool) error {
	if experienceID == "" {
		return nil
	}

	if success {
		return s.RecordSuccess(ctx, tenantID, experienceID)
	}
	return s.RecordFailure(ctx, tenantID, experienceID)
}
