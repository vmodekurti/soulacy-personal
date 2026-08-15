// ownership.go — durable session ownership.
//
// Session authorization used to be answered from a map in the gateway process.
// That map is correct until the moment it is not: a restart forgets who owned
// every open conversation, and a second gateway replica never knew. The
// failure mode is not an error — the replica simply cannot find the session,
// so it reports "not found" for a conversation the user is looking at, or, in
// an admin path that treats an unknown session as unowned, does not report
// anything at all.
//
// Ownership therefore lives in the database, next to the conversation it
// describes.
package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/soulacy/soulacy/internal/sqlitex"
)

// Visibility controls who inside the owning workspace may read a conversation.
const (
	// VisibilityPrivate restricts a conversation to the principal that created
	// it. It is the default because a conversation contains whatever the user
	// typed, and defaulting to sharing is not a decision a storage layer gets
	// to make for them.
	VisibilityPrivate = "private"
	// VisibilityWorkspace opens a conversation to every member of the owning
	// workspace. It never crosses a workspace boundary.
	VisibilityWorkspace = "workspace"
)

// ErrSessionNotFound is returned for a session that does not exist and for one
// the caller may not see. The two are deliberately indistinguishable: telling a
// caller that a session exists but is not theirs turns session IDs into an
// enumeration oracle.
var ErrSessionNotFound = errors.New("session: not found")

// ErrSessionClaimed reports that a session ID is already bound to a different
// principal, agent, or workspace. Callers surface this as not-found for the
// same reason.
var ErrSessionClaimed = errors.New("session: already claimed")

// Ownership is the durable record of who a conversation belongs to.
type Ownership struct {
	SessionID   string    `json:"session_id"`
	WorkspaceID string    `json:"workspace_id"`
	AgentID     string    `json:"agent_id"`
	Creator     string    `json:"creator"`
	Visibility  string    `json:"visibility"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Readable reports whether a principal in a workspace may read this session.
//
// The workspace is checked first and unconditionally: visibility widens access
// inside a workspace and never across one, so a "workspace"-visible
// conversation is not visible to a different tenant.
func (o Ownership) Readable(workspaceID, principal string, admin bool) bool {
	if strings.TrimSpace(workspaceID) == "" || o.WorkspaceID != workspaceID {
		return false
	}
	if o.Visibility == VisibilityWorkspace {
		return true
	}
	if admin {
		return true
	}
	return o.Creator != "" && o.Creator == principal
}

// OwnershipStore persists session ownership so authorization survives a
// restart and is identical on every replica.
type OwnershipStore interface {
	// Claim binds a session to a principal on first use and verifies the
	// binding on every later use. It is the single atomic operation that makes
	// a session ID unclaimable by a second principal.
	Claim(ctx context.Context, want Ownership) (Ownership, error)
	// Lookup returns a session's ownership record.
	Lookup(ctx context.Context, sessionID string) (Ownership, error)
	// SetVisibility changes who inside the workspace may read a conversation.
	// Only the creator may change it.
	SetVisibility(ctx context.Context, sessionID, workspaceID, principal, visibility string) (Ownership, error)
	// ListForPrincipal returns the sessions a principal may read in one
	// workspace, newest first.
	ListForPrincipal(ctx context.Context, workspaceID, principal string, admin bool, limit int) ([]Ownership, error)
	// Delete removes a session's ownership record. Callers must authorize the
	// delete first; this is the storage operation, not the policy.
	Delete(ctx context.Context, sessionID string) error
	Close() error
}

const ownershipSchema = `
CREATE TABLE IF NOT EXISTS session_owners (
    session_id   TEXT NOT NULL,
    workspace_id TEXT NOT NULL DEFAULT 'ws_personal',
    agent_id     TEXT NOT NULL DEFAULT '',
    creator      TEXT NOT NULL DEFAULT '',
    visibility   TEXT NOT NULL DEFAULT 'private' CHECK(visibility IN ('private','workspace')),
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (workspace_id, session_id)
);
CREATE INDEX IF NOT EXISTS idx_session_owners_principal ON session_owners(workspace_id, creator, updated_at);
CREATE INDEX IF NOT EXISTS idx_session_owners_session ON session_owners(session_id);
`

// SQLiteOwnershipStore is the durable ownership store for Personal and
// single-node Team deployments.
type SQLiteOwnershipStore struct{ db *sql.DB }

func NewSQLiteOwnershipStore(path string) (*SQLiteOwnershipStore, error) {
	// Claim reads a row and then inserts based on what it read, so its
	// transactions must take the write lock up front. See Options.ImmediateTx:
	// under a deferred transaction, concurrent first-uses of the same session
	// ID all take read locks and then all try to upgrade, and SQLite fails the
	// losers with "database is locked" instead of letting them wait. The caller
	// would see a 500 where it should have seen ErrSessionClaimed — and, worse,
	// a second request from the *rightful* owner could fail its idempotent
	// re-claim.
	opts := sqlitex.DefaultOptions()
	opts.ImmediateTx = true
	db, err := sqlitex.Open(path, opts)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(ownershipSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("session: ownership schema: %w", err)
	}
	if err := sqlitex.RecordSchemaVersion(db, "session_owners", 1); err != nil {
		db.Close()
		return nil, err
	}
	return &SQLiteOwnershipStore{db: db}, nil
}

func (s *SQLiteOwnershipStore) Close() error { return s.db.Close() }

func normalizeVisibility(visibility string) string {
	switch strings.ToLower(strings.TrimSpace(visibility)) {
	case VisibilityWorkspace:
		return VisibilityWorkspace
	default:
		return VisibilityPrivate
	}
}

// Claim is the whole point of this store: the first caller to present a
// session ID owns it, and a second principal presenting the same ID is
// refused rather than silently joined to the conversation.
//
// The check and the insert happen in one transaction because "look it up, then
// insert if absent" is a race that two concurrent requests win together, and
// the loser's messages would land in the winner's conversation.
func (s *SQLiteOwnershipStore) Claim(ctx context.Context, want Ownership) (Ownership, error) {
	want.SessionID = strings.TrimSpace(want.SessionID)
	want.WorkspaceID = strings.TrimSpace(want.WorkspaceID)
	want.AgentID = strings.TrimSpace(want.AgentID)
	want.Creator = strings.TrimSpace(want.Creator)
	want.Visibility = normalizeVisibility(want.Visibility)
	if want.SessionID == "" || want.WorkspaceID == "" || want.Creator == "" {
		return Ownership{}, fmt.Errorf("session: ownership requires a session, workspace, and creator")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Ownership{}, err
	}
	defer tx.Rollback() //nolint:errcheck

	existing, err := scanOwnership(tx.QueryRowContext(ctx,
		`SELECT session_id, workspace_id, agent_id, creator, visibility, created_at, updated_at
		   FROM session_owners WHERE session_id=?`, want.SessionID))
	switch {
	case err == nil:
		// A session ID is global, not per workspace: two tenants must not be
		// able to occupy the same ID, or one could probe for the other's.
		if existing.WorkspaceID != want.WorkspaceID || existing.Creator != want.Creator ||
			(want.AgentID != "" && existing.AgentID != "" && existing.AgentID != want.AgentID) {
			return Ownership{}, ErrSessionClaimed
		}
		if want.AgentID != "" && existing.AgentID == "" {
			if _, err := tx.ExecContext(ctx, `UPDATE session_owners SET agent_id=?, updated_at=CURRENT_TIMESTAMP WHERE session_id=?`, want.AgentID, want.SessionID); err != nil {
				return Ownership{}, err
			}
			existing.AgentID = want.AgentID
		}
		if err := tx.Commit(); err != nil {
			return Ownership{}, err
		}
		return existing, nil
	case errors.Is(err, sql.ErrNoRows):
	default:
		return Ownership{}, err
	}

	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO session_owners(session_id, workspace_id, agent_id, creator, visibility, created_at, updated_at)
		 VALUES(?,?,?,?,?,?,?)`,
		want.SessionID, want.WorkspaceID, want.AgentID, want.Creator, want.Visibility, now, now); err != nil {
		return Ownership{}, err
	}
	if err := tx.Commit(); err != nil {
		return Ownership{}, err
	}
	want.CreatedAt, want.UpdatedAt = now, now
	return want, nil
}

func (s *SQLiteOwnershipStore) Lookup(ctx context.Context, sessionID string) (Ownership, error) {
	owner, err := scanOwnership(s.db.QueryRowContext(ctx,
		`SELECT session_id, workspace_id, agent_id, creator, visibility, created_at, updated_at
		   FROM session_owners WHERE session_id=?`, strings.TrimSpace(sessionID)))
	if errors.Is(err, sql.ErrNoRows) {
		return Ownership{}, ErrSessionNotFound
	}
	return owner, err
}

// SetVisibility is restricted to the creator. An admin can already read a
// private conversation for support purposes; letting them publish someone
// else's conversation to the workspace is a different and larger power.
func (s *SQLiteOwnershipStore) SetVisibility(ctx context.Context, sessionID, workspaceID, principal, visibility string) (Ownership, error) {
	visibility = normalizeVisibility(visibility)
	result, err := s.db.ExecContext(ctx,
		`UPDATE session_owners SET visibility=?, updated_at=CURRENT_TIMESTAMP
		  WHERE session_id=? AND workspace_id=? AND creator=?`,
		visibility, strings.TrimSpace(sessionID), strings.TrimSpace(workspaceID), strings.TrimSpace(principal))
	if err != nil {
		return Ownership{}, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return Ownership{}, ErrSessionNotFound
	}
	return s.Lookup(ctx, sessionID)
}

func (s *SQLiteOwnershipStore) ListForPrincipal(ctx context.Context, workspaceID, principal string, admin bool, limit int) ([]Ownership, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return nil, ErrSessionNotFound
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	// The workspace predicate is not optional and is not a filter the caller
	// supplies: it is part of every query this store issues.
	query := `SELECT session_id, workspace_id, agent_id, creator, visibility, created_at, updated_at
	            FROM session_owners WHERE workspace_id=?`
	args := []any{workspaceID}
	if !admin {
		query += ` AND (creator=? OR visibility='workspace')`
		args = append(args, strings.TrimSpace(principal))
	}
	query += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Ownership{}
	for rows.Next() {
		var owner Ownership
		if err := rows.Scan(&owner.SessionID, &owner.WorkspaceID, &owner.AgentID, &owner.Creator, &owner.Visibility, &owner.CreatedAt, &owner.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, owner)
	}
	return out, rows.Err()
}

func (s *SQLiteOwnershipStore) Delete(ctx context.Context, sessionID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM session_owners WHERE session_id=?`, strings.TrimSpace(sessionID))
	return err
}

func scanOwnership(row *sql.Row) (Ownership, error) {
	var owner Ownership
	err := row.Scan(&owner.SessionID, &owner.WorkspaceID, &owner.AgentID, &owner.Creator, &owner.Visibility, &owner.CreatedAt, &owner.UpdatedAt)
	return owner, err
}
