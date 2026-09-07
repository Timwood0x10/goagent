// Package promotion provides strategy promotion and demotion logic.
// This file contains unit tests for promotion types.
package promotion

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Timwood0x10/ares/internal/runtime/ares_evolution/experience"
)

func TestStrategyState_IsValid(t *testing.T) {
	tests := []struct {
		name  string
		state StrategyState
		want  bool
	}{
		{
			name:  "candidate is valid",
			state: StrategyStateCandidate,
			want:  true,
		},
		{
			name:  "shadow is valid",
			state: StrategyStateShadow,
			want:  true,
		},
		{
			name:  "champion is valid",
			state: StrategyStateChampion,
			want:  true,
		},
		{
			name:  "demoted is valid",
			state: StrategyStateDemoted,
			want:  true,
		},
		{
			name:  "retired is valid",
			state: StrategyStateRetired,
			want:  true,
		},
		{
			name:  "invalid state",
			state: StrategyState("invalid"),
			want:  false,
		},
		{
			name:  "empty state",
			state: StrategyState(""),
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.state.IsValid())
		})
	}
}

func TestStrategyState_String(t *testing.T) {
	tests := []struct {
		name  string
		state StrategyState
		want  string
	}{
		{
			name:  "candidate string",
			state: StrategyStateCandidate,
			want:  "candidate",
		},
		{
			name:  "shadow string",
			state: StrategyStateShadow,
			want:  "shadow",
		},
		{
			name:  "champion string",
			state: StrategyStateChampion,
			want:  "champion",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.state.String())
		})
	}
}

func TestStrategyState_CanPromoteTo(t *testing.T) {
	tests := []struct {
		name    string
		current StrategyState
		target  StrategyState
		want    bool
	}{
		{
			name:    "candidate can promote to shadow",
			current: StrategyStateCandidate,
			target:  StrategyStateShadow,
			want:    true,
		},
		{
			name:    "shadow can promote to champion",
			current: StrategyStateShadow,
			target:  StrategyStateChampion,
			want:    true,
		},
		{
			name:    "demoted can promote to shadow",
			current: StrategyStateDemoted,
			target:  StrategyStateShadow,
			want:    true,
		},
		{
			name:    "candidate cannot promote to champion directly",
			current: StrategyStateCandidate,
			target:  StrategyStateChampion,
			want:    false,
		},
		{
			name:    "champion cannot promote further",
			current: StrategyStateChampion,
			target:  StrategyStateShadow,
			want:    false,
		},
		{
			name:    "retired cannot promote",
			current: StrategyStateRetired,
			target:  StrategyStateShadow,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.current.CanPromoteTo(tt.target))
		})
	}
}

func TestStrategyState_CanDemoteTo(t *testing.T) {
	tests := []struct {
		name    string
		current StrategyState
		target  StrategyState
		want    bool
	}{
		{
			name:    "candidate can demote to retired",
			current: StrategyStateCandidate,
			target:  StrategyStateRetired,
			want:    true,
		},
		{
			name:    "shadow can demote to demoted",
			current: StrategyStateShadow,
			target:  StrategyStateDemoted,
			want:    true,
		},
		{
			name:    "shadow can demote to retired",
			current: StrategyStateShadow,
			target:  StrategyStateRetired,
			want:    true,
		},
		{
			name:    "champion can demote to demoted",
			current: StrategyStateChampion,
			target:  StrategyStateDemoted,
			want:    true,
		},
		{
			name:    "demoted can demote to retired",
			current: StrategyStateDemoted,
			target:  StrategyStateRetired,
			want:    true,
		},
		{
			name:    "champion cannot demote to retired directly",
			current: StrategyStateChampion,
			target:  StrategyStateRetired,
			want:    false,
		},
		{
			name:    "retired cannot demote further",
			current: StrategyStateRetired,
			target:  StrategyStateDemoted,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.current.CanDemoteTo(tt.target))
		})
	}
}

func TestStrategyPromotionRecord_IsEmpty(t *testing.T) {
	tests := []struct {
		name   string
		record StrategyPromotionRecord
		want   bool
	}{
		{
			name:   "empty record",
			record: StrategyPromotionRecord{},
			want:   true,
		},
		{
			name: "record with strategy ID",
			record: StrategyPromotionRecord{
				StrategyID: "strategy-1",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.record.IsEmpty())
		})
	}
}

func TestStrategyInfo_IsEmpty(t *testing.T) {
	tests := []struct {
		name string
		info StrategyInfo
		want bool
	}{
		{
			name: "empty info",
			info: StrategyInfo{},
			want: true,
		},
		{
			name: "info with strategy ID",
			info: StrategyInfo{
				StrategyID: "strategy-1",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.info.IsEmpty())
		})
	}
}

func TestDefaultPromotionCriteria(t *testing.T) {
	criteria := DefaultPromotionCriteria()

	assert.NotNil(t, criteria)
	assert.Equal(t, 100, criteria.MinSampleCount)
	assert.Equal(t, 0.85, criteria.MinSuccessRate)
	assert.Equal(t, 0.15, criteria.MaxErrorRate)
	assert.Equal(t, int64(5000), criteria.MaxLatencyP95)
	assert.Equal(t, 0.7, criteria.MinConfidence)
	assert.Equal(t, 5, criteria.ChampionHoldPeriod)
	assert.Equal(t, 0.3, criteria.DemotionThreshold)
	assert.Equal(t, 3, criteria.CoolDownGenerations)
}

func TestMeetsPromotionCriteria(t *testing.T) {
	tests := []struct {
		name     string
		evidence experience.Evidence
		criteria *PromotionCriteria
		want     bool
	}{
		{
			name: "meets all criteria",
			evidence: experience.Evidence{
				SampleCount: 150,
				SuccessRate: 0.90,
				ErrorRate:   0.10,
				LatencyP95:  3000,
				Confidence:  0.85,
			},
			criteria: DefaultPromotionCriteria(),
			want:     true,
		},
		{
			name: "insufficient samples",
			evidence: experience.Evidence{
				SampleCount: 50,
				SuccessRate: 0.90,
				ErrorRate:   0.10,
				LatencyP95:  3000,
				Confidence:  0.85,
			},
			criteria: DefaultPromotionCriteria(),
			want:     false,
		},
		{
			name: "low success rate",
			evidence: experience.Evidence{
				SampleCount: 150,
				SuccessRate: 0.80,
				ErrorRate:   0.10,
				LatencyP95:  3000,
				Confidence:  0.85,
			},
			criteria: DefaultPromotionCriteria(),
			want:     false,
		},
		{
			name: "high error rate",
			evidence: experience.Evidence{
				SampleCount: 150,
				SuccessRate: 0.90,
				ErrorRate:   0.20,
				LatencyP95:  3000,
				Confidence:  0.85,
			},
			criteria: DefaultPromotionCriteria(),
			want:     false,
		},
		{
			name: "high latency",
			evidence: experience.Evidence{
				SampleCount: 150,
				SuccessRate: 0.90,
				ErrorRate:   0.10,
				LatencyP95:  6000,
				Confidence:  0.85,
			},
			criteria: DefaultPromotionCriteria(),
			want:     false,
		},
		{
			name: "low confidence",
			evidence: experience.Evidence{
				SampleCount: 150,
				SuccessRate: 0.90,
				ErrorRate:   0.10,
				LatencyP95:  3000,
				Confidence:  0.60,
			},
			criteria: DefaultPromotionCriteria(),
			want:     false,
		},
		{
			name: "nil criteria uses defaults",
			evidence: experience.Evidence{
				SampleCount: 150,
				SuccessRate: 0.90,
				ErrorRate:   0.10,
				LatencyP95:  3000,
				Confidence:  0.85,
			},
			criteria: nil,
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, MeetsPromotionCriteria(tt.evidence, tt.criteria))
		})
	}
}

func TestCalculateEvidenceScore(t *testing.T) {
	tests := []struct {
		name     string
		evidence experience.Evidence
		want     float64
	}{
		{
			name: "perfect evidence",
			evidence: experience.Evidence{
				SuccessRate: 1.0,
				ErrorRate:   0.0,
				Confidence:  1.0,
				LatencyP95:  0,
			},
			want: 1.0,
		},
		{
			name: "poor evidence",
			evidence: experience.Evidence{
				SuccessRate: 0.0,
				ErrorRate:   1.0,
				Confidence:  0.0,
				LatencyP95:  10000,
			},
			want: 0.0,
		},
		{
			name: "moderate evidence",
			evidence: experience.Evidence{
				SuccessRate: 0.85,
				ErrorRate:   0.15,
				Confidence:  0.7,
				LatencyP95:  5000,
			},
			want: 0.785, // Calculated: 0.85*0.4 + 0.85*0.3 + 0.7*0.2 + 0.5*0.1 = 0.34 + 0.255 + 0.14 + 0.05 = 0.785
		},
		{
			name: "high latency",
			evidence: experience.Evidence{
				SuccessRate: 0.90,
				ErrorRate:   0.05,
				Confidence:  0.85,
				LatencyP95:  15000,
			},
			want: 0.815, // Calculated: 0.90*0.4 + 0.95*0.3 + 0.85*0.2 + 0.0*0.1 = 0.36 + 0.285 + 0.17 + 0 = 0.815
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := CalculateEvidenceScore(tt.evidence)
			assert.InDelta(t, tt.want, score, 0.01)
		})
	}
}

func TestPromotionDecision_IsEmpty(t *testing.T) {
	decision := PromotionDecision{
		StrategyID: "strategy-1",
	}

	assert.False(t, decision.StrategyID == "")
}
