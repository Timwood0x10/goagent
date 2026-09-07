// Package ares_bootstrap — periodic storage maintenance.
//
// Closes the "write-only retention" open loop: tables
// with expires_at / decay_at columns are purged on a schedule instead of
// growing unboundedly while read paths silently filter dead rows.
package ares_bootstrap

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"time"

	"github.com/Timwood0x10/ares/internal/ares_config"
	"github.com/Timwood0x10/ares/internal/logger"
	"github.com/Timwood0x10/ares/internal/storage/postgres"
	"github.com/Timwood0x10/ares/internal/storage/postgres/repositories"
)

var logMaintenance = logger.Module("ares_bootstrap.maintenance")

// ExpiryCleaner is implemented by repositories that own rows with a
// retention window (expires_at / decay_at). Consumer-side interface: it is
// defined here so repositories do not need to know about the bootstrap
// maintenance worker.
type ExpiryCleaner interface {
	// CleanupExpired deletes expired/decayed rows and reports how many were
	// removed. Implementations must be idempotent and safe to call repeatedly.
	CleanupExpired(ctx context.Context) (int64, error)
}

// NamedExpiryCleaner pairs an ExpiryCleaner with the table family it owns, so
// maintenance logs identify what was purged without parsing SQL.
type NamedExpiryCleaner struct {
	// Name identifies the table family (e.g. "experiences_1024").
	Name string
	// Cleaner performs the purge.
	Cleaner ExpiryCleaner
}

// knowledgeRetention is the age threshold for pruning low-value knowledge
// chunks (updated_at older than this AND access_count < 10). 90 days keeps
// rarely-touched chunks from accumulating forever while preserving anything
// recently read or written.
const knowledgeRetention = 90 * 24 * time.Hour

// knowledgeCleanerAdapter bridges *KnowledgeRepository (whose CleanupExpired
// takes an explicit cutoff) to the parameterless ExpiryCleaner interface by
// supplying a rolling now-knowledgeRetention cutoff on each pass.
type knowledgeCleanerAdapter struct {
	repo *repositories.KnowledgeRepository
}

// CleanupExpired deletes knowledge chunks older than knowledgeRetention that
// are below the access-count floor. Satisfies ExpiryCleaner.
func (a knowledgeCleanerAdapter) CleanupExpired(ctx context.Context) (int64, error) {
	return a.repo.CleanupExpired(ctx, time.Now().Add(-knowledgeRetention))
}

// wireExpiryCleaners registers the remaining retention-managed repositories
// (sessions, conversations, secrets, knowledge_chunks) with the maintenance
// worker, reusing the already-open distillation pool's *sql.DB. Best-effort:
// each repo is registered independently and a construction failure for one
// (e.g. the secret cipher) skips only that table, never the others.
//
// Previously only experiences_1024
// was purged on schedule; the other four CleanupExpired implementations had
// no production caller and their dead rows grew unbounded.
func wireExpiryCleaners(comp *Components, db *sql.DB, cfg *ares_config.Config) {
	if comp == nil || db == nil {
		return
	}

	// Sessions: DELETE WHERE expired_at < now.
	sessionRepo := postgres.NewSessionRepositoryWithDB(db)
	comp.ExpiryCleaners = append(comp.ExpiryCleaners,
		NamedExpiryCleaner{Name: "sessions", Cleaner: sessionRepo})

	// Conversations: DELETE WHERE expires_at < now.
	convRepo := repositories.NewConversationRepository(db)
	comp.ExpiryCleaners = append(comp.ExpiryCleaners,
		NamedExpiryCleaner{Name: "conversations", Cleaner: convRepo})

	// Knowledge chunks: prune stale, low-access rows via the age adapter.
	knowRepo := repositories.NewKnowledgeRepository(db, db)
	comp.ExpiryCleaners = append(comp.ExpiryCleaners,
		NamedExpiryCleaner{Name: tableKnowledgeChunks,
			Cleaner: knowledgeCleanerAdapter{repo: knowRepo}})

	// Secrets: DELETE WHERE expires_at < now. NewSecretRepository requires a
	// 32-byte AES key at construction even though CleanupExpired never
	// encrypts/decrypts. Derive a stable 32-byte key from the JWT secret (or
	// a fixed label when unset) purely to satisfy the constructor; no secret
	// value is read on the cleanup path, so this key is never used to expose
	// data.
	secretKey := deriveSecretCleanupKey(cfg)
	if secretRepo, err := repositories.NewSecretRepository(db, secretKey); err != nil {
		logMaintenance.Warn("bootstrap: secret expiry cleaner not wired", "error", err)
	} else {
		comp.ExpiryCleaners = append(comp.ExpiryCleaners,
			NamedExpiryCleaner{Name: "secrets", Cleaner: secretRepo})
	}
}

// deriveSecretCleanupKey returns a deterministic 32-byte key for constructing
// the SecretRepository on the cleanup path. It hashes the configured JWT
// secret (or a fixed label if none is set) so NewSecretRepository's AES-256
// length check passes; the key is used only for construction, never to
// decrypt stored secrets during CleanupExpired.
func deriveSecretCleanupKey(cfg *ares_config.Config) []byte {
	seed := "ares-secret-cleanup"
	if cfg != nil && cfg.Security.JWTSecret != "" {
		seed = cfg.Security.JWTSecret
	}
	sum := sha256.Sum256([]byte(seed))
	return sum[:]
}

// expiryCleanupInterval is how often the maintenance worker purges expired
// rows. One hour keeps dead-row volume negligible relative to write rates
// without adding meaningful database load; the first run happens one full
// interval after startup so a booting system never pays cleanup latency.
const expiryCleanupInterval = time.Hour

// startExpiryCleanupWorker launches a background ticker on comp.bgGroup that
// periodically invokes every registered cleaner. Best-effort by design: one
// cleaner failing or panicking never cancels the loop nor blocks the others,
// and no cleaners being wired is a no-op. The goroutine exits when ctx is
// cancelled during graceful shutdown.
func startExpiryCleanupWorker(ctx context.Context, comp *Components) {
	if comp == nil || len(comp.ExpiryCleaners) == 0 {
		return
	}
	cleaners := make([]NamedExpiryCleaner, len(comp.ExpiryCleaners))
	copy(cleaners, comp.ExpiryCleaners)
	comp.bgGroup.Go(func() error {
		ticker := time.NewTicker(expiryCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				runExpiredCleanup(ctx, cleaners)
			}
		}
	})
	logMaintenance.InfoContext(ctx, "bootstrap: expiry cleanup worker started",
		"cleaners", len(cleaners),
		"interval", expiryCleanupInterval.String())
}

// runExpiredCleanup executes one purge pass over all cleaners. Panics are
// recovered per cleaner: maintenance must not take the process down.
func runExpiredCleanup(ctx context.Context, cleaners []NamedExpiryCleaner) {
	for _, nc := range cleaners {
		nc := nc
		func() {
			defer func() {
				if r := recover(); r != nil {
					logMaintenance.ErrorContext(ctx, "bootstrap: expiry cleanup panicked",
						"table", nc.Name, "panic", r)
				}
			}()
			deleted, err := nc.Cleaner.CleanupExpired(ctx)
			if err != nil {
				logMaintenance.WarnContext(ctx, "bootstrap: expiry cleanup failed",
					"table", nc.Name, "error", err)
				return
			}
			if deleted > 0 {
				logMaintenance.InfoContext(ctx, "bootstrap: expiry cleanup pass",
					"table", nc.Name, "deleted", deleted)
			}
		}()
	}
}
