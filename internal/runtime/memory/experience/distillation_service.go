// Package experience provides experience distillation service.
package experience

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Timwood0x10/ares/internal/errors"
	"github.com/Timwood0x10/ares/internal/llm"
	"github.com/Timwood0x10/ares/internal/storage/postgres"
	"github.com/Timwood0x10/ares/internal/storage/postgres/embedding"
	storage_models "github.com/Timwood0x10/ares/internal/storage/postgres/models"
	"github.com/Timwood0x10/ares/internal/storage/postgres/repositories"
)

// EmbeddingTask describes an async vector backfill for an already-created
// experience row. TaskID is the experience row id, i.e. the source row the
// vector will be written back to, per the queue's TaskID contract.
type EmbeddingTask struct {
	TaskID   string
	Content  string
	TenantID string
	Model    string
	Version  int
}

// EmbeddingEnqueuer asynchronously enqueues an embedding task so the embedding
// worker can write the vector back to the source row. Defined in the consuming
// package so DistillationService does not depend on the concrete postgres
// queue; the bootstrap wires a postgres-backed adapter (provide_distillation).
type EmbeddingEnqueuer interface {
	Enqueue(ctx context.Context, task *EmbeddingTask) error
}

// DistillationService provides experience distillation from task results.
// This service converts task execution logs into reusable experiences.
type DistillationService struct {
	llmClient         *llm.Client
	embeddingClient   *embedding.EmbeddingClient
	experienceRepo    repositories.ExperienceRepositoryInterface
	embeddingEnqueuer EmbeddingEnqueuer         // optional async backfill producer
	embeddingConfig   *postgres.EmbeddingConfig // optional; defaults applied when nil
	logger            *slog.Logger
}

// DistillationOption configures a DistillationService.
type DistillationOption func(*DistillationService)

// WithEmbeddingEnqueuer wires an async embedding producer. When set, Distill
// persists the experience row without a vector and enqueues a backfill task so
// the embedding worker writes the vector back asynchronously (REVIEW #13 A2).
// When unset (default), Distill embeds synchronously exactly as before, so
// SDK / zero-config callers observe unchanged behavior.
func WithEmbeddingEnqueuer(enqueuer EmbeddingEnqueuer) DistillationOption {
	return func(s *DistillationService) {
		s.embeddingEnqueuer = enqueuer
	}
}

// WithEmbeddingConfig supplies the storage embedding config so the async and
// synchronous paths stamp the configured embedding version instead of a
// hardcoded one. Defaults are used when unset.
func WithEmbeddingConfig(cfg *postgres.EmbeddingConfig) DistillationOption {
	return func(s *DistillationService) {
		s.embeddingConfig = cfg
	}
}

// NewDistillationService creates a new DistillationService instance.
// The optional options are applied in order; WithEmbeddingEnqueuer switches the
// service to async embedding (REVIEW #13 A2).
func NewDistillationService(
	llmClient *llm.Client,
	embeddingClient *embedding.EmbeddingClient,
	experienceRepo repositories.ExperienceRepositoryInterface,
	opts ...DistillationOption,
) *DistillationService {
	s := &DistillationService{
		llmClient:       llmClient,
		embeddingClient: embeddingClient,
		experienceRepo:  experienceRepo,
		logger:          slog.Default(),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// ShouldDistill checks if a task result should be distilled. Both successful
// and failed tasks are eligible: successful tasks yield success experiences,
// while failed tasks yield failure experiences. Only the content-length guards
// disqualify a task.
func (s *DistillationService) ShouldDistill(ctx context.Context, task *TaskResult) bool {
	if task == nil {
		return false
	}
	if len(task.Task) < 10 {
		return false
	}
	if len(task.Result) < 20 {
		return false
	}
	return true
}

// Distill extracts a reusable experience from a task result.
func (s *DistillationService) Distill(ctx context.Context, task *TaskResult) (*Experience, error) {
	if task == nil {
		return nil, errors.New("task result is nil")
	}
	if task.TenantID == "" {
		return nil, errors.New("tenant ID is required")
	}

	if !s.ShouldDistill(ctx, task) {
		return nil, errors.New("distillation skipped: task does not meet distillation criteria")
	}

	extracted, err := s.extractExperience(ctx, task)
	if err != nil {
		return nil, errors.Wrap(err, "extract experience")
	}

	if extracted.Problem == "" || extracted.Solution == "" {
		return nil, errors.New("invalid extracted experience")
	}

	expType := ExperienceTypeFailure
	if task.Success {
		expType = ExperienceTypeSuccess
	}

	exp := &storage_models.Experience{
		TenantID:    task.TenantID,
		Type:        expType,
		Problem:     extracted.Problem,
		Solution:    extracted.Solution,
		Constraints: extracted.Constraints,
		Input:       extracted.Problem,
		Output:      extracted.Solution,
		// Embedding/EmbeddingModel/EmbeddingVersion are set by the embed path
		// below, which differs depending on whether the async enqueuer is wired.
		Score:      0.0,
		Success:    task.Success,
		AgentID:    task.AgentID,
		UsageCount: 0,
		Metadata:   nil,
		CreatedAt:  time.Now(),
	}

	if s.embeddingEnqueuer != nil {
		// Async producer path. Persist the row first (embedding NULL), then
		// enqueue a backfill task so the embedding worker writes the vector back
		// asynchronously. This keeps the event subscriber loop from blocking on
		// the embedding network call.
		exp.EmbeddingModel = s.embeddingModel()
		exp.EmbeddingVersion = s.embeddingVersion()
		if err := s.experienceRepo.Create(ctx, exp); err != nil {
			return nil, errors.Wrap(err, "store experience")
		}
		if err := s.enqueueEmbeddingBackfill(ctx, exp, extracted.Problem); err != nil {
			// Logged because a permanently broken queue is otherwise
			// indistinguishable from a healthy one: the fallback below keeps
			// producing correct rows, only synchronously and slower.
			s.logger.Warn("enqueue embedding backfill failed, falling back to synchronous embed",
				"error", err,
				"experience_id", exp.ID,
				"tenant_id", exp.TenantID,
				"table", storage_models.ExperiencesTable)
			// Fall back to a synchronous embed+update so the row does not stay
			// without a vector until the reconciler picks it up.
			if backfillErr := s.backfillEmbedding(ctx, exp, extracted.Problem); backfillErr != nil {
				return nil, errors.Wrap(backfillErr, "backfill embedding after enqueue failure")
			}
		}
	} else {
		// Default (SDK / no enqueuer): embed synchronously and persist the
		// vector with the row, preserving pre-A2 behavior.
		if err := s.embedAndCreate(ctx, exp, extracted.Problem); err != nil {
			return nil, err
		}
	}

	return expToExperience(exp), nil
}

// enqueueEmbeddingBackfill enqueues an async embedding task for the given row.
func (s *DistillationService) enqueueEmbeddingBackfill(ctx context.Context, exp *storage_models.Experience, content string) error {
	task := &EmbeddingTask{
		TaskID:   exp.ID,
		Content:  content,
		TenantID: exp.TenantID,
		Model:    s.embeddingModel(),
		Version:  s.embeddingVersion(),
	}
	return s.embeddingEnqueuer.Enqueue(ctx, task)
}

// embed generates the vector for content and stamps the model/version fields on
// exp. Shared by both persistence paths so they cannot drift apart.
func (s *DistillationService) embed(ctx context.Context, exp *storage_models.Experience, content string) error {
	if s.embeddingClient == nil {
		return errors.New("embedding client is not available")
	}
	vec, err := s.embeddingClient.Embed(ctx, content)
	if err != nil {
		return errors.Wrap(err, "generate embedding")
	}
	exp.Embedding = vec
	exp.EmbeddingModel = s.embeddingModel()
	exp.EmbeddingVersion = s.embeddingVersion()
	return nil
}

// backfillEmbedding synchronously embeds content and writes the vector back to
// the row. Used as a fallback when enqueue fails.
//
// It updates only the embedding columns (not the whole row) because the async
// worker may be writing the same row concurrently: a full-row Update would
// clobber whatever it wrote.
func (s *DistillationService) backfillEmbedding(ctx context.Context, exp *storage_models.Experience, content string) error {
	if err := s.embed(ctx, exp, content); err != nil {
		return err
	}
	err := s.experienceRepo.UpdateEmbedding(
		ctx, exp.TenantID, exp.ID, exp.Embedding, exp.EmbeddingModel, exp.EmbeddingVersion)
	if err != nil {
		return errors.Wrap(err, "backfill experience embedding")
	}
	return nil
}

// embedAndCreate embeds content and persists the row with the vector (the
// synchronous default path).
func (s *DistillationService) embedAndCreate(ctx context.Context, exp *storage_models.Experience, content string) error {
	if err := s.embed(ctx, exp, content); err != nil {
		return err
	}
	if err := s.experienceRepo.Create(ctx, exp); err != nil {
		return errors.Wrap(err, "store experience")
	}
	return nil
}

// embeddingModel returns the embedding client's model name, or "" when nil.
func (s *DistillationService) embeddingModel() string {
	if s.embeddingClient == nil {
		return ""
	}
	return s.embeddingClient.GetModel()
}

// embeddingVersion returns the configured embedding schema version. It reads the
// storage embedding config instead of hardcoding 1 so a version bump does not
// have to be applied in every producer separately.
func (s *DistillationService) embeddingVersion() int {
	if s.embeddingConfig == nil {
		return postgres.DefaultEmbeddingConfig().DefaultVersion
	}
	return s.embeddingConfig.DefaultVersion
}

// expToExperience converts a stored experience into the returned domain
// experience.
func expToExperience(exp *storage_models.Experience) *Experience {
	return &Experience{
		ID:               exp.ID,
		TenantID:         exp.TenantID,
		Type:             exp.Type,
		Problem:          exp.Problem,
		Solution:         exp.Solution,
		Constraints:      exp.Constraints,
		Embedding:        exp.Embedding,
		EmbeddingModel:   exp.EmbeddingModel,
		EmbeddingVersion: exp.EmbeddingVersion,
		Score:            exp.Score,
		Success:          exp.Success,
		AgentID:          exp.AgentID,
		UsageCount:       exp.UsageCount,
		DecayAt:          exp.DecayAt,
		CreatedAt:        exp.CreatedAt,
	}
}

// DistillBatch distills multiple task results.
func (s *DistillationService) DistillBatch(ctx context.Context, tasks []*TaskResult) ([]*Experience, error) {
	if len(tasks) == 0 {
		return []*Experience{}, nil
	}

	experiences := make([]*Experience, 0, len(tasks))
	for _, task := range tasks {
		exp, err := s.Distill(ctx, task)
		if err != nil {
			s.logger.Error("Failed to distill task", "error", err)
			continue
		}
		if exp != nil {
			experiences = append(experiences, exp)
		}
	}

	return experiences, nil
}

// extractExperience extracts experience components using LLM.
func (s *DistillationService) extractExperience(ctx context.Context, task *TaskResult) (*ExtractedExperience, error) {
	if s.llmClient == nil || !s.llmClient.IsEnabled() {
		return nil, errors.New("LLM client is not available")
	}

	prompt := s.buildExtractionPrompt(task)

	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	response, err := s.llmClient.Generate(timeoutCtx, prompt)
	if err != nil {
		return nil, errors.Wrap(err, "LLM generation failed")
	}

	return s.parseExtractionResponse(response)
}

// buildExtractionPrompt builds the prompt for experience extraction.
func (s *DistillationService) buildExtractionPrompt(task *TaskResult) string {
	return fmt.Sprintf(`Extract a reusable experience from the task.

Task:
%s

Context:
%s

Result:
%s

Return:

Problem:
The core problem being solved.

Solution:
The concise solution approach.

Constraints:
Important constraints or context.

Keep each section short and concise.`,
		task.Task,
		task.Context,
		task.Result,
	)
}

// parseExtractionResponse parses the LLM response.
func (s *DistillationService) parseExtractionResponse(response string) (*ExtractedExperience, error) {
	lines := strings.Split(response, "\n")

	var problem, solution, constraints strings.Builder
	var currentSection string

	for _, line := range lines {
		trimmedLine := strings.TrimSpace(line)

		lowerLine := strings.ToLower(trimmedLine)
		switch {
		case strings.HasPrefix(lowerLine, "problem:"):
			currentSection = "problem"
			content := strings.TrimSpace(trimmedLine[8:])
			if content != "" {
				problem.WriteString(content)
			}
		case strings.HasPrefix(lowerLine, "solution:"):
			currentSection = "solution"
			content := strings.TrimSpace(trimmedLine[9:])
			if content != "" {
				solution.WriteString(content)
			}
		case strings.HasPrefix(lowerLine, "constraints:"):
			currentSection = "constraints"
			content := strings.TrimSpace(trimmedLine[12:])
			if content != "" {
				constraints.WriteString(content)
			}
		case trimmedLine != "":
			switch currentSection {
			case "problem":
				if problem.Len() > 0 {
					problem.WriteString(" ")
				}
				problem.WriteString(trimmedLine)
			case "solution":
				if solution.Len() > 0 {
					solution.WriteString(" ")
				}
				solution.WriteString(trimmedLine)
			case "constraints":
				if constraints.Len() > 0 {
					constraints.WriteString(" ")
				}
				constraints.WriteString(trimmedLine)
			}
		}
	}

	return &ExtractedExperience{
		Problem:     strings.TrimSpace(problem.String()),
		Solution:    strings.TrimSpace(solution.String()),
		Constraints: strings.TrimSpace(constraints.String()),
	}, nil
}

// ExtractedExperience represents extracted components.
type ExtractedExperience struct {
	Problem     string
	Solution    string
	Constraints string
}
