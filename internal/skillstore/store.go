package skillstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/soulacy/soulacy/internal/sqlitex"
)

const (
	RequestPending   = "pending"
	RequestInstalled = "installed"
	RequestDenied    = "denied"
)

// InstallRequest is a durable, workspace-scoped request for an administrator
// to review and install third-party skill code.
type InstallRequest struct {
	ID                string     `json:"id"`
	WorkspaceID       string     `json:"workspace_id"`
	SourceURL         string     `json:"source_url"`
	Reason            string     `json:"reason,omitempty"`
	Status            string     `json:"status"`
	RequestedBy       string     `json:"requested_by"`
	RequestedAt       time.Time  `json:"requested_at"`
	DecidedBy         string     `json:"decided_by,omitempty"`
	DecidedAt         *time.Time `json:"decided_at,omitempty"`
	DecisionReason    string     `json:"decision_reason,omitempty"`
	InstalledSkillID  string     `json:"installed_skill_id,omitempty"`
}

const schema = `
CREATE TABLE IF NOT EXISTS workspace_skill_install_requests(
	workspace_id       TEXT NOT NULL,
	id                 TEXT NOT NULL,
	source_url         TEXT NOT NULL,
	reason             TEXT NOT NULL DEFAULT '',
	status             TEXT NOT NULL DEFAULT 'pending',
	requested_by       TEXT NOT NULL,
	requested_at       TIMESTAMP NOT NULL,
	decided_by         TEXT NOT NULL DEFAULT '',
	decided_at         TIMESTAMP,
	decision_reason    TEXT NOT NULL DEFAULT '',
	installed_skill_id TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (workspace_id, id)
);
CREATE INDEX IF NOT EXISTS idx_workspace_skill_requests_status
	ON workspace_skill_install_requests(workspace_id, status, requested_at DESC);`

// Store is the durable per-workspace skill install request registry.
type Store struct{ db *sql.DB }

// Open creates or opens the store.
func Open(path string) (*Store, error) {
	db, err := sqlitex.Open(path, sqlitex.DefaultOptions())
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the underlying SQLite database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// CreateInstallRequest inserts a new skill install request.
func (s *Store) CreateInstallRequest(ctx context.Context, req InstallRequest) error {
	if s == nil || s.db == nil {
		return errors.New("skillstore: uninitialized")
	}
	query := `INSERT INTO workspace_skill_install_requests(workspace_id, id, source_url, reason, status, requested_by, requested_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, query, req.WorkspaceID, req.ID, req.SourceURL, req.Reason, req.Status, req.RequestedBy, req.RequestedAt.UTC())
	if err != nil {
		return fmt.Errorf("skillstore: create request: %w", err)
	}
	return nil
}

// ListInstallRequests lists install requests for a workspace, optionally filtered by requester.
func (s *Store) ListInstallRequests(ctx context.Context, workspaceID, requester string) ([]InstallRequest, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("skillstore: uninitialized")
	}
	var query string
	var args []any
	if requester != "" {
		query = `SELECT id, workspace_id, source_url, reason, status, requested_by, requested_at, decided_by, decided_at, decision_reason, installed_skill_id
			FROM workspace_skill_install_requests WHERE workspace_id = ? AND requested_by = ? ORDER BY requested_at DESC`
		args = []any{workspaceID, requester}
	} else {
		query = `SELECT id, workspace_id, source_url, reason, status, requested_by, requested_at, decided_by, decided_at, decision_reason, installed_skill_id
			FROM workspace_skill_install_requests WHERE workspace_id = ? ORDER BY requested_at DESC`
		args = []any{workspaceID}
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("skillstore: list requests: %w", err)
	}
	defer rows.Close()
	var out []InstallRequest
	for rows.Next() {
		var r InstallRequest
		var decidedAt sql.NullTime
		if err := rows.Scan(&r.ID, &r.WorkspaceID, &r.SourceURL, &r.Reason, &r.Status, &r.RequestedBy, &r.RequestedAt, &r.DecidedBy, &decidedAt, &r.DecisionReason, &r.InstalledSkillID); err != nil {
			return nil, fmt.Errorf("skillstore: scan request: %w", err)
		}
		if decidedAt.Valid {
			t := decidedAt.Time.UTC()
			r.DecidedAt = &t
		}
		r.RequestedAt = r.RequestedAt.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// DecideInstallRequest updates the status of a pending install request.
func (s *Store) DecideInstallRequest(ctx context.Context, workspaceID, id, status, decidedBy, reason, installedSkillID string) error {
	if s == nil || s.db == nil {
		return errors.New("skillstore: uninitialized")
	}
	now := time.Now().UTC()
	query := `UPDATE workspace_skill_install_requests SET status = ?, decided_by = ?, decided_at = ?, decision_reason = ?, installed_skill_id = ?
		WHERE workspace_id = ? AND id = ? AND status = 'pending'`
	res, err := s.db.ExecContext(ctx, query, status, decidedBy, now, reason, installedSkillID, workspaceID, id)
	if err != nil {
		return fmt.Errorf("skillstore: decide request: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}
