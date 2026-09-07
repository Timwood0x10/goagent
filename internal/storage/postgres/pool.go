package postgres

//nolint: errcheck // best-effort operations: ResponseWriter writes, cleanup Close/Wait, deferred shutdown
import (
	"context"
	"database/sql"
	stderrors "errors"
	"runtime"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/Timwood0x10/ares/internal/errors"
)

// ErrMissingTenantID is returned when a tenant-aware query is called with an
// empty tenant ID. This prevents silent data leaks across tenants when RLS
// policies are enforced via app.tenant_id.
var ErrMissingTenantID = stderrors.New("storage: missing tenant ID")

// Pool represents a database connection pool with "get usage release" pattern.
type Pool struct {
	cfg          *Config
	db           *sql.DB
	mu           sync.RWMutex
	waitCount    int
	waitDuration time.Duration
}

// NewPool creates a new database connection pool.
func NewPool(cfg *Config) (*Pool, error) {
	if err := cfg.Validate(); err != nil {
		return nil, errors.Wrap(err, "invalid config")
	}

	db, err := sql.Open("pgx", cfg.DSN())
	if err != nil {
		return nil, errors.Wrap(err, "failed to open database")
	}

	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)

	if err := db.PingContext(context.Background()); err != nil {
		return nil, errors.Wrap(err, "failed to ping database")
	}

	return &Pool{
		cfg: cfg,
		db:  db,
	}, nil
}

// Get acquires a connection from the pool.
func (p *Pool) Get(ctx context.Context) (*sql.Conn, error) {
	start := time.Now()

	conn, err := p.db.Conn(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get connection")
	}

	p.mu.Lock()
	elapsed := time.Since(start)
	p.waitDuration += elapsed
	if elapsed > time.Second {
		p.waitCount++
	}
	p.mu.Unlock()

	return conn, nil
}

// Release returns a connection to the pool.
func (p *Pool) Release(conn *sql.Conn) {
	if conn == nil {
		return
	}

	if err := conn.Close(); err != nil {
		log.Warn("pool: close connection failed", "error", err)
	}
}

// WithConnection executes a function with a connection from the pool.
// This is the recommended pattern: get usage release.
func (p *Pool) WithConnection(ctx context.Context, fn func(*sql.Conn) error) error {
	conn, err := p.Get(ctx)
	if err != nil {
		return err
	}
	defer p.Release(conn)

	return fn(conn)
}

// Close closes all connections in the pool.
func (p *Pool) Close() error {
	return p.db.Close()
}

// Stats returns connection pool statistics.
func (p *Pool) Stats() *PoolStats {
	stats := p.db.Stats()

	p.mu.RLock()
	defer p.mu.RUnlock()

	return &PoolStats{
		OpenConnections:  stats.OpenConnections,
		InUseConnections: stats.InUse,
		IdleConnections:  stats.Idle,
		WaitCount:        stats.WaitCount + int64(p.waitCount),
		WaitDuration:     stats.WaitDuration + p.waitDuration,
		MaxOpenConns:     p.cfg.MaxOpenConns,
	}
}

// PoolStats holds pool statistics.
type PoolStats struct {
	OpenConnections  int
	InUseConnections int
	IdleConnections  int
	WaitCount        int64
	WaitDuration     time.Duration
	MaxOpenConns     int
}

// IsHealthy checks if the pool is healthy by pinging the database.
func (p *Pool) IsHealthy() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return p.db.PingContext(ctx) == nil
}

// Ping pings the database to check connectivity.
func (p *Pool) Ping(ctx context.Context) error {
	return p.db.PingContext(ctx)
}

// GetDB returns the underlying *sql.DB for repository initialization.
// This is needed for repository constructors that require *sql.DB.
func (p *Pool) GetDB() *sql.DB {
	return p.db
}

// Exec executes a query without returning rows.
func (p *Pool) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.cfg.QueryTimeout)
		defer cancel()
	}

	var result sql.Result
	var execErr error

	if err := p.WithConnection(ctx, func(conn *sql.Conn) error {
		result, execErr = conn.ExecContext(ctx, query, args...)
		return execErr
	}); err != nil {
		return nil, err
	}

	return result, execErr
}

// ExecWithTenant executes a query with a mandatory tenant context on the same
// connection. Begins a transaction, sets tenant_id via set_config (transaction-
// scoped, is_local=true), executes the query, and commits. This ensures RLS
// policies see the correct app.tenant_id and no data leaks across tenants.
// Fails with ErrMissingTenantID if tenantID is empty.
func (p *Pool) ExecWithTenant(ctx context.Context, tenantID string, query string, args ...any) (sql.Result, error) {
	if tenantID == "" {
		return nil, ErrMissingTenantID
	}
	var result sql.Result
	err := p.WithConnection(ctx, func(conn *sql.Conn) error {
		// Begin transaction so set_config is visible to the query.
		tx, txErr := conn.BeginTx(ctx, nil)
		if txErr != nil {
			return errors.Wrap(txErr, "begin transaction")
		}
		defer func() { _ = tx.Rollback() }() // no-op after Commit

		if _, setErr := tx.ExecContext(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantID); setErr != nil {
			return errors.Wrap(setErr, "set tenant context")
		}
		var execErr error
		result, execErr = tx.ExecContext(ctx, query, args...)
		if execErr != nil {
			return execErr
		}
		return tx.Commit()
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// clearTenantContext resets the connection-level tenant context (is_local=false)
// so a pooled connection reused by another tenant cannot carry a stale RLS
// setting. Best-effort: failures are logged, never propagated. A bounded
// context prevents the cleanup from hanging on a broken connection.
func clearTenantContext(conn *sql.Conn) {
	if conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := conn.ExecContext(ctx, "SELECT set_config('app.tenant_id', '', false)"); err != nil {
		log.Warn("pool: clear tenant context failed", "error", err)
	}
}

// QueryWithTenant executes a query with a mandatory tenant context on the same
// connection. Sets tenant_id at connection level (is_local=false) so RLS
// policies are enforced for the query that follows — the previous is_local=true
// variant was transaction-scoped and evaporated under autocommit, silently
// disabling RLS. The connection is held open until ManagedRows.Close(), which
// clears the tenant context before returning the connection to the pool so it
// cannot leak into other tenants' requests.
func (p *Pool) QueryWithTenant(ctx context.Context, tenantID string, query string, args ...any) (*ManagedRows, error) {
	if tenantID == "" {
		return nil, ErrMissingTenantID
	}
	conn, err := p.Get(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "get connection")
	}
	// Set tenant context at connection level (is_local=false) so it survives
	// the autocommit boundary and applies to the subsequent query.
	if _, setErr := conn.ExecContext(ctx, "SELECT set_config('app.tenant_id', $1, false)", tenantID); setErr != nil {
		// Even if set_config itself failed, clear defensively so a partially
		// set tenant cannot leak into the pool.
		clearTenantContext(conn)
		_ = conn.Close()
		return nil, errors.Wrap(setErr, "set tenant context")
	}
	rows, queryErr := conn.QueryContext(ctx, query, args...)
	if queryErr != nil {
		// The tenant context was set successfully; without the clear below the
		// connection would return to the pool still bound to this tenant and
		// leak RLS state into other tenants' requests (the very bug this method
		// exists to prevent).
		clearTenantContext(conn)
		_ = conn.Close()
		return nil, queryErr
	}
	return &ManagedRows{Rows: rows, conn: conn, pool: p, tenant: true}, nil
}

// Query executes a query and returns rows.
// The connection is released when rows are closed.
func (p *Pool) Query(ctx context.Context, query string, args ...any) (*ManagedRows, error) {
	// Add query timeout if not already set in context.
	// The cancel function is stored in ManagedRows and called on Close(),
	// so the context remains valid for the full lifetime of row scanning.
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		ctx, cancel = context.WithTimeout(ctx, p.cfg.QueryTimeout)
	}

	conn, err := p.Get(ctx)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		return nil, err
	}

	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		p.Release(conn)
		if cancel != nil {
			cancel()
		}
		return nil, err
	}

	mr := &ManagedRows{
		Rows:   rows,
		conn:   conn,
		pool:   p,
		cancel: cancel,
	}
	// Set finalizer to release connection if caller forgets to call Close().
	runtime.SetFinalizer(mr, func(m *ManagedRows) {
		if m.conn != nil {
			log.Warn("ManagedRows garbage collected without Close() being called, releasing connection")
			m.pool.Release(m.conn)
			m.conn = nil
		}
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
	})

	return mr, nil
}

// ManagedRows wraps sql.Rows and manages connection lifecycle.
type ManagedRows struct {
	*sql.Rows
	conn   *sql.Conn
	pool   *Pool
	cancel context.CancelFunc
	// tenant marks connections whose tenant context was set at connection
	// level (QueryWithTenant); Close clears it before pooling so the setting
	// cannot leak into other tenants' requests.
	tenant bool
}

// Close closes the rows and releases the connection.
func (m *ManagedRows) Close() error {
	err := m.Rows.Close()
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	if m.conn != nil {
		if m.tenant {
			// Clear the connection-level tenant context before releasing the
			// connection; otherwise a pooled connection reused by another
			// tenant would still carry this tenant's RLS setting.
			clearTenantContext(m.conn)
			m.tenant = false
		}
		m.pool.Release(m.conn)
		m.conn = nil
		runtime.SetFinalizer(m, nil)
	}
	return err
}

// QueryRow executes a query and returns a single row.
// The connection is held until the row is fully consumed by Scan.
// This avoids the data race that would occur if the connection were released
// before the caller finishes reading the row data.
func (p *Pool) QueryRow(ctx context.Context, query string, args ...any) *ManagedRow {
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		ctx, cancel = context.WithTimeout(ctx, p.cfg.QueryTimeout)
	}

	conn, err := p.Get(ctx)
	if err != nil {
		if cancel != nil {
			cancel()
		}
		// Surface the connection-acquisition failure through Scan instead of
		// returning a dummy "SELECT 1 WHERE 1=0" row. The old behaviour masked
		// a real pool error as sql.ErrNoRows, so every caller misread a DB
		// outage as "record not found".
		log.Error("Failed to acquire database connection for QueryRow", "error", err)
		return &ManagedRow{err: err}
	}

	row := conn.QueryRowContext(ctx, query, args...)
	mr := &ManagedRow{Row: row, conn: conn, pool: p, cancel: cancel}
	// Set finalizer to release connection if caller never calls Scan().
	runtime.SetFinalizer(mr, func(m *ManagedRow) {
		if m.conn != nil {
			log.Warn("ManagedRow garbage collected without Scan() being called, releasing connection")
			m.pool.Release(m.conn)
			m.conn = nil
		}
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
	})
	return mr
}

// ManagedRow wraps sql.Row and manages connection lifecycle.
// The caller MUST call Scan to consume the row, which releases the connection.
type ManagedRow struct {
	*sql.Row
	conn   *sql.Conn
	pool   *Pool
	cancel context.CancelFunc
	err    error // set when the pool failed to acquire a connection; surfaced by Scan
}

// Scan scans the row and releases the connection.
// If the connection could not be acquired (err is set), it returns that
// acquisition error immediately so callers never mistake a pool outage for
// sql.ErrNoRows.
func (m *ManagedRow) Scan(dest ...any) error {
	if m.err != nil {
		return m.err
	}
	err := m.Row.Scan(dest...)
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	if m.conn != nil {
		m.pool.Release(m.conn)
		m.conn = nil
		runtime.SetFinalizer(m, nil)
	}
	return err
}

// Begin starts a new transaction.
func (p *Pool) Begin(ctx context.Context) (*sql.Tx, error) {
	return p.db.BeginTx(ctx, nil)
}

// NOTE: This module uses the standard library's database/sql package
// which already implements a connection pool. The Pool wrapper provides
// additional statistics and the "get usage release" pattern.
