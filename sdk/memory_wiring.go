// Package sdk wiring helpers for the production MemoryManager.
//
// This file closes the compression + RAG + distillation loop inside the SDK
// Runtime. It extracts the memory-construction logic out of New() so the
// constructor stays under the 100-line limit, and mirrors the reference
// wiring in internal/ares_bootstrap (retriever_wiring.go +
// provide_distillation.go) without taking a build-time dependency on that
// internal bootstrap package.
//
// The storage/knowledge adapters (experience searcher, distillation repo,
// knowledge retriever adapter) live in
// internal/ares_memory/experienceadapters and are shared with
// internal/ares_bootstrap, so the field mapping has a single source of truth.
package sdk

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	apiembed "github.com/Timwood0x10/ares/internal/embedding"
	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/adapter"
	khruntime "github.com/Timwood0x10/ares/internal/knowledge/runtime"
	"github.com/Timwood0x10/ares/internal/llm"
	memory "github.com/Timwood0x10/ares/internal/runtime/memory"
	memctx "github.com/Timwood0x10/ares/internal/runtime/memory/context"
	"github.com/Timwood0x10/ares/internal/runtime/memory/distillation"
	memembed "github.com/Timwood0x10/ares/internal/runtime/memory/embedding"
	aresexp "github.com/Timwood0x10/ares/internal/runtime/memory/experience"
	"github.com/Timwood0x10/ares/internal/runtime/memory/experienceadapters"
	"github.com/Timwood0x10/ares/internal/storage/postgres"
	pgembedding "github.com/Timwood0x10/ares/internal/storage/postgres/embedding"
	"github.com/Timwood0x10/ares/internal/storage/postgres/repositories"
)

// ErrDistillDepsMissing signals that distillation dependencies (embedding
// service URL or database host) were not configured. It is wrapped by
// wireDistillationDeps so wireMemory can distinguish "missing config" from
// "construction failed" and fall back to a non-distilling MemoryManager
// without failing the whole Runtime.
var ErrDistillDepsMissing = errors.New("distillation dependencies unavailable")

// defaultEmbeddingTimeout is the HTTP timeout used when the SDK builds an
// embedding client from cfg.embedCfg (which carries no explicit timeout).
const defaultEmbeddingTimeout = 30 * time.Second

// retrieverSetter is the minimal interface for injecting ContextRetrievers
// into a MemoryManager. Both *memory.memoryManager and
// *memory.ProductionMemoryManager satisfy it, but the public MemoryManager
// interface does not expose SetRetrievers (retrieval is an optional
// capability), so we type-assert at wiring time instead of widening the
// interface. Mirrors internal/ares_bootstrap.retrieverSetter.
type retrieverSetter interface {
	SetRetrievers(retrievers []memctx.ContextRetriever)
}

// memoryWiring bundles the outputs of wireMemory so New() can unpack a
// single struct instead of juggling five return values. embClient and
// expRepo are nil when distillation is disabled or its deps are missing;
// wireSDKRetrievers handles that gracefully. distillSvc is the standalone
// DistillationService consumed by the event-driven distillation subscriber;
// it is nil when distillation is disabled, deps are missing, or the service
// could not be constructed (non-fatal: the memory manager still works).
// akgDistiller is a separate distiller built for the AKG DistillBridge
// (conversation → KnowledgeObject pipeline). It is nil when embClient or
// expRepo is unavailable, since the distiller requires both.
type memoryWiring struct {
	mgr          memory.MemoryManager
	embClient    apiembed.EmbeddingService
	expRepo      repositories.ExperienceRepositoryInterface
	cleanup      func()
	distillSvc   *aresexp.DistillationService
	akgDistiller adapter.ConversationDistiller
}

// wireMemory constructs the production MemoryManager (compression + RAG +
// distillation) from the SDK config. When distillation is enabled and its
// dependencies are available, it returns a manager backed by
// NewMemoryManagerWithDistiller; otherwise it falls back to the
// compression-only NewMemoryManager. The returned cleanup closes the
// postgres pool when distillation deps were constructed (nil otherwise).
//
// Args:
//
//	ctx  - construction context, used for postgres pool init.
//	cfg  - fully applied SDK config; memCfg/distillCfg/embedCfg/dbCfg are read.
//
// Returns:
//
//	*memoryWiring - mgr is always non-nil on success; embClient/expRepo may be nil.
//	error         - wrapped error if the memory manager itself cannot be constructed.
func wireMemory(ctx context.Context, cfg *config) (*memoryWiring, error) {
	memCfg := buildMemoryConfig(cfg.memCfg)

	if !cfg.distillCfg.Enabled {
		mgr, err := memory.NewMemoryManager(memCfg)
		if err != nil {
			return nil, fmt.Errorf("wire memory: %w", err)
		}
		return &memoryWiring{mgr: mgr}, nil
	}

	embClient, expRepo, cleanup, err := wireDistillationDeps(ctx, cfg)
	if err != nil {
		if errors.Is(err, ErrDistillDepsMissing) {
			slog.Warn("sdk: distillation deps missing, falling back to compression-only memory",
				"error", err)
		} else {
			slog.Warn("sdk: distillation deps construction failed, falling back to compression-only memory",
				"error", err)
		}
		mgr, fallbackErr := memory.NewMemoryManager(memCfg)
		if fallbackErr != nil {
			return nil, fmt.Errorf("wire memory fallback: %w", fallbackErr)
		}
		return &memoryWiring{mgr: mgr}, nil
	}

	mgr, err := memory.NewMemoryManagerWithDistiller(memCfg, embClient, experienceadapters.NewDistillationRepo(expRepo, experienceadapters.DefaultTenant))
	if err != nil {
		if cleanup != nil {
			cleanup()
		}
		return nil, fmt.Errorf("wire memory distiller: %w", err)
	}

	// Build the standalone DistillationService consumed by the event-driven
	// distillation subscriber. Non-fatal: when construction fails the memory
	// manager still works; only event-driven distillation is disabled.
	distillSvc, derr := buildDistillationService(cfg, embClient, expRepo)
	if derr != nil {
		slog.Warn("sdk: distillation service construction failed; event-driven distillation disabled",
			"error", derr)
	}

	// Build a separate distiller for the AKG DistillBridge. The memory
	// manager's internal distiller is not exposed, so a dedicated instance is
	// constructed from the same embedding client and experience repo. Non-fatal:
	// when construction fails the AKG bridge is simply not wired. The deps-nil
	// guard lives here (not in buildAKGDistiller) so that function never returns
	// the ambiguous (nil, nil) — see the nilnil linter rule.
	var akgDistiller adapter.ConversationDistiller
	if embClient != nil && expRepo != nil {
		d, aerr := buildAKGDistiller(embClient, expRepo)
		if aerr != nil {
			slog.Warn("sdk: AKG distiller construction failed; AKG distillation disabled",
				"error", aerr)
		} else {
			akgDistiller = d
		}
	}

	return &memoryWiring{
		mgr:          mgr,
		embClient:    embClient,
		expRepo:      expRepo,
		cleanup:      cleanup,
		distillSvc:   distillSvc,
		akgDistiller: akgDistiller,
	}, nil
}

// buildMemoryConfig translates the SDK memoryCfg into a production
// memory.MemoryConfig. It starts from DefaultMemoryConfig so all
// storage/TTL/vector defaults are preserved, then overrides the user-facing
// knobs. Zero values in memoryCfg mean "use default" — they do NOT clobber
// the defaults.
func buildMemoryConfig(cfg memoryCfg) *memory.MemoryConfig {
	mc := memory.DefaultMemoryConfig()
	mc.Enabled = true
	if cfg.MaxHistory > 0 {
		mc.MaxHistory = cfg.MaxHistory
	}
	if cfg.MaxSessions > 0 {
		mc.MaxSessions = cfg.MaxSessions
	}
	mc.EnableRAG = cfg.EnableRAG
	if cfg.RAGTopK > 0 {
		mc.RAGTopK = cfg.RAGTopK
	}
	if cfg.RAGMinScore > 0 {
		mc.RAGMinScore = cfg.RAGMinScore
	}
	return mc
}

// wireDistillationDeps constructs the embedding client and postgres-backed
// experience repository required by NewMemoryManagerWithDistiller. Both are
// optional SDK features (gated by WithEmbeddingService + WithPostgres +
// WithDistillation), so a missing config yields ErrDistillDepsMissing
// rather than a hard failure.
//
// The embedding client is returned as the concrete *pgembedding.EmbeddingClient
// (which satisfies apiembed.EmbeddingService) so it can be reused by
// buildDistillationService, whose NewDistillationService target requires the
// concrete type.
//
// Args:
//
//	ctx  - construction context, used for postgres pool init (ping).
//	cfg  - fully applied SDK config; embedCfg and dbCfg are read.
//
// Returns:
//
//	embClient - concrete embedding client; satisfies apiembed.EmbeddingService; nil only on error.
//	expRepo   - postgres experience repository; nil only on error.
//	cleanup   - closes the postgres pool; safe to call when non-nil. Nil on error.
//	err       - wrapped ErrDistillDepsMissing when config is incomplete, or a
//	            construction error otherwise.
func wireDistillationDeps(ctx context.Context, cfg *config) (*pgembedding.EmbeddingClient, repositories.ExperienceRepositoryInterface, func(), error) {
	if cfg.embedCfg.ServiceURL == "" || cfg.dbCfg.Host == "" {
		return nil, nil, nil, fmt.Errorf("distillation deps: %w", ErrDistillDepsMissing)
	}

	embClient := buildEmbeddingClient(cfg.embedCfg)
	pool, err := buildPostgresPool(ctx, cfg.dbCfg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("distillation deps postgres pool: %w", err)
	}

	expRepo := repositories.NewExperienceRepository(pool.GetDB())
	cleanup := func() {
		if cerr := pool.Close(); cerr != nil {
			slog.Warn("sdk: distillation postgres pool close failed", "error", cerr)
		}
	}
	return embClient, expRepo, cleanup, nil
}

// buildEmbeddingClient constructs the postgres embedding client from the
// SDK embeddingCfg. The client satisfies apiembed.EmbeddingService. Redis
// caching is not wired (nil), matching the bootstrap default.
func buildEmbeddingClient(cfg embeddingCfg) *pgembedding.EmbeddingClient {
	return pgembedding.NewEmbeddingClient(cfg.ServiceURL, cfg.Model, nil, defaultEmbeddingTimeout)
}

// buildLLMClient constructs a standalone internal *llm.Client from the SDK
// config. The DistillationService requires a *llm.Client (not the public
// *llm.Service used by the agent loop), so this mirrors the internal-config
// conversion done by llmservice.NewService: the public core.LLMConfig is
// mapped field-by-field into the internal llm.Config. Fallbacks are not
// applied here because distillation is a best-effort background path that
// does not warrant a failover client.
//
// Args:
//
//	cfg - fully applied SDK config; llmCfg is read. A nil llmCfg yields an error.
//
// Returns:
//
//	*llm.Client - configured LLM client ready for DistillationService.Distill.
//	error       - wrapped error if llmCfg is nil or llm.NewClient fails.
func buildLLMClient(cfg *config) (*llm.Client, error) {
	if cfg == nil || cfg.llmCfg == nil {
		return nil, fmt.Errorf("build llm client: %w", ErrDistillDepsMissing)
	}
	internalCfg := &llm.Config{
		Provider:        string(cfg.llmCfg.Provider),
		APIKey:          cfg.llmCfg.APIKey,
		BaseURL:         cfg.llmCfg.BaseURL,
		Model:           cfg.llmCfg.Model,
		Timeout:         cfg.llmCfg.Timeout,
		MaxTokens:       cfg.llmCfg.MaxTokens,
		MaxPromptLength: cfg.llmCfg.MaxPromptLength,
	}
	client, err := llm.NewClient(internalCfg)
	if err != nil {
		return nil, fmt.Errorf("build llm client: %w", err)
	}
	return client, nil
}

// buildDistillationService constructs the standalone DistillationService
// consumed by the event-driven distillation subscriber. It builds a
// dedicated *llm.Client (independent of the agent loop's *llm.Service) and
// reuses the same embedding client and experience repo already wired for
// the memory manager's distiller, so distilled experiences land in the same
// store the RAG retriever reads from.
//
// Args:
//
//	cfg       - fully applied SDK config; llmCfg is read by buildLLMClient.
//	embClient - embedding client shared with the memory distiller; must be non-nil.
//	expRepo   - postgres experience repo shared with the memory distiller; must be non-nil.
//
// Returns:
//
//	*aresexp.DistillationService - ready to consume TaskCompleted/TaskFailed events.
//	error                       - wrapped ErrDistillDepsMissing when inputs are nil,
//	                             or the llm.NewClient error.
func buildDistillationService(
	cfg *config,
	embClient *pgembedding.EmbeddingClient,
	expRepo repositories.ExperienceRepositoryInterface,
) (*aresexp.DistillationService, error) {
	if embClient == nil || expRepo == nil {
		return nil, fmt.Errorf("build distillation service: %w", ErrDistillDepsMissing)
	}
	llmClient, err := buildLLMClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("build distillation service: %w", err)
	}
	return aresexp.NewDistillationService(llmClient, embClient, expRepo), nil
}

// buildAKGDistiller constructs a standalone distiller for the AKG DistillBridge.
// The memory manager builds its own internal distiller via
// NewMemoryManagerWithDistiller, but that distiller is not exposed on the
// MemoryManager interface, so a separate instance is constructed from the
// same embedding client and experience repo. The distillation config uses
// conservative defaults (DefaultDistillationConfig) and the embedding pipeline
// is wired so conflict detection uses the canonical spec builders.
//
// Precondition: both embClient and expRepo are non-nil. The caller (wireMemory)
// guards the deps-nil case so this function never has to return the ambiguous
// (nil, nil) — it always returns either a usable distiller or a real error.
//
// Args:
//
//	embClient - embedding service for vector generation; must be non-nil.
//	expRepo   - postgres experience repo; must be non-nil.
//
// Returns:
//
//	adapter.ConversationDistiller - ready to feed to NewDistillBridgeWithGate.
//	error                        - wrapped error if the embedding pipeline cannot be constructed.
func buildAKGDistiller(
	embClient apiembed.EmbeddingService,
	expRepo repositories.ExperienceRepositoryInterface,
) (adapter.ConversationDistiller, error) {
	distillRepo := experienceadapters.NewDistillationRepo(expRepo, experienceadapters.DefaultTenant)
	distiller := distillation.NewDistiller(distillation.DefaultDistillationConfig(), embClient, distillRepo)
	pipeline, err := memembed.NewEmbeddingPipeline(embClient)
	if err != nil {
		return nil, fmt.Errorf("akg distiller embedding pipeline: %w", err)
	}
	distiller.SetEmbeddingPipeline(pipeline)
	return distiller, nil
}

// buildPostgresPool opens and pings a postgres connection pool from the SDK
// databaseCfg. The pool is returned ready for use; the caller owns Close.
func buildPostgresPool(ctx context.Context, cfg databaseCfg) (*postgres.Pool, error) {
	sslMode := cfg.SSLMode
	if sslMode == "" {
		sslMode = sslModeDisable
	}
	pgCfg := &postgres.Config{
		Host:     cfg.Host,
		Port:     cfg.Port,
		User:     cfg.User,
		Password: cfg.Password,
		Database: cfg.Database,
		SSLMode:  sslMode,
	}
	pool, err := postgres.NewPool(pgCfg)
	if err != nil {
		return nil, fmt.Errorf("open postgres pool: %w", err)
	}
	_ = ctx // postgres.NewPool pings internally with context.Background();
	// ctx reserved for future use (e.g. ping with caller deadline).
	return pool, nil
}

// wireSDKRetrievers constructs the MemoryRetriever and KnowledgeRetriever
// from the wired production dependencies and injects them into the
// MemoryManager via SetRetrievers. Best-effort and non-fatal: if a
// dependency is missing (nil embedding client, nil experience repo, nil
// knowledge runtime) the corresponding retriever is skipped and a warning
// is logged. Retrieval only fires at runtime when the MemoryManager's
// config.EnableRAG is true, so callers still control the feature via
// config regardless of whether retrievers are wired.
//
// When knowStore is non-nil the KnowledgeRetriever takes the AKG read loop:
// HybridSearch against the store's AKG-distilled facts instead of re-running
// provider streaming via runtime.Execute. This closes the 0.2.9 read loop
// (facts written by the DistillBridge are recalled here).
//
// Args:
//
//	ctx        - construction context, used for KnowledgeRetriever construction.
//	cfg        - fully applied SDK config; memCfg.RAGMinScore tunes memory retriever.
//	memMgr     - the MemoryManager; type-asserted to retrieverSetter.
//	embClient  - embedding client for query embedding. Nil skips memory retriever.
//	expRepo    - postgres experience repo. Nil skips memory retriever.
//	knowRt     - AKG KnowledgeRuntime. Nil skips knowledge retriever.
//	knowStore  - optional KnowledgeStore; when non-nil the retriever reads AKG facts via HybridSearch.
//	embModel   - embedding model name for HybridSearch; empty = lexical-only.
func wireSDKRetrievers(
	ctx context.Context,
	cfg *config,
	memMgr memory.MemoryManager,
	embClient apiembed.EmbeddingService,
	expRepo repositories.ExperienceRepositoryInterface,
	knowRt *khruntime.KnowledgeRuntime,
	knowStore knowledge.KnowledgeStore,
	embModel string,
) {
	setter, ok := memMgr.(retrieverSetter)
	if !ok {
		slog.Warn("sdk: memory manager does not expose SetRetrievers; RAG wiring skipped",
			"type", fmt.Sprintf("%T", memMgr))
		return
	}

	var retrievers []memctx.ContextRetriever

	if embClient != nil && expRepo != nil {
		mr, err := buildMemoryRetriever(embClient, expRepo, cfg.memCfg.RAGMinScore)
		if err != nil {
			slog.Warn("sdk: memory retriever construction failed; skipping", "error", err)
		} else {
			retrievers = append(retrievers, mr)
			slog.Info("sdk: memory retriever wired (distilled experiences -> RAG)")
		}
	}

	if knowRt != nil {
		minScore := cfg.knowledgeRT.MinScore
		var kr *adapter.KnowledgeRetriever
		var err error
		if knowStore != nil {
			kr, err = adapter.NewKnowledgeRetrieverWithStore(ctx, knowRt, knowStore, embModel, minScore)
		} else {
			kr, err = adapter.NewKnowledgeRetriever(ctx, knowRt, minScore)
		}
		if err != nil {
			slog.Warn("sdk: knowledge retriever construction failed; skipping", "error", err)
		} else {
			retrievers = append(retrievers, experienceadapters.NewKnowledgeRetrieverAdapter(kr))
			slog.Info("sdk: knowledge retriever wired (AKG -> RAG)", "min_score", minScore, "store_backed", knowStore != nil)
		}
	}

	if len(retrievers) == 0 {
		slog.Info("sdk: no RAG retrievers wired (memory/knowledge deps unavailable)")
		return
	}

	setter.SetRetrievers(retrievers)
	slog.Info("sdk: RAG retrievers injected into memory manager", "count", len(retrievers))
}

// buildMemoryRetriever constructs a MemoryRetriever from the embedding
// client and postgres experience repository. The embedding pipeline is
// built from the embedding client so query vectors match the prefix scheme
// used at write time. minScore falls back to memctx.DefaultMinScore when
// non-positive. Extracted to keep wireSDKRetrievers under 100 lines.
func buildMemoryRetriever(
	embClient apiembed.EmbeddingService,
	expRepo repositories.ExperienceRepositoryInterface,
	minScore float64,
) (memctx.ContextRetriever, error) {
	if minScore <= 0 {
		minScore = memctx.DefaultMinScore
	}
	pipeline, err := memembed.NewEmbeddingPipeline(embClient)
	if err != nil {
		return nil, fmt.Errorf("build memory retriever pipeline: %w", err)
	}
	searcher := experienceadapters.NewExperienceSearcher(expRepo)
	mr, err := memctx.NewMemoryRetriever(embClient, pipeline, searcher, experienceadapters.DefaultTenant, minScore)
	if err != nil {
		return nil, fmt.Errorf("build memory retriever: %w", err)
	}
	return mr, nil
}
