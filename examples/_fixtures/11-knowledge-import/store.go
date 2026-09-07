// This file wires the storage stack together and implements ingestion:
// recursive markdown discovery, structural parsing, chunking, embedding, and
// persistence into PostgreSQL + pgvector.
package main

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Timwood0x10/ares/internal/llm"
	"github.com/Timwood0x10/ares/internal/storage/postgres"
	"github.com/Timwood0x10/ares/internal/storage/postgres/embedding"
	"github.com/Timwood0x10/ares/internal/storage/postgres/models"
	"github.com/Timwood0x10/ares/internal/storage/postgres/repositories"
	"github.com/Timwood0x10/ares/internal/storage/postgres/services"
)

// IngestStats summarises the result of an ingestion run.
type IngestStats struct {
	Files   int // Number of markdown files processed.
	Chunks  int // Number of chunks successfully stored.
	Skipped int // Number of chunks skipped due to per-chunk errors.
}

// DocSummary is a per-document listing entry.
type DocSummary struct {
	Source string // Source file path.
	Chunks int    // Number of chunks stored for the document.
}

// KnowledgeBase owns the storage, embedding, retrieval and LLM dependencies.
// All external clients are held via their concrete example types; the fields
// are private so callers go through the exported methods.
type KnowledgeBase struct {
	cfg       *Config
	pool      *postgres.Pool
	repo      *repositories.KnowledgeRepository
	embedder  *embedding.EmbeddingClient
	retrieval *services.SimpleRetrievalService
	llmClient *llm.Client
}

// NewKnowledgeBase constructs and validates the full stack.
//
// Args:
//
//	ctx - startup context for schema migration and health checks.
//	cfg - validated configuration, must be non-nil.
//
// Returns:
//
//	kb  - a ready knowledge base, never nil on success.
//	err - a connection, migration or health-check error with context.
func NewKnowledgeBase(ctx context.Context, cfg *Config) (*KnowledgeBase, error) {
	if cfg == nil {
		return nil, wrapf(os.ErrInvalid, "new knowledge base: nil config")
	}

	pool, err := postgres.NewPool(buildPostgresConfig(cfg))
	if err != nil {
		return nil, wrapf(err, "connect postgres %s:%d", cfg.Database.Host, cfg.Database.Port)
	}

	if err := ensureSchema(ctx, pool); err != nil {
		_ = pool.Close()
		return nil, err
	}

	embedder := embedding.NewEmbeddingClient(
		cfg.Embedding.ServiceURL,
		cfg.Embedding.Model,
		nil,
		time.Duration(cfg.Embedding.TimeoutSeconds)*time.Second,
	)
	if err := embedder.HealthCheck(ctx); err != nil {
		_ = pool.Close()
		return nil, wrapf(err, "embedding service %s unhealthy", cfg.Embedding.ServiceURL)
	}

	repo := repositories.NewKnowledgeRepository(pool.GetDB(), pool.GetDB())
	retrieval := services.NewSimpleRetrievalService(repo, embedder, &services.SimpleRetrievalConfig{
		TopK:        cfg.Knowledge.TopK,
		MinScore:    cfg.Knowledge.MinScore,
		QueryPrefix: cfg.Knowledge.QueryPrefix,
	})

	var llmClient *llm.Client
	if cfg.llmEnabled() {
		llmClient, err = buildLLMClient(cfg)
		if err != nil {
			_ = pool.Close()
			return nil, err
		}
	}

	return &KnowledgeBase{
		cfg:       cfg,
		pool:      pool,
		repo:      repo,
		embedder:  embedder,
		retrieval: retrieval,
		llmClient: llmClient,
	}, nil
}

// Close releases the LLM client and database pool. It returns the pool close
// error, if any, so callers can surface shutdown failures.
func (kb *KnowledgeBase) Close() error {
	if kb.llmClient != nil {
		kb.llmClient.Close()
	}
	return kb.pool.Close()
}

// buildPostgresConfig maps the example config to the storage pool config.
func buildPostgresConfig(cfg *Config) *postgres.Config {
	return &postgres.Config{
		Host:            cfg.Database.Host,
		Port:            cfg.Database.Port,
		User:            cfg.Database.User,
		Password:        cfg.Database.Password,
		Database:        cfg.Database.Database,
		SSLMode:         cfg.Database.SSLMode,
		MaxOpenConns:    25,
		MaxIdleConns:    10,
		ConnMaxLifetime: 5 * time.Minute,
		QueryTimeout:    30 * time.Second,
		Embedding:       postgres.DefaultEmbeddingConfig(),
	}
}

// buildLLMClient constructs the LLM client. It must only be called when an LLM
// is configured; callers detect retrieval-only mode via Config.llmEnabled.
func buildLLMClient(cfg *Config) (*llm.Client, error) {
	client, err := llm.NewClient(&llm.Config{
		Provider:  cfg.LLM.Provider,
		APIKey:    cfg.LLM.APIKey,
		BaseURL:   cfg.LLM.BaseURL,
		Model:     cfg.LLM.Model,
		Timeout:   cfg.LLM.Timeout,
		MaxTokens: cfg.LLM.MaxTokens,
	})
	if err != nil {
		return nil, wrapf(err, "init llm client %s/%s", cfg.LLM.Provider, cfg.LLM.Model)
	}
	return client, nil
}

// ensureSchema enables pgvector and applies the storage migrations. Both steps
// are idempotent and safe to run on every startup.
func ensureSchema(ctx context.Context, pool *postgres.Pool) error {
	if _, err := pool.GetDB().ExecContext(ctx, "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
		return wrapf(err, "enable pgvector extension")
	}
	if err := postgres.MigrateStorage(ctx, pool); err != nil {
		return wrapf(err, "apply storage migrations")
	}
	return nil
}

// IngestDir recursively imports every markdown file under dir.
//
// Args:
//
//	ctx      - cancellation context; ingestion stops promptly on cancel.
//	tenantID - tenant namespace for the stored chunks, must be non-empty.
//	dir      - directory to walk, must exist and be a directory.
//
// Returns:
//
//	stats - counts of processed files and stored/skipped chunks.
//	err   - a discovery or fatal ingestion error with context.
func (kb *KnowledgeBase) IngestDir(ctx context.Context, tenantID, dir string) (IngestStats, error) {
	var stats IngestStats
	if strings.TrimSpace(tenantID) == "" {
		return stats, wrapf(os.ErrInvalid, "ingest dir: empty tenant id")
	}

	info, err := os.Stat(dir)
	if err != nil {
		return stats, wrapf(err, "stat dir %q", dir)
	}
	if !info.IsDir() {
		return stats, wrapf(os.ErrInvalid, "ingest dir: %q is not a directory", dir)
	}

	files, err := findMarkdownFiles(dir)
	if err != nil {
		return stats, err
	}
	slog.Info("discovered markdown files", "dir", dir, "count", len(files))

	for _, path := range files {
		select {
		case <-ctx.Done():
			return stats, wrapf(ctx.Err(), "ingest dir cancelled")
		default:
		}
		count, skipped, ferr := kb.IngestFile(ctx, tenantID, path)
		if ferr != nil {
			slog.Warn("skip file", "file", path, "error", ferr)
			continue
		}
		stats.Files++
		stats.Chunks += count
		stats.Skipped += skipped
	}
	return stats, nil
}

// IngestFile parses, chunks, embeds and stores a single markdown file.
//
// Args:
//
//	ctx      - cancellation context.
//	tenantID - tenant namespace, must be non-empty.
//	path     - markdown file path.
//
// Returns:
//
//	stored  - number of chunks persisted.
//	skipped - number of chunks skipped due to per-chunk errors.
//	err     - a fatal error (parse failure, dimension mismatch, or nothing stored).
func (kb *KnowledgeBase) IngestFile(ctx context.Context, tenantID, path string) (int, int, error) {
	doc, err := ParseFile(path)
	if err != nil {
		return 0, 0, err
	}
	chunks := ChunkDocument(doc, kb.cfg.Knowledge)
	if len(chunks) == 0 {
		slog.Info("no chunks produced", "file", path)
		return 0, 0, nil
	}

	docID := uuid.New().String()
	stored, skipped := 0, 0

	// Build all chunk models first.
	chunkModels := make([]*models.KnowledgeChunk, 0, len(chunks))
	for _, c := range chunks {
		m := &models.KnowledgeChunk{
			TenantID: tenantID, DocumentID: docID,
			SourceType: "markdown", Source: doc.Path,
			ChunkIndex: c.Index, Content: c.Content,
			ContentHash:      hashWithIndex(c.Content, c.Index),
			EmbeddingVersion: 1,
			EmbeddingStatus:  models.EmbeddingStatusPending,
			Metadata:         chunkMetadata(c, doc),
		}
		chunkModels = append(chunkModels, m)
	}

	// Embed all chunks.
	for i, m := range chunkModels {
		select {
		case <-ctx.Done():
			return stored, skipped, wrapf(ctx.Err(), "ingest cancelled")
		default:
		}
		vec, err := kb.embedder.EmbedWithPrefix(ctx, m.Content, kb.cfg.Knowledge.PassagePrefix)
		if err != nil {
			slog.Warn("embed failed", "file", path, "index", i, "error", err)
			skipped++
			chunkModels[i] = nil // mark for removal
			continue
		}
		if len(vec) != kb.cfg.Embedding.Dimensions {
			skipped++
			chunkModels[i] = nil
			continue
		}
		m.Embedding = postgres.NormalizeVector(vec)
		m.EmbeddingStatus = models.EmbeddingStatusCompleted
	}

	// Filter out failed embeddings.
	toStore := make([]*models.KnowledgeChunk, 0, len(chunkModels))
	for _, m := range chunkModels {
		if m != nil {
			toStore = append(toStore, m)
		}
	}
	if len(toStore) == 0 {
		return 0, skipped, wrapf(os.ErrInvalid, "no chunks embedded for %q", path)
	}

	// Batch insert with retry.
	if err := retryWithBackoff(ctx, 3, func() error {
		return kb.repo.CreateBatch(ctx, toStore)
	}); err != nil {
		return 0, skipped, wrapf(err, "batch insert %q", path)
	}

	stored = len(toStore)
	slog.Info("imported file", "file", path, "stored", stored, "skipped", skipped)
	return stored, skipped, nil
}

// storeChunk embeds one chunk and writes it. Deprecated: use IngestFile with batch.

// retryWithBackoff retries fn up to maxAttempts with exponential backoff.
func retryWithBackoff(ctx context.Context, maxAttempts int, fn func() error) error {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(100<<uint(attempt-1)) * time.Millisecond
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
			slog.Warn("retrying", "attempt", attempt+1, "backoff", backoff)
		}
		if err := fn(); err != nil {
			lastErr = err
			slog.Warn("attempt failed", "attempt", attempt+1, "error", err)
			continue
		}
		return nil
	}
	return lastErr
}

// ListDocuments returns the distinct stored documents for a tenant.
func (kb *KnowledgeBase) ListDocuments(ctx context.Context, tenantID string) ([]DocSummary, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, wrapf(os.ErrInvalid, "list documents: empty tenant id")
	}
	const query = `SELECT source, COUNT(*) FROM knowledge_chunks_1024
		WHERE tenant_id = $1 GROUP BY source ORDER BY source`

	rows, err := kb.pool.Query(ctx, query, tenantID)
	if err != nil {
		return nil, wrapf(err, "query documents")
	}
	defer func() { _ = rows.Close() }()

	var docs []DocSummary
	for rows.Next() {
		var d DocSummary
		if err := rows.Scan(&d.Source, &d.Chunks); err != nil {
			return nil, wrapf(err, "scan document row")
		}
		docs = append(docs, d)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapf(err, "iterate document rows")
	}
	return docs, nil
}

// findMarkdownFiles walks dir recursively and returns all markdown file paths.
func findMarkdownFiles(dir string) ([]string, error) {
	var files []string
	walkErr := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext == ".md" || ext == ".markdown" {
			files = append(files, path)
		}
		return nil
	})
	if walkErr != nil {
		return nil, wrapf(walkErr, "walk dir %q", dir)
	}
	return files, nil
}
