package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/runtime/protocol/skills"
)

// chdir changes the working directory for the duration of the test and
// restores it via Cleanup (LIFO).
func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

// mkdirAll creates a directory tree (helper to keep tests terse).
func mkdirAll(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

// writeFile creates a file with the given content.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestStatusConfigToStatusMinimal(t *testing.T) {
	cfg := ares_config.NewMinimalConfig("http://localhost:11434/v1", "", "llama3.2")
	out := configToStatus("test", cfg, true)

	if !out.Minimal {
		t.Fatal("expected minimal=true")
	}
	if out.Server.Host != "localhost" || out.Server.Port != 8080 {
		t.Fatalf("server = %s:%d, want localhost:8080", out.Server.Host, out.Server.Port)
	}
	if out.LLM.Provider != "ollama" || out.LLM.Model != "llama3.2" {
		t.Fatalf("llm = %s/%s, want ollama/llama3.2", out.LLM.Provider, out.LLM.Model)
	}
	if !out.Memory.Enabled {
		t.Fatal("memory should default to enabled")
	}
	if len(out.Agents.Sub) != 3 {
		t.Fatalf("sub agents = %d, want 3 (default team)", len(out.Agents.Sub))
	}
	if out.Kernel.Policy != "taskfabric" {
		t.Fatalf("kernel policy = %q, want taskfabric (default)", out.Kernel.Policy)
	}
}

func TestStatusConfigToStatusFromFile(t *testing.T) {
	cfg := &ares_config.Config{}
	cfg.LLM.Provider = "openai"
	cfg.LLM.Model = "gpt-4o-mini"
	cfg.LLM.APIKey = "sk-test"
	cfg.Agents.Sub = []ares_config.SubAgentConfig{{ID: "coder-a", Type: "coder"}}
	cfg.Kernel.Policy = "taskfabric"
	cfg.Storage.Enabled = true
	cfg.Storage.Type = "sqlite"

	out := configToStatus("conf.yaml", cfg, false)
	if out.Minimal {
		t.Fatal("expected minimal=false")
	}
	if !out.LLM.APIKeySet {
		t.Fatal("api key should be reported set")
	}
	if out.Kernel.Policy != "taskfabric" {
		t.Fatalf("kernel policy = %q, want taskfabric", out.Kernel.Policy)
	}
	if !out.Storage.Enabled || out.Storage.Type != "sqlite" {
		t.Fatalf("storage = %v/%s, want enabled/sqlite", out.Storage.Enabled, out.Storage.Type)
	}
}

func TestStatusConfigServerAddr(t *testing.T) {
	cases := []struct {
		name string
		host string
		port int
		want string
	}{
		{"defaults", "", 0, "http://localhost:8080"},
		{"wildcard host", "0.0.0.0", 9090, "http://localhost:9090"},
		{"explicit host", "127.0.0.1", 8080, "http://127.0.0.1:8080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr := statusConfigServerAddr(statusConfig{Server: statusServer{Host: tc.host, Port: tc.port}})
			if addr != tc.want {
				t.Fatalf("addr = %q, want %q", addr, tc.want)
			}
		})
	}
}

func TestStatusConfigWarnings(t *testing.T) {
	t.Run("healthy config has no warnings", func(t *testing.T) {
		cfg := statusConfig{Memory: statusMemory{Enabled: true}, Kernel: statusKernel{Policy: "taskfabric"}}
		if w := statusConfigWarnings(cfg); len(w) != 0 {
			t.Fatalf("unexpected warnings: %v", w)
		}
	})
	t.Run("memory off warns", func(t *testing.T) {
		cfg := statusConfig{Memory: statusMemory{Enabled: false}, Kernel: statusKernel{Policy: "taskfabric"}}
		if w := statusConfigWarnings(cfg); len(w) != 1 {
			t.Fatalf("want 1 warning, got %v", w)
		}
	})
	t.Run("unknown kernel policy warns", func(t *testing.T) {
		cfg := statusConfig{Memory: statusMemory{Enabled: true}, Kernel: statusKernel{Policy: "quantum"}}
		if w := statusConfigWarnings(cfg); len(w) != 1 {
			t.Fatalf("want 1 warning, got %v", w)
		}
	})
}

func TestStatusProbeRuntime(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(statusHealthPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"level": "good", "agents": 2}`)
	})
	mux.HandleFunc(statusAgentsPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `[{"id": "coder-a", "role": "coder", "status": "ready"}]`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rt := probeStatusRuntime(context.Background(), srv.URL)
	if !rt.Running {
		t.Fatalf("expected running, error=%q", rt.Error)
	}
	if rt.Health == nil || rt.Health.Level != "good" || rt.Health.Anomalies != 2 {
		t.Fatalf("health = %+v, want level=good anomalies=2", rt.Health)
	}
	if len(rt.Agents) != 1 || rt.Agents[0].ID != "coder-a" {
		t.Fatalf("agents = %+v, want 1 coder-a", rt.Agents)
	}
}

func TestStatusProbeRuntimeDead(t *testing.T) {
	rt := probeStatusRuntime(context.Background(), "http://127.0.0.1:1")
	if rt.Running {
		t.Fatal("expected not running")
	}
	if rt.Error == "" {
		t.Fatal("expected an error message")
	}
}

func TestStatusProbeRuntimeTrailingSlash(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(statusHealthPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"level": "ok"}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	rt := probeStatusRuntime(context.Background(), srv.URL+"/")
	if !rt.Running {
		t.Fatalf("expected running with trailing slash, error=%q", rt.Error)
	}
	if rt.Addr != strings.TrimRight(srv.URL+"/", "/") {
		t.Fatalf("addr = %q, want trimmed", rt.Addr)
	}
}

func TestStatusDetectConfigFile(t *testing.T) {
	root := t.TempDir()
	chdir(t, root)
	// Thin YAML — only the LLM section, exactly like the minimal-config flow.
	writeFile(t, filepath.Join(root, "ares.yaml"),
		"llm:\n  provider: openai\n  base_url: https://api.openai.com/v1\n")

	out, warns := inspectStatusConfig("")
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if out.Minimal {
		t.Fatal("expected a real config file, not minimal")
	}
	if !strings.HasSuffix(out.Source, "ares.yaml") {
		t.Fatalf("source = %q, want .../ares.yaml", out.Source)
	}
	if out.LLM.Provider != "openai" {
		t.Fatalf("provider = %q, want openai", out.LLM.Provider)
	}
	if !out.Memory.Enabled {
		t.Fatal("thin YAML should default memory to enabled")
	}
}

func TestStatusDetectConfigNone(t *testing.T) {
	root := t.TempDir()
	chdir(t, root)

	out, warns := inspectStatusConfig("")
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if !out.Minimal {
		t.Fatal("expected minimal assembly with no config file")
	}
	if out.Server.Port != 8080 {
		t.Fatalf("default port = %d, want 8080", out.Server.Port)
	}
}

func TestStatusDetectConfigBroken(t *testing.T) {
	root := t.TempDir()
	chdir(t, root)
	writeFile(t, filepath.Join(root, "ares.yaml"), "llm: [not a mapping:") // malformed YAML

	out, warns := inspectStatusConfig("")
	if len(warns) != 1 {
		t.Fatalf("want 1 warning for broken config, got %v", warns)
	}
	if !out.Minimal {
		t.Fatal("broken config should fall back to minimal defaults")
	}
}

func TestStatusInspectCapabilities(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	// Keep cwd (project root) distinct from $HOME so the project and user
	// skill sources do not dedupe onto the same directory.
	work := filepath.Join(root, "work")
	mkdirAll(t, work)
	chdir(t, work)

	// Project skills: .ares/skills/{proj-a,proj-b} (SKILL.md markers).
	for _, name := range []string{"proj-a", "proj-b"} {
		writeFile(t, filepath.Join(work, ".ares", "skills", name, "SKILL.md"), "# Skill "+name)
	}
	// A directory without a marker must not count.
	mkdirAll(t, filepath.Join(work, ".ares", "skills", "not-a-skill"))

	// User skills: ~/.ares/skills/user-a.
	writeFile(t, filepath.Join(root, ".ares", "skills", "user-a", "SKILL.md"), "# Skill user-a")

	// Experience store: 2 records.
	store := ares_skills.NewJSONExperienceStore(filepath.Join(root, ".ares", "experience.json"))
	if err := store.Save(context.Background(), []ares_skills.ExperienceRecord{
		{Skill: "pdf-gen", TaskPattern: "document-to-pdf", SuccessRate: 0.94},
		{Skill: "resume-gen", TaskPattern: "cv-to-pdf", SuccessRate: 0.8},
	}); err != nil {
		t.Fatalf("save experience: %v", err)
	}

	cap := inspectStatusCapabilities(context.Background())
	if cap.Skills.Project != 2 {
		t.Fatalf("project skills = %d, want 2", cap.Skills.Project)
	}
	if cap.Skills.User != 1 {
		t.Fatalf("user skills = %d, want 1", cap.Skills.User)
	}
	if cap.Experience.Records != 2 {
		t.Fatalf("experience records = %d, want 2", cap.Experience.Records)
	}
	if cap.Experience.Path == "" {
		t.Fatal("experience path should be reported")
	}
}

func TestStatusReportJSONShape(t *testing.T) {
	r := statusReport{
		Version:   "dev",
		GoVersion: "go1.26.0",
		Runtime:   statusRuntime{Running: true, Addr: "http://localhost:8080"},
		Config: statusConfig{
			Source:  "test",
			Minimal: true,
			Memory:  statusMemory{Enabled: true},
			Agents:  statusAgentConfig{Sub: []string{"coder-a"}},
		},
		Capabilities: statusCapabilities{},
		Warnings:     []string{"w"},
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// JSON must round-trip back to the same report.
	var back statusReport
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.Config.Agents.Sub) != 1 {
		t.Fatalf("round-trip config = %+v", back.Config)
	}
	if !back.Runtime.Running || len(back.Warnings) != 1 {
		t.Fatalf("round-trip runtime/warnings = %+v / %v", back.Runtime, back.Warnings)
	}
}
