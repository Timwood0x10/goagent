package sqlitestore

//nolint: errcheck // best-effort operations: ResponseWriter writes, cleanup Close/Wait, deferred shutdown
import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Timwood0x10/ares/internal/knowledge"
)

var (
	// ErrObjectNotFound is returned when a Get call finds no matching object.
	ErrObjectNotFound = errors.New("object not found")
)

// Store is a SQLite-backed KnowledgeStore.
type Store struct {
	db *sql.DB
}

// New creates a new SQLite KnowledgeStore with the given database path.
func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", dbPath, err)
	}
	db.SetMaxOpenConns(1) // SQLite only supports single-writer
	db.SetMaxIdleConns(1)

	s := &Store{db: db}
	if err := s.initTables(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init tables: %w", err)
	}
	return s, nil
}

// NewWithDB creates a new SQLite KnowledgeStore with an existing db connection.
func NewWithDB(db *sql.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("db is nil")
	}
	s := &Store{db: db}
	if err := s.initTables(context.Background()); err != nil {
		return nil, fmt.Errorf("init tables: %w", err)
	}
	return s, nil
}

func (s *Store) initTables(ctx context.Context) error {
	// SQLite disables foreign key enforcement by default; without this
	// pragma, ON DELETE CASCADE is decorative.
	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("enable foreign_keys: %w", err)
	}
	queries := []string{
		`CREATE TABLE IF NOT EXISTS akf_objects (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL DEFAULT '',
			namespace TEXT NOT NULL DEFAULT '',
			raw BLOB,
			normalized TEXT NOT NULL DEFAULT '',
			summary TEXT NOT NULL DEFAULT '',
			metadata TEXT DEFAULT '{}',
			tags TEXT DEFAULT '',
			confidence REAL NOT NULL DEFAULT 1.0,
			version INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT '',
			quality TEXT DEFAULT '',
			relations TEXT DEFAULT '',
			embedding_model TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS akf_representations (
			id TEXT PRIMARY KEY,
			object_id TEXT NOT NULL REFERENCES akf_objects(id) ON DELETE CASCADE,
			model TEXT NOT NULL,
			dimension INTEGER NOT NULL DEFAULT 0,
			vector BLOB,
			metadata TEXT DEFAULT '{}',
			created_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_akf_objects_type ON akf_objects(type)`,
		`CREATE INDEX IF NOT EXISTS idx_akf_objects_namespace ON akf_objects(namespace)`,
		`CREATE INDEX IF NOT EXISTS idx_akf_objects_status ON akf_objects(status)`,
		`CREATE INDEX IF NOT EXISTS idx_akf_repr_obj_model ON akf_representations(object_id, model)`,
	}

	for _, q := range queries {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}

	// Migrate pre-0.2.9 databases by adding the new columns. SQLite returns a
	// "duplicate column name" error when the column already exists; ignore it.
	migrations := []string{
		`ALTER TABLE akf_objects ADD COLUMN status TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE akf_objects ADD COLUMN quality TEXT DEFAULT ''`,
		`ALTER TABLE akf_objects ADD COLUMN relations TEXT DEFAULT ''`,
		`ALTER TABLE akf_objects ADD COLUMN embedding_model TEXT NOT NULL DEFAULT ''`,
	}
	for _, m := range migrations {
		if _, err := s.db.ExecContext(ctx, m); err != nil {
			// SQLite driver returns a plain text error (no sentinel) for an
			// ALTER TABLE that adds an existing column, so errors.Is cannot
			// match it; the driver-text check is isolated in
			// isDuplicateColumnError instead of being scattered inline.
			if !isDuplicateColumnError(err) {
				return fmt.Errorf("migrate akf_objects: %w", err)
			}
		}
	}
	return nil
}

// isDuplicateColumnError reports whether a SQLite migration error is a duplicate-column skip.
func isDuplicateColumnError(err error) bool {
	return strings.Contains(err.Error(), "duplicate column")
}

// Save upserts the given knowledge objects.
func (s *Store) Save(ctx context.Context, objects ...*knowledge.KnowledgeObject) error {
	for _, obj := range objects {
		if obj.ID == "" {
			return errors.New("knowledge object ID cannot be empty")
		}

		metaJSON, _ := json.Marshal(obj.Metadata)
		tags := strings.Join(obj.Tags, ",")
		qualityJSON := marshalQuality(obj.Quality)
		relationsJSON := marshalRelations(obj.Relations)
		now := time.Now().UTC().Format(time.RFC3339)

		_, err := s.db.ExecContext(ctx, `
			INSERT INTO akf_objects (id, type, namespace, raw, normalized, summary, metadata, tags, confidence, version, created_at, updated_at, status, quality, relations, embedding_model)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (id) DO UPDATE SET
				type = excluded.type,
				namespace = excluded.namespace,
				raw = excluded.raw,
				normalized = excluded.normalized,
				summary = excluded.summary,
				metadata = excluded.metadata,
				tags = excluded.tags,
				confidence = excluded.confidence,
				version = akf_objects.version + 1,
				updated_at = excluded.updated_at,
				status = excluded.status,
				quality = excluded.quality,
				relations = excluded.relations,
				embedding_model = excluded.embedding_model
		`, obj.ID, string(obj.Type), obj.Namespace, obj.Raw, obj.Normalized, obj.Summary,
			string(metaJSON), tags, obj.Confidence, obj.Version,
			obj.CreatedAt.UTC().Format(time.RFC3339), now,
			string(obj.Status), qualityJSON, relationsJSON, obj.EmbeddingModel)
		if err != nil {
			return fmt.Errorf("save %q: %w", obj.ID, err)
		}
	}
	return nil
}

// marshalQuality returns the JSON encoding of q, or "" when q is nil.
// Marshal errors are logged as warnings and return "" so data is not silently lost.
func marshalQuality(q *knowledge.Quality) string {
	if q == nil {
		return ""
	}
	b, err := json.Marshal(q)
	if err != nil {
		slog.Warn("marshal quality failed", "error", err)
		return ""
	}
	return string(b)
}

// marshalRelations returns the JSON encoding of rels, or "" when empty.
// Marshal errors are logged as warnings and return "" so data is not silently lost.
func marshalRelations(rels []knowledge.Relation) string {
	if len(rels) == 0 {
		return ""
	}
	b, err := json.Marshal(rels)
	if err != nil {
		slog.Warn("marshal relations failed", "error", err)
		return ""
	}
	return string(b)
}

func (s *Store) Get(ctx context.Context, id string) (*knowledge.KnowledgeObject, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, type, namespace, raw, normalized, summary, metadata, tags, confidence, version, created_at, updated_at, status, quality, relations, embedding_model
		FROM akf_objects WHERE id = ?`, id)

	obj, err := scanObject(row)
	if err == sql.ErrNoRows {
		return nil, ErrObjectNotFound
	}
	if err != nil {
		return nil, err
	}
	return obj, nil
}

func (s *Store) Query(ctx context.Context, q knowledge.Query) ([]*knowledge.KnowledgeObject, error) {
	var conditions []string
	var args []interface{}

	if q.Namespace != "" {
		conditions = append(conditions, "namespace = ?")
		args = append(args, q.Namespace)
	}
	if len(q.Types) > 0 {
		placeholders := make([]string, len(q.Types))
		for i, t := range q.Types {
			placeholders[i] = "?"
			args = append(args, string(t))
		}
		conditions = append(conditions, fmt.Sprintf("type IN (%s)", strings.Join(placeholders, ",")))
	}
	if len(q.Tags) > 0 {
		tagConditions := make([]string, len(q.Tags))
		for i, tag := range q.Tags {
			tagConditions[i] = "tags LIKE ?"
			args = append(args, "%"+tag+"%")
		}
		conditions = append(conditions, "("+strings.Join(tagConditions, " OR ")+")")
	}

	query := "SELECT id, type, namespace, raw, normalized, summary, metadata, tags, confidence, version, created_at, updated_at, status, quality, relations, embedding_model FROM akf_objects"
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ") //nolint:gosec // conditions use ? placeholders, values are parameterized
	}
	query += " ORDER BY created_at DESC"

	if q.Limit > 0 {
		query += " LIMIT ?"
		args = append(args, q.Limit)
	}
	if q.Offset > 0 {
		query += " OFFSET ?"
		args = append(args, q.Offset)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var results []*knowledge.KnowledgeObject
	for rows.Next() {
		obj, err := scanObject(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, obj)
	}

	return results, rows.Err()
}

func (s *Store) Delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM akf_objects WHERE id = ?", id)
	return err
}

func (s *Store) Search(ctx context.Context, text string, _ string, limit int) ([]*knowledge.KnowledgeObject, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, type, namespace, raw, normalized, summary, metadata, tags, confidence, version, created_at, updated_at, status, quality, relations, embedding_model
		FROM akf_objects
		WHERE normalized LIKE ? OR summary LIKE ?
		ORDER BY created_at DESC
		LIMIT ?`, "%"+text+"%", "%"+text+"%", limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var results []*knowledge.KnowledgeObject
	for rows.Next() {
		obj, err := scanObject(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, obj)
	}

	return results, rows.Err()
}

func (s *Store) SaveRepresentation(ctx context.Context, rep *knowledge.Representation) error {
	if rep.ID == "" {
		return errors.New("representation ID cannot be empty")
	}
	metaJSON, _ := json.Marshal(rep.Metadata)
	now := time.Now().UTC().Format(time.RFC3339)

	// Serialize vector as JSON array.
	vecJSON, _ := json.Marshal(rep.Vector)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO akf_representations (id, object_id, model, dimension, vector, metadata, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			model = excluded.model,
			dimension = excluded.dimension,
			vector = excluded.vector,
			metadata = excluded.metadata
	`, rep.ID, rep.ObjectID, rep.Model, rep.Dimension, string(vecJSON), string(metaJSON), now)
	return err
}

func (s *Store) GetRepresentation(ctx context.Context, objectID string, model string) (*knowledge.Representation, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, object_id, model, dimension, vector, metadata, created_at
		FROM akf_representations WHERE object_id = ? AND model = ?`, objectID, model)

	var rep knowledge.Representation
	var metaJSON, vecJSON, createdAtStr string

	err := row.Scan(&rep.ID, &rep.ObjectID, &rep.Model, &rep.Dimension, &vecJSON, &metaJSON, &createdAtStr)
	if err == sql.ErrNoRows {
		return nil, ErrObjectNotFound
	}
	if err != nil {
		return nil, err
	}

	_ = json.Unmarshal([]byte(vecJSON), &rep.Vector)
	_ = json.Unmarshal([]byte(metaJSON), &rep.Metadata)
	rep.CreatedAt = parseTimeField(createdAtStr, "representation", rep.ID)
	return &rep, nil
}

// parseTimeField decodes a persisted RFC3339 timestamp column. The write
// path always stores time.Time.Format(time.RFC3339), so a parse failure
// means the row was corrupted or hand-written — keep the zero time (same
// degrade contract as the best-effort JSON unmarshals) but log the raw
// string so the corrupt row is observable, not silently blank.
func parseTimeField(raw, what, id string) time.Time {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		slog.Warn("sqlite store: corrupt timestamp column, keeping zero time",
			"what", what, "id", id, "raw", raw, "error", err)
		return time.Time{}
	}
	return t
}

// scanObject scans a row into a KnowledgeObject.
type scanner interface {
	Scan(dest ...interface{}) error
}

func scanObject(row scanner) (*knowledge.KnowledgeObject, error) {
	var obj knowledge.KnowledgeObject
	var typeStr, ns, norm, summary string
	var raw []byte
	var metaJSON, tagsStr, createdAtStr, updatedAtStr string
	var statusStr, qualityJSON, relationsJSON, embeddingModel string

	if err := row.Scan(&obj.ID, &typeStr, &ns, &raw, &norm, &summary, &metaJSON, &tagsStr,
		&obj.Confidence, &obj.Version, &createdAtStr, &updatedAtStr,
		&statusStr, &qualityJSON, &relationsJSON, &embeddingModel); err != nil {
		return nil, err
	}

	obj.Type = knowledge.ObjectType(typeStr)
	obj.Namespace = ns
	obj.Normalized = norm
	obj.Summary = summary
	obj.Raw = raw
	if tagsStr != "" {
		obj.Tags = strings.Split(tagsStr, ",")
	}
	obj.CreatedAt = parseTimeField(createdAtStr, "object", obj.ID)
	obj.UpdatedAt = parseTimeField(updatedAtStr, "object", obj.ID)
	obj.Status = knowledge.ObjectStatus(statusStr)
	obj.EmbeddingModel = embeddingModel
	// Unmarshal quality/relations best-effort: malformed JSON is ignored.
	if qualityJSON != "" && qualityJSON != "{}" {
		var q knowledge.Quality
		if err := json.Unmarshal([]byte(qualityJSON), &q); err == nil {
			obj.Quality = &q
		}
	}
	if relationsJSON != "" && relationsJSON != "null" {
		_ = json.Unmarshal([]byte(relationsJSON), &obj.Relations)
	}
	_ = json.Unmarshal([]byte(metaJSON), &obj.Metadata)

	return &obj, nil
}

// HybridSearch performs vector + lexical scoring over SQLite-stored objects.
func (s *Store) HybridSearch(ctx context.Context, req knowledge.HybridSearchRequest) ([]knowledge.ScoredObject, error) {
	conditions, args := hybridConditions(req)
	//nolint:gosec // conditions are static WHERE fragments; values use ? placeholders.
	query := `SELECT id, type, namespace, raw, normalized, summary, metadata, tags, confidence, version, created_at, updated_at, status, quality, relations, embedding_model
		FROM akf_objects` + conditions
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("hybrid search query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var candidates []*knowledge.KnowledgeObject
	var ids []string
	for rows.Next() {
		obj, err := scanObject(rows)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, obj)
		ids = append(ids, obj.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Load representations for the requested model into a map keyed by object ID.
	reps := make(map[string]*knowledge.Representation, len(ids))
	if len(ids) > 0 && req.Model != "" {
		placeholders := make([]string, len(ids))
		repArgs := make([]interface{}, 0, len(ids)+1)
		for i, id := range ids {
			placeholders[i] = "?"
			repArgs = append(repArgs, id)
		}
		repArgs = append(repArgs, req.Model)
		//nolint:gosec // placeholders are ?, ids are local object IDs
		repQuery := fmt.Sprintf(`SELECT id, object_id, model, dimension, vector, metadata, created_at FROM akf_representations WHERE object_id IN (%s) AND model = ?`, strings.Join(placeholders, ","))
		repRows, err := s.db.QueryContext(ctx, repQuery, repArgs...)
		if err != nil {
			return nil, fmt.Errorf("hybrid search reps: %w", err)
		}
		for repRows.Next() {
			rep, err := scanRepresentation(repRows)
			if err != nil {
				_ = repRows.Close()
				return nil, err
			}
			reps[rep.ObjectID] = rep
		}
		_ = repRows.Close()
		if err := repRows.Err(); err != nil {
			return nil, err
		}
	}

	scored := knowledge.ScoreHybrid(candidates, reps, req.QueryVector, req.Query)

	// Filter by MinScore.
	if req.MinScore > 0 {
		filtered := scored[:0]
		for _, r := range scored {
			if r.FinalScore >= req.MinScore {
				filtered = append(filtered, r)
			}
		}
		scored = filtered
	}

	// Sort by FinalScore descending.
	sort.SliceStable(scored, func(i, j int) bool {
		return scored[i].FinalScore > scored[j].FinalScore
	})

	topK := req.TopK
	if topK <= 0 {
		topK = 20
	}
	if len(scored) > topK {
		scored = scored[:topK]
	}
	finalK := req.FinalK
	if finalK <= 0 {
		finalK = 5
	}
	if len(scored) > finalK {
		scored = scored[:finalK]
	}
	return scored, nil
}

// hybridConditions builds the WHERE clause (with parameterized placeholders)
// and args for HybridSearch candidates based on namespace, types, and status
// filter. Empty status on a row matches the active filter for back-compat.
func hybridConditions(req knowledge.HybridSearchRequest) (string, []interface{}) {
	var conditions []string
	var args []interface{}
	if req.Namespace != "" {
		conditions = append(conditions, "namespace = ?")
		args = append(args, req.Namespace)
	}
	if len(req.Types) > 0 {
		placeholders := make([]string, len(req.Types))
		for i, t := range req.Types {
			placeholders[i] = "?"
			args = append(args, string(t))
		}
		conditions = append(conditions, fmt.Sprintf("type IN (%s)", strings.Join(placeholders, ",")))
	}
	statuses := req.StatusFilter
	if len(statuses) == 0 {
		statuses = []knowledge.ObjectStatus{knowledge.StatusActive}
	}
	var statusConds []string
	for _, st := range statuses {
		if st == knowledge.StatusActive {
			// Backward compat: empty status is treated as active.
			statusConds = append(statusConds, "status = ''")
			statusConds = append(statusConds, "status = ?")
			args = append(args, string(st))
		} else {
			statusConds = append(statusConds, "status = ?")
			args = append(args, string(st))
		}
	}
	conditions = append(conditions, "("+strings.Join(statusConds, " OR ")+")")
	return " WHERE " + strings.Join(conditions, " AND "), args
}

// scanRepresentation scans a representation row.
func scanRepresentation(row scanner) (*knowledge.Representation, error) {
	var rep knowledge.Representation
	var metaJSON, vecJSON, createdAtStr string
	if err := row.Scan(&rep.ID, &rep.ObjectID, &rep.Model, &rep.Dimension, &vecJSON, &metaJSON, &createdAtStr); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(vecJSON), &rep.Vector)
	_ = json.Unmarshal([]byte(metaJSON), &rep.Metadata)
	rep.CreatedAt = parseTimeField(createdAtStr, "representation", rep.ID)
	return &rep, nil
}

// ListByStatus returns objects in ns matching the given status.
// Empty status matches objects with no status (backward compatibility).
func (s *Store) ListByStatus(ctx context.Context, ns string, status knowledge.ObjectStatus, limit int) ([]*knowledge.KnowledgeObject, error) {
	var conditions []string
	var args []interface{}
	if ns != "" {
		conditions = append(conditions, "namespace = ?")
		args = append(args, ns)
	}
	if status == knowledge.StatusActive {
		// Backward compat: empty status is treated as active.
		conditions = append(conditions, "(status = '' OR status = ?)")
		args = append(args, string(status))
	} else {
		conditions = append(conditions, "status = ?")
		args = append(args, string(status))
	}
	//nolint:gosec // conditions are static WHERE fragments; values use ? placeholders.
	query := `SELECT id, type, namespace, raw, normalized, summary, metadata, tags, confidence, version, created_at, updated_at, status, quality, relations, embedding_model
		FROM akf_objects WHERE ` + strings.Join(conditions, " AND ") + " ORDER BY created_at DESC"
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list by status: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var results []*knowledge.KnowledgeObject
	for rows.Next() {
		obj, err := scanObject(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, obj)
	}
	return results, rows.Err()
}

// UpdateStatus transitions an object's lifecycle status.
func (s *Store) UpdateStatus(ctx context.Context, id string, status knowledge.ObjectStatus) error {
	res, err := s.db.ExecContext(ctx, "UPDATE akf_objects SET status = ?, updated_at = ? WHERE id = ?",
		string(status), time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		return fmt.Errorf("update status %q: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrObjectNotFound
	}
	return nil
}

// Promote moves a candidate to active and records its computed Quality.
func (s *Store) Promote(ctx context.Context, id string, q *knowledge.Quality) error {
	qualityJSON := marshalQuality(q)
	res, err := s.db.ExecContext(ctx, "UPDATE akf_objects SET status = ?, quality = ?, updated_at = ? WHERE id = ?",
		string(knowledge.StatusActive), qualityJSON, time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		return fmt.Errorf("promote %q: %w", id, err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrObjectNotFound
	}
	return nil
}
