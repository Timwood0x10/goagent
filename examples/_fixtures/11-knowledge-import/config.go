// Command mdkb builds a structure-aware markdown knowledge base on top of
// PostgreSQL + pgvector, and answers questions over it via RAG.
//
// This file defines the configuration model, its loader, default filling, and
// startup validation. The YAML layout is intentionally grouped into the five
// standard sections: database / embedding / llm / memory / knowledge.
package main

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// expectedVectorDim is the vector dimension the target table expects.
// The shared storage schema stores vectors in "knowledge_chunks_1024",
// declared as VECTOR(1024); embeddings of any other size cannot be inserted.
const expectedVectorDim = 1024

// DatabaseConfig holds PostgreSQL connection settings.
type DatabaseConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	Database string `yaml:"database"`
	SSLMode  string `yaml:"sslmode"`
}

// EmbeddingConfig holds settings for the text embedding service.
type EmbeddingConfig struct {
	ServiceURL string `yaml:"service_url"`
	Model      string `yaml:"model"`
	Dimensions int    `yaml:"dimensions"`
	// TimeoutSeconds bounds a single embedding HTTP call.
	TimeoutSeconds int `yaml:"timeout"`
}

// LLMConfig holds settings for the RAG answer-generation model.
// When Provider or Model is empty the example runs in retrieval-only mode.
type LLMConfig struct {
	Provider  string `yaml:"provider"`
	APIKey    string `yaml:"api_key"`
	BaseURL   string `yaml:"base_url"`
	Model     string `yaml:"model"`
	Timeout   int    `yaml:"timeout"`
	MaxTokens int    `yaml:"max_tokens"`
	// Backup is an optional fallback LLM (OpenAI-compatible) used when
	// the primary provider is unavailable.
	Backup *LLMBackupConfig `yaml:"backup"`
}

// LLMBackupConfig holds settings for a backup LLM provider.
type LLMBackupConfig struct {
	Provider  string `yaml:"provider"`
	APIKey    string `yaml:"api_key"`
	BaseURL   string `yaml:"base_url"`
	Model     string `yaml:"model"`
	Timeout   int    `yaml:"timeout"`
	MaxTokens int    `yaml:"max_tokens"`
}

// MemoryConfig is preserved for config-structure compatibility with the other
// knowledge examples. This focused example does not maintain conversational
// memory, but the section is parsed and validated so a single config.yaml can
// be shared across examples without edits.
type MemoryConfig struct {
	Enabled               bool `yaml:"enabled"`
	MaxHistory            int  `yaml:"max_history"`
	MaxSessions           int  `yaml:"max_sessions"`
	EnableDistillation    bool `yaml:"enable_distillation"`
	DistillationThreshold int  `yaml:"distillation_threshold"`
}

// KnowledgeConfig controls chunking and retrieval behaviour.
//
//   - ChunkSize is the soft maximum number of characters per chunk. Chunking is
//     section-first: a section is only split further when it exceeds this size,
//     and never in the middle of a code block or table.
//   - ChunkOverlap is the number of leading characters (heading breadcrumb) that
//     each continuation chunk repeats so a split section stays self-contained.
//   - TopK and MinScore bound retrieval.
//   - PassagePrefix / QueryPrefix are asymmetric embedding instructions used by
//     instruction-tuned models (e.g. e5 / qwen3-embedding).
type KnowledgeConfig struct {
	ChunkSize     int     `yaml:"chunk_size"`
	ChunkOverlap  int     `yaml:"chunk_overlap"`
	TopK          int     `yaml:"top_k"`
	MinScore      float64 `yaml:"min_score"`
	PassagePrefix string  `yaml:"passage_prefix"`
	QueryPrefix   string  `yaml:"query_prefix"`
}

// Config is the root configuration for the markdown knowledge base example.
type Config struct {
	Database  DatabaseConfig  `yaml:"database"`
	Embedding EmbeddingConfig `yaml:"embedding"`
	LLM       LLMConfig       `yaml:"llm"`
	Memory    MemoryConfig    `yaml:"memory"`
	Knowledge KnowledgeConfig `yaml:"knowledge"`
}

// LoadConfig reads, parses, default-fills and validates a YAML config file.
//
// Args:
//
//	path - filesystem path to the YAML config file, must be non-empty.
//
// Returns:
//
//	cfg - a fully validated configuration, never nil on success.
//	err - a read, parse or validation error with context.
func LoadConfig(path string) (*Config, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("load config: empty path")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load config %q: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}

	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validate config %q: %w", path, err)
	}
	return &cfg, nil
}

// applyDefaults fills unset fields with safe local-development defaults.
func (c *Config) applyDefaults() {
	c.applyDatabaseDefaults()
	c.applyEmbeddingDefaults()
	c.applyLLMDefaults()
	c.applyKnowledgeDefaults()
}

// applyDatabaseDefaults fills database defaults for local development.
func (c *Config) applyDatabaseDefaults() {
	if c.Database.Host == "" {
		c.Database.Host = "127.0.0.1"
	}
	if c.Database.Port == 0 {
		c.Database.Port = 5433
	}
	if c.Database.User == "" {
		c.Database.User = "postgres"
	}
	if c.Database.Database == "" {
		c.Database.Database = "ARES"
	}
	if c.Database.SSLMode == "" {
		c.Database.SSLMode = "disable"
	}
}

// applyEmbeddingDefaults fills embedding defaults.
func (c *Config) applyEmbeddingDefaults() {
	if c.Embedding.ServiceURL == "" {
		c.Embedding.ServiceURL = "http://localhost:11434"
	}
	if c.Embedding.Model == "" {
		c.Embedding.Model = "qwen3-embedding:0.6b"
	}
	if c.Embedding.Dimensions == 0 {
		c.Embedding.Dimensions = expectedVectorDim
	}
	if c.Embedding.TimeoutSeconds == 0 {
		c.Embedding.TimeoutSeconds = 30
	}
}

// applyLLMDefaults fills LLM defaults; provider/model stay empty when unset so
// the caller can detect retrieval-only mode.
func (c *Config) applyLLMDefaults() {
	if c.LLM.Timeout == 0 {
		c.LLM.Timeout = 120
	}
	if c.LLM.MaxTokens == 0 {
		c.LLM.MaxTokens = 2048
	}
	if c.LLM.Provider != "" && c.LLM.BaseURL == "" {
		c.LLM.BaseURL = "http://localhost:11434"
	}
}

// applyKnowledgeDefaults fills chunking and retrieval defaults.
func (c *Config) applyKnowledgeDefaults() {
	if c.Knowledge.ChunkSize == 0 {
		c.Knowledge.ChunkSize = 1500
	}
	if c.Knowledge.ChunkOverlap == 0 {
		c.Knowledge.ChunkOverlap = 120
	}
	if c.Knowledge.TopK == 0 {
		c.Knowledge.TopK = 6
	}
	if c.Knowledge.MinScore == 0 {
		c.Knowledge.MinScore = 0.35
	}
	if c.Knowledge.PassagePrefix == "" {
		c.Knowledge.PassagePrefix = "passage:"
	}
	if c.Knowledge.QueryPrefix == "" {
		c.Knowledge.QueryPrefix = "query:"
	}
}

// validate enforces value ranges and cross-field constraints at startup.
// It returns the first violation found so misconfiguration fails fast.
func (c *Config) validate() error {
	if err := c.validateDatabase(); err != nil {
		return err
	}
	if err := c.validateEmbedding(); err != nil {
		return err
	}
	return c.validateKnowledge()
}

// validateDatabase checks database connection fields.
func (c *Config) validateDatabase() error {
	if c.Database.Port <= 0 || c.Database.Port > 65535 {
		return fmt.Errorf("database.port %d out of range (1-65535)", c.Database.Port)
	}
	if c.Database.User == "" {
		return fmt.Errorf("database.user is required")
	}
	if c.Database.Database == "" {
		return fmt.Errorf("database.database is required")
	}
	switch c.Database.SSLMode {
	case "disable", "require", "verify-ca", "verify-full", "prefer", "allow":
	default:
		return fmt.Errorf("database.sslmode %q is invalid", c.Database.SSLMode)
	}
	return nil
}

// validateEmbedding checks embedding fields and vector dimension compatibility.
func (c *Config) validateEmbedding() error {
	if c.Embedding.ServiceURL == "" {
		return fmt.Errorf("embedding.service_url is required")
	}
	if c.Embedding.Model == "" {
		return fmt.Errorf("embedding.model is required")
	}
	if c.Embedding.TimeoutSeconds <= 0 {
		return fmt.Errorf("embedding.timeout must be > 0")
	}
	if c.Embedding.Dimensions != expectedVectorDim {
		return fmt.Errorf(
			"embedding.dimensions is %d but the storage table expects %d; "+
				"choose a %d-dim model or migrate a matching table",
			c.Embedding.Dimensions, expectedVectorDim, expectedVectorDim)
	}
	return nil
}

// validateKnowledge checks chunking and retrieval ranges.
func (c *Config) validateKnowledge() error {
	if c.Knowledge.ChunkSize < 200 {
		return fmt.Errorf("knowledge.chunk_size %d too small (min 200)", c.Knowledge.ChunkSize)
	}
	if c.Knowledge.ChunkOverlap < 0 || c.Knowledge.ChunkOverlap >= c.Knowledge.ChunkSize {
		return fmt.Errorf("knowledge.chunk_overlap %d must be in [0, chunk_size)", c.Knowledge.ChunkOverlap)
	}
	if c.Knowledge.TopK <= 0 {
		return fmt.Errorf("knowledge.top_k must be > 0")
	}
	if c.Knowledge.MinScore < 0 || c.Knowledge.MinScore > 1 {
		return fmt.Errorf("knowledge.min_score %.3f must be in [0, 1]", c.Knowledge.MinScore)
	}
	return nil
}

// llmEnabled reports whether an LLM is configured for answer generation.
func (c *Config) llmEnabled() bool {
	return c.LLM.Provider != "" && c.LLM.Model != ""
}
