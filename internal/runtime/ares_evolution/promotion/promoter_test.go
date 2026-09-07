// Package promotion provides strategy promotion and demotion logic.
// This file contains unit tests for the DefaultPromoter.
package promotion

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/experience"
)

func TestNewDefaultPromoter(t *testing.T) {
	t.Run("with default criteria", func(t *testing.T) {
		promoter := NewDefaultPromoter(nil)
		assert.NotNil(t, promoter)
		assert.NotNil(t, promoter.criteria)
		assert.Equal(t, 100, promoter.criteria.MinSampleCount)
	})

	t.Run("with custom criteria", func(t *testing.T) {
		criteria := &PromotionCriteria{
			MinSampleCount: 50,
			MinSuccessRate: 0.90,
			MaxErrorRate:   0.10,
			MaxLatencyP95:  3000,
			MinConfidence:  0.8,
		}
		promoter := NewDefaultPromoter(criteria)
		assert.NotNil(t, promoter)
		assert.Equal(t, 50, promoter.criteria.MinSampleCount)
	})
}

func TestDefaultPromoter_RegisterStrategy(t *testing.T) {
	promoter := NewDefaultPromoter(nil)

	t.Run("register new strategy", func(t *testing.T) {
		err := promoter.RegisterStrategy("strategy-1", "code_generation")
		assert.NoError(t, err)

		info, err := promoter.GetStrategyInfo(context.Background(), "strategy-1")
		require.NoError(t, err)
		assert.Equal(t, "strategy-1", info.StrategyID)
		assert.Equal(t, StrategyStateCandidate, info.CurrentState)
	})

	t.Run("register duplicate strategy", func(t *testing.T) {
		err := promoter.RegisterStrategy("strategy-1", "code_generation")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "already registered")
	})
}

func TestDefaultPromoter_Evaluate(t *testing.T) {
	promoter := NewDefaultPromoter(nil)
	ctx := context.Background()

	t.Run("new strategy starts as candidate", func(t *testing.T) {
		evidence := experience.Evidence{
			StrategyID:  "strategy-1",
			TaskType:    "code_generation",
			SampleCount: 5,
			SuccessRate: 0.6,
		}

		state, reason, err := promoter.Evaluate(ctx, "strategy-1", evidence)
		require.NoError(t, err)
		assert.Equal(t, StrategyStateCandidate, state)
		assert.Contains(t, reason, "needs more evidence")
	})

	t.Run("candidate promotes to shadow", func(t *testing.T) {
		// New strategies can transition immediately (GenerationCount == 0)
		evidence := experience.Evidence{
			StrategyID:  "strategy-2",
			TaskType:    "code_generation",
			SampleCount: 15,
			SuccessRate: 0.70,
		}

		state, reason, err := promoter.Evaluate(ctx, "strategy-2", evidence)
		require.NoError(t, err)
		assert.Equal(t, StrategyStateShadow, state)
		assert.Contains(t, reason, "promoted to shadow")
	})

	t.Run("shadow blocked by score_delta below threshold", func(t *testing.T) {
		// Fresh promoter for clean state
		p := NewDefaultPromoter(nil)
		err := p.RegisterStrategy("strategy-blocked", "code_generation")
		require.NoError(t, err)

		p.mu.Lock()
		p.strategies["strategy-blocked"].CurrentState = StrategyStateShadow
		p.strategies["strategy-blocked"].GenerationCount = 5
		p.strategies["strategy-blocked"].BaselineScore = 0.5 // Set high baseline
		p.mu.Unlock()

		// Evidence producing score ≈ 0.80 (< 0.5 improvement)
		evidence := experience.Evidence{
			StrategyID:  "strategy-blocked",
			TaskType:    "code_generation",
			SampleCount: 150,
			SuccessRate: 0.85,
			ErrorRate:   0.10,
			LatencyP95:  3000,
			Confidence:  0.80,
		}

		state, reason, err := p.Evaluate(ctx, "strategy-blocked", evidence)
		require.NoError(t, err)
		assert.Equal(t, StrategyStateShadow, state)
		assert.Contains(t, reason, "blocked")
		assert.Contains(t, reason, "score_delta")
	})

	t.Run("shadow promotes to champion when score_delta sufficient", func(t *testing.T) {
		// Fresh promoter for clean state
		p := NewDefaultPromoter(nil)
		err := p.RegisterStrategy("strategy-pass", "code_generation")
		require.NoError(t, err)

		p.mu.Lock()
		p.strategies["strategy-pass"].CurrentState = StrategyStateShadow
		p.strategies["strategy-pass"].GenerationCount = 5
		p.strategies["strategy-pass"].BaselineScore = 0.3 // Low baseline → big delta
		p.mu.Unlock()

		evidence := experience.Evidence{
			StrategyID:  "strategy-pass",
			TaskType:    "code_generation",
			SampleCount: 150,
			SuccessRate: 0.90,
			ErrorRate:   0.10,
			LatencyP95:  3000,
			Confidence:  0.85,
		}

		state, reason, err := p.Evaluate(ctx, "strategy-pass", evidence)
		require.NoError(t, err)
		assert.Equal(t, StrategyStateChampion, state)
		assert.Contains(t, reason, "promoted to champion")
	})

	t.Run("champion stays champion", func(t *testing.T) {
		// Register and promote to champion
		err := promoter.RegisterStrategy("strategy-4", "code_generation")
		require.NoError(t, err)

		// Promote to champion manually (new strategy can transition immediately)
		err = promoter.Promote(ctx, "strategy-4")
		require.NoError(t, err)

		// Promote to champion
		promoter.mu.Lock()
		promoter.strategies["strategy-4"].GenerationCount = 5 // Allow transition
		promoter.mu.Unlock()

		err = promoter.Promote(ctx, "strategy-4")
		require.NoError(t, err)

		// Set cool-down to allow evaluation
		promoter.mu.Lock()
		promoter.strategies["strategy-4"].GenerationCount = 5
		promoter.mu.Unlock()

		evidence := experience.Evidence{
			StrategyID:  "strategy-4",
			TaskType:    "code_generation",
			SampleCount: 150,
			SuccessRate: 0.90,
			ErrorRate:   0.10,
			LatencyP95:  3000,
			Confidence:  0.85,
		}

		state, reason, err := promoter.Evaluate(ctx, "strategy-4", evidence)
		require.NoError(t, err)
		assert.Equal(t, StrategyStateChampion, state)
		assert.Contains(t, reason, "champion")
	})

	t.Run("champion demotes on poor performance", func(t *testing.T) {
		// Register and promote to champion
		err := promoter.RegisterStrategy("strategy-5", "code_generation")
		require.NoError(t, err)

		// Promote to shadow (new strategy can transition immediately)
		err = promoter.Promote(ctx, "strategy-5")
		require.NoError(t, err)

		// Set generation count to allow next promotion
		promoter.mu.Lock()
		promoter.strategies["strategy-5"].GenerationCount = 5
		promoter.mu.Unlock()

		// Promote to champion
		err = promoter.Promote(ctx, "strategy-5")
		require.NoError(t, err)

		// Set generation count to allow demotion
		promoter.mu.Lock()
		promoter.strategies["strategy-5"].GenerationCount = 5
		promoter.mu.Unlock()

		evidence := experience.Evidence{
			StrategyID:  "strategy-5",
			TaskType:    "code_generation",
			SampleCount: 150,
			SuccessRate: 0.50, // Below threshold
			ErrorRate:   0.10,
			LatencyP95:  3000,
			Confidence:  0.85,
		}

		state, reason, err := promoter.Evaluate(ctx, "strategy-5", evidence)
		require.NoError(t, err)
		assert.Equal(t, StrategyStateDemoted, state)
		assert.Contains(t, reason, "demoted")
	})
}

func TestDefaultPromoter_Promote(t *testing.T) {
	promoter := NewDefaultPromoter(nil)
	ctx := context.Background()

	t.Run("promote non-existent strategy", func(t *testing.T) {
		err := promoter.Promote(ctx, "non-existent")
		assert.Error(t, err)
		assert.Equal(t, ErrStrategyNotFound, err)
	})

	t.Run("promote candidate to shadow", func(t *testing.T) {
		err := promoter.RegisterStrategy("strategy-1", "code_generation")
		require.NoError(t, err)

		// New strategy can transition immediately
		err = promoter.Promote(ctx, "strategy-1")
		assert.NoError(t, err)

		state, err := promoter.GetCurrentState(ctx, "strategy-1")
		require.NoError(t, err)
		assert.Equal(t, StrategyStateShadow, state)
	})

	t.Run("promote shadow to champion", func(t *testing.T) {
		err := promoter.RegisterStrategy("strategy-2", "code_generation")
		require.NoError(t, err)

		// Promote to shadow first (new strategy can transition immediately)
		err = promoter.Promote(ctx, "strategy-2")
		require.NoError(t, err)

		// Set generation count to allow next promotion
		promoter.mu.Lock()
		promoter.strategies["strategy-2"].GenerationCount = 5
		promoter.mu.Unlock()

		// Promote to champion
		err = promoter.Promote(ctx, "strategy-2")
		assert.NoError(t, err)

		state, err := promoter.GetCurrentState(ctx, "strategy-2")
		require.NoError(t, err)
		assert.Equal(t, StrategyStateChampion, state)

		// Check champions list
		champions, err := promoter.GetChampions(ctx, "code_generation")
		require.NoError(t, err)
		assert.Len(t, champions, 1)
		assert.Equal(t, "strategy-2", champions[0].StrategyID)
	})

	t.Run("promote during cool-down", func(t *testing.T) {
		err := promoter.RegisterStrategy("strategy-3", "code_generation")
		require.NoError(t, err)

		// Promote to shadow (new strategy can transition immediately)
		err = promoter.Promote(ctx, "strategy-3")
		require.NoError(t, err)

		// After transition, GenerationCount is reset to 0, so we need to manually set it to a low value
		// to test the cool-down period
		promoter.mu.Lock()
		promoter.strategies["strategy-3"].GenerationCount = 1 // Less than CoolDownGenerations (3)
		promoter.mu.Unlock()

		// Try to promote again immediately (cool-down active)
		err = promoter.Promote(ctx, "strategy-3")
		assert.Error(t, err)
		assert.Equal(t, ErrCoolDownActive, err)
	})

	t.Run("promote from invalid state", func(t *testing.T) {
		err := promoter.RegisterStrategy("strategy-4", "code_generation")
		require.NoError(t, err)

		// Manually set to retired
		promoter.mu.Lock()
		promoter.strategies["strategy-4"].CurrentState = StrategyStateRetired
		promoter.strategies["strategy-4"].GenerationCount = 10
		promoter.mu.Unlock()

		err = promoter.Promote(ctx, "strategy-4")
		assert.Error(t, err)
		assert.True(t, errors.Is(err, ErrInvalidStateTransition))
	})
}

func TestDefaultPromoter_Demote(t *testing.T) {
	promoter := NewDefaultPromoter(nil)
	ctx := context.Background()

	t.Run("demote non-existent strategy", func(t *testing.T) {
		err := promoter.Demote(ctx, "non-existent", "test reason")
		assert.Error(t, err)
		assert.Equal(t, ErrStrategyNotFound, err)
	})

	t.Run("demote candidate to retired", func(t *testing.T) {
		err := promoter.RegisterStrategy("strategy-1", "code_generation")
		require.NoError(t, err)

		// Retirement doesn't require cool-down
		err = promoter.Demote(ctx, "strategy-1", "test demotion")
		assert.NoError(t, err)

		state, err := promoter.GetCurrentState(ctx, "strategy-1")
		require.NoError(t, err)
		assert.Equal(t, StrategyStateRetired, state)
	})

	t.Run("demote champion to demoted", func(t *testing.T) {
		err := promoter.RegisterStrategy("strategy-2", "code_generation")
		require.NoError(t, err)

		// Promote to shadow (new strategy can transition immediately)
		err = promoter.Promote(ctx, "strategy-2")
		require.NoError(t, err)

		// Set generation count to allow next promotion
		promoter.mu.Lock()
		promoter.strategies["strategy-2"].GenerationCount = 5
		promoter.mu.Unlock()

		// Promote to champion
		err = promoter.Promote(ctx, "strategy-2")
		require.NoError(t, err)

		// Set generation count to allow demotion
		promoter.mu.Lock()
		promoter.strategies["strategy-2"].GenerationCount = 5
		promoter.mu.Unlock()

		err = promoter.Demote(ctx, "strategy-2", "performance degraded")
		assert.NoError(t, err)

		state, err := promoter.GetCurrentState(ctx, "strategy-2")
		require.NoError(t, err)
		assert.Equal(t, StrategyStateDemoted, state)

		// Check champions list is empty
		champions, err := promoter.GetChampions(ctx, "code_generation")
		require.NoError(t, err)
		assert.Len(t, champions, 0)
	})

	t.Run("demote during cool-down", func(t *testing.T) {
		err := promoter.RegisterStrategy("strategy-3", "code_generation")
		require.NoError(t, err)

		// Promote to shadow (new strategy can transition immediately)
		err = promoter.Promote(ctx, "strategy-3")
		require.NoError(t, err)

		// After transition, GenerationCount is reset to 0, so we need to manually set it to a low value
		// to test the cool-down period
		promoter.mu.Lock()
		promoter.strategies["strategy-3"].GenerationCount = 1 // Less than CoolDownGenerations (3)
		promoter.mu.Unlock()

		// Try to demote immediately (cool-down active)
		err = promoter.Demote(ctx, "strategy-3", "test")
		assert.Error(t, err)
		assert.Equal(t, ErrCoolDownActive, err)
	})
}

func TestDefaultPromoter_GetHistory(t *testing.T) {
	promoter := NewDefaultPromoter(nil)
	ctx := context.Background()

	t.Run("no history for new strategy", func(t *testing.T) {
		err := promoter.RegisterStrategy("strategy-1", "code_generation")
		require.NoError(t, err)

		history, err := promoter.GetHistory(ctx, "strategy-1")
		require.NoError(t, err)
		assert.Len(t, history, 0)
	})

	t.Run("history after promotions", func(t *testing.T) {
		err := promoter.RegisterStrategy("strategy-2", "code_generation")
		require.NoError(t, err)

		// Promote to shadow (new strategy can transition immediately)
		err = promoter.Promote(ctx, "strategy-2")
		require.NoError(t, err)

		// Set generation count to allow next promotion
		promoter.mu.Lock()
		promoter.strategies["strategy-2"].GenerationCount = 5
		promoter.mu.Unlock()

		// Promote to champion
		err = promoter.Promote(ctx, "strategy-2")
		require.NoError(t, err)

		history, err := promoter.GetHistory(ctx, "strategy-2")
		require.NoError(t, err)
		assert.Len(t, history, 2)

		// Check first promotion
		assert.Equal(t, StrategyStateShadow, history[0].State)
		assert.Equal(t, StrategyStateCandidate, history[0].PreviousState)

		// Check second promotion
		assert.Equal(t, StrategyStateChampion, history[1].State)
		assert.Equal(t, StrategyStateShadow, history[1].PreviousState)
	})
}

func TestDefaultPromoter_GetCurrentState(t *testing.T) {
	promoter := NewDefaultPromoter(nil)
	ctx := context.Background()

	t.Run("unregistered strategy returns candidate", func(t *testing.T) {
		state, err := promoter.GetCurrentState(ctx, "non-existent")
		require.NoError(t, err)
		assert.Equal(t, StrategyStateCandidate, state)
	})

	t.Run("registered strategy returns correct state", func(t *testing.T) {
		err := promoter.RegisterStrategy("strategy-1", "code_generation")
		require.NoError(t, err)

		state, err := promoter.GetCurrentState(ctx, "strategy-1")
		require.NoError(t, err)
		assert.Equal(t, StrategyStateCandidate, state)
	})
}

func TestDefaultPromoter_GetChampions(t *testing.T) {
	promoter := NewDefaultPromoter(nil)
	ctx := context.Background()

	t.Run("no champions initially", func(t *testing.T) {
		champions, err := promoter.GetChampions(ctx, "code_generation")
		require.NoError(t, err)
		assert.Len(t, champions, 0)
	})

	t.Run("multiple champions per task type", func(t *testing.T) {
		// Register multiple strategies
		err := promoter.RegisterStrategy("strategy-1", "code_generation")
		require.NoError(t, err)

		err = promoter.RegisterStrategy("strategy-2", "code_generation")
		require.NoError(t, err)

		// Promote strategy-1 to champion
		// Promote to shadow (new strategy can transition immediately)
		err = promoter.Promote(ctx, "strategy-1")
		require.NoError(t, err)

		// Set generation count to allow next promotion
		promoter.mu.Lock()
		promoter.strategies["strategy-1"].GenerationCount = 5
		promoter.mu.Unlock()

		// Promote to champion
		err = promoter.Promote(ctx, "strategy-1")
		require.NoError(t, err)

		// Promote strategy-2 to champion
		// Promote to shadow (new strategy can transition immediately)
		err = promoter.Promote(ctx, "strategy-2")
		require.NoError(t, err)

		// Set generation count to allow next promotion
		promoter.mu.Lock()
		promoter.strategies["strategy-2"].GenerationCount = 5
		promoter.mu.Unlock()

		// Promote to champion
		err = promoter.Promote(ctx, "strategy-2")
		require.NoError(t, err)

		// Get champions
		champions, err := promoter.GetChampions(ctx, "code_generation")
		require.NoError(t, err)
		assert.Len(t, champions, 2)
	})

	t.Run("different task types", func(t *testing.T) {
		err := promoter.RegisterStrategy("strategy-3", "analysis")
		require.NoError(t, err)

		// Promote to shadow (new strategy can transition immediately)
		err = promoter.Promote(ctx, "strategy-3")
		require.NoError(t, err)

		// Set generation count to allow next promotion
		promoter.mu.Lock()
		promoter.strategies["strategy-3"].GenerationCount = 5
		promoter.mu.Unlock()

		// Promote to champion
		err = promoter.Promote(ctx, "strategy-3")
		require.NoError(t, err)

		// Check champions for analysis task
		champions, err := promoter.GetChampions(ctx, "analysis")
		require.NoError(t, err)
		assert.Len(t, champions, 1)

		// Check champions for code_generation still has 2
		champions, err = promoter.GetChampions(ctx, "code_generation")
		require.NoError(t, err)
		assert.Len(t, champions, 2)
	})
}

func TestDefaultPromoter_GetStrategyInfo(t *testing.T) {
	promoter := NewDefaultPromoter(nil)
	ctx := context.Background()

	t.Run("non-existent strategy", func(t *testing.T) {
		info, err := promoter.GetStrategyInfo(ctx, "non-existent")
		assert.Error(t, err)
		assert.Equal(t, ErrStrategyNotFound, err)
		assert.Nil(t, info)
	})

	t.Run("existing strategy", func(t *testing.T) {
		err := promoter.RegisterStrategy("strategy-1", "code_generation")
		require.NoError(t, err)

		info, err := promoter.GetStrategyInfo(ctx, "strategy-1")
		require.NoError(t, err)
		assert.NotNil(t, info)
		assert.Equal(t, "strategy-1", info.StrategyID)
		assert.Equal(t, "code_generation", info.TaskType)
		assert.Equal(t, StrategyStateCandidate, info.CurrentState)
	})
}

func TestDefaultPromoter_SetGeneration(t *testing.T) {
	promoter := NewDefaultPromoter(nil)

	// Register a strategy
	err := promoter.RegisterStrategy("strategy-1", "code_generation")
	require.NoError(t, err)

	// Set generation
	promoter.SetGeneration(5)

	// Check generation count was incremented
	info, err := promoter.GetStrategyInfo(context.Background(), "strategy-1")
	require.NoError(t, err)
	assert.Equal(t, 1, info.GenerationCount)

	// Set generation again
	promoter.SetGeneration(10)

	// Check generation count was incremented again
	info, err = promoter.GetStrategyInfo(context.Background(), "strategy-1")
	require.NoError(t, err)
	assert.Equal(t, 2, info.GenerationCount)
}

func TestDefaultPromoter_GetAllStrategies(t *testing.T) {
	promoter := NewDefaultPromoter(nil)

	// Register multiple strategies
	err := promoter.RegisterStrategy("strategy-1", "code_generation")
	require.NoError(t, err)

	err = promoter.RegisterStrategy("strategy-2", "analysis")
	require.NoError(t, err)

	err = promoter.RegisterStrategy("strategy-3", "code_generation")
	require.NoError(t, err)

	// Get all strategies
	all := promoter.GetAllStrategies()
	assert.Len(t, all, 3)

	// Verify each strategy
	assert.Contains(t, all, "strategy-1")
	assert.Contains(t, all, "strategy-2")
	assert.Contains(t, all, "strategy-3")
}

func TestDefaultPromoter_ConcurrentAccess(t *testing.T) {
	promoter := NewDefaultPromoter(nil)
	ctx := context.Background()

	// Register strategies
	for i := 0; i < 10; i++ {
		err := promoter.RegisterStrategy(
			fmt.Sprintf("strategy-%d", i),
			"code_generation",
		)
		require.NoError(t, err)
	}

	// Concurrent operations
	var wg sync.WaitGroup
	var errCount int64

	// Concurrent evaluations
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			strategyID := fmt.Sprintf("strategy-%d", idx%10)
			evidence := experience.Evidence{
				StrategyID:  strategyID,
				TaskType:    "code_generation",
				SampleCount: int64(idx * 10),
				SuccessRate: 0.8,
			}
			_, _, err := promoter.Evaluate(ctx, strategyID, evidence)
			if err != nil {
				atomic.AddInt64(&errCount, 1)
			}
		}(i)
	}

	// Concurrent state queries
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			strategyID := fmt.Sprintf("strategy-%d", idx%10)
			_, err := promoter.GetCurrentState(ctx, strategyID)
			if err != nil {
				atomic.AddInt64(&errCount, 1)
			}
		}(i)
	}

	// Concurrent champion queries
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := promoter.GetChampions(ctx, "code_generation")
			if err != nil {
				atomic.AddInt64(&errCount, 1)
			}
		}()
	}

	wg.Wait()
	assert.Equal(t, int64(0), atomic.LoadInt64(&errCount))
}

func TestEvaluate_RollingImprovementFlagged(t *testing.T) {
	promoter := NewDefaultPromoter(nil)
	ctx := context.Background()

	t.Run("rolling improvement below threshold flagged", func(t *testing.T) {
		err := promoter.RegisterStrategy("strategy-ri", "code_generation")
		require.NoError(t, err)

		// Promote to champion
		err = promoter.Promote(ctx, "strategy-ri")
		require.NoError(t, err)

		promoter.mu.Lock()
		promoter.strategies["strategy-ri"].GenerationCount = 5
		promoter.mu.Unlock()

		err = promoter.Promote(ctx, "strategy-ri")
		require.NoError(t, err)

		promoter.mu.Lock()
		promoter.strategies["strategy-ri"].GenerationCount = 10
		// Set ScoreHistory so that avg rolling improvement < 0.1 after evaluate adds the new score.
		// With evidence score ≈ 0.84 and history [0.80, 0.82]:
		// rolling = ((0.82-0.80) + (0.84-0.82)) / 2 = (0.02 + 0.02) / 2 = 0.02 < 0.1
		promoter.strategies["strategy-ri"].ScoreHistory = []ScoreSnapshot{
			{Score: 0.80},
			{Score: 0.82},
		}
		promoter.mu.Unlock()

		evidence := experience.Evidence{
			StrategyID:  "strategy-ri",
			TaskType:    "code_generation",
			SampleCount: 150,
			SuccessRate: 0.85,
			ErrorRate:   0.10,
			LatencyP95:  3000,
			Confidence:  0.80,
		}

		state, reason, err := promoter.Evaluate(ctx, "strategy-ri", evidence)
		require.NoError(t, err)
		assert.Equal(t, StrategyStateChampion, state)
		assert.Contains(t, reason, "flagged")
		assert.Contains(t, reason, "rolling_improvement")
	})

	t.Run("rolling improvement above threshold maintained", func(t *testing.T) {
		err := promoter.RegisterStrategy("strategy-rm", "code_generation")
		require.NoError(t, err)

		// Promote to champion
		err = promoter.Promote(ctx, "strategy-rm")
		require.NoError(t, err)

		promoter.mu.Lock()
		promoter.strategies["strategy-rm"].GenerationCount = 5
		promoter.mu.Unlock()

		err = promoter.Promote(ctx, "strategy-rm")
		require.NoError(t, err)

		promoter.mu.Lock()
		promoter.strategies["strategy-rm"].GenerationCount = 10
		// Set ScoreHistory so that avg rolling improvement >= 0.1 after evaluate adds the new score.
		// With evidence score ≈ 0.84 and history [0.40, 0.62]:
		// rolling = ((0.62-0.40) + (0.84-0.62)) / 2 = (0.22 + 0.22) / 2 = 0.22 >= 0.1
		promoter.strategies["strategy-rm"].ScoreHistory = []ScoreSnapshot{
			{Score: 0.40},
			{Score: 0.62},
		}
		promoter.mu.Unlock()

		evidence := experience.Evidence{
			StrategyID:  "strategy-rm",
			TaskType:    "code_generation",
			SampleCount: 150,
			SuccessRate: 0.85,
			ErrorRate:   0.10,
			LatencyP95:  3000,
			Confidence:  0.80,
		}

		state, reason, err := promoter.Evaluate(ctx, "strategy-rm", evidence)
		require.NoError(t, err)
		assert.Equal(t, StrategyStateChampion, state)
		assert.Contains(t, reason, "maintained")
	})
}

func TestEvaluate_ScoreHistoryCapped(t *testing.T) {
	promoter := NewDefaultPromoter(nil)
	ctx := context.Background()

	err := promoter.RegisterStrategy("strategy-sh", "code_generation")
	require.NoError(t, err)

	// Evaluate 25 times to exceed the cap of 20
	for i := 0; i < 25; i++ {
		evidence := experience.Evidence{
			StrategyID:  "strategy-sh",
			TaskType:    "code_generation",
			SampleCount: int64(i + 1),
			SuccessRate: 0.7,
		}
		_, _, err := promoter.Evaluate(ctx, "strategy-sh", evidence)
		require.NoError(t, err)
	}

	info, err := promoter.GetStrategyInfo(ctx, "strategy-sh")
	require.NoError(t, err)
	assert.Len(t, info.ScoreHistory, 20)
}
