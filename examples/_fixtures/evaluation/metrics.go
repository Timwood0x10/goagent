// Package evaluation provides a framework for benchmarking ARES agent capabilities.
package evaluation

import "time"

// Metrics captures the results of a single evaluation run.
type Metrics struct {
	// Core identity.
	Scenario string `json:"scenario"`
	Task     string `json:"task"`

	// Functional correctness.
	Success bool    `json:"success"`
	Score   float64 `json:"score"` // 0.0 - 1.0
	Error   string  `json:"error,omitempty"`

	// Performance.
	Latency    time.Duration `json:"latency_ms"`
	TokenCount int           `json:"token_count"`
	ToolCalls  int           `json:"tool_calls"`

	// Memory.
	MemoryHit  bool `json:"memory_hit"`
	MemorySize int  `json:"memory_size"`

	// Evolution.
	Generation     int     `json:"generation,omitempty"`
	ScoreBefore    float64 `json:"score_before,omitempty"`
	ScoreAfter     float64 `json:"score_after,omitempty"`
	EvoImprovement float64 `json:"evo_improvement,omitempty"` // percentage

	// Chaos / resilience.
	InjectedFaults int           `json:"injected_faults,omitempty"`
	RecoveredCount int           `json:"recovered_count,omitempty"`
	RecoveryTime   time.Duration `json:"recovery_time_ms,omitempty"`

	// Stability.
	CrashCount    int `json:"crash_count,omitempty"`
	FailoverCount int `json:"failover_count,omitempty"`
}

// Report aggregates metrics across multiple runs.
type Report struct {
	Name        string        `json:"name"`
	Date        string        `json:"date"`
	Runs        int           `json:"runs"`
	Passed      int           `json:"passed"`
	Failed      int           `json:"failed"`
	PassRate    float64       `json:"pass_rate"`
	AvgScore    float64       `json:"avg_score"`
	AvgLatency  time.Duration `json:"avg_latency_ms"`
	MaxLatency  time.Duration `json:"max_latency_ms"`
	TotalTokens int           `json:"total_tokens"`
	Results     []Metrics     `json:"results"`
}

// Aggregate computes summary statistics from a slice of Metrics.
func Aggregate(name string, results []Metrics) Report {
	r := Report{Name: name, Date: time.Now().Format(time.RFC3339)}
	r.Results = results
	r.Runs = len(results)

	var totalLatency time.Duration
	for _, m := range results {
		if m.Success {
			r.Passed++
		} else {
			r.Failed++
		}
		r.AvgScore += m.Score
		totalLatency += m.Latency
		r.TotalTokens += m.TokenCount
		if m.Latency > r.MaxLatency {
			r.MaxLatency = m.Latency
		}
	}
	if r.Runs > 0 {
		r.PassRate = float64(r.Passed) / float64(r.Runs) * 100
		r.AvgScore /= float64(r.Runs)
		r.AvgLatency = totalLatency / time.Duration(r.Runs)
	}
	return r
}
