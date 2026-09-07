// Package evolution — PGStrategyStore: PostgreSQL-backed persistent strategy store.
package evolution

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Timwood0x10/ares/internal/logger"
)

var pgLog = logger.New("pg_strategy_store")

// ErrNoActiveStrategy is returned by GetActive when no strategy has been
// stored yet. Callers can errors.Is(err, ErrNoActiveStrategy) to distinguish
// "empty store" from a real failure.
var ErrNoActiveStrategy = errors.New("pg strategy store: no active strategy")

// PGStrategyStore is a PostgreSQL-backed implementation of StrategyStore.
// It persists strategies to a database table, enabling cross-restart continuity
// of the evolution system's deployed strategies. When no DB is configured, the
// in-memory MemoryStrategyStore is used instead.
type PGStrategyStore struct {
	db         *sql.DB
	tableName  string
	maxHistory int
}

// NewPGStrategyStore creates a PostgreSQL-backed strategy store.
// The table is created automatically if it does not exist.
//
// Args:
//
//	db         - active database connection pool.
//	tableName  - name of the table to store strategies in.
//	maxHistory - maximum history entries per strategy (0 = unlimited).
//
// Returns:
//
//	*PGStrategyStore - the configured store.
//	error - non-nil if table creation fails.
func NewPGStrategyStore(db *sql.DB, tableName string, maxHistory int) (*PGStrategyStore, error) {
	if db == nil {
		return nil, errors.New("pg strategy store: db must not be nil")
	}
	if tableName == "" {
		tableName = "evolution_strategies"
	}

	store := &PGStrategyStore{
		db:         db,
		tableName:  tableName,
		maxHistory: maxHistory,
	}

	if err := store.createTable(context.Background()); err != nil {
		return nil, fmt.Errorf("pg strategy store: create table: %w", err)
	}

	pgLog.Info(context.Background(), "pg strategy store initialized",
		"table", tableName,
		"max_history", maxHistory,
	)
	return store, nil
}

// createTable creates the strategy storage table if it does not exist.
func (s *PGStrategyStore) createTable(ctx context.Context) error {
	//nolint:gosec // G201: tableName is application-controlled, not user input
	query := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %s (
			id          BIGSERIAL PRIMARY KEY,
			strategy_id TEXT NOT NULL,
			version     INTEGER NOT NULL DEFAULT 1,
			name        TEXT NOT NULL DEFAULT '',
			parent_id   TEXT NOT NULL DEFAULT '',
			prompt_template TEXT NOT NULL DEFAULT '',
			mutation_type  TEXT NOT NULL DEFAULT '',
			mutation_desc  TEXT NOT NULL DEFAULT '',
			params      JSONB NOT NULL DEFAULT '{}',
			score       DOUBLE PRECISION NOT NULL DEFAULT -1,
			created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			is_active   BOOLEAN NOT NULL DEFAULT FALSE
		);
		CREATE INDEX IF NOT EXISTS idx_%s_sid ON %s(strategy_id);
		CREATE INDEX IF NOT EXISTS idx_%s_active ON %s(is_active) WHERE is_active = TRUE;
	`, s.tableName, s.tableName, s.tableName, s.tableName, s.tableName)
	_, err := s.db.ExecContext(ctx, query)
	return err
}

// GetActive returns the currently deployed strategy.
// Returns nil (and no error) if no strategy has been stored yet.
func (s *PGStrategyStore) GetActive(ctx context.Context) (*Strategy, error) {
	//nolint:gosec // G201: tableName is application-controlled, not user input
	query := fmt.Sprintf(`
		SELECT strategy_id, version, name, parent_id, prompt_template,
		       mutation_type, mutation_desc, params, score, created_at
		FROM %s
		WHERE is_active = TRUE
		ORDER BY created_at DESC
		LIMIT 1
	`, s.tableName)

	row := s.db.QueryRowContext(ctx, query)
	var (
		strategyID, name, parentID, promptTmpl, mutType, mutDesc string
		version                                                  int
		paramsJSON                                               []byte
		score                                                    float64
		createdAt                                                time.Time
	)
	err := row.Scan(&strategyID, &version, &name, &parentID, &promptTmpl,
		&mutType, &mutDesc, &paramsJSON, &score, &createdAt)
	if err == sql.ErrNoRows {
		return nil, ErrNoActiveStrategy
	}
	if err != nil {
		return nil, fmt.Errorf("pg strategy store: get active: %w", err)
	}

	params := make(map[string]any)
	if len(paramsJSON) > 0 {
		if err := json.Unmarshal(paramsJSON, &params); err != nil {
			return nil, fmt.Errorf("pg strategy store: unmarshal params: %w", err)
		}
	}

	return &Strategy{
		ID:                   strategyID,
		Version:              version,
		Name:                 name,
		Params:               params,
		ParentID:             parentID,
		PromptTemplate:       promptTmpl,
		StrategyMutationType: mutType,
		MutationDesc:         mutDesc,
		Score:                score,
		CreatedAt:            createdAt,
	}, nil
}

// SetActive persists a strategy as the active deployment.
// Marks all existing active strategies as inactive first, then inserts the new one.
func (s *PGStrategyStore) SetActive(ctx context.Context, strategy *Strategy) error {
	if strategy == nil {
		return errors.New("pg strategy store: strategy must not be nil")
	}

	paramsJSON, err := json.Marshal(strategy.Params)
	if err != nil {
		return fmt.Errorf("pg strategy store: marshal params: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("pg strategy store: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Deactivate all existing active strategies.
	//nolint:gosec // G201: tableName is application-controlled, not user input
	deactivateQuery := fmt.Sprintf(`UPDATE %s SET is_active = FALSE WHERE is_active = TRUE`, s.tableName)
	if _, err := tx.ExecContext(ctx, deactivateQuery); err != nil {
		return fmt.Errorf("pg strategy store: deactivate: %w", err)
	}

	// Insert the new active strategy.
	//nolint:gosec // G201: tableName is application-controlled, not user input
	insertQuery := fmt.Sprintf(`
		INSERT INTO %s (strategy_id, version, name, parent_id, prompt_template,
		                mutation_type, mutation_desc, params, score, created_at, is_active)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, TRUE)
	`, s.tableName)
	if _, err := tx.ExecContext(ctx, insertQuery,
		strategy.ID, strategy.Version, strategy.Name, strategy.ParentID,
		strategy.PromptTemplate, strategy.StrategyMutationType, strategy.MutationDesc,
		paramsJSON, strategy.Score, strategy.CreatedAt,
	); err != nil {
		return fmt.Errorf("pg strategy store: insert: %w", err)
	}

	// Prune history if maxHistory is set.
	if s.maxHistory > 0 {
		//nolint:gosec // G201: tableName is application-controlled, not user input
		pruneQuery := fmt.Sprintf(`
			DELETE FROM %s
			WHERE strategy_id = $1
			  AND id NOT IN (
			      SELECT id FROM %s
			      WHERE strategy_id = $1
			      ORDER BY created_at DESC
			      LIMIT $2
			  )
		`, s.tableName, s.tableName)
		if _, err := tx.ExecContext(ctx, pruneQuery, strategy.ID, s.maxHistory); err != nil {
			return fmt.Errorf("pg strategy store: prune: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("pg strategy store: commit: %w", err)
	}

	pgLog.Info(ctx, "pg strategy store: set active",
		"strategy_id", strategy.ID,
		"version", strategy.Version,
		"score", strategy.Score,
	)
	return nil
}

// GetHistory returns the last n strategies for the given strategy ID,
// ordered by version descending (newest first).
func (s *PGStrategyStore) GetHistory(ctx context.Context, id string, n int) ([]*Strategy, error) {
	//nolint:gosec // G201: tableName is application-controlled, not user input
	query := fmt.Sprintf(`
		SELECT strategy_id, version, name, parent_id, prompt_template,
		       mutation_type, mutation_desc, params, score, created_at
		FROM %s
		WHERE strategy_id = $1
		ORDER BY created_at DESC
	`, s.tableName)
	if n > 0 {
		query += fmt.Sprintf(" LIMIT %d", n)
	}

	rows, err := s.db.QueryContext(ctx, query, id)
	if err != nil {
		return nil, fmt.Errorf("pg strategy store: get history: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			pgLog.Warn(ctx, "pg strategy store: close rows", "error", closeErr)
		}
	}()

	var results []*Strategy
	for rows.Next() {
		var (
			strategyID, name, parentID, promptTmpl, mutType, mutDesc string
			version                                                  int
			paramsJSON                                               []byte
			score                                                    float64
			createdAt                                                time.Time
		)
		if err := rows.Scan(&strategyID, &version, &name, &parentID, &promptTmpl,
			&mutType, &mutDesc, &paramsJSON, &score, &createdAt); err != nil {
			return nil, fmt.Errorf("pg strategy store: scan: %w", err)
		}
		params := make(map[string]any)
		if len(paramsJSON) > 0 {
			_ = json.Unmarshal(paramsJSON, &params)
		}
		results = append(results, &Strategy{
			ID:                   strategyID,
			Version:              version,
			Name:                 name,
			Params:               params,
			ParentID:             parentID,
			PromptTemplate:       promptTmpl,
			StrategyMutationType: mutType,
			MutationDesc:         mutDesc,
			Score:                score,
			CreatedAt:            createdAt,
		})
	}
	return results, rows.Err()
}

// Ensure PGStrategyStore implements StrategyStore.
var _ StrategyStore = (*PGStrategyStore)(nil)
