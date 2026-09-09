// Command mock-db provides a local sqlite mock database for tests that need a
// storage table without a PostgreSQL instance. It creates a sqlite file with a
// single mock table (historically mirroring the distilled_memories shape: id,
// tenant, content, embedding, memory_type, created_at — that table's
// repository was removed as a schema ghost, but the mock keeps the generic
// column shape), inserts a sample row, and prints the row back — a
// self-contained smoke test for the storage layer.
//
// Usage:
//
//	mock-db [--db=./ares-mock.db] [--reset]
//
// The command is intentionally standalone (no PostgreSQL, no pgvector): it
// exists so tests and demos can exercise the storage path on a plain local
// file. For the real schema, use `ares db migrate`.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

// mockSchema is the DDL for the mock table. It mirrors the essential shape of
// distilled_memories but is sqlite-flavored: embedding is a BLOB instead of a
// pgvector column, and created_at uses sqlite's CURRENT_TIMESTAMP.
const mockSchema = `
CREATE TABLE IF NOT EXISTS mock_memories (
	id          TEXT PRIMARY KEY,
	tenant_id   TEXT NOT NULL,
	user_id     TEXT,
	content     TEXT NOT NULL,
	embedding   BLOB,
	memory_type TEXT NOT NULL DEFAULT 'profile',
	created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);`

// sampleRow is the fixed demo row the command inserts (idempotent via
// INSERT OR IGNORE) so a fresh database always has data to query.
const sampleRow = `INSERT OR IGNORE INTO mock_memories
	(id, tenant_id, user_id, content, embedding, memory_type)
	VALUES ('mock-1', 'tenant-demo', 'user-demo', 'Hello from the mock database', X'3D0AD7A3', 'profile')`

func main() {
	dbPath := flag.String("db", "ares-mock.db", "path to the sqlite database file")
	reset := flag.Bool("reset", false, "drop the existing database file before creating the table")
	flag.Parse()

	if err := run(*dbPath, *reset); err != nil {
		fmt.Fprintf(os.Stderr, "mock-db: %v\n", err)
		os.Exit(1)
	}
}

// run creates the sqlite mock database at dbPath, inserts the sample row, and
// prints every row in the mock table. reset removes an existing file first so
// the table is rebuilt from scratch.
//
// Args:
//   - dbPath: sqlite database file path.
//   - reset: drop the existing file before creating the table.
//
// Returns:
//   - error: any open / DDL / insert / query failure, wrapped with context.
func run(dbPath string, reset bool) error {
	ctx := context.Background()

	if reset {
		if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", dbPath, err)
		}
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", dbPath, err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "mock-db: close: %v\n", err)
		}
	}()

	if _, err := db.ExecContext(ctx, mockSchema); err != nil {
		return fmt.Errorf("create table: %w", err)
	}
	if _, err := db.ExecContext(ctx, sampleRow); err != nil {
		return fmt.Errorf("insert sample row: %w", err)
	}

	rows, err := db.QueryContext(ctx,
		`SELECT id, tenant_id, user_id, content, length(embedding), memory_type
		   FROM mock_memories ORDER BY id`)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	defer func() {
		if err := rows.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "mock-db: close rows: %v\n", err)
		}
	}()

	fmt.Printf("mock-db: database ready at %s\n", dbPath)
	for rows.Next() {
		var id, tenantID, memoryType string
		var userID sql.NullString
		var content string
		var embLen int
		if err := rows.Scan(&id, &tenantID, &userID, &content, &embLen, &memoryType); err != nil {
			return fmt.Errorf("scan row: %w", err)
		}
		fmt.Printf("  row: id=%s tenant=%s user=%v content=%q embedding_bytes=%d type=%s\n",
			id, tenantID, userID, content, embLen, memoryType)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate rows: %w", err)
	}
	return nil
}
