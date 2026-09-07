package eval

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Loader loads test suites from files.
type Loader struct{}

// NewLoader creates a new test suite loader.
func NewLoader() *Loader {
	return &Loader{}
}

// sensitiveSystemDirs lists directory names that must never be reachable via
// user-supplied suite paths, whether through absolute paths or relative
// traversal.
var sensitiveSystemDirs = []string{"etc", "proc", "sys", "dev", "boot", "root"}

// validateSuitePath rejects paths that traverse into sensitive system
// directories. Relative paths without traversal components are always allowed
// so legitimate project-relative paths (e.g. "../../../test/eval/basic.yaml")
// continue to work. Absolute paths and paths containing ".." are checked
// against the sensitive directory list.
func validateSuitePath(path string) error {
	if path == "" {
		return errors.New("invalid path: must not be empty")
	}
	cleaned := filepath.ToSlash(filepath.Clean(path))
	hasTraversal := strings.Contains(cleaned, "..")
	// Recognize both POSIX ("/abs") and Windows ("C:/abs") absolute paths;
	// previously only "/" was checked, so Windows drive paths slipped through
	// the sensitive-directory check.
	isAbsolute := strings.HasPrefix(cleaned, "/") ||
		(len(cleaned) >= 3 && cleaned[1] == ':' && (cleaned[2] == '/' || cleaned[2] == '\\'))
	// Relative paths without traversal cannot reach system directories.
	if !hasTraversal && !isAbsolute {
		return nil
	}
	cleanedLower := strings.ToLower(cleaned)
	for _, dir := range sensitiveSystemDirs {
		if strings.Contains(cleanedLower, "/"+dir+"/") ||
			strings.HasSuffix(cleanedLower, "/"+dir) ||
			cleanedLower == "/"+dir {
			return errors.New("invalid path: system directory access not allowed")
		}
	}
	return nil
}

// Load loads a test suite from a YAML file.
func (l *Loader) Load(path string) (*TestSuite, error) {
	if err := validateSuitePath(path); err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path) // #nosec G304 -- path validated by validateSuitePath
	if err != nil {
		return nil, fmt.Errorf("read suite file %q: %w", path, err)
	}

	var suite TestSuite
	if err := yaml.Unmarshal(data, &suite); err != nil {
		return nil, err
	}

	// Set default timeout for test cases without timeout
	for i := range suite.TestCases {
		if suite.TestCases[i].Timeout == 0 {
			suite.TestCases[i].Timeout = Duration(30 * time.Second)
		}
	}

	return &suite, nil
}

// LoadDir loads all test suites from a directory.
func (l *Loader) LoadDir(dir string) ([]TestSuite, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var suites []TestSuite
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		// Only load YAML files
		name := entry.Name()
		ext := filepath.Ext(name)
		if ext != ".yaml" && ext != ".yml" {
			continue
		}

		path := filepath.Join(dir, name)
		suite, err := l.Load(path)
		if err != nil {
			return nil, err
		}
		suites = append(suites, *suite)
	}

	return suites, nil
}
