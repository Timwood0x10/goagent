package sdk

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"

	_ "github.com/lib/pq"

	apiembed "github.com/Timwood0x10/ares/api/embedding"
	ares_bootstrap "github.com/Timwood0x10/ares/internal/ares_bootstrap"
	"github.com/Timwood0x10/ares/internal/evidence"
	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/adapter"
	"github.com/Timwood0x10/ares/internal/knowledge/linker"
	"github.com/Timwood0x10/ares/internal/knowledge/planner"
	"github.com/Timwood0x10/ares/internal/knowledge/provider"
	evoprovider "github.com/Timwood0x10/ares/internal/knowledge/provider/evolution"
	memprovider "github.com/Timwood0x10/ares/internal/knowledge/provider/memory"
	storeprovider "github.com/Timwood0x10/ares/internal/knowledge/provider/store"
	khruntime "github.com/Timwood0x10/ares/internal/knowledge/runtime"
	memstore "github.com/Timwood0x10/ares/internal/knowledge/store/memory"
	postgresstore "github.com/Timwood0x10/ares/internal/knowledge/store/postgres"
	sqlitestore "github.com/Timwood0x10/ares/internal/knowledge/store/sqlite"
	ares_evolution "github.com/Timwood0x10/ares/internal/runtime/ares_evolution"
	memory "github.com/Timwood0x10/ares/internal/runtime/memory"
	"github.com/Timwood0x10/ares/internal/storage/postgres"
)

// memSearcher adapts memory.MemoryManager to the memory.TaskSearcher
// interface. It converts the manager's []*models.Task results into the
// memprovider.SearchResult shape expected by the AKF memory provider.
type memSearcher struct {
	svc memory.MemoryManager
}

// SearchSimilarTasks delegates to the MemoryManager and converts each
// *models.Task into a memprovider.SearchResult. The Task.TaskID maps to
// SearchResult.ID; the "input" payload field (set by SearchSimilarTasks
// on the manager) maps to Summary. Tasks without an input payload fall
// back to the TaskID as the summary.
//
// SearchResult.Score is intentionally left 0: models.Task has no
// similarity-score field today, so there is no real query-relevance signal
// to forward. The MemoryProvider's relevanceFromScore handles Score=0 by
// deriving a rank-based Relevance from result ordering (first result → 1.0,
// decaying to a 0.1 floor), which is a honest signal rather than a fake
// constant. If a future Task revision adds a score field, populate it here
// and the provider will use it automatically.
func (s *memSearcher) SearchSimilarTasks(ctx context.Context, query string, limit int) ([]memprovider.SearchResult, error) {
	results, err := s.svc.SearchSimilarTasks(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	out := make([]memprovider.SearchResult, 0, len(results))
	for _, r := range results {
		summary := r.TaskID
		if r.Payload != nil {
			if sVal, ok := r.Payload["input"]; ok {
				if str, ok := sVal.(string); ok {
					summary = str
				}
			}
		}
		out = append(out, memprovider.SearchResult{
			ID:        r.TaskID,
			Summary:   summary,
			Timestamp: r.CreatedAt,
			// Score intentionally 0: see method comment.
		})
	}
	return out, nil
}

// memStrategyStore is an in-memory store that records evolved strategies
// and implements evoprovider.StrategyStore so the AKF knowledge fabric can
// consume them as decision-type KnowledgeObjects.
type memStrategyStore struct {
	mu      sync.Mutex
	active  *ares_evolution.Strategy
	history []*ares_evolution.Strategy
}

func newMemStrategyStore() *memStrategyStore {
	return &memStrategyStore{}
}

func (s *memStrategyStore) GetActive(_ context.Context) (*ares_evolution.Strategy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active, nil
}

func (s *memStrategyStore) GetHistory(_ context.Context, _ string, n int) ([]*ares_evolution.Strategy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n < 0 {
		n = 0
	}
	if n > len(s.history) {
		n = len(s.history)
	}
	return s.history[:n], nil
}

// knowledgeWiring bundles the outputs of wireKnowledge so New() can unpack
// a single struct instead of juggling four return values.
type knowledgeWiring struct {
	rt             *khruntime.KnowledgeRuntime
	store          knowledge.KnowledgeStore
	evolutionStore *memStrategyStore
}

// wireKnowledge constructs the AKF Knowledge Fabric runtime, store, and
// evolution strategy store from the SDK config. When knowledge is disabled,
// it returns a zero-value wiring (all nil). Extracted from New() to keep
// the constructor under the 100-line limit.
//
// Args:
//
//	cfg      - fully applied SDK config; knlCfg/evoCfg/dbCfg/sqliteStorePath are read.
//	memMgr   - memory manager; when non-nil, a memory provider is auto-registered
//	           into the knowledge provider registry so past tasks surface in the AKG.
//	embClient - embedding service used by the StoreProvider for vector recall;
//	            nil signals lexical-only search.
//	embModel - embedding model name selecting which Representation the store
//	           compares against; empty is valid when embClient is nil.
//
// Returns:
//
//	*knowledgeWiring - rt/store/evolutionStore are nil when knowledge is disabled.
//	error             - wrapped error if a knowledge store or provider fails to init.
func wireKnowledge(
	cfg *config,
	memMgr memory.MemoryManager,
	embClient apiembed.EmbeddingService,
	embModel string,
) (*knowledgeWiring, error) {
	if !cfg.knlCfg.Enabled {
		return &knowledgeWiring{}, nil
	}

	reg := provider.NewProviderRegistry()

	if err := registerKnowledgeProviders(reg, cfg, memMgr); err != nil {
		return nil, err
	}

	store, err := buildKnowledgeStore(cfg)
	if err != nil {
		return nil, err
	}

	// Register the StoreProvider so AKG-distilled facts written to the store
	// by the DistillBridge are readable by the KnowledgeRuntime as a
	// KnowledgeObject source. This closes the 0.2.9 write→read loop.
	if store != nil {
		sp := storeprovider.New("akg_store", store, embClient, embModel, akgNamespace)
		if err := reg.Register(sp); err != nil {
			return nil, fmt.Errorf("knowledge: register store provider: %w", err)
		}
	}

	var evoStore *memStrategyStore
	if cfg.evoCfg.Enabled {
		evoStore = newMemStrategyStore()
	}

	rt := khruntime.New(
		planner.NewKnowledgePlanner(),
		planner.NewSourceDiscovery(reg, planner.NewQueryPlanner()),
		reg,
		nil, // pipeline: use defaults
		[]khruntime.Linker{
			&khruntime.DefaultLinker{},
			&linker.DecisionLinker{},
			&linker.ArchitectureLinker{},
			&linker.TimelineLinker{},
			&linker.SimilarityLinker{},
		},
		[]khruntime.Reducer{&khruntime.DefaultReducer{}},
	)

	return &knowledgeWiring{rt: rt, store: store, evolutionStore: evoStore}, nil
}

// registerKnowledgeProviders registers the memory, evolution, and
// user-configured extra providers into the registry. Extracted to keep
// wireKnowledge under 100 lines.
func registerKnowledgeProviders(reg *provider.ProviderRegistry, cfg *config, memMgr memory.MemoryManager) error {
	if memMgr != nil {
		searcher := &memSearcher{svc: memMgr}
		if err := reg.Register(memprovider.New("memory", searcher)); err != nil {
			return fmt.Errorf("knowledge: register memory provider: %w", err)
		}
	}

	if cfg.evoCfg.Enabled {
		evoStore := newMemStrategyStore()
		if err := reg.Register(evoprovider.New("evolution", evoStore)); err != nil {
			return fmt.Errorf("knowledge: register evolution provider: %w", err)
		}
	}

	for _, p := range cfg.extraProviders {
		if err := reg.Register(p); err != nil {
			return fmt.Errorf("knowledge: register provider %s: %w", p.Name(), err)
		}
	}
	return nil
}

// buildKnowledgeStore selects the knowledge store backend: SQLite >
// PostgreSQL > in-memory. All opt-in via SDK options; defaults to
// in-memory to preserve prior behaviour.
func buildKnowledgeStore(cfg *config) (knowledge.KnowledgeStore, error) {
	switch {
	case cfg.sqliteStorePath != "":
		s, err := sqlitestore.New(cfg.sqliteStorePath)
		if err != nil {
			return nil, fmt.Errorf("knowledge: init sqlite store: %w", err)
		}
		return s, nil
	case cfg.dbCfg.Host != "":
		sslMode := cfg.dbCfg.SSLMode
		if sslMode == "" {
			sslMode = sslModeDisable
		}
		dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
			cfg.dbCfg.User, cfg.dbCfg.Password, cfg.dbCfg.Host,
			cfg.dbCfg.Port, cfg.dbCfg.Database, sslMode)
		db, err := sql.Open("postgres", dsn)
		if err != nil {
			return nil, fmt.Errorf("knowledge: open postgres store: %w", err)
		}
		store, err := postgresstore.New(db)
		if err != nil {
			if closeErr := db.Close(); closeErr != nil {
				err = fmt.Errorf("knowledge: init postgres store: %w (also close db: %v)", err, closeErr)
			}
			return nil, fmt.Errorf("knowledge: init postgres store: %w", err)
		}
		return store, nil
	default:
		return memstore.New(), nil
	}
}

// resolveAKGEmbeddingModel picks the embedding model for the AKG loop. The
// AKG-specific model (cfg.knlCfg.EmbeddingModel) takes precedence so users can
// pin a different model for fact distillation than the one used by the memory
// distiller. Falls back to the base embedding service model when unset.
func resolveAKGEmbeddingModel(cfg *config) string {
	if cfg.knlCfg.EmbeddingModel != "" {
		return cfg.knlCfg.EmbeddingModel
	}
	return cfg.embedCfg.Model
}

// buildAKGBridge constructs the DistillBridge that distills conversations into
// AKG KnowledgeObjects and persists them through the quality gate. Returns nil
// (no-op) when either the distiller or the store is unavailable, so the caller
// can unconditionally assign the result. The quality gate falls back to the
// knowledge package default when left at zero, matching WithAKGQualityGate's
// documented "zero value = default" contract.
func buildAKGBridge(
	cfg *config,
	distiller adapter.ConversationDistiller,
	store knowledge.KnowledgeStore,
	embClient apiembed.EmbeddingService,
	embModel string,
) *adapter.DistillBridge {
	if distiller == nil || store == nil {
		return nil
	}
	gate := cfg.knlCfg.QualityGate
	if gate.MinFinalScore == 0 {
		gate = knowledge.DefaultQualityGateConfig()
	}
	return adapter.NewDistillBridgeWithGate(
		distiller, nil, store, embClient,
		gate, knowledge.NewRelationExtractor(),
		akgNamespace, embModel,
	)
}

// buildSDKEvidenceStore creates a persistent evidence store (Postgres) only
// when it will actually be consumed by the SDK's own evolution wiring —
// evolution enabled, an SDK-owned knowledge runtime exists, and the Bootstrap
// core does not supply its own NewEvolution (which would discard evStore).
// This avoids hard-failing startup for a configured-but-unused Postgres and
// avoids an idle connection pool. SSLMode normalization mirrors
// buildKnowledgeStore/buildPostgresPool (empty means "disable"). The returned
// pool (when non-nil) is owned by the caller and closed in Runtime.Close().
//
// Args:
//
//	cfg           - fully applied SDK config; evoCfg/dbCfg are read.
//	knowRt        - SDK-owned knowledge runtime; nil when knowledge is disabled.
//	bootstrapComp - Bootstrap core (may be nil); its NewEvolution would
//	                replace the SDK evolution wiring and discard evStore.
//
// Returns:
//
//	evStore - the evidence store (nil when unused → default in-memory store).
//	pgPool  - the Postgres pool backing evStore; nil when not created.
//	err     - fail-loud error when configured Postgres cannot be created.
func buildSDKEvidenceStore(cfg *config, knowRt *khruntime.KnowledgeRuntime, bootstrapComp *ares_bootstrap.Components) (evidence.Store, *postgres.Pool, error) {
	if !cfg.evoCfg.Enabled || knowRt == nil ||
		(bootstrapComp != nil && bootstrapComp.NewEvolution != nil) ||
		cfg.dbCfg.Host == "" {
		return nil, nil, nil
	}
	sslMode := cfg.dbCfg.SSLMode
	if sslMode == "" {
		sslMode = sslModeDisable
	}
	pgCfg := &postgres.Config{
		Host:     cfg.dbCfg.Host,
		Port:     cfg.dbCfg.Port,
		User:     cfg.dbCfg.User,
		Password: cfg.dbCfg.Password,
		Database: cfg.dbCfg.Database,
		SSLMode:  sslMode,
	}
	pgPool, pgErr := postgres.NewPool(pgCfg)
	if pgErr != nil {
		return nil, nil, fmt.Errorf("sdk: evidence postgres pool: %w", pgErr)
	}
	pgStore, storeErr := evidence.NewPostgresStore(pgPool)
	if storeErr != nil {
		_ = pgPool.Close()
		return nil, nil, fmt.Errorf("sdk: evidence postgres store: %w", storeErr)
	}
	return pgStore, pgPool, nil
}

// wireEvolutionHotUpdate wires the live KnowledgeRuntime into the evolution
// patch system so knowledge patches affect the running engine. Returns nil
// (no-op) when evolution or knowledge is disabled, or when wiring fails
// (non-fatal: a warning is logged). Extracted from New() to keep the
// constructor under the 100-line limit.
//
// Branch B: SDK has no live DAG, so workflow/scheduler/recovery
// evolution is serve-only; the nil-DAG path is explicitly logged.
func wireEvolutionHotUpdate(cfg *config, knowRt *khruntime.KnowledgeRuntime, memMgr memory.MemoryConfigStore, evStore evidence.Store) *ares_bootstrap.NewEvolutionComponents {
	if !cfg.evoCfg.Enabled || knowRt == nil {
		return nil
	}
	// Branch B: SDK has no live DAG — workflow/scheduler/recovery evolution
	// is serve-only. Explicit log to eliminate silent synthetic-executor gap.
	slog.Info("sdk: evolution hot-update: dag=nil (workflow/scheduler/recovery " +
		"evolution is serve-only; SDK provides knowledge/memory evolution)")
	comps, err := ares_bootstrap.ProvideNewEvolution(nil, knowRt, memMgr, evStore)
	if err != nil {
		slog.Warn("sdk: evolution hot-update wiring failed; knowledge runtime not patchable",
			"error", err)
		return nil
	}
	slog.Info("sdk: evolution hot-update wired (knowledge runtime patchable by evolution)")
	return comps
}

// wireSDKEvolution wires the SDK's evolution hot-update path and its evidence
// store. Stage 8: when the Bootstrap core supplies a NewEvolution it is reused
// (the Bootstrap-assembled component wins and any SDK-owned evStore is
// discarded); otherwise the SDK dual-track wiring is kept as a compatibility
// fallback. Evidence persistence: the persistent evidence store is
// created only when it will actually be consumed — evolution enabled, an
// SDK-owned knowledge runtime exists, and the Bootstrap core does not supply
// its own NewEvolution. This avoids hard-failing startup for a
// configured-but-unused Postgres and avoids an idle pool. SSLMode
// normalization mirrors buildKnowledgeStore/buildPostgresPool (empty means
// "disable" for local/dev PostgreSQL). Extracted from New() to keep the
// constructor under the 100-line limit.
func wireSDKEvolution(cfg *config, kw *knowledgeWiring, bootstrapComp *ares_bootstrap.Components) (*ares_bootstrap.NewEvolutionComponents, *postgres.Pool, error) {
	evStore, pgPool, err := buildSDKEvidenceStore(cfg, kw.rt, bootstrapComp)
	if err != nil {
		return nil, nil, err
	}
	evoComponents := wireEvolutionHotUpdate(cfg, kw.rt, nil, evStore)
	if bootstrapComp != nil && bootstrapComp.NewEvolution != nil {
		evoComponents = bootstrapComp.NewEvolution
	}
	return evoComponents, pgPool, nil
}
