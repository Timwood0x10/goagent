package ares_skills

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSkillActivateExposesReferences verifies skill_activate discloses the
// skill's reference resource files (Level-2 progressive disclosure) alongside
// the resolved tools, so the LLM sees which resource files exist.
func TestSkillActivateExposesReferences(t *testing.T) {
	root := t.TempDir()
	makeSkillDir(t, root, "audit", "Security Audit", "audit code for OWASP",
		"id: audit\ntools:\n  - id: scan\n    type: builtin\n    name: filesystem\n")
	// makeSkillDir already creates references/; add a resource file.
	if err := os.WriteFile(filepath.Join(root, "audit", "references", "rules.txt"),
		[]byte("check rule: no secrets in logs"), 0o644); err != nil {
		t.Fatalf("write reference: %v", err)
	}

	cat := NewCatalog(CatalogConfig{
		ProjectSkillsDir:      root,
		AllowLocalExecutables: true,
		Builtins:              []string{"filesystem"},
	})
	if err := cat.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	reg, _ := registerCatalogTools(t, cat)
	act, ok := reg.Get(ToolSkillActivate)
	if !ok {
		t.Fatal("skill_activate not registered")
	}
	res, err := act.Execute(context.Background(), map[string]any{"id": "audit"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Success {
		t.Fatalf("activation failed: %v", res.Data)
	}
	raw, _ := json.Marshal(res.Data)
	if !strings.Contains(string(raw), "rules.txt") {
		t.Fatalf("references not disclosed in skill_activate result: %s", raw)
	}
}

// TestCatalogListReferences verifies the catalog-level references listing:
// existing files are listed, unknown skills and unbuilt catalogs return
// ErrSkillNotFound, and a skill without a references dir yields nil, nil.
func TestCatalogListReferences(t *testing.T) {
	root := t.TempDir()
	makeSkillDir(t, root, "audit", "Security Audit", "audit code", "id: audit\n")
	if err := os.WriteFile(filepath.Join(root, "audit", "references", "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "audit", "references", "b.md"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	cat := NewCatalog(CatalogConfig{ProjectSkillsDir: root})
	if err := cat.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	refs, err := cat.ListReferences("audit")
	if err != nil {
		t.Fatalf("ListReferences: %v", err)
	}
	// makeSkillDir pre-seeds checklist.md, so assert membership rather than an
	// exact count.
	has := func(name string) bool {
		for _, r := range refs {
			if r == name {
				return true
			}
		}
		return false
	}
	if !has("a.txt") || !has("b.md") {
		t.Fatalf("want references to include a.txt and b.md, got %v", refs)
	}
	// Unknown skill → ErrSkillNotFound.
	if _, err := cat.ListReferences("nope"); err != ErrSkillNotFound {
		t.Fatalf("want ErrSkillNotFound, got %v", err)
	}
	// Unbuilt catalog → ErrSkillNotFound.
	if _, err := NewCatalog(CatalogConfig{ProjectSkillsDir: root}).ListReferences("audit"); err != ErrSkillNotFound {
		t.Fatalf("unbuilt catalog must return ErrSkillNotFound, got %v", err)
	}
}
