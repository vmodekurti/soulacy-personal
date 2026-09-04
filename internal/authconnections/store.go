// Package authconnections stores references to authenticated external services.
// Secret browser state and OAuth tokens never live in this store; they are kept
// in the encrypted credential vault under a connection-specific namespace.
package authconnections

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/soulacy/soulacy/internal/sqlitex"
	"github.com/soulacy/soulacy/internal/workspacepurge"
)

const (
	ScopeUser      = "user"
	ScopeWorkspace = "workspace"

	KindBrowser = "browser_session"
	KindOAuth   = "oauth"

	StatusPending = "pending"
	StatusReady   = "ready"
	StatusExpired = "expired"
	StatusRevoked = "revoked"
)

var ErrNotFound = errors.New("authenticated connection not found")

// Connection is deliberately secret-free. HasSecret indicates that encrypted
// material exists without revealing its value, names, cookies, or token shape.
type Connection struct {
	ID              string     `json:"id"`
	WorkspaceID     string     `json:"workspace_id"`
	OwnerSubject    string     `json:"owner_subject,omitempty"`
	Scope           string     `json:"scope"`
	Kind            string     `json:"kind"`
	Name            string     `json:"name"`
	BaseURL         string     `json:"base_url,omitempty"`
	AllowedDomains  []string   `json:"allowed_domains"`
	Status          string     `json:"status"`
	HasSecret       bool       `json:"has_secret"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	LastValidatedAt *time.Time `json:"last_validated_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	AgentIDs        []string   `json:"agent_ids"`
}

type CreateInput struct {
	ID             string
	WorkspaceID    string
	OwnerSubject   string
	Scope          string
	Kind           string
	Name           string
	BaseURL        string
	AllowedDomains []string
	ExpiresAt      *time.Time
}

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS authenticated_connections (
  id TEXT NOT NULL,
  workspace_id TEXT NOT NULL,
  owner_subject TEXT NOT NULL DEFAULT '',
  scope TEXT NOT NULL CHECK(scope IN ('user','workspace')),
  kind TEXT NOT NULL CHECK(kind IN ('browser_session','oauth')),
  name TEXT NOT NULL,
  base_url TEXT NOT NULL DEFAULT '',
  allowed_domains TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'pending',
  has_secret INTEGER NOT NULL DEFAULT 0,
  expires_at TIMESTAMP,
  last_validated_at TIMESTAMP,
  created_at TIMESTAMP NOT NULL,
  updated_at TIMESTAMP NOT NULL,
  PRIMARY KEY (workspace_id, id)
);
CREATE INDEX IF NOT EXISTS authenticated_connections_visible
  ON authenticated_connections(workspace_id, scope, owner_subject, updated_at DESC);
CREATE TABLE IF NOT EXISTS authenticated_connection_grants (
  workspace_id TEXT NOT NULL,
  connection_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  created_at TIMESTAMP NOT NULL,
  PRIMARY KEY (workspace_id, connection_id, agent_id),
  FOREIGN KEY (workspace_id, connection_id)
    REFERENCES authenticated_connections(workspace_id, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS authenticated_connection_grants_agent
  ON authenticated_connection_grants(workspace_id, agent_id);
`

func Open(path string) (*Store, error) {
	db, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		return nil, fmt.Errorf("authenticated connections: open: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("authenticated connections: schema: %w", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("authenticated connections: foreign keys: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Create(ctx context.Context, in CreateInput) (Connection, error) {
	now := time.Now().UTC()
	if strings.TrimSpace(in.ID) == "" {
		in.ID = "conn_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	status := StatusPending
	_, err := s.db.ExecContext(ctx, `INSERT INTO authenticated_connections
      (id,workspace_id,owner_subject,scope,kind,name,base_url,allowed_domains,status,created_at,updated_at,expires_at)
      VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, in.ID, in.WorkspaceID, in.OwnerSubject, in.Scope,
		in.Kind, in.Name, in.BaseURL, strings.Join(in.AllowedDomains, "\n"), status, now, now, in.ExpiresAt)
	if err != nil {
		return Connection{}, fmt.Errorf("authenticated connections: create: %w", err)
	}
	return s.Get(ctx, in.WorkspaceID, in.ID)
}

// ListVisible returns workspace connections plus only the caller's private
// connections. Workspace administrators intentionally do not gain visibility
// into another member's private connection metadata through this API.
func (s *Store) ListVisible(ctx context.Context, workspaceID, subject string) ([]Connection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,workspace_id,owner_subject,scope,kind,name,base_url,
      allowed_domains,status,has_secret,expires_at,last_validated_at,created_at,updated_at
      FROM authenticated_connections WHERE workspace_id=? AND
      (scope='workspace' OR (scope='user' AND owner_subject=?)) ORDER BY updated_at DESC`, workspaceID, subject)
	if err != nil {
		return nil, fmt.Errorf("authenticated connections: list: %w", err)
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		connection, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		connection.AgentIDs, err = s.AgentGrants(ctx, workspaceID, connection.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, connection)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, workspaceID, id string) (Connection, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,workspace_id,owner_subject,scope,kind,name,base_url,
      allowed_domains,status,has_secret,expires_at,last_validated_at,created_at,updated_at
      FROM authenticated_connections WHERE workspace_id=? AND id=?`, workspaceID, id)
	connection, err := scanConnection(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Connection{}, ErrNotFound
	}
	if err != nil {
		return Connection{}, err
	}
	connection.AgentIDs, err = s.AgentGrants(ctx, workspaceID, id)
	return connection, err
}

type scanner interface{ Scan(...any) error }

func scanConnection(row scanner) (Connection, error) {
	var c Connection
	var domains string
	var secret int
	if err := row.Scan(&c.ID, &c.WorkspaceID, &c.OwnerSubject, &c.Scope, &c.Kind, &c.Name,
		&c.BaseURL, &domains, &c.Status, &secret, &c.ExpiresAt, &c.LastValidatedAt,
		&c.CreatedAt, &c.UpdatedAt); err != nil {
		return Connection{}, err
	}
	c.HasSecret = secret != 0
	if domains != "" {
		c.AllowedDomains = strings.Split(domains, "\n")
	} else {
		c.AllowedDomains = []string{}
	}
	c.AgentIDs = []string{}
	return c, nil
}

func (s *Store) MarkSecret(ctx context.Context, workspaceID, id string, expiresAt *time.Time) error {
	status := StatusReady
	if expiresAt != nil && !expiresAt.After(time.Now()) {
		status = StatusExpired
	}
	result, err := s.db.ExecContext(ctx, `UPDATE authenticated_connections SET has_secret=1,status=?,
      expires_at=?,last_validated_at=?,updated_at=? WHERE workspace_id=? AND id=?`, status, expiresAt,
		time.Now().UTC(), time.Now().UTC(), workspaceID, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetStatus(ctx context.Context, workspaceID, id, status string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE authenticated_connections SET status=?,updated_at=?
      WHERE workspace_id=? AND id=?`, status, time.Now().UTC(), workspaceID, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, workspaceID, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM authenticated_connections WHERE workspace_id=? AND id=?`, workspaceID, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// PurgeWorkspace removes all secret-free connection metadata and grants for a
// deleted workspace. Encrypted browser state and OAuth material are erased by
// the credential vault's workspace purger in the same deletion workflow.
func (s *Store) PurgeWorkspace(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return workspacepurge.Removed{}, err
	}
	defer tx.Rollback() //nolint:errcheck
	grants, err := tx.ExecContext(ctx, `DELETE FROM authenticated_connection_grants WHERE workspace_id=?`, workspaceID)
	if err != nil {
		return workspacepurge.Removed{}, err
	}
	connections, err := tx.ExecContext(ctx, `DELETE FROM authenticated_connections WHERE workspace_id=?`, workspaceID)
	if err != nil {
		return workspacepurge.Removed{}, err
	}
	if err := tx.Commit(); err != nil {
		return workspacepurge.Removed{}, err
	}
	grantRows, _ := grants.RowsAffected()
	connectionRows, _ := connections.RowsAffected()
	return workspacepurge.Removed{
		Rows: grantRows + connectionRows,
		Note: "authenticated connection metadata and agent grants",
	}, nil
}

func (s *Store) ReplaceAgentGrants(ctx context.Context, workspaceID, id string, agentIDs []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM authenticated_connections WHERE workspace_id=? AND id=?`, workspaceID, id).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM authenticated_connection_grants WHERE workspace_id=? AND connection_id=?`, workspaceID, id); err != nil {
		return err
	}
	for _, agentID := range uniqueNonEmpty(agentIDs) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO authenticated_connection_grants(workspace_id,connection_id,agent_id,created_at) VALUES(?,?,?,?)`, workspaceID, id, agentID, time.Now().UTC()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) AgentGrants(ctx context.Context, workspaceID, id string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT agent_id FROM authenticated_connection_grants WHERE workspace_id=? AND connection_id=? ORDER BY agent_id`, workspaceID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// SyncAgentSelection changes one agent's grants across the supplied manageable
// connections without disturbing grants for any other agent. Callers must pass
// only records the current principal owns or administrates.
func (s *Store) SyncAgentSelection(ctx context.Context, workspaceID, agentID string, manageableIDs, selectedIDs []string) error {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return errors.New("agent id is required")
	}
	manageable := map[string]bool{}
	for _, id := range uniqueNonEmpty(manageableIDs) {
		manageable[id] = true
	}
	selected := map[string]bool{}
	for _, id := range uniqueNonEmpty(selectedIDs) {
		selected[id] = true
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for id := range manageable {
		if selected[id] {
			if _, err := tx.ExecContext(ctx, `INSERT INTO authenticated_connection_grants
          (workspace_id,connection_id,agent_id,created_at) VALUES(?,?,?,?)
          ON CONFLICT(workspace_id,connection_id,agent_id) DO NOTHING`, workspaceID, id, agentID, time.Now().UTC()); err != nil {
				return err
			}
		} else if _, err := tx.ExecContext(ctx, `DELETE FROM authenticated_connection_grants
        WHERE workspace_id=? AND connection_id=? AND agent_id=?`, workspaceID, id, agentID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func uniqueNonEmpty(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, value := range in {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}
