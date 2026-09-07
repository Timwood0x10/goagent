// Package memory provides unified memory management for the StyleAgent framework.
// This file implements the Pipeline coordinator that wires ExperienceStore →
// Distiller → ReportGenerator → PushService into a single end-to-end flow.
package memory

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/runtime/memory/distillation"
	"github.com/Timwood0x10/ares/internal/runtime/memory/push"
	"github.com/Timwood0x10/ares/internal/runtime/memory/report"
)

// ConversationBatch is a unit of distillation input: a conversation ID plus
// the messages that compose it, along with tenant and user scope.
type ConversationBatch struct {
	// ConversationID uniquely identifies the conversation.
	ConversationID string
	// TenantID is the tenant scope for multi-tenancy.
	TenantID string
	// UserID is the user that owns the conversation.
	UserID string
	// Messages are the conversation messages to distill.
	Messages []distillation.Message
}

// ConversationSource yields conversation batches for the pipeline to distill.
// Implementations may pull from a message store, an event subscription, or a
// test fixture. The source must be safe for concurrent use.
type ConversationSource interface {
	// Next returns the next conversation batch, or io.EOF when exhausted.
	// Implementations should respect ctx cancellation.
	//
	// Args:
	//   ctx - timeout and cancellation context.
	//
	// Returns:
	//   *ConversationBatch - the next batch, or nil if no more are available.
	//   error - context cancellation, source error, or io.EOF when exhausted.
	Next(ctx context.Context) (*ConversationBatch, error)
}

// Distiller is the interface the pipeline uses to drive distillation.
// It is satisfied by *distillation.Distiller but defined as an interface
// for testability and dependency injection.
type Distiller interface {
	// DistillConversation distills memories from a conversation.
	DistillConversation(ctx context.Context, conversationID string, messages []distillation.Message, tenantID, userID string) ([]distillation.Memory, error)
}

// PipelineReportSink consumes generated reports (e.g., writes them to a file or store).
type PipelineReportSink interface {
	// Save persists the generated report text.
	//
	// Args:
	//   ctx - timeout and cancellation context.
	//   tenantID - tenant scope for the report.
	//   content - the formatted report text.
	//
	// Returns:
	//   error - non-nil if persistence fails.
	Save(ctx context.Context, tenantID, content string) error
}

// PipelineConfig holds configuration for the Pipeline coordinator.
type PipelineConfig struct {
	// TenantID is the default tenant scope when a batch has no tenant.
	TenantID string
	// ReportInterval controls how often a report is generated.
	// 0 means generate one report at the end of each Run.
	ReportInterval time.Duration
	// PushAfterDistill controls whether PushRelevant is invoked after each distillation batch.
	PushAfterDistill bool
	// GenerateReportAtEnd controls whether a final report is generated when Run completes.
	GenerateReportAtEnd bool
}

// DefaultPipelineConfig returns a PipelineConfig with sensible defaults.
func DefaultPipelineConfig() *PipelineConfig {
	return &PipelineConfig{
		TenantID:            "default",
		ReportInterval:      0,
		PushAfterDistill:    true,
		GenerateReportAtEnd: true,
	}
}

// Pipeline coordinates the full ExperienceStore → Distiller → Report → Push flow.
// It depends only on interfaces, never on concrete types, and handles partial
// failures gracefully (logs and continues, never panics).
type Pipeline struct {
	source     ConversationSource
	distiller  Distiller
	reportGen  report.ReportGenerator
	pushSvc    push.PushService
	reportSink PipelineReportSink
	config     *PipelineConfig

	// Distillation metrics tracked across the run.
	mu            sync.Mutex
	totalBatches  int
	totalMemories int
	failedBatches int
	pushedItems   int
	startedAt     time.Time
}

// NewPipeline creates a new Pipeline coordinator.
//
// Args:
//
//	source - conversation source to drive distillation (must not be nil).
//	distiller - distiller interface (must not be nil).
//	reportGen - report generator (must not be nil).
//	pushSvc - push service (must not be nil).
//	reportSink - optional sink for persisting reports (may be nil).
//	config - pipeline configuration (nil uses defaults).
//
// Returns:
//
//	*Pipeline - the configured pipeline.
//	error - ErrInvalidPipelineConfig if a required dependency is nil.
func NewPipeline(
	source ConversationSource,
	distiller Distiller,
	reportGen report.ReportGenerator,
	pushSvc push.PushService,
	reportSink PipelineReportSink,
	config *PipelineConfig,
) (*Pipeline, error) {
	if source == nil {
		return nil, fmt.Errorf("pipeline: source is nil: %w", ErrInvalidPipelineConfig)
	}
	if distiller == nil {
		return nil, fmt.Errorf("pipeline: distiller is nil: %w", ErrInvalidPipelineConfig)
	}
	if reportGen == nil {
		return nil, fmt.Errorf("pipeline: report generator is nil: %w", ErrInvalidPipelineConfig)
	}
	if pushSvc == nil {
		return nil, fmt.Errorf("pipeline: push service is nil: %w", ErrInvalidPipelineConfig)
	}
	if config == nil {
		config = DefaultPipelineConfig()
	}
	return &Pipeline{
		source:     source,
		distiller:  distiller,
		reportGen:  reportGen,
		pushSvc:    pushSvc,
		reportSink: reportSink,
		config:     config,
	}, nil
}

// Run executes the full pipeline until the conversation source is exhausted
// or ctx is cancelled. Partial failures (a single batch failing to distill,
// push failures, report generation failures) are logged and do not abort
// the run. Returns nil on clean exhaustion, an error only on context cancellation
// or unrecoverable source errors.
//
// Args:
//
//	ctx - lifecycle context; cancelling stops the pipeline.
//
// Returns:
//
//	*PipelineRunResult - aggregate metrics (always non-nil, even on error).
//	error - non-nil only on context cancellation or unrecoverable source failure.
func (p *Pipeline) Run(ctx context.Context) (*PipelineRunResult, error) {
	p.mu.Lock()
	p.startedAt = time.Now()
	p.mu.Unlock()

	var lastReportErr error
	var lastPushErr error
	var lastReportTime time.Time

	for {
		if err := ctx.Err(); err != nil {
			break
		}

		batch, err := p.source.Next(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				slog.DebugContext(ctx, "[Pipeline] source exhausted")
			} else {
				slog.WarnContext(ctx, "[Pipeline] source error", "error", err)
			}
			break
		}
		if batch == nil {
			break
		}

		p.processBatch(ctx, batch)

		// Optionally push after each batch.
		if p.config.PushAfterDistill {
			if err := p.runPush(ctx); err != nil {
				lastPushErr = err
				slog.WarnContext(ctx, "[Pipeline] push after distill failed", "error", err)
			}
		}

		// Optionally generate a report on an interval.
		if p.config.ReportInterval > 0 {
			if lastReportTime.IsZero() || time.Since(lastReportTime) >= p.config.ReportInterval {
				if err := p.runReport(ctx); err != nil {
					lastReportErr = err
					slog.WarnContext(ctx, "[Pipeline] periodic report generation failed", "error", err)
				}
				lastReportTime = time.Now()
			}
		}
	}

	// Final push after source exhaustion.
	if p.config.PushAfterDistill {
		if err := p.runPush(ctx); err != nil {
			lastPushErr = err
			slog.WarnContext(ctx, "[Pipeline] final push failed", "error", err)
		}
	}

	// Final report generation.
	if p.config.GenerateReportAtEnd {
		if err := p.runReport(ctx); err != nil {
			lastReportErr = err
			slog.WarnContext(ctx, "[Pipeline] final report generation failed", "error", err)
		}
	}

	p.mu.Lock()
	result := &PipelineRunResult{
		TotalBatches:    p.totalBatches,
		TotalMemories:   p.totalMemories,
		FailedBatches:   p.failedBatches,
		PushedItems:     p.pushedItems,
		Duration:        time.Since(p.startedAt),
		LastReportError: lastReportErr,
		LastPushError:   lastPushErr,
	}
	p.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return result, fmt.Errorf("pipeline run cancelled: %w", err)
	}
	return result, nil
}

// processBatch distills a single conversation batch and updates pipeline metrics.
// Errors are logged and counted; the method never panics.
func (p *Pipeline) processBatch(ctx context.Context, batch *ConversationBatch) {
	tenantID := batch.TenantID
	if tenantID == "" {
		tenantID = p.config.TenantID
	}

	memories, err := p.distiller.DistillConversation(ctx, batch.ConversationID, batch.Messages, tenantID, batch.UserID)
	if err != nil {
		p.mu.Lock()
		p.failedBatches++
		p.mu.Unlock()
		slog.WarnContext(ctx, "[Pipeline] distillation failed for batch",
			"conversation_id", batch.ConversationID,
			"tenant_id", tenantID,
			"error", err)
		return
	}

	p.mu.Lock()
	p.totalBatches++
	p.totalMemories += len(memories)
	p.mu.Unlock()

	slog.InfoContext(ctx, "[Pipeline] batch distilled",
		"conversation_id", batch.ConversationID,
		"memories_created", len(memories))
}

// runPush invokes the push service to deliver relevant knowledge to targets.
// Errors are wrapped and returned but never panic.
func (p *Pipeline) runPush(ctx context.Context) error {
	res, err := p.pushSvc.PushRelevant(ctx)
	if err != nil {
		return fmt.Errorf("pipeline push: %w", err)
	}
	if res == nil {
		return nil
	}
	p.mu.Lock()
	p.pushedItems += res.Delivered
	p.mu.Unlock()
	return nil
}

// runReport generates a structured report, formats it, and (if a sink is
// configured) persists it. Errors are wrapped and returned but never panic.
func (p *Pipeline) runReport(ctx context.Context) error {
	rpt, err := p.reportGen.Generate(ctx, p.config.TenantID)
	if err != nil {
		return fmt.Errorf("pipeline report generate: %w", err)
	}
	if rpt == nil {
		return nil
	}

	content := rpt.Format()
	if p.reportSink != nil {
		if err := p.reportSink.Save(ctx, p.config.TenantID, content); err != nil {
			return fmt.Errorf("pipeline report save: %w", err)
		}
	}
	return nil
}

// PipelineRunResult aggregates metrics from a single Pipeline.Run invocation.
type PipelineRunResult struct {
	// TotalBatches is the number of conversation batches successfully distilled.
	TotalBatches int `json:"total_batches"`
	// TotalMemories is the total number of memories created across all batches.
	TotalMemories int `json:"total_memories"`
	// FailedBatches is the number of batches whose distillation failed.
	FailedBatches int `json:"failed_batches"`
	// PushedItems is the total number of knowledge items successfully pushed.
	PushedItems int `json:"pushed_items"`
	// Duration is the wall-clock duration of the run.
	Duration time.Duration `json:"duration"`
	// LastReportError, when non-nil, records the last report generation error.
	LastReportError error `json:"-"`
	// LastPushError, when non-nil, records the last push error.
	LastPushError error `json:"-"`
}
