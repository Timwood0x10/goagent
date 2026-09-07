package ares_skills

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Indexer builds metadata-only SkillIndexEntry records from declared sources.
// Level 0 of progressive disclosure: only the front matter and manifest are
// read; the SKILL.md body and references are deliberately left unloaded.
type Indexer struct{}

// NewIndexer creates an Indexer.
//
// Returns:
//   - *Indexer: ready to index declared sources.
func NewIndexer() *Indexer {
	return &Indexer{}
}

// Index scans every declared source and returns one metadata entry per skill
// directory. Non-declared directories are skipped by the SourceManager.
//
// Args:
//   - sources: the declared source directories.
//   - sm: the SourceManager used to enumerate skill dirs.
//
// Returns:
//   - []SkillIndexEntry: metadata-only entries, sorted by ID.
//   - error: wrapped error on failure, or nil.
func (ix *Indexer) Index(sources []SourceDir, sm *SourceManager) ([]SkillIndexEntry, error) {
	var entries []SkillIndexEntry
	for _, src := range sources {
		dirs, err := sm.SkillDirs(src)
		if err != nil {
			return nil, err
		}
		for _, dir := range dirs {
			entry, err := ix.indexOne(src.Kind, dir)
			if err != nil {
				return nil, err
			}
			if entry.ID != "" {
				entries = append(entries, entry)
			}
		}
	}
	sortEntries(entries)
	return entries, nil
}

// indexOne builds a single metadata entry for a skill directory, merging the
// SKILL.md front matter (if any) with the skill.yaml manifest (if any).
//
// Args:
//   - kind: the declaring source kind.
//   - dir: the skill directory.
//
// Returns:
//   - SkillIndexEntry: the metadata record.
//   - error: wrapped error on failure, or nil.
func (ix *Indexer) indexOne(kind SourceKind, dir string) (SkillIndexEntry, error) {
	entry := SkillIndexEntry{
		Source: kind,
		Path:   dir,
		ID:     filepath.Base(dir),
	}

	// Front matter from SKILL.md: id / name / description / keywords / version.
	front, err := parseFrontMatter(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return SkillIndexEntry{}, err
	}
	if v, ok := front["name"].(string); ok && v != "" {
		entry.Name = v
	}
	if v, ok := front["description"].(string); ok && v != "" {
		entry.Description = v
	}
	if v, ok := front["version"].(string); ok && v != "" {
		entry.Version = v
	}
	entry.Keywords = toStringSlice(front["keywords"])
	if caps, ok := front["capabilities"].([]string); ok {
		entry.Capabilities = caps
	}

	// Manifest declares execution carriers; its fields win over front matter.
	manifest, err := loadManifest(filepath.Join(dir, "skill.yaml"))
	if err != nil {
		return SkillIndexEntry{}, err
	}
	if manifest != nil {
		if manifest.ID != "" {
			entry.ID = manifest.ID
		}
		if manifest.Name != "" {
			entry.Name = manifest.Name
		}
		if manifest.Description != "" {
			entry.Description = manifest.Description
		}
		if len(manifest.Keywords) > 0 {
			entry.Keywords = manifest.Keywords
		}
		if manifest.Version != "" {
			entry.Version = manifest.Version
		}
		for _, t := range manifest.Tools {
			entry.ToolTypes = append(entry.ToolTypes, t.Type)
		}
	}

	// Deterministic content hash for change detection (design §5).
	entry.Hash = contentHash(dir)

	if entry.Name == "" {
		entry.Name = entry.ID
	}
	if entry.Description == "" {
		entry.Description = entry.ID // degraded but searchable
	}
	return entry, nil
}

// parseFrontMatter reads YAML front matter delimited by "---" lines from a
// SKILL.md file. Missing or malformed front matter yields empty metadata.
//
// Args:
//   - path: the SKILL.md path.
//
// Returns:
//   - map[string]any: parsed front matter, or nil.
//   - error: wrapped read error, or nil.
func parseFrontMatter(path string) (map[string]any, error) {
	f, err := os.Open(path) //nolint:gosec // path is a fixed file inside a declared source dir
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("ares_skills: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	var lines []string
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "---" {
		return map[string]any{}, nil // no front matter
	}
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "---" {
			break
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("ares_skills: scan %s: %w", path, err)
	}
	if len(lines) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := yaml.Unmarshal([]byte(strings.Join(lines, "\n")), &out); err != nil {
		return map[string]any{}, nil // malformed front matter degrades to empty metadata
	}
	return out, nil
}

// loadManifest parses a skill.yaml manifest, or returns nil when absent.
//
// Args:
//   - path: the manifest path.
//
// Returns:
//   - *Manifest: parsed manifest, or nil when the file is absent.
//   - error: wrapped parse error on malformed YAML, or nil.
func loadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path) //nolint:gosec // manifest path is inside a declared source dir
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil //nolint:nilnil // absent manifest is a valid "no tools declared" state
		}
		return nil, fmt.Errorf("ares_skills: read manifest %s: %w", path, err)
	}
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("ares_skills: parse manifest %s: %w", path, err)
	}
	return &m, nil
}

// toStringSlice normalizes an unknown front-matter value to []string.
//
// Args:
//   - v: the raw front-matter value.
//
// Returns:
//   - []string: the normalized slice (may be empty).
func toStringSlice(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// contentHash computes a deterministic hash over the skill directory's
// declaration files (SKILL.md + skill.yaml + tools listing), used for change
// detection without loading bodies.
//
// Args:
//   - dir: the skill directory.
//
// Returns:
//   - string: hex-encoded SHA-256 hash.
func contentHash(dir string) string {
	h := sha256.New()
	for _, name := range []string{"SKILL.md", "skill.yaml"} {
		if data, err := os.ReadFile(filepath.Join(dir, name)); err == nil { //nolint:gosec // fixed file names inside a declared source dir
			h.Write([]byte(name))
			h.Write(data)
		}
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "tools")); err == nil {
		for _, e := range entries {
			h.Write([]byte("tools:" + e.Name()))
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// sortEntries orders entries by ID then Source for deterministic output.
//
// Args:
//   - entries: entries to sort in place.
func sortEntries(entries []SkillIndexEntry) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && (entries[j].ID < entries[j-1].ID ||
			(entries[j].ID == entries[j-1].ID && entries[j].Source < entries[j-1].Source)); j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
}
