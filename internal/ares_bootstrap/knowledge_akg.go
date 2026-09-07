package ares_bootstrap

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq" // postgres driver registration for the AKG store
	"golang.org/x/sync/errgroup"

	apiembed "github.com/Timwood0x10/ares/api/embedding"
	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/ares_events"
	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/adapter"
	memstore "github.com/Timwood0x10/ares/internal/knowledge/store/memory"
	postgresstore "github.com/Timwood0x10/ares/internal/knowledge/store/postgres"
	"github.com/Timwood0x10/ares/internal/runtime/memory/distillation"
	memembed "github.com/Timwood0x10/ares/internal/runtime/memory/embedding"
	"github.com/Timwood0x10/ares/internal/runtime/memory/experienceadapters"
	"github.com/Timwood0x10/ares/internal/storage/postgres/embedding"
	"github.com/Timwood0x10/ares/internal/storage/postgres/repositories"
)

// akgNamespace is the namespace assigned to every AKG-distilled fact so the
// write loop (DistillBridge) and the read loop (StoreProvider) address the
// same slice of the knowledge store. It must match the value used by the
// SDK path (sdk/sdk.go) so both entry points see the same facts.
const akgNamespace = "default"

// storageTypePostgres is the storage backend identifier for PostgreSQL used
// across the bootstrap wiring to select the persistent store backend.
const storageTypePostgres = "postgres"

// akgBridgeTimeout bounds a single DistillBridge call so a slow LLM/embedding
// invocation cannot block the event subscriber loop indefinitely.
const akgBridgeTimeout = 30 * time.Second

// akgRoleUser and akgRoleAssistant are the conversation roles used when
// replaying a task event into the AKG DistillBridge as a user→assistant pair.
const (
	akgRoleUser      = "user"
	akgRoleAssistant = "assistant"
)

// buildBootstrapKnowledgeStore selects the AKG store backend for the
// bootstrap path. Per the product decision (2026-08-03): in-memory is the
// default so no external database is required; PostgreSQL is used when
// cfg.Storage is explicitly configured for postgres (host set). The
// returned store is nil when AKG is disabled (handled by the caller).
//
// Args:
//
//	cfg - full config; Storage.Enabled/Type/Host decide PG vs in-memory.
//
// Returns:
//
//	knowledge.KnowledgeStore - in-memory store by default, PG store when
//	                           postgres storage is configured.
//	error                    - wrapped error if PG init fails (caller falls
//	                           back to read-only mode, not hard failure).
func buildBootstrapKnowledgeStore(cfg *ares_config.Config) (knowledge.KnowledgeStore, error) {
	if cfg.Storage.Enabled && cfg.Storage.Type == storageTypePostgres && cfg.Storage.Host != "" {
		dsn := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
			cfg.Storage.Host, cfg.Storage.Port, cfg.Storage.Username,
			cfg.Storage.Password, cfg.Storage.Database, cfg.Storage.SSLMode)
		db, err := sql.Open(storageTypePostgres, dsn)
		if err != nil {
			return nil, fmt.Errorf("bootstrap: open postgres knowledge store: %w", err)
		}
		pingCtx, pingCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer pingCancel()
		if err := db.PingContext(pingCtx); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("bootstrap: ping postgres knowledge store: %w", err)
		}
		store, err := postgresstore.New(db)
		if err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("bootstrap: init postgres knowledge store: %w", err)
		}
		return store, nil
	}
	return memstore.New(), nil
}

// buildAKGDistiller constructs a standalone conversation distiller for the
// AKG write loop. The memory manager builds its own internal distiller that
// is not exposed on its interface, so a separate instance is constructed
// from the same embedding client and experience repo — mirroring the SDK
// path (sdk/memory_wiring.go buildAKGDistiller).
//
// Args:
//
//	emb     - embedding service used for vector generation; must be non-nil.
//	expRepo - postgres experience repo backing the distiller's store;
//	          must be non-nil.
//
// Returns:
//
//	adapter.ConversationDistiller - ready to feed to the DistillBridge.
//	error                         - wrapped error if the embedding pipeline
//	                                cannot be constructed.
func buildAKGDistiller(
	emb apiembed.EmbeddingService,
	expRepo repositories.ExperienceRepositoryInterface,
) (adapter.ConversationDistiller, error) {
	distillRepo := experienceadapters.NewDistillationRepo(expRepo, experienceadapters.DefaultTenant)
	distiller := distillation.NewDistiller(distillation.DefaultDistillationConfig(), emb, distillRepo)
	pipeline, err := memembed.NewEmbeddingPipeline(emb)
	if err != nil {
		return nil, fmt.Errorf("akg distiller embedding pipeline: %w", err)
	}
	distiller.SetEmbeddingPipeline(pipeline)
	return distiller, nil
}

// buildBootstrapAKGBridge constructs the write-side DistillBridge that turns
// task lifecycle events into KnowledgeObjects persisted through the AKG
// quality gate. Returns nil (no-op) when either the distiller or the store
// is unavailable so callers can unconditionally assign the result. The
// quality gate uses the knowledge package default; the embedding model is
// taken from emb when present.
func buildBootstrapAKGBridge(
	distiller adapter.ConversationDistiller,
	store knowledge.KnowledgeStore,
	emb apiembed.EmbeddingService,
) *adapter.DistillBridge {
	if distiller == nil || store == nil {
		return nil
	}
	return adapter.NewDistillBridgeWithGate(
		distiller,
		nil, // pipeline: nil skips the AKF pipeline, matching the SDK path
		store,
		emb,
		knowledge.DefaultQualityGateConfig(),
		knowledge.NewRelationExtractor(),
		akgNamespace,
		akgModelName(emb),
	)
}

// wireAKGLoop assembles the AKG closed loop for the bootstrap path:
//   - builds the KnowledgeStore (in-memory default, PG optional),
//   - registers nothing here (BuildKnowledgeRuntime registers the store
//     provider on the read side),
//   - builds the write-side DistillBridge when embedding + experience repo
//     are available.
//
// The whole loop is gated on cfg.Knowledge.RetrievalEnabled so minimal
// configs (default false) keep the pre-AKG behavior. Failures are non-fatal:
// each step logs and degrades (store-only, or no loop at all) instead of
// failing bootstrap.
//
// Returns:
//
//	store  - the KnowledgeStore, or nil when AKG is disabled / store init failed.
//	bridge - the DistillBridge, or nil when the write deps are unavailable.
func wireAKGLoop(
	cfg *ares_config.Config,
	deps *BootstrapDeps,
	embClient *embedding.EmbeddingClient,
) (knowledge.KnowledgeStore, *adapter.DistillBridge) {
	if cfg == nil || !cfg.Knowledge.RetrievalEnabled {
		return nil, nil
	}

	store, err := buildBootstrapKnowledgeStore(cfg)
	if err != nil {
		log.Warn("bootstrap: AKG knowledge store init failed; AKG loop skipped", "error", err)
		return nil, nil
	}

	if embClient == nil || deps == nil || deps.ExpRepo == nil {
		log.Info("bootstrap: AKG store wired (read loop); embedding/experience repo unavailable, write loop skipped",
			"backend", akgStoreBackend(cfg))
		return store, nil
	}

	distiller, err := buildAKGDistiller(embClient, deps.ExpRepo)
	if err != nil {
		log.Warn("bootstrap: AKG distiller init failed; write loop skipped (read loop active)",
			"error", err)
		return store, nil
	}

	bridge := buildBootstrapAKGBridge(distiller, store, embClient)
	if bridge != nil {
		log.Info("bootstrap: AKG DistillBridge wired (conversations → knowledge store)",
			"backend", akgStoreBackend(cfg))
	}
	return store, bridge
}

// triggerAKGBridge extracts task/result text from a task-lifecycle event and
// feeds it to the AKG DistillBridge as a user→assistant conversation. The
// call runs in a bounded goroutine under eg with a 30s timeout so a slow
// bridge never blocks the subscriber loop. Errors are logged and never
// returned (best-effort): the bridge runs alongside the experience
// DistillationService and a failure here does not affect the main path.
//
// Content guards mirror HandleTaskCompletedForDistillation: events without a
// tenant_id or sufficient task/result text are skipped because the distiller
// cannot produce meaningful memories from them.
func triggerAKGBridge(
	ctx context.Context,
	eg *errgroup.Group,
	ev *ares_events.Event,
	bridge *adapter.DistillBridge,
) {
	if bridge == nil || ev == nil {
		return
	}
	taskText := akgEventStringField(ev.Payload, ares_events.EventKeyTask)
	resultText := akgEventStringField(ev.Payload, ares_events.EventKeyResult)
	tenantID := akgEventStringField(ev.Payload, ares_events.EventKeyTenantID)
	agentID := akgEventStringField(ev.Payload, "agent_id")

	if tenantID == "" || len(taskText) < 10 || len(resultText) < 20 {
		return
	}

	messages := []distillation.Message{
		{Role: akgRoleUser, Content: taskText},
		{Role: akgRoleAssistant, Content: resultText},
	}

	eg.Go(func() error {
		bridgeCtx, cancel := context.WithTimeout(ctx, akgBridgeTimeout)
		defer cancel()
		if _, err := bridge.DistillConversation(bridgeCtx, ev.ID, messages, tenantID, agentID); err != nil {
			log.Warn("bootstrap: AKG distill conversation failed",
				"event_id", ev.ID, "error", err)
		}
		return nil
	})
}

// akgEventStringField reads a string field from an event payload map,
// returning "" when the key is missing or not a string.
func akgEventStringField(p map[string]any, key string) string {
	if v, ok := p[key].(string); ok {
		return v
	}
	return ""
}

// akgModelName returns the embedding model name for the AKG loop, or ""
// when no embedding service is wired (lexical-only HybridSearch).
func akgModelName(emb apiembed.EmbeddingService) string {
	if emb == nil {
		return ""
	}
	return emb.GetModel()
}

// akgStoreBackend returns a human-readable backend label for logs.
func akgStoreBackend(cfg *ares_config.Config) string {
	if cfg.Storage.Enabled && cfg.Storage.Type == storageTypePostgres && cfg.Storage.Host != "" {
		return storageTypePostgres
	}
	return "in-memory"
}
