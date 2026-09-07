package ares_skills

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadSkillSourcesParsesGitAndHTTP verifies config parsing for git and
// http/oci source types (git URL + local_dir, http manifest_url).
func TestLoadSkillSourcesParsesGitAndHTTP(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	content := `
[[skill_sources]]
type = "git"
url = "https://example.com/skills.git"
local_dir = "~/.ares/cache/skills"

[[skill_sources]]
type = "http"
manifest_url = "https://example.com/manifest.json"

[[skill_sources]]
type = "oci"
manifest_url = "https://registry.example.com/skills"

[[skill_sources]]
type = "directory"
path = "/abs/skills"
`
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	dirs, gits, httpSrcs, err := LoadSkillSources(cfgPath)
	if err != nil {
		t.Fatalf("LoadSkillSources: %v", err)
	}
	if len(dirs) != 1 {
		t.Fatalf("want 1 dir, got %v", dirs)
	}
	if len(gits) != 1 || gits[0].URL != "https://example.com/skills.git" {
		t.Fatalf("want 1 git source, got %+v", gits)
	}
	if gits[0].LocalDir == "" || strings.Contains(gits[0].LocalDir, "~") {
		t.Fatalf("git local_dir must be tilde-expanded, got %q", gits[0].LocalDir)
	}
	if len(httpSrcs) != 2 {
		t.Fatalf("want 2 http/oci sources, got %+v", httpSrcs)
	}
}

// TestSyncGitSourceRejectsBadConfig verifies the git sync guard.
func TestSyncGitSourceRejectsBadConfig(t *testing.T) {
	if err := SyncGitSource(context.Background(), GitSource{}); err == nil {
		t.Fatal("git source without url/local_dir must be rejected")
	}
}

// TestFetchHTTPManifest verifies manifest fetch, field mapping and missing-ID
// skipping against a local httptest server.
func TestFetchHTTPManifest(t *testing.T) {
	body := `{"skills":[
		{"id":"audit","name":"Security Audit","description":"audit code","keywords":["security"],"version":"1.2.0"},
		{"name":"No ID skill","description":"skipped"}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	entries, err := FetchHTTPManifest(context.Background(), HTTPSource{URL: srv.URL})
	if err != nil {
		t.Fatalf("FetchHTTPManifest: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry (missing-ID skipped), got %d", len(entries))
	}
	e := entries[0]
	if e.ID != "audit" || e.Version != "1.2.0" || e.Source != SourceRegistered {
		t.Fatalf("unexpected entry: %+v", e)
	}
}

// TestFetchHTTPManifestNon200 verifies non-200 responses surface an error.
func TestFetchHTTPManifestNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, err := FetchHTTPManifest(context.Background(), HTTPSource{URL: srv.URL}); err == nil {
		t.Fatal("non-200 must error")
	}
}

// TestFTS5SearchAndFallback verifies the FTS5 index ranks matches and the
// Discovery fallback path keeps working when FTS5 is not attached.
func TestFTS5SearchAndFallback(t *testing.T) {
	entries := []SkillIndexEntry{
		{ID: "rust-review", Name: "Rust Review", Description: "audit rust ownership and unsafe", Keywords: []string{"rust", "unsafe"}},
		{ID: "web-search", Name: "Web Search", Description: "search the web", Keywords: []string{"web"}},
	}
	idx, err := NewFTS5Index(entries)
	if err != nil {
		t.Fatalf("NewFTS5Index: %v", err)
	}
	defer func() { _ = idx.Close() }()

	got, err := idx.Search("rust unsafe", 5, entries)
	if err != nil {
		t.Fatalf("FTS5 search: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("FTS5 must find rust-review")
	}
	if got[0].ID != "rust-review" {
		t.Fatalf("top FTS5 result want rust-review, got %q", got[0].ID)
	}

	// Discovery fallback: without FTS5, keyword matching still works.
	d := NewDiscovery(entries)
	if hits := d.Search("web", 5); len(hits) != 1 || hits[0].ID != "web-search" {
		t.Fatalf("keyword fallback want web-search, got %+v", hits)
	}
	// With FTS5 attached, Search prefers it.
	d.SetFTS5(idx)
	if hits := d.Search("rust unsafe", 5); len(hits) == 0 {
		t.Fatal("FTS5-backed discovery must find rust-review")
	}
}

// TestCatalogBuildWithHTTPAndGitSources verifies Catalog integration: a
// remote http source's entries join the local index during Build, and the
// SourceManager exposes git cache dirs as registered sources.
func TestCatalogBuildWithHTTPAndGitSources(t *testing.T) {
	root := t.TempDir()
	makeSkillDir(t, root, "local", "Local", "local skill", "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"skills":[{"id":"remote","name":"Remote","description":"remote skill","version":"0.1.0"}]}`))
	}))
	defer srv.Close()

	cat := NewCatalog(CatalogConfig{ProjectSkillsDir: root})
	cat.SetHTTPSources([]HTTPSource{{URL: srv.URL}})
	if err := cat.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	matches := cat.Search("remote", 5)
	if len(matches) != 1 || matches[0].ID != "remote" {
		t.Fatalf("remote source skill must be indexed, got %+v", matches)
	}
	if len(cat.All()) != 2 {
		t.Fatalf("want 2 entries (local + remote), got %d", len(cat.All()))
	}
}
