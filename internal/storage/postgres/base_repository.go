package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/Timwood0x10/ares/internal/errors"
	storage_models "github.com/Timwood0x10/ares/internal/storage/postgres/models"
)

// Scannable is satisfied by *sql.Row and *sql.Rows.
type Scannable interface {
	Scan(dest ...interface{}) error
}

// allowedTables is the whitelist of table names that may be substituted into
// dynamic SQL. Adding a table here is required before passing it to
// GetByID/DeleteByID/CountByTenant. This prevents SQL injection through
// attacker-controlled table names.
var (
	// userProfilesTable is the canonical name for the user_profiles table.
	userProfilesTable = "user_profiles"
)

var allowedTables = map[string]struct{}{
	storage_models.KnowledgeChunksTable: {},
	storage_models.ExperiencesTable:     {},
	"embeddings":                        {},
	"recommendations":                   {},
	"sessions":                          {},
	userProfilesTable:                   {},
	"secrets":                           {},
	"embedding_queue":                   {},
	"embedding_dead_letter":             {},
	"tasks":                             {},
	storage_models.TaskResultsTable:     {},
	"tools":                             {},
	"strategies":                        {},
	"conversations":                     {},
	"agent_checkpoints":                 {},
}

// quoteIdentifier quotes a SQL identifier (table/column name) for safe
// interpolation into a query. It mirrors lib/pq's QuoteIdentifier: doubles
// any embedded double quotes and wraps the result in double quotes.
// Callers MUST still verify the identifier is on the whitelist before
// quoting, since quoting alone does not prevent abuse of reserved words or
// schema-qualified names.
func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// validateTable checks that table is on the allowedTables whitelist.
// Returns the quoted identifier on success, or an error if the table is
// not allowed.
func validateTable(table string) (string, error) {
	if table == "" {
		return "", errors.ErrInvalidArgument
	}
	if _, ok := allowedTables[table]; !ok {
		return "", fmt.Errorf("table %q is not in the allowed whitelist: %w", table, errors.ErrInvalidArgument)
	}
	return quoteIdentifier(table), nil
}

// GetByID retrieves a single entity by its primary key.
// The table must be on the allowedTables whitelist. When tenantID is
// non-empty, the result is additionally scoped to that tenant to enforce
// row-level isolation.
func GetByID[T any](ctx context.Context, db DBTX, table, id, tenantID string, scan func(Scannable) (*T, error)) (*T, error) {
	quotedTable, err := validateTable(table)
	if err != nil {
		return nil, errors.Wrap(err, "validate table name")
	}
	if id == "" {
		return nil, errors.ErrInvalidArgument
	}

	query := fmt.Sprintf("SELECT * FROM %s WHERE id = $1", quotedTable)
	args := []any{id}
	if tenantID != "" {
		query += " AND tenant_id = $2"
		args = append(args, tenantID)
	}

	row := db.QueryRowContext(ctx, query, args...)
	entity, err := scan(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, errors.ErrRecordNotFound
		}
		return nil, errors.Wrap(err, "get by id")
	}
	return entity, nil
}

// DeleteByID deletes a single entity by its primary key.
// The table must be on the allowedTables whitelist. When tenantID is
// non-empty, deletion is scoped to that tenant so one tenant cannot delete
// another tenant's row.
// Returns ErrRecordNotFound if no row was deleted.
func DeleteByID(ctx context.Context, db DBTX, table, id, tenantID string) error {
	quotedTable, err := validateTable(table)
	if err != nil {
		return errors.Wrap(err, "validate table name")
	}
	if id == "" {
		return errors.ErrInvalidArgument
	}

	query := fmt.Sprintf("DELETE FROM %s WHERE id = $1", quotedTable)
	args := []any{id}
	if tenantID != "" {
		query += " AND tenant_id = $2"
		args = append(args, tenantID)
	}

	result, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		return errors.Wrap(err, "delete by id")
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return errors.Wrap(err, "get rows affected")
	}
	if rows == 0 {
		return errors.ErrRecordNotFound
	}
	return nil
}

// CountByTenant counts entities for a given tenant.
// The table must be on the allowedTables whitelist.
func CountByTenant(ctx context.Context, db DBTX, table, tenantID string) (int64, error) {
	quotedTable, err := validateTable(table)
	if err != nil {
		return 0, errors.Wrap(err, "validate table name")
	}
	if tenantID == "" {
		return 0, errors.ErrInvalidArgument
	}

	query := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE tenant_id = $1", quotedTable)
	var count int64
	err = db.QueryRowContext(ctx, query, tenantID).Scan(&count)
	if err != nil {
		return 0, errors.Wrap(err, "count by tenant")
	}
	return count, nil
}
