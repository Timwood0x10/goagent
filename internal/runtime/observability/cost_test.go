package observability

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewCostTracker(t *testing.T) {
	pricing := DefaultPricingConfig()
	tracker := NewCostTracker(pricing)

	if tracker == nil {
		t.Fatal("expected non-nil tracker")
	}

	if tracker.TotalCost() != 0 {
		t.Errorf("expected initial cost 0, got: %.4f", tracker.TotalCost())
	}

	in, out := tracker.TotalTokens()
	if in != 0 || out != 0 {
		t.Errorf("expected initial tokens (0,0), got: (%d,%d)", in, out)
	}
}

func TestCostTracker_RecordCall_KnownModel(t *testing.T) {
	pricing := DefaultPricingConfig()
	tracker := NewCostTracker(pricing)

	// gpt-4o: $0.0025/1K input, $0.01/1K output
	// 1000 input tokens = $0.0025
	// 500 output tokens = $0.005
	// Total = $0.0075
	tracker.RecordCall("gpt-4o", 1000, 500)

	cost := tracker.TotalCost()
	expected := 0.0075
	if cost != expected {
		t.Errorf("expected cost %.4f, got: %.4f", expected, cost)
	}

	in, out := tracker.TotalTokens()
	if in != 1000 || out != 500 {
		t.Errorf("expected tokens (1000,500), got: (%d,%d)", in, out)
	}
}

func TestCostTracker_RecordCall_MultipleCalls(t *testing.T) {
	pricing := DefaultPricingConfig()
	tracker := NewCostTracker(pricing)

	tracker.RecordCall("gpt-4o", 1000, 500)
	tracker.RecordCall("gpt-3.5-turbo", 2000, 1000)

	cost := tracker.TotalCost()
	// gpt-4o: 1000*0.0025/1000 + 500*0.01/1000 = 0.0025 + 0.005 = 0.0075
	// gpt-3.5-turbo: 2000*0.0005/1000 + 1000*0.0015/1000 = 0.001 + 0.0015 = 0.0025
	// Total = 0.0100
	expected := 0.0100
	if cost != expected {
		t.Errorf("expected total cost %.4f, got: %.4f", expected, cost)
	}

	in, out := tracker.TotalTokens()
	if in != 3000 || out != 1500 {
		t.Errorf("expected tokens (3000,1500), got: (%d,%d)", in, out)
	}
}

func TestCostTracker_RecordCall_UnknownModel(t *testing.T) {
	pricing := PricingConfig{
		Models: map[string]ModelPricing{},
	}
	tracker := NewCostTracker(pricing)

	tracker.RecordCall("unknown-model", 1000, 500)

	cost := tracker.TotalCost()
	if cost != 0 {
		t.Errorf("expected cost 0 for unknown model, got: %.4f", cost)
	}

	entries := tracker.Entries()
	if len(entries) != 0 {
		t.Errorf("expected no entries for unknown model, got: %d", len(entries))
	}
}

func TestCostTracker_Entries(t *testing.T) {
	pricing := DefaultPricingConfig()
	tracker := NewCostTracker(pricing)

	tracker.RecordCall("gpt-4o", 100, 50)
	tracker.RecordCall("gpt-3.5-turbo", 200, 100)

	entries := tracker.Entries()
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got: %d", len(entries))
	}

	if entries[0].Model != "gpt-4o" {
		t.Errorf("expected first entry model gpt-4o, got: %s", entries[0].Model)
	}
	if entries[1].Model != "gpt-3.5-turbo" {
		t.Errorf("expected second entry model gpt-3.5-turbo, got: %s", entries[1].Model)
	}
}

func TestCostTracker_Entries_CopySafety(t *testing.T) {
	pricing := DefaultPricingConfig()
	tracker := NewCostTracker(pricing)

	tracker.RecordCall("gpt-4o", 100, 50)

	entries1 := tracker.Entries()
	entries2 := tracker.Entries()

	if &entries1[0] == &entries2[0] {
		t.Error("expected Entries() to return a copy, not the same slice")
	}
}

func TestCostTracker_Reset(t *testing.T) {
	pricing := DefaultPricingConfig()
	tracker := NewCostTracker(pricing)

	tracker.RecordCall("gpt-4o", 1000, 500)
	tracker.Reset()

	cost := tracker.TotalCost()
	if cost != 0 {
		t.Errorf("expected cost 0 after reset, got: %.4f", cost)
	}

	in, out := tracker.TotalTokens()
	if in != 0 || out != 0 {
		t.Errorf("expected tokens (0,0) after reset, got: (%d,%d)", in, out)
	}

	entries := tracker.Entries()
	if len(entries) != 0 {
		t.Errorf("expected 0 entries after reset, got: %d", len(entries))
	}
}

func TestCostTracker_Report_Empty(t *testing.T) {
	pricing := DefaultPricingConfig()
	tracker := NewCostTracker(pricing)

	report := tracker.Report()

	if !strings.Contains(report, "No calls recorded") {
		t.Error("expected report to contain 'No calls recorded' for empty tracker")
	}
	if !strings.Contains(report, "## Cost Summary") {
		t.Error("expected report to contain header")
	}
}

func TestCostTracker_Report_WithData(t *testing.T) {
	pricing := DefaultPricingConfig()
	tracker := NewCostTracker(pricing)

	tracker.RecordCall("gpt-4o", 1000, 500)

	report := tracker.Report()

	if !strings.Contains(report, "gpt-4o") {
		t.Error("expected report to contain model name")
	}
	if !strings.Contains(report, "1000") {
		t.Error("expected report to contain input token count")
	}
	if !strings.Contains(report, "500") {
		t.Error("expected report to contain output token count")
	}
	if !strings.Contains(report, "$0.0075") {
		t.Error("expected report to contain cost value")
	}
}

func TestCostTracker_NilTracker(t *testing.T) {
	var tracker *CostTracker

	cost := tracker.TotalCost()
	if cost != 0 {
		t.Errorf("expected 0 for nil tracker, got: %.4f", cost)
	}

	in, out := tracker.TotalTokens()
	if in != 0 || out != 0 {
		t.Errorf("expected (0,0) for nil tracker, got: (%d,%d)", in, out)
	}

	entries := tracker.Entries()
	if entries != nil {
		t.Error("expected nil entries for nil tracker")
	}

	report := tracker.Report()
	if !strings.Contains(report, "No cost data available") {
		t.Error("expected 'No cost data available' for nil tracker")
	}

	tracker.RecordCall("gpt-4o", 100, 50)
	tracker.Reset()
}

func TestCostTracker_ConcurrentRecordCalls(t *testing.T) {
	pricing := DefaultPricingConfig()
	tracker := NewCostTracker(pricing)

	var wg sync.WaitGroup
	numGoroutines := 10
	callsPerGoroutine := 100

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(model string) {
			defer wg.Done()
			for j := 0; j < callsPerGoroutine; j++ {
				tracker.RecordCall(model, 100, 50)
			}
		}("gpt-4o")
	}

	wg.Wait()

	entries := tracker.Entries()
	expectedEntries := numGoroutines * callsPerGoroutine
	if len(entries) != expectedEntries {
		t.Errorf("expected %d entries, got: %d", expectedEntries, len(entries))
	}

	in, out := tracker.TotalTokens()
	expectedIn := expectedEntries * 100
	expectedOut := expectedEntries * 50
	if in != expectedIn || out != expectedOut {
		t.Errorf("expected tokens (%d,%d), got: (%d,%d)",
			expectedIn, expectedOut, in, out)
	}
}

func TestDefaultPricingConfig(t *testing.T) {
	pricing := DefaultPricingConfig()

	expectedModels := []string{"gpt-4o", "gpt-4o-mini", "gpt-3.5-turbo", "claude-3.5-sonnet"}
	for _, model := range expectedModels {
		p, ok := pricing.Models[model]
		if !ok {
			t.Errorf("expected model %s in default pricing", model)
			continue
		}
		if p.InputCostPer1K <= 0 {
			t.Errorf("expected positive input cost for %s", model)
		}
		if p.OutputCostPer1K <= 0 {
			t.Errorf("expected positive output cost for %s", model)
		}
	}
}

func TestCostEntry_Timestamp(t *testing.T) {
	pricing := DefaultPricingConfig()
	tracker := NewCostTracker(pricing)

	before := time.Now()
	tracker.RecordCall("gpt-4o", 100, 50)
	after := time.Now()

	entries := tracker.Entries()
	if len(entries) != 1 {
		t.Fatal("expected 1 entry")
	}

	entry := entries[0]
	if entry.Timestamp.Before(before) || entry.Timestamp.After(after) {
		t.Errorf("timestamp %v outside expected range [%v, %v]",
			entry.Timestamp, before, after)
	}
}
