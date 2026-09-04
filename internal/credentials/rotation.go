package credentials

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const versionSchema = `
CREATE TABLE IF NOT EXISTS credential_versions (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_id   TEXT NOT NULL,
    key        TEXT NOT NULL,
    version    INTEGER NOT NULL,
    ciphertext BLOB NOT NULL,
    created_at DATETIME NOT NULL,
    is_active  INTEGER NOT NULL DEFAULT 0,
    UNIQUE(agent_id, key, version)
);
CREATE INDEX IF NOT EXISTS idx_cv_lookup ON credential_versions(agent_id, key);
`

func ensureVersionSchema(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, versionSchema)
	return err
}

// CredentialVersion describes one historical version of a credential value.
type CredentialVersion struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	IsActive  bool      `json:"is_active"`
}

// VersionedVault extends Vault with credential versioning and rotation.
// SQLiteVault implements this interface; use a type assertion to access it.
type VersionedVault interface {
	Vault

	// Rotate generates a new encryption of the current active value for
	// agentID/key and stores it as the new active version. The previous
	// version is retained for audit. Returns the new version number.
	Rotate(ctx context.Context, agentID, key string) (int, error)

	// ListVersions returns all stored versions for agentID/key, newest first.
	ListVersions(ctx context.Context, agentID, key string) ([]CredentialVersion, error)

	// DeleteVersion removes a specific historical version. Returns
	// ErrNotFound if the version does not exist or is still active.
	DeleteVersion(ctx context.Context, agentID, key string, version int) error
}

// Rotate re-encrypts the current active value for (agentID, key) and stores
// it as the new active version in credential_versions. The previous version
// is retained for audit. Returns the new version number.
func (v *SQLiteVault) Rotate(ctx context.Context, agentID, key string) (int, error) {
	if err := ensureVersionSchema(ctx, v.db); err != nil {
		return 0, fmt.Errorf("credentials: rotate: ensure schema: %w", err)
	}

	// Get current plaintext value.
	plaintext, err := v.Get(ctx, agentID, key)
	if err != nil {
		return 0, fmt.Errorf("credentials: rotate: get current value: %w", err)
	}

	// Re-encrypt to produce a fresh ciphertext.
	encKey, err := v.kms.DeriveKey(ctx, agentID)
	if err != nil {
		return 0, fmt.Errorf("credentials: rotate: derive key: %w", err)
	}
	ct, err := encrypt(encKey, plaintext)
	if err != nil {
		return 0, fmt.Errorf("credentials: rotate: encrypt: %w", err)
	}

	// Determine next version number.
	var maxVersion sql.NullInt64
	err = v.db.QueryRowContext(ctx,
		`SELECT MAX(version) FROM credential_versions WHERE agent_id = ? AND key = ?`,
		agentID, key,
	).Scan(&maxVersion)
	if err != nil {
		return 0, fmt.Errorf("credentials: rotate: query max version: %w", err)
	}
	newVersion := 1
	if maxVersion.Valid {
		newVersion = int(maxVersion.Int64) + 1
	}

	// Mark all previous versions inactive.
	_, err = v.db.ExecContext(ctx,
		`UPDATE credential_versions SET is_active = 0 WHERE agent_id = ? AND key = ?`,
		agentID, key,
	)
	if err != nil {
		return 0, fmt.Errorf("credentials: rotate: deactivate old versions: %w", err)
	}

	// Insert new active version.
	_, err = v.db.ExecContext(ctx,
		`INSERT INTO credential_versions (agent_id, key, version, ciphertext, created_at, is_active)
		 VALUES (?, ?, ?, ?, ?, 1)`,
		agentID, key, newVersion, ct, time.Now().UTC(),
	)
	if err != nil {
		return 0, fmt.Errorf("credentials: rotate: insert version: %w", err)
	}

	// Update the main credentials table with the new ciphertext.
	if err := v.Set(ctx, agentID, key, plaintext); err != nil {
		return 0, fmt.Errorf("credentials: rotate: update main store: %w", err)
	}

	return newVersion, nil
}

// ListVersions returns all stored versions for (agentID, key), newest first.
func (v *SQLiteVault) ListVersions(ctx context.Context, agentID, key string) ([]CredentialVersion, error) {
	if err := ensureVersionSchema(ctx, v.db); err != nil {
		return nil, fmt.Errorf("credentials: list versions: ensure schema: %w", err)
	}

	rows, err := v.db.QueryContext(ctx,
		`SELECT version, created_at, is_active
		 FROM credential_versions
		 WHERE agent_id = ? AND key = ?
		 ORDER BY version DESC`,
		agentID, key,
	)
	if err != nil {
		return nil, fmt.Errorf("credentials: list versions: query: %w", err)
	}
	defer rows.Close()

	var versions []CredentialVersion
	for rows.Next() {
		var cv CredentialVersion
		var isActive int
		if err := rows.Scan(&cv.Version, &cv.CreatedAt, &isActive); err != nil {
			return nil, fmt.Errorf("credentials: list versions: scan: %w", err)
		}
		cv.IsActive = isActive == 1
		versions = append(versions, cv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("credentials: list versions: rows: %w", err)
	}
	return versions, nil
}

// DeleteVersion removes a specific historical version. Returns ErrNotFound if
// the version does not exist. Returns an error if the version is still active.
func (v *SQLiteVault) DeleteVersion(ctx context.Context, agentID, key string, version int) error {
	if err := ensureVersionSchema(ctx, v.db); err != nil {
		return fmt.Errorf("credentials: delete version: ensure schema: %w", err)
	}

	var isActive int
	err := v.db.QueryRowContext(ctx,
		`SELECT is_active FROM credential_versions WHERE agent_id = ? AND key = ? AND version = ?`,
		agentID, key, version,
	).Scan(&isActive)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("credentials: delete version: query: %w", err)
	}
	if isActive == 1 {
		return fmt.Errorf("cannot delete active version")
	}

	_, err = v.db.ExecContext(ctx,
		`DELETE FROM credential_versions WHERE agent_id = ? AND key = ? AND version = ?`,
		agentID, key, version,
	)
	if err != nil {
		return fmt.Errorf("credentials: delete version: delete: %w", err)
	}
	return nil
}
