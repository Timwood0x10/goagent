package ares_skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// SkillSourcesConfig is the top-level shape of ~/.ares/config.toml skill
// section (design §4: registered extra sources are declared, never scanned).
type SkillSourcesConfig struct {
	// SkillSources lists declared extra skill directories.
	SkillSources []SkillSourceEntry `toml:"skill_sources"`
}

// SkillSourceEntry is one declared skill source.
type SkillSourceEntry struct {
	// Type is the source type ("directory" or "git"; http/oci are future).
	Type string `toml:"type"`
	// Path is the declared directory path (may contain "~").
	Path string `toml:"path"`
	// URL is the git repository URL (type = "git").
	URL string `toml:"url"`
	// LocalDir is the git clone cache directory (type = "git", may contain "~").
	LocalDir string `toml:"local_dir"`
	// ManifestURL is the skill manifest endpoint (type = "http"/"oci").
	ManifestURL string `toml:"manifest_url"`
}

// DefaultConfigPath returns the user-level ARES config path (~/.ares/config.toml).
//
// Returns:
//   - string: the config path, or "" when the home directory is unavailable.
func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ares", "config.toml")
}

// LoadRegisteredSkillDirs parses the [[skill_sources]] entries of an ARES
// config file and returns the declared directory paths with "~" expanded.
// Only type "directory" entries are honored; git sources are returned by
// LoadSkillSources and unknown types are skipped.
//
// Args:
//   - configPath: path to the config.toml file ("" means the default path;
//     a missing file yields no sources, not an error).
//
// Returns:
//   - []string: expanded, deduplicated declared directories.
//   - error: wrapped parse error on malformed TOML, or nil.
func LoadRegisteredSkillDirs(configPath string) ([]string, error) {
	dirs, _, _, err := LoadSkillSources(configPath)
	return dirs, err
}

// LoadSkillSources parses the [[skill_sources]] entries of an ARES config
// file into directory, git and http/oci sources. Directory paths are expanded
// ("~") and deduplicated; git and http sources keep their URL. Unknown source
// types are skipped so future source kinds do not break existing configs.
//
// Args:
//   - configPath: path to the config.toml file ("" means the default path;
//     a missing file yields empty results, not an error).
//
// Returns:
//   - []string: declared directory paths.
//   - []GitSource: declared git sources.
//   - []HTTPSource: declared http/oci manifest sources.
//   - error: wrapped parse error on malformed TOML, or nil.
func LoadSkillSources(configPath string) ([]string, []GitSource, []HTTPSource, error) {
	if configPath == "" {
		configPath = DefaultConfigPath()
	}
	data, err := os.ReadFile(configPath) //nolint:gosec // explicit user config path
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil, nil
		}
		return nil, nil, nil, fmt.Errorf("ares_skills: read config %s: %w", configPath, err)
	}
	var cfg SkillSourcesConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, nil, nil, fmt.Errorf("ares_skills: parse config %s: %w", configPath, err)
	}
	seen := make(map[string]bool)
	var dirs []string
	var gits []GitSource
	var httpSources []HTTPSource
	for _, src := range cfg.SkillSources {
		switch src.Type {
		case "", "directory":
			path := expandHome(src.Path)
			if path == "" || seen[path] {
				continue
			}
			seen[path] = true
			dirs = append(dirs, path)
		case "git":
			if src.URL == "" {
				continue
			}
			gits = append(gits, GitSource{URL: src.URL, LocalDir: expandHome(src.LocalDir)})
		case "http", "oci":
			if src.ManifestURL == "" {
				continue
			}
			httpSources = append(httpSources, HTTPSource{URL: src.ManifestURL})
		default:
			continue // future source types are declared but not yet honored
		}
	}
	return dirs, gits, httpSources, nil
}

// expandHome replaces a leading "~" with the user's home directory.
//
// Args:
//   - path: the raw path from the config.
//
// Returns:
//   - string: the expanded path, or the original when home is unavailable.
func expandHome(path string) string {
	if path == "" || path == "~" {
		return ""
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	return path
}
