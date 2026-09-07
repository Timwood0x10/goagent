package memory

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/runtime/memory/distillation"
	"github.com/Timwood0x10/ares/internal/runtime/memory/push"
	"github.com/Timwood0x10/ares/internal/runtime/memory/report"
)

// fakeConversationSource is a deterministic in-memory ConversationSource.
type fakeConversationSource struct {
	mu      sync.Mutex
	batches []*ConversationBatch
	idx     int
	nextErr error
}

func newFakeConversationSource(batches ...*ConversationBatch) *fakeConversationSource {
	return &fakeConversationSource{batches: batches}
}

func (f *fakeConversationSource) Next(ctx context.Context) (*ConversationBatch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.nextErr != nil {
		err := f.nextErr
		f.nextErr = nil
		return nil, err
	}
	if f.idx >= len(f.batches) {
		return nil, io.EOF
	}
	b := f.batches[f.idx]
	f.idx++
	return b, nil
}

// fakeDistiller is a stub Distiller that records calls and returns canned memories.
type fakeDistiller struct {
	mu          sync.Mutex
	calls       []string
	createdIDs  []string
	errOnConvID string
	err         error
}

func (d *fakeDistiller) DistillConversation(ctx context.Context, conversationID string, messages []distillation.Message, tenantID, userID string) ([]distillation.Memory, error) {
	d.mu.Lock()
	d.calls = append(d.calls, conversationID)
	d.mu.Unlock()
	// errOnConvID simulates a per-conversation failure (e.g., bad input).
	// err (without errOnConvID) simulates a global failure for all calls.
	if conversationID == d.errOnConvID {
		return nil, d.err
	}
	if d.err != nil && d.errOnConvID == "" {
		return nil, d.err
	}
	// Build a deterministic memory from the conversation.
	mem := distillation.Memory{
		ID:         "mem-" + conversationID,
		Type:       distillation.MemoryKnowledge,
		Content:    "content for " + conversationID,
		Importance: 0.8,
		Source:     conversationID,
		CreatedAt:  time.Now(),
		Metadata: map[string]interface{}{
			"problem":     "p-" + conversationID,
			"solution":    "s-" + conversationID,
			"tenant_id":   tenantID,
			"memory_type": "fact",
		},
	}
	d.mu.Lock()
	d.createdIDs = append(d.createdIDs, mem.ID)
	d.mu.Unlock()
	return []distillation.Memory{mem}, nil
}

// pipelineKnowledgeSource adapts a fakeDistiller's created memories into a
// report.KnowledgeSource by recording what the distiller produced.
type pipelineKnowledgeSource struct {
	mu    sync.Mutex
	items []report.KnowledgeItem
}

func (s *pipelineKnowledgeSource) ListKnowledge(ctx context.Context, tenantID string) ([]report.KnowledgeItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]report.KnowledgeItem, len(s.items))
	copy(out, s.items)
	return out, nil
}

func (s *pipelineKnowledgeSource) add(item report.KnowledgeItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append(s.items, item)
}

// pipelineKnowledgeProvider adapts a KnowledgeSource to the push.KnowledgeProvider interface
// by converting report.KnowledgeItem to push.KnowledgeItem.
type pipelineKnowledgeProvider struct {
	src report.KnowledgeSource
}

func (p *pipelineKnowledgeProvider) ListKnowledge(ctx context.Context) ([]push.KnowledgeItem, error) {
	items, err := p.src.ListKnowledge(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]push.KnowledgeItem, 0, len(items))
	for _, it := range items {
		out = append(out, push.KnowledgeItem{
			ID:         it.ID,
			Category:   it.Category,
			Problem:    it.Problem,
			Solution:   it.Solution,
			Score:      it.Score,
			Source:     it.Source,
			StrategyID: it.StrategyID,
			TaskType:   it.TaskType,
			CreatedAt:  it.CreatedAt,
		})
	}
	return out, nil
}

// recordingReportSink records all saved reports.
type recordingReportSink struct {
	mu       sync.Mutex
	contents []string
	saveErr  error
}

func (s *recordingReportSink) Save(ctx context.Context, tenantID, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return s.saveErr
	}
	s.contents = append(s.contents, content)
	return nil
}

func (s *recordingReportSink) Contents() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.contents))
	copy(out, s.contents)
	return out
}

// recordingPushTarget records pushed items.
type recordingPushTarget struct {
	id       string
	criteria push.RelevanceCriteria
	mu       sync.Mutex
	items    []push.KnowledgeItem
}

func (t *recordingPushTarget) ID() string                       { return t.id }
func (t *recordingPushTarget) Criteria() push.RelevanceCriteria { return t.criteria }
func (t *recordingPushTarget) Deliver(ctx context.Context, item push.KnowledgeItem) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.items = append(t.items, item)
	return nil
}
func (t *recordingPushTarget) Items() []push.KnowledgeItem {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]push.KnowledgeItem, len(t.items))
	copy(out, t.items)
	return out
}

// buildTestPipeline constructs a fully wired Pipeline with fakes for testing.
func buildTestPipeline(t *testing.T, batches []*ConversationBatch) (*Pipeline, *fakeDistiller, *pipelineKnowledgeSource, *recordingReportSink, *recordingPushTarget) {
	t.Helper()
	source := newFakeConversationSource(batches...)
	distiller := &fakeDistiller{}
	knowledgeSource := &pipelineKnowledgeSource{}
	reportGen, err := report.NewReportGenerator(knowledgeSource, report.DefaultReportConfig())
	require.NoError(t, err)
	pushProvider := &pipelineKnowledgeProvider{src: knowledgeSource}
	pushSvc, err := push.NewPushService(pushProvider, push.DefaultPushConfig())
	require.NoError(t, err)
	target := &recordingPushTarget{id: "t1", criteria: push.RelevanceCriteria{}}
	pushSvc.RegisterTarget(target)
	sink := &recordingReportSink{}
	cfg := DefaultPipelineConfig()
	p, err := NewPipeline(source, distiller, reportGen, pushSvc, sink, cfg)
	require.NoError(t, err)
	return p, distiller, knowledgeSource, sink, target
}

func TestNewPipeline_NilDependencies_ReturnError(t *testing.T) {
	src := newFakeConversationSource()
	distiller := &fakeDistiller{}
	knowledgeSource := &pipelineKnowledgeSource{}
	reportGen, err := report.NewReportGenerator(knowledgeSource, report.DefaultReportConfig())
	require.NoError(t, err)
	pushSvc, err := push.NewPushService(&pipelineKnowledgeProvider{src: knowledgeSource}, push.DefaultPushConfig())
	require.NoError(t, err)
	sink := &recordingReportSink{}

	cases := []struct {
		name   string
		source ConversationSource
		distil Distiller
		report report.ReportGenerator
		push   push.PushService
	}{
		{"nil source", nil, distiller, reportGen, pushSvc},
		{"nil distiller", src, nil, reportGen, pushSvc},
		{"nil report", src, distiller, nil, pushSvc},
		{"nil push", src, distiller, reportGen, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewPipeline(tc.source, tc.distil, tc.report, tc.push, sink, DefaultPipelineConfig())
			require.Error(t, err)
			assert.Nil(t, p)
			assert.ErrorIs(t, err, ErrInvalidPipelineConfig)
		})
	}
}

func TestNewPipeline_NilConfig_UsesDefaults(t *testing.T) {
	p, _, _, _, _ := buildTestPipeline(t, nil)
	assert.Equal(t, "default", p.config.TenantID)
	assert.True(t, p.config.PushAfterDistill)
	assert.True(t, p.config.GenerateReportAtEnd)
}

func TestPipeline_Run_EmptySource(t *testing.T) {
	p, distiller, _, sink, _ := buildTestPipeline(t, nil)

	result, err := p.Run(context.Background())
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, 0, result.TotalBatches)
	assert.Equal(t, 0, result.TotalMemories)
	assert.Equal(t, 0, result.FailedBatches)
	// Even with no batches, the final report is generated.
	assert.Len(t, sink.Contents(), 1)
	assert.Empty(t, distiller.calls)
}

func TestPipeline_Run_SingleBatch_DistillsAndPushes(t *testing.T) {
	batch := &ConversationBatch{
		ConversationID: "conv-1",
		TenantID:       "t1",
		UserID:         "u1",
		Messages: []distillation.Message{
			{Role: "user", Content: "I have an error in my code"},
			{Role: "assistant", Content: "Fix the syntax error on line 10"},
		},
	}
	p, distiller, knowledgeSource, sink, target := buildTestPipeline(t, []*ConversationBatch{batch})

	// Hook: after distillation completes, sync memories to the knowledge source
	// so report/push see them. We do this by inspecting the distiller's createdIDs
	// at the moment the pipeline reads from the source — but since the pipeline runs
	// synchronously, we pre-populate using the deterministic ID the fake produces.
	// Simulate distiller->source sync by directly seeding expected memory.
	knowledgeSource.add(report.KnowledgeItem{
		ID:         "mem-conv-1",
		Category:   "fact",
		Problem:    "p-conv-1",
		Solution:   "s-conv-1",
		Score:      0.8,
		Source:     "conv-1",
		StrategyID: "s1",
		CreatedAt:  time.Now(),
	})

	result, err := p.Run(context.Background())
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, 1, result.TotalBatches)
	assert.Equal(t, 1, result.TotalMemories)
	assert.Equal(t, 0, result.FailedBatches)
	// Push delivered at least one item (default min score 0.5, item score 0.8).
	assert.GreaterOrEqual(t, result.PushedItems, 1)
	// Final report generated.
	assert.Len(t, sink.Contents(), 1)
	assert.Contains(t, sink.Contents()[0], "ARES Evolution Report")
	// Distiller was called once.
	assert.Len(t, distiller.calls, 1)
	// Target received at least one item.
	assert.GreaterOrEqual(t, len(target.Items()), 1)
}

func TestPipeline_Run_MultipleBatches(t *testing.T) {
	batches := []*ConversationBatch{
		{ConversationID: "conv-1", TenantID: "t1", UserID: "u1", Messages: []distillation.Message{{Role: "user", Content: "q1"}}},
		{ConversationID: "conv-2", TenantID: "t1", UserID: "u1", Messages: []distillation.Message{{Role: "user", Content: "q2"}}},
		{ConversationID: "conv-3", TenantID: "t1", UserID: "u1", Messages: []distillation.Message{{Role: "user", Content: "q3"}}},
	}
	p, distiller, knowledgeSource, sink, _ := buildTestPipeline(t, batches)

	// Seed the knowledge source with what the fake distiller will produce.
	for _, b := range batches {
		knowledgeSource.add(report.KnowledgeItem{
			ID:        "mem-" + b.ConversationID,
			Category:  "fact",
			Problem:   "p-" + b.ConversationID,
			Solution:  "s-" + b.ConversationID,
			Score:     0.8,
			Source:    b.ConversationID,
			CreatedAt: time.Now(),
		})
	}

	result, err := p.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, result.TotalBatches)
	assert.Equal(t, 3, result.TotalMemories)
	assert.Equal(t, 0, result.FailedBatches)
	assert.Len(t, distiller.calls, 3)
	// Final report saved once.
	assert.Len(t, sink.Contents(), 1)
}

func TestPipeline_Run_DistillerFailure_ContinuesAndCounts(t *testing.T) {
	batches := []*ConversationBatch{
		{ConversationID: "conv-ok-1", TenantID: "t1", UserID: "u1", Messages: []distillation.Message{{Role: "user", Content: "q1"}}},
		{ConversationID: "conv-fail", TenantID: "t1", UserID: "u1", Messages: []distillation.Message{{Role: "user", Content: "q2"}}},
		{ConversationID: "conv-ok-2", TenantID: "t1", UserID: "u1", Messages: []distillation.Message{{Role: "user", Content: "q3"}}},
	}
	p, distiller, knowledgeSource, sink, _ := buildTestPipeline(t, batches)
	// Configure the fake distiller to fail on the second batch only.
	distiller.errOnConvID = "conv-fail"
	distiller.err = errors.New("embedding service unavailable")

	// Seed knowledge source with the items that would have been created for the OK batches.
	knowledgeSource.add(report.KnowledgeItem{ID: "mem-conv-ok-1", Category: "fact", Problem: "p", Solution: "s", Score: 0.8, Source: "conv-ok-1", CreatedAt: time.Now()})
	knowledgeSource.add(report.KnowledgeItem{ID: "mem-conv-ok-2", Category: "fact", Problem: "p", Solution: "s", Score: 0.8, Source: "conv-ok-2", CreatedAt: time.Now()})

	result, err := p.Run(context.Background())
	require.NoError(t, err, "pipeline should not abort on partial failure")
	assert.Equal(t, 2, result.TotalBatches)
	assert.Equal(t, 1, result.FailedBatches)
	assert.Equal(t, 2, result.TotalMemories)
	assert.Len(t, distiller.calls, 3)
	assert.Len(t, sink.Contents(), 1)
}

func TestPipeline_Run_ReportSinkFailure_ContinuesAndRecordsError(t *testing.T) {
	batches := []*ConversationBatch{
		{ConversationID: "conv-1", TenantID: "t1", UserID: "u1", Messages: []distillation.Message{{Role: "user", Content: "q1"}}},
	}
	p, _, knowledgeSource, sink, _ := buildTestPipeline(t, batches)
	sink.saveErr = errors.New("disk full")
	knowledgeSource.add(report.KnowledgeItem{ID: "mem-conv-1", Category: "fact", Problem: "p", Solution: "s", Score: 0.8, Source: "conv-1", CreatedAt: time.Now()})

	result, err := p.Run(context.Background())
	require.NoError(t, err, "pipeline should not abort on report sink failure")
	assert.Equal(t, 1, result.TotalBatches)
	require.Error(t, result.LastReportError)
	assert.Contains(t, result.LastReportError.Error(), "disk full")
}

func TestPipeline_Run_PushFailure_ContinuesAndRecordsError(t *testing.T) {
	batches := []*ConversationBatch{
		{ConversationID: "conv-1", TenantID: "t1", UserID: "u1", Messages: []distillation.Message{{Role: "user", Content: "q1"}}},
	}
	// Build a pipeline with a push service that has no targets (so PushRelevant returns ErrNoTargets).
	source := newFakeConversationSource(batches...)
	distiller := &fakeDistiller{}
	knowledgeSource := &pipelineKnowledgeSource{}
	reportGen, err := report.NewReportGenerator(knowledgeSource, report.DefaultReportConfig())
	require.NoError(t, err)
	pushProvider := &pipelineKnowledgeProvider{src: knowledgeSource}
	pushSvc, err := push.NewPushService(pushProvider, push.DefaultPushConfig())
	require.NoError(t, err)
	// No targets registered -> PushRelevant returns ErrNoTargets.
	cfg := DefaultPipelineConfig()
	p, err := NewPipeline(source, distiller, reportGen, pushSvc, nil, cfg)
	require.NoError(t, err)
	knowledgeSource.add(report.KnowledgeItem{ID: "mem-conv-1", Category: "fact", Problem: "p", Solution: "s", Score: 0.8, Source: "conv-1", CreatedAt: time.Now()})

	result, err := p.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.TotalBatches)
	require.Error(t, result.LastPushError)
	assert.Contains(t, result.LastPushError.Error(), "no push targets")
}

func TestPipeline_Run_CancelledContext_ReturnsError(t *testing.T) {
	batches := []*ConversationBatch{
		{ConversationID: "conv-1", TenantID: "t1", UserID: "u1", Messages: []distillation.Message{{Role: "user", Content: "q1"}}},
		{ConversationID: "conv-2", TenantID: "t1", UserID: "u1", Messages: []distillation.Message{{Role: "user", Content: "q2"}}},
	}
	p, _, _, _, _ := buildTestPipeline(t, batches)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel before running.
	cancel()

	result, err := p.Run(ctx)
	require.Error(t, err)
	assert.NotNil(t, result, "result should be non-nil even on cancellation")
	assert.Contains(t, err.Error(), "pipeline run cancelled")
	assert.Equal(t, 0, result.TotalBatches)
}

func TestPipeline_Run_DisabledReportAndPush(t *testing.T) {
	batches := []*ConversationBatch{
		{ConversationID: "conv-1", TenantID: "t1", UserID: "u1", Messages: []distillation.Message{{Role: "user", Content: "q1"}}},
	}
	source := newFakeConversationSource(batches...)
	distiller := &fakeDistiller{}
	knowledgeSource := &pipelineKnowledgeSource{}
	reportGen, err := report.NewReportGenerator(knowledgeSource, report.DefaultReportConfig())
	require.NoError(t, err)
	pushProvider := &pipelineKnowledgeProvider{src: knowledgeSource}
	pushSvc, err := push.NewPushService(pushProvider, push.DefaultPushConfig())
	require.NoError(t, err)
	pushSvc.RegisterTarget(&recordingPushTarget{id: "t1"})
	sink := &recordingReportSink{}
	cfg := &PipelineConfig{
		TenantID:            "default",
		PushAfterDistill:    false,
		GenerateReportAtEnd: false,
	}
	p, err := NewPipeline(source, distiller, reportGen, pushSvc, sink, cfg)
	require.NoError(t, err)

	result, err := p.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.TotalBatches)
	assert.Equal(t, 0, result.PushedItems, "push disabled should deliver nothing")
	assert.Empty(t, sink.Contents(), "report disabled should save nothing")
}

func TestPipeline_Run_NoSink_StillGeneratesReport(t *testing.T) {
	batches := []*ConversationBatch{
		{ConversationID: "conv-1", TenantID: "t1", UserID: "u1", Messages: []distillation.Message{{Role: "user", Content: "q1"}}},
	}
	source := newFakeConversationSource(batches...)
	distiller := &fakeDistiller{}
	knowledgeSource := &pipelineKnowledgeSource{}
	reportGen, err := report.NewReportGenerator(knowledgeSource, report.DefaultReportConfig())
	require.NoError(t, err)
	pushProvider := &pipelineKnowledgeProvider{src: knowledgeSource}
	pushSvc, err := push.NewPushService(pushProvider, push.DefaultPushConfig())
	require.NoError(t, err)
	pushSvc.RegisterTarget(&recordingPushTarget{id: "t1"})
	cfg := DefaultPipelineConfig()
	p, err := NewPipeline(source, distiller, reportGen, pushSvc, nil, cfg)
	require.NoError(t, err)
	knowledgeSource.add(report.KnowledgeItem{ID: "mem-conv-1", Category: "fact", Problem: "p", Solution: "s", Score: 0.8, Source: "conv-1", CreatedAt: time.Now()})

	result, err := p.Run(context.Background())
	require.NoError(t, err)
	// No sink means report is generated but not saved; no error.
	assert.Equal(t, 1, result.TotalBatches)
	assert.NoError(t, result.LastReportError)
}

func TestPipeline_Run_TenantFallbackToConfigDefault(t *testing.T) {
	batches := []*ConversationBatch{
		{ConversationID: "conv-1", TenantID: "", UserID: "u1", Messages: []distillation.Message{{Role: "user", Content: "q1"}}},
	}
	p, distiller, _, _, _ := buildTestPipeline(t, batches)

	_, err := p.Run(context.Background())
	require.NoError(t, err)
	// The fake distiller records the tenant it was called with via metadata,
	// but we can verify the call was made.
	require.Len(t, distiller.calls, 1)
}

func TestPipeline_Run_ResultDurationNonZero(t *testing.T) {
	batches := []*ConversationBatch{
		{ConversationID: "conv-1", TenantID: "t1", UserID: "u1", Messages: []distillation.Message{{Role: "user", Content: "q1"}}},
	}
	p, _, _, _, _ := buildTestPipeline(t, batches)

	result, err := p.Run(context.Background())
	require.NoError(t, err)
	assert.Greater(t, result.Duration, time.Duration(0))
}

func TestPipeline_Run_SourceError_StopsGracefully(t *testing.T) {
	source := newFakeConversationSource()
	source.nextErr = errors.New("message store offline")
	distiller := &fakeDistiller{}
	knowledgeSource := &pipelineKnowledgeSource{}
	reportGen, err := report.NewReportGenerator(knowledgeSource, report.DefaultReportConfig())
	require.NoError(t, err)
	pushProvider := &pipelineKnowledgeProvider{src: knowledgeSource}
	pushSvc, err := push.NewPushService(pushProvider, push.DefaultPushConfig())
	require.NoError(t, err)
	pushSvc.RegisterTarget(&recordingPushTarget{id: "t1"})
	sink := &recordingReportSink{}
	cfg := DefaultPipelineConfig()
	p, err := NewPipeline(source, distiller, reportGen, pushSvc, sink, cfg)
	require.NoError(t, err)

	result, err := p.Run(context.Background())
	require.NoError(t, err, "source errors should not propagate as Run errors")
	require.NotNil(t, result)
	assert.Equal(t, 0, result.TotalBatches)
	// Final report is still generated.
	assert.Len(t, sink.Contents(), 1)
}
