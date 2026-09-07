package ares_skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildSkillDir writes one skill (SKILL.md + skill.yaml) under root.
func buildSkillDir(tb testing.TB, root string, i int) {
	tb.Helper()
	id := fmt.Sprintf("skill-%03d", i)
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		tb.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(fmt.Sprintf(
		"# %s\n\ndescription: benchmark skill %d for catalog performance\n\n## When to use\n\nwhen the task involves domain %d\n", id, i, i)), 0o644); err != nil {
		tb.Fatalf("write SKILL.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "skill.yaml"), []byte(fmt.Sprintf(
		"id: %s\nname: %s\ndescription: benchmark skill %d\ntools:\n  - id: tool%d\n    type: builtin\n    name: filesystem\n", id, id, i, i)), 0o644); err != nil {
		tb.Fatalf("write skill.yaml: %v", err)
	}
}

// buildCatalog100 creates a catalog over 100 generated skills and builds it.
func buildCatalog100(tb testing.TB) *Catalog {
	tb.Helper()
	root := tb.TempDir()
	for i := 0; i < 100; i++ {
		buildSkillDir(tb, root, i)
	}
	cat := NewCatalog(CatalogConfig{
		ProjectSkillsDir:      root,
		AllowLocalExecutables: true,
		Builtins:              []string{"filesystem"},
	})
	if err := cat.Build(); err != nil {
		tb.Fatalf("Build: %v", err)
	}
	return cat
}

// BenchmarkCatalogBuild100Skills measures metadata indexing time for 100
// skills (design §5: zero-disk-scan, Level-0 only).
func BenchmarkCatalogBuild100Skills(b *testing.B) {
	root := b.TempDir()
	for i := 0; i < 100; i++ {
		buildSkillDir(b, root, i)
	}
	cat := NewCatalog(CatalogConfig{
		ProjectSkillsDir:      root,
		AllowLocalExecutables: true,
		Builtins:              []string{"filesystem"},
	})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := cat.Build(); err != nil {
			b.Fatalf("Build: %v", err)
		}
	}
}

// BenchmarkCatalogSearch100Skills measures Level-0 retrieval over 100 skills
// (Discovery prefers the FTS5 index; a miss falls back to keyword matching).
func BenchmarkCatalogSearch100Skills(b *testing.B) {
	cat := buildCatalog100(b)
	b.Run("fts5-hit", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = cat.Search("skill-050", 5)
		}
	})
	b.Run("keyword-fallback", func(b *testing.B) {
		// A query with FTS5 operators is unsafe and falls back to keyword
		// matching (discovery.go Search fallback path).
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = cat.Search("skill-", 5)
		}
	})
}

// BenchmarkExperienceBestMatch100 measures the relevance-prior lookup over
// 100 recorded patterns (the SkillOutcomeRecorder closed loop read side).
func BenchmarkExperienceBestMatch100(b *testing.B) {
	cat := buildCatalog100(b)
	exp := cat.Experience()
	for i := 0; i < 100; i++ {
		pattern := fmt.Sprintf("domain task %d benchmark", i)
		if err := exp.Record("skill-050", pattern, 0.9); err != nil {
			b.Fatalf("Record: %v", err)
		}
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = exp.BestMatch("domain task 50")
	}
}

// TestResidentMetadataTokenBudget verifies the progressive-disclosure promise:
// 100 skills keep only ~100 tokens of metadata resident (name + description),
// NOT full SKILL.md bodies — measured by the total resident-block size the
// memoryManager would prepend (manager_impl.go "Available skills" block).
func TestResidentMetadataTokenBudget(t *testing.T) {
	cat := buildCatalog100(t)
	var resident strings.Builder
	for _, e := range cat.All() {
		resident.WriteString(e.Name + ": " + e.Description + "\n")
	}
	// Rough token estimate: ~4 chars per token for English text. The promise
	// is ~100 tokens/skill, so 100 skills ≈ ~10k tokens ≈ ~40k chars.
	approxTokens := len(resident.String()) / 4
	t.Logf("100-skill resident metadata: %d chars ≈ ~%d tokens (~%d tokens/skill)",
		len(resident.String()), approxTokens, approxTokens/100)
	if approxTokens > 20_000 {
		t.Fatalf("resident metadata blew the token budget: ~%d tokens (want ~10k)", approxTokens)
	}
	// The resident block must never contain SKILL.md bodies (Level-0 only).
	if strings.Contains(resident.String(), "## When to use") {
		t.Fatal("resident metadata must not contain SKILL.md body content")
	}
}
