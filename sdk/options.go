package sdk

import (
	"errors"
	"fmt"
	"os"
	"time"

	tools "github.com/Timwood0x10/ares/internal/apitools"
	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/provider"
	llmcore "github.com/Timwood0x10/ares/internal/llmcore"
	"github.com/Timwood0x10/ares/internal/tools/toolsource"
)

// ---- Config options ----

// ConfigOption configures the Runtime during construction using a YAML file.
// Loads ares.yaml from the given path and converts it to internal options.
// It is an alias of Option so WithConfig/WithConfigFromEnv can be passed
// directly to New/NewRuntime.
type ConfigOption = Option

// WithConfig loads configuration from a YAML file, parses and validates it,
// then converts it to internal options and applies them.
//
// Args:
//
//	path - filesystem path to the YAML file (ares.yaml by default)
//
// Returns:
//
//	A Runtime option that applies the loaded configuration.
func WithConfig(path string) ConfigOption {
	return func(c *config) error {
		return applyConfigFile(c, path)
	}
}

// WithConfigFromEnv loads configuration from a YAML file, allowing override
// via the ARES_YAML environment variable. If ARES_YAML is set, it will be
// used as the config path. Otherwise, it falls back to ./ares.yaml.
func WithConfigFromEnv() ConfigOption {
	return func(c *config) error {
		path := "./ares.yaml"
		if p := os.Getenv("ARES_YAML"); p != "" {
			path = p
		}
		return applyConfigFile(c, path)
	}
}

// applyConfigFile is the shared implementation behind WithConfig and
// WithConfigFromEnv: it loads the YAML file at path, converts it to internal
// options, and applies them in order. Keeping the load→convert→apply loop in
// one place guarantees every config entry point behaves identically.
func applyConfigFile(c *config, path string) error {
	sdkCfg, err := LoadConfigFile(path)
	if err != nil {
		return fmt.Errorf("load config %s: %w", path, err)
	}
	opts, err := sdkCfg.ToOptions()
	if err != nil {
		return fmt.Errorf("config to options: %w", err)
	}
	for _, opt := range opts {
		if err := opt(c); err != nil {
			return err
		}
	}
	return nil
}

// ---- Runtime options ----

// Option configures the Runtime during construction.
type Option func(*config) error

// config holds the internal configuration state while options are applied.
type config struct {
	llmCfg      *llmcore.LLMConfig
	baseCfg     *llmcore.BaseConfig
	memCfg      memoryCfg
	evoCfg      evolutionCfg
	knlCfg      knowledgeCfg
	dbCfg       databaseCfg     // optional PostgreSQL connection
	embedCfg    embeddingCfg    // optional external embedding service
	distillCfg  distillationCfg // optional memory distillation
	knowledgeRT knowledgeRTCfg  // optional retrieval tuning
	// extraProviders holds user-registered GraphProviders appended via
	// WithKnowledgeProvider (e.g. code, mysql, postgres providers).
	extraProviders []provider.GraphProvider
	// sqliteStorePath, when non-empty, selects the SQLite knowledge store
	// instead of the default in-memory store.
	sqliteStorePath string
	mcpConns        []MCPConn
	fallbacks       []*llmcore.LLMConfig
	trace           bool
}

// memoryCfg holds memory subsystem configuration.
type memoryCfg struct {
	Enabled     bool
	MaxHistory  int // 0 → component default
	MaxSessions int // 0 → component default
	// EnableRAG enables retrieval-augmented generation; RAGTopK and RAGMinScore
	// tune retrieval when EnableRAG is true.
	EnableRAG   bool
	RAGTopK     int
	RAGMinScore float64
}

type evolutionCfg struct {
	Enabled bool
}

type knowledgeCfg struct {
	Enabled bool
	// QualityGate configures the AKG fact quality gate. Zero value falls back
	// to knowledge.DefaultQualityGateConfig at apply time.
	QualityGate knowledge.QualityGateConfig
	// EmbeddingModel selects the embedding model used to vectorize distilled
	// facts and retrieval queries. Empty falls back to the embedding service
	// default.
	EmbeddingModel string
	// EmbeddingBaseURL is the embedding service endpoint used by the AKG
	// distillation pipeline and StoreProvider. Empty falls back to the
	// embedding service configured via WithEmbeddingService.
	EmbeddingBaseURL string
}

// databaseCfg holds PostgreSQL connection parameters. Empty host signals
// in-memory storage fallback.
type databaseCfg struct {
	Host     string
	Port     int
	User     string
	Password string
	Database string
	SSLMode  string
}

// embeddingCfg holds an external embedding service endpoint. Empty URL signals
// default embedding fallback.
type embeddingCfg struct {
	ServiceURL string
	Model      string
}

// distillationCfg holds memory distillation knobs. Zero threshold signals
// component default; enabled=false disables the distiller.
type distillationCfg struct {
	Enabled   bool
	Threshold int
}

// knowledgeRTCfg tunes retrieval chunking and similarity bounds. Zero values
// signal component defaults.
type knowledgeRTCfg struct {
	ChunkSize    int
	ChunkOverlap int
	TopK         int
	MinScore     float64
}

func defaultConfig() *config {
	return &config{
		llmCfg: &llmcore.LLMConfig{
			Provider:    llmcore.LLMProviderOllama,
			Model:       defaultModel,
			Temperature: 0.7,
			MaxTokens:   2048,
			Timeout:     60,
		},
		baseCfg: &llmcore.BaseConfig{
			RequestTimeout: 60,
			MaxRetries:     3,
		},
		// memCfg.Enabled defaults to true so users no longer need
		// WithDefaultMemory() for the quickstart path. wireMemory falls back
		// to compression-only memory when no embedding service is available
		// (see sdk/memory_wiring.go), so this is safe without an embedding
		// service. Use WithoutMemory() to opt out.
		memCfg: memoryCfg{Enabled: true},
		evoCfg: evolutionCfg{Enabled: false},
		trace:  true,
		// dbCfg, embedCfg, distillCfg, knowledgeRT default to zero values,
		// signalling component defaults downstream.
	}
}

// WithOpenAI configures the OpenAI provider.
// model: model name, e.g. "gpt-4o-mini" or "gpt-4o". Reads OPENAI_API_KEY
// from the environment when apiKey is empty.
// Default base URL is https://api.openai.com/v1.
func WithOpenAI(model string) Option {
	return func(c *config) error {
		c.llmCfg.Provider = llmcore.LLMProviderOpenAI
		c.llmCfg.Model = model
		if c.llmCfg.BaseURL == "" {
			c.llmCfg.BaseURL = "https://api.openai.com/v1"
		}
		return nil
	}
}

// WithOllama configures the Ollama provider.
// model: model name, e.g. "llama3.2" or "qwen2.5". Ollama typically does not
// require an API key.
func WithOllama(model string) Option {
	return func(c *config) error {
		c.llmCfg.Provider = llmcore.LLMProviderOllama
		c.llmCfg.Model = model
		return nil
	}
}

// WithAnthropic configures the Anthropic provider.
// model: model name, e.g. "claude-3-haiku" or "claude-3-opus". Reads
// ANTHROPIC_API_KEY from the environment when apiKey is empty.
// Default base URL is https://api.anthropic.com/v1.
func WithAnthropic(model string) Option {
	return func(c *config) error {
		c.llmCfg.Provider = llmcore.LLMProviderAnthropic
		c.llmCfg.Model = model
		if c.llmCfg.BaseURL == "" {
			c.llmCfg.BaseURL = "https://api.anthropic.com/v1"
		}
		return nil
	}
}

// WithOpenRouter configures the OpenRouter provider.
// model: model name, e.g. "openai/gpt-4o-mini". Reads OPENROUTER_API_KEY
// from the environment when apiKey is empty.
// Default base URL is https://openrouter.ai/api/v1.
func WithOpenRouter(model string) Option {
	return func(c *config) error {
		c.llmCfg.Provider = llmcore.LLMProviderOpenRouter
		c.llmCfg.Model = model
		if c.llmCfg.BaseURL == "" {
			c.llmCfg.BaseURL = "https://openrouter.ai/api/v1"
		}
		return nil
	}
}

// WithBaseURL overrides the default API base URL for the provider.
func WithBaseURL(url string) Option {
	return func(c *config) error {
		c.llmCfg.BaseURL = url
		return nil
	}
}

// WithAPIKey sets the API key explicitly (instead of reading from the
// environment variable).
func WithAPIKey(key string) Option {
	return func(c *config) error {
		c.llmCfg.APIKey = key
		return nil
	}
}

// WithLLMConfig applies a full llmcore.LLMConfig. Useful when you already have a
// configuration object from a YAML file or shared config store.
func WithLLMConfig(cfg *llmcore.LLMConfig) Option {
	return func(c *config) error {
		if cfg == nil {
			return errors.New("with llm config: config is nil")
		}
		c.llmCfg = cfg
		return nil
	}
}

// WithFallbackLLM adds a fallback LLM provider for automatic failover.
// When the primary provider fails (timeout, rate limit, network error),
// the Runtime automatically tries fallbacks in order. Call multiple times
// to add multiple fallbacks.
func WithFallbackLLM(cfg *llmcore.LLMConfig) Option {
	return func(c *config) error {
		c.fallbacks = append(c.fallbacks, cfg)
		return nil
	}
}

// WithDefaultMemory enables in-memory session storage. Each Run call creates a
// session and conversation history is available to the LLM on subsequent calls.
//
// As of the defaultConfig flip, memory is enabled by default, so this option is
// now a no-op kept for backward compatibility. Use it to make the intent
// explicit in code that relies on default memory.
func WithDefaultMemory() Option {
	return func(c *config) error {
		c.memCfg.Enabled = true
		return nil
	}
}

// WithoutMemory disables the memory subsystem, overriding the
// defaultConfig-enabled memory. Use this when a Runtime should not maintain
// session history (e.g. stateless CLI tools or tests that assert the
// memory-off path).
func WithoutMemory() Option {
	return func(c *config) error {
		c.memCfg.Enabled = false
		return nil
	}
}

// WithMemoryConfig overrides default memory sizing. Fields left at zero fall
// back to the component default, mirroring the yaml-driven philosophy.
//
// Args:
//
//	maxHistory - max conversation turns retained per session; 0 → default.
//	maxSessions - max concurrent sessions tracked; 0 → default.
func WithMemoryConfig(maxHistory, maxSessions int) Option {
	return func(c *config) error {
		if maxHistory < 0 || maxSessions < 0 {
			return fmt.Errorf("memory config: %w", ErrInvalidRange)
		}
		c.memCfg.Enabled = true
		c.memCfg.MaxHistory = maxHistory
		c.memCfg.MaxSessions = maxSessions
		return nil
	}
}

// WithDistillation enables memory distillation. The threshold controls how
// many conversation rounds accumulate before distillation fires. A threshold
// of 0 falls back to the component default. Mirrors v0.2.4
// examples/knowledge-base config.yaml distillation_threshold semantics.
//
// Args:
//
//	threshold - conversation rounds between distillation triggers; 0 → default.
func WithDistillation(threshold int) Option {
	return func(c *config) error {
		if threshold < 0 {
			return fmt.Errorf("distillation threshold %d: %w", threshold, ErrInvalidRange)
		}
		c.distillCfg.Enabled = true
		c.distillCfg.Threshold = threshold
		return nil
	}
}

// WithRAG enables retrieval-augmented generation. Past experiences and distilled
// memories are retrieved and injected into the LLM prompt.
//
// Args:
//
//	topK     - max retrieved snippets to inject; must be >= 1.
//	minScore - minimum similarity score in [0, 1]; snippets below are filtered.
//
// Returns:
//
//	An Option that arms the memory RAG subsystem. Returns an error wrapping
//	ErrInvalidRange when topK < 1 or minScore is outside [0, 1].
func WithRAG(topK int, minScore float64) Option {
	return func(c *config) error {
		if topK < 1 {
			return fmt.Errorf("rag top_k %d: %w", topK, ErrInvalidRange)
		}
		if minScore < 0 || minScore > 1 {
			return fmt.Errorf("rag min_score %v: %w", minScore, ErrInvalidRange)
		}
		c.memCfg.Enabled = true
		c.memCfg.EnableRAG = true
		c.memCfg.RAGTopK = topK
		c.memCfg.RAGMinScore = minScore
		return nil
	}
}

// WithEmbeddingService injects an external embedding service endpoint. Empty
// url signals the sdk to fall back to default embedding behaviour.
//
// Args:
//
//	url   - embedding service URL, required when this option is used.
//	model - embedding model name, required when this option is used.
func WithEmbeddingService(url, model string) Option {
	return func(c *config) error {
		if url == "" {
			return fmt.Errorf("embedding service: %w", ErrMissingValue)
		}
		if model == "" {
			return fmt.Errorf("embedding model: %w", ErrMissingValue)
		}
		c.embedCfg.ServiceURL = url
		c.embedCfg.Model = model
		return nil
	}
}

// WithPostgres enables PostgreSQL-backed memory. Empty host signals in-memory
// storage fallback; when host is set, the sdk wires a pool to the Runtime.
//
// Args:
//
//	cfg - database connection parameters; host is the trigger field.
func WithPostgres(cfg DatabaseFileConfig) Option {
	return func(c *config) error {
		if cfg.Host == "" {
			return fmt.Errorf("postgres host: %w", ErrMissingValue)
		}
		if cfg.Port < 1 || cfg.Port > 65535 {
			return fmt.Errorf("postgres port %d: %w", cfg.Port, ErrInvalidRange)
		}
		c.dbCfg = databaseCfg(cfg)
		return nil
	}
}

// WithKnowledgeConfig tunes retrieval chunking and similarity bounds. Zero
// fields fall back to component defaults.
//
// Args:
//
//	cfg - knowledge retrieval parameters; chunk_size > 0 signals the section is active.
func WithKnowledgeConfig(cfg KnowledgeFileConfig) Option {
	return func(c *config) error {
		if cfg.ChunkSize > 0 {
			if cfg.ChunkOverlap < 0 || cfg.ChunkOverlap >= cfg.ChunkSize {
				return fmt.Errorf("knowledge chunk_overlap %d vs chunk_size %d: %w",
					cfg.ChunkOverlap, cfg.ChunkSize, ErrInvalidRange)
			}
			if cfg.TopK < 1 {
				return fmt.Errorf("knowledge top_k %d: %w", cfg.TopK, ErrInvalidRange)
			}
			if cfg.MinScore < 0 || cfg.MinScore > 1 {
				return fmt.Errorf("knowledge min_score %v: %w", cfg.MinScore, ErrInvalidRange)
			}
		}
		c.knowledgeRT = knowledgeRTCfg{
			ChunkSize:    cfg.ChunkSize,
			ChunkOverlap: cfg.ChunkOverlap,
			TopK:         cfg.TopK,
			MinScore:     cfg.MinScore,
		}
		return nil
	}
}

// WithEvolution enables strategy evolution. When enabled, the Runtime tracks
// agent performance and can evolve instructions to improve results over time.
func WithEvolution() Option {
	return func(c *config) error {
		c.evoCfg.Enabled = true
		return nil
	}
}

// WithKnowledge enables the AKF Knowledge Fabric pipeline.
// When enabled, each Agent.Run call automatically builds a knowledge graph
// from registered providers (e.g. Memory) and injects relevant context
// into the LLM's system prompt.
//
// If WithDefaultMemory is also enabled, historical tasks are automatically
// registered as a knowledge source.
func WithKnowledge() Option {
	return func(c *config) error {
		c.knlCfg.Enabled = true
		return nil
	}
}

// WithAKGQualityGate configures the AKG fact quality gate. The gate controls
// fact promotion: distilled candidates whose computed Confidence clears
// gate.MinFinalScore are promoted to active and become retrievable by the
// StoreProvider. A zero-value QualityGateConfig falls back to
// knowledge.DefaultQualityGateConfig at apply time.
//
// Args:
//
//	q - AKG quality gate configuration; MinFinalScore, DedupThreshold and
//	     MaxFactsPerIngest drive promotion and dedup behaviour.
func WithAKGQualityGate(q knowledge.QualityGateConfig) Option {
	return func(c *config) error {
		c.knlCfg.QualityGate = q
		return nil
	}
}

// WithAKGEmbedding configures the embedding model and endpoint used by the
// AKG distillation pipeline to vectorize distilled facts and by the
// StoreProvider to embed retrieval queries. When WithKnowledge is enabled this
// arms both the write side (DistillBridge) and the read side (StoreProvider)
// with vector recall.
//
// Args:
//
//	model   - embedding model name (e.g. "intfloat/e5-large-v2"); required.
//	baseURL - embedding service endpoint; empty defers to the service
//	          configured via WithEmbeddingService.
func WithAKGEmbedding(model, baseURL string) Option {
	return func(c *config) error {
		if model == "" {
			return fmt.Errorf("akg embedding model: %w", ErrMissingValue)
		}
		c.knlCfg.EmbeddingModel = model
		c.knlCfg.EmbeddingBaseURL = baseURL
		// Wire baseURL into embedCfg so buildEmbeddingClient actually
		// uses it. Previously it was only stored in knlCfg.EmbeddingBaseURL
		// which no reader consumed (dead parameter).
		if baseURL != "" && c.embedCfg.ServiceURL == "" {
			c.embedCfg.ServiceURL = baseURL
		}
		if c.embedCfg.Model == "" {
			c.embedCfg.Model = model
		}
		return nil
	}
}

// WithKnowledgeProvider registers an additional GraphProvider with the AKF
// Knowledge Fabric. Call multiple times to register multiple providers (e.g.
// code, mysql, postgres). Providers are only wired into the runtime when
// WithKnowledge is also enabled.
//
// Args:
//
//	p - a GraphProvider implementation; must not be nil.
//
// Returns:
//
//	An Option that appends p to the extra provider list. Returns an error
//	wrapping ErrNilProvider when p is nil.
func WithKnowledgeProvider(p provider.GraphProvider) Option {
	return func(c *config) error {
		if p == nil {
			return fmt.Errorf("knowledge provider: %w", ErrNilProvider)
		}
		c.extraProviders = append(c.extraProviders, p)
		return nil
	}
}

// WithSQLiteKnowledgeStore selects a file-backed SQLite knowledge store instead
// of the default in-memory store. Only takes effect when WithKnowledge is also
// enabled. When the SQLite path is set it takes priority over the PostgreSQL
// store configured via WithPostgres.
//
// Args:
//
//	dbPath - filesystem path to the SQLite database file; must be non-empty.
//
// Returns:
//
//	An Option that records the SQLite path. Returns an error wrapping
//	ErrMissingValue when dbPath is empty.
func WithSQLiteKnowledgeStore(dbPath string) Option {
	return func(c *config) error {
		if dbPath == "" {
			return fmt.Errorf("sqlite knowledge store path: %w", ErrMissingValue)
		}
		c.sqliteStorePath = dbPath
		return nil
	}
}

// MCPConn configures an MCP server connection.
type MCPConn struct {
	// Name is a human-readable label for this MCP server.
	Name string
	// Command is the absolute path to the MCP server binary.
	Command string
	// Args are command-line arguments passed to the server.
	Args []string
}

// WithMCP connects to an MCP server and registers its tools with the Runtime.
// Call multiple times to connect to multiple servers.
func WithMCP(conn MCPConn) Option {
	return func(c *config) error {
		if conn.Name == "" {
			conn.Name = "mcp"
		}
		if conn.Command == "" {
			return errors.New("mcp: command is required")
		}
		c.mcpConns = append(c.mcpConns, conn)
		return nil
	}
}

// WithTrace toggles per-step trace logging. Enabled by default.
func WithTrace(isEnabled bool) Option {
	return func(c *config) error {
		c.trace = isEnabled
		return nil
	}
}

// ---- Agent options ----

// AgentOption configures an Agent during construction.
type AgentOption func(*agentConfig)

type agentConfig struct {
	instruction string
	tools       []tools.Tool
	humanInput  HumanInputFunc
	maxIter     int
	// maxTokens caps the cumulative prompt+completion tokens across all LLM
	// calls in one run (<=0 = unbounded). Passed through to agentloop.Request.
	maxTokens int
	// timeout caps the total wall-clock duration of one run (<=0 = no limit).
	// Passed through to agentloop.Request.
	timeout time.Duration
	// discovery enables runtime tool discovery: when true the agent exposes a
	// discover_tools meta-tool so the LLM can search the tool pool at runtime
	// and expand its active tool set on demand. Default is off (backward
	// compatible: identical to the legacy WithTools-only path).
	discovery bool
	// toolSource, when non-nil, is the ToolSource used for discovery. When nil
	// and discovery is on, the SDK defaults to a RegistrySource over the
	// Runtime's tool registry.
	toolSource toolsource.ToolSource
	// selector, when non-nil, narrows the available tool pool before each run.
	// When nil and discovery is on, the SDK defaults to AllSelector (expose all
	// available tools — zero behaviour change vs. legacy).
	selector toolsource.ToolSelector
}

func defaultAgentConfig() *agentConfig {
	return &agentConfig{
		instruction: "",
		maxIter:     defaultMaxIterations,
	}
}

// WithInstruction sets the system-level instruction (system prompt) for the
// agent. This is always prepended to the conversation.
func WithInstruction(instruction string) AgentOption {
	return func(c *agentConfig) {
		c.instruction = instruction
	}
}

// WithTools attaches tools to the agent. The agent will expose these tools to
// the LLM as function-calling primitives.
func WithTools(tt ...tools.Tool) AgentOption {
	return func(c *agentConfig) {
		c.tools = append(c.tools, tt...)
	}
}

// WithHumanInput attaches a human-in-the-loop approval function. Before each
// tool call, the function is invoked so a human can approve or reject it.
// Return true to approve, false to skip the tool call.
func WithHumanInput(fn HumanInputFunc) AgentOption {
	return func(c *agentConfig) {
		c.humanInput = fn
	}
}

// WithMaxIterations caps the number of ReAct (tool-calling) iterations the
// agent will run before returning a "max iterations reached" result. Values
// <= 0 fall back to the default (defaultMaxIterations).
func WithMaxIterations(n int) AgentOption {
	return func(c *agentConfig) {
		if n > 0 {
			c.maxIter = n
		}
	}
}

// WithMaxTokens caps the cumulative prompt+completion tokens across all LLM
// calls in one agent run. When the budget is exceeded the run stops early and
// returns "max tokens reached" instead of burning more iterations (primitive
// 4: bounded autonomous execution). Values <= 0 mean unbounded (default).
func WithMaxTokens(n int) AgentOption {
	return func(c *agentConfig) {
		if n > 0 {
			c.maxTokens = n
		}
	}
}

// WithTimeout caps the total wall-clock duration of one agent run. When the
// deadline passes between LLM calls the run stops and returns "timeout
// reached" (primitive 4). Values <= 0 mean no time budget (default).
func WithTimeout(d time.Duration) AgentOption {
	return func(c *agentConfig) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// WithToolDiscovery enables runtime tool discovery. When enabled, the agent
// exposes a discover_tools meta-tool so the LLM can search the available tool
// pool at runtime by name/description/tag and expand its active tool set on
// demand. Tools discovered at runtime are expanded via the agentloop engine's
// ToolExpander path (no second execution loop).
//
// Default is off: behaviour is byte-for-byte identical to the legacy
// WithTools-only path (no meta-tool, no expander, Engine.Tools = registry).
func WithToolDiscovery() AgentOption {
	return func(c *agentConfig) {
		c.discovery = true
	}
}

// WithToolSource sets the ToolSource used to discover available tools for
// each run. Setting a source also implies discovery on (equivalent to also
// calling WithToolDiscovery). When discovery is on and no source is set, the
// SDK defaults to a RegistrySource over the Runtime's tool registry.
func WithToolSource(s toolsource.ToolSource) AgentOption {
	return func(c *agentConfig) {
		c.toolSource = s
		c.discovery = true
	}
}

// WithToolSelector sets the ToolSelector used to narrow the available tool
// pool before each run. Setting a selector also implies discovery on
// (equivalent to also calling WithToolDiscovery). When discovery is on and no
// selector is set, the SDK defaults to AllSelector (expose all available
// tools — zero behaviour change vs. legacy).
func WithToolSelector(s toolsource.ToolSelector) AgentOption {
	return func(c *agentConfig) {
		c.selector = s
		c.discovery = true
	}
}
