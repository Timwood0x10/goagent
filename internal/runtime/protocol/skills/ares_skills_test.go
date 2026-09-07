package ares_skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Timwood0x10/ares/internal/knowledge/skills"
	"github.com/Timwood0x10/ares/internal/tools/envcap"
)

// makeSkillDir creates a skill directory with a SKILL.md (front matter) and an
// optional skill.yaml manifest, returning its absolute path.
func makeSkillDir(t *testing.T, root, id, name, desc string, manifest string) string {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	skillMD := "---\nid: " + id + "\nname: " + name + "\ndescription: " + desc +
		"\nkeywords: [k1, k2]\nversion: 1.0.0\n---\n\n# " + name + "\n\nBody instructions for " + name + ".\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(skillMD), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	if manifest != "" {
		if err := os.WriteFile(filepath.Join(dir, "skill.yaml"), []byte(manifest), 0o644); err != nil {
			t.Fatalf("write skill.yaml: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "references", "checklist.md"), []byte("checklist body"), 0o644); err != nil {
		t.Fatalf("write reference: %v", err)
	}
	return dir
}

func TestSourceManagerSourcesDedupAndOrder(t *testing.T) {
	sm := NewSourceManager("/p/.ares/skills", "/u/.ares/skills", []string{"/p/.ares/skills", "/x/extra"})
	sources := sm.Sources()
	if len(sources) != 3 {
		t.Fatalf("want 3 deduped sources, got %d", len(sources))
	}
	if sources[0].Kind != SourceProject || sources[1].Kind != SourceUser || sources[2].Kind != SourceRegistered {
		t.Fatalf("unexpected source order/kinds: %+v", sources)
	}
}

func TestSourceManagerSkillDirsDeclaredOnly(t *testing.T) {
	root := t.TempDir()
	skillDir := makeSkillDir(t, root, "alpha", "Alpha", "alpha skill", "")
	// A non-declared directory (no SKILL.md / skill.yaml) must be skipped.
	if err := os.MkdirAll(filepath.Join(root, "noise"), 0o755); err != nil {
		t.Fatal(err)
	}
	sm := NewSourceManager("", "", nil)
	dirs, err := sm.SkillDirs(SourceDir{Kind: SourceProject, Path: root})
	if err != nil {
		t.Fatalf("SkillDirs: %v", err)
	}
	if len(dirs) != 1 || dirs[0] != skillDir {
		t.Fatalf("want only declared dir %q, got %v", skillDir, dirs)
	}
}

func TestSourceManagerMissingRootIsNotError(t *testing.T) {
	sm := NewSourceManager("", "", nil)
	dirs, err := sm.SkillDirs(SourceDir{Kind: SourceProject, Path: filepath.Join(t.TempDir(), "nope")})
	if err != nil {
		t.Fatalf("missing root must not error, got %v", err)
	}
	if len(dirs) != 0 {
		t.Fatalf("want no dirs, got %v", dirs)
	}
}

func TestIndexerMergesFrontMatterAndManifest(t *testing.T) {
	root := t.TempDir()
	makeSkillDir(t, root, "audit", "Front Name", "front description", `
id: audit
name: Manifest Name
description: manifest description
keywords: [sec, owasp]
version: 2.0.0
tools:
  - id: semgrep
    type: executable
    command: sh
`)
	sm := NewSourceManager("", "", nil)
	ix := NewIndexer()
	entries, err := ix.Index([]SourceDir{{Kind: SourceProject, Path: root}}, sm)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.ID != "audit" || e.Name != "Manifest Name" || e.Description != "manifest description" {
		t.Fatalf("manifest should win over front matter: %+v", e)
	}
	if len(e.Keywords) != 2 || e.Keywords[0] != "sec" {
		t.Fatalf("unexpected keywords: %v", e.Keywords)
	}
	if len(e.ToolTypes) != 1 || e.ToolTypes[0] != "executable" {
		t.Fatalf("unexpected tool types: %v", e.ToolTypes)
	}
	if e.Hash == "" {
		t.Fatal("content hash must be non-empty")
	}
}

func TestIndexerDoesNotLoadBody(t *testing.T) {
	root := t.TempDir()
	dir := makeSkillDir(t, root, "alpha", "Alpha", "alpha desc", "")
	sm := NewSourceManager("", "", nil)
	entries, err := NewIndexer().Index([]SourceDir{{Kind: SourceProject, Path: root}}, sm)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	if entries[0].Description == "" || entries[0].Hash == "" {
		t.Fatal("metadata must be populated")
	}
	data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("fixture body missing")
	}
	// Index entry must not contain the body (progressive disclosure Level 0).
	if entries[0].Description == string(data) {
		t.Fatal("index must hold metadata only, not the full body")
	}
}

func TestDiscoverySearchRanksAndLimits(t *testing.T) {
	entries := []SkillIndexEntry{
		{ID: "rust-review", Name: "Rust Review", Description: "audit rust code", Keywords: []string{"rust", "unsafe"}},
		{ID: "unsafe-audit", Name: "Unsafe Audit", Description: "find unsafe blocks", Keywords: []string{"unsafe", "audit"}},
		{ID: "web-search", Name: "Web Search", Description: "search the web", Keywords: []string{"web"}},
	}
	d := NewDiscovery(entries)

	// "unsafe audit" matches rust-review (2 terms) and unsafe-audit (2 terms).
	got := d.Search("unsafe audit", 5)
	if len(got) == 0 {
		t.Fatal("want matches, got none")
	}
	if got[0].ID != "rust-review" && got[0].ID != "unsafe-audit" {
		t.Fatalf("top result should be a 2-term match, got %q", got[0].ID)
	}

	limited := d.Search("unsafe audit", 1)
	if len(limited) != 1 {
		t.Fatalf("limit must cap results, got %d", len(limited))
	}

	if got := d.Search("zzz-nothing", 5); len(got) != 0 {
		t.Fatalf("no-match query should return empty, got %v", got)
	}
	if d.Count() != 3 {
		t.Fatalf("Count want 3, got %d", d.Count())
	}
}

func TestLoaderLoadAndNotFound(t *testing.T) {
	root := t.TempDir()
	makeSkillDir(t, root, "alpha", "Alpha", "alpha desc", "")
	sm := NewSourceManager("", "", nil)
	entries, err := NewIndexer().Index([]SourceDir{{Kind: SourceProject, Path: root}}, sm)
	if err != nil {
		t.Fatal(err)
	}
	l := NewLoader(entries)
	if !l.Has("alpha") {
		t.Fatal("alpha must be indexed")
	}
	body, err := l.Load("alpha")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if body == "" || body == entries[0].Description {
		t.Fatal("Load must return the full body, not metadata")
	}
	if _, err := l.Load("missing"); err != ErrSkillNotFound {
		t.Fatalf("want ErrSkillNotFound, got %v", err)
	}
}

func TestLoaderReferencesAndTraversalGuard(t *testing.T) {
	root := t.TempDir()
	makeSkillDir(t, root, "alpha", "Alpha", "alpha desc", "")
	sm := NewSourceManager("", "", nil)
	entries, err := NewIndexer().Index([]SourceDir{{Kind: SourceProject, Path: root}}, sm)
	if err != nil {
		t.Fatal(err)
	}
	l := NewLoader(entries)
	refs, err := l.ListReferences("alpha")
	if err != nil {
		t.Fatalf("ListReferences: %v", err)
	}
	if len(refs) != 1 || refs[0] != "checklist.md" {
		t.Fatalf("want [checklist.md], got %v", refs)
	}
	content, err := l.LoadReference("alpha", "checklist.md")
	if err != nil || content != "checklist body" {
		t.Fatalf("LoadReference: content=%q err=%v", content, err)
	}
	if _, err := l.LoadReference("alpha", "../outside.md"); err == nil {
		t.Fatal("path traversal reference must be rejected")
	}
}

func TestResolverTrustGate(t *testing.T) {
	r := NewResolver(true, []string{"filesystem"})

	// Untrusted source (external/learned): executable is rejected at binding.
	decl := ToolDecl{ID: "x", Type: "executable", Command: "sh"}
	if _, err := r.Resolve([]ToolDecl{decl}, SourceExperience); err == nil {
		t.Fatal("untrusted source executable should be rejected")
	}
	// Registered source (TrustAsk): binding is allowed; the ask gate applies
	// at execution time, not at descriptor resolution.
	tools, err := r.Resolve([]ToolDecl{decl}, SourceRegistered)
	if err != nil {
		t.Fatalf("registered executable should bind: %v", err)
	}
	if len(tools) != 1 || tools[0].Kind != ToolExecutable || tools[0].Target != "sh" {
		t.Fatalf("unexpected resolved tool: %+v", tools)
	}
	// Project source: executable allowed.
	tools, err = r.Resolve([]ToolDecl{decl}, SourceProject)
	if err != nil {
		t.Fatalf("project executable should resolve: %v", err)
	}
	if len(tools) != 1 || tools[0].Kind != ToolExecutable || tools[0].Target != "sh" {
		t.Fatalf("unexpected resolved tool: %+v", tools)
	}
}

func TestResolverKinds(t *testing.T) {
	r := NewResolver(true, []string{"filesystem"})
	decls := []ToolDecl{
		{ID: "fs", Type: "builtin", Name: "filesystem"},
		{ID: "gh", Type: "mcp", Server: "github"},
		{ID: "semgrep", Type: "executable", Command: "sh", Args: []string{"--json"}},
	}
	tools, err := r.Resolve(decls, SourceProject)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(tools) != 3 {
		t.Fatalf("want 3 tools, got %d", len(tools))
	}
	if tools[0].Kind != ToolBuiltin || tools[0].Target != "filesystem" {
		t.Fatalf("builtin wrong: %+v", tools[0])
	}
	if tools[1].Kind != ToolMCP || tools[1].Target != "github" {
		t.Fatalf("mcp wrong: %+v", tools[1])
	}
	if tools[2].Kind != ToolExecutable || tools[2].Target != "sh" || len(tools[2].Args) != 1 {
		t.Fatalf("executable wrong: %+v", tools[2])
	}
	// Unknown builtin must be rejected.
	if _, err := r.Resolve([]ToolDecl{{ID: "b", Type: "builtin", Name: "nope"}}, SourceProject); err == nil {
		t.Fatal("unknown builtin must be rejected")
	}
	// Executables disabled: gate rejects.
	r2 := NewResolver(false, nil)
	if _, err := r2.Resolve([]ToolDecl{decls[2]}, SourceProject); err == nil {
		t.Fatal("allowLocalExecutables=false must reject executables")
	}
}

func TestExperienceRecordAndBestMatch(t *testing.T) {
	e := NewExperience()
	if err := e.Record("pdf-gen", "document-to-pdf", 0.94); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := e.Record("pdf-gen", "document-to-pdf", 1.5); err != nil {
		t.Fatal(err)
	}
	best, ok := e.BestMatch("document-to-pdf")
	if !ok || best.SuccessRate != 1.0 {
		t.Fatalf("want clamped 1.0, got %+v ok=%v", best, ok)
	}
	if _, ok := e.BestMatch("no-such-task"); ok {
		t.Fatal("no match expected")
	}
	if e.Count() != 1 {
		t.Fatalf("re-record must dedupe, count=%d", e.Count())
	}
	if err := e.Record("", "pattern", 0.5); err == nil {
		t.Fatal("empty skill must be rejected")
	}
}

// TestBestMatchKeywordScoring verifies long (full-description) patterns match
// by keyword overlap rather than raw substring containment, so two verbose
// descriptions sharing only an incidental word do not spuriously match — while
// genuinely overlapping descriptions still match and rank by success rate.
func TestBestMatchKeywordScoring(t *testing.T) {
	e := NewExperience()
	// A stored prior describing a PDF-conversion task.
	if err := e.Record("pdf-gen", "convert markdown documents into a pdf report", 0.9); err != nil {
		t.Fatalf("Record: %v", err)
	}

	// Genuinely overlapping long query: must match the pdf-gen prior.
	best, ok := e.BestMatch("please convert this markdown document into a pdf report for the team")
	if !ok {
		t.Fatal("overlapping long query must match")
	}
	if best.Skill != "pdf-gen" {
		t.Fatalf("want pdf-gen, got %+v", best)
	}

	// Incidental single-word overlap: "report" appears in both but the rest of
	// the query is about something else. With substring semantics the stored
	// pattern ("...into a pdf report") is NOT contained in the query and the
	// query is not contained in it, so this would 0-match regardless; with the
	// keyword scorer the shared-token ratio stays below the no-match threshold.
	if _, ok := e.BestMatch("give me the weekly expense report please"); ok {
		t.Fatal("incidental single-word overlap must not match")
	}

	// Short coarse pattern keeps legacy substring semantics.
	if err := e.Record("audit", "agent_top", 1.0); err != nil {
		t.Fatalf("Record coarse: %v", err)
	}
	best, ok = e.BestMatch("agent_top")
	if !ok || best.Skill != "audit" {
		t.Fatalf("short pattern must match via substring, got %+v ok=%v", best, ok)
	}
}

func TestCatalogEndToEnd(t *testing.T) {
	root := t.TempDir()
	makeSkillDir(t, root, "audit", "Security Audit", "audit code for OWASP", `
id: audit
name: Security Audit
description: audit code for OWASP
keywords: [security, owasp]
version: 1.0.0
tools:
  - id: semgrep
    type: executable
    command: sh
`)
	cat := NewCatalog(CatalogConfig{
		ProjectSkillsDir:      root,
		UserSkillsDir:         "",
		AllowLocalExecutables: true,
		Builtins:              []string{"filesystem"},
	})
	if err := cat.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	matches := cat.Search("owasp", 5)
	if len(matches) != 1 || matches[0].ID != "audit" {
		t.Fatalf("search want audit, got %+v", matches)
	}
	body, err := cat.Load("audit")
	if err != nil || body == "" {
		t.Fatalf("Load: %v", err)
	}
	tools, err := cat.ResolveTools("audit")
	if err != nil {
		t.Fatalf("ResolveTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Kind != ToolExecutable {
		t.Fatalf("resolve want executable semgrep, got %+v", tools)
	}
	if _, err := cat.ResolveTools("missing"); err != ErrSkillNotFound {
		t.Fatalf("want ErrSkillNotFound, got %v", err)
	}
}

// TestCatalogSeedsEnvcapAggregation verifies the envcap integration point:
// after SeedRegistry, the catalog's index becomes the skills source of an
// envcap.Searcher, so a unified tools/skills/commands query can hit catalog
// skills (design §12: envcap is the ToolResolver query front-end).
func TestCatalogSeedsEnvcapAggregation(t *testing.T) {
	root := t.TempDir()
	makeSkillDir(t, root, "audit", "Security Audit", "audit code for OWASP", "")
	cat := NewCatalog(CatalogConfig{ProjectSkillsDir: root})
	if err := cat.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	reg := skills.NewRegistry()
	if err := cat.SeedRegistry(reg); err != nil {
		t.Fatalf("SeedRegistry: %v", err)
	}

	searcher := envcap.NewSearcher(nil, reg, nil)
	got, err := searcher.Search(context.Background(), "owasp", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("envcap search must surface catalog skills via the seeded registry")
	}
	found := false
	for _, c := range got {
		if c.Kind == envcap.KindSkill && c.Name == "audit" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want skill capability audit, got %+v", got)
	}
}

// TestCatalogActivateConnectsMCP verifies lazy MCP connection (acceptance #3):
// an MCP tool's declared server is connected only when the skill is
// activated, and never before.
func TestCatalogActivateConnectsMCP(t *testing.T) {
	root := t.TempDir()
	makeSkillDir(t, root, "gh", "GitHub", "github workflows", `
id: gh
name: GitHub
description: github workflows
tools:
  - id: gh-api
    type: mcp
    server: github
`)
	cat := NewCatalog(CatalogConfig{ProjectSkillsDir: root})
	if err := cat.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	// Without a connector, activation resolves descriptors but never connects.
	tools, err := cat.Activate(context.Background(), "gh")
	if err != nil {
		t.Fatalf("Activate without connector: %v", err)
	}
	if len(tools) != 1 || tools[0].Kind != ToolMCP {
		t.Fatalf("want 1 mcp tool, got %+v", tools)
	}

	// With a connector, the declared server is connected at activation.
	conn := &fakeMCPConnector{}
	cat.SetMCPConnector(conn)
	if _, err := cat.Activate(context.Background(), "gh"); err != nil {
		t.Fatalf("Activate with connector: %v", err)
	}
	if !conn.connected["github"] {
		t.Fatalf("server %q must be lazily connected on activation", "github")
	}
}

// fakeMCPConnector records which servers were lazily connected.
type fakeMCPConnector struct {
	connected map[string]bool
}

func (f *fakeMCPConnector) ConnectServer(_ context.Context, name string) error {
	if f.connected == nil {
		f.connected = make(map[string]bool)
	}
	f.connected[name] = true
	return nil
}
