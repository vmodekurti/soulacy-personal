package rbac

import (
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/mattn/go-sqlite3"

	"github.com/soulacy/soulacy/internal/sqlitex"
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
    role       TEXT NOT NULL,
    agent_id   TEXT NOT NULL,
    actions    TEXT NOT NULL,
    PRIMARY KEY (role, agent_id)
);
`

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
	return s.CanAccessAgentResource(role, agentID, ResourceAgents, action)
}

func (s *SQLiteStore) CanAccessAgentResource(role, agentID, resource, action string) (bool, error) {
	// 1. Exact match
	if allowed, found, err := s.lookupGrant(role, agentID, action); err != nil {
		return false, err
	} else if found {
		return allowed, nil
	}

	// 2. Wildcard
	if agentID != "*" {
		if allowed, found, err := s.lookupGrant(role, "*", action); err != nil {
			return false, err
		} else if found {
			return allowed, nil
		}
	}

	// 3. Static default policy
	return HasPermission(role, resource, action), nil
}

// lookupGrant returns (allowed, found, error). found is false when no row exists.
func (s *SQLiteStore) lookupGrant(role, agentID, action string) (bool, bool, error) {
	var rawActions string
	err := s.db.QueryRow(
		`SELECT actions FROM rbac_agent_grants WHERE role = ? AND agent_id = ?`,
		role, agentID,
	).Scan(&rawActions)
	if err == sql.ErrNoRows {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("rbac: lookup grant: %w", err)
	}
	for _, a := range strings.Split(rawActions, ",") {
		if strings.TrimSpace(a) == action {
			return true, true, nil
		}
	}
	// Row exists but action not listed → explicitly denied
	return false, true, nil
}

// SetAgentGrant implements Store.
func (s *SQLiteStore) SetAgentGrant(g AgentGrant) error {
	if g.Role == "" || g.AgentID == "" {
		return fmt.Errorf("rbac: role and agent_id are required")
	}
	if !IsKnownRole(g.Role) {
		return fmt.Errorf("rbac: unknown role %q", g.Role)
	}
	_, err := s.db.Exec(
		`INSERT INTO rbac_agent_grants (role, agent_id, actions) VALUES (?, ?, ?)
		 ON CONFLICT(role, agent_id) DO UPDATE SET actions = excluded.actions`,
		g.Role, g.AgentID, strings.Join(g.Actions, ","),
	)
	if err != nil {
		return fmt.Errorf("rbac: set agent grant: %w", err)
	}
	return nil
}

// DeleteAgentGrant implements Store.
func (s *SQLiteStore) DeleteAgentGrant(role, agentID string) error {
	_, err := s.db.Exec(
		`DELETE FROM rbac_agent_grants WHERE role = ? AND agent_id = ?`,
		role, agentID,
	)
	return err
}

// ListAgentGrants implements Store.
func (s *SQLiteStore) ListAgentGrants() ([]AgentGrant, error) {
	return s.listGrants(`SELECT role, agent_id, actions FROM rbac_agent_grants ORDER BY role, agent_id`)
}

// ListAgentGrantsForRole implements Store.
func (s *SQLiteStore) ListAgentGrantsForRole(role string) ([]AgentGrant, error) {
	return s.listGrants(
		`SELECT role, agent_id, actions FROM rbac_agent_grants WHERE role = ? ORDER BY agent_id`,
		role,
	)
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
		if err := rows.Scan(&g.Role, &g.AgentID, &rawActions); err != nil {
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
