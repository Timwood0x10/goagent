package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Timwood0x10/ares/internal/errors"
)

// WorkflowLoader loads workflow definitions from various sources.
type WorkflowLoader interface {
	Load(ctx context.Context, source string) (*Workflow, error)
}

// FileLoader loads workflows from files.
type FileLoader struct {
	decoder    Decoder
	allowedDir string
}

// Decoder decodes workflow files.
type Decoder interface {
	Decode(data []byte, v interface{}) error
}

// JSONDecoder decodes JSON files.
type JSONDecoder struct{}

// Decode decodes JSON data.
func (d *JSONDecoder) Decode(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

// YAMLDecoder decodes YAML files.
type YAMLDecoder struct{}

// Decode decodes YAML data.
func (d *YAMLDecoder) Decode(data []byte, v interface{}) error {
	return yaml.Unmarshal(data, v)
}

// NewFileLoader creates a new FileLoader.
type FileLoaderOption func(*FileLoader)

// WithAllowedDir sets the allowed directory for security checks.
func WithAllowedDir(dir string) FileLoaderOption {
	return func(fl *FileLoader) {
		fl.allowedDir = dir
	}
}

func NewFileLoader(decoder Decoder, opts ...FileLoaderOption) *FileLoader {
	fl := &FileLoader{
		decoder: decoder,
	}
	for _, opt := range opts {
		opt(fl)
	}
	return fl
}

// NewJSONFileLoader creates a FileLoader for JSON files.
func NewJSONFileLoader(opts ...FileLoaderOption) *FileLoader {
	return NewFileLoader(&JSONDecoder{}, opts...)
}

// NewYAMLFileLoader creates a FileLoader for YAML files.
func NewYAMLFileLoader(opts ...FileLoaderOption) *FileLoader {
	return NewFileLoader(&YAMLDecoder{}, opts...)
}

// Load loads a workflow from a file.
func (l *FileLoader) Load(ctx context.Context, source string) (*Workflow, error) {
	// Security: validate path is within allowed directory.
	// filepath.Rel is used instead of strings.HasPrefix because the prefix
	// check is vulnerable to sibling-directory attacks (e.g. /safe/dir-evil
	// passes a /safe/dir prefix check).
	if l.allowedDir != "" {
		absPath, err := filepath.Abs(source)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to get absolute path: %s", source)
		}
		absDir, err := filepath.Abs(l.allowedDir)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to get absolute directory: %s", l.allowedDir)
		}
		rel, err := filepath.Rel(absDir, absPath)
		if err != nil {
			return nil, fmt.Errorf("path %s is outside allowed directory %s", source, l.allowedDir)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("path %s is outside allowed directory %s", source, l.allowedDir)
		}
	}

	data, err := os.ReadFile(source) // #nosec G304
	if err != nil {
		return nil, errors.Wrapf(err, "read workflow file %s", source)
	}

	return l.Parse(ctx, data, source)
}

// Parse parses workflow data.
func (l *FileLoader) Parse(ctx context.Context, data []byte, source string) (*Workflow, error) {
	var workflow WorkflowFile
	if err := l.decoder.Decode(data, &workflow); err != nil {
		return nil, errors.Wrapf(err, "decode workflow %s", source)
	}

	return l.convert(&workflow, source)
}

// convert converts a WorkflowFile to a Workflow.
func (l *FileLoader) convert(f *WorkflowFile, source string) (*Workflow, error) {
	now := time.Now()
	steps := make([]*Step, 0, len(f.Steps))

	for i, stepFile := range f.Steps {
		step, err := convertStep(stepFile)
		if err != nil {
			return nil, errors.Wrapf(err, "convert step %d", i)
		}
		steps = append(steps, step)
	}

	variables := make(map[string]string)
	for k, v := range f.Variables {
		variables[k] = v
	}

	metadata := make(map[string]string)
	for k, v := range f.Metadata {
		metadata[k] = v
	}

	return &Workflow{
		ID:          f.ID,
		Name:        f.Name,
		Version:     f.Version,
		Description: f.Description,
		Steps:       steps,
		Variables:   variables,
		Metadata:    metadata,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

// convertStep converts a StepFile to a Step.
func convertStep(f *StepFile) (*Step, error) {
	step := &Step{
		ID:        f.ID,
		Name:      f.Name,
		AgentType: f.AgentType,
		Input:     f.Input,
		DependsOn: f.DependsOn,
		Timeout:   f.Timeout,
		Metadata:  f.Metadata,
		Status:    StepStatusPending,
	}

	if f.RetryPolicy != nil {
		step.RetryPolicy = &RetryPolicy{
			MaxAttempts:       f.RetryPolicy.MaxAttempts,
			InitialDelay:      f.RetryPolicy.InitialDelay,
			MaxDelay:          f.RetryPolicy.MaxDelay,
			BackoffMultiplier: f.RetryPolicy.BackoffMultiplier,
		}
	}

	return step, nil
}

// WorkflowFile represents a workflow definition from a file.
type WorkflowFile struct {
	ID          string            `json:"id" yaml:"id"`
	Name        string            `json:"name" yaml:"name"`
	Version     string            `json:"version" yaml:"version"`
	Description string            `json:"description" yaml:"description"`
	Steps       []*StepFile       `json:"steps" yaml:"steps"`
	Variables   map[string]string `json:"variables" yaml:"variables"`
	Metadata    map[string]string `json:"metadata" yaml:"metadata"`
}

// StepFile represents a step definition from a file.
type StepFile struct {
	ID          string            `json:"id" yaml:"id"`
	Name        string            `json:"name" yaml:"name"`
	AgentType   string            `json:"agent_type" yaml:"agent_type"`
	Input       string            `json:"input" yaml:"input"`
	DependsOn   []string          `json:"depends_on" yaml:"depends_on"`
	Timeout     time.Duration     `json:"timeout" yaml:"timeout"`
	RetryPolicy *RetryPolicyFile  `json:"retry_policy" yaml:"retry_policy"`
	Metadata    map[string]string `json:"metadata" yaml:"metadata"`
}

// RetryPolicyFile represents retry policy from a file.
type RetryPolicyFile struct {
	MaxAttempts       int           `json:"max_attempts" yaml:"max_attempts"`
	InitialDelay      time.Duration `json:"initial_delay" yaml:"initial_delay"`
	MaxDelay          time.Duration `json:"max_delay" yaml:"max_delay"`
	BackoffMultiplier float64       `json:"backoff_multiplier" yaml:"backoff_multiplier"`
}

// DirectoryLoader loads workflows from a directory.
type DirectoryLoader struct {
	fileLoader *FileLoader
}

// NewDirectoryLoader creates a new DirectoryLoader.
func NewDirectoryLoader(fileLoader *FileLoader) *DirectoryLoader {
	return &DirectoryLoader{
		fileLoader: fileLoader,
	}
}

// LoadAll loads all workflows from a directory.
func (l *DirectoryLoader) LoadAll(ctx context.Context, dir string) (map[string]*Workflow, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, errors.Wrapf(err, "read directory %s", dir)
	}

	workflows := make(map[string]*Workflow)

	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "" {
			continue
		}

		ext := getFileExt(entry.Name())
		if ext != ".json" && ext != ".yaml" && ext != ".yml" {
			continue
		}

		path := dir + "/" + entry.Name()
		workflow, err := l.fileLoader.Load(ctx, path)
		if err != nil {
			return nil, errors.Wrapf(err, "load workflow %s", path)
		}

		if _, exists := workflows[workflow.ID]; exists {
			return nil, ErrDuplicateID
		}

		workflows[workflow.ID] = workflow
	}

	return workflows, nil
}

// getFileExt returns the file extension.
func getFileExt(filename string) string {
	for i := len(filename) - 1; i >= 0; i-- {
		if filename[i] == '.' {
			return filename[i:]
		}
	}
	return ""
}
