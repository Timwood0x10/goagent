// Package experience provides types for managing evolution experiences.
// This file defines RawExperience and NormalizerConfig used by the normalizer.
package experience

import (
	"sort"
	"time"
)

// RawExperience represents unprocessed experience data collected from various sources.
// It contains fields that may be incomplete, have inconsistent types, or require
// normalization before being used in the evolution system.
type RawExperience struct {
	// StrategyID is the unique identifier of the strategy that generated this experience.
	StrategyID string

	// TaskType categorizes the type of task (e.g., "code_generation", "analysis").
	TaskType string

	// Timestamp is when the experience was recorded.
	Timestamp time.Time

	// Score is the fitness score achieved (may be in various ranges).
	Score interface{}

	// Latency is the execution latency (may be in various units).
	Latency interface{}

	// WallTime is the wall-clock execution time (may be in various units).
	WallTime interface{}

	// ErrorRate is the error rate (may be percentage or fraction).
	ErrorRate interface{}

	// Success indicates whether the task was completed successfully.
	Success interface{}

	// Cost is the computational cost incurred.
	Cost interface{}

	// MutationType describes the type of mutation used.
	MutationType string

	// Metadata holds additional unstructured data.
	Metadata map[string]interface{}
}

// NormalizerConfig holds configuration parameters for the normalizer.
type NormalizerConfig struct {
	// MaxLatencyMs is the maximum acceptable latency in milliseconds.
	// Experiences with latency above this threshold are filtered as noise.
	// Default: 10000 (10 seconds).
	MaxLatencyMs int64

	// MaxErrorRate is the maximum acceptable error rate [0, 1].
	// Experiences with error rate above this threshold are filtered as noise.
	// Default: 1.0 (disabled).
	MaxErrorRate float64

	// DedupWindowMinutes is the time window in minutes for deduplication.
	// Experiences with same strategy_id + task_type within this window are duplicates.
	// Default: 1 minute.
	DedupWindowMinutes int

	// DefaultScore is the default score for missing values.
	// Default: 0.0.
	DefaultScore float64

	// DefaultLatencyMs is the default latency for missing values.
	// Default: 0.
	DefaultLatencyMs int64

	// DefaultWallTimeSeconds is the default wall time for missing values.
	// Default: 0.0.
	DefaultWallTimeSeconds float64

	// DefaultErrorRate is the default error rate for missing values.
	// Default: 0.0.
	DefaultErrorRate float64

	// DefaultSuccess is the default success value for missing values.
	// Default: false.
	DefaultSuccess bool

	// DefaultCost is the default cost for missing values.
	// Default: 0.0.
	DefaultCost float64
}

// DefaultNormalizerConfig returns a NormalizerConfig with sensible defaults.
func DefaultNormalizerConfig() *NormalizerConfig {
	return &NormalizerConfig{
		MaxLatencyMs:           10000, // 10 seconds
		MaxErrorRate:           1.0,   // Allow up to 100% error rate (disabled)
		DedupWindowMinutes:     1,
		DefaultScore:           0.0,
		DefaultLatencyMs:       0,
		DefaultWallTimeSeconds: 0.0,
		DefaultErrorRate:       0.0,
		DefaultSuccess:         false,
		DefaultCost:            0.0,
	}
}

// ToolCallRecord represents raw execution data from tool calls.
// It captures detailed metrics about individual tool invocations including
// timing, success/failure status, and result characteristics.
// This is used by the GA/Memory/Tool fusion system to track tool performance.
type ToolCallRecord struct {
	// StrategyID is the identifier of the strategy that initiated this tool call.
	StrategyID string `json:"strategy_id"`

	// TaskType is the type of task being executed (e.g., "code_generation", "analysis").
	TaskType string `json:"task_type"`

	// ToolName is the name of the tool that was invoked.
	ToolName string `json:"tool_name"`

	// InputSummary is a concise summary of the input parameters.
	// This is a truncated or hashed representation to avoid excessive storage.
	InputSummary string `json:"input_summary"`

	// OutputSummary is a concise summary of the output or result.
	// This captures key information without storing full responses.
	OutputSummary string `json:"output_summary"`

	// ErrorCode is the error code if the tool call failed, empty on success.
	// This follows standard error code conventions (e.g., "TIMEOUT", "INVALID_INPUT").
	ErrorCode string `json:"error_code"`

	// LatencyMs is the execution latency in milliseconds.
	LatencyMs int64 `json:"latency_ms"`

	// Success indicates whether the tool call completed successfully.
	Success bool `json:"success"`

	// Timestamp is when the tool call was initiated.
	Timestamp time.Time `json:"timestamp"`

	// RetryCount is the number of retry attempts made for this tool call.
	RetryCount int `json:"retry_count"`

	// ResultSizeBytes is the size of the result in bytes.
	ResultSizeBytes int64 `json:"result_size_bytes"`
}

// Evidence represents multi-dimensional aggregated statistics.
// It aggregates multiple normalized experiences into statistical summaries
// to support strategy evaluation and selection decisions.
// This is used by the GA/Memory/Tool fusion system for strategy comparison.
type Evidence struct {
	// StrategyID is the identifier of the strategy this evidence applies to.
	StrategyID string `json:"strategy_id"`

	// TaskType is the type of task this evidence covers.
	TaskType string `json:"task_type"`

	// SuccessRate is the proportion of successful executions (0.0 to 1.0).
	SuccessRate float64 `json:"success_rate"`

	// LatencyP50 is the 50th percentile (median) latency in milliseconds.
	LatencyP50 int64 `json:"latency_p50"`

	// LatencyP95 is the 95th percentile latency in milliseconds.
	LatencyP95 int64 `json:"latency_p95"`

	// ErrorRate is the overall proportion of errors encountered (0.0 to 1.0).
	ErrorRate float64 `json:"error_rate"`

	// SampleCount is the number of experiences aggregated into this evidence.
	SampleCount int64 `json:"sample_count"`

	// ToolChainHash is a hash representing the most common tool chain pattern.
	// Populated from NormalizedExperience.ToolChain when available.
	ToolChainHash string `json:"tool_chain_hash"`

	// Confidence is the confidence level of this evidence (0.0 to 1.0).
	// Higher values indicate more reliable statistics based on larger sample sizes.
	Confidence float64 `json:"confidence"`

	// LastUpdated is when this evidence was last updated.
	LastUpdated time.Time `json:"last_updated"`
}

// IsEmpty returns true if the ToolCallRecord has no meaningful data.
// A record is considered empty if StrategyID and ToolName are both empty.
func (r *ToolCallRecord) IsEmpty() bool {
	return r.StrategyID == "" && r.ToolName == ""
}

// IsEmpty returns true if the Evidence has no meaningful data.
// Evidence is considered empty if StrategyID is empty.
func (e *Evidence) IsEmpty() bool {
	return e.StrategyID == ""
}

// HasSamples returns true if the Evidence has at least one sample.
func (e *Evidence) HasSamples() bool {
	return e.SampleCount > 0
}

// AggregateEvidence combines multiple NormalizedExperience values into a single
// Evidence. This is the canonical aggregation path: NormalizedExperience →
// Evidence. All callers should use this function instead of implementing
// their own aggregation logic.
//
// Returns an empty Evidence if the input slice is empty.
func AggregateEvidence(experiences []NormalizedExperience) Evidence {
	if len(experiences) == 0 {
		return Evidence{}
	}

	var (
		totalLatency   int64
		successCount   int
		totalErrorRate float64
		strategyID     string
		taskType       string
		toolChainHash  string
		lastTimestamp  time.Time
		latencyValues  []int64
	)

	for _, exp := range experiences {
		if exp.StrategyID != "" {
			if strategyID == "" {
				strategyID = exp.StrategyID
			} else if strategyID != exp.StrategyID {
				log.Warn("AggregateEvidence: mixed strategy IDs in batch",
					"first", strategyID, "encountered", exp.StrategyID)
			}
		}
		if exp.TaskType != "" {
			if taskType == "" {
				taskType = exp.TaskType
			} else if taskType != exp.TaskType {
				log.Warn("AggregateEvidence: mixed task types in batch",
					"first", taskType, "encountered", exp.TaskType)
			}
		}
		if exp.ToolChain != "" && toolChainHash == "" {
			toolChainHash = exp.ToolChain
		}
		if exp.CreatedAt.After(lastTimestamp) {
			lastTimestamp = exp.CreatedAt
		}

		totalLatency += exp.LatencyMs
		totalErrorRate += exp.ErrorRate
		if exp.Success || exp.Outcome == "success" {
			successCount++
		}
		latencyValues = append(latencyValues, exp.LatencyMs)
	}

	count := float64(len(experiences))

	latencyP50, latencyP95 := calculateLatencyPercentiles(latencyValues)

	return Evidence{
		StrategyID:    strategyID,
		TaskType:      taskType,
		SuccessRate:   float64(successCount) / count,
		LatencyP50:    latencyP50,
		LatencyP95:    latencyP95,
		ErrorRate:     totalErrorRate / count,
		SampleCount:   int64(len(experiences)),
		ToolChainHash: toolChainHash,
		Confidence:    0.0,
		LastUpdated:   lastTimestamp,
	}
}

// AggregateEvidenceByTask groups NormalizedExperience values by TaskType
// and produces aggregated Evidence for each group. Returns a map keyed by
// TaskType. This avoids the mixed-task-type warning from AggregateEvidence
// when experiences span multiple task types.
//
// Returns an empty map if the input slice is empty.
func AggregateEvidenceByTask(experiences []NormalizedExperience) map[string]Evidence {
	result := make(map[string]Evidence)
	if len(experiences) == 0 {
		return result
	}

	byTask := make(map[string][]NormalizedExperience)
	for _, exp := range experiences {
		taskType := exp.TaskType
		if taskType == "" {
			taskType = "unknown"
		}
		byTask[taskType] = append(byTask[taskType], exp)
	}

	for taskType, exps := range byTask {
		result[taskType] = AggregateEvidence(exps)
	}

	return result
}

// AggregateEvidenceCrossTask aggregates experiences across multiple task types
// silently, without emitting mixed-task-type or mixed-strategy-ID warnings.
// This is intended for display/demo contexts where cross-task aggregation is
// intentional and the warnings would be noisy. For strict homogeneous batches,
// use AggregateEvidence instead.
func AggregateEvidenceCrossTask(experiences []NormalizedExperience) Evidence {
	if len(experiences) == 0 {
		return Evidence{}
	}

	var (
		totalLatency   int64
		successCount   int
		totalErrorRate float64
		strategyID     string
		toolChainHash  string
		lastTimestamp  time.Time
		latencyValues  []int64
	)

	for _, exp := range experiences {
		if exp.StrategyID != "" && strategyID == "" {
			strategyID = exp.StrategyID
		}
		if exp.ToolChain != "" && toolChainHash == "" {
			toolChainHash = exp.ToolChain
		}
		if exp.CreatedAt.After(lastTimestamp) {
			lastTimestamp = exp.CreatedAt
		}
		totalLatency += exp.LatencyMs
		totalErrorRate += exp.ErrorRate
		if exp.Success || exp.Outcome == "success" {
			successCount++
		}
		latencyValues = append(latencyValues, exp.LatencyMs)
	}

	count := float64(len(experiences))
	latencyP50, latencyP95 := calculateLatencyPercentiles(latencyValues)

	return Evidence{
		StrategyID:    strategyID,
		SuccessRate:   float64(successCount) / count,
		LatencyP50:    latencyP50,
		LatencyP95:    latencyP95,
		ErrorRate:     totalErrorRate / count,
		SampleCount:   int64(len(experiences)),
		ToolChainHash: toolChainHash,
		Confidence:    calculateSampleConfidence(int64(len(experiences))),
		LastUpdated:   lastTimestamp,
	}
}

func calculateSampleConfidence(sampleCount int64) float64 {
	if sampleCount <= 0 {
		return 0.0
	}

	const (
		minSampleCount int64 = 10
		maxSampleCount int64 = 1000
	)

	if sampleCount >= maxSampleCount {
		return 1.0
	}
	if sampleCount < minSampleCount {
		return float64(sampleCount) / float64(minSampleCount) * 0.5
	}

	rangeSize := maxSampleCount - minSampleCount
	position := sampleCount - minSampleCount
	return float64(position)/float64(rangeSize)*0.5 + 0.5
}

// calculateLatencyPercentiles computes p50 and p95 from a slice of raw
// latency values in milliseconds.
func calculateLatencyPercentiles(values []int64) (p50 int64, p95 int64) {
	if len(values) == 0 {
		return 0, 0
	}

	sorted := make([]int64, len(values))
	copy(sorted, values)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	p50Index := len(sorted) / 2
	p50 = sorted[p50Index]

	p95Index := int(float64(len(sorted)) * 0.95)
	if p95Index >= len(sorted) {
		p95Index = len(sorted) - 1
	}
	p95 = sorted[p95Index]

	return p50, p95
}
