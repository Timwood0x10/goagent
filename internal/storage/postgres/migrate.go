package postgres

import (
	"context"

	"github.com/Timwood0x10/ares/internal/errors"
)

// coreMigrationStatements contains the DDL for core application tables.
//
// Note: The storage/vector tables (knowledge_chunks_1024, experiences_1024,
// embedding_queue, embedding_dead_letter, tools, conversations,
// task_results_1024, secrets, distilled_memories) are owned by
// migrate_storage.go (storageMigrations) which defines them with full
// Row-Level Security policies and complete indexes. They must NOT be
// duplicated here to avoid schema drift between the two definitions.
var coreMigrationStatements = []string{
	`CREATE TABLE IF NOT EXISTS user_profiles (
			user_id VARCHAR(255) PRIMARY KEY,
			name VARCHAR(255) NOT NULL,
			gender VARCHAR(50),
			age INTEGER,
			occupation VARCHAR(255),
			style JSONB,
			budget JSONB,
			colors JSONB,
			occasions JSONB,
			body_type VARCHAR(100),
			preferences JSONB,
			created_at TIMESTAMP DEFAULT NOW(),
			updated_at TIMESTAMP DEFAULT NOW()
		)`,

	`CREATE TABLE IF NOT EXISTS sessions (
			session_id VARCHAR(255) PRIMARY KEY,
			user_id VARCHAR(255) NOT NULL,
			input TEXT,
			status VARCHAR(50),
			user_profile JSONB,
			metadata JSONB,
			created_at TIMESTAMP DEFAULT NOW(),
			updated_at TIMESTAMP DEFAULT NOW(),
			expired_at TIMESTAMP
		)`,

	`CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions(user_id)`,
	`CREATE INDEX IF NOT EXISTS idx_sessions_expired_at ON sessions(expired_at)`,

	`CREATE TABLE IF NOT EXISTS recommendations (
			id SERIAL PRIMARY KEY,
			session_id VARCHAR(255) UNIQUE NOT NULL,
			user_id VARCHAR(255) NOT NULL,
			items JSONB,
			reason TEXT,
			total_price DECIMAL(10, 2),
			match_score DECIMAL(5, 2),
			occasion VARCHAR(100),
			season VARCHAR(50),
			feedback JSONB,
			metadata JSONB,
			created_at TIMESTAMP DEFAULT NOW()
		)`,

	`CREATE INDEX IF NOT EXISTS idx_recommendations_user_id ON recommendations(user_id)`,
	`CREATE INDEX IF NOT EXISTS idx_recommendations_created_at ON recommendations(created_at)`,

	`CREATE TABLE IF NOT EXISTS embeddings (
			id VARCHAR(255) PRIMARY KEY,
			table_name VARCHAR(100) NOT NULL,
			embedding VECTOR(1536),
			metadata JSONB,
			created_at TIMESTAMP DEFAULT NOW()
		)`,

	`CREATE INDEX IF NOT EXISTS idx_embeddings_table_name ON embeddings(table_name)`,

	// agent_checkpoints - session checkpoints for agent recovery. Renamed from
	// leader_checkpoints. The DO block migrates
	// existing databases without data loss; on fresh databases all IF EXISTS
	// checks are no-ops. Rename only when the target table is absent — if
	// agent_checkpoints already exists (idempotent re-run), the old table's
	// data was already migrated and a rename would collide.
	`DO $$ BEGIN
		IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'leader_checkpoints')
			AND NOT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'agent_checkpoints') THEN
			ALTER TABLE leader_checkpoints RENAME TO agent_checkpoints;
		END IF;
		IF EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'agent_checkpoints') THEN
			IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'agent_checkpoints' AND column_name = 'leader_id') THEN
				ALTER TABLE agent_checkpoints RENAME COLUMN leader_id TO agent_id;
			END IF;
			IF EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'idx_leader_checkpoints_status') THEN
				ALTER INDEX idx_leader_checkpoints_status RENAME TO idx_agent_checkpoints_status;
			END IF;
		END IF;
	END $$;`,

	`CREATE TABLE IF NOT EXISTS agent_checkpoints (
			agent_id VARCHAR(255) NOT NULL,
			session_id VARCHAR(255) NOT NULL,
			status VARCHAR(50) NOT NULL DEFAULT 'active',
			metadata JSONB DEFAULT '{}'::jsonb,
			updated_at TIMESTAMP DEFAULT NOW(),
			PRIMARY KEY (agent_id)
		)`,

	`CREATE INDEX IF NOT EXISTS idx_agent_checkpoints_status ON agent_checkpoints(status)`,

	// events - Event sourcing store with optimistic concurrency control.
	`CREATE TABLE IF NOT EXISTS events (
			id VARCHAR(255) NOT NULL,
			stream_id VARCHAR(255) NOT NULL,
			type VARCHAR(100) NOT NULL,
			payload JSONB NOT NULL,
			metadata JSONB DEFAULT '{}',
			version BIGINT NOT NULL,
			created_at TIMESTAMP DEFAULT NOW(),
			PRIMARY KEY (id)
		)`,

	`CREATE UNIQUE INDEX IF NOT EXISTS uq_events_stream_version ON events(stream_id, version)`,
	`CREATE INDEX IF NOT EXISTS idx_events_type ON events(type)`,

	// event_summaries - Compacted event summaries stored in relational DB (not vector DB).
	// Each summary represents a window of events that have been compacted/summarized
	// for long-running agent tasks. Bound to agent, task, and user request context.
	`CREATE TABLE IF NOT EXISTS event_summaries (
			id VARCHAR(255) PRIMARY KEY,
			stream_id VARCHAR(255) NOT NULL,
			agent_id VARCHAR(255) NOT NULL,
			task_id VARCHAR(255),
			session_id VARCHAR(255),
			user_id VARCHAR(255),
			summary_text TEXT NOT NULL,
			event_count INTEGER NOT NULL DEFAULT 0,
			start_version BIGINT NOT NULL,
			end_version BIGINT NOT NULL,
			start_time TIMESTAMP NOT NULL,
			end_time TIMESTAMP NOT NULL,
			event_type_counts JSONB DEFAULT '{}'::jsonb,
			tasks_created JSONB DEFAULT '[]'::jsonb,
			tools_called JSONB DEFAULT '[]'::jsonb,
			errors JSONB DEFAULT '[]'::jsonb,
			request_summary TEXT,
			outcome VARCHAR(50) NOT NULL DEFAULT 'active',
			metadata JSONB DEFAULT '{}'::jsonb,
			created_at TIMESTAMP DEFAULT NOW()
		)`,

	`CREATE INDEX IF NOT EXISTS idx_event_summaries_stream ON event_summaries(stream_id)`,
	`CREATE INDEX IF NOT EXISTS idx_event_summaries_agent ON event_summaries(agent_id)`,
	`CREATE INDEX IF NOT EXISTS idx_event_summaries_agent_task ON event_summaries(agent_id, task_id)`,
	`CREATE INDEX IF NOT EXISTS idx_event_summaries_created ON event_summaries(created_at)`,

	// Evolution strategies — persisted state for autonomous evolution system.
	`CREATE TABLE IF NOT EXISTS evolution_strategies (
			id VARCHAR(255) PRIMARY KEY,
			is_active BOOLEAN NOT NULL DEFAULT false,
			name VARCHAR(255) NOT NULL DEFAULT '',
			version INTEGER NOT NULL DEFAULT 1,
			params JSONB DEFAULT '{}'::jsonb,
			parent_id VARCHAR(255) DEFAULT '',
			prompt_template TEXT DEFAULT '',
			strategy_mutation_type VARCHAR(100) DEFAULT '',
			mutation_desc TEXT DEFAULT '',
			score DOUBLE PRECISION DEFAULT -1,
			created_at TIMESTAMP DEFAULT NOW(),
			updated_at TIMESTAMP DEFAULT NOW()
		)`,

	`CREATE INDEX IF NOT EXISTS idx_evolution_strategies_active ON evolution_strategies(is_active)`,
	`CREATE INDEX IF NOT EXISTS idx_evolution_strategies_score ON evolution_strategies(score)`,

	// Rollback events — audit trail for strategy rollback decisions.
	`CREATE TABLE IF NOT EXISTS evolution_rollback_events (
			id SERIAL PRIMARY KEY,
			strategy_id VARCHAR(255) NOT NULL,
			previous_strategy_id VARCHAR(255) DEFAULT '',
			reason TEXT NOT NULL DEFAULT '',
			decision JSONB DEFAULT '{}'::jsonb,
			current_score DOUBLE PRECISION DEFAULT 0,
			reference_score DOUBLE PRECISION DEFAULT 0,
			degradation DOUBLE PRECISION DEFAULT 0,
			threshold DOUBLE PRECISION DEFAULT 0,
			recommended_action TEXT DEFAULT '',
			created_at TIMESTAMP DEFAULT NOW()
		)`,

	`CREATE INDEX IF NOT EXISTS idx_rollback_events_strategy ON evolution_rollback_events(strategy_id)`,
	`CREATE INDEX IF NOT EXISTS idx_rollback_events_created ON evolution_rollback_events(created_at)`,
}

// Migrate runs database migrations.
func Migrate(ctx context.Context, pool *Pool) error {
	for i, migration := range coreMigrationStatements {
		if _, err := pool.Exec(ctx, migration); err != nil {
			return errors.Wrapf(err, "migration %d failed", i)
		}
	}
	return nil
}

// RollbackLast rolls back the last migration.
// The migration list is a flat set of idempotent DDL statements without a
// version table, so a precise rollback is not possible. It validates the
// pool and returns a clear error instead of pretending success.
// TODO: introduce a schema_migrations version table to enable real rollback
// (expected by 2026-12-31).
func RollbackLast(ctx context.Context, pool *Pool) error {
	if pool == nil {
		return errors.Wrap(errors.ErrNilPointer, "rollback last migration")
	}
	return errors.Wrap(errors.ErrRollbackUnsupported, "rollback last migration")
}

// Seed creates seed data for testing.
// It inserts a sample user profile idempotently so tests and demo setups
// have baseline data without requiring an external fixture.
func Seed(ctx context.Context, pool *Pool) error {
	if pool == nil {
		return errors.Wrap(errors.ErrNilPointer, "seed data")
	}
	query := `
		INSERT INTO user_profiles (user_id, name, created_at, updated_at)
		VALUES ('seed-user', 'Seed User', NOW(), NOW())
		ON CONFLICT (user_id) DO NOTHING
	`
	if _, err := pool.Exec(ctx, query); err != nil {
		return errors.Wrap(err, "seed user profile")
	}
	return nil
}
