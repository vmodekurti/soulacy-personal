package apikeys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/soulacy/soulacy/internal/sqlitex"
)

// APIKey is a stored API key record. KeyHash is never returned to callers;
// the Prefix is shown for display purposes (first 8 chars of the key).
type APIKey struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"` // first 8 chars, display only
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// Store defines the API key management interface.
type Store interface {
	// Create generates a new API key with the given name and scopes.
	// Returns the plaintext key (shown ONCE) and the stored record.
	Create(ctx context.Context, name string, scopes []string) (plaintext string, key APIKey, err error)

	// Validate checks a plaintext key, updates last_used_at, and returns
	// the record. Returns ErrInvalidKey if the key is missing, revoked, or wrong.
	Validate(ctx context.Context, plaintext string) (APIKey, error)

	// Revoke marks a key as revoked by ID. Returns ErrNotFound if no such key.
	Revoke(ctx context.Context, id string) error

	// List returns all keys (excluding revoked by default).
	List(ctx context.Context, includeRevoked bool) ([]APIKey, error)

	// Close releases the DB connection.
	Close() error
}

var (
	ErrInvalidKey = fmt.Errorf("apikeys: invalid or revoked key")
	ErrNotFound   = fmt.Errorf("apikeys: key not found")
)

const schema = `
CREATE TABLE IF NOT EXISTS api_keys (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    key_hash      TEXT NOT NULL UNIQUE,
    prefix        TEXT NOT NULL,
    scopes        TEXT NOT NULL DEFAULT '',
    created_at    DATETIME NOT NULL,
    last_used_at  DATETIME,
    revoked_at    DATETIME
);
CREATE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys(key_hash);
`

// SQLiteStore implements Store using a SQLite database.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore opens (or creates) the API keys SQLite database at path.
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	db, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}

	// Schema versioning (E22 adoption): v1 = the idempotent bootstrap above;
	// future changes go through sqlitex.MigrateSchema with v2+.
	if err := sqlitex.RecordSchemaVersion(db, "apikeys", 1); err != nil {
		db.Close()
		return nil, err
	}
	return &SQLiteStore{db: db}, nil
}

// Create generates a new API key with the given name and scopes.
func (s *SQLiteStore) Create(ctx context.Context, name string, scopes []string) (string, APIKey, error) {
	// Generate 32 random bytes for the key
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		return "", APIKey{}, fmt.Errorf("apikeys: failed to generate key: %w", err)
	}
	plaintext := "sk_" + hex.EncodeToString(keyBytes)

	// Hash the plaintext key
	hashBytes := sha256.Sum256([]byte(plaintext))
	keyHash := hex.EncodeToString(hashBytes[:])

	// Prefix is first 8 chars
	prefix := plaintext[:8]

	// Generate ID from 8 random bytes
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return "", APIKey{}, fmt.Errorf("apikeys: failed to generate id: %w", err)
	}
	id := hex.EncodeToString(idBytes)

	now := time.Now().UTC()
	scopesStr := strings.Join(scopes, ",")

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO api_keys (id, name, key_hash, prefix, scopes, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, name, keyHash, prefix, scopesStr,
		now.Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		return "", APIKey{}, fmt.Errorf("apikeys: failed to insert key: %w", err)
	}

	key := APIKey{
		ID:        id,
		Name:      name,
		Prefix:    prefix,
		Scopes:    scopes,
		CreatedAt: now,
	}
	return plaintext, key, nil
}

// Validate checks a plaintext key and returns the stored record.
func (s *SQLiteStore) Validate(ctx context.Context, plaintext string) (APIKey, error) {
	hashBytes := sha256.Sum256([]byte(plaintext))
	keyHash := hex.EncodeToString(hashBytes[:])

	var (
		key          APIKey
		scopesStr    string
		lastUsedTime sql.NullTime
		revokedTime  sql.NullTime
	)

	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, prefix, scopes, created_at, last_used_at, revoked_at
		 FROM api_keys WHERE key_hash = ?`,
		keyHash,
	).Scan(&key.ID, &key.Name, &key.Prefix, &scopesStr, &key.CreatedAt, &lastUsedTime, &revokedTime)
	if err == sql.ErrNoRows {
		return APIKey{}, ErrInvalidKey
	}
	if err != nil {
		return APIKey{}, fmt.Errorf("apikeys: query error: %w", err)
	}

	if revokedTime.Valid {
		return APIKey{}, ErrInvalidKey
	}

	if lastUsedTime.Valid {
		t := lastUsedTime.Time
		key.LastUsedAt = &t
	}

	if scopesStr != "" {
		key.Scopes = strings.Split(scopesStr, ",")
	} else {
		key.Scopes = []string{}
	}

	// Update last_used_at in the background — fire-and-forget
	go func() {
		now := time.Now().UTC().Format("2006-01-02 15:04:05")
		_, _ = s.db.Exec(
			`UPDATE api_keys SET last_used_at = ? WHERE key_hash = ?`,
			now, keyHash,
		)
	}()

	return key, nil
}

// Revoke marks a key as revoked by ID.
func (s *SQLiteStore) Revoke(ctx context.Context, id string) error {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	res, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		now, id,
	)
	if err != nil {
		return fmt.Errorf("apikeys: revoke error: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("apikeys: rows affected: %w", err)
	}
	if n == 0 {
		// Check if the key exists at all
		var count int
		err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_keys WHERE id = ?`, id).Scan(&count)
		if err != nil {
			return fmt.Errorf("apikeys: existence check: %w", err)
		}
		if count == 0 {
			return ErrNotFound
		}
	}
	return nil
}

// List returns all API keys, optionally including revoked ones.
func (s *SQLiteStore) List(ctx context.Context, includeRevoked bool) ([]APIKey, error) {
	query := `SELECT id, name, prefix, scopes, created_at, last_used_at, revoked_at FROM api_keys`
	if !includeRevoked {
		query += ` WHERE revoked_at IS NULL`
	}
	query += ` ORDER BY created_at DESC`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("apikeys: list error: %w", err)
	}
	defer rows.Close()

	var out []APIKey
	for rows.Next() {
		var (
			key          APIKey
			scopesStr    string
			lastUsedTime sql.NullTime
			revokedTime  sql.NullTime
		)
		if err := rows.Scan(&key.ID, &key.Name, &key.Prefix, &scopesStr, &key.CreatedAt, &lastUsedTime, &revokedTime); err != nil {
			return nil, fmt.Errorf("apikeys: scan error: %w", err)
		}

		if lastUsedTime.Valid {
			t := lastUsedTime.Time
			key.LastUsedAt = &t
		}

		if revokedTime.Valid {
			t := revokedTime.Time
			key.RevokedAt = &t
		}

		if scopesStr != "" {
			key.Scopes = strings.Split(scopesStr, ",")
		} else {
			key.Scopes = []string{}
		}

		out = append(out, key)
	}
	return out, rows.Err()
}

// Close closes the DB connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
