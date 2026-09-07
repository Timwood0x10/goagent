package ares_skills

import "testing"

// TestExperienceConfidenceSourceWithMatch verifies the adapter returns the
// best prior's success rate when Experience has a matching record, and that
// the value feeds the taskfabric scheduler score unchanged (design §8:
// Score's Confidence comes from Experience BestMatch SuccessRate).
func TestExperienceConfidenceSourceWithMatch(t *testing.T) {
	exp := NewExperience()
	if err := exp.Record("pdf-gen", "document-to-pdf", 0.94); err != nil {
		t.Fatalf("Record: %v", err)
	}
	src := NewExperienceConfidenceSource(exp)
	if got := src.Confidence("document-to-pdf"); got != 0.94 {
		t.Fatalf("want 0.94, got %v", got)
	}
}

// TestExperienceConfidenceSourceNoMatch verifies the adapter returns 0 when
// no prior matches — the candidate keeps its declared confidence or stays
// unscheduled (design §8: "0 means no experience yet").
func TestExperienceConfidenceSourceNoMatch(t *testing.T) {
	exp := NewExperience()
	if err := exp.Record("pdf-gen", "document-to-pdf", 0.94); err != nil {
		t.Fatalf("Record: %v", err)
	}
	src := NewExperienceConfidenceSource(exp)
	if got := src.Confidence("no-such-task"); got != 0 {
		t.Fatalf("want 0 for unmatched pattern, got %v", got)
	}
}

// TestExperienceConfidenceSourceNilSafe verifies the adapter is nil-safe on
// both the source and the wrapped Experience — a nil receiver or nil
// Experience yields 0 rather than panicking (code_rules §4.2: no panic on
// business paths).
func TestExperienceConfidenceSourceNilSafe(t *testing.T) {
	var src *ExperienceConfidenceSource
	if got := src.Confidence("anything"); got != 0 {
		t.Fatalf("nil receiver must yield 0, got %v", got)
	}
	src = NewExperienceConfidenceSource(nil)
	if got := src.Confidence("anything"); got != 0 {
		t.Fatalf("nil Experience must yield 0, got %v", got)
	}
}
