// Package postgres provides PostgreSQL database operations for the storage system.
package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	stderrors "errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Timwood0x10/ares/internal/errors"
)

// WriteBuffer provides write batching to reduce database and embedding load.
// This implements an in-memory buffer with periodic flushing to batch database operations.
type WriteBuffer struct {
	db              *Pool
	buffer          chan *WriteItem
	batchSize       int
	flushInterval   time.Duration
	queue           *EmbeddingQueue
	embeddingConfig *EmbeddingConfig
	mu              sync.Mutex
	wg              sync.WaitGroup
	stopped         atomic.Bool
	closeOnce       sync.Once // Ensure channel is closed only once
	g               *errgroup.Group
	gctx            context.Context
}

// WriteItem represents a single write operation to be batched.
type WriteItem struct {
	TenantID string
	Table    string
	Content  string
	Metadata map[string]interface{}

	// EmbeddingSpec fields for canonical spec tracking.
	SpecKind   string
	SpecPrefix string
	SpecDim    int
	SpecHash   string
}

// NewWriteBuffer creates a new WriteBuffer instance.
// Args:
// pool - database connection pool.
// queue - embedding queue for async processing.
// batchSize - number of items to batch before flushing.
// flushInterval - maximum time between flushes.
// embeddingConfig - embedding configuration for model and version settings.
// Returns new WriteBuffer instance.
func NewWriteBuffer(pool *Pool, queue *EmbeddingQueue, batchSize int, flushInterval time.Duration, embeddingConfig *EmbeddingConfig) *WriteBuffer {
	if embeddingConfig == nil {
		embeddingConfig = DefaultEmbeddingConfig()
	}
	return &WriteBuffer{
		db:              pool,
		buffer:          make(chan *WriteItem, batchSize*2), // Double size to avoid blocking
		batchSize:       batchSize,
		flushInterval:   flushInterval,
		queue:           queue,
		embeddingConfig: embeddingConfig,
	}
}

// Start begins the buffer processing loop in a background goroutine.
// This method returns immediately after starting the goroutine.
// The processing loop runs until Stop is called.
//
// Args:
// ctx - context for cancellation and graceful shutdown.
// Returns error if the goroutine fails to start.
func (b *WriteBuffer) Start(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.stopped.Load() {
		return errors.New("write buffer already stopped")
	}
	if b.g != nil {
		return errors.New("write buffer already started")
	}

	// Create errgroup for goroutine management
	b.g, b.gctx = errgroup.WithContext(ctx)

	b.wg.Add(1)
	b.g.Go(func() error {
		defer b.wg.Done()
		if err := b.processLoop(b.gctx); err != nil {
			log.Error("Write buffer processing loop failed", "error", err)
			return err
		}
		return nil
	})

	return nil
}

// processLoop runs the buffer processing loop.
// This method blocks until ctx is cancelled or an error occurs.
func (b *WriteBuffer) processLoop(ctx context.Context) error {
	ticker := time.NewTicker(b.flushInterval)
	defer ticker.Stop()

	batch := make([]*WriteItem, 0, b.batchSize)
	const maxRetries = 3

	for {
		select {
		case <-ctx.Done():
			// Flush remaining items on shutdown with a fresh context.
			if len(batch) > 0 {
				flushCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				err := b.flushBatchWithRetry(flushCtx, batch, maxRetries)
				cancel()
				if err != nil {
					log.Error("Failed to flush final batch", "error", err)
					return errors.Wrap(err, "flush final batch")
				}
			}
			return nil

		case item, ok := <-b.buffer:
			if !ok {
				// Channel closed, flush any remaining batch before exiting.
				if len(batch) > 0 {
					flushCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					err := b.flushBatchWithRetry(flushCtx, batch, maxRetries)
					cancel()
					if err != nil {
						log.Error("Failed to flush remaining batch on channel close", "error", err)
						return errors.Wrap(err, "flush remaining batch on close")
					}
				}
				return nil
			}
			if item == nil {
				return nil
			}
			batch = append(batch, item)
			if len(batch) >= b.batchSize {
				if err := b.flushBatchWithRetry(ctx, batch, maxRetries); err != nil {
					// CRITICAL: do not discard the failed batch. Re-queue its
					// items so they are retried on the next flush instead of
					// being silently dropped (which caused data loss).
					log.Error("Failed to flush batch after retries, re-queuing items",
						"error", err, "batch_size", len(batch))
					b.requeueItems(batch)
					batch = batch[:0]
					continue
				}
				batch = batch[:0]
			}

		case <-ticker.C:
			if len(batch) > 0 {
				if err := b.flushBatchWithRetry(ctx, batch, maxRetries); err != nil {
					// Re-queue items rather than dropping them.
					log.Error("Failed to flush batch on timer after retries, re-queuing items",
						"error", err, "batch_size", len(batch))
					b.requeueItems(batch)
					batch = batch[:0]
					continue
				}
				batch = batch[:0]
			}
		}
	}
}

// requeueItems re-queues items that failed to flush. It attempts a non-blocking
// send back into the buffer channel; if the channel is full or closed, the
// items are logged and dropped as a last resort to avoid blocking the
// processing loop. The stopped flag is checked first so we don't accidentally
// send on a closed channel.
func (b *WriteBuffer) requeueItems(items []*WriteItem) {
	if b.stopped.Load() {
		log.Warn("Write buffer stopped, cannot re-queue items", "count", len(items))
		return
	}
	var dropped int
	for _, item := range items {
		select {
		case b.buffer <- item:
		default:
			// Channel is full; drop the item to avoid blocking. This is a
			// last resort and should be rare because the channel is sized to
			// batchSize*2.
			dropped++
		}
	}
	if dropped > 0 {
		log.Warn("Re-queue dropped items because buffer is full",
			"dropped", dropped, "total", len(items))
	}
}

// flushBatchWithRetry attempts to flush a batch with exponential backoff retries.
// Returns nil on success or the last error after all retries are exhausted.
func (b *WriteBuffer) flushBatchWithRetry(ctx context.Context, batch []*WriteItem, maxRetries int) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 100ms, 200ms, 400ms, ...
			backoff := time.Duration(100<<uint(attempt-1)) * time.Millisecond
			wbTimer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				wbTimer.Stop()
				return ctx.Err()
			case <-wbTimer.C:
			}
			log.Warn("Retrying batch flush", "attempt", attempt, "backoff", backoff)
		}

		if err := b.flushBatch(ctx, batch); err != nil {
			lastErr = err
			log.Error("Flush attempt failed", "attempt", attempt, "error", err)
			continue
		}
		return nil
	}
	return lastErr
}

// Write queues a write operation for batch processing.
// This is non-blocking and returns immediately if the buffer has capacity.
// If the buffer is full, it returns an error instead of spawning a goroutine.
//
// Thread-safety: The stopped flag is checked atomically. The send is guarded
// by b.mu to prevent a race with Stop() closing the channel: Stop() also
// acquires b.mu before closing, so a Write holding (or waiting for) the mutex
// either observes stopped=true before sending, or sends before Stop() closes.
//
// Args:
// ctx - context for cancellation.
// item - write operation to queue.
// Returns error if buffer is stopped, item is invalid, or buffer is full.
func (b *WriteBuffer) Write(ctx context.Context, item *WriteItem) error {
	if item == nil {
		return errors.ErrInvalidArgument
	}

	// Acquire the mutex to serialize with Stop(). This avoids the previous
	// recover()-based panic handling for send-on-closed-channel, which is an
	// anti-pattern. Stop() holds the same mutex while closing the channel, so
	// we cannot race with the close.
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.stopped.Load() {
		return errors.ErrServiceUnavailable
	}

	select {
	case b.buffer <- item:
		return nil
	case <-ctx.Done():
		return errors.ErrServiceUnavailable
	default:
		// Buffer is full. Retry briefly to absorb transient bursts without
		// dropping the item immediately.
		flushTimer := time.NewTimer(100 * time.Millisecond)
		defer flushTimer.Stop()
		select {
		case <-flushTimer.C:
			return errors.ErrServiceUnavailable
		case b.buffer <- item:
			return nil
		case <-ctx.Done():
			return errors.ErrServiceUnavailable
		}
	}
}

// flushBatch writes a batch of items to the database and queues embedding tasks.
// Args:
// ctx - database operation context.
// batch - items to write.
// Returns error if database write or embedding enqueue fails.
func (b *WriteBuffer) flushBatch(ctx context.Context, batch []*WriteItem) error {
	if len(batch) == 0 {
		return nil
	}

	tx, err := b.db.Begin(ctx)
	if err != nil {
		return errors.Wrap(err, "begin transaction")
	}

	committed := false
	defer func() {
		if !committed {
			if rbErr := tx.Rollback(); rbErr != nil {
				log.Error("Failed to rollback transaction", "error", rbErr)
			}
		}
	}()

	// Batch insert into database with content hash deduplication (per design standard).
	// Each insert returns the source row id, which becomes the queue task's
	// TaskID (REVIEW #13 contract: task_id == source row id, so the worker's
	// UpdateEmbedding targets the correct row).
	entityIDs := make([]string, len(batch))
	for i, item := range batch {
		switch item.Table {
		case "knowledge_chunks_1024":
			// Generate content hash for real-time deduplication (per design standard)
			contentHash := b.computeContentHash(item.Content)

			// embedding is left NULL, not a zero vector: a 1024-dimensional
			// all-zero vector is a *valid* vector, so it satisfies
			// `embedding IS NOT NULL`, gets picked up by the partial ivfflat
			// index, and would be returned by SearchByVector with a meaningless
			// distance. NULL is the only value readers can reliably exclude
			// until the worker backfills the real vector.
			err := tx.QueryRowContext(ctx, `
			  INSERT INTO knowledge_chunks_1024
				(tenant_id, content, content_hash, embedding, embedding_model, embedding_version,
				 embedding_status, embedding_queued_at, source_type, metadata, created_at, updated_at)
				VALUES ($1, $2, $3, NULL, $4, $5, 'pending', NOW(), 'memory', $6, NOW(), NOW())
				ON CONFLICT (content_hash) DO UPDATE SET
					access_count = knowledge_chunks_1024.access_count + 1,
					updated_at = NOW()
				RETURNING id
			`, item.TenantID, item.Content, contentHash,
				b.embeddingConfig.DefaultModel, b.embeddingConfig.DefaultVersion, item.Metadata).Scan(&entityIDs[i])
			if err != nil {
				return errors.Wrap(err, "insert knowledge chunk")
			}

		case "experiences_1024":
			// Embed spec metadata into the JSONB metadata field for traceability.
			// This allows detection of embedding drift without schema changes.
			md := item.Metadata
			if md == nil {
				md = make(map[string]interface{})
			}
			if item.SpecKind != "" {
				md["embedding_kind"] = item.SpecKind
				md["embedding_prefix"] = item.SpecPrefix
				md["embedding_text_hash"] = item.SpecHash
				if item.SpecDim > 0 {
					md["embedding_dim"] = item.SpecDim
				}
			}
			// embedding stays NULL until the worker backfills it. See the
			// knowledge_chunks_1024 branch above for why a zero vector is not
			// an acceptable placeholder.
			//
			// Unlike knowledge_chunks_1024, this table has no
			// embedding_status/embedding_queued_at columns: `embedding IS NULL`
			// is itself the pending marker, and that is what Reconcile's
			// experiences pass keys on.
			err := tx.QueryRowContext(ctx, `
			  INSERT INTO experiences_1024
				(tenant_id, type, input, output, embedding, embedding_model, embedding_version,
				 agent_id, metadata, score, success, decay_at, created_at)
				VALUES ($1, 'solution', $2, $3, NULL, $4, $5, 'style-agent', $6, 0.8, true, NOW() + INTERVAL '30 days', NOW())
				RETURNING id
			`, item.TenantID, item.Content, item.Metadata["output"],
				b.embeddingConfig.DefaultModel, b.embeddingConfig.DefaultVersion, md).Scan(&entityIDs[i])
			if err != nil {
				return errors.Wrap(err, "insert experience")
			}

		default:
			return fmt.Errorf("unsupported table type: %s (currently only knowledge_chunks_1024 and experiences_1024 are supported)", item.Table)
		}
	}

	// Enqueue embedding tasks within the same transaction.
	// This ensures atomicity: if the commit fails, both the data writes
	// and the queue entries are rolled back together, preventing orphans.
	// TaskID is the source row id captured via RETURNING above (REVIEW #13:
	// the worker writes the vector back by this id).
	for i, item := range batch {
		task := &EmbeddingTask{
			TaskID:   entityIDs[i],
			Table:    item.Table,
			Content:  item.Content,
			TenantID: item.TenantID,
			Model:    b.embeddingConfig.DefaultModel,
			Version:  b.embeddingConfig.DefaultVersion,
			Kind:     item.SpecKind,
			Prefix:   item.SpecPrefix,
			Dim:      item.SpecDim,
			SpecHash: item.SpecHash,
		}
		if err := b.queue.EnqueueTx(ctx, tx, task); err != nil {
			if stderrors.Is(err, ErrDuplicateTask) {
				// Task already queued; proceed without rolling back.
				log.Debug("Duplicate embedding task, skipping", "table", item.Table)
				continue
			}
			log.Error("Failed to enqueue embedding task, rolling back transaction", "table", item.Table, "error", err)
			return errors.Wrapf(err, "enqueue embedding task for table %s", item.Table)
		}
	}

	// Commit transaction only after all embedding tasks are enqueued successfully
	if err := tx.Commit(); err != nil {
		return errors.Wrap(err, "commit transaction")
	}
	committed = true

	return nil
}

// Stop gracefully shuts down the buffer and flushes remaining items.
// This should be called during application shutdown.
//
// Thread-safety: Uses sync.Once to ensure the channel is closed only once,
// preventing panic from concurrent close operations. The stopped flag is
// checked atomically to avoid unnecessary mutex contention.
//
// Args:
// ctx - context for cancellation.
// Returns error if stopping fails.
func (b *WriteBuffer) Stop(ctx context.Context) error {
	// Check stopped flag atomically first to avoid mutex contention
	if b.stopped.Load() {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	// Double-check stopped flag under lock
	if b.stopped.Load() {
		return nil
	}

	// Use sync.Once to ensure channel is closed only once
	b.closeOnce.Do(func() {
		b.stopped.Store(true)
		close(b.buffer)
	})

	// Wait for any ongoing processing to complete
	b.wg.Wait()

	// Wait for errgroup to complete (ignoring errors as we're shutting down)
	if b.g != nil {
		if err := b.g.Wait(); err != nil {
			log.Warn("write buffer: flush wait", "error", err)
		}
	}

	return nil
}

// computeContentHash computes content hash for deduplication (per design standard).
// This implements real-time hash deduplication as specified in storage-implementation-plan.md.
// Uses SHA256 for strong collision resistance.
func (b *WriteBuffer) computeContentHash(content string) string {
	hash := sha256.Sum256([]byte(content))
	return hex.EncodeToString(hash[:])
}
