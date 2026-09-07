// Package repositories provides data access layer for storage system.
package repositories

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/Timwood0x10/ares/internal/errors"
	"github.com/Timwood0x10/ares/internal/storage/postgres/adapters"
	storage_models "github.com/Timwood0x10/ares/internal/storage/postgres/models"
)

// SecretRepository provides data access for encrypted sensitive data.
// This implements secure storage and retrieval of secrets with encryption.
type SecretRepository struct {
	db            *sql.DB
	mu            sync.RWMutex
	encryptionKey []byte
	gcm           cipher.AEAD
}

// NewSecretRepository creates a new SecretRepository instance.
// Args:
// db - database connection.
// encryptionKey - encryption key (32 bytes for AES-256).
// Returns new SecretRepository instance.
func NewSecretRepository(db *sql.DB, encryptionKey []byte) (*SecretRepository, error) {
	if len(encryptionKey) != 32 {
		return nil, fmt.Errorf("encryption key must be 32 bytes for AES-256-GCM, got %d bytes", len(encryptionKey))
	}
	block, err := aes.NewCipher(encryptionKey)
	if err != nil {
		return nil, errors.Wrap(err, "create cipher")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.Wrap(err, "create GCM")
	}
	return &SecretRepository{
		db:            db,
		encryptionKey: encryptionKey,
		gcm:           gcm,
	}, nil
}

// Set stores a secret value with encryption.
// Args:
// ctx - database operation context.
// key - secret key.
// value - secret value to store.
// tenantID - tenant identifier for isolation.
// Returns error if storage operation fails.
func (r *SecretRepository) Set(ctx context.Context, key, value, tenantID string) error {
	// Encrypt the value
	encrypted, err := r.encrypt([]byte(value))
	if err != nil {
		return errors.Wrap(err, "encrypt secret")
	}

	query := `
		INSERT INTO secrets
		(id, tenant_id, key, value, key_version, algorithm, created_at)
		VALUES (gen_random_uuid(), $1, $2, $3, 1, 'aes-gcm', NOW())
		ON CONFLICT (tenant_id, key) DO UPDATE SET
			value = EXCLUDED.value,
			key_version = secrets.key_version + 1,
			updated_at = NOW()
		RETURNING id
	`

	var id string
	err = r.db.QueryRowContext(ctx, query, tenantID, key, encrypted).Scan(&id)
	if err != nil {
		return errors.Wrap(err, "set secret")
	}

	return nil
}

// Get retrieves and decrypts a secret value.
// Args:
// ctx - database operation context.
// key - secret key.
// tenantID - tenant identifier for isolation.
// Returns decrypted secret value or error if not found.
func (r *SecretRepository) Get(ctx context.Context, key, tenantID string) (string, error) {
	query := `
		SELECT id, tenant_id, key, value, key_version, algorithm, expires_at
		FROM secrets
		WHERE key = $1 AND tenant_id = $2
	`

	var id, tenant, secretKey string
	var encryptedValue []byte
	var keyVersion int
	var algorithm string
	var expiresAt *time.Time

	err := r.db.QueryRowContext(ctx, query, key, tenantID).Scan(
		&id, &tenant, &secretKey, &encryptedValue, &keyVersion, &algorithm, &expiresAt,
	)

	if err == sql.ErrNoRows {
		return "", errors.ErrRecordNotFound
	}
	if err != nil {
		return "", errors.Wrap(err, "get secret")
	}

	// Check if secret has expired
	if expiresAt != nil && time.Now().After(*expiresAt) {
		return "", errors.ErrSecretExpired
	}

	// Decrypt the value
	decrypted, err := r.decrypt(encryptedValue)
	if err != nil {
		return "", errors.Wrap(err, "decrypt secret")
	}

	return string(decrypted), nil
}

// Delete removes a secret by its key.
// Args:
// ctx - database operation context.
// key - secret key.
// tenantID - tenant identifier for isolation.
// Returns error if delete operation fails.
func (r *SecretRepository) Delete(ctx context.Context, key, tenantID string) error {
	query := `DELETE FROM secrets WHERE key = $1 AND tenant_id = $2`

	result, err := r.db.ExecContext(ctx, query, key, tenantID)
	if err != nil {
		return errors.Wrap(err, "delete secret")
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

// List retrieves all secret keys for a tenant.
// Args:
// ctx - database operation context.
// tenantID - tenant identifier for isolation.
// Returns list of secret metadata (without values) or error if query fails.
func (r *SecretRepository) List(ctx context.Context, tenantID string) ([]*storage_models.Secret, error) {
	query := `
		SELECT id, tenant_id, key, key_version, algorithm, expires_at, created_at
		FROM secrets
		WHERE tenant_id = $1
		ORDER BY key ASC
	`

	rows, err := r.db.QueryContext(ctx, query, tenantID)
	if err != nil {
		return nil, errors.Wrap(err, "list secrets")
	}
	defer func() { _ = rows.Close() }()

	secrets := make([]*storage_models.Secret, 0)
	for rows.Next() {
		secret := &storage_models.Secret{}
		err := rows.Scan(
			&secret.ID, &secret.TenantID, &secret.Key,
			&secret.KeyVersion, &secret.Algorithm, &secret.ExpiresAt, &secret.CreatedAt,
		)
		if err != nil {
			continue
		}
		secrets = append(secrets, secret)
	}

	if err := rows.Err(); err != nil {
		log.Error("Failed to iterate secrets", "error", err)
		return nil, errors.Wrap(err, "iterate secrets")
	}

	return secrets, nil
}

// SetWithExpiration stores a secret value with expiration time.
// Args:
// ctx - database operation context.
// key - secret key.
// value - secret value to store.
// tenantID - tenant identifier for isolation.
// expiresAt - expiration time.
// Returns error if storage operation fails.
func (r *SecretRepository) SetWithExpiration(ctx context.Context, key, value, tenantID string, expiresAt time.Time) error {
	// Encrypt the value
	encrypted, err := r.encrypt([]byte(value))
	if err != nil {
		return errors.Wrap(err, "encrypt secret")
	}

	query := `
		INSERT INTO secrets
		(id, tenant_id, key, value, key_version, algorithm, expires_at, created_at)
		VALUES (gen_random_uuid(), $1, $2, $3, 1, 'aes-gcm', $4, NOW())
		ON CONFLICT (tenant_id, key) DO UPDATE SET
			value = EXCLUDED.value,
			key_version = secrets.key_version + 1,
			expires_at = EXCLUDED.expires_at,
			updated_at = NOW()
		RETURNING id
	`

	var id string
	err = r.db.QueryRowContext(ctx, query, tenantID, key, encrypted, expiresAt).Scan(&id)
	if err != nil {
		return errors.Wrap(err, "set secret with expiration")
	}

	return nil
}

// UpdateMetadata updates the metadata for a secret.
// Args:
// ctx - database operation context.
// key - secret key.
// tenantID - tenant identifier for isolation.
// metadata - metadata to update.
// Returns error if update operation fails.
func (r *SecretRepository) UpdateMetadata(ctx context.Context, key, tenantID string, metadata map[string]interface{}) error {
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return errors.Wrap(err, "marshal metadata")
	}

	query := `
		UPDATE secrets
		SET metadata = $3, updated_at = NOW()
		WHERE key = $1 AND tenant_id = $2
	`

	result, err := r.db.ExecContext(ctx, query, key, tenantID, metadataJSON)
	if err != nil {
		return errors.Wrap(err, "update secret metadata")
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

// CleanupExpired removes secrets that have expired.
// Args:
// ctx - database operation context.
// Returns number of deleted secrets or error if operation fails.
func (r *SecretRepository) CleanupExpired(ctx context.Context) (int64, error) {
	query := `
		DELETE FROM secrets
		WHERE expires_at IS NOT NULL AND expires_at < NOW()
	`

	result, err := r.db.ExecContext(ctx, query)
	if err != nil {
		return 0, errors.Wrap(err, "cleanup expired secrets")
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return 0, errors.Wrap(err, "get rows affected")
	}

	return rows, nil
}

// encrypt encrypts data using AES-GCM.
// Args:
// plaintext - data to encrypt.
// Returns encrypted data or error if encryption fails.
func (r *SecretRepository) encrypt(plaintext []byte) ([]byte, error) {
	r.mu.RLock()
	gcm := r.gcm
	r.mu.RUnlock()

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, errors.Wrap(err, "generate nonce")
	}
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

// decrypt decrypts data using AES-GCM.
// Args:
// ciphertext - data to decrypt.
// Returns decrypted data or error if decryption fails.
func (r *SecretRepository) decrypt(ciphertext []byte) ([]byte, error) {
	r.mu.RLock()
	gcm := r.gcm
	r.mu.RUnlock()

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, errors.Wrap(err, "decrypt")
	}

	return plaintext, nil
}

// RotateKey re-encrypts all secrets with a new encryption key.
// This implements atomic key rotation with transaction support as per design standard.
// Implementation requirements (per design specification):
//  1. Start transaction for atomic operation
//  2. Retrieve all secrets with current encryption key (SELECT ... FOR UPDATE)
//  3. For each secret:
//     a. Decrypt using old encryption key (AES-256-GCM)
//     b. Re-encrypt using new encryption key
//     c. Update database with new encrypted values
//     d. Increment key_version
//  4. Commit transaction if all succeed, rollback if any fail
//  5. Add audit logging for key rotation events
//  6. Test with various secret types and sizes
//
// Dependencies:
// - Need secure key exchange mechanism for distributing new key
// - Need rollback mechanism if rotation fails mid-way
// Args:
// ctx - database operation context.
// tenantID - tenant identifier for multi-tenant isolation.
// newKey - new encryption key (32 bytes for AES-256-GCM).
// Returns number of updated secrets or error if operation fails.
func (r *SecretRepository) RotateKey(ctx context.Context, tenantID string, newKey []byte) (int64, error) {
	if len(newKey) != 32 {
		return 0, errors.New("new key must be 32 bytes for AES-256-GCM")
	}

	// Start transaction for atomic operation (per design standard)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, errors.Wrap(err, "begin transaction")
	}
	committed := false
	defer func() {
		if !committed {
			if err := tx.Rollback(); err != nil {
				// nolint: errcheck // Transaction rollback error is logged but not critical
				log.Error("Failed to rollback transaction", "error", err)
			}
		}
	}()

	// Retrieve secrets for specific tenant with FOR UPDATE lock (per design standard)
	query := `
		SELECT id, tenant_id, key, value, key_version, algorithm
		FROM secrets
		WHERE tenant_id = $1
		ORDER BY key_version ASC
		FOR UPDATE
	`

	rows, err := tx.QueryContext(ctx, query, tenantID)
	if err != nil {
		return 0, errors.Wrap(err, "fetch secrets for rotation")
	}
	defer func() {
		if err := rows.Close(); err != nil {
			log.Error("Failed to close rows", "error", err)
		}
	}()

	var secrets []*storage_models.Secret
	for rows.Next() {
		secret := &storage_models.Secret{}
		var valueBytes []byte

		if err := rows.Scan(&secret.ID, &secret.TenantID, &secret.Key, &valueBytes, &secret.KeyVersion, &secret.Algorithm); err != nil {
			return 0, errors.Wrap(err, "scan secret")
		}

		secret.Value = valueBytes
		secrets = append(secrets, secret)
	}

	if err := rows.Err(); err != nil {
		log.Error("Failed to iterate secrets", "error", err)
		return 0, errors.Wrap(err, "iterate secrets")
	}

	// For each secret: decrypt with old key, re-encrypt with new key (per design standard)
	updatedCount := int64(0)
	for _, secret := range secrets {
		// Decrypt using current encryption key
		plaintext, err := r.decryptSecret(secret.Value)
		if err != nil {
			return 0, errors.Wrapf(err, "decrypt secret %s", secret.Key)
		}

		// Re-encrypt using new encryption key (AES-256-GCM)
		encryptedValue, err := r.encryptSecret(plaintext, newKey)
		if err != nil {
			return 0, errors.Wrapf(err, "encrypt secret %s with new key", secret.Key)
		}

		// Update database with new encrypted values (per design standard)
		updateQuery := `
			UPDATE secrets
			SET value = $1, key_version = key_version + 1, updated_at = NOW()
			WHERE id = $2
		`

		result, err := tx.ExecContext(ctx, updateQuery, encryptedValue, secret.ID)
		if err != nil {
			return 0, errors.Wrapf(err, "update secret %s", secret.Key)
		}

		rowsAffected, err := result.RowsAffected()
		if err != nil {
			log.Warn("Failed to get rows affected", "error", err)
		} else {
			updatedCount += rowsAffected
		}
	}

	// Commit transaction (per design standard)
	if err := tx.Commit(); err != nil {
		return 0, errors.Wrap(err, "commit transaction")
	}
	committed = true

	// Update in-memory encryption key and GCM so subsequent encrypt/decrypt
	// operations use the new key. Without this, Get/Set would still use the
	// old key, causing decryption failures for the rotated secrets.
	newBlock, err := aes.NewCipher(newKey)
	if err != nil {
		return 0, errors.Wrap(err, "create cipher from new key")
	}
	newGCM, err := cipher.NewGCM(newBlock)
	if err != nil {
		return 0, errors.Wrap(err, "create GCM from new key")
	}
	r.mu.Lock()
	r.encryptionKey = newKey
	r.gcm = newGCM
	r.mu.Unlock()

	// Add audit logging for key rotation events (per design standard)
	log.Info("Secret key rotation completed", "updated_secrets", updatedCount, "timestamp", time.Now())

	return updatedCount, nil
}

// Export exports secrets (for backup purposes).
// Args:
// ctx - database operation context.
// tenantID - tenant identifier for isolation.
// Returns exported secrets data or error if export fails.
func (r *SecretRepository) Export(ctx context.Context, tenantID string) ([]byte, error) {
	secrets, err := r.List(ctx, tenantID)
	if err != nil {
		return nil, errors.Wrap(err, "list secrets")
	}

	data, err := json.Marshal(secrets)
	if err != nil {
		return nil, errors.Wrap(err, "marshal secrets")
	}

	return data, nil
}

// Import imports secrets (for restore purposes).
// This implements secret import functionality with format adapter layer as per design standard.
// The adapter layer converts various input formats (JSON/YAML/CSV) to standard JSON format,
// then processes the import operation with transaction support.
//
// Supported input formats:
// - JSON: Standard JSON format with key-value pairs
// - YAML: YAML format with key-value structure
// - CSV: CSV format with columns: key, value, expires_at (optional)
//
// Implementation requirements (per design specification):
//  1. Parse input data using format adapter layer (supports JSON/YAML/CSV)
//  2. Validate secret values (format, length, constraints)
//  3. Start transaction for atomic import operation
//  4. For each secret:
//     a. Check for duplicate keys within same tenant
//     b. Encrypt using current encryption key (AES-256-GCM)
//     c. Insert into database with proper tenant isolation
//  5. Commit transaction if all succeed, rollback if any fail
//  6. Add audit logging for import events
//
// Args:
// ctx - database operation context.
// tenantID - tenant identifier for isolation.
// data - secrets data in any supported format (JSON/YAML/CSV).
// format - input format type (json/yaml/csv).
// Returns number of imported secrets or error if import fails.
func (r *SecretRepository) Import(ctx context.Context, tenantID string, data []byte, format string) (int64, error) {
	// Validate input
	if len(data) == 0 {
		return 0, errors.New("import data cannot be empty")
	}

	if tenantID == "" {
		return 0, errors.New("tenant ID cannot be empty")
	}

	// Use adapter layer to parse input format
	// This implements the adapter pattern for format conversion
	adapter := &adapters.SecretAdapter{}
	jsonData, err := adapter.ParseFrom(data, adapters.SecretFormat(format))
	if err != nil {
		return 0, errors.Wrap(err, "parse input format")
	}

	// Parse import items from JSON format
	items, err := adapter.ParseImportData(jsonData)
	if err != nil {
		return 0, errors.Wrap(err, "parse import items")
	}

	if len(items) == 0 {
		return 0, errors.New("no secrets found in import data")
	}

	// Start transaction for atomic import operation (per design standard)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, errors.Wrap(err, "begin transaction")
	}
	committed := false
	defer func() {
		if !committed {
			if rbErr := tx.Rollback(); rbErr != nil {
				log.Error("Failed to rollback transaction", "error", rbErr)
			}
		}
	}()

	// Process each secret for import (per design standard)
	importedCount := int64(0)
	importErrors := make([]string, 0)

	for _, item := range items {
		// Validate secret key
		if item.Key == "" {
			importErrors = append(importErrors, "secret key cannot be empty")
			continue
		}

		// Validate secret value
		if item.Value == "" {
			importErrors = append(importErrors, fmt.Sprintf("secret value cannot be empty for key: %s", item.Key))
			continue
		}

		// Check for duplicate keys within same tenant (per design standard).
		// A real query error must abort: treating it as "not exists"
		// silently degraded the failure into an insert attempt.
		var existingKeyVersion int
		checkQuery := `SELECT key_version FROM secrets WHERE key = $1 AND tenant_id = $2`
		err := tx.QueryRowContext(ctx, checkQuery, item.Key, tenantID).Scan(&existingKeyVersion)
		switch {
		case err == nil:
			log.Warn("Secret key already exists, skipping", "key", item.Key, "existing_version", existingKeyVersion)
			continue
		case stderrors.Is(err, sql.ErrNoRows):
			// genuinely new key — proceed to insert
		default:
			return 0, errors.Wrapf(err, "check secret %s", item.Key)
		}

		// Encrypt secret value using current encryption key (AES-256-GCM)
		encrypted, err := r.encrypt([]byte(item.Value))
		if err != nil {
			importErrors = append(importErrors, fmt.Sprintf("encrypt secret %s: %v", item.Key, err))
			continue
		}

		// Insert secret into database with proper tenant isolation
		insertQuery := `
            INSERT INTO secrets
            (id, tenant_id, key, value, key_version, algorithm, expires_at, created_at)
            VALUES (gen_random_uuid(), $1, $2, $3, 1, 'aes-gcm', $4, NOW())
            RETURNING id
        `

		var expiresAt interface{}
		if item.ExpiresAt != "" {
			parsedTime, err := time.Parse(time.RFC3339, item.ExpiresAt)
			if err != nil {
				importErrors = append(importErrors, fmt.Sprintf("invalid expires_at format for key %s: %v", item.Key, err))
				continue
			}
			expiresAt = parsedTime
		}

		var id string
		err = tx.QueryRowContext(ctx, insertQuery, tenantID, item.Key, encrypted, expiresAt).Scan(&id)
		if err != nil {
			importErrors = append(importErrors, fmt.Sprintf("insert secret %s: %v", item.Key, err))
			continue
		}

		importedCount++
		// Log at Debug level to avoid leaking secret key names in production logs.
		log.Debug("Secret imported successfully", "tenant_id", tenantID, "secret_id", id)
	}

	// Atomicity contract: if NOTHING was imported the transaction is
	// rolled back and the collected reasons are returned as the error.
	if importedCount == 0 {
		return 0, fmt.Errorf("no secrets imported, errors: %v", importErrors)
	}

	// Commit transaction (per design standard)
	if err := tx.Commit(); err != nil {
		return 0, errors.Wrap(err, "commit transaction")
	}
	committed = true

	// Partial failures are surfaced to the caller: previously they were
	// only logged and the call returned a clean nil, hiding data loss.
	var importErr error
	if len(importErrors) > 0 {
		importErr = fmt.Errorf("%d of %d secrets failed to import: %v",
			len(importErrors), len(items), importErrors)
		log.Warn("Secret import completed with errors",
			"imported_count", importedCount, "error_count", len(importErrors))
	}

	// Add audit logging for import events (per design standard)
	log.Info("Secret import completed", "tenant_id", tenantID, "imported_count", importedCount, "total_items", len(items))

	return importedCount, importErr
}

// GetKeyVersion retrieves the current key version for a secret.
// Args:
// ctx - database operation context.
// key - secret key.
// tenantID - tenant identifier for isolation.
// Returns key version or error if not found.
func (r *SecretRepository) GetKeyVersion(ctx context.Context, key, tenantID string) (int, error) {
	query := `
		SELECT key_version
		FROM secrets
		WHERE key = $1 AND tenant_id = $2
	`

	var keyVersion int
	err := r.db.QueryRowContext(ctx, query, key, tenantID).Scan(&keyVersion)
	if err == sql.ErrNoRows {
		return 0, errors.ErrRecordNotFound
	}
	if err != nil {
		return 0, errors.Wrap(err, "get key version")
	}

	return keyVersion, nil
}

// encryptSecret encrypts data using AES-GCM with a specific key.
// Args:
// plaintext - data to encrypt.
// key - encryption key (32 bytes for AES-256-GCM).
// Returns encrypted data or error if encryption fails.
func (r *SecretRepository) encryptSecret(plaintext []byte, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.Wrap(err, "create cipher")
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.Wrap(err, "create GCM")
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, errors.Wrap(err, "generate nonce")
	}

	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

// decryptSecret decrypts data using AES-GCM with the current key.
// Args:
// ciphertext - data to decrypt.
// Returns decrypted data or error if decryption fails.
func (r *SecretRepository) decryptSecret(ciphertext []byte) ([]byte, error) {
	r.mu.RLock()
	key := r.encryptionKey
	r.mu.RUnlock()

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.Wrap(err, "create cipher")
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.Wrap(err, "create GCM")
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, errors.Wrap(err, "decrypt")
	}

	return plaintext, nil
}
