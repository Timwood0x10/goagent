package ares_skills

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestExperienceRecordCapsLongPattern verifies overlong task patterns (e.g.
// an unbounded precise task_desc) are truncated to maxPatternLength runes,
// keeping experience.json compact and BestMatch matching fast — while the
// truncated prefix stays matchable by short follow-up queries.
func TestExperienceRecordCapsLongPattern(t *testing.T) {
	cat := buildTestCatalog(t)
	exp := cat.Experience()

	long := strings.Repeat("长描述", 500) // 1500 runes
	if err := exp.Record("audit", long, 1.0); err != nil {
		t.Fatalf("Record long: %v", err)
	}
	best, ok := exp.BestMatch(strings.Repeat("长描述", 10)) // a prefix substring
	if !ok {
		t.Fatal("truncated pattern must still match via its prefix")
	}
	if best.Skill != "audit" || best.SuccessRate != 1.0 {
		t.Fatalf("unexpected prior: %+v", best)
	}
	if n := len([]rune(best.TaskPattern)); n > maxPatternLength {
		t.Fatalf("pattern not capped: %d runes", n)
	}

	// Normal-length patterns are untouched.
	if err := exp.Record("audit", "short pattern", 0.8); err != nil {
		t.Fatalf("Record short: %v", err)
	}
	short, ok := exp.BestMatch("short pattern")
	if !ok || short.TaskPattern != "short pattern" {
		t.Fatalf("short pattern must stay intact, got %+v", short)
	}
}

// TestCapPatternLength verifies rune-aware truncation: ASCII and multi-byte
// UTF-8 inputs are both capped at maxPatternLength runes without breaking
// UTF-8, and in-bound patterns are returned unchanged.
func TestCapPatternLength(t *testing.T) {
	// ASCII long input truncated to exactly maxPatternLength runes.
	if got := capPatternLength(strings.Repeat("a", 1000)); len([]rune(got)) != maxPatternLength {
		t.Fatalf("ascii: want %d runes, got %d", maxPatternLength, len([]rune(got)))
	}
	// Multi-byte UTF-8 must not be cut mid-rune.
	got := capPatternLength(strings.Repeat("测", 600)) // 600 runes × 3 bytes
	if !utf8.ValidString(got) {
		t.Fatal("truncation must not break UTF-8")
	}
	if n := len([]rune(got)); n != maxPatternLength {
		t.Fatalf("utf8: want %d runes, got %d", maxPatternLength, n)
	}
	// Within bound: unchanged.
	if got := capPatternLength("short"); got != "short" {
		t.Fatalf("short must be unchanged, got %q", got)
	}
}
