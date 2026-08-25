// Package mcpstore holds the MCP servers a WORKSPACE defined for itself.
//
// The operator's servers live in config.yaml and are a template every
// workspace instantiates. That template is the operator's to edit — the
// routes that write it sit behind the platform credential — and it is the
// wrong place for a tenant's own server: a tenant writing into it would be
// editing the deployment, and there is nowhere in a single shared file to say
// "this one is only theirs".
//
// So a tenant's servers live here instead, one row per (workspace, server).
// Before this they lived in the pool's in-memory `overrides` map and were lost
// on the next restart, which is a feature that appears to work until the first
// deploy.
//
// SECRETS ARE NOT IN THIS TABLE. A server's credentials go to the per-workspace
// credential vault, under the same namespace the operator's template servers
// use (mcp.CredentialNamespace), so a tenant sets a credential in exactly one
// place whether the server is theirs or the operator's — and so this file,
// which is plain SQLite on the gateway's disk, never holds one.
package mcpstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/soulacy/soulacy/internal/sqlitex"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// Server is one workspace's own MCP server definition.
//
// It deliberately mirrors the operator's config.yaml shape minus the fields
// that are not a tenant's to set: WorkDir, Limits and SelfPath are stamped by
// the pool from the workspace's confinement, and InheritAll is the documented
// escape hatch that hands third-party code the gateway's whole environment,
// which is the operator's decision and never a tenant's.
type Server struct {
	WorkspaceID        string            `json:"workspace_id"`
	ID                 string            `json:"id"`
	Transport          string            `json:"transport,omitempty"`
	Command            string            `json:"command,omitempty"`
	Args               []string          `json:"args,omitempty"`
	Env                map[string]string `json:"env,omitempty"`
	URL                string            `json:"url,omitempty"`
	Headers            map[string]string `json:"headers,omitempty"`
	InheritEnv         []string          `json:"inherit_env,omitempty"`
	ContainerNetwork   string            `json:"container_network,omitempty"`
	ContainerWorkspace string            `json:"container_workspace,omitempty"`
	Environment        []EnvironmentItem `json:"environment,omitempty"`
	CreatedBy          string            `json:"created_by,omitempty"`
	UpdatedAt          time.Time         `json:"updated_at,omitempty"`
}

// EnvironmentItem is the reviewed, non-secret configuration contract for an
// installed server. Values never live here; secret values remain in the vault
// and ordinary values remain in Env.
type EnvironmentItem struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required"`
	Secret      bool   `json:"secret"`
}

const schema = `
CREATE TABLE IF NOT EXISTS workspace_mcp_servers(
	workspace_id TEXT NOT NULL,
	id           TEXT NOT NULL,
	transport    TEXT NOT NULL DEFAULT '',
	command      TEXT NOT NULL DEFAULT '',
	args         TEXT NOT NULL DEFAULT '[]',
	env          TEXT NOT NULL DEFAULT '{}',
	url          TEXT NOT NULL DEFAULT '',
	headers      TEXT NOT NULL DEFAULT '{}',
	inherit_env  TEXT NOT NULL DEFAULT '[]',
	container_network TEXT NOT NULL DEFAULT 'public',
	container_workspace TEXT NOT NULL DEFAULT 'none',
	environment  TEXT NOT NULL DEFAULT '[]',
	created_by   TEXT NOT NULL DEFAULT '',
	updated_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
	PRIMARY KEY (workspace_id, id)
);`

// Store is the durable per-workspace server registry.
type Store struct{ db *sql.DB }

// Open creates or opens the store.
//
// The primary key is COMPOSITE, (workspace_id, id), not id alone. Server IDs
// are human-chosen slugs and collide across tenants by design — two teams will
// both call theirs "github" — and keying on the id alone would let the last
// writer silently take over the first's definition. The same trap agent IDs
// and plugin IDs have already sprung in this codebase.
func Open(path string) (*Store, error) {
	db, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Existing installations predate container permissions. SQLite has no
	// ADD COLUMN IF NOT EXISTS, so inspect first and migrate without replacing
	// or rebuilding the tenant-scoped table.
	for name, definition := range map[string]string{
		"container_network":   "TEXT NOT NULL DEFAULT 'public'",
		"container_workspace": "TEXT NOT NULL DEFAULT 'none'",
		"environment":         "TEXT NOT NULL DEFAULT '[]'",
	} {
		if err := ensureColumn(db, "workspace_mcp_servers", name, definition); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return &Store{db: db}, nil
}

func ensureColumn(db *sql.DB, table, name, definition string) error {
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid int
		var column, kind string
		var notNull int
		var defaultValue any
		var primaryKey int
		if err := rows.Scan(&cid, &column, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		found = found || column == name
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + name + ` ` + definition)
	return err
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// ErrUnavailable is returned when the store was never opened. Callers treat it
// as "this workspace has no servers of its own", never as "use the shared
// ones".
var ErrUnavailable = errors.New("mcpstore: store is unavailable")

// Put stores or replaces one workspace's server definition.
func (s *Store) Put(ctx context.Context, server Server) error {
	if s == nil || s.db == nil {
		return ErrUnavailable
	}
	workspaceID := wsroot.Normalize(server.WorkspaceID)
	id := strings.TrimSpace(server.ID)
	if id == "" {
		return fmt.Errorf("mcpstore: server id is required")
	}
	args, _ := json.Marshal(nonNilStrings(server.Args))
	env, _ := json.Marshal(nonNilMap(server.Env))
	headers, _ := json.Marshal(nonNilMap(server.Headers))
	inherit, _ := json.Marshal(nonNilStrings(server.InheritEnv))
	environment, _ := json.Marshal(nonNilEnvironment(server.Environment))

	_, err := s.db.ExecContext(ctx, `INSERT INTO workspace_mcp_servers(
		workspace_id, id, transport, command, args, env, url, headers, inherit_env,
		container_network, container_workspace, environment, created_by, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(workspace_id, id) DO UPDATE SET
			transport=excluded.transport, command=excluded.command, args=excluded.args,
			env=excluded.env, url=excluded.url, headers=excluded.headers,
			inherit_env=excluded.inherit_env, container_network=excluded.container_network,
			container_workspace=excluded.container_workspace, environment=excluded.environment, created_by=excluded.created_by,
			updated_at=excluded.updated_at`,
		workspaceID, id, server.Transport, server.Command, string(args), string(env),
		server.URL, string(headers), string(inherit), server.ContainerNetwork,
		server.ContainerWorkspace, string(environment), server.CreatedBy, time.Now().UTC())
	return err
}

// Delete removes one workspace's server. Deleting one it does not own is a
// no-op rather than an error: idempotent, and silent about whether that id
// exists in some other tenant.
func (s *Store) Delete(ctx context.Context, workspaceID, id string) error {
	if s == nil || s.db == nil {
		return ErrUnavailable
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM workspace_mcp_servers WHERE workspace_id = ? AND id = ?`,
		wsroot.Normalize(workspaceID), strings.TrimSpace(id))
	return err
}

// List returns one workspace's servers. The workspace predicate is part of
// every query in this file; there is no unscoped read.
func (s *Store) List(ctx context.Context, workspaceID string) ([]Server, error) {
	if s == nil || s.db == nil {
		return nil, ErrUnavailable
	}
	workspaceID = wsroot.Normalize(workspaceID)
	rows, err := s.db.QueryContext(ctx, `SELECT id, transport, command, args, env, url, headers,
		inherit_env, container_network, container_workspace, environment, created_by, updated_at FROM workspace_mcp_servers
		WHERE workspace_id = ? ORDER BY id`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Server
	for rows.Next() {
		server := Server{WorkspaceID: workspaceID}
		var args, env, headers, inherit, environment string
		if err := rows.Scan(&server.ID, &server.Transport, &server.Command, &args, &env,
			&server.URL, &headers, &inherit, &server.ContainerNetwork,
			&server.ContainerWorkspace, &environment, &server.CreatedBy, &server.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(args), &server.Args)
		_ = json.Unmarshal([]byte(env), &server.Env)
		_ = json.Unmarshal([]byte(headers), &server.Headers)
		_ = json.Unmarshal([]byte(inherit), &server.InheritEnv)
		_ = json.Unmarshal([]byte(environment), &server.Environment)
		out = append(out, server)
	}
	return out, rows.Err()
}

// PurgeWorkspace removes every server a workspace defined. Called by the
// workspace deletion lifecycle, which is why it exists as a named operation
// rather than as a Delete loop somebody has to remember to write.
func (s *Store) PurgeWorkspace(ctx context.Context, workspaceID string) (int, error) {
	if s == nil || s.db == nil {
		return 0, ErrUnavailable
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM workspace_mcp_servers WHERE workspace_id = ?`,
		wsroot.Normalize(workspaceID))
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func nonNilMap(v map[string]string) map[string]string {
	if v == nil {
		return map[string]string{}
	}
	return v
}

func nonNilEnvironment(v []EnvironmentItem) []EnvironmentItem {
	if v == nil {
		return []EnvironmentItem{}
	}
	return v
}
