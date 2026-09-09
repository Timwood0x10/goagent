package sdk

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/Timwood0x10/ares/internal/knowledge"
)

// Sentinel errors for config validation. Wrap with %w to preserve chain.
var (
	// ErrNilConfig signals Validate was called on a nil ConfigFile.
	ErrNilConfig = errors.New("nil config")
	// ErrInvalidRange signals a field value outside its valid range.
	ErrInvalidRange = errors.New("value out of valid range")
	// ErrMissingValue signals a required companion field was left unset.
	ErrMissingValue = errors.New("required field missing")
	// ErrNilProvider signals a nil provider was passed to WithKnowledgeProvider.
	ErrNilProvider = errors.New("nil provider")
)

// Provider constants used in config file parsing.
const (
	providerOllama     = "ollama"
	providerOpenAI     = "openai"
	providerAnthropic  = "anthropic"
	providerOpenRouter = "openrouter"
	defaultModel       = "llama3.2"
	// defaultOpenAIModel is the default model used when the OpenAI provider
	// is selected without an explicit model.
	defaultOpenAIModel = "gpt-4o-mini"
	// defaultMaxIterations is the default cap on the ReAct tool-calling loop.
	defaultMaxIterations = 10
)

// ConfigFile mirrors ares.yaml structure for config-driven Runtime creation.
// Use LoadConfigFile to read from disk, then pass to New.
//
// Each section is optional: a section left at its zero value causes the sdk
// to fall back to the corresponding component default, mirroring the
// "one yaml drives all components; missing means default" philosophy
// established by examples/knowledge-base in v0.2.4.
type ConfigFile struct {
	LLM       LLMFileConfig       `yaml:"llm"`
	Database  DatabaseFileConfig  `yaml:"database"`
	Embedding EmbeddingFileConfig `yaml:"embedding"`
	Memory    MemoryFileConfig    `yaml:"memory"`
	Knowledge KnowledgeFileConfig `yaml:"knowledge"`
	Tools     struct {
		Builtin bool     `yaml:"builtin"`
		MCP     []string `yaml:"mcp"`
	} `yaml:"tools"`
	Reflection struct {
		Enabled bool `yaml:"enabled"`
	} `yaml:"reflection"`
	Evolution struct {
		Enabled bool `yaml:"enabled"`
	} `yaml:"evolution"`
}

// MemoryFileConfig carries all memory subsystem knobs. Fields left at their
// zero value cause the sdk to fall back to the component default.
type MemoryFileConfig struct {
	Enabled     bool `yaml:"enabled"`
	MaxHistory  int  `yaml:"max_history"`
	MaxSessions int  `yaml:"max_sessions"`
	// EnableDistillation tri-state: nil defaults to true,
	// mirroring ares_config.MemoryConfig so SDK yaml and serve yaml agree.
	EnableDistillation    *bool `yaml:"enable_distillation"`
	DistillationThreshold int   `yaml:"distillation_threshold"`
	// EnableRAG enables retrieval-augmented generation: past experiences and
	// distilled memories are retrieved and injected into the LLM prompt.
	// Default: false (opt-in).
	EnableRAG bool `yaml:"enable_rag"`
	// RAGTopK is the maximum number of retrieved snippets to inject.
	// Must be >= 1 when EnableRAG is true.
	RAGTopK int `yaml:"rag_top_k"`
	// RAGMinScore is the minimum similarity score for a retrieved snippet to
	// be included. Must be in [0, 1] when EnableRAG is true.
	RAGMinScore float64 `yaml:"rag_min_score"`
}

// DistillationEnabled reports the tri-state: nil defaults to true,
// mirroring ares_config.MemoryConfig so SDK yaml and serve yaml
// agree.
func (m *MemoryFileConfig) DistillationEnabled() bool {
	return m.EnableDistillation == nil || *m.EnableDistillation
}

// DatabaseFileConfig declares PostgreSQL connection parameters. When the
// Database section is omitted entirely, the sdk uses in-memory storage.
type DatabaseFileConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	Database string `yaml:"database"`
	SSLMode  string `yaml:"ssl_mode"`
}

// EmbeddingFileConfig declares an external embedding service endpoint. When
// omitted, the sdk falls back to the default embedding behaviour.
type EmbeddingFileConfig struct {
	ServiceURL string `yaml:"service_url"`
	Model      string `yaml:"model"`
}

// KnowledgeFileConfig controls retrieval chunking and similarity bounds. When
// omitted, the sdk uses default retrieval parameters.
type KnowledgeFileConfig struct {
	ChunkSize    int     `yaml:"chunk_size"`
	ChunkOverlap int     `yaml:"chunk_overlap"`
	TopK         int     `yaml:"top_k"`
	MinScore     float64 `yaml:"min_score"`
	// Quality configures the AKG fact quality gate. When all fields are zero
	// the sdk falls back to knowledge.DefaultQualityGateConfig at apply time.
	Quality   QualityGateFileConfig        `yaml:"quality"`
	Embedding KnowledgeEmbeddingFileConfig `yaml:"embedding"`
}

// QualityGateFileConfig mirrors the quality section of the knowledge block in
// ares.yaml. It maps directly to knowledge.QualityGateConfig. Fields left at
// zero fall back to the knowledge package defaults.
type QualityGateFileConfig struct {
	MinExtraction     float64 `yaml:"min_extraction"`
	MinConsistency    float64 `yaml:"min_consistency"`
	MinFinalScore     float64 `yaml:"min_final_score"`
	MaxFactsPerIngest int     `yaml:"max_facts_per_ingest"`
	EnableDedup       bool    `yaml:"enable_dedup"`
	DedupThreshold    float64 `yaml:"dedup_threshold"`
}

// KnowledgeEmbeddingFileConfig configures the embedding model and endpoint
// used by the AKG distillation pipeline and StoreProvider. When model is empty
// the sdk falls back to the embedding service configured via the top-level
// embedding block.
type KnowledgeEmbeddingFileConfig struct {
	Model   string `yaml:"model"`
	BaseURL string `yaml:"base_url"`
}

// LLMFileConfig mirrors the llm section of ares.yaml.
type LLMFileConfig struct {
	Provider    string  `yaml:"provider"`
	Model       string  `yaml:"model"`
	APIKey      string  `yaml:"api_key"`
	BaseURL     string  `yaml:"base_url"`
	Temperature float64 `yaml:"temperature"`
	MaxTokens   int     `yaml:"max_tokens"`
	// MaxPromptLength caps the prompt character count (0 = provider default
	// 8192). Previously NOT parsed here — the field existed in core.LLMConfig
	// but the YAML→Option bridge dropped it, so a large value in ares.yaml
	// was silently ignored and long agent runs died at 8192.
	MaxPromptLength int `yaml:"max_prompt_length"`
}

// LoadConfigFile reads, parses and validates a YAML config file.
// Returns an error if the file cannot be read, parsed, or fails validation.
//
// Args:
//
//	path - filesystem path to the YAML file, must be non-empty.
//
// Returns:
//
//	cfg - a fully validated configuration, never nil on success.
//	err - a read, parse or validation error with context wrapping.
func LoadConfigFile(path string) (*ConfigFile, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path comes from user flag, safe
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg ConfigFile
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}
	return &cfg, nil
}

// Validate verifies that all configured values fall within their valid ranges.
// Sections left at zero value are skipped: they defer to the component default.
//
// Returns:
//
//	err - nil when valid, otherwise a wrapped sentinel describing the offending field.
func (c *ConfigFile) Validate() error {
	if c == nil {
		return fmt.Errorf("config: %w", ErrNilConfig)
	}
	if err := c.validateLLM(); err != nil {
		return err
	}
	if err := c.validateMemory(); err != nil {
		return err
	}
	// Database section: validate only when host is set (section present).
	if c.Database.Host != "" {
		if c.Database.Port < 1 || c.Database.Port > 65535 {
			return fmt.Errorf("database.port %d: %w", c.Database.Port, ErrInvalidRange)
		}
	}
	// Embedding section: validate only when service URL is set.
	if c.Embedding.ServiceURL != "" && c.Embedding.Model == "" {
		return fmt.Errorf("embedding.model: %w", ErrMissingValue)
	}
	return c.validateKnowledge()
}

// validateLLM checks the LLM section: temperature and max_tokens ranges fire
// only when a provider is configured.
func (c *ConfigFile) validateLLM() error {
	if c.LLM.Provider == "" {
		return nil
	}
	if c.LLM.Temperature < 0 || c.LLM.Temperature > 2 {
		return fmt.Errorf("llm.temperature %v: %w", c.LLM.Temperature, ErrInvalidRange)
	}
	if c.LLM.MaxTokens < 0 {
		return fmt.Errorf("llm.max_tokens %d: %w", c.LLM.MaxTokens, ErrInvalidRange)
	}
	return nil
}

// validateMemory checks the Memory section, including RAG fields that only fire
// when EnableRAG is true.
func (c *ConfigFile) validateMemory() error {
	if !c.Memory.Enabled {
		return nil
	}
	if c.Memory.MaxHistory < 0 {
		return fmt.Errorf("memory.max_history %d: %w", c.Memory.MaxHistory, ErrInvalidRange)
	}
	if c.Memory.MaxSessions < 0 {
		return fmt.Errorf("memory.max_sessions %d: %w", c.Memory.MaxSessions, ErrInvalidRange)
	}
	// DistillationThreshold 0 means "unset": the sdk falls back to the
	// component default at apply time. Negative is invalid.
	if c.Memory.DistillationThreshold < 0 {
		return fmt.Errorf("memory.distillation_threshold %d: %w",
			c.Memory.DistillationThreshold, ErrInvalidRange)
	}
	// RAG validation only fires when EnableRAG is true. RAGTopK must be
	// at least 1 (zero is invalid here, unlike other memory knobs where
	// zero means "use default"), and RAGMinScore must be a valid
	// similarity score in [0, 1].
	if c.Memory.EnableRAG {
		if c.Memory.RAGTopK < 1 {
			return fmt.Errorf("memory.rag_top_k %d: %w", c.Memory.RAGTopK, ErrInvalidRange)
		}
		if c.Memory.RAGMinScore < 0 || c.Memory.RAGMinScore > 1 {
			return fmt.Errorf("memory.rag_min_score %v: %w", c.Memory.RAGMinScore, ErrInvalidRange)
		}
	}
	return nil
}

// validateKnowledge checks the Knowledge chunking fields and the 0.2.9 quality
// gate score ranges. The quality gate block fires only when MinFinalScore > 0
// (the trigger field, matching ToOptions apply logic).
func (c *ConfigFile) validateKnowledge() error {
	if c.Knowledge.ChunkSize > 0 {
		if c.Knowledge.ChunkOverlap < 0 || c.Knowledge.ChunkOverlap >= c.Knowledge.ChunkSize {
			return fmt.Errorf("knowledge.chunk_overlap %d vs chunk_size %d: %w",
				c.Knowledge.ChunkOverlap, c.Knowledge.ChunkSize, ErrInvalidRange)
		}
		if c.Knowledge.TopK < 1 {
			return fmt.Errorf("knowledge.top_k %d: %w", c.Knowledge.TopK, ErrInvalidRange)
		}
		if c.Knowledge.MinScore < 0 || c.Knowledge.MinScore > 1 {
			return fmt.Errorf("knowledge.min_score %v: %w", c.Knowledge.MinScore, ErrInvalidRange)
		}
	}
	if c.Knowledge.Quality.MinFinalScore <= 0 {
		return nil
	}
	q := c.Knowledge.Quality
	if q.MinExtraction < 0 || q.MinExtraction > 1 {
		return fmt.Errorf("knowledge.quality.min_extraction %v: %w", q.MinExtraction, ErrInvalidRange)
	}
	if q.MinConsistency < 0 || q.MinConsistency > 1 {
		return fmt.Errorf("knowledge.quality.min_consistency %v: %w", q.MinConsistency, ErrInvalidRange)
	}
	if q.MinFinalScore < 0 || q.MinFinalScore > 1 {
		return fmt.Errorf("knowledge.quality.min_final_score %v: %w", q.MinFinalScore, ErrInvalidRange)
	}
	if q.MaxFactsPerIngest < 0 {
		return fmt.Errorf("knowledge.quality.max_facts_per_ingest %d: %w", q.MaxFactsPerIngest, ErrInvalidRange)
	}
	if q.DedupThreshold < 0 || q.DedupThreshold > 1 {
		return fmt.Errorf("knowledge.quality.dedup_threshold %v: %w", q.DedupThreshold, ErrInvalidRange)
	}
	return nil
}

// resolveAPIKey returns the config-provided key when non-empty, otherwise falls
// back to the named environment variable. This avoids storing secrets in YAML.
func resolveAPIKey(configKey, envVar string) string {
	if configKey != "" {
		return configKey
	}
	return os.Getenv(envVar)
}

// ToOptions converts a ConfigFile into a slice of Option values that can be
// passed to New or NewRuntime.
func (c *ConfigFile) ToOptions() ([]Option, error) {
	var opts []Option

	// LLM provider.
	switch c.LLM.Provider {
	case "", providerOllama:
		model := c.LLM.Model
		if model == "" {
			model = defaultModel
		}
		opts = append(opts, WithOllama(model))
	case providerOpenAI:
		model := c.LLM.Model
		if model == "" {
			model = defaultOpenAIModel
		}
		opts = append(opts, WithOpenAI(model))
		if key := resolveAPIKey(c.LLM.APIKey, "OPENAI_API_KEY"); key != "" {
			opts = append(opts, WithAPIKey(key))
		}
	case providerAnthropic:
		model := c.LLM.Model
		if model == "" {
			model = "claude-3-haiku"
		}
		opts = append(opts, WithAnthropic(model))
		if key := resolveAPIKey(c.LLM.APIKey, "ANTHROPIC_API_KEY"); key != "" {
			opts = append(opts, WithAPIKey(key))
		}
	case providerOpenRouter:
		model := c.LLM.Model
		if model == "" {
			model = "openai/gpt-4o-mini"
		}
		opts = append(opts, WithOpenRouter(model))
		if key := resolveAPIKey(c.LLM.APIKey, "OPENROUTER_API_KEY"); key != "" {
			opts = append(opts, WithAPIKey(key))
		}
	default:
		return nil, fmt.Errorf("unknown LLM provider: %s", c.LLM.Provider)
	}

	if c.LLM.BaseURL != "" {
		opts = append(opts, WithBaseURL(c.LLM.BaseURL))
	}

	// Bridge YAML LLM tuning fields into core.LLMConfig. Without these the
	// values in ares.yaml were silently dropped (the fields existed in
	// core.LLMConfig but nothing wired them), so users got hardcoded
	// defaults 0.7/2048 regardless of what they configured.
	if c.LLM.Temperature > 0 {
		opts = append(opts, func(cfg *config) error {
			cfg.llmCfg.Temperature = c.LLM.Temperature
			return nil
		})
	}
	if c.LLM.MaxTokens > 0 {
		opts = append(opts, func(cfg *config) error {
			cfg.llmCfg.MaxTokens = c.LLM.MaxTokens
			return nil
		})
	}
	if c.LLM.MaxPromptLength > 0 {
		opts = append(opts, func(cfg *config) error {
			cfg.llmCfg.MaxPromptLength = c.LLM.MaxPromptLength
			return nil
		})
	}

	// Database (optional). Without a host, sdk falls back to in-memory storage.
	if c.Database.Host != "" {
		opts = append(opts, WithPostgres(c.Database))
	}

	// Embedding (optional). Without a service URL, sdk uses default embeddings.
	if c.Embedding.ServiceURL != "" {
		opts = append(opts, WithEmbeddingService(c.Embedding.ServiceURL, c.Embedding.Model))
	}

	// Memory. Each unset field falls back to the component default.
	if c.Memory.Enabled {
		opts = append(opts, WithMemoryConfig(c.Memory.MaxHistory, c.Memory.MaxSessions))
		if c.Memory.DistillationEnabled() {
			// DistillationThreshold 0 means "ungated": fire on every event,
			// matching every downstream component's contract. We pass it
			// straight through instead of substituting a default, so users
			// can express ungated behaviour explicitly via yaml.
			opts = append(opts, WithDistillation(c.Memory.DistillationThreshold))
		}
		if c.Memory.EnableRAG {
			opts = append(opts, WithRAG(c.Memory.RAGTopK, c.Memory.RAGMinScore))
		}
	} else {
		opts = append(opts, WithoutMemory())
	}

	// Knowledge (optional). Without chunk_size, sdk uses default retrieval.
	if c.Knowledge.ChunkSize > 0 {
		opts = append(opts, WithKnowledgeConfig(c.Knowledge))
	}

	// Knowledge quality gate (optional). Applied as a whole struct when the
	// trigger field MinFinalScore is set, so users who configure the gate own
	// all its values. A zero-value gate falls back to the package default at
	// apply time (see buildAKGBridge).
	if c.Knowledge.Quality.MinFinalScore > 0 {
		opts = append(opts, WithAKGQualityGate(knowledge.QualityGateConfig{
			MinExtraction:     c.Knowledge.Quality.MinExtraction,
			MinConsistency:    c.Knowledge.Quality.MinConsistency,
			MinFinalScore:     c.Knowledge.Quality.MinFinalScore,
			MaxFactsPerIngest: c.Knowledge.Quality.MaxFactsPerIngest,
			EnableDedup:       c.Knowledge.Quality.EnableDedup,
			DedupThreshold:    c.Knowledge.Quality.DedupThreshold,
		}))
	}

	// Knowledge embedding (optional). Applied when model is non-empty; the
	// model is required by WithAKGEmbedding, while baseURL may be empty (the
	// sdk falls back to the top-level embedding service endpoint).
	if c.Knowledge.Embedding.Model != "" {
		opts = append(opts, WithAKGEmbedding(
			c.Knowledge.Embedding.Model, c.Knowledge.Embedding.BaseURL))
	}

	// Evolution.
	if c.Evolution.Enabled {
		opts = append(opts, WithEvolution())
	}

	return opts, nil
}
