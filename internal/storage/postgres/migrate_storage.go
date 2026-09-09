// Package postgres provides PostgreSQL database operations for the storage system.
package postgres

import (
	"context"

	"github.com/Timwood0x10/ares/internal/errors"
	storage_models "github.com/Timwood0x10/ares/internal/storage/postgres/models"
)

// storageMigrations contains the DDL statements for the vector-based storage schema.
// Each entry is executed as a single SQL statement in order.
var storageMigrations = []string{
	// 1. knowledge_chunks_1024 table - RAG knowledge base with fixed 1024 dimensions
	`CREATE TABLE IF NOT EXISTS knowledge_chunks_1024 (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id TEXT NOT NULL,
			content TEXT NOT NULL,
			embedding VECTOR(1024),
			embedding_model TEXT NOT NULL DEFAULT 'intfloat/e5-large',
			embedding_version INT NOT NULL DEFAULT 1,
			embedding_status TEXT DEFAULT 'completed',
			embedding_queued_at TIMESTAMP,
			embedding_processed_at TIMESTAMP,
			embedding_error TEXT,
			tsv TSVECTOR,
			source_type VARCHAR(50),
			source TEXT,
			metadata JSONB DEFAULT '{}'::jsonb,
			document_id UUID,
			chunk_index INTEGER,
			content_hash TEXT UNIQUE,
			access_count INTEGER DEFAULT 0,
			created_at TIMESTAMP DEFAULT NOW(),
			updated_at TIMESTAMP DEFAULT NOW()
		)`,

	// Enable RLS for knowledge_chunks_1024
	`ALTER TABLE knowledge_chunks_1024 ENABLE ROW LEVEL SECURITY`,

	// Create tenant isolation policy
	`DROP POLICY IF EXISTS tenant_isolation_knowledge_1024 ON knowledge_chunks_1024`,
	`CREATE POLICY tenant_isolation_knowledge_1024 ON knowledge_chunks_1024
		USING (tenant_id = current_setting('app.tenant_id', true))`,

	// Create auto-update trigger for tsv
	`DROP TRIGGER IF EXISTS tsvector_update_knowledge_1024 ON knowledge_chunks_1024`,
	`CREATE TRIGGER tsvector_update_knowledge_1024 BEFORE INSERT OR UPDATE ON knowledge_chunks_1024
		FOR EACH ROW EXECUTE FUNCTION
		tsvector_update_trigger(tsv, 'pg_catalog.simple', content)`,

	// Create indexes for knowledge_chunks_1024.
	//
	// The vector index is PARTIAL on `embedding IS NOT NULL` to match the
	// predicate SearchByVector uses (the column is nullable because the async
	// embedding worker backfills it). ivfflat never indexes NULL rows anyway, so
	// the partial form does not change what is stored; it declares the predicate
	// so the planner can prove the query filter is implied and still use the
	// index. Databases migrated before this change keep the non-partial index,
	// which remains correct — the searches simply lose that proof.
	`CREATE INDEX IF NOT EXISTS idx_knowledge_1024_embedding 
		ON knowledge_chunks_1024 
		USING ivfflat (embedding vector_cosine_ops) 
		WITH (lists = 100)
		WHERE embedding IS NOT NULL`,

	`CREATE INDEX IF NOT EXISTS idx_knowledge_1024_tsv 
		ON knowledge_chunks_1024 
		USING GIN(tsv)`,

	`CREATE INDEX IF NOT EXISTS idx_knowledge_1024_doc_chunk 
		ON knowledge_chunks_1024(document_id, chunk_index)`,

	`CREATE INDEX IF NOT EXISTS idx_knowledge_1024_source_type 
		ON knowledge_chunks_1024(source_type)`,

	`CREATE INDEX IF NOT EXISTS idx_knowledge_1024_tenant 
		ON knowledge_chunks_1024(tenant_id)`,

	`CREATE INDEX IF NOT EXISTS idx_knowledge_1024_content_hash 
		ON knowledge_chunks_1024(content_hash)`,

	// 2. experiences_1024 table - Agent experiences with decay mechanism
	`CREATE TABLE IF NOT EXISTS experiences_1024 (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id TEXT NOT NULL,
			type VARCHAR(50) NOT NULL CHECK (type IN ('success', 'failure', 'query', 'solution', 'pattern', 'distilled')),
			input TEXT,
			output TEXT,
			embedding VECTOR(1024),
			embedding_model TEXT NOT NULL DEFAULT 'intfloat/e5-large',
			embedding_version INT NOT NULL DEFAULT 1,
			score FLOAT DEFAULT 0.5 CHECK (score >= 0 AND score <= 1),
			success BOOLEAN DEFAULT true,
			agent_id VARCHAR(255),
			metadata JSONB DEFAULT '{}'::jsonb,
			decay_at TIMESTAMP DEFAULT NOW() + INTERVAL '30 days',
			created_at TIMESTAMP DEFAULT NOW(),
			updated_at TIMESTAMP DEFAULT NOW(),
			usage_count INTEGER DEFAULT 0
		)`,

	`ALTER TABLE experiences_1024 ENABLE ROW LEVEL SECURITY`,

	`DROP POLICY IF EXISTS tenant_isolation_experiences_1024 ON experiences_1024`,
	`CREATE POLICY tenant_isolation_experiences_1024 ON experiences_1024
		USING (tenant_id = current_setting('app.tenant_id', true))`,

	// Create indexes for experiences_1024.
	// Partial on `embedding IS NOT NULL` for the same reason as the knowledge
	// index above: it mirrors SearchByVector's predicate. This column is the one
	// that is actually left NULL in production (distillation inserts the row
	// before the vector exists).
	`CREATE INDEX IF NOT EXISTS idx_experiences_1024_embedding 
		ON experiences_1024 
		USING ivfflat (embedding vector_cosine_ops) 
		WITH (lists = 100)
		WHERE embedding IS NOT NULL`,

	`CREATE INDEX IF NOT EXISTS idx_experiences_1024_type 
		ON experiences_1024(type)`,

	`CREATE INDEX IF NOT EXISTS idx_experiences_1024_agent 
		ON experiences_1024(agent_id)`,

	`CREATE INDEX IF NOT EXISTS idx_experiences_1024_score 
		ON experiences_1024(score DESC)`,

	`CREATE INDEX IF NOT EXISTS idx_experiences_1024_tenant 
		ON experiences_1024(tenant_id)`,

	`CREATE INDEX IF NOT EXISTS idx_experiences_1024_decay 
		ON experiences_1024(decay_at) WHERE decay_at IS NOT NULL`,

	// experiences_1024.embedding is nullable (NOT NULL relaxed here) so
	// distillation can persist an experience row first and let the async
	// embedding worker backfill the vector afterwards. Readers must therefore
	// filter NULL vectors explicitly: "NULL is not indexed by ivfflat" only
	// holds for index scans, a sequential scan still returns those rows.
	//
	// The guard exists because DROP NOT NULL takes an ACCESS EXCLUSIVE lock even
	// when the column is already nullable, and MigrateStorage runs on every
	// `db migrate`. Skipping the no-op keeps repeated migrations lock-free.
	`DO $$
		BEGIN
			IF EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_name = 'experiences_1024'
				  AND column_name = 'embedding'
				  AND is_nullable = 'NO'
			) THEN
				ALTER TABLE experiences_1024 ALTER COLUMN embedding DROP NOT NULL;
			END IF;
		END
	$$`,

	// Legacy repair, kept from the pre-nullable schema: the old synchronous
	// write path could leave zero-dimension vectors behind, and pgvector raises
	// "different vector dimensions" as soon as such a row is compared against a
	// real 1024-dim query vector, which takes down vector search for the whole
	// tenant.
	//
	// Two guards keep this from costing anything on a healthy database:
	//
	//   - atttypmod = -1 means the column is a dimension-less VECTOR, the only
	//     shape that can hold a row of the wrong width. A declared VECTOR(1024)
	//     rejects other dimensions on write, so the scan is skipped entirely.
	//   - vector_dims() is used instead of the original `embedding = '[]'`: that
	//     comparison coerces the literal to a 0-dim vector and raises the very
	//     dimension mismatch it was meant to clean up.
	`DO $$
		BEGIN
			IF EXISTS (
				SELECT 1 FROM pg_attribute
				WHERE attrelid = 'experiences_1024'::regclass
				  AND attname = 'embedding'
				  AND atttypmod = -1
			) THEN
				UPDATE experiences_1024
				SET embedding = NULL
				WHERE embedding IS NOT NULL
				  AND vector_dims(embedding) <> 1024;
			END IF;
		END
	$$`,

	// 3. tools table - Tools with semantic embedding
	`CREATE TABLE IF NOT EXISTS tools (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id TEXT NOT NULL,
			name VARCHAR(255) NOT NULL,
			description TEXT,
			embedding VECTOR(1024) NOT NULL,
			embedding_model TEXT NOT NULL DEFAULT 'intfloat/e5-large',
			embedding_version INT NOT NULL DEFAULT 1,
			agent_type VARCHAR(50),
			tags TEXT[] DEFAULT ARRAY[]::TEXT[],
			usage_count INTEGER DEFAULT 0,
			success_rate FLOAT DEFAULT 0.0,
			last_used_at TIMESTAMP,
			metadata JSONB DEFAULT '{}'::jsonb,
			created_at TIMESTAMP DEFAULT NOW(),
			UNIQUE (tenant_id, name)
		)`,

	`ALTER TABLE tools ENABLE ROW LEVEL SECURITY`,

	`DROP POLICY IF EXISTS tenant_isolation_tools ON tools`,
	`CREATE POLICY tenant_isolation_tools ON tools
		USING (tenant_id = current_setting('app.tenant_id', true))`,

	// Create indexes for tools
	`CREATE INDEX IF NOT EXISTS idx_tools_tenant_name 
		ON tools(tenant_id, name)`,

	`CREATE INDEX IF NOT EXISTS idx_tools_usage_count 
		ON tools(usage_count DESC)`,

	`CREATE INDEX IF NOT EXISTS idx_tools_agent_type 
		ON tools(agent_type)`,

	`CREATE INDEX IF NOT EXISTS idx_tools_tags 
		ON tools USING GIN(tags)`,

	`CREATE INDEX IF NOT EXISTS idx_tools_embedding 
		ON tools 
		USING ivfflat (embedding vector_cosine_ops) 
		WHERE embedding IS NOT NULL`,

	// 4. conversations table - Conversation history without vector embedding
	`CREATE TABLE IF NOT EXISTS conversations (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			session_id VARCHAR(64) NOT NULL,
			tenant_id TEXT NOT NULL,
			user_id VARCHAR(64),
			agent_id VARCHAR(64),
			role VARCHAR(32) NOT NULL,
			content TEXT NOT NULL,
			metadata JSONB DEFAULT '{}'::jsonb,
			expires_at TIMESTAMP,
			created_at TIMESTAMP DEFAULT NOW()
		)`,

	`ALTER TABLE conversations ENABLE ROW LEVEL SECURITY`,

	`DROP POLICY IF EXISTS tenant_isolation_conversations ON conversations`,
	`CREATE POLICY tenant_isolation_conversations ON conversations
		USING (tenant_id = current_setting('app.tenant_id', true))`,

	// Create indexes for conversations
	`CREATE INDEX IF NOT EXISTS idx_conversations_session 
		ON conversations(session_id, created_at)`,

	`CREATE INDEX IF NOT EXISTS idx_conversations_tenant 
		ON conversations(tenant_id)`,

	`CREATE INDEX IF NOT EXISTS idx_conversations_user 
		ON conversations(user_id, created_at)`,

	`CREATE INDEX IF NOT EXISTS idx_conversations_agent 
		ON conversations(agent_id, created_at)`,

	`CREATE INDEX IF NOT EXISTS idx_conversations_expires 
		ON conversations(expires_at) WHERE expires_at IS NOT NULL`,

	// 5. task_results_1024 table - Task execution results with vector embedding.
	// Table name is sourced from storage_models.TaskResultsTable so the
	// dimension suffix stays in sync across all files.
	`CREATE TABLE IF NOT EXISTS ` + storage_models.TaskResultsTable + ` (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id TEXT NOT NULL,
			session_id VARCHAR(64) NOT NULL,
			task_type VARCHAR(64),
			agent_id VARCHAR(64),
			input JSONB NOT NULL,
			output JSONB,
			embedding VECTOR(1024),
			embedding_model TEXT NOT NULL DEFAULT 'intfloat/e5-large',
			embedding_version INT NOT NULL DEFAULT 1,
			status VARCHAR(32) NOT NULL DEFAULT 'pending',
			error TEXT,
			latency_ms INTEGER,
			metadata JSONB DEFAULT '{}'::jsonb,
			created_at TIMESTAMP DEFAULT NOW()
		)`,

	`ALTER TABLE ` + storage_models.TaskResultsTable + ` ENABLE ROW LEVEL SECURITY`,

	`DROP POLICY IF EXISTS tenant_isolation_task_results_1024 ON ` + storage_models.TaskResultsTable,
	`CREATE POLICY tenant_isolation_task_results_1024 ON ` + storage_models.TaskResultsTable + `
		USING (tenant_id = current_setting('app.tenant_id', true))`,

	// Create indexes for task_results_1024
	`CREATE INDEX IF NOT EXISTS idx_task_results_1024_embedding
		ON ` + storage_models.TaskResultsTable + `
		USING ivfflat (embedding vector_cosine_ops)
		WHERE embedding IS NOT NULL`,

	`CREATE INDEX IF NOT EXISTS idx_task_results_1024_type
		ON ` + storage_models.TaskResultsTable + `(task_type)`,

	`CREATE INDEX IF NOT EXISTS idx_task_results_1024_status
		ON ` + storage_models.TaskResultsTable + `(status)`,

	`CREATE INDEX IF NOT EXISTS idx_task_results_1024_session
		ON ` + storage_models.TaskResultsTable + `(session_id)`,

	`CREATE INDEX IF NOT EXISTS idx_task_results_1024_agent
		ON ` + storage_models.TaskResultsTable + `(agent_id)`,

	`CREATE INDEX IF NOT EXISTS idx_task_results_1024_tenant
		ON ` + storage_models.TaskResultsTable + `(tenant_id)`,

	// 6. secrets table - Encrypted sensitive data
	`CREATE TABLE IF NOT EXISTS secrets (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			tenant_id TEXT NOT NULL,
			key VARCHAR(255) NOT NULL,
			value BYTEA NOT NULL,
			key_version INTEGER NOT NULL DEFAULT 1,
			algorithm VARCHAR(32) NOT NULL DEFAULT 'aes-gcm',
			expires_at TIMESTAMP,
			metadata JSONB DEFAULT '{}'::jsonb,
			created_at TIMESTAMP DEFAULT NOW(),
			updated_at TIMESTAMP DEFAULT NOW(),
			UNIQUE (tenant_id, key)
		)`,

	`ALTER TABLE secrets ENABLE ROW LEVEL SECURITY`,

	`DROP POLICY IF EXISTS tenant_isolation_secrets ON secrets`,
	`CREATE POLICY tenant_isolation_secrets ON secrets
		USING (tenant_id = current_setting('app.tenant_id', true))`,

	// Create indexes for secrets
	`CREATE INDEX IF NOT EXISTS idx_secrets_tenant_key 
		ON secrets(tenant_id, key)`,

	`CREATE INDEX IF NOT EXISTS idx_secrets_tenant 
		ON secrets(tenant_id)`,

	`CREATE INDEX IF NOT EXISTS idx_secrets_expires 
		ON secrets(expires_at) WHERE expires_at IS NOT NULL`,

	`CREATE INDEX IF NOT EXISTS idx_secrets_key_version 
		ON secrets(key_version)`,

	// 7. embedding_queue table - Async embedding task queue with idempotency
	`CREATE TABLE IF NOT EXISTS embedding_queue (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			task_id TEXT NOT NULL,
			table_name TEXT NOT NULL,
			content TEXT NOT NULL,
			tenant_id TEXT NOT NULL,
			embedding_model TEXT DEFAULT 'e5-large',
			embedding_version INT DEFAULT 1,
			dedupe_key TEXT UNIQUE,
			retry_count INTEGER DEFAULT 0,
			status TEXT DEFAULT 'pending',
			queued_at TIMESTAMP DEFAULT NOW(),
			processing_at TIMESTAMP,
			completed_at TIMESTAMP,
			error_message TEXT
		)`,

	// Create indexes for embedding_queue
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_embedding_queue_dedupe ON embedding_queue(dedupe_key)`,

	`CREATE INDEX IF NOT EXISTS idx_embedding_queue_status ON embedding_queue(status, queued_at) 
		WHERE status IN ('pending', 'processing')`,

	// 8. embedding_dead_letter table - Failed embedding tasks
	`CREATE TABLE IF NOT EXISTS embedding_dead_letter (
			id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
			task_id TEXT NOT NULL,
			table_name TEXT NOT NULL,
			content TEXT NOT NULL,
			tenant_id TEXT NOT NULL,
			embedding_model TEXT,
			embedding_version INT,
			error_message TEXT,
			retry_count INTEGER,
			created_at TIMESTAMP DEFAULT NOW()
		)`,

	// Create indexes for embedding_dead_letter
	`CREATE INDEX IF NOT EXISTS idx_embedding_dead_letter_tenant ON embedding_dead_letter(tenant_id)`,
	`CREATE INDEX IF NOT EXISTS idx_embedding_dead_letter_created ON embedding_dead_letter(created_at)`,

	// Reconcile probes this table once per orphan-scan to skip rows that were
	// already given up on; without the index that NOT EXISTS degrades to a
	// sequential scan on every reconcile tick.
	`CREATE INDEX IF NOT EXISTS idx_embedding_dead_letter_task 
		ON embedding_dead_letter(task_id, table_name)`,

	// 9-12. distilled_memories table family (table, RLS, indexes,
	// content_hash, dedup index, updated_at) — REMOVED (RUNTIME.md §8-A4):
	// the repository and tools that read/wrote it were deleted as a schema
	// ghost (zero production constructors), so fresh deployments must not
	// create the table. Existing databases keep their table untouched
	// (CREATE TABLE IF NOT EXISTS removal is inert for them); dropping it
	// is a data decision for operators, not a schema migration.
}

// MigrateStorage runs the storage system database migrations.
func MigrateStorage(ctx context.Context, pool *Pool) error {
	for _, migration := range storageMigrations {
		if _, err := pool.db.ExecContext(ctx, migration); err != nil {
			return errors.Wrap(err, "migration failed")
		}
	}
	return nil
}
