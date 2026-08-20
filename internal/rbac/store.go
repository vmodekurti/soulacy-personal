package rbac

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/mattn/go-sqlite3"

	"github.com/soulacy/soulacy/internal/sqlitex"
	"github.com/soulacy/soulacy/internal/workspacepurge"
	"github.com/soulacy/soulacy/internal/wsroot"
)

// Store persists per-agent grants and provides permission queries.
// The static default policy in rbac.go never touches the store — it is
// evaluated purely in memory. The store is only consulted for per-agent
// overrides.
type Store interface {
	// CanAccessAgent returns true if role is allowed to perform action on
	// agentID. Lookup order:
	//  1. Exact (role, agentID) row.
	//  2. Wildcard (role, "*") row.
	//  3. Static default policy (HasPermission).
	// Returns (false, nil) when access is denied; error only on DB failure.
	CanAccessAgent(role, agentID, action string) (bool, error)

	// SetAgentGrant upserts a grant row.
	SetAgentGrant(grant AgentGrant) error

	// DeleteAgentGrant removes the grant for (role, agentID). A wildcard
	// agentID ("*") removes the blanket grant for that role.
	DeleteAgentGrant(role, agentID string) error

	// ListAgentGrants returns all stored per-agent grants.
	ListAgentGrants() ([]AgentGrant, error)

	// ListAgentGrantsForRole returns grants for a specific role.
	ListAgentGrantsForRole(role string) ([]AgentGrant, error)

	Close() error
}

// resourceAgentStore is implemented by stores that can preserve per-agent
// overrides while using the route's real resource for the static fallback.
// It is intentionally optional so external Store implementations remain
// source-compatible.
type resourceAgentStore interface {
	CanAccessAgentResource(role, agentID, resource, action string) (bool, error)
}

// ---------------------------------------------------------------------------
// SQLite implementation
// ---------------------------------------------------------------------------

const grantSchema = `
CREATE TABLE IF NOT EXISTS rbac_agent_grants (
    workspace_id TEXT NOT NULL DEFAULT 'ws_personal',
    role       TEXT NOT NULL,
    agent_id   TEXT NOT NULL,
    actions    TEXT NOT NULL,
    elevated   INTEGER NOT NULL DEFAULT 0 CHECK(elevated IN (0,1)),
    granted_by_role TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (workspace_id, role, agent_id)
);
`

// personalWorkspace must be the same string every other store uses. It was
// "personal" while the rest of the codebase used wsroot.PersonalWorkspaceID
// ("ws_personal"), which meant a grant written by a request carrying a
// workspace identity landed under one key and a lookup from a claims-only
// request went to the other. A missed grant is not a denial here —
// CanAccessAgentInWorkspace falls through to the static role baseline, which
// is broader — so the divergence silently *widened* access.
const personalWorkspace = wsroot.PersonalWorkspaceID

// SQLiteStore is the default Store backed by a single SQLite file.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore opens (or creates) the RBAC SQLite database at path.
// Uses the same WAL + busy-timeout settings as other Soulacy SQLite stores.
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	db, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		return nil, fmt.Errorf("rbac: open sqlite %s: %w", path, err)
	}
	if _, err := db.Exec(grantSchema); err != nil {
		return nil, fmt.Errorf("rbac: schema: %w", err)
	}
	if err := migrateWorkspaceGrantSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrateLegacyPersonalGrants(db); err != nil {
		db.Close()
		return nil, err
	}

	// Schema versioning (E22 adoption): v1 = the idempotent bootstrap above;
	// future changes go through sqlitex.MigrateSchema with v2+.
	if err := sqlitex.RecordSchemaVersion(db, "rbac", 1); err != nil {
		db.Close()
		return nil, err
	}
	return &SQLiteStore{db: db}, nil
}

// CanAccessAgent implements Store.
func (s *SQLiteStore) CanAccessAgent(role, agentID, action string) (bool, error) {
	return s.CanAccessAgentInWorkspace(personalWorkspace, role, agentID, ResourceAgents, action)
}

func (s *SQLiteStore) CanAccessAgentResource(role, agentID, resource, action string) (bool, error) {
	return s.CanAccessAgentInWorkspace(personalWorkspace, role, agentID, resource, action)
}

// CanAccessAgentInWorkspace evaluates the membership role first, then applies
// a workspace-specific object rule. Ordinary rules are allow-lists that can
// only narrow the role. An elevation is honored only when an owner created it.
func (s *SQLiteStore) CanAccessAgentInWorkspace(workspaceID, role, agentID, resource, action string) (bool, error) {
	baseline := HasPermission(role, resource, action)
	// 1. Exact match
	if allowed, elevated, found, err := s.lookupGrant(workspaceID, role, agentID, action); err != nil {
		return false, err
	} else if found {
		return allowed && (baseline || elevated), nil
	}

	// 2. Wildcard
	if agentID != "*" {
		if allowed, elevated, found, err := s.lookupGrant(workspaceID, role, "*", action); err != nil {
			return false, err
		} else if found {
			return allowed && (baseline || elevated), nil
		}
	}

	// 3. Static default policy
	return baseline, nil
}

// lookupGrant returns (allowed, elevated, found, error).
func (s *SQLiteStore) lookupGrant(workspaceID, role, agentID, action string) (bool, bool, bool, error) {
	var rawActions string
	var elevated bool
	var grantedByRole string
	err := s.db.QueryRow(
		`SELECT actions,elevated,granted_by_role FROM rbac_agent_grants WHERE workspace_id = ? AND role = ? AND agent_id = ?`,
		normalizeWorkspace(workspaceID), role, agentID,
	).Scan(&rawActions, &elevated, &grantedByRole)
	if err == sql.ErrNoRows {
		return false, false, false, nil
	}
	if err != nil {
		return false, false, false, fmt.Errorf("rbac: lookup grant: %w", err)
	}
	elevated = elevated && grantedByRole == RoleOwner
	for _, a := range strings.Split(rawActions, ",") {
		if strings.TrimSpace(a) == action {
			return true, elevated, true, nil
		}
	}
	// Row exists but action not listed → explicitly denied
	return false, elevated, true, nil
}

// SetAgentGrant implements Store.
func (s *SQLiteStore) SetAgentGrant(g AgentGrant) error {
	return s.SetAgentGrantInWorkspace(g)
}

func (s *SQLiteStore) SetAgentGrantInWorkspace(g AgentGrant) error {
	if g.Role == "" || g.AgentID == "" {
		return fmt.Errorf("rbac: role and agent_id are required")
	}
	if !IsKnownRole(g.Role) {
		return fmt.Errorf("rbac: unknown role %q", g.Role)
	}
	if g.Elevated && g.GrantedByRole != RoleOwner {
		return fmt.Errorf("rbac: only a workspace owner may create an elevated object grant")
	}
	g.WorkspaceID = normalizeWorkspace(g.WorkspaceID)
	_, err := s.db.Exec(
		`INSERT INTO rbac_agent_grants (workspace_id,role,agent_id,actions,elevated,granted_by_role) VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(workspace_id,role,agent_id) DO UPDATE SET actions=excluded.actions,elevated=excluded.elevated,granted_by_role=excluded.granted_by_role`,
		g.WorkspaceID, g.Role, g.AgentID, strings.Join(g.Actions, ","), g.Elevated, g.GrantedByRole,
	)
	if err != nil {
		return fmt.Errorf("rbac: set agent grant: %w", err)
	}
	return nil
}

// DeleteAgentGrant implements Store.
func (s *SQLiteStore) DeleteAgentGrant(role, agentID string) error {
	return s.DeleteAgentGrantInWorkspace(personalWorkspace, role, agentID)
}

func (s *SQLiteStore) DeleteAgentGrantInWorkspace(workspaceID, role, agentID string) error {
	_, err := s.db.Exec(
		`DELETE FROM rbac_agent_grants WHERE workspace_id = ? AND role = ? AND agent_id = ?`,
		normalizeWorkspace(workspaceID), role, agentID,
	)
	return err
}

// ListAgentGrants implements Store.
func (s *SQLiteStore) ListAgentGrants() ([]AgentGrant, error) {
	return s.ListAgentGrantsInWorkspace(personalWorkspace)
}

func (s *SQLiteStore) ListAgentGrantsInWorkspace(workspaceID string) ([]AgentGrant, error) {
	return s.listGrants(`SELECT workspace_id,role,agent_id,actions,elevated,granted_by_role FROM rbac_agent_grants WHERE workspace_id=? ORDER BY role,agent_id`, normalizeWorkspace(workspaceID))
}

// ListAgentGrantsForRole implements Store.
func (s *SQLiteStore) ListAgentGrantsForRole(role string) ([]AgentGrant, error) {
	return s.ListAgentGrantsForRoleInWorkspace(personalWorkspace, role)
}

func (s *SQLiteStore) ListAgentGrantsForRoleInWorkspace(workspaceID, role string) ([]AgentGrant, error) {
	return s.listGrants(
		`SELECT workspace_id,role,agent_id,actions,elevated,granted_by_role FROM rbac_agent_grants WHERE workspace_id=? AND role=? ORDER BY agent_id`,
		normalizeWorkspace(workspaceID), role,
	)
}

func (s *SQLiteStore) PurgeWorkspace(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
	return workspacepurge.PurgeCatalogTables(ctx, s.db, "agents", workspaceID)
}

func (s *SQLiteStore) listGrants(query string, args ...any) ([]AgentGrant, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("rbac: list grants: %w", err)
	}
	defer rows.Close()

	var out []AgentGrant
	for rows.Next() {
		var g AgentGrant
		var rawActions string
		if err := rows.Scan(&g.WorkspaceID, &g.Role, &g.AgentID, &rawActions, &g.Elevated, &g.GrantedByRole); err != nil {
			return nil, fmt.Errorf("rbac: scan grant: %w", err)
		}
		for _, a := range strings.Split(rawActions, ",") {
			if t := strings.TrimSpace(a); t != "" {
				g.Actions = append(g.Actions, t)
			}
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func normalizeWorkspace(workspaceID string) string {
	if workspaceID = strings.TrimSpace(workspaceID); workspaceID != "" {
		return workspaceID
	}
	return personalWorkspace
}

func migrateWorkspaceGrantSchema(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(rbac_agent_grants)`)
	if err != nil {
		return fmt.Errorf("rbac: inspect schema: %w", err)
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	rows.Close()
	if columns["workspace_id"] {
		for _, migration := range []struct {
			column string
			sql    string
		}{
			{"elevated", `ALTER TABLE rbac_agent_grants ADD COLUMN elevated INTEGER NOT NULL DEFAULT 0 CHECK(elevated IN (0,1))`},
			{"granted_by_role", `ALTER TABLE rbac_agent_grants ADD COLUMN granted_by_role TEXT NOT NULL DEFAULT ''`},
		} {
			if !columns[migration.column] {
				if _, err := db.Exec(migration.sql); err != nil {
					return fmt.Errorf("rbac: add %s column: %w", migration.column, err)
				}
			}
		}
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`ALTER TABLE rbac_agent_grants RENAME TO rbac_agent_grants_legacy`); err != nil {
		return fmt.Errorf("rbac: rename legacy grants: %w", err)
	}
	if _, err := tx.Exec(grantSchema); err != nil {
		return fmt.Errorf("rbac: create scoped grants: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO rbac_agent_grants(workspace_id,role,agent_id,actions) SELECT 'personal',role,agent_id,actions FROM rbac_agent_grants_legacy`); err != nil {
		return fmt.Errorf("rbac: migrate legacy grants: %w", err)
	}
	if _, err := tx.Exec(`DROP TABLE rbac_agent_grants_legacy`); err != nil {
		return err
	}
	return tx.Commit()
}

// Close closes the underlying database connection.
func (s *SQLiteStore) Close() error { return s.db.Close() }

// ---------------------------------------------------------------------------
// No-op store for open / apikey-only deployments
// ---------------------------------------------------------------------------

// NoopStore satisfies the Store interface but always falls back to the static
// default policy. Use this when RBAC storage is not needed (e.g. single-user
// apikey mode where the static policy is sufficient).
type NoopStore struct{}

func (NoopStore) CanAccessAgent(role, _, action string) (bool, error) {
	return HasPermission(role, ResourceAgents, action), nil
}
func (NoopStore) CanAccessAgentResource(role, _, resource, action string) (bool, error) {
	return HasPermission(role, resource, action), nil
}
func (NoopStore) SetAgentGrant(AgentGrant) error                        { return nil }
func (NoopStore) DeleteAgentGrant(_, _ string) error                    { return nil }
func (NoopStore) ListAgentGrants() ([]AgentGrant, error)                { return nil, nil }
func (NoopStore) ListAgentGrantsForRole(_ string) ([]AgentGrant, error) { return nil, nil }
func (NoopStore) Close() error                                          { return nil }

// ErrorStore fails every object-level authorization decision. It is used by
// multi-user deployments when the grant store cannot be opened: availability
// degradation must never become an authorization bypass.
type ErrorStore struct{ Err error }

func (s ErrorStore) failure() error {
	if s.Err != nil {
		return s.Err
	}
	return fmt.Errorf("rbac: authorization store unavailable")
}
func (s ErrorStore) CanAccessAgent(_, _, _ string) (bool, error) { return false, s.failure() }
func (s ErrorStore) SetAgentGrant(AgentGrant) error              { return s.failure() }
func (s ErrorStore) DeleteAgentGrant(_, _ string) error          { return s.failure() }
func (s ErrorStore) ListAgentGrants() ([]AgentGrant, error)      { return nil, s.failure() }
func (s ErrorStore) ListAgentGrantsForRole(string) ([]AgentGrant, error) {
	return nil, s.failure()
}
func (s ErrorStore) Close() error { return nil }

// migrateLegacyPersonalGrants moves grants written under the old "personal"
// key onto wsroot.PersonalWorkspaceID.
//
// It runs on every open, not only once. A grant left under the old key is not
// merely unreachable: because a missed lookup falls through to the static role
// baseline, the restriction it encodes stops being applied at all. Silently
// widening access is the worst failure this package has, so the sweep is
// unconditional.
//
// A row that would collide with an already-migrated one is dropped rather than
// overwriting it: the row under the current key is the one the live code path
// has been reading and writing, so it is the authoritative one.
func migrateLegacyPersonalGrants(db *sql.DB) error {
	if _, err := db.Exec(
		`UPDATE OR IGNORE rbac_agent_grants SET workspace_id = ? WHERE workspace_id = 'personal'`,
		wsroot.PersonalWorkspaceID,
	); err != nil {
		return fmt.Errorf("rbac: migrate legacy personal grants: %w", err)
	}
	if _, err := db.Exec(`DELETE FROM rbac_agent_grants WHERE workspace_id = 'personal'`); err != nil {
		return fmt.Errorf("rbac: drop legacy personal grants: %w", err)
	}
	return nil
}
