// Package ares_bootstrap — SKILLS progressive-disclosure wiring.
//
// REVIEW #11 closure: the ares_skills subsystem (Catalog → Registry) was only
// reachable from the `ares status` CLI; `ares serve` never constructed it, so
// the memory manager's resident "Available skills" block was always empty and
// progressive disclosure (Level-0 metadata resident, Level-1 body on demand)
// never fired in production. wireSkills assembles the catalog once at startup
// and seeds it into the memory manager.
package ares_bootstrap

import (
	"context"
	"os"
	"path/filepath"

	"github.com/Timwood0x10/ares/internal/knowledge/skills"
	ares_skills "github.com/Timwood0x10/ares/internal/runtime/protocol/skills"
)

// skillsRegistrySetter is the minimal interface for injecting a skills registry
// into a MemoryManager. Both *memoryManager and *ProductionMemoryManager
// satisfy it, but the public MemoryManager interface does not expose
// SetSkillsRegistry (progressive disclosure is an optional capability), so we
// type-assert at wiring time instead of widening the interface — the same
// pattern used by retrieverSetter.
type skillsRegistrySetter interface {
	SetSkillsRegistry(reg *skills.Registry)
}

// wireSkills builds the SKILLS catalog from the declared sources, indexes it
// once, seeds a fresh registry, and attaches that registry to the memory
// manager for progressive disclosure. It is best-effort and non-fatal:
//
//   - When memory is disabled (mem == nil) there is nowhere to attach the
//     resident skill block, so wiring is skipped.
//   - When the memory manager does not expose SetSkillsRegistry, wiring is
//     skipped with a warning (retrieval-only managers still work).
//   - An index build failure is logged, not fatal: serve continues without
//     the skill block rather than failing startup over a missing directory.
//
// Sources mirror the `ares status` convention: project-local ".ares/skills",
// user-global "~/.ares/skills", plus any [[skill_sources]] declared in the
// default config.toml (directory/git/http). The catalog is seeded exactly once
// here; there is no runtime refresh loop (design principle: keep it lean).
//
// Args:
//   - ctx: bootstrap context (git source sync honors cancellation).
//   - mem: the constructed memory manager (nil when memory is disabled).
//   - mcp: the MCP manager, attached as the lazy MCP connector so a skill's
//     declared MCP servers connect only at activation time. May be nil.
//
// Returns:
//   - *ares_skills.Catalog: the wired catalog (nil when skipped) so the caller
//     can register a Close cleanup for the FTS5 backing store.
//   - *skills.Registry: the seeded registry (nil when skipped) so the caller
//     can also feed it to the environment-capability searcher (envcap), which
//     completes the SKILLS-as-searchable-capability half of progressive
//     disclosure alongside the memory-resident block.
func wireSkills(ctx context.Context, mem any, mcp mcpConnector) (*ares_skills.Catalog, *skills.Registry) {
	setter, ok := mem.(skillsRegistrySetter)
	if !ok {
		log.Info("bootstrap: skills disabled (memory manager does not expose SetSkillsRegistry), skipping")
		return nil, nil
	}

	extraDirs, gitSources, httpSources, err := ares_skills.LoadSkillSources("")
	if err != nil {
		log.Warn("bootstrap: skills config load failed; wiring skipped", "error", err)
		return nil, nil
	}

	projectDir := filepath.Join(".", ".ares", "skills")
	userDir := ""
	expPath := ""
	if home, homeErr := os.UserHomeDir(); homeErr == nil && home != "" {
		userDir = filepath.Join(home, ".ares", "skills")
		expPath = filepath.Join(home, ".ares", "experience.json")
	}

	catalog := ares_skills.NewCatalog(ares_skills.CatalogConfig{
		ProjectSkillsDir: projectDir,
		UserSkillsDir:    userDir,
		RegisteredDirs:   extraDirs,
		ExperiencePath:   expPath,
	})
	catalog.SetGitSources(gitSources)
	catalog.SetHTTPSources(httpSources)
	if mcp != nil {
		catalog.SetMCPConnector(mcp)
	}

	// Refresh git clones before indexing; a sync failure is non-fatal (the
	// catalog still indexes whatever local checkouts exist).
	if syncErr := catalog.SyncGitSources(ctx); syncErr != nil {
		log.Warn("bootstrap: skills git source sync had errors (indexing local checkouts only)", "error", syncErr)
	}

	if buildErr := catalog.Build(); buildErr != nil {
		log.Warn("bootstrap: skills index build failed; skill block skipped", "error", buildErr)
		_ = catalog.Close()
		return nil, nil
	}

	reg := skills.NewRegistry()
	if seedErr := catalog.SeedRegistry(reg); seedErr != nil {
		log.Warn("bootstrap: skills registry seed failed; skill block skipped", "error", seedErr)
		_ = catalog.Close()
		return nil, nil
	}
	setter.SetSkillsRegistry(reg)

	log.Info("bootstrap: skills wired for progressive disclosure",
		"indexed", catalog.Count(),
		"project_dir", projectDir,
		"user_dir", userDir,
		"registered_dirs", len(extraDirs),
		"git_sources", len(gitSources),
		"http_sources", len(httpSources),
	)
	return catalog, reg
}

// mcpConnector is the wiring-local view of the MCP manager needed by the
// catalog's lazy connector. *ares_mcp.MCPManager satisfies it via ConnectServer.
type mcpConnector interface {
	ConnectServer(ctx context.Context, name string) error
}
