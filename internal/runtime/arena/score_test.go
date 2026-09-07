package arena

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// --- Backward-compatible tests (using CalculateScoreV1) ---

func TestCalculateScore_PerfectRecovery(t *testing.T) {
	stats := Stats{
		TotalActions:      10,
		SuccessfulActions: 10,
		FailedActions:     0,
	}
	score := CalculateScoreV1(stats, 500*time.Millisecond)

	assert.InDelta(t, 100.0, score.Score, 1.0)
	assert.Equal(t, "A+", score.Grade)
	assert.InDelta(t, 100.0, score.RecoveryRate, 0.01)
	assert.Equal(t, 10, score.TotalFaults)
	assert.Equal(t, 10, score.RecoveredFaults)
	assert.Equal(t, 0, score.FailedFaults)
}

func TestCalculateScore_ZeroActions(t *testing.T) {
	stats := Stats{}
	score := CalculateScoreV1(stats, 0)

	// With no actions: Availability=0, Recovery=0, Consistency=100 (heuristic)
	// Score = 0*0.4 + 0*0.3 + 100*0.3 = 30
	assert.InDelta(t, 30.0, score.Score, 1.0)
	assert.Equal(t, "F", score.Grade)
	assert.Equal(t, 0, score.TotalFaults)
}

func TestCalculateScore_FullFailure(t *testing.T) {
	stats := Stats{
		TotalActions:      10,
		SuccessfulActions: 0,
		FailedActions:     10,
	}
	score := CalculateScoreV1(stats, 2*time.Second)

	// Availability = 0, Recovery = 0, Consistency ~87.5 (10 failed -> dataRelated=5 -> 100-25=75)
	// Score = 0*0.4 + 0*0.3 + 75*0.3 = 22.5
	assert.Less(t, score.Score, 60.0)
	assert.Equal(t, "F", score.Grade)
	assert.InDelta(t, 0.0, score.RecoveryRate, 0.01)
	assert.Equal(t, 10, score.FailedFaults)
}

func TestCalculateScore_SlowRecovery(t *testing.T) {
	stats := Stats{
		TotalActions:      10,
		SuccessfulActions: 10,
		FailedActions:     0,
	}
	// At 10s avg recovery, speed score = 0.
	score := CalculateScoreV1(stats, 10*time.Second)

	// Availability=100, Recovery=100*0.7+0*0.3=70, Consistency=100
	// Score = 100*0.4 + 70*0.3 + 100*0.3 = 40+21+30 = 91
	assert.InDelta(t, 91.0, score.Score, 1.0)
	assert.Equal(t, "A", score.Grade)
}

func TestCalculateScore_GradeThresholds(t *testing.T) {
	tests := []struct {
		name  string
		score float64
		grade string
	}{
		{"A+ boundary", 95.0, "A+"},
		{"A boundary", 90.0, "A"},
		{"B boundary", 80.0, "B"},
		{"C boundary", 70.0, "C"},
		{"D boundary", 60.0, "D"},
		{"F boundary", 59.9, "F"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			grade := gradeFromScore(tt.score)
			assert.Equal(t, tt.grade, grade)
		})
	}
}

func TestCalculateScore_SpeedScoreLinearDecay(t *testing.T) {
	stats := Stats{
		TotalActions:      10,
		SuccessfulActions: 10,
		FailedActions:     0,
	}

	// At 1s, speed = 100, recovery dim = 100*0.7+100*0.3=100
	score1s := CalculateScoreV1(stats, 1*time.Second)
	assert.InDelta(t, 100.0, score1s.Score, 1.0)

	// At 5.5s (midpoint), speed = 50, recovery dim = 100*0.7+50*0.3=85
	scoreMid := CalculateScoreV1(stats, 5500*time.Millisecond)
	expectedMid := 100*0.4 + 85*0.3 + 100*0.3 // = 40+25.5+30 = 95.5
	assert.InDelta(t, expectedMid, scoreMid.Score, 1.0)

	// At 10s, speed = 0, recovery dim = 100*0.7+0*0.3=70
	score10s := CalculateScoreV1(stats, 10*time.Second)
	expected10s := 100*0.4 + 70*0.3 + 100*0.3 // = 40+21+30 = 91
	assert.InDelta(t, expected10s, score10s.Score, 1.0)
}

func TestCalculateScore_PartialRecovery(t *testing.T) {
	stats := Stats{
		TotalActions:      10,
		SuccessfulActions: 8,
		FailedActions:     2,
	}
	score := CalculateScoreV1(stats, 1*time.Second)

	// Availability = 80, Recovery = 80*0.7+100*0.3=86, Consistency = 100-(2/2)*5=95
	// Score = 80*0.4 + 86*0.3 + 95*0.3 = 32+25.8+28.5 = 86.3
	assert.InDelta(t, 86.3, score.Score, 1.0)
	assert.Equal(t, "B", score.Grade)
	assert.InDelta(t, 80.0, score.RecoveryRate, 0.01)
}

func TestCalculateScore_FastRecovery(t *testing.T) {
	stats := Stats{
		TotalActions:      20,
		SuccessfulActions: 19,
		FailedActions:     1,
	}
	score := CalculateScoreV1(stats, 200*time.Millisecond)

	// Availability = 95, Recovery = 95*0.7+100*0.3=96.5, Consistency = 97.5
	// Score = 95*0.4 + 96.5*0.3 + 97.5*0.3 = 38+28.95+29.25 = 96.2
	assert.InDelta(t, 96.2, score.Score, 1.0)
	assert.Equal(t, "A+", score.Grade)
}

// --- New 3D scoring tests with metrics ---

func TestCalculateScoreWithMetrics_ThreeDimensions(t *testing.T) {
	stats := Stats{
		TotalActions:      10,
		SuccessfulActions: 9,
		FailedActions:     1,
	}
	metrics := &MetricsSnapshot{
		DataConsistencyRate: 95.0,
	}
	score := CalculateScore(stats, 500*time.Millisecond, metrics)

	// Availability = 90, Recovery = 90*0.7+100*0.3=93, Consistency = 95
	// Score = 90*0.4 + 93*0.3 + 95*0.3 = 36+27.9+28.5 = 92.4
	assert.InDelta(t, 92.4, score.Score, 1.0)
	assert.Equal(t, "A", score.Grade)
	assert.InDelta(t, 90.0, score.AvailabilityScore, 0.01)
	assert.InDelta(t, 95.0, score.ConsistencyScore, 0.01)
	assert.InDelta(t, 90.0, score.Dimensions.Availability, 0.01)
	assert.InDelta(t, 93.0, score.Dimensions.Recovery, 1.0)
	assert.InDelta(t, 95.0, score.Dimensions.Consistency, 0.01)
}

func TestCalculateScoreWithoutMetrics_BackwardCompatible(t *testing.T) {
	stats := Stats{
		TotalActions:      10,
		SuccessfulActions: 10,
		FailedActions:     0,
	}
	// Using V1 wrapper should produce same result as passing nil metrics.
	scoreV1 := CalculateScoreV1(stats, 500*time.Millisecond)
	scoreNil := CalculateScore(stats, 500*time.Millisecond, nil)

	assert.Equal(t, scoreV1.Score, scoreNil.Score)
	assert.Equal(t, scoreV1.Grade, scoreNil.Grade)
	assert.Equal(t, scoreV1.Dimensions, scoreNil.Dimensions)
}

func TestGradeBoundaries_AllGrades(t *testing.T) {
	tests := []struct {
		name        string
		stats       Stats
		avgRecovery time.Duration
		metrics     *MetricsSnapshot
		wantGrade   string
	}{
		{
			name:        "A+ perfect",
			stats:       Stats{TotalActions: 10, SuccessfulActions: 10, FailedActions: 0},
			avgRecovery: 500 * time.Millisecond,
			metrics:     &MetricsSnapshot{DataConsistencyRate: 100},
			wantGrade:   "A+",
		},
		{
			name:        "A good",
			stats:       Stats{TotalActions: 10, SuccessfulActions: 10, FailedActions: 0},
			avgRecovery: 500 * time.Millisecond,
			metrics:     &MetricsSnapshot{DataConsistencyRate: 80},
			wantGrade:   "A",
		},
		{
			name:        "B moderate",
			stats:       Stats{TotalActions: 10, SuccessfulActions: 8, FailedActions: 2},
			avgRecovery: 2 * time.Second,
			metrics:     &MetricsSnapshot{DataConsistencyRate: 80},
			wantGrade:   "B",
		},
		{
			name:        "C below average",
			stats:       Stats{TotalActions: 10, SuccessfulActions: 7, FailedActions: 3},
			avgRecovery: 3 * time.Second,
			metrics:     &MetricsSnapshot{DataConsistencyRate: 75},
			wantGrade:   "C",
		},
		{
			name:        "D poor",
			stats:       Stats{TotalActions: 10, SuccessfulActions: 6, FailedActions: 4},
			avgRecovery: 3 * time.Second,
			metrics:     &MetricsSnapshot{DataConsistencyRate: 60},
			wantGrade:   "D",
		},
		{
			name:        "F failing",
			stats:       Stats{TotalActions: 10, SuccessfulActions: 2, FailedActions: 8},
			avgRecovery: 10 * time.Second,
			metrics:     &MetricsSnapshot{DataConsistencyRate: 30},
			wantGrade:   "F",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := CalculateScore(tt.stats, tt.avgRecovery, tt.metrics)
			assert.Equal(t, tt.wantGrade, score.Grade)
		})
	}
}

func TestCalculateScore_DimensionBreakdown(t *testing.T) {
	stats := Stats{
		TotalActions:      20,
		SuccessfulActions: 18,
		FailedActions:     2,
	}
	metrics := &MetricsSnapshot{
		DataConsistencyRate: 90.0,
	}
	score := CalculateScore(stats, 2*time.Second, metrics)

	// Verify all dimension fields are populated correctly.
	assert.GreaterOrEqual(t, score.Dimensions.Availability, 0.0)
	assert.LessOrEqual(t, score.Dimensions.Availability, 100.0)
	assert.GreaterOrEqual(t, score.Dimensions.Recovery, 0.0)
	assert.LessOrEqual(t, score.Dimensions.Recovery, 100.0)
	assert.GreaterOrEqual(t, score.Dimensions.Consistency, 0.0)
	assert.LessOrEqual(t, score.Dimensions.Consistency, 100.0)

	// Verify the weighted formula matches.
	expectedScore := score.Dimensions.Availability*0.4 +
		score.Dimensions.Recovery*0.3 +
		score.Dimensions.Consistency*0.3
	assert.InDelta(t, expectedScore, score.Score, 0.01)
}

func TestCalcAvailability_ZeroFaults(t *testing.T) {
	avail := calcAvailability(0, 0, 0)
	assert.InDelta(t, 0.0, avail, 0.01)
}

func TestCalcAvailability_AllPassed(t *testing.T) {
	avail := calcAvailability(10, 10, 0)
	assert.InDelta(t, 100.0, avail, 0.01)
}

func TestCalcAvailability_HalfFailed(t *testing.T) {
	avail := calcAvailability(10, 5, 5)
	assert.InDelta(t, 50.0, avail, 0.01)
}

func TestClamp(t *testing.T) {
	assert.InDelta(t, 50.0, clamp(50, 0, 100), 0.01)
	assert.InDelta(t, 0.0, clamp(-10, 0, 100), 0.01)
	assert.InDelta(t, 100.0, clamp(200, 0, 100), 0.01)
}
