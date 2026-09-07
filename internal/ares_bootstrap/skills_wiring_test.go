package ares_bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/knowledge/skills"
	ares_memory "github.com/Timwood0x10/ares/internal/runtime/memory"
)

// stubSkillsSetter records the registry attached via SetSkillsRegistry so the
// test can assert the resident skill block was seeded from the indexed sources.
type stubSkillsSetter struct {
	reg *skills.Registry
}

func (s *stubSkillsSetter) SetSkillsRegistry(reg *skills.Registry) { s.reg = reg }

// writeSkill lays down a minimal SKILL.md under dir/name so the catalog indexer
// discovers one skill (frontmatter name + description are the resident view).
func writeSkill(t *testing.T, root, name, desc string) {
	t.Helper()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	body := "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n# " + name + "\n\nfull body here.\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644))
}

// TestWireSkills_SeedsRegistryFromProjectDir verifies #11 closure: wireSkills
// indexes the project skills dir, seeds a registry, and attaches it to the
// memory manager. Progressive disclosure means List carries name+description
// while the body is loaded on demand via the catalog-backed detail loader.
func TestWireSkills_SeedsRegistryFromProjectDir(t *testing.T) {
	// Run inside a temp working dir so the project-local ".ares/skills" root
	// (a relative path in wireSkills) points at our fixture, not the repo.
	tmp := t.TempDir()
	orig, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(orig) })
	require.NoError(t, os.Chdir(tmp))

	writeSkill(t, filepath.Join(tmp, ".ares", "skills"), "shell", "Run shell commands")

	mem := &stubSkillsSetter{}
	catalog, reg := wireSkills(context.Background(), mem, nil)
	require.NotNil(t, catalog, "catalog should be wired when a skill source exists")
	require.NotNil(t, reg, "registry should be returned for envcap wiring")
	t.Cleanup(func() { _ = catalog.Close() })

	require.NotNil(t, mem.reg, "registry must be attached to the memory manager")
	require.Same(t, reg, mem.reg, "returned registry and attached registry must be the same instance")
	require.True(t, mem.reg.Has("shell"), "indexed skill should be registered")

	body, ok := mem.reg.LoadDetail("shell")
	require.True(t, ok, "detail loader should resolve the on-demand body")
	require.Contains(t, body, "full body here.")
}

// TestWireSkills_SkippedWhenNoSetter verifies the best-effort contract: a
// memory manager that does not expose SetSkillsRegistry yields no catalog and
// no panic (retrieval-only managers still work).
func TestWireSkills_SkippedWhenNoSetter(t *testing.T) {
	catalog, reg := wireSkills(context.Background(), struct{}{}, nil)
	require.Nil(t, catalog)
	require.Nil(t, reg)
}

// TestWireSkills_RealMemoryManagerSatisfiesSetter guards the type assertion:
// the production MemoryManager must satisfy skillsRegistrySetter, otherwise the
// wiring silently degrades in serve.
func TestWireSkills_RealMemoryManagerSatisfiesSetter(t *testing.T) {
	mem, err := ares_memory.NewMemoryManager(ares_memory.DefaultMemoryConfig())
	require.NoError(t, err)
	_, ok := mem.(skillsRegistrySetter)
	require.True(t, ok, "MemoryManager must expose SetSkillsRegistry for #11 wiring")
}
