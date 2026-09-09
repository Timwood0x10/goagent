package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Timwood0x10/ares/internal/knowledge"
	"github.com/Timwood0x10/ares/internal/knowledge/provider"
)

// failingScanDriver is a database/sql driver whose rows all fail Scan: the
// time column yields an int64, and sql.NullTime.Scan rejects that driver
// value. Every row therefore takes Stream's scan-error path — the exact
// multi-error scenario that used to block the second errCh send forever.
type failingScanDriver struct{}

func (failingScanDriver) Open(string) (driver.Conn, error) { return failingScanConn{}, nil }

type failingScanConn struct{}

func (failingScanConn) Prepare(query string) (driver.Stmt, error) {
	return failingScanStmt{}, nil
}

func (failingScanConn) Close() error              { return nil }
func (failingScanConn) Begin() (driver.Tx, error) { return nil, errors.New("tx unsupported") }

type failingScanStmt struct{}

func (failingScanStmt) Close() error  { return nil }
func (failingScanStmt) NumInput() int { return -1 }

func (failingScanStmt) Exec(args []driver.Value) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}

func (failingScanStmt) Query(args []driver.Value) (driver.Rows, error) {
	return &failingScanRows{remaining: 3}, nil // 3 rows → 3 scan errors
}

type failingScanRows struct {
	remaining int
}

func (failingScanRows) Columns() []string { return []string{"id", "summary", "ts"} }

func (r *failingScanRows) Close() error { return nil }

func (r *failingScanRows) Next(dest []driver.Value) error {
	if r.remaining == 0 {
		return io.EOF
	}
	r.remaining--
	dest[0] = fmt.Sprintf("row-%d", r.remaining)
	dest[1] = "summary"
	// int64 into sql.NullTime: NullTime.Scan rejects it — every row fails.
	dest[2] = int64(r.remaining)
	return nil
}

// newFailingScanProvider builds a PGProvider over the failing-scan driver.
func newFailingScanProvider(t *testing.T) *PGProvider {
	t.Helper()
	db, err := sql.Open("ares-failing-scan", "unused-dsn")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return &PGProvider{
		config: provider.ProviderConfig{
			Name:      "pg-fail",
			Namespace: "ns",
			Table:     "t",
		},
		db: db,
		mapping: provider.ColumnMapping{
			IDColumn:      "id",
			SummaryColumn: "summary",
			TimeColumn:    "ts",
		},
	}
}

// TestPGProvider_Stream_MultipleScanErrors_DoNotDeadlock is the 1.5
// regression: every row failing scan made Stream send one error per row to
// errCh (capacity 1) while the consumer reads exactly one — the second send
// blocked forever, so objCh/errCh were never closed and the deferred
// rows.Close never ran (goroutine + *sql.Rows leak). The fix emits only the
// FIRST error, once, after the loop.
//
// The guard is a timeout: with the bug the test hangs on channel close (and
// fails via the deadline), with the fix both channels close promptly.
func TestPGProvider_Stream_MultipleScanErrors_DoNotDeadlock(t *testing.T) {
	// Registering twice in one process panics; the driver is stateless so a
	// sync.Once-style guard suffices.
	registerFailingScanDriverOnce()

	p := newFailingScanProvider(t)

	done := make(chan struct{})
	var objCount int
	var streamErr error

	go func() {
		defer close(done)
		objCh, errCh := p.Stream(context.Background(), knowledge.Intent{})
		for range objCh { // must terminate: Stream must close objCh
			objCount++
		}
		select {
		case err := <-errCh:
			streamErr = err
		default:
		}
	}()

	select {
	case <-done:
		assert.Zero(t, objCount, "scan failures must not produce objects")
		assert.Error(t, streamErr, "the first scan error must surface exactly once")
		assert.True(t, strings.Contains(streamErr.Error(), "scan"),
			"error should describe the scan failure, got: %v", streamErr)
	case <-time.After(5 * time.Second):
		t.Fatal("Stream deadlocked: channels never closed after multiple scan errors (1.5)")
	}
}

var failingScanDriverOnce sync.Once

func registerFailingScanDriverOnce() {
	failingScanDriverOnce.Do(func() {
		sql.Register("ares-failing-scan", failingScanDriver{})
	})
}

// TestPGProvider_Stream_QueryError_SingleError ensures the pre-query error
// path (buildQuery/QueryContext failure) still sends exactly one error and
// closes both channels — the fix must not regress the single-error case.
func TestPGProvider_Stream_QueryError_SingleError(t *testing.T) {
	registerFailingScanDriverOnce()

	// A provider whose mapping lacks required columns fails at buildQuery,
	// before any rows exist.
	p := &PGProvider{
		config:  provider.ProviderConfig{Name: "pg-bad", Namespace: "ns", Table: "t"},
		db:      nil, // buildQuery fails first; db is never touched
		mapping: provider.ColumnMapping{},
	}

	objCh, errCh := p.Stream(context.Background(), knowledge.Intent{})

	select {
	case err := <-errCh:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "build postgres query")
	case <-time.After(5 * time.Second):
		t.Fatal("error channel never delivered the build error")
	}

	// Both channels must close even on the early-error path.
	timeout := time.After(5 * time.Second)
	for open := 2; open > 0; {
		select {
		case _, ok := <-objCh:
			if !ok {
				open--
				objCh = nil
			}
		case _, ok := <-errCh:
			if !ok {
				open--
				errCh = nil
			}
		case <-timeout:
			t.Fatal("channels never closed after build error")
			return
		}
	}
}
