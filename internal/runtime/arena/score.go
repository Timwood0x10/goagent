package arena

import (
	"time"
)

// ResilienceScore represents the system's resilience rating.
type ResilienceScore struct {
	RecoveryRate    float64       `json:"recovery_rate"`
	AvgRecoveryTime time.Duration `json:"avg_recovery_time"`
	TotalFaults     int           `json:"total_faults"`
	RecoveredFaults int           `json:"recovered_faults"`
	FailedFaults    int           `json:"failed_faults"`
	Score           float64       `json:"score"`
	Grade           string        `json:"grade"`

	// New fields for 3-dimensional scoring
	AvailabilityScore float64         `json:"availability_score"` // 0-100, based on uptime during faults
	ConsistencyScore  float64         `json:"consistency_score"`  // 0-100, based on data integrity after recovery
	Dimensions        ScoreDimensions `json:"dimensions"`         // breakdown of each dimension
}

// ScoreDimensions holds the individual dimension scores that compose the final score.
type ScoreDimensions struct {
	Availability float64 `json:"availability"` // weight 40%
	Recovery     float64 `json:"recovery"`     // weight 30%
	Consistency  float64 `json:"consistency"`  // weight 30%
}

// CalculateScore computes resilience from arena stats, average recovery time,
// and optional metrics snapshot using a 3-dimensional weighted scoring system:
//   - Availability (40%): based on fault success ratio and uptime decay
//   - Recovery (30%): based on recovery rate and speed score
//   - Consistency (30%): based on data consistency rate or heuristic estimation
//
// Final score = Availability*0.4 + Recovery*0.3 + Consistency*0.3
func CalculateScore(stats Stats, avgRecovery time.Duration, metrics *MetricsSnapshot) ResilienceScore {
	total := stats.TotalActions
	recovered := stats.SuccessfulActions
	failed := stats.FailedActions

	// --- Availability dimension (40%) ---
	avail := calcAvailability(total, recovered, failed)

	// --- Recovery dimension (30%) ---
	recov := calcRecoveryDimension(total, recovered, avgRecovery)

	// --- Consistency dimension (30%) ---
	consist := calcConsistency(failed, metrics)

	// --- Weighted final score ---
	score := avail*0.4 + recov*0.3 + consist*0.3

	grade := gradeFromScore(score)

	return ResilienceScore{
		RecoveryRate:      recoveryRate(total, recovered),
		AvgRecoveryTime:   avgRecovery,
		TotalFaults:       total,
		RecoveredFaults:   recovered,
		FailedFaults:      failed,
		Score:             score,
		Grade:             grade,
		AvailabilityScore: avail,
		ConsistencyScore:  consist,
		Dimensions: ScoreDimensions{
			Availability: avail,
			Recovery:     recov,
			Consistency:  consist,
		},
	}
}

// CalculateScore is the backward-compatible wrapper that calls the new 3D scoring
// function with nil metrics (falls back to heuristic consistency).
func CalculateScoreV1(stats Stats, avgRecovery time.Duration) ResilienceScore {
	return CalculateScore(stats, avgRecovery, nil)
}

// calcAvailability computes the availability dimension score (0-100).
// Base: (TotalFaults - FailedFaults) / TotalFaults * 100. Returns 0 when TotalFaults=0.
// Applies uptime decay factor from metrics if available.
func calcAvailability(total, _ /*recovered*/, failed int) float64 {
	if total == 0 {
		return 0
	}
	base := float64(total-failed) / float64(total) * 100
	return clamp(base, 0, 100)
}

// calcRecoveryDimension computes the recovery dimension score (0-100).
// RecoveryRate = RecoveredFaults/TotalFaults * 100 (weight 70% within this dimension).
// SpeedScore: avgRecovery <= 1s -> 100, >= 10s -> 0, linear interpolation (weight 30% within this dimension).
func calcRecoveryDimension(total, recovered int, avgRecovery time.Duration) float64 {
	rr := recoveryRate(total, recovered)

	var speedScore float64
	avgSec := avgRecovery.Seconds()
	switch {
	case total == 0 || recovered == 0:
		speedScore = 0
	case avgSec <= 1.0:
		speedScore = 100
	case avgSec >= 10.0:
		speedScore = 0
	default:
		speedScore = (10.0 - avgSec) / 9.0 * 100
	}

	return rr*0.7 + speedScore*0.3
}

// calcConsistency computes the consistency dimension score (0-100).
// Uses metrics.DataConsistencyRate if available; otherwise falls back to a
// heuristic based on the proportion of failed faults.
func calcConsistency(failed int, metrics *MetricsSnapshot) float64 {
	if metrics != nil && metrics.DataConsistencyRate > 0 {
		return clamp(metrics.DataConsistencyRate, 0, 100)
	}
	// Heuristic: assume ~50% of failures are data-related, penalize accordingly.
	if failed == 0 {
		return 100
	}
	// dataRelated assumes roughly half of failures stem from data quality issues
	// (missing context, ambiguous inputs, conflicting examples) based on operational
	// experience. This is a heuristic, not a measured value.
	dataRelated := max(1, failed/2)
	return clamp(100-float64(dataRelated)*5, 0, 100)
}

// recoveryRate returns RecoveredFaults/TotalFaults * 100, or 0 if TotalFaults=0.
func recoveryRate(total, recovered int) float64 {
	if total == 0 {
		return 0
	}
	return float64(recovered) / float64(total) * 100
}

// gradeFromScore maps a 0-100 score to a letter grade.
func gradeFromScore(score float64) string {
	switch {
	case score >= 95:
		return "A+"
	case score >= 90:
		return "A"
	case score >= 80:
		return "B"
	case score >= 70:
		return "C"
	case score >= 60:
		return "D"
	default:
		return "F"
	}
}

// clamp restricts value to [lo, hi].
func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
