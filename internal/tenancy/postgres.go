package tenancy

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

const (
	MembershipActive    = "active"
	MembershipSuspended = "suspended"
	MembershipDeleted   = "deleted"
)

// Mutation identifies who changed tenant state. It is required for every
// mutation so audit data cannot be accidentally omitted by a caller.
type Mutation struct {
	ActorSubject string
	RequestID    string
	At           time.Time
}

type Organization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Workspace struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	Name           string `json:"name"`
}

type User struct {
	ID          string `json:"id"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"display_name"`
}

type Identity struct {
	ID              string `json:"id"`
	UserID          string `json:"user_id"`
	Provider        string `json:"provider"`
	ExternalSubject string `json:"external_subject"`
	Status          string `json:"status"`
}

type Invitation struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	WorkspaceID    string    `json:"workspace_id"`
	Email          string    `json:"email"`
	Role           string    `json:"role"`
	Status         string    `json:"status"`
	ExpiresAt      time.Time `json:"expires_at"`
	// Token is returned only when an invitation is created. Persistence keeps
	// only its SHA-256 digest so a database read cannot disclose a live invite.
	Token string `json:"token,omitempty"`
}

type ServiceAccount struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	WorkspaceID    string `json:"workspace_id"`
	Name           string `json:"name"`
	Role           string `json:"role"`
	Status         string `json:"status"`
}

type Credential struct {
	ID               string     `json:"id"`
	UserID           string     `json:"user_id,omitempty"`
	ServiceAccountID string     `json:"service_account_id,omitempty"`
	Scopes           []string   `json:"scopes"`
	Status           string     `json:"status"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
}

type StoredMembership struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	WorkspaceID    string `json:"workspace_id"`
	UserID         string `json:"user_id"`
	Role           string `json:"role"`
	Status         string `json:"status"`
	Email          string `json:"email,omitempty"`
	DisplayName    string `json:"display_name,omitempty"`
}

// PostgresStore is the durable Team/Scale identity catalog and implements the
// request-time membership Resolver used by the gateway.
type PostgresStore struct{ pool *pgxpool.Pool }

// LinkOIDCIdentity resolves or creates a local user from a cryptographically
// verified issuer/subject pair. An unverified email is never persisted or used
// to attach the identity to an existing account.
func (s *PostgresStore) LinkOIDCIdentity(ctx context.Context, provider, externalSubject, email string, emailVerified bool, displayName string) (string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	externalSubject = strings.TrimSpace(externalSubject)
	if provider == "" || externalSubject == "" {
		return "", errors.New("verified provider and subject are required")
	}
	var existing string
	err := s.pool.QueryRow(ctx, `SELECT user_id FROM identities WHERE provider=$1 AND external_subject=$2 AND status='active'`, provider, externalSubject).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// A SELECT FOR UPDATE cannot lock an absent identity row. Take a
	// transaction-scoped advisory lock so concurrent first logins for the same
	// verified provider subject cannot create duplicate local users.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, provider+"\x00"+externalSubject); err != nil {
		return "", err
	}
	if err = tx.QueryRow(ctx, `SELECT user_id FROM identities WHERE provider=$1 AND external_subject=$2 AND status='active' FOR UPDATE`, provider, externalSubject).Scan(&existing); err == nil {
		_ = tx.Commit(ctx)
		return existing, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	userID := ""
	normalized := normalizeEmail(email)
	if emailVerified && normalized != "" {
		err = tx.QueryRow(ctx, `SELECT id FROM users WHERE normalized_email=$1 FOR UPDATE`, normalized).Scan(&userID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
	}
	if userID == "" {
		userID = newID("usr")
		if strings.TrimSpace(displayName) == "" {
			displayName = "Soulacy user"
		}
		var storedEmail any
		if emailVerified && normalized != "" {
			storedEmail = normalized
		}
		if _, err = tx.Exec(ctx, `INSERT INTO users(id,normalized_email,display_name) VALUES($1,$2,$3)`, userID, storedEmail, strings.TrimSpace(displayName)); err != nil {
			return "", err
		}
	}
	identityID := newID("idn")
	if _, err = tx.Exec(ctx, `INSERT INTO identities(id,user_id,provider,external_subject,status) VALUES($1,$2,$3,$4,'active')`, identityID, userID, provider, externalSubject); err != nil {
		return "", err
	}
	// Through insertAudit, like every other mutation in this file.
	//
	// The hand-written INSERT this replaces named columns that do not exist —
	// `before_state`/`after_state` against a table that defines
	// `before_data`/`after_data` — and omitted `id`, which is a NOT NULL
	// primary key with a format CHECK. It could never have succeeded, and it
	// runs INSIDE the linking transaction with its error returned, so
	// first-time OIDC sign-in failed on the audit write. One writer rather
	// than two is the fix that keeps it fixed: a second INSERT against the
	// same table is a second chance to disagree with the schema, and this one
	// disagreed in three ways at once.
	if err = insertAudit(ctx, tx,
		Mutation{ActorSubject: "oidc:" + externalSubject, RequestID: "oidc-login"},
		"identity.link", "identity", identityID,
		nil, Identity{ID: identityID, UserID: userID, Provider: provider, ExternalSubject: externalSubject, Status: "active"},
	); err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return userID, nil
}

func OpenPostgres(ctx context.Context, pool *pgxpool.Pool) (*PostgresStore, error) {
	if pool == nil {
		return nil, errors.New("tenancy postgres pool is required")
	}
	for _, statement := range postgresSchema {
		if _, err := pool.Exec(ctx, statement); err != nil {
			return nil, fmt.Errorf("tenancy schema: %w", err)
		}
	}
	return &PostgresStore{pool: pool}, nil
}

func (s *PostgresStore) CreateOrganization(ctx context.Context, mutation Mutation, name string) (Organization, error) {
	org := Organization{ID: newID("org"), Name: strings.TrimSpace(name)}
	if org.Name == "" {
		return Organization{}, errors.New("organization name is required")
	}
	err := s.mutate(ctx, mutation, "organization.create", "organization", org.ID, nil, org, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO organizations(id, name) VALUES($1, $2)`, org.ID, org.Name)
		return err
	})
	return org, err
}

func (s *PostgresStore) CreateWorkspace(ctx context.Context, mutation Mutation, organizationID, name string) (Workspace, error) {
	ws := Workspace{ID: newID("ws"), OrganizationID: strings.TrimSpace(organizationID), Name: strings.TrimSpace(name)}
	if ws.OrganizationID == "" || ws.Name == "" {
		return Workspace{}, errors.New("organization ID and workspace name are required")
	}
	err := s.mutate(ctx, mutation, "workspace.create", "workspace", ws.ID, nil, ws, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO workspaces(id, organization_id, name) VALUES($1, $2, $3)`, ws.ID, ws.OrganizationID, ws.Name)
		return err
	})
	return ws, err
}

func (s *PostgresStore) CreateUser(ctx context.Context, mutation Mutation, email, displayName string) (User, error) {
	user := User{ID: newID("usr"), Email: normalizeEmail(email), DisplayName: strings.TrimSpace(displayName)}
	if user.DisplayName == "" {
		return User{}, errors.New("display name is required")
	}
	var normalizedEmail any
	if user.Email != "" {
		normalizedEmail = user.Email
	}
	err := s.mutate(ctx, mutation, "user.create", "user", user.ID, nil, user, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO users(id, normalized_email, display_name) VALUES($1, $2, $3)`, user.ID, normalizedEmail, user.DisplayName)
		return err
	})
	return user, err
}

func (s *PostgresStore) CreateIdentity(ctx context.Context, mutation Mutation, userID, provider, externalSubject string) (Identity, error) {
	identity := Identity{ID: newID("idn"), UserID: strings.TrimSpace(userID), Provider: strings.ToLower(strings.TrimSpace(provider)), ExternalSubject: strings.TrimSpace(externalSubject), Status: "active"}
	if identity.UserID == "" || identity.Provider == "" || identity.ExternalSubject == "" {
		return Identity{}, errors.New("user ID, provider, and external subject are required")
	}
	err := s.mutate(ctx, mutation, "identity.create", "identity", identity.ID, nil, identity, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO identities(id,user_id,provider,external_subject,status) VALUES($1,$2,$3,$4,$5)`, identity.ID, identity.UserID, identity.Provider, identity.ExternalSubject, identity.Status)
		return err
	})
	return identity, err
}

func (s *PostgresStore) CreateInvitation(ctx context.Context, mutation Mutation, organizationID, workspaceID, actorRole, email, role string, expiresAt time.Time) (Invitation, error) {
	invitation := Invitation{ID: newID("inv"), OrganizationID: strings.TrimSpace(organizationID), WorkspaceID: strings.TrimSpace(workspaceID), Email: normalizeEmail(email), Role: strings.ToLower(strings.TrimSpace(role)), Status: "pending", ExpiresAt: expiresAt.UTC()}
	if invitation.OrganizationID == "" || invitation.WorkspaceID == "" || invitation.Email == "" || !IsMembershipRole(invitation.Role) || expiresAt.IsZero() || !invitation.ExpiresAt.After(time.Now().UTC()) {
		return Invitation{}, errors.New("organization, workspace, email, role, and expiry are required")
	}
	if !CanAdministerRole(actorRole, invitation.Role) {
		return Invitation{}, ErrRoleEscalation
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return Invitation{}, fmt.Errorf("generate invitation secret: %w", err)
	}
	token := hex.EncodeToString(secret)
	tokenHash := sha256.Sum256([]byte(token))
	// Never include the bearer secret in durable audit data.
	err := s.mutate(ctx, mutation, "invitation.create", "invitation", invitation.ID, nil, invitation, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO invitations(id,organization_id,workspace_id,normalized_email,role,status,expires_at,token_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, invitation.ID, invitation.OrganizationID, invitation.WorkspaceID, invitation.Email, invitation.Role, invitation.Status, invitation.ExpiresAt, tokenHash[:])
		return err
	})
	if err == nil {
		invitation.Token = token
	}
	return invitation, err
}

func (s *PostgresStore) CreateServiceAccount(ctx context.Context, mutation Mutation, organizationID, workspaceID, name, role string) (ServiceAccount, error) {
	account := ServiceAccount{ID: newID("svc"), OrganizationID: strings.TrimSpace(organizationID), WorkspaceID: strings.TrimSpace(workspaceID), Name: strings.TrimSpace(name), Role: strings.ToLower(strings.TrimSpace(role)), Status: MembershipActive}
	if account.OrganizationID == "" || account.WorkspaceID == "" || account.Name == "" || account.Role == "" {
		return ServiceAccount{}, errors.New("organization, workspace, name, and role are required")
	}
	err := s.mutate(ctx, mutation, "service_account.create", "service_account", account.ID, nil, account, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO service_accounts(id,organization_id,workspace_id,name,role,status) VALUES($1,$2,$3,$4,$5,$6)`, account.ID, account.OrganizationID, account.WorkspaceID, account.Name, account.Role, account.Status); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO service_account_workspaces(service_account_id,organization_id,workspace_id,role,status) VALUES($1,$2,$3,$4,$5)`, account.ID, account.OrganizationID, account.WorkspaceID, account.Role, account.Status)
		return err
	})
	return account, err
}

// BindServiceAccountWorkspace grants an existing non-human principal authority
// in another workspace in the same organization. Credential workspace lists
// can only reference bindings created here; a token cannot create a grant.
func (s *PostgresStore) BindServiceAccountWorkspace(ctx context.Context, mutation Mutation, serviceAccountID, organizationID, workspaceID, role string) error {
	serviceAccountID, organizationID, workspaceID, role = strings.TrimSpace(serviceAccountID), strings.TrimSpace(organizationID), strings.TrimSpace(workspaceID), strings.ToLower(strings.TrimSpace(role))
	if serviceAccountID == "" || organizationID == "" || workspaceID == "" || !IsMembershipRole(role) {
		return errors.New("service account, organization, workspace, and role are required")
	}
	after := map[string]string{"service_account_id": serviceAccountID, "organization_id": organizationID, "workspace_id": workspaceID, "role": role, "status": MembershipActive}
	return s.mutate(ctx, mutation, "service_account.workspace.bind", "service_account_binding", serviceAccountID+":"+workspaceID, nil, after, func(tx pgx.Tx) error {
		var accountOrg string
		if err := tx.QueryRow(ctx, `SELECT organization_id FROM service_accounts WHERE id=$1 AND status='active'`, serviceAccountID).Scan(&accountOrg); err != nil {
			return err
		}
		if accountOrg != organizationID {
			return errors.New("service account organization mismatch")
		}
		_, err := tx.Exec(ctx, `INSERT INTO service_account_workspaces(service_account_id,organization_id,workspace_id,role,status) VALUES($1,$2,$3,$4,'active') ON CONFLICT(service_account_id,workspace_id) DO UPDATE SET role=EXCLUDED.role,status='active'`, serviceAccountID, organizationID, workspaceID, role)
		return err
	})
}

func (s *PostgresStore) CreateCredential(ctx context.Context, mutation Mutation, userID, serviceAccountID string, secretHash []byte, scopes []string, expiresAt *time.Time) (Credential, error) {
	credential := Credential{ID: newID("cred"), UserID: strings.TrimSpace(userID), ServiceAccountID: strings.TrimSpace(serviceAccountID), Scopes: append([]string(nil), scopes...), Status: "active", ExpiresAt: expiresAt}
	if (credential.UserID == "") == (credential.ServiceAccountID == "") {
		return Credential{}, errors.New("credential requires exactly one user or service account")
	}
	if len(secretHash) == 0 {
		return Credential{}, errors.New("credential secret hash is required")
	}
	err := s.mutate(ctx, mutation, "credential.create", "credential", credential.ID, nil, credential, func(tx pgx.Tx) error {
		scopesJSON, err := json.Marshal(credential.Scopes)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO credentials(id,user_id,service_account_id,secret_hash,scopes,status,expires_at) VALUES($1,NULLIF($2,''),NULLIF($3,''),$4,$5,$6,$7)`, credential.ID, credential.UserID, credential.ServiceAccountID, secretHash, scopesJSON, credential.Status, credential.ExpiresAt)
		return err
	})
	return credential, err
}

// credentialsForWorkspaceQuery is kept next to the table it reads so the join
// that carries the tenant is visible with the schema rather than three files
// away.
//
// `credentials` has no workspace column, and correctly so: a credential belongs
// to a principal — exactly one user or one service account, enforced by
// CHECK(NUM_NONNULLS(user_id,service_account_id)=1) — and the principal belongs
// to workspaces. Copying a workspace_id onto the row would create a second
// source of truth that could disagree with the membership, and the row would
// win over the membership that was actually revoked. The tenant is therefore
// derived by joining, every time.
//
// Only active memberships and active service-account bindings count. A
// suspended member's credential must stop appearing in their workspace's
// inventory the moment the membership is suspended, not when someone
// remembers to revoke the credential too.
const credentialsForWorkspaceQuery = `
SELECT c.id, COALESCE(c.user_id,''), COALESCE(c.service_account_id,''), c.scopes, c.status, c.expires_at
FROM credentials c
WHERE (c.user_id IS NOT NULL AND EXISTS (
        SELECT 1 FROM memberships m
        WHERE m.user_id = c.user_id AND m.workspace_id = $1 AND m.status = 'active'))
   OR (c.service_account_id IS NOT NULL AND EXISTS (
        SELECT 1 FROM service_account_workspaces saw
        WHERE saw.service_account_id = c.service_account_id AND saw.workspace_id = $1 AND saw.status = 'active'))
ORDER BY c.created_at DESC, c.id`

// CredentialsForWorkspace lists the credentials whose principal belongs to
// workspaceID.
//
// It exists before any caller does, deliberately. `credentials` is currently
// write-only, and the first read someone adds is the one that will decide
// whether this table is tenant-scoped — a plain `SELECT * FROM credentials`
// would compile, look complete, and return every tenant's. Shipping the scoped
// read with the schema means the boundary is inherited rather than invented.
func (s *PostgresStore) CredentialsForWorkspace(ctx context.Context, workspaceID string) ([]Credential, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return nil, errors.New("tenancy: a workspace is required to list credentials")
	}
	rows, err := s.pool.Query(ctx, credentialsForWorkspaceQuery, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Credential
	for rows.Next() {
		var c Credential
		var scopesJSON []byte
		if err := rows.Scan(&c.ID, &c.UserID, &c.ServiceAccountID, &scopesJSON, &c.Status, &c.ExpiresAt); err != nil {
			return nil, err
		}
		if len(scopesJSON) > 0 {
			if err := json.Unmarshal(scopesJSON, &c.Scopes); err != nil {
				return nil, err
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *PostgresStore) CreateMembership(ctx context.Context, mutation Mutation, organizationID, workspaceID, userID, role string) (StoredMembership, error) {
	m := StoredMembership{
		ID: newID("mem"), OrganizationID: strings.TrimSpace(organizationID), WorkspaceID: strings.TrimSpace(workspaceID),
		UserID: strings.TrimSpace(userID), Role: strings.ToLower(strings.TrimSpace(role)), Status: MembershipActive,
	}
	if m.OrganizationID == "" || m.WorkspaceID == "" || m.UserID == "" || !IsMembershipRole(m.Role) {
		return StoredMembership{}, errors.New("organization, workspace, user, and role are required")
	}
	err := s.mutate(ctx, mutation, "membership.create", "membership", m.ID, nil, m, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO memberships(id, organization_id, workspace_id, user_id, role, status)
			VALUES($1, $2, $3, $4, $5, $6)`, m.ID, m.OrganizationID, m.WorkspaceID, m.UserID, m.Role, m.Status)
		return err
	})
	return m, err
}

func (s *PostgresStore) ResolveMembership(ctx context.Context, subject, requestedWorkspaceID string) (Membership, error) {
	subject, requestedWorkspaceID = strings.TrimSpace(subject), strings.TrimSpace(requestedWorkspaceID)
	if subject == "" || requestedWorkspaceID == "" {
		return Membership{}, ErrMembershipNotFound
	}
	// The workspace's lifecycle is read in the SAME query as the membership.
	// A separate lookup is a window: deletion begins between the two reads and
	// the write it was meant to stop lands anyway.
	row := s.pool.QueryRow(ctx, `SELECT m.organization_id, m.workspace_id, m.id, m.user_id, m.role, COALESCE(w.status,'active')
		FROM memberships m
		JOIN workspaces w ON w.id=m.workspace_id
		LEFT JOIN identities i ON i.user_id=m.user_id AND i.status='active'
		WHERE m.workspace_id=$1 AND m.status='active' AND (m.user_id=$2 OR i.external_subject=$2)
		LIMIT 1`, requestedWorkspaceID, subject)
	var m Membership
	if err := row.Scan(&m.OrganizationID, &m.WorkspaceID, &m.MembershipID, &m.UserID, &m.Role, &m.WorkspaceStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The service-account path carries the workspace status too. A
			// machine credential is exactly the caller that keeps writing into
			// a workspace nobody is watching being deleted.
			serviceRow := s.pool.QueryRow(ctx, `SELECT b.organization_id,b.workspace_id,b.service_account_id,b.service_account_id,b.role,COALESCE(w.status,'active') FROM service_account_workspaces b JOIN service_accounts s ON s.id=b.service_account_id JOIN workspaces w ON w.id=b.workspace_id WHERE b.workspace_id=$1 AND b.service_account_id=$2 AND b.status='active' AND s.status='active'`, requestedWorkspaceID, subject)
			if serviceErr := serviceRow.Scan(&m.OrganizationID, &m.WorkspaceID, &m.MembershipID, &m.UserID, &m.Role, &m.WorkspaceStatus); serviceErr != nil {
				if errors.Is(serviceErr, pgx.ErrNoRows) {
					return Membership{}, ErrMembershipNotFound
				}
				return Membership{}, serviceErr
			}
			return m, nil
		}
		return Membership{}, err
	}
	return m, nil
}

func (s *PostgresStore) mutate(ctx context.Context, mutation Mutation, action, resourceType, resourceID string, before, after any, operation func(pgx.Tx) error) error {
	tx, err := s.beginMutation(ctx, mutation)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := operation(tx); err != nil {
		return err
	}
	if err := insertAudit(ctx, tx, mutation, action, resourceType, resourceID, before, after); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) beginMutation(ctx context.Context, mutation Mutation) (pgx.Tx, error) {
	if strings.TrimSpace(mutation.ActorSubject) == "" || strings.TrimSpace(mutation.RequestID) == "" {
		return nil, errors.New("mutation actor and request ID are required")
	}
	if s == nil || s.pool == nil {
		return nil, errors.New("tenancy store is unavailable")
	}
	return s.pool.Begin(ctx)
}

func insertAudit(ctx context.Context, tx pgx.Tx, mutation Mutation, action, resourceType, resourceID string, before, after any) error {
	beforeJSON, err := json.Marshal(before)
	if err != nil {
		return err
	}
	afterJSON, err := json.Marshal(after)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO tenant_mutation_audit(id, actor_subject, request_id, action, resource_type, resource_id, before_data, after_data, created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, newID("aud"), mutation.ActorSubject, mutation.RequestID, action, resourceType, resourceID, beforeJSON, afterJSON, mutationTime(mutation))
	return err
}

func mutationTime(m Mutation) time.Time {
	if m.At.IsZero() {
		return time.Now().UTC()
	}
	return m.At.UTC()
}

func normalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

type rowScanner interface{ Scan(...any) error }

func scanMembership(row rowScanner, target *StoredMembership) error {
	return row.Scan(&target.ID, &target.OrganizationID, &target.WorkspaceID, &target.UserID, &target.Role, &target.Status)
}

var postgresSchema = []string{
	`CREATE TABLE IF NOT EXISTS organizations(
		id TEXT PRIMARY KEY CHECK (id ~ '^org_[a-f0-9]{32}$'), name TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`,
	`CREATE TABLE IF NOT EXISTS workspaces(
		id TEXT NOT NULL CHECK (id ~ '^ws_[a-f0-9]{32}$'), organization_id TEXT NOT NULL REFERENCES organizations(id),
		name TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), PRIMARY KEY(id), UNIQUE(id, organization_id))`,
	// MU-032. The workspace lifecycle, added separately so an existing
	// deployment upgrades without a table rewrite.
	//
	// DEFAULT 'active' and a backfill, not just a default: a default applies
	// to new rows only, so every workspace that predates this column would
	// read NULL — and a NULL status compared against 'active' is false, which
	// would take an entire deployment's writes offline on upgrade. The
	// backfill runs unconditionally for the reason MU-012 established: a row
	// with an empty scope value is data that has silently stopped matching,
	// which is harder to notice than a leak.
	`ALTER TABLE workspaces ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'active'`,
	`ALTER TABLE workspaces ADD COLUMN IF NOT EXISTS deletion_requested_at TIMESTAMPTZ`,
	`ALTER TABLE workspaces ADD COLUMN IF NOT EXISTS recoverable_until TIMESTAMPTZ`,
	// The purge lease. Expiring rather than held, so an instance that dies
	// mid-purge does not leave a workspace unpurgeable forever with its
	// customer having been told it was deleted.
	`ALTER TABLE workspaces ADD COLUMN IF NOT EXISTS purge_claimed_by TEXT`,
	`ALTER TABLE workspaces ADD COLUMN IF NOT EXISTS purge_claimed_until TIMESTAMPTZ`,
	`UPDATE workspaces SET status='active' WHERE status IS NULL OR BTRIM(status)=''`,
	`CREATE TABLE IF NOT EXISTS users(
		id TEXT PRIMARY KEY CHECK (id ~ '^usr_[a-f0-9]{32}$'), normalized_email TEXT,
		display_name TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		CONSTRAINT users_email_normalized CHECK (normalized_email IS NULL OR normalized_email=LOWER(BTRIM(normalized_email))))`,
	`CREATE UNIQUE INDEX IF NOT EXISTS users_normalized_email_unique ON users(normalized_email) WHERE normalized_email IS NOT NULL`,
	`CREATE TABLE IF NOT EXISTS identities(
		id TEXT PRIMARY KEY CHECK (id ~ '^idn_[a-f0-9]{32}$'), user_id TEXT NOT NULL REFERENCES users(id),
		provider TEXT NOT NULL CHECK(provider=LOWER(BTRIM(provider))), external_subject TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','disabled')),
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), UNIQUE(provider, external_subject))`,
	`CREATE TABLE IF NOT EXISTS memberships(
		id TEXT PRIMARY KEY CHECK (id ~ '^mem_[a-f0-9]{32}$'), organization_id TEXT NOT NULL, workspace_id TEXT NOT NULL,
		user_id TEXT NOT NULL REFERENCES users(id), role TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','suspended','deleted')),
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		UNIQUE(workspace_id,user_id), FOREIGN KEY(workspace_id,organization_id) REFERENCES workspaces(id,organization_id))`,
	`CREATE TABLE IF NOT EXISTS invitations(
		id TEXT PRIMARY KEY CHECK (id ~ '^inv_[a-f0-9]{32}$'), organization_id TEXT NOT NULL, workspace_id TEXT NOT NULL,
		normalized_email TEXT NOT NULL CHECK(normalized_email=LOWER(BTRIM(normalized_email))), role TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending' CHECK(status IN ('pending','accepted','revoked','expired')),
		expires_at TIMESTAMPTZ NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		FOREIGN KEY(workspace_id,organization_id) REFERENCES workspaces(id,organization_id))`,
	`ALTER TABLE invitations ADD COLUMN IF NOT EXISTS token_hash BYTEA`,
	`ALTER TABLE invitations ADD COLUMN IF NOT EXISTS accepted_by TEXT REFERENCES users(id)`,
	`ALTER TABLE invitations ADD COLUMN IF NOT EXISTS accepted_at TIMESTAMPTZ`,
	`CREATE UNIQUE INDEX IF NOT EXISTS invitations_token_hash_unique ON invitations(token_hash) WHERE token_hash IS NOT NULL`,
	`CREATE UNIQUE INDEX IF NOT EXISTS invitations_pending_unique ON invitations(workspace_id,normalized_email) WHERE status='pending'`,
	`CREATE TABLE IF NOT EXISTS service_accounts(
		id TEXT PRIMARY KEY CHECK (id ~ '^svc_[a-f0-9]{32}$'), organization_id TEXT NOT NULL, workspace_id TEXT NOT NULL,
		name TEXT NOT NULL, role TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','suspended','deleted')),
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), FOREIGN KEY(workspace_id,organization_id) REFERENCES workspaces(id,organization_id))`,
	`CREATE TABLE IF NOT EXISTS service_account_workspaces(
		service_account_id TEXT NOT NULL REFERENCES service_accounts(id), organization_id TEXT NOT NULL, workspace_id TEXT NOT NULL,
		role TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','suspended','deleted')),
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), PRIMARY KEY(service_account_id,workspace_id),
		FOREIGN KEY(workspace_id,organization_id) REFERENCES workspaces(id,organization_id))`,
	`INSERT INTO service_account_workspaces(service_account_id,organization_id,workspace_id,role,status)
		SELECT id,organization_id,workspace_id,role,status FROM service_accounts ON CONFLICT(service_account_id,workspace_id) DO NOTHING`,
	`CREATE TABLE IF NOT EXISTS credentials(
		id TEXT PRIMARY KEY CHECK (id ~ '^cred_[a-f0-9]{32}$'), user_id TEXT REFERENCES users(id),
		service_account_id TEXT REFERENCES service_accounts(id), secret_hash BYTEA NOT NULL, scopes JSONB NOT NULL DEFAULT '[]'::jsonb,
		status TEXT NOT NULL DEFAULT 'active' CHECK(status IN ('active','revoked','expired')), expires_at TIMESTAMPTZ,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(), CHECK(NUM_NONNULLS(user_id,service_account_id)=1))`,
	`CREATE TABLE IF NOT EXISTS tenant_mutation_audit(
		id TEXT PRIMARY KEY CHECK (id ~ '^aud_[a-f0-9]{32}$'), actor_subject TEXT NOT NULL, request_id TEXT NOT NULL,
		action TEXT NOT NULL, resource_type TEXT NOT NULL, resource_id TEXT NOT NULL,
		before_data JSONB NOT NULL, after_data JSONB NOT NULL, created_at TIMESTAMPTZ NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS memberships_lookup_active ON memberships(workspace_id,user_id) WHERE status='active'`,
	`CREATE INDEX IF NOT EXISTS identities_subject_active ON identities(external_subject,user_id) WHERE status='active'`,
}

// ListSubjectWorkspaces returns every active workspace the subject may select,
// covering both human memberships (matched by user ID or by a verified
// external identity subject) and service-account bindings. Suspended
// memberships, suspended service accounts, and inactive identities are
// excluded, so a context switcher never offers a workspace that the next
// request would reject.
func (s *PostgresStore) ListSubjectWorkspaces(ctx context.Context, subject string) ([]SubjectWorkspace, error) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return nil, ErrMembershipNotFound
	}
	rows, err := s.pool.Query(ctx, `
		SELECT m.organization_id, o.name, m.workspace_id, w.name, m.id, m.role, 'user'
		  FROM memberships m
		  JOIN organizations o ON o.id=m.organization_id
		  JOIN workspaces w ON w.id=m.workspace_id
		  LEFT JOIN identities i ON i.user_id=m.user_id AND i.status='active'
		 WHERE m.status='active' AND (m.user_id=$1 OR i.external_subject=$1)
		UNION
		SELECT b.organization_id, o.name, b.workspace_id, w.name, b.service_account_id, b.role, 'service_account'
		  FROM service_account_workspaces b
		  JOIN service_accounts sa ON sa.id=b.service_account_id AND sa.status='active'
		  JOIN organizations o ON o.id=b.organization_id
		  JOIN workspaces w ON w.id=b.workspace_id
		 WHERE b.status='active' AND b.service_account_id=$1
		 ORDER BY 2, 4`, subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SubjectWorkspace{}
	for rows.Next() {
		var w SubjectWorkspace
		if err := rows.Scan(&w.OrganizationID, &w.OrganizationName, &w.WorkspaceID, &w.WorkspaceName, &w.MembershipID, &w.Role, &w.PrincipalKind); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
