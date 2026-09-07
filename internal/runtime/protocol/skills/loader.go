package ares_skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Loader implements SkillLoader: it loads the Level-1 SKILL.md body and the
// Level-2 references for a skill by ID. Bodies are fetched on demand only —
// the index never holds them (progressive disclosure §6).
type Loader struct {
	// index maps skill ID -> index entry (built by the catalog).
	index map[string]SkillIndexEntry
}

// NewLoader creates a Loader backed by an entry set.
//
// Args:
//   - entries: the metadata index entries.
//
// Returns:
//   - *Loader: ready to load skill bodies by ID.
func NewLoader(entries []SkillIndexEntry) *Loader {
	m := make(map[string]SkillIndexEntry, len(entries))
	for _, e := range entries {
		m[e.ID] = e
	}
	return &Loader{index: m}
}

// Load returns the SKILL.md body for a skill ID. The body is the Level-1
// disclosure (full instructions + when-to-use), fetched only after the task
// matched the metadata.
//
// Args:
//   - id: the skill ID.
//
// Returns:
//   - string: the SKILL.md content.
//   - error: ErrSkillNotFound when unknown, wrapped error on read failure.
func (l *Loader) Load(id string) (string, error) {
	entry, ok := l.index[id]
	if !ok {
		return "", ErrSkillNotFound
	}
	// P1-8: Remote skills (from HTTP manifest) have a Path that is a URL.
	// SKILL.md is not available locally; return a clear error instead of
	// trying to read from CWD.
	if strings.HasPrefix(entry.Path, "http://") || strings.HasPrefix(entry.Path, "https://") {
		return "", fmt.Errorf("ares_skills: skill %s is remote (source: %s); body not available locally", id, entry.Path)
	}
	path := filepath.Join(entry.Path, "SKILL.md")
	data, err := os.ReadFile(path) //nolint:gosec // skill path comes from the declared-source index
	if err != nil {
		return "", fmt.Errorf("ares_skills: load skill %s: %w", id, err)
	}
	return string(data), nil
}

// Has reports whether a skill ID is indexed (without loading its body).
//
// Args:
//   - id: the skill ID.
//
// Returns:
//   - bool: true when the skill is known to the index.
func (l *Loader) Has(id string) bool {
	_, ok := l.index[id]
	return ok
}

// ListReferences returns the file names under a skill's references directory
// (Level-2 disclosure surface). Contents are read only when explicitly needed.
//
// Args:
//   - id: the skill ID.
//
// Returns:
//   - []string: reference file names (may be empty).
//   - error: ErrSkillNotFound when unknown, wrapped error on read failure.
func (l *Loader) ListReferences(id string) ([]string, error) {
	entry, ok := l.index[id]
	if !ok {
		return nil, ErrSkillNotFound
	}
	if strings.HasPrefix(entry.Path, "http://") || strings.HasPrefix(entry.Path, "https://") {
		return nil, nil
	}
	refDir := filepath.Join(entry.Path, "references")
	entries, err := os.ReadDir(refDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("ares_skills: list references %s: %w", id, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// LoadReference returns the content of one reference file for a skill.
//
// Args:
//   - id: the skill ID.
//   - name: the reference file name (no path traversal allowed).
//
// Returns:
//   - string: the reference content.
//   - error: ErrSkillNotFound / wrapped error, or nil.
func (l *Loader) LoadReference(id, name string) (string, error) {
	entry, ok := l.index[id]
	if !ok {
		return "", ErrSkillNotFound
	}
	if strings.HasPrefix(entry.Path, "http://") || strings.HasPrefix(entry.Path, "https://") {
		return "", fmt.Errorf("ares_skills: skill %s is remote; references not available locally", id)
	}
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") || name == ".." {
		return "", fmt.Errorf("ares_skills: invalid reference name %q", name)
	}
	data, err := os.ReadFile(filepath.Join(entry.Path, "references", name)) //nolint:gosec // traversal rejected above; path is inside the declared skill dir
	if err != nil {
		return "", fmt.Errorf("ares_skills: load reference %s/%s: %w", id, name, err)
	}
	return string(data), nil
}

// ErrSkillNotFound is returned when a skill ID is absent from the index.
var ErrSkillNotFound = errors.New("ares_skills: skill not found")
