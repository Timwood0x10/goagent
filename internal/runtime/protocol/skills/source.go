package ares_skills

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SourceManager knows the declared skill sources. It NEVER scans the whole
// disk or PATH: only the explicitly declared directories are enumerated, and
// each directory is read one level deep for skill subdirectories.
type SourceManager struct {
	projectDir string
	userDir    string
	extraDirs  []string
	gitSources []GitSource
}

// NewSourceManager creates a SourceManager over the declared skill roots.
//
// Args:
//   - projectDir: the project-local skills root (".ares/skills"), may be empty.
//   - userDir: the user-global skills root ("~/.ares/skills"), may be empty.
//   - extraDirs: registered directory sources from config.toml.
//
// Returns:
//   - *SourceManager: ready to enumerate declared sources.
func NewSourceManager(projectDir, userDir string, extraDirs []string) *SourceManager {
	return &SourceManager{
		projectDir: projectDir,
		userDir:    userDir,
		extraDirs:  extraDirs,
	}
}

// SetGitSources attaches declared git sources (config.toml type="git").
//
// Args:
//   - gits: the git sources to index via their local cache directories.
func (s *SourceManager) SetGitSources(gits []GitSource) {
	s.gitSources = append([]GitSource(nil), gits...)
}

// SyncGitSources clones or refreshes every declared git source into its
// local cache directory. Failures are collected and returned together so a
// single unreachable source does not silently hide the others.
//
// Args:
//   - ctx: context for cancellation.
//
// Returns:
//   - error: joined sync errors, or nil.
func (s *SourceManager) SyncGitSources(ctx context.Context) error {
	var errs []error
	for _, g := range s.gitSources {
		if err := SyncGitSource(ctx, g); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// Sources returns all declared source directories, deduplicated by path.
// Order is deterministic: project, user, registered extras, then git cache
// directories (their LocalDir participates as a registered-style source).
//
// Returns:
//   - []SourceDir: the declared sources (may be empty).
func (s *SourceManager) Sources() []SourceDir {
	seen := make(map[string]bool)
	var out []SourceDir
	add := func(kind SourceKind, dir string) {
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		out = append(out, SourceDir{Kind: kind, Path: dir})
	}
	add(SourceProject, s.projectDir)
	add(SourceUser, s.userDir)
	for _, d := range s.extraDirs {
		add(SourceRegistered, d)
	}
	for _, g := range s.gitSources {
		add(SourceRegistered, g.LocalDir)
	}
	return out
}

// SkillDirs lists the skill subdirectories under a declared source. It reads
// exactly one level below the source root and requires a SKILL.md marker file
// (or a skill.yaml manifest) to count a directory as a skill — declaration
// only, never a deep recursive scan.
//
// Args:
//   - source: the declared source directory.
//
// Returns:
//   - []string: absolute skill directories, sorted for determinism.
//   - error: wrapped filesystem error, or nil.
func (s *SourceManager) SkillDirs(source SourceDir) ([]string, error) {
	entries, err := os.ReadDir(source.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // declared but absent root is not an error
		}
		return nil, fmt.Errorf("ares_skills: read source %s: %w", source.Path, err)
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(source.Path, e.Name())
		if hasDeclaredMarker(dir) {
			dirs = append(dirs, dir)
		}
	}
	sort.Strings(dirs)
	return dirs, nil
}

// hasDeclaredMarker reports whether a directory is a declared skill: it must
// contain a SKILL.md file (the instruction surface) or a skill.yaml manifest.
//
// Args:
//   - dir: candidate skill directory.
//
// Returns:
//   - bool: true when the directory declares itself as a skill.
func hasDeclaredMarker(dir string) bool {
	if st, err := os.Stat(filepath.Join(dir, "SKILL.md")); err == nil && !st.IsDir() {
		return true
	}
	if st, err := os.Stat(filepath.Join(dir, "skill.yaml")); err == nil && !st.IsDir() {
		return true
	}
	return false
}
