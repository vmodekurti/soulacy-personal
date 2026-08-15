package apikeys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/soulacy/soulacy/internal/sqlitex"
)

// APIKey is a stored API key record. KeyHash is never returned to callers;
// the Prefix is shown for display purposes (first 8 chars of the key).
type APIKey struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	Prefix         string     `json:"prefix"` // first 8 chars, display only
	Scopes         []string   `json:"scopes"`
	Kind           string     `json:"kind"`
	SubjectID      string     `json:"subject_id"`
	OrganizationID string     `json:"organization_id"`
	WorkspaceIDs   []string   `json:"workspace_ids"`
	Role           string     `json:"role"`
	Issuer         string     `json:"issuer"`
	Status         string     `json:"status"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	RotatedFromID  string     `json:"rotated_from_id,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
}

const (
	KindPersonal    = "personal_access_token"
	KindService     = "service_account"
	StatusActive    = "active"
	StatusRevoked   = "revoked"
	StatusSuspended = "suspended"
	StatusDeleted   = "deleted"
)

// CreateRequest describes the durable authority attached to a credential.
// WorkspaceIDs are explicit rather than inferred at request time, so a token
// copied to another workspace never gains ambient access there.
type CreateRequest struct {
	Name           string     `json:"name"`
	Kind           string     `json:"kind"`
	SubjectID      string     `json:"subject_id"`
	OrganizationID string     `json:"organization_id"`
	WorkspaceIDs   []string   `json:"workspace_ids"`
	Role           string     `json:"role"`
	Scopes         []string   `json:"scopes"`
	Issuer         string     `json:"issuer"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	ActorSubject   string     `json:"-"`
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

// ScopedStore is implemented by stores that support tenant-bound credentials.
// Keeping Store's original Create method preserves compatibility for Personal
// installations and third-party embeddings while Team mode can require this
// stronger contract.
type ScopedStore interface {
	Store
	CreateScoped(context.Context, CreateRequest) (string, APIKey, error)
	Rotate(context.Context, string) (string, APIKey, error)
	SetStatus(context.Context, string, string) error
}

// ScopedLister is implemented by stores that can restrict a credential
// listing to one organization and workspace inside the query itself. The API
// layer prefers it over List so a management call never materializes another
// tenant's credential metadata, even transiently in process memory.
type ScopedLister interface {
	ListForWorkspace(ctx context.Context, organizationID, workspaceID string, includeRevoked bool) ([]APIKey, error)
}

var (
	ErrInvalidKey     = fmt.Errorf("apikeys: invalid or revoked key")
	ErrNotFound       = fmt.Errorf("apikeys: key not found")
	ErrInvalidRequest = fmt.Errorf("apikeys: invalid credential request")
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

var scopedColumns = []string{
	`ALTER TABLE api_keys ADD COLUMN kind TEXT NOT NULL DEFAULT 'personal_access_token'`,
	`ALTER TABLE api_keys ADD COLUMN subject_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE api_keys ADD COLUMN organization_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE api_keys ADD COLUMN workspace_ids TEXT NOT NULL DEFAULT '[]'`,
	`ALTER TABLE api_keys ADD COLUMN role TEXT NOT NULL DEFAULT 'operator'`,
	`ALTER TABLE api_keys ADD COLUMN issuer TEXT NOT NULL DEFAULT 'soulacy'`,
	`ALTER TABLE api_keys ADD COLUMN status TEXT NOT NULL DEFAULT 'active'`,
	`ALTER TABLE api_keys ADD COLUMN expires_at DATETIME`,
	`ALTER TABLE api_keys ADD COLUMN rotated_from_id TEXT NOT NULL DEFAULT ''`,
}

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
	for _, statement := range scopedColumns {
		if _, err := db.Exec(statement); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("apikeys: migrate scoped credentials: %w", err)
		}
	}
	// Preserve credentials created before scoped authority was introduced.
	// Personal mode resolves these well-known legacy aliases to its implicit
	// tenant; newly-created credentials always use the caller's real IDs.
	if _, err := db.Exec(`UPDATE api_keys SET
		subject_id=CASE WHEN subject_id='' THEN 'local-owner' ELSE subject_id END,
		organization_id=CASE WHEN organization_id='' THEN 'org_personal' ELSE organization_id END,
		workspace_ids=CASE WHEN workspace_ids='[]' THEN '["ws_personal"]' ELSE workspace_ids END,
		issuer=CASE WHEN issuer='' THEN 'soulacy-personal' ELSE issuer END,
		status=CASE WHEN revoked_at IS NOT NULL THEN 'revoked' ELSE status END`); err != nil {
		db.Close()
		return nil, fmt.Errorf("apikeys: backfill scoped credentials: %w", err)
	}

	// Schema versioning (E22 adoption): v1 = the idempotent bootstrap above;
	// future changes go through sqlitex.MigrateSchema with v2+.
	if err := sqlitex.RecordSchemaVersion(db, "apikeys", 2); err != nil {
		db.Close()
		return nil, err
	}
	return &SQLiteStore{db: db}, nil
}

// Create generates a new API key with the given name and scopes.
func (s *SQLiteStore) Create(ctx context.Context, name string, scopes []string) (string, APIKey, error) {
	return s.create(ctx, CreateRequest{Name: name, Kind: KindPersonal, SubjectID: "local-owner", OrganizationID: "org_personal", WorkspaceIDs: []string{"ws_personal"}, Role: "operator", Scopes: scopes, Issuer: "soulacy-personal"}, "")
}

// CreateScoped creates a tenant-bound credential and returns its plaintext
// exactly once. Persistence receives only its SHA-256 digest; SHA-256 is
// appropriate here because the input is a uniformly random 256-bit secret.
func (s *SQLiteStore) CreateScoped(ctx context.Context, req CreateRequest) (string, APIKey, error) {
	return s.create(ctx, req, "")
}

func (s *SQLiteStore) create(ctx context.Context, req CreateRequest, rotatedFromID string) (string, APIKey, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.Kind = strings.TrimSpace(req.Kind)
	req.SubjectID = strings.TrimSpace(req.SubjectID)
	req.OrganizationID = strings.TrimSpace(req.OrganizationID)
	req.Role = strings.ToLower(strings.TrimSpace(req.Role))
	req.Issuer = strings.TrimSpace(req.Issuer)
	req.WorkspaceIDs = cleanUnique(req.WorkspaceIDs)
	req.Scopes = cleanUnique(req.Scopes)
	if req.Name == "" || (req.Kind != KindPersonal && req.Kind != KindService) || req.SubjectID == "" || req.OrganizationID == "" || len(req.WorkspaceIDs) == 0 || req.Role == "" || req.Issuer == "" {
		return "", APIKey{}, ErrInvalidRequest
	}
	if req.ExpiresAt != nil {
		expires := req.ExpiresAt.UTC()
		if !expires.After(time.Now().UTC()) {
			return "", APIKey{}, ErrInvalidRequest
		}
		req.ExpiresAt = &expires
	}
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
	workspaceJSON, _ := json.Marshal(req.WorkspaceIDs)
	scopesStr := strings.Join(req.Scopes, ",")

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO api_keys (id, name, key_hash, prefix, scopes, created_at, kind, subject_id, organization_id, workspace_ids, role, issuer, status, expires_at, rotated_from_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, req.Name, keyHash, prefix, scopesStr,
		now.Format("2006-01-02 15:04:05"),
		req.Kind, req.SubjectID, req.OrganizationID, string(workspaceJSON), req.Role, req.Issuer, StatusActive, req.ExpiresAt, rotatedFromID,
	)
	if err != nil {
		return "", APIKey{}, fmt.Errorf("apikeys: failed to insert key: %w", err)
	}

	key := APIKey{
		ID:     id,
		Name:   req.Name,
		Prefix: prefix,
		Scopes: req.Scopes,
		Kind:   req.Kind, SubjectID: req.SubjectID, OrganizationID: req.OrganizationID,
		WorkspaceIDs: req.WorkspaceIDs, Role: req.Role, Issuer: req.Issuer,
		Status: StatusActive, ExpiresAt: req.ExpiresAt, RotatedFromID: rotatedFromID,
		CreatedAt: now,
	}
	return plaintext, key, nil
}

// Validate checks a plaintext key and returns the stored record.
func (s *SQLiteStore) Validate(ctx context.Context, plaintext string) (APIKey, error) {
	hashBytes := sha256.Sum256([]byte(plaintext))
	keyHash := hex.EncodeToString(hashBytes[:])

	var (
		key           APIKey
		scopesStr     string
		lastUsedTime  sql.NullTime
		revokedTime   sql.NullTime
		workspaceJSON string
		expiresTime   sql.NullTime
	)

	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, prefix, scopes, created_at, last_used_at, revoked_at,
		 kind, subject_id, organization_id, workspace_ids, role, issuer, status, expires_at, rotated_from_id
		 FROM api_keys WHERE key_hash = ?`,
		keyHash,
	).Scan(&key.ID, &key.Name, &key.Prefix, &scopesStr, &key.CreatedAt, &lastUsedTime, &revokedTime,
		&key.Kind, &key.SubjectID, &key.OrganizationID, &workspaceJSON, &key.Role, &key.Issuer, &key.Status, &expiresTime, &key.RotatedFromID)
	if err == sql.ErrNoRows {
		return APIKey{}, ErrInvalidKey
	}
	if err != nil {
		return APIKey{}, fmt.Errorf("apikeys: query error: %w", err)
	}

	if revokedTime.Valid || key.Status != StatusActive || (expiresTime.Valid && !expiresTime.Time.After(time.Now().UTC())) {
		return APIKey{}, ErrInvalidKey
	}
	if expiresTime.Valid {
		t := expiresTime.Time.UTC()
		key.ExpiresAt = &t
	}
	_ = json.Unmarshal([]byte(workspaceJSON), &key.WorkspaceIDs)

	if lastUsedTime.Valid {
		t := lastUsedTime.Time
		key.LastUsedAt = &t
	}

	if scopesStr != "" {
		key.Scopes = strings.Split(scopesStr, ",")
	} else {
		key.Scopes = []string{}
	}

	// Keep validation immediately consistent. This also avoids detached writes
	// outliving a short-lived test or embedded store.
	_, _ = s.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at = ? WHERE key_hash = ?`, time.Now().UTC(), keyHash)

	return key, nil
}

// Rotate atomically revokes the current key and creates a replacement with
// identical authority. Existing requests are rejected as soon as the
// transaction commits; no process restart or cache invalidation is required.
func (s *SQLiteStore) Rotate(ctx context.Context, id string) (string, APIKey, error) {
	current, err := s.byID(ctx, strings.TrimSpace(id))
	if err != nil {
		return "", APIKey{}, err
	}
	if current.Status != StatusActive || (current.ExpiresAt != nil && !current.ExpiresAt.After(time.Now().UTC())) {
		return "", APIKey{}, ErrInvalidKey
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", APIKey{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE api_keys SET status=?, revoked_at=? WHERE id=? AND status=?`, StatusRevoked, time.Now().UTC(), id, StatusActive); err != nil {
		_ = tx.Rollback()
		return "", APIKey{}, err
	}
	// createWithTx is intentionally inlined to ensure there is no window where
	// both credentials remain active.
	plaintext, replacement, err := newCredentialMaterial(CreateRequest{Name: current.Name, Kind: current.Kind, SubjectID: current.SubjectID, OrganizationID: current.OrganizationID, WorkspaceIDs: current.WorkspaceIDs, Role: current.Role, Scopes: current.Scopes, Issuer: current.Issuer, ExpiresAt: current.ExpiresAt}, current.ID)
	if err != nil {
		_ = tx.Rollback()
		return "", APIKey{}, err
	}
	workspaceBytes, _ := json.Marshal(replacement.WorkspaceIDs)
	hash := sha256.Sum256([]byte(plaintext))
	_, err = tx.ExecContext(ctx, `INSERT INTO api_keys(id,name,key_hash,prefix,scopes,created_at,kind,subject_id,organization_id,workspace_ids,role,issuer,status,expires_at,rotated_from_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, replacement.ID, replacement.Name, hex.EncodeToString(hash[:]), replacement.Prefix, strings.Join(replacement.Scopes, ","), replacement.CreatedAt, replacement.Kind, replacement.SubjectID, replacement.OrganizationID, string(workspaceBytes), replacement.Role, replacement.Issuer, replacement.Status, replacement.ExpiresAt, replacement.RotatedFromID)
	if err != nil {
		_ = tx.Rollback()
		return "", APIKey{}, err
	}
	if err = tx.Commit(); err != nil {
		return "", APIKey{}, err
	}
	return plaintext, replacement, nil
}

func newCredentialMaterial(req CreateRequest, rotatedFromID string) (string, APIKey, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", APIKey{}, err
	}
	plaintext := "sk_" + hex.EncodeToString(secret)
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return "", APIKey{}, err
	}
	now := time.Now().UTC()
	return plaintext, APIKey{ID: hex.EncodeToString(idBytes), Name: req.Name, Prefix: plaintext[:8], Scopes: append([]string(nil), req.Scopes...), Kind: req.Kind, SubjectID: req.SubjectID, OrganizationID: req.OrganizationID, WorkspaceIDs: append([]string(nil), req.WorkspaceIDs...), Role: req.Role, Issuer: req.Issuer, Status: StatusActive, ExpiresAt: req.ExpiresAt, RotatedFromID: rotatedFromID, CreatedAt: now}, nil
}

func (s *SQLiteStore) SetStatus(ctx context.Context, id, status string) error {
	status = strings.ToLower(strings.TrimSpace(status))
	if !slices.Contains([]string{StatusActive, StatusRevoked, StatusSuspended, StatusDeleted}, status) {
		return ErrInvalidRequest
	}
	var revoked any
	if status == StatusRevoked {
		revoked = time.Now().UTC()
	}
	id = strings.TrimSpace(id)
	res, err := s.db.ExecContext(ctx, `UPDATE api_keys SET status=?, revoked_at=COALESCE(?,revoked_at)
		WHERE id=? AND (status NOT IN ('revoked','deleted') OR status=?)`, status, revoked, id, status)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var current string
		if err := s.db.QueryRowContext(ctx, `SELECT status FROM api_keys WHERE id=?`, id).Scan(&current); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		return ErrInvalidRequest
	}
	return nil
}

func cleanUnique(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func (s *SQLiteStore) byID(ctx context.Context, id string) (APIKey, error) {
	var key APIKey
	var scopes, workspaces string
	var lastUsed, revoked, expires sql.NullTime
	err := s.db.QueryRowContext(ctx, `SELECT id,name,prefix,scopes,created_at,last_used_at,revoked_at,kind,subject_id,organization_id,workspace_ids,role,issuer,status,expires_at,rotated_from_id FROM api_keys WHERE id=?`, id).Scan(&key.ID, &key.Name, &key.Prefix, &scopes, &key.CreatedAt, &lastUsed, &revoked, &key.Kind, &key.SubjectID, &key.OrganizationID, &workspaces, &key.Role, &key.Issuer, &key.Status, &expires, &key.RotatedFromID)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	if err != nil {
		return APIKey{}, err
	}
	key.Scopes = cleanUnique(strings.Split(scopes, ","))
	_ = json.Unmarshal([]byte(workspaces), &key.WorkspaceIDs)
	if key.WorkspaceIDs == nil {
		key.WorkspaceIDs = []string{}
	}
	if lastUsed.Valid {
		t := lastUsed.Time.UTC()
		key.LastUsedAt = &t
	}
	if revoked.Valid {
		t := revoked.Time.UTC()
		key.RevokedAt = &t
	}
	if expires.Valid {
		t := expires.Time.UTC()
		key.ExpiresAt = &t
	}
	return key, nil
}

// Revoke marks a key as revoked by ID.
func (s *SQLiteStore) Revoke(ctx context.Context, id string) error {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	res, err := s.db.ExecContext(ctx,
		`UPDATE api_keys SET revoked_at = ?, status = ? WHERE id = ? AND status != ?`,
		now, StatusRevoked, id, StatusRevoked,
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
	query := `SELECT id,name,prefix,scopes,created_at,last_used_at,revoked_at,kind,subject_id,organization_id,workspace_ids,role,issuer,status,expires_at,rotated_from_id FROM api_keys`
	if !includeRevoked {
		query += ` WHERE status = 'active'`
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
			key           APIKey
			scopesStr     string
			lastUsedTime  sql.NullTime
			revokedTime   sql.NullTime
			workspaceJSON string
			expiresTime   sql.NullTime
		)
		if err := rows.Scan(&key.ID, &key.Name, &key.Prefix, &scopesStr, &key.CreatedAt, &lastUsedTime, &revokedTime, &key.Kind, &key.SubjectID, &key.OrganizationID, &workspaceJSON, &key.Role, &key.Issuer, &key.Status, &expiresTime, &key.RotatedFromID); err != nil {
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
		if expiresTime.Valid {
			t := expiresTime.Time.UTC()
			key.ExpiresAt = &t
		}
		_ = json.Unmarshal([]byte(workspaceJSON), &key.WorkspaceIDs)
		if key.WorkspaceIDs == nil {
			key.WorkspaceIDs = []string{}
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

// ListForWorkspace returns only the credentials issued inside one
// organization and bound to one workspace. SQLite stores workspace bindings as
// a JSON array, so the organization predicate runs in SQL and the workspace
// binding is filtered after decoding; both are server-controlled and neither
// is derived from client input.
func (s *SQLiteStore) ListForWorkspace(ctx context.Context, organizationID, workspaceID string, includeRevoked bool) ([]APIKey, error) {
	organizationID, workspaceID = strings.TrimSpace(organizationID), strings.TrimSpace(workspaceID)
	if organizationID == "" || workspaceID == "" {
		return nil, ErrInvalidRequest
	}
	all, err := s.List(ctx, includeRevoked)
	if err != nil {
		return nil, err
	}
	out := make([]APIKey, 0, len(all))
	for _, key := range all {
		if strings.TrimSpace(key.OrganizationID) != organizationID {
			continue
		}
		if !slices.Contains(key.WorkspaceIDs, workspaceID) {
			continue
		}
		out = append(out, key)
	}
	return out, nil
}

// Close closes the DB connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}
