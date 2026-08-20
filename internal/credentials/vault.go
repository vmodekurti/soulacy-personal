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
	"github.com/soulacy/soulacy/internal/wsroot"
	"io"
	"os"
	"strings"

	_ "github.com/mattn/go-sqlite3"

	"github.com/soulacy/soulacy/internal/sqlitex"
)

// ErrNotFound is returned when a requested credential does not exist.
var ErrNotFound = errors.New("credential not found")

// Vault stores and retrieves encrypted agent credentials.
//
// A secret name is unique within a workspace, not globally: "openai_api_key"
// is a name two tenants will both use, and one of them must not overwrite or
// read the other's.
type Vault interface {
	// Set encrypts value and stores it under (workspaceID, agentID, key).
	Set(ctx context.Context, workspaceID, agentID, key string, value []byte) error
	// Get retrieves and decrypts the value for (workspaceID, agentID, key).
	// Returns ErrNotFound if absent — including when it exists in another
	// workspace, which must be indistinguishable from absent.
	Get(ctx context.Context, workspaceID, agentID, key string) ([]byte, error)
	// Delete removes a credential.
	Delete(ctx context.Context, workspaceID, agentID, key string) error
	// List returns all keys for one workspace's agent.
	List(ctx context.Context, workspaceID, agentID string) ([]string, error)
	// WriteBlob is used by Python tools via env helper for mutable state
	// (e.g. cookie jars). Delegates to Set.
	WriteBlob(ctx context.Context, workspaceID, agentID, key string, data []byte) error
	// ReadBlob delegates to Get.
	ReadBlob(ctx context.Context, workspaceID, agentID, key string) ([]byte, error)
	Close() error
}

const credentialSchema = `
CREATE TABLE IF NOT EXISTS credentials (
    workspace_id TEXT NOT NULL DEFAULT 'ws_personal',
    agent_id   TEXT NOT NULL,
    key        TEXT NOT NULL,
    ciphertext BLOB NOT NULL,
    PRIMARY KEY (workspace_id, agent_id, key)
)
`

// SQLiteVault is a Vault backed by a SQLite database with AES-256-GCM encryption.
type SQLiteVault struct {
	db  *sql.DB
	kms KMSProvider
	// keys manages the versioned per-workspace data keys (MU-015 part 2).
	// Nil when the KMS cannot wrap — every WRITE then behaves exactly as it
	// did before envelope encryption existed, which is what keeps a deployment
	// with a bespoke KMSProvider working (invariant 7).
	keys *dataKeys
	// versioned records whether the SCHEMA carries key_version, which is a
	// different question from whether this KMS can wrap.
	//
	// The two come apart in the case that matters: a vault written by a build
	// with a wrapping KMS, reopened by one without. Reading the shape from the
	// KMS would make that vault read every enveloped row as legacy and fail
	// with a MAC error — "corruption" — when the data is fine and the KMS is
	// wrong. Reading it from the schema lets the row say which key it needs,
	// so the error can name the real problem.
	versioned bool
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
	if err := migrateVaultWorkspaceSchema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("credentials: secure vault permissions: %w", err)
	}

	vault := &SQLiteVault{db: db, kms: kms}
	// Envelope encryption is opt-in BY CAPABILITY, not by configuration. A KMS
	// that can wrap gets versioned data keys; one that cannot keeps the
	// derived-key path. That is the whole compatibility story: a deployment
	// supplying its own KMSProvider — which cannot have implemented an
	// interface that did not exist — is unaffected rather than broken.
	if wrapper, ok := kms.(KeyWrapper); ok {
		if err := ensureDataKeySchema(db); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("credentials: data key schema: %w", err)
		}
		if err := ensureCredentialKeyVersionColumn(db); err != nil {
			_ = db.Close()
			return nil, err
		}
		vault.keys = &dataKeys{db: db, wrapper: wrapper}
	}
	// Asked of the SCHEMA, after any migration, so a vault that already has
	// the column is read correctly by a build that cannot write it.
	versioned, err := vaultColumnExists(db, "credentials", "key_version")
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	vault.versioned = versioned
	return vault, nil
}

// ensureCredentialKeyVersionColumn adds the version column.
//
// DEFAULT 0 means every existing row is already correctly labelled as
// legacy-encrypted, so the migration adds a column and rewrites nothing. A
// migration that had to touch every row would be one that can fail halfway on
// a vault, which is the one table where a half-migration is unrecoverable.
func ensureCredentialKeyVersionColumn(db *sql.DB) error {
	present, err := vaultColumnExists(db, "credentials", "key_version")
	if err != nil {
		return err
	}
	if present {
		return nil
	}
	if _, err := db.Exec(`ALTER TABLE credentials ADD COLUMN key_version INTEGER NOT NULL DEFAULT 0`); err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			return fmt.Errorf("credentials: add key_version: %w", err)
		}
	}
	return nil
}

// Set encrypts value and upserts it under (agentID, key).
func (v *SQLiteVault) Set(ctx context.Context, workspaceID, agentID, key string, value []byte) error {
	workspaceID = wsroot.Normalize(workspaceID)
	version, ct, err := v.seal(ctx, workspaceID, agentID, key, value)
	if err != nil {
		return err
	}
	if !v.versioned {
		// No column to write. A vault whose schema predates envelope
		// encryption is written exactly as it always was — and `seal` above
		// has already produced legacy ciphertext, because keys is nil
		// whenever the column is absent on a fresh open.
		if _, err := v.db.ExecContext(ctx,
			`INSERT INTO credentials (workspace_id, agent_id, key, ciphertext)
			 VALUES (?, ?, ?, ?)
			 ON CONFLICT(workspace_id, agent_id, key) DO UPDATE SET ciphertext = excluded.ciphertext`,
			workspaceID, agentID, key, ct); err != nil {
			return fmt.Errorf("credentials: set: %w", err)
		}
		return nil
	}
	_, err = v.db.ExecContext(ctx,
		`INSERT INTO credentials (workspace_id, agent_id, key, ciphertext, key_version)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(workspace_id, agent_id, key) DO UPDATE SET
		   ciphertext = excluded.ciphertext, key_version = excluded.key_version`,
		workspaceID, agentID, key, ct, version,
	)
	if err != nil {
		return fmt.Errorf("credentials: set: %w", err)
	}
	return nil
}

// seal encrypts one value and reports which key version protected it.
//
// A write ALWAYS uses the newest data key, which is what makes a rotation
// take effect: nothing re-encrypts existing rows, but every subsequent write
// lands under the new version, so a vault converges without a migration that
// could fail halfway.
func (v *SQLiteVault) seal(ctx context.Context, workspaceID, agentID, key string, value []byte) (int, []byte, error) {
	if v.keys == nil {
		encKey, err := v.kms.DeriveKey(ctx, workspaceID, agentID)
		if err != nil {
			return 0, nil, fmt.Errorf("credentials: derive key: %w", err)
		}
		ct, err := encrypt(encKey, value)
		if err != nil {
			return 0, nil, fmt.Errorf("credentials: encrypt: %w", err)
		}
		return legacyKeyVersion, ct, nil
	}
	version, dek, err := v.keys.active(ctx, workspaceID)
	if err != nil {
		return 0, nil, err
	}
	ct, err := sealWithAAD(dek, value, credentialAAD(workspaceID, agentID, key))
	if err != nil {
		return 0, nil, fmt.Errorf("credentials: encrypt: %w", err)
	}
	return version, ct, nil
}

// Get retrieves and decrypts the value for (agentID, key).
func (v *SQLiteVault) Get(ctx context.Context, workspaceID, agentID, key string) ([]byte, error) {
	workspaceID = wsroot.Normalize(workspaceID)
	var ct []byte
	version := legacyKeyVersion
	err := v.scanCredential(ctx, workspaceID, agentID, key, &ct, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("credentials: get: %w", err)
	}
	return v.open(ctx, workspaceID, agentID, key, version, ct)
}

// scanCredential reads a row, tolerating a vault whose key_version column does
// not exist yet.
//
// A vault opened with a KMS that cannot wrap never gets the column, and a
// deployment can swap between the two — so the read has to work either way
// rather than assuming the writer's shape.
func (v *SQLiteVault) scanCredential(ctx context.Context, workspaceID, agentID, key string, ct *[]byte, version *int) error {
	if !v.versioned {
		return v.db.QueryRowContext(ctx,
			`SELECT ciphertext FROM credentials WHERE workspace_id = ? AND agent_id = ? AND key = ?`,
			workspaceID, agentID, key).Scan(ct)
	}
	return v.db.QueryRowContext(ctx,
		`SELECT ciphertext, key_version FROM credentials WHERE workspace_id = ? AND agent_id = ? AND key = ?`,
		workspaceID, agentID, key).Scan(ct, version)
}

// open decrypts one credential under whichever key its row names.
//
// Version 0 is the derived key, forever. That is not a deprecation with a
// removal date: a vault restored from a backup taken before this change
// contains version-0 rows, and a build that could not read them would turn a
// restore into data loss.
func (v *SQLiteVault) open(ctx context.Context, workspaceID, agentID, key string, version int, ct []byte) ([]byte, error) {
	if version == legacyKeyVersion {
		encKey, err := v.kms.DeriveKey(ctx, workspaceID, agentID)
		if err != nil {
			return nil, fmt.Errorf("credentials: derive key: %w", err)
		}
		plaintext, err := decrypt(encKey, ct)
		if err != nil {
			return nil, fmt.Errorf("credentials: decrypt: %w", err)
		}
		return plaintext, nil
	}
	if v.keys == nil {
		// The row was written under an envelope and this vault has no wrapper.
		// Refusing beats a decryption error somebody reads as corruption: the
		// data is fine and the KMS is wrong.
		return nil, fmt.Errorf("credentials: this credential is envelope-encrypted (key version %d) but the configured KMS cannot unwrap data keys", version)
	}
	_, dek, err := v.keys.at(ctx, workspaceID, version)
	if err != nil {
		return nil, err
	}
	plaintext, err := openWithAAD(dek, ct, credentialAAD(workspaceID, agentID, key))
	if err != nil {
		return nil, fmt.Errorf("credentials: decrypt: %w", err)
	}
	return plaintext, nil
}

// RotateWorkspaceKey retires the workspace's current data key and mints the
// next, returning the new version.
//
// Existing ciphertext is NOT re-encrypted and stays readable under the version
// its row records. That is the property the derived-key design could not have:
// rotation is a key operation rather than a data migration, so it cannot fail
// halfway and leave a vault partly unreadable.
func (v *SQLiteVault) RotateWorkspaceKey(ctx context.Context, workspaceID string) (int, error) {
	if v.keys == nil {
		return 0, fmt.Errorf("credentials: the configured KMS cannot wrap data keys, so there is no key to rotate")
	}
	version, _, err := v.keys.rotate(ctx, workspaceID)
	return version, err
}

// ReencryptWorkspace rewrites every credential in one workspace under the
// current data key, and reports how many moved.
//
// Separate from rotation on purpose. Rotating changes what NEW writes use and
// is instant; re-encrypting closes out the old key and touches every row. An
// operator responding to a suspected key compromise needs both, in that order,
// and needs to be able to run the second one again after it fails partway —
// which it can, because a row already at the current version is rewritten
// harmlessly.
func (v *SQLiteVault) ReencryptWorkspace(ctx context.Context, workspaceID string) (int, error) {
	if v.keys == nil {
		return 0, fmt.Errorf("credentials: the configured KMS cannot wrap data keys")
	}
	workspaceID = wsroot.Normalize(workspaceID)
	rows, err := v.db.QueryContext(ctx,
		`SELECT agent_id, key FROM credentials WHERE workspace_id = ?`, workspaceID)
	if err != nil {
		return 0, err
	}
	type target struct{ agentID, key string }
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.agentID, &t.key); err != nil {
			rows.Close()
			return 0, err
		}
		targets = append(targets, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	moved := 0
	for _, t := range targets {
		// Read through Get so a legacy row is decrypted by the derived key and
		// an enveloped one by its own version — the re-encryption does not
		// need to know which, and cannot get it wrong.
		plaintext, err := v.Get(ctx, workspaceID, t.agentID, t.key)
		if err != nil {
			return moved, fmt.Errorf("credentials: re-encrypt %s/%s: %w", t.agentID, t.key, err)
		}
		if err := v.Set(ctx, workspaceID, t.agentID, t.key, plaintext); err != nil {
			return moved, fmt.Errorf("credentials: re-encrypt %s/%s: %w", t.agentID, t.key, err)
		}
		moved++
	}
	return moved, nil
}

// Delete removes a credential. Returns nil if the credential did not exist.
func (v *SQLiteVault) Delete(ctx context.Context, workspaceID, agentID, key string) error {
	_, err := v.db.ExecContext(ctx,
		`DELETE FROM credentials WHERE workspace_id = ? AND agent_id = ? AND key = ?`,
		wsroot.Normalize(workspaceID), agentID, key,
	)
	if err != nil {
		return fmt.Errorf("credentials: delete: %w", err)
	}
	return nil
}

// List returns all credential keys for agentID.
func (v *SQLiteVault) List(ctx context.Context, workspaceID, agentID string) ([]string, error) {
	rows, err := v.db.QueryContext(ctx,
		`SELECT key FROM credentials WHERE workspace_id = ? AND agent_id = ? ORDER BY key`,
		wsroot.Normalize(workspaceID), agentID,
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
func (v *SQLiteVault) WriteBlob(ctx context.Context, workspaceID, agentID, key string, data []byte) error {
	return v.Set(ctx, workspaceID, agentID, key, data)
}

// ReadBlob delegates to Get.
func (v *SQLiteVault) ReadBlob(ctx context.Context, workspaceID, agentID, key string) ([]byte, error) {
	return v.Get(ctx, workspaceID, agentID, key)
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

// migrateVaultWorkspaceSchema brings a vault created before tenants existed up
// to the current shape.
//
// The primary key changes from (agent_id, key) to (workspace_id, agent_id,
// key), and SQLite cannot alter a primary key in place, so the table is
// rebuilt and its rows copied inside one transaction. Existing rows become
// personal, and they stay decryptable because the personal workspace keeps
// the original key derivation (see deriveInfo).
func migrateVaultWorkspaceSchema(db *sql.DB) error {
	present, err := vaultColumnExists(db, "credentials", "workspace_id")
	if err != nil {
		return err
	}
	if present {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for _, statement := range []string{
		`ALTER TABLE credentials RENAME TO credentials_legacy`,
		`CREATE TABLE credentials (
			workspace_id TEXT NOT NULL DEFAULT 'ws_personal',
			agent_id   TEXT NOT NULL,
			key        TEXT NOT NULL,
			ciphertext BLOB NOT NULL,
			PRIMARY KEY (workspace_id, agent_id, key)
		)`,
		`INSERT INTO credentials (workspace_id, agent_id, key, ciphertext)
		 SELECT 'ws_personal', agent_id, key, ciphertext FROM credentials_legacy`,
		`DROP TABLE credentials_legacy`,
	} {
		if _, err := tx.Exec(statement); err != nil {
			lowered := strings.ToLower(err.Error())
			if strings.Contains(lowered, "already exists") || strings.Contains(lowered, "no such table") {
				continue
			}
			return fmt.Errorf("credentials: workspace migration (%.50s): %w", statement, err)
		}
	}
	return tx.Commit()
}

func vaultColumnExists(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, fmt.Errorf("credentials: inspect %s: %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid              int
			name, columnType string
			notNull, pk      int
			defaultValue     sql.NullString
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}
