package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/provider"
)

// defaultPGReliability is the Confidence assigned to every postgres-recalled
// object. It is a reliability prior, NOT a query-relevance score: rows read
// from an external Postgres table are assumed moderately reliable as facts
// (they are operational data, not rumours).
const defaultPGReliability = 0.5

// defaultPGRelevance is the Relevance assigned to every postgres-recalled
// object when the table exposes no rank/score column. It is a NEUTRAL PRIOR,
// not a real query-relevance signal: the PG provider does a full-table scan
// with no relevance ranking, so claiming any non-neutral relevance would be
// a lie (no fake constant returns). The neutral 0.5 keeps PG objects
// rankable alongside other providers without pretending to know how well
// they match the query. Actual query-time filtering is delegated to
// collectSnippets' topK + the real Relevance scores produced by other
// providers (vector, memory, code).
const defaultPGRelevance = 0.5

// PGProvider connects to an external PostgreSQL database and streams table rows
// as KnowledgeObjects. Configuration is provided via ProviderConfig.
type PGProvider struct {
	config  provider.ProviderConfig
	db      *sql.DB
	mapping provider.ColumnMapping
}

// validateIdentifier returns an error if name is empty, longer than the
// PostgreSQL identifier limit (63 bytes), or contains characters outside the
// safe set [a-zA-Z0-9_]. Schema-qualified names (containing '.') are rejected
// to prevent bypassing the table allowlist via "evil.public_table" style names.
func validateIdentifier(name string) error {
	if name == "" {
		return errors.New("identifier cannot be empty")
	}
	if len(name) > 63 {
		return fmt.Errorf("identifier too long: %d bytes (max 63)", len(name))
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c < 'a' || c > 'z') &&
			(c < 'A' || c > 'Z') &&
			(c < '0' || c > '9') &&
			c != '_' {
			return fmt.Errorf("identifier %q contains illegal character %q", name, c)
		}
	}
	return nil
}

// NewPGProvider creates a PostgreSQL provider with the given DSN and config.
func NewPGProvider(dsn string, cfg provider.ProviderConfig, mapping provider.ColumnMapping) (*PGProvider, error) {
	if cfg.Name == "" {
		return nil, errors.New("provider name is required")
	}
	if dsn == "" {
		return nil, errors.New("DSN is required")
	}
	if cfg.Table == "" {
		return nil, errors.New("provider config Table is required")
	}
	if err := validateIdentifier(cfg.Table); err != nil {
		return nil, fmt.Errorf("invalid table name %q: %w", cfg.Table, err)
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	db.SetMaxOpenConns(5)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)

	return &PGProvider{
		config:  cfg,
		db:      db,
		mapping: mapping,
	}, nil
}

// Name returns the provider identifier.
func (p *PGProvider) Name() string { return p.config.Name }

// ProviderType returns the backing data source type for query-planning routing.
func (p *PGProvider) ProviderType() provider.ProviderType { return provider.ProviderPostgres }

// Compile-time guard that PGProvider satisfies TypedProvider.
var _ provider.TypedProvider = (*PGProvider)(nil)

// IntentMatch scores based on configured intent tags.
func (p *PGProvider) IntentMatch(intent knowledge.Intent) float64 {
	if len(p.config.IntentTags) == 0 || intent.Goal == "" {
		return 0.3
	}
	goal := strings.ToLower(intent.Goal)
	matches := 0
	for _, tag := range p.config.IntentTags {
		if strings.Contains(goal, strings.ToLower(tag)) {
			matches++
		}
	}
	if matches == 0 {
		return 0.1
	}
	return 0.3 + (float64(matches)/float64(len(p.config.IntentTags)))*0.7
}

// Stream queries the configured table and streams KnowledgeObjects.
func (p *PGProvider) Stream(ctx context.Context, intent knowledge.Intent) (<-chan *knowledge.KnowledgeObject, <-chan error) {
	objCh := make(chan *knowledge.KnowledgeObject, 32)
	errCh := make(chan error, 1)

	// Use errgroup for structured concurrency so the streaming goroutine is
	// ctx-cancelable. The errgroup is not waited on here; callers observe
	// completion via objCh/errCh being closed.
	g, gCtx := errgroup.WithContext(ctx)
	g.Go(func() error {
		defer close(objCh)
		defer close(errCh)

		query, args, err := p.buildQuery(intent)
		if err != nil {
			errCh <- fmt.Errorf("build postgres query: %w", err)
			return nil
		}
		rows, err := p.db.QueryContext(gCtx, query, args...)
		if err != nil {
			errCh <- fmt.Errorf("postgres query: %w", err)
			return nil
		}
		defer func() { _ = rows.Close() }()

		// errCh has capacity 1 and the consumer reads at most one error, so
		// the loop must NEVER send directly: a second scan failure would
		// block forever, leaving objCh/errCh unclosed and rows leaked (the
		// loadAndProcess consumer hangs). Record the first error here and
		// emit it exactly once after the loop.
		var firstErr error
		for rows.Next() {
			obj, err := p.scanRow(rows)
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("scan row: %w", err)
				}
				continue
			}
			if obj == nil {
				continue
			}

			select {
			case objCh <- obj:
			case <-gCtx.Done():
				return nil
			}
		}

		if err := rows.Err(); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("rows iteration: %w", err)
			}
		}
		if firstErr != nil {
			errCh <- firstErr
		}
		return nil
	})

	return objCh, errCh
}

// Close closes the database connection.
func (p *PGProvider) Close() error {
	if p.db != nil {
		return p.db.Close()
	}
	return nil
}

func (p *PGProvider) buildQuery(intent knowledge.Intent) (string, []any, error) {
	maxResults := intent.Scope.MaxObjects
	if maxResults <= 0 {
		maxResults = 100
	}

	if p.config.Table == "" {
		return "", nil, fmt.Errorf("postgres provider %s: config.Table is required", p.config.Name)
	}
	if p.mapping.IDColumn == "" || p.mapping.SummaryColumn == "" {
		return "", nil, fmt.Errorf("postgres provider %s: id_column and summary_column are required", p.config.Name)
	}

	// Quote every identifier to prevent SQL injection via configured column
	// or table names. Identifier quoting follows the PostgreSQL rule: wrap
	// in double quotes and double any embedded double quotes.
	columns := []string{quoteIdentifier(p.mapping.IDColumn), quoteIdentifier(p.mapping.SummaryColumn)}
	if p.mapping.ContentColumn != "" {
		columns = append(columns, quoteIdentifier(p.mapping.ContentColumn))
	}
	if p.mapping.TagColumn != "" {
		columns = append(columns, quoteIdentifier(p.mapping.TagColumn))
	}
	if p.mapping.TimeColumn != "" {
		columns = append(columns, quoteIdentifier(p.mapping.TimeColumn))
	}

	table := quoteIdentifier(p.config.Table)

	var query string
	var args []any
	if p.mapping.TimeColumn != "" {
		orderCol := quoteIdentifier(p.mapping.TimeColumn)
		query = fmt.Sprintf(
			"SELECT %s FROM %s ORDER BY %s DESC NULLS LAST LIMIT $1",
			strings.Join(columns, ", "),
			table,
			orderCol,
		)
	} else {
		query = fmt.Sprintf(
			"SELECT %s FROM %s LIMIT $1",
			strings.Join(columns, ", "),
			table,
		)
	}
	args = []any{maxResults}
	return query, args, nil
}

// quoteIdentifier wraps a PostgreSQL identifier in double quotes and escapes
// any embedded double quotes by doubling them, per the SQL standard. This
// prevents identifier injection when configured column or table names are
// substituted into a query.
func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func (p *PGProvider) scanRow(rows *sql.Rows) (*knowledge.KnowledgeObject, error) {
	var id, summary string
	var content, tagCol sql.NullString
	var timeCol sql.NullTime

	args := []any{&id, &summary}
	if p.mapping.ContentColumn != "" {
		args = append(args, &content)
	}
	if p.mapping.TagColumn != "" {
		args = append(args, &tagCol)
	}
	if p.mapping.TimeColumn != "" {
		args = append(args, &timeCol)
	}

	if err := rows.Scan(args...); err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}

	obj := &knowledge.KnowledgeObject{
		ID:         fmt.Sprintf("%s:%s", p.config.Namespace, id),
		Type:       knowledge.ObjectDocument,
		Namespace:  p.config.Namespace,
		Summary:    summary,
		Confidence: defaultPGReliability,
		// Relevance is the neutral prior documented above: the PG provider
		// does not compute query relevance, so we do not fake one.
		Relevance: defaultPGRelevance,
	}

	if content.Valid {
		obj.Raw = []byte(content.String)
	}
	if timeCol.Valid {
		obj.CreatedAt = timeCol.Time
	}
	// Populate Tags from the configured tag column (e.g. source path).
	if tagCol.Valid && tagCol.String != "" {
		obj.Tags = []string{tagCol.String}
	}

	return obj, nil
}
