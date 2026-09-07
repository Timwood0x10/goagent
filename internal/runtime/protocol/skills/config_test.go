package ares_skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestLoadRegisteredSkillDirsParsesConfig verifies [[skill_sources]] parsing,
// "~" expansion, type filtering and dedup.
func TestLoadRegisteredSkillDirsParsesConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	content := `
[[skill_sources]]
type = "directory"
path = "/abs/skills"

[[skill_sources]]
type = "directory"
path = "~/my-company/skills"

[[skill_sources]]
type = "git"
url = "https://example.com/repo.git"

[[skill_sources]]
type = "directory"
path = "/abs/skills"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	dirs, err := LoadRegisteredSkillDirs(cfgPath)
	if err != nil {
		t.Fatalf("LoadRegisteredSkillDirs: %v", err)
	}
	// "~" expanded, git skipped, duplicate dropped.
	if len(dirs) != 2 {
		t.Fatalf("want 2 dirs, got %v", dirs)
	}
	if dirs[0] != "/abs/skills" {
		t.Fatalf("first dir want /abs/skills, got %q", dirs[0])
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if dirs[1] != filepath.Join(home, "my-company", "skills") {
		t.Fatalf("tilde expansion wrong: %q", dirs[1])
	}
}

func TestLoadRegisteredSkillDirsMissingFileIsEmpty(t *testing.T) {
	dirs, err := LoadRegisteredSkillDirs(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("missing config must not error, got %v", err)
	}
	if len(dirs) != 0 {
		t.Fatalf("want no dirs, got %v", dirs)
	}
}

// TestDetectIndexChanges verifies added/modified/removed classification by
// content hash.
func TestDetectIndexChanges(t *testing.T) {
	prev := []SkillIndexEntry{
		{ID: "a", Source: SourceProject, Hash: "h1"},
		{ID: "b", Source: SourceProject, Hash: "h2"},
		{ID: "c", Source: SourceProject, Hash: "h3"},
	}
	next := []SkillIndexEntry{
		{ID: "a", Source: SourceProject, Hash: "h1"}, // unchanged
		{ID: "b", Source: SourceProject, Hash: "H2"}, // modified
		{ID: "d", Source: SourceProject, Hash: "h4"}, // added
	}
	change := DetectIndexChanges(prev, next)
	if len(change.Added) != 1 || change.Added[0].ID != "d" {
		t.Fatalf("added want [d], got %+v", change.Added)
	}
	if len(change.Modified) != 1 || change.Modified[0].ID != "b" {
		t.Fatalf("modified want [b], got %+v", change.Modified)
	}
	if len(change.Removed) != 1 || change.Removed[0].ID != "c" {
		t.Fatalf("removed want [c], got %+v", change.Removed)
	}
}

// TestCatalogRefreshDetectsChanges verifies Refresh re-indexes and reports the
// diff: adding a skill and touching another yields Added+Modified.
func TestCatalogRefreshDetectsChanges(t *testing.T) {
	root := t.TempDir()
	makeSkillDir(t, root, "alpha", "Alpha", "alpha desc", "")
	cat := NewCatalog(CatalogConfig{ProjectSkillsDir: root})
	if err := cat.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Add a second skill and bump the first one's content.
	makeSkillDir(t, root, "beta", "Beta", "beta desc", "")
	alphaDir := filepath.Join(root, "alpha")
	skillMD := "---\nid: alpha\nname: Alpha\nversion: 2.0.0\n---\n\n# Alpha v2\n"
	if err := os.WriteFile(filepath.Join(alphaDir, "SKILL.md"), []byte(skillMD), 0o644); err != nil {
		t.Fatal(err)
	}

	change, err := cat.Refresh()
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(change.Added) != 1 || change.Added[0].ID != "beta" {
		t.Fatalf("added want [beta], got %+v", change.Added)
	}
	if len(change.Modified) != 1 || change.Modified[0].ID != "alpha" {
		t.Fatalf("modified want [alpha], got %+v", change.Modified)
	}
	if len(change.Removed) != 0 {
		t.Fatalf("removed want none, got %+v", change.Removed)
	}
	// The refreshed index must carry the bumped version (front matter
	// version 2.0.0 is metadata, searchable via the index entry).
	refreshed := cat.All()
	var alphaEntry *SkillIndexEntry
	for i := range refreshed {
		if refreshed[i].ID == "alpha" {
			alphaEntry = &refreshed[i]
		}
	}
	if alphaEntry == nil {
		t.Fatal("alpha must remain indexed after refresh")
	}
	if alphaEntry.Version != "2.0.0" {
		t.Fatalf("refreshed alpha version want 2.0.0, got %q", alphaEntry.Version)
	}
}

// TestExperiencePersistsAcrossStores verifies JSON store round-trip: records
// recorded on one store are visible after reload from the file.
func TestExperiencePersistsAcrossStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "experience.json")
	store := NewJSONExperienceStore(path)

	e1 := NewExperienceWithStore(context.Background(), store)
	if err := e1.Record("pdf-gen", "document-to-pdf", 0.94); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// A fresh store reading the same file sees the persisted record.
	e2 := NewExperienceWithStore(context.Background(), NewJSONExperienceStore(path))
	best, ok := e2.BestMatch("document-to-pdf")
	if !ok || best.SuccessRate != 0.94 || best.Skill != "pdf-gen" {
		t.Fatalf("persisted record not reloaded: %+v ok=%v", best, ok)
	}
}
