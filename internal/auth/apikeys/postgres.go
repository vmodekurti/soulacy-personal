package apikeys

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore is the shared Team/Scale credential catalog. It deliberately
// owns no pool lifecycle because the application shares the tenancy pool.
type PostgresStore struct{ pool *pgxpool.Pool }

const postgresTableSchema = `CREATE TABLE IF NOT EXISTS access_credentials(
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	secret_hash BYTEA NOT NULL UNIQUE,
	prefix TEXT NOT NULL,
	kind TEXT NOT NULL CHECK(kind IN ('personal_access_token','service_account')),
	subject_id TEXT NOT NULL,
	organization_id TEXT NOT NULL,
	workspace_ids JSONB NOT NULL,
	role TEXT NOT NULL,
	scopes JSONB NOT NULL DEFAULT '[]'::jsonb,
	issuer TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','revoked','suspended','deleted')),
	expires_at TIMESTAMPTZ,
	rotated_from_id TEXT,
	created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	last_used_at TIMESTAMPTZ,
	revoked_at TIMESTAMPTZ
)`

var postgresSchema = []string{
	postgresTableSchema,
	`CREATE INDEX IF NOT EXISTS access_credentials_hash ON access_credentials(secret_hash)`,
	`CREATE INDEX IF NOT EXISTS access_credentials_subject ON access_credentials(organization_id,subject_id)`,
}

func NewPostgresStore(ctx context.Context, pool *pgxpool.Pool) (*PostgresStore, error) {
	if pool == nil {
		return nil, errors.New("apikeys: postgres pool is required")
	}
	for _, statement := range postgresSchema {
		if _, err := pool.Exec(ctx, statement); err != nil {
			return nil, fmt.Errorf("apikeys: postgres schema: %w", err)
		}
	}
	return &PostgresStore{pool: pool}, nil
}

func (s *PostgresStore) Close() error { return nil }

func (s *PostgresStore) Create(ctx context.Context, name string, scopes []string) (string, APIKey, error) {
	return "", APIKey{}, fmt.Errorf("%w: Team credentials require explicit tenant bindings", ErrInvalidRequest)
}

func normalizeCreateRequest(req CreateRequest) (CreateRequest, error) {
	req.Name, req.Kind, req.SubjectID = strings.TrimSpace(req.Name), strings.TrimSpace(req.Kind), strings.TrimSpace(req.SubjectID)
	req.OrganizationID, req.Role, req.Issuer = strings.TrimSpace(req.OrganizationID), strings.ToLower(strings.TrimSpace(req.Role)), strings.TrimSpace(req.Issuer)
	req.WorkspaceIDs, req.Scopes = cleanUnique(req.WorkspaceIDs), cleanUnique(req.Scopes)
	if req.Name == "" || (req.Kind != KindPersonal && req.Kind != KindService) || req.SubjectID == "" || req.OrganizationID == "" || len(req.WorkspaceIDs) == 0 || req.Role == "" || req.Issuer == "" {
		return CreateRequest{}, ErrInvalidRequest
	}
	if req.ExpiresAt != nil {
		expires := req.ExpiresAt.UTC()
		if !expires.After(time.Now().UTC()) {
			return CreateRequest{}, ErrInvalidRequest
		}
		req.ExpiresAt = &expires
	}
	return req, nil
}

func postgresMaterial(req CreateRequest, rotatedFrom string) (string, []byte, APIKey, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", nil, APIKey{}, err
	}
	plaintext := "sk_" + hex.EncodeToString(secret)
	digest := sha256.Sum256([]byte(plaintext))
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return "", nil, APIKey{}, err
	}
	now := time.Now().UTC()
	return plaintext, digest[:], APIKey{ID: "cred_" + hex.EncodeToString(idBytes), Name: req.Name, Prefix: plaintext[:8], Kind: req.Kind, SubjectID: req.SubjectID, OrganizationID: req.OrganizationID, WorkspaceIDs: append([]string(nil), req.WorkspaceIDs...), Role: req.Role, Scopes: append([]string(nil), req.Scopes...), Issuer: req.Issuer, Status: StatusActive, ExpiresAt: req.ExpiresAt, RotatedFromID: rotatedFrom, CreatedAt: now}, nil
}

func (s *PostgresStore) CreateScoped(ctx context.Context, input CreateRequest) (string, APIKey, error) {
	req, err := normalizeCreateRequest(input)
	if err != nil {
		return "", APIKey{}, err
	}
	// The subject and every workspace binding must already exist in the tenancy
	// catalog. This prevents a credential record from manufacturing authority.
	for _, workspaceID := range req.WorkspaceIDs {
		var valid bool
		if req.Kind == KindPersonal {
			err = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memberships WHERE user_id=$1 AND organization_id=$2 AND workspace_id=$3 AND status='active')`, req.SubjectID, req.OrganizationID, workspaceID).Scan(&valid)
		} else {
			var actorRole string
			if err = s.pool.QueryRow(ctx, `SELECT role FROM memberships WHERE user_id=$1 AND organization_id=$2 AND workspace_id=$3 AND status='active'`, strings.TrimSpace(req.ActorSubject), req.OrganizationID, workspaceID).Scan(&actorRole); err != nil {
				return "", APIKey{}, ErrInvalidRequest
			}
			if actorRole != "owner" && actorRole != "admin" {
				return "", APIKey{}, ErrInvalidRequest
			}
			err = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM service_account_workspaces b JOIN service_accounts s ON s.id=b.service_account_id WHERE b.service_account_id=$1 AND b.organization_id=$2 AND b.workspace_id=$3 AND b.status='active' AND s.status='active')`, req.SubjectID, req.OrganizationID, workspaceID).Scan(&valid)
		}
		if err != nil {
			return "", APIKey{}, err
		}
		if !valid {
			return "", APIKey{}, ErrInvalidRequest
		}
	}
	plaintext, digest, key, err := postgresMaterial(req, "")
	if err != nil {
		return "", APIKey{}, err
	}
	workspaces, _ := json.Marshal(key.WorkspaceIDs)
	scopes, _ := json.Marshal(key.Scopes)
	_, err = s.pool.Exec(ctx, `INSERT INTO access_credentials(id,name,secret_hash,prefix,kind,subject_id,organization_id,workspace_ids,role,scopes,issuer,status,expires_at,rotated_from_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,NULLIF($14,''),$15)`, key.ID, key.Name, digest, key.Prefix, key.Kind, key.SubjectID, key.OrganizationID, workspaces, key.Role, scopes, key.Issuer, key.Status, key.ExpiresAt, key.RotatedFromID, key.CreatedAt)
	if err != nil {
		return "", APIKey{}, err
	}
	return plaintext, key, nil
}

func scanPostgresKey(row pgx.Row) (APIKey, error) {
	var key APIKey
	var workspaces, scopes []byte
	var lastUsed, revoked, expires *time.Time
	err := row.Scan(&key.ID, &key.Name, &key.Prefix, &key.Kind, &key.SubjectID, &key.OrganizationID, &workspaces, &key.Role, &scopes, &key.Issuer, &key.Status, &expires, &key.RotatedFromID, &key.CreatedAt, &lastUsed, &revoked)
	if err != nil {
		return APIKey{}, err
	}
	_ = json.Unmarshal(workspaces, &key.WorkspaceIDs)
	_ = json.Unmarshal(scopes, &key.Scopes)
	key.ExpiresAt, key.LastUsedAt, key.RevokedAt = expires, lastUsed, revoked
	return key, nil
}

const postgresSelect = `SELECT id,name,prefix,kind,subject_id,organization_id,workspace_ids,role,scopes,issuer,status,expires_at,COALESCE(rotated_from_id,''),created_at,last_used_at,revoked_at FROM access_credentials`

func (s *PostgresStore) Validate(ctx context.Context, plaintext string) (APIKey, error) {
	digest := sha256.Sum256([]byte(plaintext))
	key, err := scanPostgresKey(s.pool.QueryRow(ctx, postgresSelect+` WHERE secret_hash=$1`, digest[:]))
	if errors.Is(err, pgx.ErrNoRows) {
		return APIKey{}, ErrInvalidKey
	}
	if err != nil {
		return APIKey{}, err
	}
	if key.Status != StatusActive || key.RevokedAt != nil || (key.ExpiresAt != nil && !key.ExpiresAt.After(time.Now().UTC())) {
		return APIKey{}, ErrInvalidKey
	}
	var subjectActive bool
	if key.Kind == KindService {
		err = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM service_accounts WHERE id=$1 AND organization_id=$2 AND status='active')`, key.SubjectID, key.OrganizationID).Scan(&subjectActive)
	} else {
		err = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM memberships WHERE user_id=$1 AND organization_id=$2 AND status='active')`, key.SubjectID, key.OrganizationID).Scan(&subjectActive)
	}
	if err != nil || !subjectActive {
		return APIKey{}, ErrInvalidKey
	}
	_, _ = s.pool.Exec(ctx, `UPDATE access_credentials SET last_used_at=NOW() WHERE id=$1`, key.ID)
	return key, nil
}

func (s *PostgresStore) Revoke(ctx context.Context, id string) error {
	return s.SetStatus(ctx, id, StatusRevoked)
}

func (s *PostgresStore) SetStatus(ctx context.Context, id, status string) error {
	status = strings.ToLower(strings.TrimSpace(status))
	if status != StatusActive && status != StatusRevoked && status != StatusSuspended && status != StatusDeleted {
		return ErrInvalidRequest
	}
	id = strings.TrimSpace(id)
	res, err := s.pool.Exec(ctx, `UPDATE access_credentials SET status=$1,revoked_at=CASE WHEN $1='revoked' THEN NOW() ELSE revoked_at END
		WHERE id=$2 AND (status NOT IN ('revoked','deleted') OR status=$1)`, status, id)
	if err != nil {
		return err
	}
	if res.RowsAffected() == 0 {
		var current string
		if err := s.pool.QueryRow(ctx, `SELECT status FROM access_credentials WHERE id=$1`, id).Scan(&current); errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return err
		}
		return ErrInvalidRequest
	}
	return nil
}

func (s *PostgresStore) Rotate(ctx context.Context, id string) (string, APIKey, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", APIKey{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	current, err := scanPostgresKey(tx.QueryRow(ctx, postgresSelect+` WHERE id=$1 FOR UPDATE`, strings.TrimSpace(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return "", APIKey{}, ErrNotFound
	}
	if err != nil {
		return "", APIKey{}, err
	}
	if current.Status != StatusActive || (current.ExpiresAt != nil && !current.ExpiresAt.After(time.Now().UTC())) {
		return "", APIKey{}, ErrInvalidKey
	}
	if _, err = tx.Exec(ctx, `UPDATE access_credentials SET status='revoked',revoked_at=NOW() WHERE id=$1`, current.ID); err != nil {
		return "", APIKey{}, err
	}
	req := CreateRequest{Name: current.Name, Kind: current.Kind, SubjectID: current.SubjectID, OrganizationID: current.OrganizationID, WorkspaceIDs: current.WorkspaceIDs, Role: current.Role, Scopes: current.Scopes, Issuer: current.Issuer, ExpiresAt: current.ExpiresAt}
	plaintext, digest, replacement, err := postgresMaterial(req, current.ID)
	if err != nil {
		return "", APIKey{}, err
	}
	workspaces, _ := json.Marshal(replacement.WorkspaceIDs)
	scopes, _ := json.Marshal(replacement.Scopes)
	_, err = tx.Exec(ctx, `INSERT INTO access_credentials(id,name,secret_hash,prefix,kind,subject_id,organization_id,workspace_ids,role,scopes,issuer,status,expires_at,rotated_from_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, replacement.ID, replacement.Name, digest, replacement.Prefix, replacement.Kind, replacement.SubjectID, replacement.OrganizationID, workspaces, replacement.Role, scopes, replacement.Issuer, replacement.Status, replacement.ExpiresAt, replacement.RotatedFromID, replacement.CreatedAt)
	if err != nil {
		return "", APIKey{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", APIKey{}, err
	}
	return plaintext, replacement, nil
}

// postgresScopedListQuery is factored out so the tenant predicate is asserted
// by test even where no PostgreSQL instance is available. The organization and
// the workspace binding are both required; neither is optional and neither is
// supplied by the client.
func postgresScopedListQuery(includeRevoked bool) string {
	query := postgresSelect + ` WHERE organization_id=$1 AND workspace_ids @> to_jsonb($2::text)`
	if !includeRevoked {
		query += ` AND status='active'`
	}
	return query + ` ORDER BY created_at DESC`
}

// ListForWorkspace restricts the credential listing to one organization and
// one bound workspace inside the SQL predicate, so a management request never
// reads another tenant's rows. The workspace binding is matched against the
// stored JSONB array rather than a client-supplied filter.
func (s *PostgresStore) ListForWorkspace(ctx context.Context, organizationID, workspaceID string, includeRevoked bool) ([]APIKey, error) {
	organizationID, workspaceID = strings.TrimSpace(organizationID), strings.TrimSpace(workspaceID)
	if organizationID == "" || workspaceID == "" {
		return nil, ErrInvalidRequest
	}
	rows, err := s.pool.Query(ctx, postgresScopedListQuery(includeRevoked), organizationID, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		key, scanErr := scanPostgresKey(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

func (s *PostgresStore) List(ctx context.Context, includeRevoked bool) ([]APIKey, error) {
	query := postgresSelect
	if !includeRevoked {
		query += ` WHERE status='active'`
	}
	query += ` ORDER BY created_at DESC`
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		key, scanErr := scanPostgresKey(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, key)
	}
	return out, rows.Err()
}

func (s *PostgresStore) RevokeWorkspace(ctx context.Context, workspaceID string) (int64, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return 0, ErrInvalidRequest
	}
	result, err := s.pool.Exec(ctx, `UPDATE access_credentials
		SET status='revoked', revoked_at=COALESCE(revoked_at,NOW())
		WHERE status NOT IN ('revoked','deleted') AND workspace_ids @> to_jsonb($1::text)`, workspaceID)
	if err != nil {
		return 0, fmt.Errorf("apikeys: revoke workspace credentials: %w", err)
	}
	return result.RowsAffected(), nil
}

func (s *PostgresStore) PurgeWorkspace(ctx context.Context, workspaceID string) (int64, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return 0, ErrInvalidRequest
	}
	if _, err := s.RevokeWorkspace(ctx, workspaceID); err != nil {
		return 0, err
	}
	result, err := s.pool.Exec(ctx, `DELETE FROM access_credentials
		WHERE workspace_ids @> to_jsonb($1::text)`, workspaceID)
	if err != nil {
		return 0, fmt.Errorf("apikeys: purge workspace credential hashes: %w", err)
	}
	return result.RowsAffected(), nil
}
