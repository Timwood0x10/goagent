// Package push provides active knowledge recommendation to strategies.
package push

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Test constants for repeated test data
const (
	testCategoryFact  = "fact"
	testTemplateA     = "tpl-A"
	testStrategyOther = "other"
)

// fakeProvider is a deterministic in-memory KnowledgeProvider for tests.
type fakeProvider struct {
	mu      sync.Mutex
	items   []KnowledgeItem
	listErr error
}

func (f *fakeProvider) ListKnowledge(ctx context.Context) ([]KnowledgeItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]KnowledgeItem, len(f.items))
	copy(out, f.items)
	return out, nil
}

// recordingTarget records all delivered items. Thread-safe.
type recordingTarget struct {
	id             string
	criteria       RelevanceCriteria
	mu             sync.Mutex
	items          []KnowledgeItem
	deliverErr     error
	deliveredCount int64
}

func (t *recordingTarget) ID() string                  { return t.id }
func (t *recordingTarget) Criteria() RelevanceCriteria { return t.criteria }

func (t *recordingTarget) Deliver(ctx context.Context, item KnowledgeItem) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	atomic.AddInt64(&t.deliveredCount, 1)
	if t.deliverErr != nil {
		return t.deliverErr
	}
	t.items = append(t.items, item)
	return nil
}

func (t *recordingTarget) Items() []KnowledgeItem {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]KnowledgeItem, len(t.items))
	copy(out, t.items)
	return out
}

func samplePushItems() []KnowledgeItem {
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	return []KnowledgeItem{
		{ID: "i-1", Category: testCategoryFact, Score: 0.9, StrategyID: "s1", TaskType: "tt1", Problem: "p1", Solution: "s1", CreatedAt: base},
		{ID: "i-2", Category: testCategoryFact, Score: 0.8, StrategyID: "s2", TaskType: "tt2", Problem: "p2", Solution: "s2", CreatedAt: base},
		{ID: "i-3", Category: testCategoryFact, Score: 0.3, StrategyID: "s1", TaskType: "tt1", Problem: "p3", Solution: "s3", CreatedAt: base}, // below min score
		{ID: "i-4", Category: testCategoryFact, Score: 0.7, StrategyID: "s3", TaskType: "tt1", PromptTemplate: testTemplateA, Problem: "p4", Solution: "s4", CreatedAt: base},
	}
}

func TestNewPushService_NilProvider(t *testing.T) {
	s, err := NewPushService(nil, DefaultPushConfig())
	require.Error(t, err)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrInvalidConfig)
}

func TestNewPushService_InvalidMinScore(t *testing.T) {
	p := &fakeProvider{}
	s, err := NewPushService(p, &PushConfig{Policy: PolicyOnDemand, MinScore: 1.5})
	require.Error(t, err)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrInvalidConfig)
}

func TestNewPushService_ScheduledRequiresInterval(t *testing.T) {
	p := &fakeProvider{}
	s, err := NewPushService(p, &PushConfig{Policy: PolicyScheduled, Interval: 0})
	require.Error(t, err)
	assert.Nil(t, s)
	assert.ErrorIs(t, err, ErrInvalidConfig)
}

func TestNewPushService_NilConfig_UsesDefaults(t *testing.T) {
	p := &fakeProvider{items: samplePushItems()}
	s, err := NewPushService(p, nil)
	require.NoError(t, err)
	require.NotNil(t, s)
	assert.Equal(t, PolicyOnDemand, s.config.Policy)
	assert.Equal(t, 0.5, s.config.MinScore)
}

func TestRelevanceCriteria_IsEmpty(t *testing.T) {
	assert.True(t, RelevanceCriteria{}.IsEmpty())
	assert.False(t, RelevanceCriteria{StrategyID: "s1"}.IsEmpty())
	assert.False(t, RelevanceCriteria{TaskType: "tt"}.IsEmpty())
}

func TestMatchesCriteria_EmptyCriteriaMatchesAll(t *testing.T) {
	item := KnowledgeItem{ID: "x", StrategyID: "s1"}
	assert.True(t, matchesCriteria(RelevanceCriteria{}, item))
}

func TestMatchesCriteria_StrategyID(t *testing.T) {
	item := KnowledgeItem{ID: "x", StrategyID: "s1"}
	assert.True(t, matchesCriteria(RelevanceCriteria{StrategyID: "s1"}, item))
	assert.False(t, matchesCriteria(RelevanceCriteria{StrategyID: testStrategyOther}, item))
}

func TestMatchesCriteria_TaskType(t *testing.T) {
	item := KnowledgeItem{ID: "x", TaskType: "tt1"}
	assert.True(t, matchesCriteria(RelevanceCriteria{TaskType: "tt1"}, item))
	assert.False(t, matchesCriteria(RelevanceCriteria{TaskType: testStrategyOther}, item))
}

func TestMatchesCriteria_PromptTemplate(t *testing.T) {
	item := KnowledgeItem{ID: "x", PromptTemplate: testTemplateA}
	assert.True(t, matchesCriteria(RelevanceCriteria{PromptTemplate: testTemplateA}, item))
	assert.False(t, matchesCriteria(RelevanceCriteria{PromptTemplate: "tpl-B"}, item))
}

func TestMatchesCriteria_EvidenceKey(t *testing.T) {
	item := KnowledgeItem{ID: "x", EvidenceKey: "ek-1"}
	assert.True(t, matchesCriteria(RelevanceCriteria{EvidenceKey: "ek-1"}, item))
	assert.False(t, matchesCriteria(RelevanceCriteria{EvidenceKey: "ek-other"}, item))
}

func TestMatchesCriteria_MultipleFieldsAllMustMatch(t *testing.T) {
	item := KnowledgeItem{ID: "x", StrategyID: "s1", TaskType: "tt1"}
	criteria := RelevanceCriteria{StrategyID: "s1", TaskType: "tt1"}
	assert.True(t, matchesCriteria(criteria, item))

	criteria = RelevanceCriteria{StrategyID: "s1", TaskType: testStrategyOther}
	assert.False(t, matchesCriteria(criteria, item))
}

func TestPushRelevant_NoTargets_ReturnsError(t *testing.T) {
	p := &fakeProvider{items: samplePushItems()}
	s, err := NewPushService(p, DefaultPushConfig())
	require.NoError(t, err)

	r, err := s.PushRelevant(context.Background())
	require.Error(t, err)
	assert.Nil(t, r)
	assert.ErrorIs(t, err, ErrNoTargets)
}

func TestPushRelevant_ProviderError(t *testing.T) {
	p := &fakeProvider{listErr: errors.New("db down")}
	s, err := NewPushService(p, DefaultPushConfig())
	require.NoError(t, err)
	s.RegisterTarget(&recordingTarget{id: "t1"})

	r, err := s.PushRelevant(context.Background())
	require.Error(t, err)
	assert.Nil(t, r)
	assert.Contains(t, err.Error(), "list knowledge")
}

func TestPushRelevant_CancelledContext(t *testing.T) {
	p := &fakeProvider{items: samplePushItems()}
	s, err := NewPushService(p, DefaultPushConfig())
	require.NoError(t, err)
	s.RegisterTarget(&recordingTarget{id: "t1"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, err := s.PushRelevant(ctx)
	require.Error(t, err)
	assert.Nil(t, r)
}

func TestPushRelevant_EmptyCriteriaTargetReceivesAllEligible(t *testing.T) {
	p := &fakeProvider{items: samplePushItems()}
	s, err := NewPushService(p, DefaultPushConfig())
	require.NoError(t, err)

	target := &recordingTarget{id: "all", criteria: RelevanceCriteria{}}
	s.RegisterTarget(target)

	r, err := s.PushRelevant(context.Background())
	require.NoError(t, err)
	// Default min score 0.5: items i-1 (0.9), i-2 (0.8), i-4 (0.7) eligible; i-3 (0.3) skipped.
	assert.Equal(t, 3, r.Delivered)
	assert.Equal(t, 1, r.Skipped)
	assert.Equal(t, 0, r.Failed)
	assert.Len(t, target.Items(), 3)
}

func TestPushRelevant_StrategyFilter(t *testing.T) {
	p := &fakeProvider{items: samplePushItems()}
	s, err := NewPushService(p, DefaultPushConfig())
	require.NoError(t, err)

	target := &recordingTarget{id: "s1-target", criteria: RelevanceCriteria{StrategyID: "s1"}}
	s.RegisterTarget(target)

	r, err := s.PushRelevant(context.Background())
	require.NoError(t, err)
	// Only i-1 has StrategyID=s1 and score >= 0.5.
	assert.Equal(t, 1, r.Delivered)
	require.Len(t, target.Items(), 1)
	assert.Equal(t, "i-1", target.Items()[0].ID)
}

func TestPushRelevant_TaskTypeFilter(t *testing.T) {
	p := &fakeProvider{items: samplePushItems()}
	s, err := NewPushService(p, DefaultPushConfig())
	require.NoError(t, err)

	target := &recordingTarget{id: "tt1-target", criteria: RelevanceCriteria{TaskType: "tt1"}}
	s.RegisterTarget(target)

	r, err := s.PushRelevant(context.Background())
	require.NoError(t, err)
	// i-1 (s1, tt1, 0.9), i-4 (s3, tt1, 0.7) match; i-3 (s1, tt1, 0.3) below min score.
	assert.Equal(t, 2, r.Delivered)
	require.Len(t, target.Items(), 2)
}

func TestPushRelevant_PromptTemplateFilter(t *testing.T) {
	p := &fakeProvider{items: samplePushItems()}
	s, err := NewPushService(p, DefaultPushConfig())
	require.NoError(t, err)

	target := &recordingTarget{id: "tpl-target", criteria: RelevanceCriteria{PromptTemplate: "tpl-A"}}
	s.RegisterTarget(target)

	r, err := s.PushRelevant(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, r.Delivered)
	require.Len(t, target.Items(), 1)
	assert.Equal(t, "i-4", target.Items()[0].ID)
}

func TestPushRelevant_MaxItemsPerTargetCap(t *testing.T) {
	p := &fakeProvider{items: samplePushItems()}
	cfg := &PushConfig{Policy: PolicyOnDemand, MinScore: 0.5, MaxItemsPerTarget: 1}
	s, err := NewPushService(p, cfg)
	require.NoError(t, err)

	target := &recordingTarget{id: "capped", criteria: RelevanceCriteria{}}
	s.RegisterTarget(target)

	r, err := s.PushRelevant(context.Background())
	require.NoError(t, err)
	// Only 1 item delivered (cap), others skipped.
	assert.Equal(t, 1, r.Delivered)
	// 3 eligible items, 1 delivered, 2 skipped due to cap; 1 skipped due to low score.
	assert.Equal(t, 3, r.Skipped)
}

func TestPushRelevant_DeliveryFailureRecorded(t *testing.T) {
	p := &fakeProvider{items: samplePushItems()}
	s, err := NewPushService(p, DefaultPushConfig())
	require.NoError(t, err)

	target := &recordingTarget{id: "failing", criteria: RelevanceCriteria{}, deliverErr: errors.New("target offline")}
	s.RegisterTarget(target)

	r, err := s.PushRelevant(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, r.Delivered)
	assert.Equal(t, 3, r.Failed)
	assert.Len(t, r.Results, 3)
	for _, res := range r.Results {
		assert.False(t, res.Delivered)
		assert.Contains(t, res.Error, "target offline")
	}
}

func TestPushRelevant_MultipleTargetsDeliveredIndependently(t *testing.T) {
	p := &fakeProvider{items: samplePushItems()}
	s, err := NewPushService(p, DefaultPushConfig())
	require.NoError(t, err)

	t1 := &recordingTarget{id: "t1", criteria: RelevanceCriteria{StrategyID: "s1"}}
	t2 := &recordingTarget{id: "t2", criteria: RelevanceCriteria{StrategyID: "s2"}}
	s.RegisterTarget(t1)
	s.RegisterTarget(t2)

	r, err := s.PushRelevant(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, r.Delivered)
	assert.Len(t, t1.Items(), 1)
	assert.Equal(t, "i-1", t1.Items()[0].ID)
	assert.Len(t, t2.Items(), 1)
	assert.Equal(t, "i-2", t2.Items()[0].ID)
}

func TestPushRelevant_DeterministicTargetOrder(t *testing.T) {
	p := &fakeProvider{items: samplePushItems()}
	s, err := NewPushService(p, DefaultPushConfig())
	require.NoError(t, err)

	// Register out of order; results should be ordered by ID.
	s.RegisterTarget(&recordingTarget{id: "z-target", criteria: RelevanceCriteria{}})
	s.RegisterTarget(&recordingTarget{id: "a-target", criteria: RelevanceCriteria{}})

	r, err := s.PushRelevant(context.Background())
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(r.Results), 2)
	// Targets are sorted by ID; a-target appears before z-target regardless of registration order.
	firstATargetIdx := -1
	firstZTargetIdx := -1
	for i, res := range r.Results {
		if res.TargetID == "a-target" && firstATargetIdx == -1 {
			firstATargetIdx = i
		}
		if res.TargetID == "z-target" && firstZTargetIdx == -1 {
			firstZTargetIdx = i
		}
	}
	assert.GreaterOrEqual(t, firstATargetIdx, 0, "a-target should have at least one result")
	assert.GreaterOrEqual(t, firstZTargetIdx, 0, "z-target should have at least one result")
	assert.Less(t, firstATargetIdx, firstZTargetIdx, "a-target results should appear before z-target results")
}

func TestRegisterTarget_NilOrEmptyID_Ignored(t *testing.T) {
	p := &fakeProvider{}
	s, err := NewPushService(p, DefaultPushConfig())
	require.NoError(t, err)

	s.RegisterTarget(nil)
	s.RegisterTarget(&recordingTarget{id: ""})
	assert.Equal(t, 0, len(s.listTargets()))
}

func TestUnregisterTarget(t *testing.T) {
	p := &fakeProvider{items: samplePushItems()}
	s, err := NewPushService(p, DefaultPushConfig())
	require.NoError(t, err)

	s.RegisterTarget(&recordingTarget{id: "t1"})
	require.Len(t, s.listTargets(), 1)
	s.UnregisterTarget("t1")
	assert.Len(t, s.listTargets(), 0)
}

func TestRegisterTarget_ReplacesSameID(t *testing.T) {
	p := &fakeProvider{}
	s, err := NewPushService(p, DefaultPushConfig())
	require.NoError(t, err)

	s.RegisterTarget(&recordingTarget{id: "t1", criteria: RelevanceCriteria{StrategyID: "old"}})
	s.RegisterTarget(&recordingTarget{id: "t1", criteria: RelevanceCriteria{StrategyID: "new"}})
	require.Len(t, s.listTargets(), 1)
	assert.Equal(t, "new", s.listTargets()[0].Criteria().StrategyID)
}

func TestStart_OnDemandPolicy_NoOp(t *testing.T) {
	p := &fakeProvider{}
	s, err := NewPushService(p, DefaultPushConfig())
	require.NoError(t, err)

	err = s.Start(context.Background())
	require.NoError(t, err)
	// On-demand does not start a loop; Stop is a no-op.
	s.Stop()
}

func TestStart_ScheduledLoop_PushesPeriodically(t *testing.T) {
	p := &fakeProvider{items: samplePushItems()}
	cfg := &PushConfig{Policy: PolicyScheduled, MinScore: 0.5, Interval: 10 * time.Millisecond, MaxItemsPerTarget: 10}
	s, err := NewPushService(p, cfg)
	require.NoError(t, err)

	target := &recordingTarget{id: "t1", criteria: RelevanceCriteria{}}
	s.RegisterTarget(target)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err = s.Start(ctx)
	require.NoError(t, err)

	// Wait for at least one tick to fire.
	time.Sleep(80 * time.Millisecond)
	s.Stop()

	// Should have received at least one delivery via the scheduled loop.
	delivered := atomic.LoadInt64(&target.deliveredCount)
	assert.GreaterOrEqual(t, delivered, int64(1), "scheduled loop should deliver at least one item")
}

func TestStart_AlreadyRunning_ReturnsError(t *testing.T) {
	p := &fakeProvider{items: samplePushItems()}
	cfg := &PushConfig{Policy: PolicyScheduled, MinScore: 0.5, Interval: time.Second}
	s, err := NewPushService(p, cfg)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err = s.Start(ctx)
	require.NoError(t, err)
	defer s.Stop()

	err = s.Start(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAlreadyRunning)
}

func TestStop_Idempotent(t *testing.T) {
	p := &fakeProvider{}
	s, err := NewPushService(p, DefaultPushConfig())
	require.NoError(t, err)

	// Stop without Start is a no-op.
	s.Stop()
	s.Stop()
}

func TestStop_StopsScheduledLoop(t *testing.T) {
	p := &fakeProvider{items: samplePushItems()}
	cfg := &PushConfig{Policy: PolicyScheduled, MinScore: 0.5, Interval: 10 * time.Millisecond}
	s, err := NewPushService(p, cfg)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, s.Start(ctx))

	done := make(chan struct{})
	go func() {
		s.Stop()
		close(done)
	}()

	select {
	case <-done:
		// Stop returned; good.
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return within 2s")
	}
}

func TestStart_EventTriggered(t *testing.T) {
	p := &fakeProvider{items: samplePushItems()}
	cfg := &PushConfig{Policy: PolicyEventTriggered, MinScore: 0.5}
	s, err := NewPushService(p, cfg)
	require.NoError(t, err)
	s.RegisterTarget(&recordingTarget{id: "t1", criteria: RelevanceCriteria{}})

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, s.Start(ctx))

	// Event-triggered mode does not push on its own; trigger manually.
	r, err := s.PushRelevant(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, r.Delivered)

	cancel()
	s.Stop()
}

func TestStart_UnknownPolicy(t *testing.T) {
	p := &fakeProvider{}
	cfg := &PushConfig{Policy: PushPolicy("bogus"), MinScore: 0.5}
	s, err := NewPushService(p, cfg)
	require.NoError(t, err)

	err = s.Start(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidConfig)
}

func TestPushRelevant_ConcurrentCalls(t *testing.T) {
	p := &fakeProvider{items: samplePushItems()}
	s, err := NewPushService(p, DefaultPushConfig())
	require.NoError(t, err)

	// Register several targets.
	for i := 0; i < 5; i++ {
		s.RegisterTarget(&recordingTarget{id: string(rune('a' + i)), criteria: RelevanceCriteria{}})
	}

	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.PushRelevant(context.Background())
			if err != nil {
				errs <- err
				return
			}
			if r == nil {
				errs <- errors.New("nil result")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent push failed: %v", err)
	}
}

func TestPushRelevant_ContextCancellationMidBatch(t *testing.T) {
	// Use a target that blocks on Deliver until ctx is cancelled.
	p := &fakeProvider{items: samplePushItems()}
	s, err := NewPushService(p, DefaultPushConfig())
	require.NoError(t, err)

	blockingTarget := &blockingTarget{id: "blocker", criteria: RelevanceCriteria{}}
	s.RegisterTarget(blockingTarget)

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a short delay to interrupt delivery.
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()

	r, err := s.PushRelevant(ctx)
	// Either the cancellation propagates as an error or the result is partial.
	if err != nil {
		assert.Contains(t, err.Error(), "push relevant")
	} else {
		// Some items may have been delivered before cancellation.
		assert.NotNil(t, r)
	}
}

// blockingTarget blocks Deliver for a short time, allowing ctx cancellation to interrupt.
type blockingTarget struct {
	id       string
	criteria RelevanceCriteria
}

func (b *blockingTarget) ID() string                  { return b.id }
func (b *blockingTarget) Criteria() RelevanceCriteria { return b.criteria }

func (b *blockingTarget) Deliver(ctx context.Context, item KnowledgeItem) error {
	select {
	case <-time.After(20 * time.Millisecond):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
