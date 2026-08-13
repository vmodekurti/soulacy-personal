// Package credentials provides an encrypted key-value store for agent credentials.
// Values are encrypted with AES-256-GCM using a key derived from the KMSProvider.
package credentials

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"

	_ "github.com/mattn/go-sqlite3"

	"github.com/soulacy/soulacy/internal/sqlitex"
)

// ErrNotFound is returned when a requested credential does not exist.
var ErrNotFound = errors.New("credential not found")

// Vault stores and retrieves encrypted agent credentials.
type Vault interface {
	// Set encrypts value and stores it under (agentID, key).
	Set(ctx context.Context, agentID, key string, value []byte) error
	// Get retrieves and decrypts the value for (agentID, key).
	// Returns ErrNotFound if absent.
	Get(ctx context.Context, agentID, key string) ([]byte, error)
	// Delete removes a credential.
	Delete(ctx context.Context, agentID, key string) error
	// List returns all keys for agentID.
	List(ctx context.Context, agentID string) ([]string, error)
	// WriteBlob is used by Python tools via env helper for mutable state
	// (e.g. cookie jars). Delegates to Set.
	WriteBlob(ctx context.Context, agentID, key string, data []byte) error
	// ReadBlob delegates to Get.
	ReadBlob(ctx context.Context, agentID, key string) ([]byte, error)
	Close() error
}

const credentialSchema = `
CREATE TABLE IF NOT EXISTS credentials (
    agent_id   TEXT NOT NULL,
    key        TEXT NOT NULL,
    ciphertext BLOB NOT NULL,
    PRIMARY KEY (agent_id, key)
)
`

// SQLiteVault is a Vault backed by a SQLite database with AES-256-GCM encryption.
type SQLiteVault struct {
	db  *sql.DB
	kms KMSProvider
}

// NewSQLiteVault opens (or creates) the SQLite database at path and returns a
// Vault that encrypts every value with a key from kms.
func NewSQLiteVault(path string, kms KMSProvider) (*SQLiteVault, error) {
	db, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		return nil, fmt.Errorf("credentials: open sqlite %s: %w", path, err)
	}
	if _, err := db.Exec(credentialSchema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("credentials: schema migration: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("credentials: secure vault permissions: %w", err)
	}

	return &SQLiteVault{db: db, kms: kms}, nil
}

// Set encrypts value and upserts it under (agentID, key).
func (v *SQLiteVault) Set(ctx context.Context, agentID, key string, value []byte) error {
	encKey, err := v.kms.DeriveKey(ctx, agentID)
	if err != nil {
		return fmt.Errorf("credentials: derive key: %w", err)
	}
	ct, err := encrypt(encKey, value)
	if err != nil {
		return fmt.Errorf("credentials: encrypt: %w", err)
	}
	_, err = v.db.ExecContext(ctx,
		`INSERT INTO credentials (agent_id, key, ciphertext)
		 VALUES (?, ?, ?)
		 ON CONFLICT(agent_id, key) DO UPDATE SET ciphertext = excluded.ciphertext`,
		agentID, key, ct,
	)
	if err != nil {
		return fmt.Errorf("credentials: set: %w", err)
	}
	return nil
}

// Get retrieves and decrypts the value for (agentID, key).
func (v *SQLiteVault) Get(ctx context.Context, agentID, key string) ([]byte, error) {
	var ct []byte
	err := v.db.QueryRowContext(ctx,
		`SELECT ciphertext FROM credentials WHERE agent_id = ? AND key = ?`,
		agentID, key,
	).Scan(&ct)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("credentials: get: %w", err)
	}

	encKey, err := v.kms.DeriveKey(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("credentials: derive key: %w", err)
	}
	plaintext, err := decrypt(encKey, ct)
	if err != nil {
		return nil, fmt.Errorf("credentials: decrypt: %w", err)
	}
	return plaintext, nil
}

// Delete removes a credential. Returns nil if the credential did not exist.
func (v *SQLiteVault) Delete(ctx context.Context, agentID, key string) error {
	_, err := v.db.ExecContext(ctx,
		`DELETE FROM credentials WHERE agent_id = ? AND key = ?`,
		agentID, key,
	)
	if err != nil {
		return fmt.Errorf("credentials: delete: %w", err)
	}
	return nil
}

// List returns all credential keys for agentID.
func (v *SQLiteVault) List(ctx context.Context, agentID string) ([]string, error) {
	rows, err := v.db.QueryContext(ctx,
		`SELECT key FROM credentials WHERE agent_id = ? ORDER BY key`,
		agentID,
	)
	if err != nil {
		return nil, fmt.Errorf("credentials: list: %w", err)
	}
	defer rows.Close()

	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, fmt.Errorf("credentials: list scan: %w", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("credentials: list rows: %w", err)
	}
	return keys, nil
}

// WriteBlob delegates to Set.
func (v *SQLiteVault) WriteBlob(ctx context.Context, agentID, key string, data []byte) error {
	return v.Set(ctx, agentID, key, data)
}

// ReadBlob delegates to Get.
func (v *SQLiteVault) ReadBlob(ctx context.Context, agentID, key string) ([]byte, error) {
	return v.Get(ctx, agentID, key)
}

// Close closes the underlying database connection.
func (v *SQLiteVault) Close() error { return v.db.Close() }

// --- AES-256-GCM helpers ---

// encrypt encrypts plaintext with the given 32-byte key using AES-256-GCM.
// The returned ciphertext has a fresh 12-byte random nonce prepended.
func encrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize()) // 12 bytes for standard GCM
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	// Prepend nonce to the ciphertext so decrypt can extract it.
	sealed := gcm.Seal(nil, nonce, plaintext, nil)
	out := make([]byte, len(nonce)+len(sealed))
	copy(out, nonce)
	copy(out[len(nonce):], sealed)
	return out, nil
}

// decrypt decrypts ciphertext (nonce || sealed) with the given 32-byte key.
func decrypt(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, sealed := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return gcm.Open(nil, nonce, sealed, nil)
}
