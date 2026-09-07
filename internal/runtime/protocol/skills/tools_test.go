package ares_skills

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Timwood0x10/ares/internal/knowledge/skills"
	"github.com/Timwood0x10/ares/internal/tools/resources/core"
)

// buildTestCatalog builds a catalog over a temp dir with one executable-declaring
// skill ("audit") and one MCP-declaring skill ("gh").
func buildTestCatalog(t *testing.T) *Catalog {
	t.Helper()
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
	makeSkillDir(t, root, "gh", "GitHub", "github workflows", `
id: gh
name: GitHub
description: github workflows
tools:
  - id: gh-api
    type: mcp
    server: github
`)
	cat := NewCatalog(CatalogConfig{
		ProjectSkillsDir:      root,
		AllowLocalExecutables: true,
		Builtins:              []string{"filesystem"},
	})
	if err := cat.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	return cat
}

// registerCatalogTools registers CatalogTools into a fresh core.Registry and
// returns the registry plus the tool names.
func registerCatalogTools(t *testing.T, cat *Catalog) (*core.Registry, []string) {
	t.Helper()
	reg := core.NewRegistry()
	var names []string
	for _, tool := range CatalogTools(cat) {
		if err := reg.Register(tool); err != nil {
			t.Fatalf("Register %s: %v", tool.Name(), err)
		}
		names = append(names, tool.Name())
	}
	return reg, names
}

func TestCatalogToolsExposeSchemasToLLM(t *testing.T) {
	cat := buildTestCatalog(t)
	reg, names := registerCatalogTools(t, cat)

	schemas := reg.GetSchemas()
	if len(schemas) != len(names) {
		t.Fatalf("want %d schemas, got %d", len(names), len(schemas))
	}
	byName := make(map[string]core.ToolSchema, len(schemas))
	for _, s := range schemas {
		byName[s.Name] = s
	}
	for _, n := range names {
		if _, ok := byName[n]; !ok {
			t.Fatalf("schema for %q missing", n)
		}
	}
	// skill_search requires a query parameter.
	search := byName[ToolSkillSearch]
	if search.Parameters == nil || search.Parameters.Required == nil ||
		len(search.Parameters.Required) != 1 || search.Parameters.Required[0] != "query" {
		t.Fatalf("skill_search schema wrong: %+v", search.Parameters)
	}
	// skill_load / skill_activate require an id.
	for _, n := range []string{ToolSkillLoad, ToolSkillActivate} {
		s := byName[n]
		if s.Parameters == nil || len(s.Parameters.Required) != 1 || s.Parameters.Required[0] != "id" {
			t.Fatalf("%s schema wrong: %+v", n, s.Parameters)
		}
	}
	// skill_experience requires a task_pattern.
	exp := byName[ToolSkillExperience]
	if exp.Parameters == nil || len(exp.Parameters.Required) != 1 || exp.Parameters.Required[0] != "task_pattern" {
		t.Fatalf("skill_experience schema wrong: %+v", exp.Parameters)
	}
}

func TestCatalogToolsIdempotency(t *testing.T) {
	cat := buildTestCatalog(t)
	reg, _ := registerCatalogTools(t, cat)
	// search/load/list/experience are read-only and safe to retry; activate is not.
	for _, n := range []string{ToolSkillSearch, ToolSkillLoad, ToolSkillList, ToolSkillExperience} {
		tool, ok := reg.Get(n)
		if !ok {
			t.Fatalf("tool %q not registered", n)
		}
		it, ok := tool.(core.IdempotentTool)
		if !ok || !it.IsIdempotent() {
			t.Fatalf("%s must be idempotent", n)
		}
	}
	if tool, ok := reg.Get(ToolSkillActivate); !ok {
		t.Fatal("skill_activate not registered")
	} else if it, ok := tool.(core.IdempotentTool); ok && it.IsIdempotent() {
		t.Fatal("skill_activate must NOT be idempotent (it connects MCP servers)")
	}
}

func TestSkillSearchTool(t *testing.T) {
	cat := buildTestCatalog(t)
	reg, _ := registerCatalogTools(t, cat)
	tool, _ := reg.Get(ToolSkillSearch)

	res, err := tool.Execute(context.Background(), map[string]any{"query": "owasp"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Success {
		t.Fatalf("search must succeed, got %s", res.Error)
	}
	var views []skillView
	if err := json.Unmarshal([]byte(res.Data.(string)), &views); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if len(views) != 1 || views[0].ID != "audit" {
		t.Fatalf("want [audit], got %+v", views)
	}
	if views[0].Source != SourceProject {
		t.Fatalf("source wrong: %+v", views[0])
	}
	// Limit is honored: "security github" matches both skills.
	res, err = tool.Execute(context.Background(), map[string]any{"query": "security github", "limit": float64(1)})
	if err != nil {
		t.Fatalf("Execute limited: %v", err)
	}
	_ = json.Unmarshal([]byte(res.Data.(string)), &views)
	if len(views) != 1 {
		t.Fatalf("limit must cap results, got %d", len(views))
	}
	// Missing query is rejected.
	res, _ = tool.Execute(context.Background(), map[string]any{})
	if res.Success || !strings.Contains(res.Error, "query") {
		t.Fatalf("missing query must error, got success=%v error=%q", res.Success, res.Error)
	}
}

func TestSkillLoadTool(t *testing.T) {
	cat := buildTestCatalog(t)
	reg, _ := registerCatalogTools(t, cat)
	tool, _ := reg.Get(ToolSkillLoad)

	res, err := tool.Execute(context.Background(), map[string]any{"id": "audit"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Success || !strings.Contains(res.Data.(string), "Body instructions for Security Audit") {
		t.Fatalf("load must return the SKILL.md body, got success=%v data=%q", res.Success, res.Data)
	}
	// Unknown id errors without panicking.
	res, _ = tool.Execute(context.Background(), map[string]any{"id": "missing"})
	if res.Success {
		t.Fatalf("unknown skill must error, got %q", res.Data)
	}
}

func TestSkillActivateToolConnectsMCP(t *testing.T) {
	cat := buildTestCatalog(t)
	conn := &fakeMCPConnector{connected: map[string]bool{}}
	cat.SetMCPConnector(conn)
	reg, _ := registerCatalogTools(t, cat)
	tool, _ := reg.Get(ToolSkillActivate)

	// Before activation no MCP server is connected (lazy-loading principle).
	if conn.connected["github"] {
		t.Fatal("MCP server must not be connected before activation")
	}
	res, err := tool.Execute(context.Background(), map[string]any{"id": "gh"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Success {
		t.Fatalf("activate must succeed, got %s", res.Error)
	}
	if !conn.connected["github"] {
		t.Fatal("declared MCP server must be connected on activation")
	}
	var views []resolvedToolView
	if err := json.Unmarshal([]byte(res.Data.(string)), &views); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if len(views) != 1 || views[0].Kind != ToolMCP || views[0].Target != "github" {
		t.Fatalf("want mcp tool github, got %+v", views)
	}
}

func TestSkillListTool(t *testing.T) {
	cat := buildTestCatalog(t)
	reg, _ := registerCatalogTools(t, cat)
	tool, _ := reg.Get(ToolSkillList)

	res, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Success {
		t.Fatalf("list must succeed, got %s", res.Error)
	}
	var views []skillView
	if err := json.Unmarshal([]byte(res.Data.(string)), &views); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("want 2 skills, got %+v", views)
	}
}

// TestCatalogSeedRegistryBacksLoadDetail verifies the progressive-disclosure
// loop is closed: the resident registry holds name+description only, while
// LoadDetail returns the SKILL.md body on demand through the catalog loader
// (previously it always returned an empty body — dead code).
func TestCatalogSeedRegistryBacksLoadDetail(t *testing.T) {
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

	// Level-0: List carries name + description only, never the body.
	list := reg.List()
	if len(list) != 1 {
		t.Fatalf("want 1 skill, got %d", len(list))
	}
	if list[0].Description == "" {
		t.Fatal("description must be resident")
	}

	// Level-1: LoadDetail must fetch the body through the catalog on demand.
	detail, ok := reg.LoadDetail("audit")
	if !ok || detail == "" {
		t.Fatalf("LoadDetail must return the SKILL.md body, got ok=%v detail=%q", ok, detail)
	}
	if detail == list[0].Description {
		t.Fatal("LoadDetail must return the body, not the resident description")
	}
	if !strings.Contains(detail, "Body instructions for Security Audit") {
		t.Fatalf("unexpected body: %q", detail)
	}
	// Unknown skill still reports not-found.
	if _, ok := reg.LoadDetail("missing"); ok {
		t.Fatal("unknown skill must report not-found")
	}
}

// TestSkillExperienceTool verifies the learned-source query tool: recorded
// priors are returned, unknown patterns yield a notice (not an error), and the
// tool never executes anything.
func TestSkillExperienceTool(t *testing.T) {
	cat := buildTestCatalog(t)
	if err := cat.Experience().Record("audit", "security-scan", 0.9); err != nil {
		t.Fatalf("Record: %v", err)
	}
	reg, _ := registerCatalogTools(t, cat)
	tool, _ := reg.Get(ToolSkillExperience)

	res, err := tool.Execute(context.Background(), map[string]any{"task_pattern": "security-scan"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.Success {
		t.Fatalf("experience query must succeed, got %s", res.Error)
	}
	var rec ExperienceRecord
	if err := json.Unmarshal([]byte(res.Data.(string)), &rec); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if rec.Skill != "audit" || rec.SuccessRate != 0.9 {
		t.Fatalf("unexpected prior: %+v", rec)
	}

	// Unknown pattern: success with a notice, not an error.
	res, _ = tool.Execute(context.Background(), map[string]any{"task_pattern": "nope"})
	if !res.Success || !strings.Contains(res.Data.(string), "no recorded experience") {
		t.Fatalf("unknown pattern should return a notice, got success=%v data=%q", res.Success, res.Data)
	}
	// Missing argument is rejected.
	res, _ = tool.Execute(context.Background(), map[string]any{})
	if res.Success {
		t.Fatalf("missing task_pattern must error, got %q", res.Data)
	}
}
