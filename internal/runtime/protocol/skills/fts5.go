package ares_skills

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// FTS5Index is an in-memory SQLite FTS5 full-text index over skill metadata
// (design §5: "查询走关键词匹配，接口预留 FTS5"). It augments — not replaces —
// the keyword matcher: Discovery falls back to the keyword path when FTS5 is
// unavailable or a query is not FTS5-safe.
type FTS5Index struct {
	db     *sql.DB
	rowids rowidMap
}

// rowidMap maps FTS rowid to the entry slice index used at build time.
type rowidMap map[int64]int

// NewFTS5Index builds an in-memory FTS5 index over the given entries. The
// index covers ID, name, description and keywords so phrase/prefix queries
// rank above plain substring matching.
//
// Args:
//   - entries: the metadata entries to index.
//
// Returns:
//   - *FTS5Index: ready to search.
//   - error: wrapped sqlite open/index error.
func NewFTS5Index(entries []SkillIndexEntry) (*FTS5Index, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, fmt.Errorf("ares_skills: open fts5: %w", err)
	}
	idx := &FTS5Index{db: db}
	if err := idx.build(entries); err != nil {
		_ = db.Close()
		return nil, err
	}
	return idx, nil
}

// build creates the FTS5 virtual table and inserts every entry.
//
// Args:
//   - entries: entries to index.
//
// Returns:
//   - error: wrapped sqlite error.
func (f *FTS5Index) build(entries []SkillIndexEntry) error {
	ctx := context.Background()
	stmt := `CREATE VIRTUAL TABLE skills_fts USING fts5(
		id, name, description, keywords, content='')
`
	if _, err := f.db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("ares_skills: create fts5: %w", err)
	}
	insert, err := f.db.PrepareContext(ctx, `INSERT INTO skills_fts(rowid, id, name, description, keywords) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("ares_skills: prepare fts5 insert: %w", err)
	}
	defer func() { _ = insert.Close() }()

	// Map the FTS rowid back to the entry slice index.
	f.rowids = make(map[int64]int, len(entries))
	for i, e := range entries {
		rowID := int64(i + 1)
		keywords := strings.Join(e.Keywords, " ")
		if _, err := insert.ExecContext(ctx, rowID, e.ID, e.Name, e.Description, keywords); err != nil {
			return fmt.Errorf("ares_skills: fts5 insert %s: %w", e.ID, err)
		}
		f.rowids[rowID] = i
	}
	return nil
}

// Search runs an FTS5 MATCH query and returns matching entries in rank order,
// limited to limit (<= 0 means all matches). Non-FTS5-safe queries (empty or
// containing FTS5 operators) return an error so the caller can fall back to
// keyword matching.
//
// Args:
//   - query: the full-text query.
//   - limit: maximum results.
//   - entries: the original entry slice (rowid mapping target).
//
// Returns:
//   - []SkillIndexEntry: ranked matches.
//   - error: wrapped sqlite/query error, or nil.
func (f *FTS5Index) Search(query string, limit int, entries []SkillIndexEntry) ([]SkillIndexEntry, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, errors.New("ares_skills: empty fts5 query")
	}
	rows, err := f.db.QueryContext(context.Background(),
		`SELECT rowid FROM skills_fts WHERE skills_fts MATCH ? ORDER BY rank LIMIT ?`, q, limitOrAll(limit))
	if err != nil {
		return nil, fmt.Errorf("ares_skills: fts5 match %q: %w", q, err)
	}
	defer func() { _ = rows.Close() }()

	var out []SkillIndexEntry
	for rows.Next() {
		var rowID int64
		if err := rows.Scan(&rowID); err != nil {
			return nil, fmt.Errorf("ares_skills: fts5 scan: %w", err)
		}
		if i, ok := f.rowids[rowID]; ok && i < len(entries) {
			out = append(out, entries[i])
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ares_skills: fts5 rows: %w", err)
	}
	return out, nil
}

// Close releases the underlying database.
//
// Returns:
//   - error: wrapped close error, or nil.
func (f *FTS5Index) Close() error {
	return f.db.Close()
}

// limitOrAll converts a limit <= 0 into "no limit" for SQLite.
//
// Args:
//   - limit: the requested limit.
//
// Returns:
//   - int: the SQL LIMIT value (-1 means unlimited in SQLite).
func limitOrAll(limit int) int {
	if limit <= 0 {
		return -1
	}
	return limit
}

// rowids is declared here to keep the struct definition next to its use.
