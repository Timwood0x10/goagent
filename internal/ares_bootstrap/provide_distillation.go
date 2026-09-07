package ares_bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/llm"
	evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
	aresexp "github.com/Timwood0x10/ares/internal/runtime/memory/experience"
	"github.com/Timwood0x10/ares/internal/storage/postgres"
	"github.com/Timwood0x10/ares/internal/storage/postgres/embedding"
	storage_models "github.com/Timwood0x10/ares/internal/storage/postgres/models"
	"github.com/Timwood0x10/ares/internal/storage/postgres/repositories"
)

// defaultDistillTenant aligns distillation writes (event trigger) and GA hint
// reads (GuidanceProvider) in the single-tenant default configuration. It is
// sourced from ares_events.DefaultTenantID — the same value the sub-agent
// emitter writes into EventTaskCompleted/EventTaskFailed payloads — so both
// sides agree. The experience repository scopes every read by tenant_id, so a
// mismatch would silently starve the GA of hints.
const defaultDistillTenant = ares_events.DefaultTenantID

// provideDistillation constructs the experience distillation service and a
// GuidanceProvider that feeds distilled experiences back into the GA's
// experience-guided mutation. It is intentionally non-fatal: any failure
// (e.g. Postgres unreachable, LLM client of unexpected type) is returned as an
// error and the caller logs + skips, leaving the system running without
// distillation.
// distillationWiring bundles what provideDistillation builds. It exists so the
// embedding queue (and the config it was built with) can be handed to
// wireEmbeddingWorker instead of being constructed a second time from the same
// pool, which is what previously created two queue instances for one queue.
type distillationWiring struct {
	pool             *postgres.Pool
	embeddingClient  *embedding.EmbeddingClient
	experienceRepo   repositories.ExperienceRepositoryInterface
	service          *aresexp.DistillationService
	guidanceProvider evolution.GuidanceProvider
	embeddingQueue   *postgres.EmbeddingQueue
	embeddingConfig  *postgres.EmbeddingConfig
}

func provideDistillation(
	ctx context.Context,
	cfg *ares_config.Config,
	llmClientArg interface{},
) (*distillationWiring, error) {
	llmClient, ok := llmClientArg.(*llm.Client)
	if !ok {
		return nil, fmt.Errorf("distillation requires *llm.Client, got %T", llmClientArg)
	}

	pgCfg := &postgres.Config{
		Host:     cfg.Storage.Host,
		Port:     cfg.Storage.Port,
		User:     cfg.Storage.Username,
		Password: cfg.Storage.Password,
		Database: cfg.Storage.Database,
		SSLMode:  cfg.Storage.SSLMode,
	}
	pool, err := postgres.NewPool(pgCfg)
	if err != nil {
		return nil, fmt.Errorf("distillation: open postgres pool: %w", err)
	}

	timeout := time.Duration(cfg.Embedding.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	embClient := embedding.NewEmbeddingClient(cfg.Embedding.BaseURL, cfg.Embedding.Model, nil, timeout)

	expRepo := repositories.NewExperienceRepository(pool.GetDB())

	// Feed the embedding queue from the distillation path so the async worker
	// (wireEmbeddingWorker) backfills experience vectors instead of embedding
	// synchronously. The adapter bridges the consuming-package interface to the
	// concrete postgres queue; the queue is returned so the worker shares it.
	embCfg := postgres.DefaultEmbeddingConfig()
	embedQueue := postgres.NewEmbeddingQueue(pool, embCfg)
	distSvc := aresexp.NewDistillationService(llmClient, embClient, expRepo,
		aresexp.WithEmbeddingEnqueuer(postgresEmbeddingEnqueuer{queue: embedQueue}),
		aresexp.WithEmbeddingConfig(embCfg))

	guidProv := &evolution.FuncGuidanceProvider{
		HintsFunc: func(ctx context.Context, taskType string, limit int) ([]evolution.EvolutionHint, error) {
			if limit <= 0 {
				limit = 5
			}
			exps := fetchExperiences(ctx, expRepo, embClient, taskType, limit)
			hints := make([]evolution.EvolutionHint, 0, len(exps))
			for _, exp := range exps {
				hints = append(hints, experienceToHint(exp))
			}
			return hints, nil
		},
		// RecordStrategyOutcome persists the actual strategy result back into
		// the experience store (Track A write side). Previously the GA core
		// invoked RecordStrategyOutcome but the FuncGuidanceProvider treated a
		// nil RecordFunc as a successful no-op — the outcome was silently
		// dropped. Now it is written so the next round's mutation guidance can
		// read the outcome (Strategy → Experience → Guidance loop, Stage 4.3).
		RecordFunc: func(ctx context.Context, outcome evolution.StrategyOutcome) error {
			return recordStrategyOutcome(ctx, expRepo, outcome)
		},
	}

	return &distillationWiring{
		pool:             pool,
		embeddingClient:  embClient,
		experienceRepo:   expRepo,
		service:          distSvc,
		guidanceProvider: guidProv,
		embeddingQueue:   embedQueue,
		embeddingConfig:  embCfg,
	}, nil
}

// recordStrategyOutcome persists a GA strategy outcome as an experience so the
// Strategy → Experience → Guidance loop closes: the next mutation reads the
// recorded outcome via HintsForTask. Failures are returned (never swallowed),
// so a broken write side is visible instead of a silent no-op.
//
// Args:
//
//	ctx - cancellation context forwarded to the repository.
//	repo - the experience repository; must be non-nil.
//	outcome - the strategy outcome produced by the GA core.
//
// Returns:
//
//	err - repository or validation error; nil on success.
func recordStrategyOutcome(ctx context.Context, repo repositories.ExperienceRepositoryInterface, outcome evolution.StrategyOutcome) error {
	if repo == nil {
		return errors.New("record strategy outcome: experience repository is nil")
	}
	expType := "failure"
	if outcome.Success {
		expType = "success"
	}
	exp := &storage_models.Experience{
		TenantID: defaultDistillTenant,
		Type:     expType,
		// Problem/Input carry the task type so HintsForTask(taskType) retrieves
		// the outcome by the same key the GA queries hints with.
		Problem: outcome.TaskType,
		Input:   outcome.TaskType,
		// Solution/Output carry the strategy identity and achieved score.
		Solution: fmt.Sprintf("strategy=%s score=%.4f", outcome.StrategyID, outcome.Score),
		Output:   fmt.Sprintf("strategy=%s score=%.4f", outcome.StrategyID, outcome.Score),
		Score:    outcome.Score,
		Success:  outcome.Success,
		Metadata: map[string]interface{}{
			"strategy_id": outcome.StrategyID,
			"cost":        outcome.Cost,
		},
	}
	if err := repo.Create(ctx, exp); err != nil {
		return fmt.Errorf("record strategy outcome: %w", err)
	}
	log.Info("bootstrap: strategy outcome recorded to experience store",
		"strategy_id", outcome.StrategyID,
		"task_type", outcome.TaskType,
		"success", outcome.Success,
		"score", outcome.Score,
	)
	return nil
}

// fetchExperiences retrieves candidate experiences for the GA's hint lookup.
// It prefers semantic vector search (embedding the task type) and falls back to
// keyword search. Two tenant scopes are tried to tolerate single-tenant
// conventions (an explicit "default" tenant vs an empty tenant).
func fetchExperiences(
	ctx context.Context,
	repo repositories.ExperienceRepositoryInterface,
	emb *embedding.EmbeddingClient,
	taskType string,
	limit int,
) []*storage_models.Experience {
	for _, tenant := range []string{defaultDistillTenant, ""} {
		if emb != nil {
			if vec, e := emb.Embed(ctx, taskType); e == nil {
				if exps, e := repo.SearchByVector(ctx, vec, tenant, limit); e == nil && len(exps) > 0 {
					return exps
				}
			}
		}
		if exps, e := repo.SearchByKeyword(ctx, taskType, tenant, limit); e == nil && len(exps) > 0 {
			return exps
		}
	}
	return nil
}

// experienceToHint maps a stored experience into an evolution hint consumed by
// the GA's experience-guided mutator. Read fields are exp.Input (problem) and
// exp.Output (solution); constraints are lifted from metadata via GetConstraints.
func experienceToHint(exp *storage_models.Experience) evolution.EvolutionHint {
	confidence := exp.Score
	if confidence < 0 {
		confidence = 0
	}
	if confidence > 1 {
		confidence = 1
	}

	var constraints []string
	if c := exp.GetConstraints(); c != "" {
		constraints = strings.Split(c, "\n")
	}

	return evolution.EvolutionHint{
		ID:                  exp.ID,
		TaskType:            exp.Type,
		Problem:             exp.Input,
		Solution:            exp.Output,
		Constraints:         constraints,
		Confidence:          confidence,
		SourceExperienceIDs: []string{exp.ID},
	}
}

// HandleTaskCompletedForDistillation turns a task-completed/failed event into
// a distilled experience. The sub-agent emitter (and the SDK Runtime via
// Agent.Run) enriches these events with task text, result text, tenant_id, and
// the consumed experience ID, so the loop is live. A guard still applies:
// Distill requires a non-empty tenant_id plus task/result text of sufficient
// length, which holds for normally completed tasks and any failure whose error
// text is long enough to be useful.
//
// Exported so the SDK (sdk/distill_events.go) can reuse the exact production
// distillation payload extraction and content-length guards without duplicating
// the logic.
//
// Args:
//
//	ctx  - lifecycle/cancellation context forwarded to DistillationService.Distill.
//	svc  - the distillation service that consumes the event; must be non-nil.
//	ev   - the TaskCompleted/TaskFailed event; payload fields are read by key.
func HandleTaskCompletedForDistillation(ctx context.Context, svc *aresexp.DistillationService, ev *ares_events.Event) {
	p := ev.Payload

	taskText := stringField(p, ares_events.EventKeyTask)
	resultText := stringField(p, ares_events.EventKeyResult)
	tenantID := stringField(p, ares_events.EventKeyTenantID)
	agentID := stringField(p, "agent_id")
	usedExpID := stringField(p, ares_events.EventKeyUsedExperienceID)

	if tenantID == "" || len(taskText) < 10 || len(resultText) < 20 {
		log.Debug("bootstrap: distillation skipped — event payload lacks task/result/tenant content",
			"event_id", ev.ID, "type", ev.Type)
		return
	}

	taskResult := &aresexp.TaskResult{
		Task:             taskText,
		Result:           resultText,
		TenantID:         tenantID,
		AgentID:          agentID,
		UsedExperienceID: usedExpID,
		Success:          ev.Type == ares_events.EventTaskCompleted,
	}
	if _, err := svc.Distill(ctx, taskResult); err != nil {
		log.Warn("bootstrap: distillation on task completion failed", "error", err)
	}
}

// postgresEmbeddingEnqueuer adapts the consuming-package EmbeddingEnqueuer
// interface to the concrete postgres EmbeddingQueue so the distillation path can
// enqueue async experience-vector backfill tasks (REVIEW #13 A2).
type postgresEmbeddingEnqueuer struct {
	queue *postgres.EmbeddingQueue
}

func (e postgresEmbeddingEnqueuer) Enqueue(ctx context.Context, task *aresexp.EmbeddingTask) error {
	return e.queue.Enqueue(ctx, &postgres.EmbeddingTask{
		TaskID:   task.TaskID,
		Table:    storage_models.ExperiencesTable,
		Content:  task.Content,
		TenantID: task.TenantID,
		Model:    task.Model,
		Version:  task.Version,
	})
}

// stringField returns the first non-empty string value among the given keys.
func stringField(p map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := p[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
