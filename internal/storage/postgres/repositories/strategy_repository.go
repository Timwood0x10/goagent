package repositories

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/Timwood0x10/ares/internal/errors"
	"github.com/Timwood0x10/ares/internal/storage/postgres"
)

// StrategyRow is a database row representation of an evolution strategy.
type StrategyRow struct {
	ID                   string
	Name                 string
	Version              int
	Params               map[string]any
	ParentID             string
	PromptTemplate       string
	StrategyMutationType string
	MutationDesc         string
	Score                float64
	CreatedAt            time.Time
	IsActive             bool
}

// StrategyRepository provides Postgres persistence for evolution strategies.
type StrategyRepository struct {
	db postgres.DBTX
}

// NewStrategyRepository creates a new StrategyRepository.
//
// Args:
//
//	db - database connection or transaction implementing DBTX interface.
//
// Returns:
//
//	*StrategyRepository - the configured repository instance.
func NewStrategyRepository(db postgres.DBTX) *StrategyRepository {
	return &StrategyRepository{db: db}
}

// GetActive returns the currently active strategy.
// Returns nil if no strategy is marked as active.
//
// Args:
//
//	ctx - database operation context.
//
// Returns:
//
//	*StrategyRow - the active strategy row, or nil.
//	error - non-nil if query fails.
func (r *StrategyRepository) GetActive(ctx context.Context) (*StrategyRow, error) {
	query := `SELECT id, name, version, params, parent_id, prompt_template,
		strategy_mutation_type, mutation_desc, score, created_at
		FROM evolution_strategies WHERE is_active = true
		ORDER BY version DESC LIMIT 1`

	row := r.db.QueryRowContext(ctx, query)

	var (
		id, name, parentID, promptTmpl, mutationType, mutationDesc string
		version                                                    int
		score                                                      float64
		createdAt                                                  time.Time
		paramsJSON                                                 []byte
	)

	err := row.Scan(&id, &name, &version, &paramsJSON, &parentID,
		&promptTmpl, &mutationType, &mutationDesc, &score, &createdAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, errors.ErrNotFound
		}
		return nil, errors.Wrap(err, "get active strategy")
	}

	params := make(map[string]any)
	if len(paramsJSON) > 0 {
		if err := json.Unmarshal(paramsJSON, &params); err != nil {
			return nil, errors.Wrap(err, "unmarshal params")
		}
	}

	return &StrategyRow{
		ID:                   id,
		Name:                 name,
		Version:              version,
		Params:               params,
		ParentID:             parentID,
		PromptTemplate:       promptTmpl,
		StrategyMutationType: mutationType,
		MutationDesc:         mutationDesc,
		Score:                score,
		CreatedAt:            createdAt,
	}, nil
}

// beginTxer abstracts transaction creation for *sql.DB.
// *sql.Tx and other DBTX implementations fall back to no-tx path.
type beginTxer interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// SetActive persists a strategy and marks it as active.
// Any previously active strategy is deactivated.
//
// Args:
//
//	ctx - database operation context.
//	s - the strategy to persist.
//
// Returns:
//
//	error - non-nil if insert or update fails.
func (r *StrategyRepository) SetActive(ctx context.Context, s StrategyRow) error {
	paramsJSON, err := json.Marshal(s.Params)
	if err != nil {
		return errors.Wrap(err, "marshal params")
	}

	if btx, ok := r.db.(beginTxer); ok {
		return r.setActiveTx(ctx, btx, s, paramsJSON)
	}

	return r.setActiveNoTx(ctx, s, paramsJSON)
}

func (r *StrategyRepository) setActiveTx(ctx context.Context, db beginTxer, s StrategyRow, paramsJSON []byte) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return errors.Wrap(err, "begin tx")
	}
	committed := false
	defer func() {
		if !committed {
			if err := tx.Rollback(); err != nil {
				log.Warn("strategy repo: rollback", "error", err)
			}
		}
	}()

	deactivateQ := `UPDATE evolution_strategies SET is_active = false WHERE is_active = true`
	// RowsAffected is intentionally ignored: 0 affected rows means no
	// previously active strategy (normal on first deployment).
	if _, err := tx.ExecContext(ctx, deactivateQ); err != nil {
		return errors.Wrap(err, "deactivate strategies")
	}

	insertQ := `INSERT INTO evolution_strategies
		(id, is_active, name, version, params, parent_id, prompt_template,
		 strategy_mutation_type, mutation_desc, score, created_at, updated_at)
		VALUES ($1, true, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW())`

	now := time.Now()
	createdAt := s.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}

	if _, err = tx.ExecContext(ctx, insertQ,
		s.ID, s.Name, s.Version, paramsJSON,
		s.ParentID, s.PromptTemplate,
		s.StrategyMutationType, s.MutationDesc,
		s.Score, createdAt,
	); err != nil {
		return errors.Wrap(err, "insert strategy")
	}

	committed = true
	return errors.Wrap(tx.Commit(), "commit tx")
}

func (r *StrategyRepository) setActiveNoTx(ctx context.Context, s StrategyRow, paramsJSON []byte) error {
	deactivateQ := `UPDATE evolution_strategies SET is_active = false WHERE is_active = true`
	if _, err := r.db.ExecContext(ctx, deactivateQ); err != nil {
		return errors.Wrap(err, "deactivate strategies")
	}

	insertQ := `INSERT INTO evolution_strategies
		(id, is_active, name, version, params, parent_id, prompt_template,
		 strategy_mutation_type, mutation_desc, score, created_at, updated_at)
		VALUES ($1, true, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW())`

	now := time.Now()
	createdAt := s.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}

	_, err := r.db.ExecContext(ctx, insertQ,
		s.ID, s.Name, s.Version, paramsJSON,
		s.ParentID, s.PromptTemplate,
		s.StrategyMutationType, s.MutationDesc,
		s.Score, createdAt,
	)
	return errors.Wrap(err, "insert strategy")
}

// List returns the last n strategies ordered by version descending.
//
// Args:
//
//	ctx - database operation context.
//	n - maximum number of strategies to return.
//
// Returns:
//
//	[]StrategyRow - the strategy list (never nil).
//	error - non-nil if query fails.
func (r *StrategyRepository) List(ctx context.Context, n int) ([]StrategyRow, error) {
	query := `SELECT id, name, version, params, parent_id, prompt_template,
		strategy_mutation_type, mutation_desc, score, created_at
		FROM evolution_strategies ORDER BY version DESC LIMIT $1`

	rows, err := r.db.QueryContext(ctx, query, n)
	if err != nil {
		return nil, errors.Wrap(err, "list strategies")
	}
	defer func() {
		if err := rows.Close(); err != nil {
			log.Warn("strategy repo: close rows", "error", err)
		}
	}()

	var strategies []StrategyRow
	for rows.Next() {
		var (
			id, name, parentID, promptTmpl, mutationType, mutationDesc string
			version                                                    int
			score                                                      float64
			createdAt                                                  time.Time
			paramsJSON                                                 []byte
		)

		if err := rows.Scan(&id, &name, &version, &paramsJSON, &parentID,
			&promptTmpl, &mutationType, &mutationDesc, &score, &createdAt); err != nil {
			return nil, errors.Wrap(err, "scan strategy")
		}

		params := make(map[string]any)
		if len(paramsJSON) > 0 {
			if err := json.Unmarshal(paramsJSON, &params); err != nil {
				return nil, errors.Wrap(err, "unmarshal params")
			}
		}

		strategies = append(strategies, StrategyRow{
			ID:                   id,
			Name:                 name,
			Version:              version,
			Params:               params,
			ParentID:             parentID,
			PromptTemplate:       promptTmpl,
			StrategyMutationType: mutationType,
			MutationDesc:         mutationDesc,
			Score:                score,
			CreatedAt:            createdAt,
		})
	}

	if strategies == nil {
		strategies = make([]StrategyRow, 0)
	}

	return strategies, rows.Err()
}
