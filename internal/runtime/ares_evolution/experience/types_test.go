// Package experience provides core data structures for the GA/Memory/Tool
// fusion system.
package experience

import (
	"testing"
	"time"
)

func TestToolCallRecordIsEmpty(t *testing.T) {
	tests := []struct {
		name     string
		record   ToolCallRecord
		expected bool
	}{
		{
			name:     "empty record",
			record:   ToolCallRecord{},
			expected: true,
		},
		{
			name: "record with strategy ID",
			record: ToolCallRecord{
				StrategyID: "strategy-123",
			},
			expected: false,
		},
		{
			name: "fully populated record",
			record: ToolCallRecord{
				StrategyID:      "strategy-123",
				TaskType:        "code_generation",
				ToolName:        "search",
				LatencyMs:       150,
				Success:         true,
				Timestamp:       time.Now(),
				RetryCount:      0,
				ResultSizeBytes: 1024,
			},
			expected: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := test.record.IsEmpty()
			if result != test.expected {
				t.Errorf("IsEmpty() = %v, expected %v", result, test.expected)
			}
		})
	}
}

func TestEvidenceIsEmpty(t *testing.T) {
	tests := []struct {
		name     string
		evidence Evidence
		expected bool
	}{
		{
			name:     "empty evidence",
			evidence: Evidence{},
			expected: true,
		},
		{
			name: "evidence with strategy ID",
			evidence: Evidence{
				StrategyID:  "strategy-123",
				SampleCount: 10,
			},
			expected: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := test.evidence.IsEmpty()
			if result != test.expected {
				t.Errorf("IsEmpty() = %v, expected %v", result, test.expected)
			}
		})
	}
}

func TestEvidenceHasSamples(t *testing.T) {
	tests := []struct {
		name     string
		evidence Evidence
		expected bool
	}{
		{
			name:     "zero samples",
			evidence: Evidence{SampleCount: 0},
			expected: false,
		},
		{
			name:     "one sample",
			evidence: Evidence{SampleCount: 1},
			expected: true,
		},
		{
			name:     "many samples",
			evidence: Evidence{SampleCount: 100},
			expected: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := test.evidence.HasSamples()
			if result != test.expected {
				t.Errorf("HasSamples() = %v, expected %v", result, test.expected)
			}
		})
	}
}

func TestAggregateEvidence(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name                string
		experiences         []NormalizedExperience
		expectedEmpty       bool
		expectedSampleCount int64
		expectedSuccessRate float64
	}{
		{
			name:                "empty slice",
			experiences:         []NormalizedExperience{},
			expectedEmpty:       true,
			expectedSampleCount: 0,
		},
		{
			name: "single experience",
			experiences: []NormalizedExperience{
				{
					StrategyID: "strategy-123",
					TaskType:   "code_generation",
					Score:      0.8,
					Success:    true,
					Outcome:    "success",
					LatencyMs:  500,
					ErrorRate:  0.02,
					ToolChain:  "search|read",
					CreatedAt:  now,
				},
			},
			expectedEmpty:       false,
			expectedSampleCount: 1,
			expectedSuccessRate: 1.0,
		},
		{
			name: "multiple experiences",
			experiences: []NormalizedExperience{
				{
					StrategyID: "strategy-456",
					TaskType:   "analysis",
					Score:      0.9,
					Success:    true,
					Outcome:    "success",
					LatencyMs:  200,
					ErrorRate:  0.01,
					ToolChain:  "search|analyze",
					CreatedAt:  now,
				},
				{
					StrategyID: "strategy-456",
					TaskType:   "analysis",
					Score:      0.3,
					Success:    false,
					Outcome:    "failure",
					LatencyMs:  5000,
					ErrorRate:  0.4,
					ToolChain:  "search|analyze",
					CreatedAt:  now,
				},
			},
			expectedEmpty:       false,
			expectedSampleCount: 2,
			expectedSuccessRate: 0.5,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := AggregateEvidence(test.experiences)

			if result.IsEmpty() != test.expectedEmpty {
				t.Errorf("IsEmpty() = %v, expected %v", result.IsEmpty(), test.expectedEmpty)
			}

			if result.SampleCount != test.expectedSampleCount {
				t.Errorf("SampleCount = %v, expected %v", result.SampleCount, test.expectedSampleCount)
			}

			if !test.expectedEmpty && result.SuccessRate != test.expectedSuccessRate {
				t.Errorf("SuccessRate = %v, expected %v", result.SuccessRate, test.expectedSuccessRate)
			}

			if !test.expectedEmpty {
				if result.Confidence != 0.0 {
					t.Errorf("Confidence should be 0 (no aggregator), got %v", result.Confidence)
				}
				if result.ErrorRate < 0 || result.ErrorRate > 1.0 {
					t.Errorf("ErrorRate out of range [0, 1]: %v", result.ErrorRate)
				}
			}
		})
	}
}

func TestAggregateEvidenceTimestampHandling(t *testing.T) {
	earlier := time.Now().Add(-2 * time.Hour)
	latest := time.Now()

	experiences := []NormalizedExperience{
		{
			StrategyID: "strategy-time",
			TaskType:   "test",
			Score:      0.8,
			Success:    true,
			Outcome:    "success",
			CreatedAt:  earlier,
		},
		{
			StrategyID: "strategy-time",
			TaskType:   "test",
			Score:      0.6,
			Success:    true,
			Outcome:    "success",
			CreatedAt:  latest,
		},
	}

	result := AggregateEvidence(experiences)
	if !result.LastUpdated.Equal(latest) {
		t.Errorf("Expected LastUpdated to be %v, got %v", latest, result.LastUpdated)
	}
}

func TestAggregateEvidenceLatencyPercentiles(t *testing.T) {
	now := time.Now()
	experiences := []NormalizedExperience{
		{
			StrategyID: "strategy-latency",
			TaskType:   "test",
			Score:      0.8,
			Success:    true,
			Outcome:    "success",
			LatencyMs:  100,
			CreatedAt:  now,
		},
		{
			StrategyID: "strategy-latency",
			TaskType:   "test",
			Score:      0.7,
			Success:    true,
			Outcome:    "success",
			LatencyMs:  200,
			CreatedAt:  now,
		},
		{
			StrategyID: "strategy-latency",
			TaskType:   "test",
			Score:      0.9,
			Success:    true,
			Outcome:    "success",
			LatencyMs:  300,
			CreatedAt:  now,
		},
	}

	result := AggregateEvidence(experiences)

	// Sorted: [100, 200, 300], p50 (index 1) = 200, p95 (index 2) = 300.
	if result.LatencyP50 != 200 {
		t.Errorf("Expected LatencyP50 200, got %d", result.LatencyP50)
	}
	if result.LatencyP95 != 300 {
		t.Errorf("Expected LatencyP95 300, got %d", result.LatencyP95)
	}
}

func TestAggregateEvidenceCrossTask(t *testing.T) {
	now := time.Now()

	t.Run("empty input returns empty", func(t *testing.T) {
		result := AggregateEvidenceCrossTask(nil)
		if !result.IsEmpty() {
			t.Error("expected empty Evidence for nil input")
		}
		result = AggregateEvidenceCrossTask([]NormalizedExperience{})
		if !result.IsEmpty() {
			t.Error("expected empty Evidence for empty slice")
		}
	})

	t.Run("mixed task types aggregated together", func(t *testing.T) {
		experiences := []NormalizedExperience{
			{
				StrategyID: "strategy-A",
				TaskType:   "code_generation",
				Score:      0.9,
				Success:    true,
				Outcome:    "success",
				LatencyMs:  100,
				ErrorRate:  0.01,
				ToolChain:  "search|write",
				CreatedAt:  now,
			},
			{
				StrategyID: "strategy-A",
				TaskType:   "code_review",
				Score:      0.7,
				Success:    true,
				Outcome:    "success",
				LatencyMs:  200,
				ErrorRate:  0.03,
				ToolChain:  "search|read",
				CreatedAt:  now,
			},
			{
				StrategyID: "strategy-A",
				TaskType:   "testing",
				Score:      0.4,
				Success:    false,
				Outcome:    "failure",
				LatencyMs:  500,
				ErrorRate:  0.15,
				ToolChain:  "test|run",
				CreatedAt:  now,
			},
		}

		result := AggregateEvidenceCrossTask(experiences)

		if result.SampleCount != 3 {
			t.Errorf("SampleCount = %d, want 3", result.SampleCount)
		}
		if result.SuccessRate != 2.0/3.0 {
			t.Errorf("SuccessRate = %.2f, want 0.67", result.SuccessRate)
		}
		if result.StrategyID != "strategy-A" {
			t.Errorf("StrategyID = %q, want strategy-A", result.StrategyID)
		}
		// Sorted latencies: [100, 200, 500], p50 (index 1) = 200, p95 (index 2) = 500.
		if result.LatencyP50 != 200 {
			t.Errorf("LatencyP50 = %d, want 200", result.LatencyP50)
		}
		if result.LatencyP95 != 500 {
			t.Errorf("LatencyP95 = %d, want 500", result.LatencyP95)
		}
		// ErrorRate averaged: (0.01 + 0.03 + 0.15) / 3 ≈ 0.063
		if result.ErrorRate < 0.06 || result.ErrorRate > 0.07 {
			t.Errorf("ErrorRate = %.4f, want ≈0.063", result.ErrorRate)
		}
		if result.Confidence <= 0 {
			t.Errorf("Confidence = %.2f, want positive sample-based confidence", result.Confidence)
		}
	})

	t.Run("mixed strategy IDs picks first", func(t *testing.T) {
		experiences := []NormalizedExperience{
			{
				StrategyID: "strategy-first",
				TaskType:   "task_a",
				Score:      0.8,
				Success:    true,
				Outcome:    "success",
				CreatedAt:  now,
			},
			{
				StrategyID: "strategy-second",
				TaskType:   "task_b",
				Score:      0.6,
				Success:    true,
				Outcome:    "success",
				CreatedAt:  now,
			},
		}

		result := AggregateEvidenceCrossTask(experiences)
		if result.StrategyID != "strategy-first" {
			t.Errorf("Expected first strategy ID %q, got %q", "strategy-first", result.StrategyID)
		}
	})

	t.Run("SampleCount and success from Outcome string", func(t *testing.T) {
		// Tests the fallback: Success bool is false but Outcome == "success" counts as success.
		experiences := []NormalizedExperience{
			{
				TaskType:  "task_x",
				Success:   false,
				Outcome:   "success",
				LatencyMs: 150,
				CreatedAt: now,
			},
			{
				TaskType:  "task_y",
				Success:   true,
				Outcome:   "",
				LatencyMs: 250,
				CreatedAt: now,
			},
		}

		result := AggregateEvidenceCrossTask(experiences)
		if result.SampleCount != 2 {
			t.Errorf("SampleCount = %d, want 2", result.SampleCount)
		}
		// Both should count as success: task_x via Outcome, task_y via Success bool.
		if result.SuccessRate != 1.0 {
			t.Errorf("SuccessRate = %.2f, want 1.0", result.SuccessRate)
		}
	})

	t.Run("empty TaskType is valid", func(t *testing.T) {
		experiences := []NormalizedExperience{
			{
				TaskType:  "",
				Score:     0.5,
				Success:   true,
				Outcome:   "",
				CreatedAt: now,
			},
		}
		result := AggregateEvidenceCrossTask(experiences)
		if result.SampleCount != 1 {
			t.Errorf("SampleCount = %d, want 1", result.SampleCount)
		}
		if result.SuccessRate != 1.0 {
			t.Errorf("empty TaskType should still be valid, SuccessRate = %.2f", result.SuccessRate)
		}
	})
}

func TestAggregateEvidenceToolChain(t *testing.T) {
	now := time.Now()

	experiences := []NormalizedExperience{
		{
			StrategyID: "strategy-tc",
			TaskType:   "test",
			Score:      0.8,
			Success:    true,
			Outcome:    "success",
			ToolChain:  "search|read",
			CreatedAt:  now,
		},
	}

	result := AggregateEvidence(experiences)
	if result.ToolChainHash != "search|read" {
		t.Errorf("Expected ToolChainHash 'search|read', got '%s'", result.ToolChainHash)
	}
}
