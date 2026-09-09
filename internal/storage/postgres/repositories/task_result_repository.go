// Package repositories provides data access layer for storage system.
package repositories

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/Timwood0x10/ares/internal/errors"
	"github.com/Timwood0x10/ares/internal/storage/postgres"
	storage_models "github.com/Timwood0x10/ares/internal/storage/postgres/models"
)

// TaskResultRepository provides data access for task execution results.
// This implements CRUD operations and vector search for task results.
// It depends on the DBTX interface to support both database connections and transactions.
type TaskResultRepository struct {
	db postgres.DBTX
}

// NewTaskResultRepository creates a new TaskResultRepository instance.
// Args:
// db - database connection or transaction implementing DBTX interface.
// Returns new TaskResultRepository instance.
func NewTaskResultRepository(db postgres.DBTX) *TaskResultRepository {
	return &TaskResultRepository{db: db}
}

// Create inserts a new task result into the database.
// Args:
// ctx - database operation context.
// result - task result to create. ID should be empty to let database generate it.
// Returns error if insert operation fails.
func (r *TaskResultRepository) Create(ctx context.Context, result *storage_models.TaskResult) error {
	inputJSON, err := json.Marshal(result.Input)
	if err != nil {
		return errors.Wrap(err, "marshal input")
	}

	var outputJSON []byte
	if result.Output != nil {
		outputJSON, err = json.Marshal(result.Output)
		if err != nil {
			return errors.Wrap(err, "marshal output")
		}
	}

	metadataJSON, err := json.Marshal(result.Metadata)
	if err != nil {
		return errors.Wrap(err, "marshal metadata")
	}

	// Convert embedding to pgvector format. An empty embedding must be
	// NULL, not "[]" — the ::vector cast rejects an empty array literal.
	var embeddingStr any
	if len(result.Embedding) > 0 {
		embeddingStr = postgres.FormatVector(result.Embedding)
	}

	// Build query based on whether ID is provided
	var query string
	var args []interface{}

	// Check if CreatedAt is zero value (0001-01-01)
	// If zero, use NOW() from database instead
	createdAtIsZero := result.CreatedAt.IsZero()

	if result.ID == "" {
		// Insert with auto-generated ID
		if createdAtIsZero {
			query = `
				INSERT INTO ` + storage_models.TaskResultsTable + `
				(tenant_id, session_id, task_type, agent_id, input, output, embedding,
				 embedding_model, embedding_version, status, error, latency_ms, metadata, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7::vector, $8, $9, $10, $11, $12, $13, NOW())
				RETURNING id
			`
			args = []interface{}{
				result.TenantID, result.SessionID, result.TaskType,
				result.AgentID, inputJSON, outputJSON, embeddingStr,
				result.EmbeddingModel, result.EmbeddingVersion, result.Status,
				result.Error, result.LatencyMs, metadataJSON,
			}
		} else {
			query = `
				INSERT INTO ` + storage_models.TaskResultsTable + `
				(tenant_id, session_id, task_type, agent_id, input, output, embedding,
				 embedding_model, embedding_version, status, error, latency_ms, metadata, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7::vector, $8, $9, $10, $11, $12, $13, $14)
				RETURNING id
			`
			args = []interface{}{
				result.TenantID, result.SessionID, result.TaskType,
				result.AgentID, inputJSON, outputJSON, embeddingStr,
				result.EmbeddingModel, result.EmbeddingVersion, result.Status,
				result.Error, result.LatencyMs, metadataJSON, result.CreatedAt,
			}
		}
	} else {
		// Insert with specified ID
		if createdAtIsZero {
			query = `
				INSERT INTO ` + storage_models.TaskResultsTable + `
				(id, tenant_id, session_id, task_type, agent_id, input, output, embedding,
				 embedding_model, embedding_version, status, error, latency_ms, metadata, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8::vector, $9, $10, $11, $12, $13, $14, NOW())
				RETURNING id
			`
			args = []interface{}{
				result.ID, result.TenantID, result.SessionID, result.TaskType,
				result.AgentID, inputJSON, outputJSON, embeddingStr,
				result.EmbeddingModel, result.EmbeddingVersion, result.Status,
				result.Error, result.LatencyMs, metadataJSON,
			}
		} else {
			query = `
				INSERT INTO ` + storage_models.TaskResultsTable + `
				(id, tenant_id, session_id, task_type, agent_id, input, output, embedding,
				 embedding_model, embedding_version, status, error, latency_ms, metadata, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8::vector, $9, $10, $11, $12, $13, $14, $15)
				RETURNING id
			`
			args = []interface{}{
				result.ID, result.TenantID, result.SessionID, result.TaskType,
				result.AgentID, inputJSON, outputJSON, embeddingStr,
				result.EmbeddingModel, result.EmbeddingVersion, result.Status,
				result.Error, result.LatencyMs, metadataJSON, result.CreatedAt,
			}
		}
	}

	var id string
	err = r.db.QueryRowContext(ctx, query, args...).Scan(&id)

	if err != nil {
		return errors.Wrap(err, "create task result")
	}

	result.ID = id
	return nil
}

// GetByID retrieves a task result by ID.
// Args:
// ctx - database operation context.
// id - task result ID, must be non-empty.
// Returns task result or error if not found or invalid argument.
func (r *TaskResultRepository) GetByID(ctx context.Context, tenantID, id string) (*storage_models.TaskResult, error) {
	if id == "" {
		return nil, errors.ErrInvalidArgument
	}
	if tenantID == "" {
		return nil, postgres.ErrMissingTenantID
	}

	query := `
		SELECT id, tenant_id, session_id, task_type, agent_id, input, output,
			   embedding_model, embedding_version, status, error, latency_ms, metadata::text, created_at
		FROM ` + storage_models.TaskResultsTable + `
		WHERE id = $1 AND tenant_id = $2
	`

	result := &storage_models.TaskResult{}
	var inputJSON, outputJSON []byte
	var metadataStr string

	err := r.db.QueryRowContext(ctx, query, id, tenantID).Scan(
		&result.ID, &result.TenantID, &result.SessionID, &result.TaskType,
		&result.AgentID, &inputJSON, &outputJSON,
		&result.EmbeddingModel, &result.EmbeddingVersion, &result.Status,
		&result.Error, &result.LatencyMs, &metadataStr, &result.CreatedAt,
	)

	if err == sql.ErrNoRows {
		return nil, errors.ErrRecordNotFound
	}
	if err != nil {
		return nil, errors.Wrap(err, "get task result by id")
	}

	// Parse input JSON
	if err := json.Unmarshal(inputJSON, &result.Input); err != nil {
		return nil, errors.Wrap(err, "unmarshal input")
	}

	// Parse output JSON
	if outputJSON != nil {
		if err := json.Unmarshal(outputJSON, &result.Output); err != nil {
			return nil, errors.Wrap(err, "unmarshal output")
		}
	}

	// Parse metadata JSON string to map
	if metadataStr != "" {
		if err := json.Unmarshal([]byte(metadataStr), &result.Metadata); err != nil {
			return nil, errors.Wrap(err, "parse metadata")
		}
	}

	return result, nil
}

// Update updates an existing task result.
// Args:
// ctx - database operation context.
// result - task result with updated values.
// Returns error if update operation fails.
func (r *TaskResultRepository) Update(ctx context.Context, result *storage_models.TaskResult) error {
	if result.TenantID == "" {
		return postgres.ErrMissingTenantID
	}
	inputJSON, err := json.Marshal(result.Input)
	if err != nil {
		return errors.Wrap(err, "marshal input")
	}

	var outputJSON []byte
	if result.Output != nil {
		outputJSON, err = json.Marshal(result.Output)
		if err != nil {
			return errors.Wrap(err, "marshal output")
		}
	}

	// Convert metadata to JSON for database storage
	metadataJSON, err := json.Marshal(result.Metadata)
	if err != nil {
		return errors.Wrap(err, "marshal metadata")
	}

	// Convert embedding to pgvector format
	embeddingStr := postgres.FormatVector(result.Embedding)

	query := `
		UPDATE ` + storage_models.TaskResultsTable + `
		SET task_type = $2, agent_id = $3, input = $4, output = $5, embedding = $6::vector,
			embedding_model = $7, embedding_version = $8, status = $9, error = $10,
			latency_ms = $11, metadata = $12
		WHERE id = $1 AND tenant_id = $13
	`

	resultSQL, err := r.db.ExecContext(ctx, query,
		result.ID, result.TaskType, result.AgentID, inputJSON, outputJSON,
		embeddingStr, result.EmbeddingModel, result.EmbeddingVersion,
		result.Status, result.Error, result.LatencyMs, metadataJSON, result.TenantID,
	)
	if err != nil {
		return errors.Wrap(err, "update task result")
	}

	rows, err := resultSQL.RowsAffected()
	if err != nil {
		return errors.Wrap(err, "get rows affected")
	}

	if rows == 0 {
		return errors.ErrRecordNotFound
	}

	return nil
}

// Delete removes a task result by its ID with tenant scoping.
// Args:
// ctx - database operation context.
// id - task result identifier.
// tenantID - tenant identifier for isolation.
// Returns error if delete operation fails.
func (r *TaskResultRepository) Delete(ctx context.Context, id, tenantID string) error {
	return postgres.DeleteByID(ctx, r.db, storage_models.TaskResultsTable, id, tenantID)
}

// SearchByVector performs vector similarity search for task results.
// Args:
// ctx - database operation context.
// embedding - query vector embedding.
// tenantID - tenant identifier for isolation.
// limit - maximum number of results to return.
// Returns list of similar task results ordered by similarity.
func (r *TaskResultRepository) SearchByVector(ctx context.Context, embedding []float64, tenantID string, limit int) ([]*storage_models.TaskResult, error) {
	// Handle empty embedding - return empty results
	if len(embedding) == 0 {
		return []*storage_models.TaskResult{}, nil
	}

	// Convert embedding to pgvector format
	embeddingStr := postgres.FormatVector(embedding)

	query := `
		SELECT id, tenant_id, session_id, task_type, agent_id, input, output, embedding::text,
			   embedding_model, embedding_version, status, error, latency_ms, metadata::text, created_at,
			   1 - (embedding <=> $1::vector) as similarity
		FROM ` + storage_models.TaskResultsTable + `
		WHERE tenant_id = $2
		  AND embedding IS NOT NULL
		  AND status = 'completed'
		ORDER BY embedding <=> $1::vector
		LIMIT $3
	`

	rows, err := r.db.QueryContext(ctx, query, embeddingStr, tenantID, limit)
	if err != nil {
		return nil, errors.Wrap(err, "vector search")
	}
	defer func() {
		if err := rows.Close(); err != nil {
			log.Error("Failed to close rows", "error", err)
		}
	}()

	results := make([]*storage_models.TaskResult, 0)
	skippedCount := 0
	for rows.Next() {
		result := &storage_models.TaskResult{}
		var inputJSON, outputJSON []byte
		var embeddingStr, metadataStr string
		var similarity float64

		err := rows.Scan(
			&result.ID, &result.TenantID, &result.SessionID, &result.TaskType,
			&result.AgentID, &inputJSON, &outputJSON, &embeddingStr,
			&result.EmbeddingModel, &result.EmbeddingVersion, &result.Status,
			&result.Error, &result.LatencyMs, &metadataStr, &result.CreatedAt, &similarity,
		)
		if err != nil {
			log.Error("Failed to scan task result row", "error", err)
			skippedCount++
			continue
		}

		// Parse embedding string to float64 array
		result.Embedding, err = postgres.ParseVectorString(embeddingStr)
		if err != nil {
			log.Error("Failed to parse embedding vector", "task_id", result.ID, "error", err)
			skippedCount++
			continue
		}

		// Parse input JSON
		if err := json.Unmarshal(inputJSON, &result.Input); err != nil {
			log.Error("Failed to parse input JSON", "task_id", result.ID, "error", err)
			skippedCount++
			continue
		}

		// Parse output JSON
		if outputJSON != nil {
			if err := json.Unmarshal(outputJSON, &result.Output); err != nil {
				log.Error("Failed to parse output JSON", "task_id", result.ID, "error", err)
				skippedCount++
				continue
			}
		}

		// Parse metadata JSON string to map
		if metadataStr != "" {
			if err := json.Unmarshal([]byte(metadataStr), &result.Metadata); err != nil {
				result.Metadata = make(map[string]interface{})
			}
		}

		// Ensure metadata is initialized before storing similarity
		if result.Metadata == nil {
			result.Metadata = make(map[string]interface{})
		}

		// Store similarity in metadata
		// SQL query already computes similarity as: 1 - cosine_distance
		// where cosine_distance range is [0,2], so similarity range is [-1,1]
		result.Metadata["similarity"] = similarity
		results = append(results, result)
	}

	if skippedCount > 0 {
		log.Warn("Skipped task results due to errors", "skipped_count", skippedCount, "total_count", len(results)+skippedCount)
	}

	if err := rows.Err(); err != nil {
		log.Error("Failed to iterate task results", "error", err)
		return nil, errors.Wrap(err, "iterate task results")
	}

	return results, nil
}

// ListByType retrieves task results by type.
// Args:
// ctx - database operation context.
// taskType - task type filter.
// tenantID - tenant identifier for isolation.
// limit - maximum number of results to return.
// Returns list of task results ordered by created time (descending).
func (r *TaskResultRepository) ListByType(ctx context.Context, taskType, tenantID string, limit int) ([]*storage_models.TaskResult, error) {
	query := `
		SELECT id, tenant_id, session_id, task_type, agent_id, input, output,
			   embedding_model, embedding_version, status, error, latency_ms, metadata::text, created_at
		FROM ` + storage_models.TaskResultsTable + `
		WHERE task_type = $1 AND tenant_id = $2
		ORDER BY created_at DESC
		LIMIT $3
	`

	rows, err := r.db.QueryContext(ctx, query, taskType, tenantID, limit)
	if err != nil {
		return nil, errors.Wrap(err, "list task results by type")
	}
	defer func() {
		if err := rows.Close(); err != nil {
			log.Error("Failed to close rows", "error", err)
		}
	}()

	results := make([]*storage_models.TaskResult, 0)
	skippedCount := 0
	for rows.Next() {
		result := &storage_models.TaskResult{}
		var inputJSON, outputJSON []byte
		var metadataStr string

		err := rows.Scan(
			&result.ID, &result.TenantID, &result.SessionID, &result.TaskType,
			&result.AgentID, &inputJSON, &outputJSON,
			&result.EmbeddingModel, &result.EmbeddingVersion, &result.Status,
			&result.Error, &result.LatencyMs, &metadataStr, &result.CreatedAt,
		)
		if err != nil {
			log.Error("Failed to scan task result row", "error", err)
			skippedCount++
			continue
		}

		// Parse input JSON
		if err := json.Unmarshal(inputJSON, &result.Input); err != nil {
			log.Error("Failed to parse input JSON", "task_id", result.ID, "error", err)
			skippedCount++
			continue
		}

		// Parse output JSON
		if outputJSON != nil {
			if err := json.Unmarshal(outputJSON, &result.Output); err != nil {
				log.Error("Failed to parse output JSON", "task_id", result.ID, "error", err)
				skippedCount++
				continue
			}
		}

		// Parse metadata JSON string to map
		if metadataStr != "" {
			if err := json.Unmarshal([]byte(metadataStr), &result.Metadata); err != nil {
				result.Metadata = make(map[string]interface{})
			}
		} else {
			result.Metadata = make(map[string]interface{})
		}

		results = append(results, result)
	}

	if skippedCount > 0 {
		log.Warn("Skipped task results due to errors", "skipped_count", skippedCount, "total_count", len(results)+skippedCount)
	}

	if err := rows.Err(); err != nil {
		log.Error("Failed to iterate task results", "error", err)
		return nil, errors.Wrap(err, "iterate task results")
	}

	return results, nil
}

// ListBySession retrieves task results for a specific session.
// Args:
// ctx - database operation context.
// sessionID - session identifier.
// tenantID - tenant identifier for isolation.
// limit - maximum number of results to return.
// Returns list of task results ordered by created time (descending).
func (r *TaskResultRepository) ListBySession(ctx context.Context, sessionID, tenantID string, limit int) ([]*storage_models.TaskResult, error) {
	query := `
		SELECT id, tenant_id, session_id, task_type, agent_id, input, output,
			   embedding_model, embedding_version, status, error, latency_ms, metadata::text, created_at
		FROM ` + storage_models.TaskResultsTable + `
		WHERE session_id = $1 AND tenant_id = $2
		ORDER BY created_at DESC
		LIMIT $3
	`

	rows, err := r.db.QueryContext(ctx, query, sessionID, tenantID, limit)
	if err != nil {
		return nil, errors.Wrap(err, "list task results by session")
	}
	defer func() {
		if err := rows.Close(); err != nil {
			log.Error("Failed to close rows", "error", err)
		}
	}()

	results := make([]*storage_models.TaskResult, 0)
	for rows.Next() {
		result := &storage_models.TaskResult{}
		var inputJSON, outputJSON []byte
		var metadataStr string

		err := rows.Scan(
			&result.ID, &result.TenantID, &result.SessionID, &result.TaskType,
			&result.AgentID, &inputJSON, &outputJSON,
			&result.EmbeddingModel, &result.EmbeddingVersion, &result.Status,
			&result.Error, &result.LatencyMs, &metadataStr, &result.CreatedAt,
		)
		if err != nil {
			continue
		}

		// Parse input JSON
		if err := json.Unmarshal(inputJSON, &result.Input); err != nil {
			continue
		}

		// Parse output JSON
		if outputJSON != nil {
			if err := json.Unmarshal(outputJSON, &result.Output); err != nil {
				continue
			}
		}

		// Parse metadata JSON string to map
		if metadataStr != "" {
			if err := json.Unmarshal([]byte(metadataStr), &result.Metadata); err != nil {
				result.Metadata = make(map[string]interface{})
			}
		} else {
			result.Metadata = make(map[string]interface{})
		}

		results = append(results, result)
	}

	if err := rows.Err(); err != nil {
		log.Error("Failed to iterate task results", "error", err)
		return nil, errors.Wrap(err, "iterate task results")
	}

	return results, nil
}

// UpdateEmbedding updates the embedding for a task result.
// Args:
// ctx - database operation context.
// id - task result identifier.
// embedding - vector embedding.
// model - embedding model name.
// version - embedding model version.
// Returns error if update operation fails.
func (r *TaskResultRepository) UpdateEmbedding(ctx context.Context, tenantID, id string, embedding []float64, model string, version int) error {
	if tenantID == "" {
		return postgres.ErrMissingTenantID
	}
	// Convert embedding to pgvector format
	embeddingStr := postgres.FormatVector(embedding)

	query := `
		UPDATE ` + storage_models.TaskResultsTable + `
		SET embedding = $2::vector, embedding_model = $3, embedding_version = $4
		WHERE id = $1 AND tenant_id = $5
	`

	result, err := r.db.ExecContext(ctx, query, id, embeddingStr, model, version, tenantID)
	if err != nil {
		return errors.Wrap(err, "update embedding")
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

// UpdateStatus updates the status of a task result.
// Args:
// ctx - database operation context.
// id - task result identifier.
// status - new status value.
// errorMsg - error message if status is failed.
// latencyMs - execution latency in milliseconds.
// Returns error if update operation fails.
func (r *TaskResultRepository) UpdateStatus(ctx context.Context, tenantID, id, status, errorMsg string, latencyMs int) error {
	if tenantID == "" {
		return postgres.ErrMissingTenantID
	}
	query := `
		UPDATE ` + storage_models.TaskResultsTable + `
		SET status = $2, error = $3, latency_ms = $4
		WHERE id = $1 AND tenant_id = $5
	`

	result, err := r.db.ExecContext(ctx, query, id, status, errorMsg, latencyMs, tenantID)
	if err != nil {
		return errors.Wrap(err, "update status")
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

// GetStatistics returns statistics about task results.
// Args:
// ctx - database operation context.
// tenantID - tenant identifier for isolation.
// Returns task result statistics or error if query fails.
func (r *TaskResultRepository) GetStatistics(ctx context.Context, tenantID string) (map[string]int64, error) {
	query := `
		SELECT
			task_type,
			status,
			COUNT(*) as count
		FROM ` + storage_models.TaskResultsTable + `
		WHERE tenant_id = $1
		GROUP BY task_type, status
	`

	rows, err := r.db.QueryContext(ctx, query, tenantID)
	if err != nil {
		return nil, errors.Wrap(err, "get task result statistics")
	}
	defer func() { _ = rows.Close() }()

	stats := make(map[string]int64)
	for rows.Next() {
		var taskType, status string
		var count int64
		if err := rows.Scan(&taskType, &status, &count); err != nil {
			continue
		}
		key := fmt.Sprintf("%s:%s", taskType, status)
		stats[key] = count
	}

	if err := rows.Err(); err != nil {
		log.Error("Failed to iterate task result stats", "error", err)
		return nil, errors.Wrap(err, "iterate task result stats")
	}

	return stats, nil
}
