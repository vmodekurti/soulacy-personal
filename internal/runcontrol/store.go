// Package runcontrol is the shared cancellation signal for non-durable chat
// runs. The executing gateway still owns the context.CancelFunc; PostgreSQL is
// the cross-replica mailbox that tells that owner to invoke it.
package runcontrol

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/soulacy/soulacy/internal/workspacepurge"
	"github.com/soulacy/soulacy/internal/wsroot"
)

const schema = `
CREATE TABLE IF NOT EXISTS chat_run_controls (
    workspace_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    status TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (workspace_id, run_id)
);
CREATE INDEX IF NOT EXISTS idx_chat_run_controls_expiry ON chat_run_controls(expires_at);
`

type Store struct{ db *sql.DB }

func OpenPostgres(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("pgx", strings.TrimSpace(dsn))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(3)
	if err = db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("runcontrol: ping postgres: %w", err)
	}
	if _, err = db.ExecContext(ctx, schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("runcontrol: schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Register(ctx context.Context, workspaceID, runID string, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO chat_run_controls(workspace_id,run_id,status,updated_at,expires_at)
VALUES($1,$2,'active',$3,$4)
ON CONFLICT(workspace_id,run_id) DO UPDATE SET status='active',updated_at=EXCLUDED.updated_at,expires_at=EXCLUDED.expires_at`,
		wsroot.Normalize(workspaceID), strings.TrimSpace(runID), now, now.Add(ttl))
	return err
}

func (s *Store) RequestCancel(ctx context.Context, workspaceID, runID string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE chat_run_controls SET status='cancelling',updated_at=$3
WHERE workspace_id=$1 AND run_id=$2 AND status='active' AND expires_at>$3`,
		wsroot.Normalize(workspaceID), strings.TrimSpace(runID), time.Now().UTC())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) CancelRequested(ctx context.Context, workspaceID, runID string) (bool, error) {
	var status string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM chat_run_controls WHERE workspace_id=$1 AND run_id=$2 AND expires_at>$3`,
		wsroot.Normalize(workspaceID), strings.TrimSpace(runID), time.Now().UTC()).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return status == "cancelling", err
}

func (s *Store) Done(ctx context.Context, workspaceID, runID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM chat_run_controls WHERE workspace_id=$1 AND run_id=$2`,
		wsroot.Normalize(workspaceID), strings.TrimSpace(runID))
	return err
}

func (s *Store) PurgeWorkspace(ctx context.Context, workspaceID string) (workspacepurge.Removed, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM chat_run_controls WHERE workspace_id=$1`, wsroot.Normalize(workspaceID))
	if err != nil {
		return workspacepurge.Removed{}, err
	}
	n, _ := res.RowsAffected()
	return workspacepurge.Removed{Rows: n, Note: "active chat cancellation controls"}, nil
}
