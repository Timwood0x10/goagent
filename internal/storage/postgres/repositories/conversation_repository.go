// Package repositories provides data access layer for storage system.
package repositories

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/Timwood0x10/ares/internal/errors"
	"github.com/Timwood0x10/ares/internal/storage/postgres"
	storage_models "github.com/Timwood0x10/ares/internal/storage/postgres/models"
)

// ConversationRepository provides data access for conversation history.
// This implements CRUD operations for storing and retrieving conversation messages.
// It depends on the DBTX interface to support both database connections and transactions.
type ConversationRepository struct {
	db postgres.DBTX
}

// NewConversationRepository creates a new ConversationRepository instance.
// Args:
// db - database connection or transaction implementing DBTX interface.
// Returns new ConversationRepository instance.
func NewConversationRepository(db postgres.DBTX) *ConversationRepository {
	return &ConversationRepository{db: db}
}

// Create inserts a new conversation message into the database.
// Args:
// ctx - database operation context.
// conv - conversation message to create. ID should be empty to let database generate it.
// Returns error if insert operation fails.
func (r *ConversationRepository) Create(ctx context.Context, conv *storage_models.Conversation) error {
	// Prepare metadata as JSONB for storage
	var metadataArg interface{}
	if conv.Metadata != nil {
		metadataJSON, err := json.Marshal(conv.Metadata)
		if err != nil {
			return errors.Wrap(err, "marshal metadata")
		}
		metadataArg = metadataJSON
	}

	// Build query based on whether ID is provided
	var query string
	var args []interface{}

	// Check if CreatedAt is zero value (0001-01-01)
	// If zero, use NOW() from database instead
	createdAtIsZero := conv.CreatedAt.IsZero()

	if conv.ID == "" {
		// Insert with auto-generated ID
		if createdAtIsZero {
			query = `
				INSERT INTO conversations
				(session_id, tenant_id, user_id, agent_id, role, content, metadata, expires_at, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
				RETURNING id
			`
			args = []interface{}{
				conv.SessionID, conv.TenantID, conv.UserID,
				conv.AgentID, conv.Role, conv.Content, metadataArg, conv.ExpiresAt,
			}
		} else {
			query = `
				INSERT INTO conversations
				(session_id, tenant_id, user_id, agent_id, role, content, metadata, expires_at, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
				RETURNING id
			`
			args = []interface{}{
				conv.SessionID, conv.TenantID, conv.UserID,
				conv.AgentID, conv.Role, conv.Content, metadataArg, conv.ExpiresAt, conv.CreatedAt,
			}
		}
	} else {
		// Insert with specified ID
		if createdAtIsZero {
			query = `
				INSERT INTO conversations
				(id, session_id, tenant_id, user_id, agent_id, role, content, metadata, expires_at, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW())
				RETURNING id
			`
			args = []interface{}{
				conv.ID, conv.SessionID, conv.TenantID, conv.UserID,
				conv.AgentID, conv.Role, conv.Content, metadataArg, conv.ExpiresAt,
			}
		} else {
			query = `
				INSERT INTO conversations
				(id, session_id, tenant_id, user_id, agent_id, role, content, metadata, expires_at, created_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
				RETURNING id
			`
			args = []interface{}{
				conv.ID, conv.SessionID, conv.TenantID, conv.UserID,
				conv.AgentID, conv.Role, conv.Content, metadataArg, conv.ExpiresAt, conv.CreatedAt,
			}
		}
	}

	var id string
	err := r.db.QueryRowContext(ctx, query, args...).Scan(&id)

	if err != nil {
		return errors.Wrap(err, "create conversation")
	}

	conv.ID = id
	return nil
}

// GetByID retrieves a conversation by ID.
// Args:
// ctx - database operation context.
// id - conversation ID, must be non-empty.
// Returns conversation or error if not found or invalid argument.
func (r *ConversationRepository) GetByID(ctx context.Context, id string) (*storage_models.Conversation, error) {
	if id == "" {
		return nil, errors.ErrInvalidArgument
	}

	query := `
		SELECT id, session_id, tenant_id, user_id, agent_id, role, content, metadata, expires_at, created_at
		FROM conversations
		WHERE id = $1
	`

	conv := &storage_models.Conversation{}
	var metadataBytes []byte
	var expiresAt sql.NullTime
	err := r.db.QueryRowContext(ctx, query, id).Scan(
		&conv.ID, &conv.SessionID, &conv.TenantID, &conv.UserID,
		&conv.AgentID, &conv.Role, &conv.Content, &metadataBytes, &expiresAt, &conv.CreatedAt,
	)
	if expiresAt.Valid {
		conv.ExpiresAt = expiresAt.Time
	}
	if metadataBytes != nil {
		if err := json.Unmarshal(metadataBytes, &conv.Metadata); err != nil {
			log.Warn("Failed to unmarshal metadata", "error", err)
		}
	}

	if err == sql.ErrNoRows {
		return nil, errors.ErrRecordNotFound
	}
	if err != nil {
		return nil, errors.Wrap(err, "get conversation by id")
	}

	return conv, nil
}

// GetBySession retrieves all conversation messages for a specific session.
// Args:
// ctx - database operation context.
// sessionID - session identifier.
// tenantID - tenant identifier for isolation.
// limit - maximum number of results to return.
// Returns list of conversation messages ordered by created time (ascending).
func (r *ConversationRepository) GetBySession(ctx context.Context, sessionID, tenantID string, limit int) ([]*storage_models.Conversation, error) {
	query := `
		SELECT id, session_id, tenant_id, user_id, agent_id, role, content, metadata, expires_at, created_at
		FROM conversations
		WHERE session_id = $1 AND tenant_id = $2
		ORDER BY created_at ASC
		LIMIT $3
	`

	rows, err := r.db.QueryContext(ctx, query, sessionID, tenantID, limit)
	if err != nil {
		return nil, errors.Wrap(err, "get conversations by session")
	}
	defer func() {
		if err := rows.Close(); err != nil {
			log.Error("Failed to close rows", "error", err)
		}
	}()

	conversations := make([]*storage_models.Conversation, 0)
	for rows.Next() {
		conv := &storage_models.Conversation{}
		var metadataBytes []byte
		err := rows.Scan(
			&conv.ID, &conv.SessionID, &conv.TenantID, &conv.UserID,
			&conv.AgentID, &conv.Role, &conv.Content, &metadataBytes, &conv.ExpiresAt, &conv.CreatedAt,
		)
		if err != nil {
			log.Error("Failed to scan conversation row", "error", err)
			continue
		}
		if metadataBytes != nil {
			if err := json.Unmarshal(metadataBytes, &conv.Metadata); err != nil {
				log.Warn("Failed to unmarshal metadata", "error", err)
			}
		}
		conversations = append(conversations, conv)
	}

	if err := rows.Err(); err != nil {
		log.Error("Failed to iterate conversations", "error", err)
		return nil, errors.Wrap(err, "iterate conversations")
	}

	return conversations, nil
}

// DeleteBySession removes all conversation messages for a specific session.
// Args:
// ctx - database operation context.
// sessionID - session identifier.
// tenantID - tenant identifier for isolation.
// Returns number of deleted messages or error if operation fails.
func (r *ConversationRepository) DeleteBySession(ctx context.Context, sessionID, tenantID string) (int64, error) {
	query := `
		DELETE FROM conversations
		WHERE session_id = $1 AND tenant_id = $2
	`

	result, err := r.db.ExecContext(ctx, query, sessionID, tenantID)
	if err != nil {
		return 0, errors.Wrap(err, "delete conversations by session")
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return 0, errors.Wrap(err, "get rows affected")
	}

	return rows, nil
}

// Delete removes a conversation message by its ID.
// Args:
// ctx - database operation context.
// id - conversation message identifier.
// Returns error if delete operation fails.
func (r *ConversationRepository) Delete(ctx context.Context, id, tenantID string) error {
	return postgres.DeleteByID(ctx, r.db, "conversations", id, tenantID)
}

// GetByUser retrieves recent conversation messages for a specific user.
// Args:
// ctx - database operation context.
// userID - user identifier.
// tenantID - tenant identifier for isolation.
// limit - maximum number of results to return.
// Returns list of recent conversation messages ordered by created time (descending).
func (r *ConversationRepository) GetByUser(ctx context.Context, userID, tenantID string, limit int) ([]*storage_models.Conversation, error) {
	query := `
		SELECT id, session_id, tenant_id, user_id, agent_id, role, content, metadata, expires_at, created_at
		FROM conversations
		WHERE user_id = $1 AND tenant_id = $2
		ORDER BY created_at DESC
		LIMIT $3
	`

	rows, err := r.db.QueryContext(ctx, query, userID, tenantID, limit)
	if err != nil {
		return nil, errors.Wrap(err, "get conversations by user")
	}
	defer func() {
		if err := rows.Close(); err != nil {
			log.Error("Failed to close rows", "error", err)
		}
	}()

	conversations := make([]*storage_models.Conversation, 0)
	for rows.Next() {
		conv := &storage_models.Conversation{}
		var metadataBytes []byte
		err := rows.Scan(
			&conv.ID, &conv.SessionID, &conv.TenantID, &conv.UserID,
			&conv.AgentID, &conv.Role, &conv.Content, &metadataBytes, &conv.ExpiresAt, &conv.CreatedAt,
		)
		if err != nil {
			log.Warn("Failed to scan conversation row", "error", err)
			continue
		}
		if metadataBytes != nil {
			if err := json.Unmarshal(metadataBytes, &conv.Metadata); err != nil {
				log.Warn("Failed to unmarshal metadata", "error", err)
			}
		}
		conversations = append(conversations, conv)
	}

	if err := rows.Err(); err != nil {
		log.Error("Failed to iterate conversations", "error", err)
		return nil, errors.Wrap(err, "iterate conversations")
	}

	return conversations, nil
}

// GetByAgent retrieves recent conversation messages for a specific agent.
// Args:
// ctx - database operation context.
// agentID - agent identifier.
// tenantID - tenant identifier for isolation.
// limit - maximum number of results to return.
// Returns list of recent conversation messages ordered by created time (descending).
func (r *ConversationRepository) GetByAgent(ctx context.Context, agentID, tenantID string, limit int) ([]*storage_models.Conversation, error) {
	query := `
		SELECT id, session_id, tenant_id, user_id, agent_id, role, content, metadata, expires_at, created_at
		FROM conversations
		WHERE agent_id = $1 AND tenant_id = $2
		ORDER BY created_at DESC
		LIMIT $3
	`

	rows, err := r.db.QueryContext(ctx, query, agentID, tenantID, limit)
	if err != nil {
		return nil, errors.Wrap(err, "get conversations by agent")
	}
	defer func() {
		if err := rows.Close(); err != nil {
			log.Error("Failed to close rows", "error", err)
		}
	}()

	conversations := make([]*storage_models.Conversation, 0)
	for rows.Next() {
		conv := &storage_models.Conversation{}
		var metadataBytes []byte
		err := rows.Scan(
			&conv.ID, &conv.SessionID, &conv.TenantID, &conv.UserID,
			&conv.AgentID, &conv.Role, &conv.Content, &metadataBytes, &conv.ExpiresAt, &conv.CreatedAt,
		)
		if err != nil {
			log.Error("Failed to scan conversation row", "error", err)
			continue
		}
		if metadataBytes != nil {
			if err := json.Unmarshal(metadataBytes, &conv.Metadata); err != nil {
				log.Warn("Failed to unmarshal metadata", "error", err)
			}
		}
		conversations = append(conversations, conv)
	}

	if err := rows.Err(); err != nil {
		log.Error("Failed to iterate conversations", "error", err)
		return nil, errors.Wrap(err, "iterate conversations")
	}

	return conversations, nil
}

// CleanupExpired removes conversation messages that have expired.
// Args:
// ctx - database operation context.
// Returns number of deleted messages or error if operation fails.
func (r *ConversationRepository) CleanupExpired(ctx context.Context) (int64, error) {
	query := `
		DELETE FROM conversations
		WHERE expires_at IS NOT NULL AND expires_at < NOW()
	`

	result, err := r.db.ExecContext(ctx, query)
	if err != nil {
		return 0, errors.Wrap(err, "cleanup expired conversations")
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return 0, errors.Wrap(err, "get rows affected")
	}

	return rows, nil
}

// UpdateExpiresAt updates the expiration time for a conversation session.
// Args:
// ctx - database operation context.
// sessionID - session identifier.
// tenantID - tenant identifier for isolation.
// expiresAt - new expiration time.
// Returns number of updated messages or error if operation fails.
func (r *ConversationRepository) UpdateExpiresAt(ctx context.Context, sessionID, tenantID string, expiresAt time.Time) (int64, error) {
	query := `
		UPDATE conversations
		SET expires_at = $1
		WHERE session_id = $2 AND tenant_id = $3
	`

	result, err := r.db.ExecContext(ctx, query, expiresAt, sessionID, tenantID)
	if err != nil {
		return 0, errors.Wrap(err, "update expires at")
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return 0, errors.Wrap(err, "get rows affected")
	}

	return rows, nil
}

// CountBySession returns the number of messages in a session.
// Args:
// ctx - database operation context.
// sessionID - session identifier.
// tenantID - tenant identifier for isolation.
// Returns message count or error if query fails.
func (r *ConversationRepository) CountBySession(ctx context.Context, sessionID, tenantID string) (int64, error) {
	query := `
		SELECT COUNT(*)
		FROM conversations
		WHERE session_id = $1 AND tenant_id = $2
	`

	var count int64
	err := r.db.QueryRowContext(ctx, query, sessionID, tenantID).Scan(&count)
	if err != nil {
		return 0, errors.Wrap(err, "count messages in session")
	}

	return count, nil
}

// GetRecentSessions retrieves recent conversation sessions for a tenant.
// Args:
// ctx - database operation context.
// tenantID - tenant identifier for isolation.
// limit - maximum number of sessions to return.
// Returns list of session identifiers ordered by last activity (descending).
func (r *ConversationRepository) GetRecentSessions(ctx context.Context, tenantID string, limit int) ([]string, error) {
	query := `
		SELECT session_id
		FROM conversations
		WHERE tenant_id = $1
		GROUP BY session_id
		ORDER BY MAX(created_at) DESC
		LIMIT $2
	`

	rows, err := r.db.QueryContext(ctx, query, tenantID, limit)
	if err != nil {
		return nil, errors.Wrap(err, "get recent sessions")
	}
	defer func() { _ = rows.Close() }()

	sessions := make([]string, 0)
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			continue
		}
		sessions = append(sessions, sessionID)
	}

	if err := rows.Err(); err != nil {
		log.Error("Failed to iterate sessions", "error", err)
		return nil, errors.Wrap(err, "iterate sessions")
	}

	return sessions, nil
}
